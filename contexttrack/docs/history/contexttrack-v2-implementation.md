# How ContextTrack v2 is implemented

ContextTrack records HTTP operations in a patched Go standard library and turns
those observations into typed semantic message labels. A known shared Go
context is evidence of **possible influence from a received message to a sent
message**. It is not a proof of causality, delivery, test coverage, or complete
capture.

This document describes the implemented v2 producer and reader. The normative
record details are in [OUTPUT.md](OUTPUT.md), and the executable schema lives in
`src/contexttrack/events.py`.

## 1. Architecture

```text
selected Go workload built with patched go1.26.6
        |
        v
patched net/http observation owners
  - one context root per server ingress
  - optional explicit client roots
  - one exchange per server request or client attempt
  - explicit API and route metadata
        |
        v
fresh capture directory
  <random-process-id>.jsonl (one file per process)
        |
        v
contexttrack.capture.read_capture
  - strict Pydantic shape validation
  - sequence and relational integrity checks
  - exact metadata/response joins by exchange
        |
        v
resolved immutable MessageLabel occurrences
        |
        +--> groups diagnostic
        |
        +--> shared-context receive x send pairs --> graph diagnostic
```

The producer is embedded in `net/http`; applications do not import a separate
runtime library. The exported annotation helpers are part of the patched
`net/http` package.

### Maintained components

| Path | Responsibility |
| --- | --- |
| `_stdlib/net/http/conftamer.go` | Typed records, context/exchange state, request snapshots, API and route helpers. |
| `_stdlib/net/http/conftamer_log.go` | Configuration, per-process file creation, sequence/write lock, validation, and latched failures. |
| `_stdlib/net/http/conftamer_internal_test.go` | Writer, JSON shape, routing-prefix, and internal helper tests. |
| `_stdlib/net/http/conftamer_test.go` | Black-box HTTP, subprocess, concurrency, configuration, and behavior-preservation tests. |
| `go-inlibrary.patch` | Portable native hook/state changes for the pinned Go tree. It does not duplicate the overlay files. |
| `apply-go-patch.sh` | Clean-tree/version/check-before-apply gate and four-file overlay copy. |
| `prometheus-common-route.patch` | `httprouter` route adapter for `prometheus/common` v0.70.1. |
| `src/contexttrack/events.py` | Strict Pydantic record variants and immutable semantic labels. |
| `src/contexttrack/capture.py` | Strict file reader, cross-record integrity, exact label resolution, and context pairing. |
| `src/contexttrack/inspect.py` | Text/DOT diagnostics and the `contexttrack` command. |
| `analysis/*.py` | Compatibility entry paths that call the same diagnostics; they are not independent readers. |
| `testdata/v2-chain.jsonl` | Auditable five-record synthetic fixture. |
| `event.schema.json` | Generated interoperability schema, not a second source of truth. |

The old heap-address context patch and heuristic v1 reader/graphs have been
retired. Historical v1 captures remain historical and are not converted.

## 2. Three scoped identities

The format deliberately keeps record, exchange, and context identities separate:

| Identity | Meaning |
| --- | --- |
| `(capture_id, process_id, seq)` | One successfully written record in one process file. |
| `(capture_id, process_id, exchange_id)` | One server request or one client transport attempt. |
| `(capture_id, process_id, context_id)` | One known inherited context root used for influence grouping. |

All counters are positive uint64 values. Process IDs are random 128-bit values
rendered as 32 lowercase hexadecimal characters. Equal local counters in
different processes or captures are unrelated. None of these identities is a
semantic graph node, network header, operating-system PID, goroutine ID, or
stable cross-run identifier.

A semantic node is a `MessageLabel`: direction/type, API ID, method, client
authority/path or server route/concrete path, route dialect, and response
status. Equal semantic labels may intentionally deduplicate across occurrences;
runtime IDs never do.

## 3. Configuration and logger behavior

Capture initialization reads:

```text
CONFTAMER_EVENTS_DIR=/absolute/existing/directory
CONFTAMER_CAPTURE_ID=nonempty-capture-name
```

Both absent means disabled. A partial/invalid configuration, or any set legacy
`CONFTAMER_EVENTS`, prints a configuration error and leaves capture disabled.
Initialization does not alter the application's exit status.

Each enabled process:

1. generates 16 random bytes with `crypto/rand`;
2. exclusively creates `<process_id>.jsonl` with mode `0600`;
3. publishes one process-local logger; and
4. prints an enabled diagnostic containing capture, process, and path values.

The logger mutex covers sequence allocation, string validation, JSON marshaling,
and one complete `Write` call. Successful sequences begin at 1 and are
contiguous. Invalid UTF-8 is rejected before `encoding/json` can replace it.
Marshal errors, write errors, and short writes latch the first error, emit one
stderr diagnostic, and atomically stop later hook work.

Logging is synchronous and can add latency. There is no queue, shutdown marker,
reopen/append path, or per-record `fsync`. The consumer can validate a prefix but
cannot prove that the workload finished or that every intended observation was
written.

## 4. Context roots without caller mutation

`ConftamerContext(ctx)` is the explicit root boundary for autonomous client
work. When capture is enabled it returns an already stamped context unchanged or
a derived context with a new local ID. Nil remains nil. When capture is disabled
it returns the original argument exactly.

Callers must use the returned context:

```go
ctx := http.ConftamerContext(context.Background())
req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
```

Server ingress automatically stamps the server-owned request before handler
dispatch. Derived contexts inherit the private value normally. Each independent
server request receives a new root; no connection-wide root is used.

Client observation only reads a root. It does not mutate the caller's request,
replace its context, or allocate a per-log fallback. An unstamped client request
therefore has `context_id: null`. Its request and response still join exactly by
exchange ID, but null roots never group together. Redirects, retries, and
`context.WithoutCancel` retain a known value through normal Go behavior;
replacing a context with `context.Background()` loses it.

## 5. Immutable exchange state and observation owners

A private `conftamerExchange` owns an ID, a known-or-zero context ID, and an
immutable request origin snapshot. Server request copies retain the server
exchange pointer. Client state is allocated per transport attempt and is stored
in `transportRequest` or `http2clientStream`, not in a reusable caller request
or `context.Value`.

| Observation | Authoritative hook in Go 1.26.6 | Meaning |
| --- | --- | --- |
| HTTP/1 receive request | `serverHandler.ServeHTTP` | Attach a server root/exchange once before dispatch. |
| HTTP/2 receive request | `http2serverConn.runHandler` | Attach once; later common dispatch sees existing state. |
| HTTP/1 send request | `persistConn.roundTrip` | Allocate and publish one attempt before protocol workers. |
| HTTP/2 send request | `http2clientStream.encodeAndWriteHeaders` | Emit after successful header encoding, before writing headers. |
| HTTP/1 receive response | `persistConn.readResponse` | Emit one accepted final response after the informational loop. |
| HTTP/2 receive response | `http2clientConnReadLoop.processHeaders` | Emit after successful final response construction, before notification. |
| HTTP/1 send response | `response.WriteHeader` | Emit at the accepted final-header state transition. |
| HTTP/2 send response | `http2responseWriterState.writeHeader` | Emit below native public guards at the accepted final transition. |

The old client-level duplicate hooks are absent. Ordinary informational
responses and trailers are not semantic messages. HTTP/1 status 101 and final
statuses 200 through 999 are representable. Implicit responses, empty handlers,
repeated `WriteHeader`, handler panic, redirect hops, a deterministic retry,
cancellation, trailers, flushing, and body read/close behavior have focused
regressions.

A send records a transport attempt or accepted final headers. It is not proof
that bytes reached a peer or that a body completed. Failed attempts may have no
response, which is valid capture state.

## 6. Request snapshots and explicit API ownership

A request origin records:

- method, with Go's empty-method default represented as `GET`;
- effective HTTP authority: `Request.Host`, otherwise `URL.Host`, otherwise null;
- raw `URL.Path`, including an explicit empty string; and
- an optional reviewed API binding.

`ConftamerWithClientAPI(r, apiID)` returns a shallow request copy carrying an
immutable binding to that exact method/authority/path snapshot. The input is not
mutated. If the target later changes, the binding is ignored. Redirects do not
inherit it automatically; a reviewed `CheckRedirect` callback can explicitly
bind each callback-owned request.

`ConftamerSetServerAPI(r, apiID)` emits API-only metadata for an already traced
server exchange. Repeated identical declarations remain observations and are
accepted by the reader. Conflicting known values fail capture integrity. Server
bindings do not flow to an outbound client request merely because its context
was inherited.

API IDs represent the organization that owns the communication API, not the
calling module. There is no stack walk, handler reflection, organization
shortening, neighbor recovery, configuration wildcard, or default Prometheus
owner. Missing evidence remains null.

Invalid/empty annotation strings stop capture with a diagnostic but do not
change HTTP results. Annotation of an untraced request is a no-op and cannot
invent an exchange.

## 7. Route evidence and rewrites

`ConftamerLogRouted(r, dialect, pattern)` emits route-only metadata for a traced
server exchange. Supported dialects are:

- `go_serve_mux`;
- `go_serve_mux_121`; and
- `httprouter`.

Patterns are preserved exactly. A request copy made by successful native
`StripPrefix` carries its prefix state by value. Full-pattern composition is
accepted only when the original path equals `prefix + matched_path`, the method
and authority are unchanged, and the pattern contains a path slash. Arbitrary
rewrites, method changes, authority changes, and mismatched encoded paths yield
`full_pattern: null`; they are never reconstructed by suffix search.

Metadata can arrive after a final response in a timeout race. The reader
therefore resolves all records after loading. The last route observation for an
exchange controls the final label. If that route has no verified full pattern,
the label falls back to the origin's concrete path instead of an earlier coarse
pattern. API-only metadata does not erase the route.

The Prometheus adapter reports `r.prefix + handlerName` as `httprouter` evidence
before dispatch. It does not assign an API ID.

## 8. Record shapes

Every JSONL record has this envelope:

```text
schema_version=2, capture_id, process_id, seq, exchange_id, kind
```

The five kinds use three shapes:

- `send_request` and `receive_request`: context, request snapshot, API ID;
- `send_response` and `receive_response`: integer final status only; and
- `request_metadata`: nullable route and API fields, with at least one present.

Required nullable fields are explicit JSON nulls. Fields from another variant
are absent. Unknown fields, kinds, and versions are errors. Responses and
metadata refer to one exact request declaration through exchange identity; they
do not repeat request snapshots.

Pydantic after-validators enforce rules that generated JSON Schema cannot fully
express, such as terminal status, a client host, nonempty metadata, and valid
semantic label endpoint combinations. `event.schema.json` is useful for
interoperability but is not a replacement for the documented invariants.

## 9. Reader and exact association

`read_capture(path)` accepts a file or a directory. Directory files are selected
as sorted `*.jsonl` names. It reads binary physical lines and performs strict
UTF-8 decoding before Pydantic validation. Blank lines are ignored without
renumbering locations. Shape errors become `ValueError` messages containing the
exact file and physical line; filesystem errors are preserved.

Cross-record checks require:

- one capture/process identity per nonempty file;
- one capture ID per directory;
- one process in at most one file;
- sequence 1 followed by contiguous process-local values;
- one preceding request declaration for every response/metadata record;
- response direction consistent with the request direction;
- at most one final response per exchange;
- metadata only on received/server requests; and
- one consistent known API binding per exchange.

Requests without responses, empty process files, empty capture directories,
unknown context/API values, metadata after responses, and equal local counters
in different processes are valid.

After all route/API evidence is resolved, the reader constructs occurrences in
record order. A response copies the exact origin label and context, adding only
its response kind and status. There is no URL, FIFO, context, stack, or nearest
request search.

Server labels contain either a verified route plus dialect or a tagged concrete
path fallback. Client labels contain authority and path. Semantic normalization
changes only an explicit empty client/server path to `/`; it does not fold case,
drop ports, clean paths, or translate router dialects.

## 10. Shared-context influence

For each known scoped context `c`, the implementation forms sets of received and
sent labels:

```text
R(c) = {receive_request, receive_response labels}
S(c) = {send_request, send_response labels}
E    = union over c of R(c) x S(c)
```

`shared_context_pairs` is order-independent and deduplicates semantic label
pairs only after exact occurrence association. It deliberately has no temporal
filter and no same-exchange exclusion. Thus a received response can influence an
earlier request send that shares its context. This is the selected
overapproximation, not time-travel causality.

Unknown contexts never group. Equal counter values in different processes or
capture IDs never join. Isolated labels remain in diagnostic node output even
when they participate in no pair.

`testdata/v2-chain.jsonl` demonstrates four message occurrences and four edges:
each of its two receives pairs with each of its two sends. Its route metadata is
not a fifth message.

## 11. Diagnostics and downstream boundary

`contexttrack groups INPUT` retains and prints each occurrence with scoped
context and exchange IDs. `contexttrack graph INPUT --format text|dot` interns
immutable labels, retains isolates, and renders possible-influence pairs. DOT
strings escape quotes, backslashes, control characters, Unicode, and newlines;
a capture with no nodes still emits valid `digraph` syntax. DOT counts go to
stderr.

The local commands are diagnostics, not a PMGraph implementation or module
ownership resolver. The separately owned `conftamer-cli` importer must consume
the shared package, accept module identity from its caller, and map resolved
labels/pairs into its PMGraph. That consumer migration and artifact distribution
remain separately approved work; no v1 compatibility reader is hidden here.

## 12. Acceptance evidence, size audit, and limitations

### Size audit

The final audit counts physical lines, including comments and blanks, and counts
overlay source once. Native patch additions are counted only for hook/state lines
not present in the overlays.

| Maintained local area | Final | Planned | Disposition |
| --- | ---: | ---: | --- |
| Go helpers plus native hook additions | 576 | 500-650 | 544 overlay production lines plus 32 native additions; within range. |
| Prometheus adapter additions | 3 | 5-10 | Smaller than estimated; one call and its local explanation are sufficient. |
| Shared Python models/reader/exports | 440 | 260-360 | 22% above the upper estimate. The explicit typed variants, immutable labels, location-bearing validation, and cross-record error paths were retained rather than compressed. |
| Diagnostics and wrappers | 204 | 140-200 | Four lines above the estimate; still one renderer/CLI with two seven-line wrappers. |
| Patch application script | 66 | 40-60 | Six lines above the estimate due to explicit destination/version/cleanliness/repeat-application checks. |
| **Local production subtotal** | **1,289** | **945-1,280** | Nine lines (0.7%) above the aggregate upper estimate. |

`go-inlibrary.patch` is 153 physical diff lines with 32 source additions;
`prometheus-common-route.patch` is 14 diff lines with 3 source additions. These
are packaging sizes and are not added again to the production subtotal.

Go tests total 1,582 lines (340 internal and 1,242 black-box) versus the
600-900 planning budget. Local Python tests total 1,109 lines versus 260-380.
The estimates undercounted the required negative shape/integrity matrix and the
protocol, behavior-preservation, concurrency, process-isolation, and timeout
matrix. The human reviewer accepted these deviations at the Task 7 size gate;
validation and readable test setup were not weakened to meet a quota.

Generated schema, fixture, lockfile, and documentation are excluded. The
separately owned downstream importer/model are also excluded because Gate C
consumer migration was not authorized as part of this producer task.

### Acceptance runs

Task 7 acceptance used a fresh Go tree at commit
`1ea5a71ad8ceb7b9f16b4b6f8ea4739a4327dd6e`, whose `VERSION` is `go1.26.6`.
The test binary was compiled with all capture variables absent and then run with
capture enabled only for selected tests.

The black-box `TestConftamerCaptureExample` emitted six valid records: four
message occurrences plus ServeMux route and explicit server API metadata. The
reader resolved four labels and the diagnostic graph produced two context-local
edges, including the intended received-response-to-earlier-request edge.

A disposable Prometheus checkout at
`d15adb9ad7e5d9fbde3a9a8f30200593a5a14d86` used a clean
`prometheus/common` v0.70.1 checkout at
`b63d8c0f100a0788a91445e376ec3b1598e69c99` with the adapter patch:

- `TestTargetScraperScrapeOK` passed and emitted 16 message records. Its eight
  client occurrences had unknown contexts and all API IDs were unknown, as
  expected without explicit annotations.
- The bounded full `scrape` package test binary passed from its package working
  directory and emitted 587 valid message occurrences: 147 request sends, 146
  response receives, 147 request receives, and 147 response sends. One client
  attempt without a final response is valid evidence, not a repaired record.

No routes appeared in those Prometheus test captures because their selected
fixture servers dispatch directly rather than through the patched
`prometheus/common/route` adapter. Adapter application and its `./route` tests
were verified separately.

Current boundaries remain important:

- only standard-library HTTP/1 and bundled HTTP/2 hook paths are covered;
- external `x/net/http2`, custom transports, mocks, tunnels, and pre-dispatch
  protocol rejection can bypass observations;
- API ownership requires reviewed annotations;
- autonomous clients require explicit roots for influence grouping;
- bodies, headers, query strings, timestamps, PIDs, source locations, and
  network trace propagation are absent;
- synchronous logging can affect timing even though focused tests preserve HTTP
  values, bodies, cancellation, retries, redirects, trailers, and callbacks;
- Caddy and actual Kubernetes integration remain follow-ups; and
- successful validation proves internal consistency of consumed records, not
  capture completeness or workload/module purity.
