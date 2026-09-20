# ContextTrack v2 Revision Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` after implementation approval. Execute the checkbox steps task by task, with test-first changes and the review gates below. Do not delegate, commit, push, or modify existing sibling workspaces without separate authorization.
>
> **Status: replacement plan for human review, not authorization to implement.** This supersedes [2026-09-15-contexttrack-output-cleanup.md](2026-09-15-contexttrack-output-cleanup.md) in full. Keep that draft as historical evidence; do not combine its contracts with this one.

**Goal:** Produce small, typed, reliably associated HTTP observations that one shared Pydantic reader can consume, and project the paper's shared-context message influence without heuristic request matching.

**Architecture:** Keep capture in patched Go `net/http`; emit versioned JSONL with separate context and exchange identities. A small `contexttrack` Python package owns record models, integrity checking, exact metadata attachment, and the shared-context pairing rule. `conftamer-cli` remains the owner of PMGraph construction; local commands are diagnostics, not another PMGraph implementation.

**Tech Stack:** Go `go1.26.6`, Go standard library only in the producer, Python >=3.14, Pydantic >=2.13.5,<3, pytest >=9.1.1 for Python tests, Ruff for linting, ty for type checking, optional Graphviz. No runtime `jsonschema`, parser framework, HTTP-body tracing, or new Go library dependency.

**Spec:** The current user request, the four decisions recorded below, and the proposed contract in Sections 2-5. Conceptual authority: `../../ConfTamer_HotNets_2026.pdf`, Sections 4-5, Figures 2-3. This plan deliberately excludes ParamTrack, CType, and AppGraph stitching.

## Global constraints and decisions

The user selected these planning directions on 2026-09-18:

1. **Clean v2 break.** No v1 reader, dual emission, converter, or compatibility aliases. Preserve historical captures unchanged; their old tooling remains historical.
2. **Shared-context pairs.** Every received-message label is related to every sent-message label in the same scoped context, irrespective of observation order. This replaces the downstream reader's receive-before-send restriction.
3. **Core fixes only.** Implement standard-library HTTP/1, bundled HTTP/2, ServeMux, and the existing Prometheus router adapter. Caddy/external HTTP/2 and Kubernetes integration closure are explicit follow-ups, not hidden requirements of this change.
4. **One shared Python contract.** Use the existing `src/contexttrack/` scaffold for Pydantic models and reading; both local diagnostics and the downstream importer consume it.

Additional constraints:

- These decisions approve planning direction, not all details below or implementation.
- Read and preserve existing dirty/untracked work. In particular, do not overwrite the existing Python scaffold or documentation as incidental setup.
- Sibling captures and producer workspaces are read-only references. Use fresh disposable Go, `common`, application, and consumer checkouts during authorized execution.
- Never patch the system toolchain or Go module cache. Regenerate patches from formatted source; never edit hunk counts by hand.
- All Go invocations use an explicit executable, unset `GOROOT`, and `GOTOOLCHAIN=local`. Run captures uncached and outside source-fixture directories.
- Do not consume bodies, change HTTP return values, suppress errors, alter cancellation, or mutate caller-owned requests for instrumentation. Synchronous logging can add latency; do not promise timing equivalence.
- The implementation must distinguish unobserved, unknown, invalid, and unsupported data. Passing validation does not prove complete coverage or a complete capture.
- A shared dependency does not authorize edits in `../../conftamer-cli/`. Its migration is a coordinated, separately approved consumer change at Gate C.
- At every implementation checkpoint and before task completion, run both `uvx ruff check .` and `uvx ty check`. Tests do not replace either check; record any inability to run them as a verification limitation.

---

## 1. Evidence and differences from the superseded draft

### Inspected baseline

- Producer repository: `222c9b6`, with a pre-existing README modification and untracked guide, implementation notes, plans, and Python scaffold.
- Consumer repository: `4b61650`, branch `contexttrack-import`, with untracked documentation and captures.
- Applied Go reference: `../../run-ctxtrk/go-conftamer/VERSION` says `go1.26.6`. It is not a clean patch-application test.
- Prometheus reference source: `d15adb9ad7e5d9fbde3a9a8f30200593a5a14d86`, with local changes. Its `go.mod` requires `common v0.70.1`; README's `v0.69.0` recipe is stale for that reference. The copied `common` directory is not a Git checkout.
- The consumer's source has the callable importer and basic PMGraph models. Its actual `cli.py` is still a greeting. Its untracked guide describes build/query/export functionality and model validators not present in this source revision. **Do not build this plan on those advertised commands.**

Observed problems, not conjectured success criteria:

| Evidence | Required response |
| --- | --- |
| `git apply --numstat go-inlibrary.patch` fails at line 428; the new-file hunk declares 392 lines but contains 379 | Rebuild a portable patch from the matching Go tree; parsing is an explicit gate |
| Small real capture: 20 records for four HTTP exchanges, including eight response receives | Give each observation one owner, not a downstream deduplication heuristic |
| Current context reader allocates a fresh unstored ID if lookup fails; `Client.do` mutates `req.ctx` | Separate root creation from lookup; stop modifying caller-owned client contexts |
| H1 response hooks run before filtering ordinary 1xx; H2 public `WriteHeader` logs before native guards | Log the accepted final-header transition, including implicit responses |
| H2 receipt can pass through both H2 and common server dispatch; no H2 client wire-response hook exists | Attach server state once and add the missing client owner before deleting the duplicate client hook |
| Routes/responses lack occurrence IDs; local graphs guess suffixes and reuse method/path matches | Exact exchange references; only verified route composition |
| `all-tests.jsonl`: 13,954 records, 69 PIDs, 8,552 PID/context groups; 4,408 records without API hints | Scope process identity properly; do not assume missing API values are rare or infer them from neighbors |
| In that capture, AWS client calls and their test-server receives can have different organization hints | Caller/handler organization is not reliable API ownership |
| Local graph code removes repeated labels before projection and hides isolated nodes | Keep occurrences until association; use the approved shared-context rule and retain isolates |
| File writes ignore errors; one append file can mix processes/runs | Separate process files, exclusive creation, checked writes, explicit failure diagnostics |

### Deliberate changes to the previous proposal

- Pydantic models are the executable record contract. Generate JSON Schema from them; do not maintain a handwritten schema plus a second handwritten shape validator.
- Use **exchange IDs**, not `request_id`, for occurrence references. The paper's `Request_ID` remains a semantic request label, not this counter.
- Store a request label once. Responses contain its exact exchange reference and integer status, not repeated request snapshots and null-filled unrelated fields.
- Replace routing-only metadata with `request_metadata`: a typed route observation and/or an explicit API binding. It is never a semantic message.
- Remove API stack/handler guessing from semantic output rather than preserving it under an evidence wrapper.
- Drop receive-before-send filtering. Sequence numbers support integrity and metadata resolution, not the influence criterion.
- Do not promise automatic client-root discovery. Section 4 gives an explicit, non-mutating boundary and identifies its coverage trade-off.
- Reuse a small Python package instead of maintaining three independent readers/projectors. Do not add the old draft's schema-only dependency.

## 2. Paper semantics to preserve

### 2.1 Three different identities

| Identity | Meaning | Lifetime/use |
| --- | --- | --- |
| `(capture_id, process_id, seq)` | One observed record | Integrity and diagnostic location |
| `(capture_id, process_id, exchange_id)` | One server request or client attempt | Exact request/response/metadata association |
| `(capture_id, process_id, context_id)` | A known, inherited context root | Possible influence; absent IDs never form a group |

None is a semantic PMGraph node ID, a network trace header, an OS PID, a goroutine ID, or a stable identifier across tests/runs. Separate processes may use identical local counters. Across tests, equal **semantic labels** may share a graph node; context/exchange counters must not be made equal to force correlation.

For each known context `c`, let `R(c)` and `S(c)` be its received and sent **message labels**, after exact association and route resolution:

```text
E = union over c of R(c) x S(c)
R = {receive_request, receive_response}
S = {send_request, send_response}
```

This implements the user's chosen reading of Section 5.1, Propagator 4: "When a send shares a context with a receive, PMTool records influence from the receive to the send." The paper does not specify a log-order restriction. This is an overapproximation, not temporal causality: a received response may therefore point to the earlier request send in the same context. **Do not add a same-exchange exclusion or temporal filter to remove that result.**

Ordering remains important for valid declarations, unique final responses, and serial routing chains. Preserve every raw occurrence; deduplicate final semantic nodes/edges, not the evidence needed to attach their labels.

### 2.2 Labels and module boundaries

- The consumer supplies one nonempty `module_id` for an audited module capture. Neither process identity nor API identity supplies it.
- Client labels contain direction/request-or-response, API ID, method, HTTP authority, and path. Responses use their request's label plus status.
- Server labels contain direction/request-or-response, API ID, method, and a verified route pattern **with its dialect**. If unavailable, retain a tagged concrete-path fallback; never claim it is a known route pattern.
- An explicit empty `URL.Path` is valid raw data; semantic path normalization changes only `""` to `"/"`. Missing is not empty. Do not clean paths, fold method/host case, drop ports, or turn one router dialect into another.
- `API_ID` identifies the organization that developed the communication API (paper Figure 2 and Section 5.2), not the module sending a request. A client in organization A using B's API must be labelable as API B.
- Unknown API IDs remain null. Unknown IDs are not a wildcard for future stitching. No stitching is implemented here.
- Test helpers can simulate another module inside the same process. A clean event file is not proof that every event belongs to the caller-supplied module. Use audited test scope or an uninstrumented/separately captured fixture server; report mixed-module captures as diagnostic evidence, not automatically valid PMGraphs.

## 3. Proposed v2 capture contract

### 3.1 Files and failures

Require these two settings when enabled:

```text
CONFTAMER_EVENTS_DIR=/absolute/path/to/a/fresh/capture-directory
CONFTAMER_CAPTURE_ID=scrape-smoke-unique-run
```

The caller creates the directory. Each process generates a random 128-bit identity, rendered as 32 lowercase hex characters, and exclusively creates `<process_id>.jsonl` with mode `0600`. No append/reopen mode and no shared-process file. A process file may be empty.

- Both settings absent: disabled, with no request/context mutation, snapshots, stack walks, or file I/O.
- Partially specified/invalid settings: diagnostic and disabled capture, without changing application exit status or HTTP results.
- A set legacy `CONFTAMER_EVENTS` is a configuration error with a migration diagnostic, even if new settings are also set. Do not silently ignore it or reinterpret its file as a directory.
- Initialization is once per process. Print `conftamer: enabled` with process ID, capture ID, and file path after successful creation.
- Under one logger mutex, assign `seq`, encode, and write a complete JSON line. Successful sequences start at 1 and are contiguous. Counters are positive uint64 values; exchange/context counters need not be contiguous in the file.
- Reject unrepresentable logged strings (invalid UTF-8) before Go's JSON encoder can replace them. Check marshal errors, write errors, and short writes. Latch the first failure, report it once to stderr, and stop that logger. Never insert diagnostics into JSONL or silently continue after losing a record.
- No background queue, shutdown hook, `fsync` per event, or claim of crash durability. A valid prefix can still be incomplete; consumers cannot prove success from JSON alone. Capture acceptance also requires checking diagnostics and expected activity.
- No PID, goroutine/thread IDs, source locations, timestamps, query strings, headers, or bodies in the public record. File/physical-line locations are added by the Python reader for errors, not serialized as fake source evidence.

### 3.2 Five kinds, three record shapes

All records have `schema_version`, `capture_id`, `process_id`, `seq`, `exchange_id`, and `kind`. The kinds are:

```text
send_request, receive_request, send_response, receive_response, request_metadata
```

Request records add:

```text
context_id: positive integer or null
request: {method: nonempty string, host: nonempty string or null, path: string}
api_id: nonempty string or null
```

Response records add only `status_code`, an integer final status: HTTP/1 `101`, or `200..999`. Ordinary informational responses and trailers are not semantic messages. If native code accepts a value outside this representable contract, report a capture failure; do not change native HTTP handling to make it fit.

Metadata records add these two required, nullable fields; at least one must be non-null:

```text
route: {dialect, pattern, matched_path, full_pattern} or null
api_id: nonempty string or null
```

Metadata only references a **server** exchange. `route.dialect` is `go_serve_mux`, `go_serve_mux_121`, or `httprouter`. Preserve `pattern` exactly; `matched_path` is the router's local `URL.Path`; `full_pattern` is a verified pattern in the original request's path space, or null.

All nullable fields belonging to a variant must be explicit nulls when unknown. Fields belonging to another variant must be absent. Unknown fields/kinds/versions are errors, not ignored extras.

**Host decision:** `request.host` is the effective HTTP authority: `Request.Host` when nonempty, otherwise `URL.Host`; an absent server authority is null. This deliberately replaces the old client-only `URL.Host` spelling when a Host override exists, so host-qualified routes describe the same authority. It is not DNS resolution, proxy address, or API ownership. Preserve spelling and port. Method `""` becomes Go's default `GET`; retain raw `URL.Path`, including `""`. RawPath/opaque request-target fidelity and non-HTTP tunnel traffic are outside this contract.

### 3.3 Executable Pydantic shape

Put these definitions in `src/contexttrack/events.py`. No default values on required nullable fields. A small version validator is intentional: `Literal[2]` alone can accept numerically equal `2.0`.

```python
from typing import Annotated, Literal, Self
from pydantic import (
    BaseModel, ConfigDict, Field, TypeAdapter, field_validator, model_validator,
)

Counter = Annotated[int, Field(strict=True, ge=1, le=2**64 - 1)]
Text = Annotated[str, Field(min_length=1)]
ProcessID = Annotated[str, Field(pattern=r"^[0-9a-f]{32}$")]
Dialect = Literal["go_serve_mux", "go_serve_mux_121", "httprouter"]

class Model(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)

class RequestLabel(Model):
    method: Text
    host: Text | None
    path: str

class Route(Model):
    dialect: Dialect
    pattern: Text
    matched_path: str
    full_pattern: Text | None

class Envelope(Model):
    schema_version: Literal[2]
    capture_id: Text
    process_id: ProcessID
    seq: Counter
    exchange_id: Counter

    @field_validator("schema_version", mode="before")
    @classmethod
    def integer_version(cls, value: object) -> object:
        if type(value) is not int:
            raise ValueError("schema_version must be an integer")
        return value

class RequestEvent(Envelope):
    kind: Literal["send_request", "receive_request"]
    context_id: Counter | None
    request: RequestLabel
    api_id: Text | None

    @model_validator(mode="after")
    def client_host(self) -> Self:
        if self.kind == "send_request" and self.request.host is None:
            raise ValueError("send_request requires a host")
        return self

class ResponseEvent(Envelope):
    kind: Literal["send_response", "receive_response"]
    status_code: Annotated[int, Field(strict=True, ge=100, le=999)]

    @model_validator(mode="after")
    def terminal_status(self) -> Self:
        if self.status_code < 200 and self.status_code != 101:
            raise ValueError("only terminal responses are recorded")
        return self

class MetadataEvent(Envelope):
    kind: Literal["request_metadata"]
    route: Route | None
    api_id: Text | None

    @model_validator(mode="after")
    def has_metadata(self) -> Self:
        if self.route is None and self.api_id is None:
            raise ValueError("metadata needs a route or API binding")
        return self

Event = Annotated[
    RequestEvent | ResponseEvent | MetadataEvent, Field(discriminator="kind")
]
EVENT_ADAPTER = TypeAdapter(Event)
```

Parsing one line is simply `EVENT_ADAPTER.validate_json(line)`. Generate `event.schema.json` with `EVENT_ADAPTER.json_schema()`; it is a derived interoperability artifact, not a separately edited source of truth. Generated JSON Schema does not express every Python after-validator rule (for example, nonempty metadata), so non-Python consumers also need the invariants in `OUTPUT.md`. Pydantic validates record shape and cross-field rules; the reader validates cross-record relationships. Do not claim a field schema can establish trace integrity.

### 3.4 Auditable five-record fixture

Create `testdata/v2-chain.jsonl` with these complete synthetic records, one per physical line:

```jsonl
{"schema_version":2,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":1,"exchange_id":1,"kind":"receive_request","context_id":7,"request":{"method":"GET","host":"module-b.test","path":"/front/42"},"api_id":null}
{"schema_version":2,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":2,"exchange_id":1,"kind":"request_metadata","route":{"dialect":"go_serve_mux","pattern":"GET /front/{id}","matched_path":"/front/42","full_pattern":"GET /front/{id}"},"api_id":"example.org/b"}
{"schema_version":2,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":3,"exchange_id":2,"kind":"send_request","context_id":7,"request":{"method":"GET","host":"module-c.test","path":"/back"},"api_id":"example.org/c"}
{"schema_version":2,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":4,"exchange_id":2,"kind":"receive_response","status_code":200}
{"schema_version":2,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":5,"exchange_id":1,"kind":"send_response","status_code":200}
```

There are four message occurrences/nodes and **four** influence edges, not three. Both receives influence both sends under the selected approximation. Metadata is not a fifth message. Response 4 uses request 3's client label; response 5 uses request 1's resolved server pattern/API. No path or context search is needed to associate either response.

## 4. Producer ownership, contexts, and labels

### 4.1 Context policy: fix the mutation rather than conceal it

Server ingress creates one context root on its **server-owned request** before handler dispatch. Do not stamp connection-wide/background contexts or share a root between independent ingress requests. Derived contexts inherit its private value normally.

Client observations read a stamped context but **do not create one by mutating or secretly replacing the caller's request**. An unstamped client context is null; its request/response still associate exactly by exchange ID. Direct `Transport.RoundTrip` obeys the same rule, with no per-log fallback allocation.

Provide a small explicit origin helper for autonomous client work:

```go
func ConftamerContext(ctx context.Context) context.Context
```

When enabled, this returns an already-stamped context unchanged or a child with a new ID; nil remains nil. When disabled, it returns its argument unchanged. Callers must use the returned context. Do not modify `context` internals or traverse heap addresses.

```go
ctx := http.ConftamerContext(context.Background())
req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
if err != nil {
    return err
}
response, err := client.Do(req)
```

**Coverage trade-off requiring Gate A approval:** unannotated client-only operations now report unknown context instead of the old automatic, stored request-origin root. That is a real coverage reduction, distinct from fixing the unstored per-log fallback. Server-derived outbound operations remain automatic; autonomous tasks need the helper at their actual operation origin. This is not a claim to recover arbitrary unstamped/custom context ancestry. Replacing a propagated context with `context.Background()` loses influence; `WithoutCancel` retains values and therefore the root. Redirects/retries inherit an existing context through native Go behavior; they do not get a new root per log.

### 4.2 Exchange state and observation owners

Use a private immutable exchange record containing its ID, known context ID, and origin snapshot. Keep it in server `Request`, HTTP/1 `transportRequest`, and HTTP/2 `http2clientStream`. Allocate client occurrence state per attempt, not on reusable caller requests or in `context.Value`. Server copies retain the server exchange pointer; an outbound use of a server request creates a different client exchange.

| Observation | Authoritative location in pinned Go | Requirement |
| --- | --- | --- |
| HTTP/1 receive request | `serverHandler.ServeHTTP`, before handler dispatch | Attach root/exchange and emit once on the server-owned request |
| HTTP/2 receive request | `http2serverConn.runHandler` | Same attach-once helper; later common dispatch reuses state without another event |
| HTTP/1 send request | `persistConn.roundTrip`, before publishing to `writech`/`reqch` | New attempt ID; origin immutable before concurrent workers see it |
| HTTP/2 send request | `http2clientStream.encodeAndWriteHeaders`, after successful header encoding, before `cc.writeHeaders` | Prepare immutable attempt state in `http2ClientConn.roundTrip` before launching `cs.doRequest`; failed encoding produces no sent message |
| HTTP/1 receive response | `persistConn.readResponse`, after its informational-response loop | One final response using `rc.treq`'s exchange |
| HTTP/2 receive response | `http2clientConnReadLoop.processHeaders`, after successful non-null `handleResponse`, before `respHeaderRecv` notification | Stream exchange; not an informational frame or trailer |
| HTTP/1 send response | `response.WriteHeader`, at accepted `wroteHeader/status` transition | After validation, duplicate/hijack guards, and informational return; includes implicit 200 and terminal 101 |
| HTTP/2 send response | `http2responseWriterState.writeHeader`, at accepted final-header transition | Below public wrapper, after native guards; covers implicit/empty-handler response; no HTTP/2 terminal 101 |

Delete **all** old `Client.do` instrumentation, including direct `req.ctx` assignment and response logging. Delete the transport-entry request log. Do not retain duplicate hooks behind a generic deduplication cache.

A send records a transport attempt/accepted final headers, not proof of delivery or body completion. A failed attempt can lack a response. Retries and redirect hops have distinct exchange IDs, including attempts with identical labels. Protocol rejection before handler dispatch, hijacked/tunneled traffic, custom transports, mocked handlers, and external `x/net/http2` are not silently promoted to covered messages.

### 4.3 Explicit API ownership, never inherited as context identity

Remove `conftamerFindCaller`, handler reflection, organization shortening, stack parsing, and API recovery from nearby events. Provide these narrow helpers:

```go
func ConftamerWithClientAPI(r *Request, apiID string) *Request
func ConftamerSetServerAPI(r *Request, apiID string)
func ConftamerLogRouted(r *Request, dialect, pattern string)
```

- When enabled, the client helper returns a shallow copy with a private, immutable API binding to that request's method/authority/path; when disabled, it returns its input unchanged. Never mutate its input or put the binding in a context. If a later copy changes those target fields, the old binding does not apply.
- No automatic client API binding propagation through redirects. An audited `CheckRedirect` callback can assign `*next = *http.ConftamerWithClientAPI(next, apiID)` to the callback-owned request after checking its target; merely calling the copy-returning helper and discarding its result does not bind anything. Unknown hops remain null. Native context propagation is independent of API binding.
- The server helper emits API-only metadata for an already attached server exchange. It does not attach the server's API ID to an outbound request that inherits its context. Repeating an identical binding is allowed; conflicting bindings are a trace-integrity error, not last-wins attribution.
- Route reporting emits route-only metadata. Both helpers do nothing for an untraced server request rather than inventing receipt/exchange records.
- Invalid API/dialect strings cause capture diagnostics, not altered HTTP results. Unbound IDs remain null. The generic Prometheus `common/route` adapter must **not** hardcode Prometheus API ownership: other applications can use that library.

Example of reviewed provider-side labeling:

```go
mux.HandleFunc("GET /front/{id}", func(w http.ResponseWriter, r *http.Request) {
    http.ConftamerSetServerAPI(r, "example.org/b")
    w.WriteHeader(http.StatusOK)
})
```

A cross-organization test must show API B on both a client in module A and the server implementing B. The producer does not automatically discover arbitrary API ownership. Missing bindings are a visible coverage limitation; a capture with null API IDs is not advertised as stitch-ready. Target-specific binding additions need their own reviewed call-site evidence, not a new heuristic resolver or generic configuration language.

### 4.4 Routes and rewrites

Retain the original server snapshot. Preserve a stripped-prefix value on request copies, updating it only in successful native `http.StripPrefix` transformations (including its existing RawPath checks). Emit metadata immediately, without holding the request open until completion.

A full pattern is verified only when method/authority are unchanged and:

```go
func conftamerFullPattern(originalPath, prefix, matchedPath, pattern string) (string, bool) {
    if originalPath != prefix+matchedPath {
        return "", false
    }
    slash := strings.IndexByte(pattern, '/')
    if slash < 0 {
        return "", false
    }
    return pattern[:slash] + prefix + pattern[slash:], true
}
```

Use this only with the recorded native prefix chain, never a suffix guessed from two URLs. Arbitrary rewrites yield `full_pattern: null` while preserving the actual local pattern. Request copies carry prefix state by value, not as shared mutable exchange state.

For one exchange, the **last route observation** controls its final label. If it is unverifiable, fall back to the original concrete path rather than an earlier coarse pattern. API-only metadata does not erase the last route. Serial nested routing is supported; arbitrary concurrent dispatch/rewrite reconstruction is not. Routing and API metadata may arrive after a response in timeout races, so resolve labels after reading the capture; do not use metadata time as proof it caused that response.

## 5. Shared reader and downstream boundary

### Public interfaces

`src/contexttrack/events.py` also defines an immutable `MessageLabel` with exactly these fields:

```text
kind: send_request | receive_request | send_response | receive_response
api_id: str | None
method: str
host: str | None
path: str | None
pattern: str | None
pattern_dialect: Dialect | None
status_code: int | None
```

Use the same strict/frozen model base. Labels contain no occurrence/capture IDs. Client messages have host/path and no pattern; server messages have host=null and either a pattern+dialect or a concrete path. Requests have null status. Responses copy their associated request's resolved fields plus status. `MessageLabel` is hashable and is the semantic key, not an ad-hoc tuple with omitted fields.

`src/contexttrack/capture.py` defines ordinary frozen dataclasses:

```text
RecordedEvent(event: Event, location: str)
Occurrence(label: MessageLabel, context_key: tuple[str,str,int] | None,
           exchange_key: tuple[str,str,int], seq: int, location: str)
Capture(records: tuple[RecordedEvent,...], occurrences: tuple[Occurrence,...])

read_capture(path: str | Path) -> Capture
shared_context_pairs(occurrences: Iterable[Occurrence])
    -> set[tuple[MessageLabel, MessageLabel]]
```

These are capture/label operations, not a PMGraph model or module-ownership resolver.

### Reader rules

1. Accept one file or one directory. Read directory `*.jsonl` files in sorted filename order. Iterate binary lines and decode each with strict UTF-8, so even decoding failures have an exact physical line; ignore blank lines without renumbering locations. Empty captures return empty data, not proof instrumentation ran.
2. Validate each line once with the Pydantic adapter. Wrap failures as `ValueError` containing `path:physical-line` and the original reason; preserve filesystem errors. Do not warn-and-skip malformed consumed records.
3. A nonempty file contains one capture/process identity. One directory contains one capture ID; a process cannot occur in two files. Each process sequence starts at 1 and is contiguous. File ordering is deterministic presentation, not cross-process chronology.
4. Declare each exchange exactly once with a request. Require a preceding origin for response/metadata references; verify server/client response direction and at most one final response per exchange. Requests without responses are valid. Do not silently absorb duplicate final-response hooks.
5. Metadata targets a received request. Resolve the last route and a consistent non-null API binding by exact exchange key. No nearest/FIFO/context/URL/stack matching. Conflicting known API bindings fail with both relevant locations.
6. Construct message occurrences in record order after resolving labels. Responses inherit their origin's known context key. Null contexts never group together; unknown API IDs do not discard otherwise usable occurrences.
7. Preserve route dialect and the pattern-versus-concrete-path distinction in semantic identity. Retain isolates. A response association is not itself a graph edge; edges come only from shared contexts.

The pairing helper is deliberately small:

```python
def shared_context_pairs(occurrences):
    groups = {}
    for occurrence in occurrences:
        if occurrence.context_key is None:
            continue
        receives, sends = groups.setdefault(occurrence.context_key, (set(), set()))
        destination = receives if occurrence.label.kind in (
            "receive_request", "receive_response"
        ) else sends
        destination.add(occurrence.label)
    return {(received, sent) for receives, sends in groups.values()
            for received in receives for sent in sends}
```

No graph class, event visitor, parser registry, streaming join engine, or multi-capture normalization stage is needed. Retaining a capture in memory is acceptable for this prototype; evaluate memory use on larger inputs before adding complexity.

### Consumer migration, after separate approval

Keep `load_contexttrack(path, *, module_id) -> PMGraph`. Its implementation validates module ID, reads resolved occurrences, interns labels, creates existing `Message` models, and maps shared-context label pairs to node IDs:

```python
from pathlib import Path
from contexttrack.capture import read_capture, shared_context_pairs
from conftamer.pmgraph.models import Message, PMGraph

def load_contexttrack(path: str | Path, *, module_id: str) -> PMGraph:
    if not isinstance(module_id, str):
        raise TypeError("module_id must be a string")
    if not module_id:
        raise ValueError("module_id must be nonempty")
    capture = read_capture(path)
    labels = dict.fromkeys(item.label for item in capture.occurrences)
    node_ids = {label: f"n{i}" for i, label in enumerate(labels)}
    nodes = {
        node_ids[label]: Message.model_validate(label.model_dump())
        for label in labels
    }
    edges = {
        (node_ids[source], node_ids[target])
        for source, target in shared_context_pairs(capture.occurrences)
    }
    return PMGraph(module_id=module_id, nodes=nodes, edges=edges)
```

Add only `pattern_dialect: Literal["go_serve_mux", "go_serve_mux_121", "httprouter"] | None = None` to the downstream `Message` model, and test preservation. Do not redesign the generic PMGraph, parameters, node IDs, persistence, queries, or CLI. Remove the old `_Event`, dotted-field parsing, string-status conversion, `_correlate`, and `prior_receives` implementation together; no hidden v1 dispatch remains.

Distribution is a real handoff requirement: build `contexttrack` as a versioned wheel (`0.2.0` for this first contract), pass that artifact and its digest to the consumer owner, and test installation in a disposable consumer environment. Local editable installation may use the sibling path. Before a downstream release/CI migration, its owner must pin and make that wheel available through their approved artifact channel; merely importing a development checkout is not integration completion. Do not copy the Pydantic models into the consumer to avoid this dependency.

## 6. Planned files and responsibilities

Paths are relative to `contexttrack/` unless explicitly marked otherwise.

| Files | Change/responsibility |
| --- | --- |
| `src/contexttrack/events.py` | Canonical Pydantic records and immutable resolved labels |
| `src/contexttrack/capture.py` | File loading, relational integrity, exact label resolution, context pairing |
| `src/contexttrack/inspect.py` | Small argparse `groups` and `graph` diagnostics; text/DOT rendering only |
| `src/contexttrack/__init__.py`, `pyproject.toml`, new `uv.lock` | Replace greeting entry point, package exports, Pydantic/test dependency, lock versioned package |
| `analysis/group_by_context.py`, `analysis/message_graph.py` | Thin wrappers around diagnostics; delete heuristic logic and old flags |
| Delete `analysis/event_io.py` | No second reader |
| `_stdlib/net/http/conftamer.go` | Typed records, immutable exchange state, context/API/route helpers |
| `_stdlib/net/http/conftamer_log.go` | Environment, exclusive process file, sequence/write lock, latched failures |
| `_stdlib/net/http/conftamer_internal_test.go` | Isolated writer/state/prefix tests in package `http` |
| `_stdlib/net/http/conftamer_test.go` | Subprocess/network behavior tests in package `http_test` |
| `go-inlibrary.patch` | Portable hooks-only patch for `request.go`, `client.go`, `transport.go`, `server.go`, `h2_bundle.go` |
| `apply-go-patch.sh` | Version/clean-tree/check-before-apply checks; copies the four explicit overlay files; no downloads/builds |
| `prometheus-common-route.patch` | Portable adapter patch against a clean `common v0.70.1` source |
| `tests/test_events.py`, `tests/test_capture.py`, `tests/test_inspect.py` | Shape, integrity/semantics, and command/rendering tests |
| `testdata/v2-chain.jsonl`, `event.schema.json`, `OUTPUT.md` | Synthetic fixture, generated schema, short approved output contract |
| `../.gitignore` | Allow only reviewed synthetic `contexttrack/testdata/*.jsonl`; keep real captures ignored |
| `README.md`, `IMPLEMENTATION.md`, `AGENTS.md` | Current behavior, setup, compatibility break, coverage matrix after implementation |
| Delete `go-inlibrary-optional.patch` | Retire the unsupported heap-address alternative, retained in repository history |
| Consumer handoff: `../../conftamer-cli/src/conftamer/contexttrack.py`, `src/conftamer/pmgraph/models.py`, `tests/test_contexttrack.py`, relevant model tests, packaging/CI | Separate approved migration; never edit the existing sibling checkout as part of producer work |

The `_stdlib` prefix keeps overlay files out of the parent module's ordinary Go package discovery. Do not also embed these helper files in the generated patch. Maintain normal, gofmt-able source as the review surface.

## 7. Execution tasks and gates

All commands below are **future execution checks**, not checks performed while writing this plan. Each behavior change follows red -> minimal implementation -> green. No automatic commit steps.

### Gate A: contract and scope approval

- [ ] Approve the record example and variant models, authority definition, unknown-client-context policy, explicit API helpers, route fallback, capture settings, and failure behavior.
- [ ] A consumer reviewer confirms the intentionally broader order-independent rule, including response-to-earlier-request edges. Update its old approved importer contract explicitly; do not call this a parser-only change.
- [ ] Confirm that explicit API bindings/unknowns are acceptable for core release; automatic API-owner inference and Caddy/Kubernetes closure are not promised.
- [ ] Obtain authorization for disposable producer build/capture work. Obtain separate authorization before any consumer implementation. Preserve the existing workspaces and captures.

### Task 1: make the Python contract executable

**Files:** `events.py`, `pyproject.toml`, `uv.lock`, `tests/test_events.py`, `testdata/v2-chain.jsonl`, `event.schema.json`, `OUTPUT.md`, `../.gitignore`.

**Consumes:** Section 3. **Produces:** `EVENT_ADAPTER`, `MessageLabel`, fixture and schema.

- [ ] Prepare the test environment without implementing behavior: preserve existing package metadata, set version `0.2.0`, set project dependencies to `["pydantic>=2.13.5,<3"]`, add development dependencies `["pytest>=9.1.1"]`, and run `uv sync --dev`. Retain Python >=3.14 from the scaffold. Run Ruff and ty ephemerally with `uvx`; no additional project dependency is needed.
- [ ] Add the synthetic fixture and tests before models. Use this actual regression in `tests/test_events.py`:

```python
import json
from pathlib import Path
import pytest
from pydantic import ValidationError
from contexttrack.events import EVENT_ADAPTER

FIXTURE = Path(__file__).parents[1] / "testdata/v2-chain.jsonl"

@pytest.mark.parametrize("status", ["200", True, 103])
def test_response_status_is_strict_and_terminal(status):
    record = json.loads(FIXTURE.read_text().splitlines()[3])
    record["status_code"] = status
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_json(json.dumps(record))
```

- [ ] Run `uv run pytest -q tests/test_events.py`; observe collection/missing behavior failure, then implement the proposed model definitions. Add negative cases after initial import works so rejection behavior, not just a missing module, is tested.
- [ ] Require rejection of bool/string/zero counters, wrong/missing version, version `2.0`, unknown kinds/fields, missing nullable fields, absent client host, invalid dialects, empty metadata, and invalid UTF-8/escaped surrogates. Accept explicit empty paths, null context/API, ordinary Unicode, and all five kinds.
- [ ] Define `MessageLabel` fields from Section 5 with after-validation for client/server endpoint and request/response status combinations. Test different hosts, kinds, dialects, and literal-path versus pattern as unequal semantic keys.
- [ ] Generate and snapshot-test the JSON Schema, not a second validator:

```bash
uv run python -c 'import json; from contexttrack.events import EVENT_ADAPTER; print(json.dumps(EVENT_ADAPTER.json_schema(), indent=2, sort_keys=True))' > event.schema.json
uv run pytest -q tests/test_events.py
uvx ruff check .
uvx ty check
```

- [ ] Add the exact ignore exception `!/contexttrack/testdata/*.jsonl`; fixtures are synthetic, not rewritten real traces.

**Review result:** both maintainers can parse and explain five records directly. No event dictionary repair is required.

### Task 2: reproducible patch and checked writer

**Files:** overlay Go files/tests, `apply-go-patch.sh`, regenerated main patch.

**Consumes:** event structs corresponding to the three Pydantic variants. **Produces:** private logger plus usable clean-tree application recipe.

Define `conftamerRequestEvent`, `conftamerResponseEvent`, and `conftamerMetadataEvent` embedding `conftamerEnvelope`, with exported Go fields and JSON tags corresponding directly to Section 3; required nullable fields have pointer types without `omitempty`. A small internal writer can accept the envelope pointer and its containing record, so it fills identity/sequence under one lock before marshaling. Do not introduce custom reflection, arbitrary field maps, or a serialization interface hierarchy.

```go
func newConftamerLogger(w io.Writer, captureID, processID string) *conftamerLogger
func (log *conftamerLogger) write(header *conftamerEnvelope, record any) error
```

- [ ] Create a fresh matching Go source checkout and record its base commit and `VERSION`. Start with typed records, immutable state declarations, writer/configuration code, and the private native state fields only; do not port the faulty old hooks as a supposedly working baseline. This gives Task 2 a compilable, nonempty structural patch; Task 3 adds message emission. Regenerate from source rather than repairing the old patch text.
- [ ] Add a failing isolated writer test using this injectable short writer, plus concurrent sequence and failure-latching cases:

```go
type conftamerShortWriter struct{ calls int }

func (w *conftamerShortWriter) Write(p []byte) (int, error) {
    w.calls++
    return len(p) - 1, nil
}
```

Use the short writer in this regression (imports: `errors`, `io`, `testing`):

```go
func TestConftamerLoggerShortWrite(t *testing.T) {
    output := &conftamerShortWriter{}
    log := newConftamerLogger(output, "unit", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
    event := conftamerResponseEvent{StatusCode: 200}
    event.Kind = "send_response"
    event.ExchangeID = 1
    for range 2 {
        if err := log.write(&event.conftamerEnvelope, &event); !errors.Is(err, io.ErrShortWrite) {
            t.Fatalf("write error = %v, want io.ErrShortWrite", err)
        }
    }
    if output.calls != 1 {
        t.Fatalf("write calls = %d, want 1", output.calls)
    }
}
```

Also test marshal/string validation failure without swallowing errors. Run the patched-Go `test -count=1 net/http -run '^TestConftamerLogger'` before and after implementation with the environment isolation in Section 9.

- [ ] Implement exclusive `0600` process files, strict configuration, disabled fast paths, complete-line locking, checked writes, and stderr diagnostics. Guard the latched error under the writer lock and use atomic state for a cross-goroutine disabled fast path; do not replace it with an unsynchronized global boolean. Child processes, not global `sync.Once` resets, test enabled/disabled/invalid environment paths.
- [ ] Implement application-script checks: exactly one destination argument; clean matching Go Git tree; first `VERSION` line equals `go1.26.6`; overlay paths absent; `git apply --check` before any mutation; copy only the four listed overlay files. Reject repeat application and wrong bases.
- [ ] Regenerate after gofmt using native-file diffs only:

```bash
git -C "$GO_WORK" diff -- src/net/http/request.go src/net/http/client.go \
  src/net/http/transport.go src/net/http/server.go src/net/http/h2_bundle.go \
  > "$REPO/go-inlibrary.patch"
git apply --numstat "$REPO/go-inlibrary.patch"
```

`REPO` is the absolute ContextTrack checkout; `GO_WORK` is the new, authorized disposable tree. Keep those paths in execution notes. Check/apply every revised patch to a second pristine matching tree before claiming reproducibility.

**Review result:** source is readable outside a patch, patch parsing/application is demonstrated, logger failures cannot become silent successful captures.

### Task 3: fix exchange lifetimes and the four HTTP messages

**Files:** `conftamer.go`, Go tests, native hook patch.

**Consumes:** writer and event structs. **Produces:** once-owned observations, independent exchange/root counters, `ConftamerContext`.

- [ ] Add failing subprocess tests for H1, TLS H2, derived contexts, unknown client contexts, direct `RoundTrip`, and duplicate server entry. At minimum, assert four message records for one successful client/server exchange (two origins, two finals), distinct local exchanges, and matching response references.
- [ ] Implement server-root/attach-once helpers and per-attempt client state at the sites in Section 4. Remove legacy hooks in the same change, after the replacement H2 client hook exists.
- [ ] Cover native header lifecycles: explicit/implicit 200, empty handler, repeated `WriteHeader`, 103 then 200, H1 101, body write, and handler panic. No record for rejected/ignored header calls or H2 informational headers.
- [ ] Test redirects and a deterministic transport retry by arranging the connection failure with test synchronization, not sleeps. Distinct attempts need distinct IDs; known root continuity is preserved. Invalid request/encoding cases must preserve native failures, not emit malformed records.
- [ ] Prove behavioral preservation: original request/context identity unchanged, `Response.Request` and redirect callbacks unchanged, legacy `CancelRequest`, Request.Cancel, deadlines/cancellation, body read/close counts, returned errors, trailers and flushing unchanged. Use the same workload with logging on/off.
- [ ] Prove unknown-context lookup allocates nothing; explicit roots and standard derived contexts retain one ID; separate server requests and separately created roots remain separate. Server-to-client request copies must not reuse the server exchange.
- [ ] Run the focused suite and race detector with all capture variables unset in the parent test process; capture subprocesses opt in themselves.

**Gate B:** Review immutable state publication before goroutine handoff, header ownership, and the deliberate absence of automatic unstamped client ancestry. Reject global pointer maps, response dedup caches, or changes that require mutating the user's request to pass tests.

### Task 4: exact routing and explicit API bindings

**Files:** Go overlay/tests, main patch, `prometheus-common-route.patch`.

**Consumes:** server exchange state and metadata variant. **Produces:** three public label helpers and verified route metadata.

- [ ] Write table-driven failing tests for the concrete prefix function:

```go
cases := []struct{ original, prefix, matched, pattern, want string }{
    {"/api/items/7", "/api", "/items/7", "GET /items/{id}", "GET /api/items/{id}"},
    {"/api/items/7", "", "/items/7", "GET /items/{id}", ""},
    {"/api/items/7", "/api", "/items/7", "/items/:id", "/api/items/:id"},
}
```

For the second row require `ok == false`, not an inferred prefix. Add nested strips, host-qualified patterns, method/authority changes, encoded paths/RawPath checks, last-unverifiable route, and request-copy isolation.

- [ ] Implement prefix-copy state and both ServeMux dialect calls. Update the Prometheus adapter to `http.ConftamerLogRouted(req, "httprouter", r.prefix+handlerName)` without adding an API owner guess.
- [ ] Regenerate that adapter patch against clean `common v0.70.1` with relative `route/route.go` paths. Run `git apply --check` and patched-Go `go test -count=1 ./route` in a disposable writable copy.
- [ ] Add tests where server API B and outbound API C share one context: neither binding leaks to the other exchange. Check the client helper leaves its argument untouched, a changed target invalidates the binding, redirects do not inherit it implicitly, repeated identical server bindings work, and untraced server annotation creates no origin.
- [ ] Demonstrate API B on a synthetic client/server pair belonging to modules A/B. This verifies the representation and annotation path, not automatic real-world API attribution.

**Review result:** metadata attaches by one integer reference, and every claimed API owner/full route has an explicit source of evidence.

### Task 5: replace heuristic loading with exact joins

**Files:** `capture.py`, `tests/test_capture.py`.

**Consumes:** models/fixture. **Produces:** `Capture`, `Occurrence`, `read_capture` and fully resolved immutable labels.

- [ ] Add the basic resolution regression before implementing:

```python
from pathlib import Path
from contexttrack.capture import read_capture

FIXTURE = Path(__file__).parents[1] / "testdata/v2-chain.jsonl"

def test_responses_reuse_exact_origin_labels():
    capture = read_capture(FIXTURE)
    received, sent, response_in, response_out = capture.occurrences
    assert response_in.exchange_key == sent.exchange_key
    assert response_out.exchange_key == received.exchange_key
    assert response_in.label.host == "module-c.test"
    assert response_out.label.pattern == "GET /front/{id}"
    assert response_out.label.pattern_dialect == "go_serve_mux"
    assert response_out.label.api_id == "example.org/b"
```

- [ ] Implement strict UTF-8 line reading and Pydantic dispatch first, then the direct exchange dictionary and last-route/API resolution. Keep source locations beside parsed events, not in emitted JSON.
- [ ] Add failing negative traces by changing the fixture's response reference, kind, sequence, or API binding; require a location-bearing error for each, including the original request location for conflicts. Add double declaration, duplicate final response, response-before-origin, route-to-client, mixed capture/process files, duplicate process files, malformed lines, missing fields, invalid Unicode, and unsupported v1 input.
- [ ] Add positive cases for missing responses, no metadata, API-only metadata after routing, metadata after final response, last unverifiable route, empty path, unknown context/API, empty files/directories, and same local IDs in different processes.
- [ ] Interleave two identical-label requests sharing a context and deliver responses in reverse order. Both must associate exactly, without an ambiguity warning or request search.
- [ ] Rerun `uv run pytest -q tests/test_events.py tests/test_capture.py`. Test reference validation separately from record-shape validation.

### Task 6: shared-context projection, concise diagnostics, consumer handoff

**Files:** `capture.py`, `inspect.py`, package exports/entry point, thin analysis wrappers, Python tests; consumer migration only after its approval.

**Consumes:** resolved occurrences. **Produces:** the pairing function, diagnostic commands, and a bounded downstream adapter.

- [ ] Add the decisive semantic regression:

```python
from pathlib import Path
from contexttrack.capture import read_capture, shared_context_pairs

FIXTURE = Path(__file__).parents[1] / "testdata/v2-chain.jsonl"

def test_chain_has_all_shared_context_pairs():
    capture = read_capture(FIXTURE)
    pairs = shared_context_pairs(capture.occurrences)
    assert {(a.kind, b.kind) for a, b in pairs} == {
        ("receive_request", "send_request"),
        ("receive_request", "send_response"),
        ("receive_response", "send_request"),
        ("receive_response", "send_response"),
    }
    assert shared_context_pairs(reversed(capture.occurrences)) == pairs
```

- [ ] Implement Section 5's small pairing helper. Test repeated labels (`send X, receive Y, send X`), same-exchange receive/send pairs, unknown roots, reused counters across processes/captures, and isolates. No temporal or URL-based edges.
- [ ] Replace local scripts with wrappers; put argparse/rendering in `inspect.py`. Commands are `contexttrack groups INPUT` and `contexttrack graph INPUT --format text|dot`. Remove the legacy `--recv-sent` option and consecutive-edge mode. Group output retains occurrences and prints scoped context/exchange IDs; graphs retain isolated nodes and describe possible influence.
- [ ] Use proper DOT string escaping for quotes, backslashes, Unicode, and newlines. A graph with no edges or nodes must still emit valid `digraph` syntax. Summary counts go to stderr in DOT mode.
- [ ] Export the capture API without the scaffold greeting; define `main() -> None` in `src/contexttrack/inspect.py` and set `[project.scripts]` to `contexttrack = "contexttrack.inspect:main"`. Test both CLI subcommands and Python imports from an installed wheel, not just `PYTHONPATH`.
- [ ] Build the wheel and hand it, generated schema, synthetic fixture, and approved semantic contract to the consumer owner. After separate approval, replace its importer with the Section 5 adapter; add `pattern_dialect`, install the shared dependency, and update tests/its importer contract. Require four nodes/four edges for this fixture and rejection of the v1 fixture by the new reader. Preserve old real captures unchanged.

**Gate C:** Consumer migration runs its focused/full tests in a disposable checkout with the built wheel. Verify the callable importer; do not claim nonexistent build/export/query CLI commands. If the owner has not approved migration or dependency distribution, report end-to-end integration as blocked, not completed.

### Task 7: acceptance captures, documentation, and size audit

**Files:** Go/Python tests, `README.md`, `IMPLEMENTATION.md`, `AGENTS.md`, `OUTPUT.md`; retire the optional patch.

- [ ] Add this black-box manual example in `conftamer_test.go` (imports: `context`, `io`, `net/http`, `net/http/httptest`, `testing`). Its capture must have exactly four message records plus routing/API metadata:

```go
func TestConftamerCaptureExample(t *testing.T) {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
        http.ConftamerSetServerAPI(r, "example.org/items")
        w.WriteHeader(http.StatusOK)
    })
    server := httptest.NewServer(mux)
    t.Cleanup(server.Close)
    ctx := http.ConftamerContext(context.Background())
    req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/items/7", nil)
    if err != nil {
        t.Fatal(err)
    }
    req = http.ConftamerWithClientAPI(req, "example.org/items")
    response, err := http.DefaultClient.Do(req)
    if err != nil {
        t.Fatal(err)
    }
    defer response.Body.Close()
    if _, err := io.Copy(io.Discard, response.Body); err != nil {
        t.Fatal(err)
    }
    if response.StatusCode != http.StatusOK {
        t.Fatalf("status = %d, want 200", response.StatusCode)
    }
}
```

- [ ] Add subprocess captures for parallel exchanges and two child processes sharing one capture directory. Confirm unique process files, contiguous sequences, independent IDs, no races, and identical HTTP workload results with capture disabled.
- [ ] Add a bounded timeout race fixture with channel synchronization and an actual final 503/504 response. It must keep the original exchange/context despite a late handler and metadata. Do not increase an application's timeout to hide an orphan. This is a core regression, **not** proof Kubernetes integration is fixed.
- [ ] Compile test binaries with capture disabled, then enable capture only while running the selected binary. This avoids collecting module-download/build-tool HTTP traffic as application messages. Record base/source versions, exact commands and stderr outside the source tree.
- [ ] Run a fresh small Prometheus `TestTargetScraperScrapeOK` capture in a disposable checkout, using the patched Go binary and a replacement to the disposable patched `common` copy. Start with the recorded Prometheus base commit; if it cannot be reproduced, stop and obtain a reviewed replacement ref. Do not change existing sibling copies or substitute edited v1 records for v2 evidence.
- [ ] Validate each actual v2 line with Pydantic, then run relational integrity and diagnostics. Inspect unknown API/context counts and unverifiable routes. They need not be zero; unannotated client-only Prometheus work may legitimately have unknown contexts. Require one final receive per actual successful attempt, not equality with old line/edge counts.
- [ ] Exercise a larger bounded synthetic/concurrent capture and, with target-run authorization, a larger selected Prometheus package capture. This is not approval for `go test ./...` over external repositories.
- [ ] Update docs and guides to reflect only implemented behavior, including new Python requirement, explicit origins/bindings, v1 incompatibility, generated schema, reader errors, and the coverage matrix below. Delete the optional legacy patch and references to its use.
- [ ] Measure the final source/test counts using Section 8's convention; report deviations and inspect the full diff/untracked files. Run all acceptance checks. Obtain human approval before publishing the new contract or retiring active v1 consumers.

## 8. Current versus projected line counts

Measured on 2026-09-18. These are **physical lines including comments and blanks**, not statements or promises. Go counts are the source lines contributed by instrumentation: lexically extracted `+` lines excluding `+++` headers because the current patch is malformed. Do not count full patched stdlib files or count overlay sources twice inside the generated patch.

| Maintained production area | Current | Projected | Explanation |
| --- | ---: | ---: | --- |
| Go instrumentation helpers + native hook additions | 435 | 500-650 | Currently 379 helper + 56 hook/import lines; more lifecycle/writer correctness, less stack/reflection/debug code |
| Prometheus adapter additions | 5 | 5-10 | Remains one small routing adapter |
| Shared Python models/reader/exports | 2 | 260-360 | Scaffold becomes the single typed contract, integrity checker and resolver |
| Local diagnostics and wrappers | 683 | 140-200 | Replace 31-line reader, 265-line grouping script and 387-line graph script; remove repeated summaries/heuristics |
| Downstream ContextTrack importer | 271 | 80-120 | Exact typed input; no old parser/correlation machinery |
| Downstream PMGraph model file | 32 | 33-36 | Existing file retained; only route-dialect preservation added |
| Go patch application script | 0 | 40-60 | Reproducible version/clean-tree checks |
| **Affected production total** | **1,428** | **1,058-1,436** | Midpoint about **1,247**, roughly **13% smaller**; upper bound is approximately flat |

Additional accounting:

- Current diff-file lengths are **540** main-patch and **15** adapter-patch lines; these are packaging size, not an additional 555 lines of implementation. Record their new lengths separately after regeneration; do not promise a patch size before native diffs exist.
- The historical optional patch is **220** lines. Its deletion is not counted as a reduction in the active producer.
- ContextTrack-specific tests: producer/local **0** checked-in tests today; downstream importer tests **220** lines. Budget **600-900 Go**, **260-380 local Python**, and **200-300 downstream importer test** lines: **1,060-1,580 total**. Existing generic PMGraph tests remain outside this subtotal; extend the relevant ones for dialect preservation.
- Docs, generated JSON Schema, fixtures, lockfiles, dependency/CI configuration, and unmodified downstream functionality are excluded from production totals. They must still be reviewed; moving code into these files does not make it disappear from the size audit.
- Total maintained work including tests will grow. The objective is less heuristic production code and a much smaller downstream importer, not fewer tests or a misleading total reduction.

**Readability guardrails:** one authoritative Python raw-event schema, three raw variants, one exact exchange table, immutable labels, ordinary functions. Keep the Go writer separate from hook/identity helpers; do not split every tiny helper into a new abstraction. If a production area exceeds its upper estimate by >20%, pause for a design review and explain the added requirement. Do not compress statements, remove useful comments, or weaken validation to hit a quota.

Reproduce the baseline counts with:

```bash
wc -l analysis/*.py src/contexttrack/__init__.py \
  ../../conftamer-cli/src/conftamer/contexttrack.py \
  ../../conftamer-cli/src/conftamer/pmgraph/models.py \
  ../../conftamer-cli/tests/test_contexttrack.py
python3 -B - <<'PY'
from pathlib import Path
for name in ('go-inlibrary.patch', 'prometheus-common-route.patch'):
    lines = Path(name).read_text().splitlines()
    additions = [line for line in lines if line.startswith('+') and not line.startswith('+++')]
    print(name, 'diff lines:', len(lines), 'source additions:', len(additions))
PY
```

After execution, count normal overlay/package/script source once and add only native hook/adapter additions; report tests and generated artifacts separately.

## 9. Verification and coverage gates

### Future execution commands

Set `REPO` to the absolute producer ContextTrack checkout and `WORK` to a fresh authorized temporary workspace. Prepare `GO_WORK` from a clean `go1.26.6` checkout, not the applied sibling tree:

```bash
REPO="$PWD"
WORK="$(mktemp -d)"
git clone --depth 1 --branch go1.26.6 https://go.googlesource.com/go "$WORK/go"
GO_WORK="$WORK/go"
bash "$REPO/apply-go-patch.sh" "$GO_WORK"
(cd "$GO_WORK/src" && env -u GOROOT -u CONFTAMER_EVENTS \
  -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local ./make.bash)
PATCHED_GO="$GO_WORK/bin/go"
env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR \
  -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local "$PATCHED_GO" version
env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR \
  -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local "$PATCHED_GO" env GOROOT
```

Then run:

```bash
env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR \
  -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local "$PATCHED_GO" \
  test -count=1 net/http -run '^TestConftamer'
env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR \
  -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local "$PATCHED_GO" \
  test -race -count=1 net/http -run '^TestConftamer'
env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR \
  -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local "$PATCHED_GO" test -count=1 net/http
uv run pytest -q
uvx ruff check .
uvx ty check
uv build --out-dir "$WORK/wheels"
git diff --check
git status --short --branch
```

Manual capture example, after the test exists:

```bash
env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR \
  -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local "$PATCHED_GO" \
  test -c -o "$WORK/http.test" net/http
CAPTURE="$(mktemp -d "$WORK/capture.XXXXXX")"
env -u GOROOT -u CONFTAMER_EVENTS GOTOOLCHAIN=local \
  CONFTAMER_EVENTS_DIR="$CAPTURE" CONFTAMER_CAPTURE_ID="$(basename "$CAPTURE")" \
  "$WORK/http.test" -test.run '^TestConftamerCaptureExample$' -test.count=1 -test.v \
  > "$WORK/capture.stdout" 2> "$WORK/capture.stderr"
uv run contexttrack groups "$CAPTURE"
uv run contexttrack graph "$CAPTURE" --format dot > "$WORK/influence.dot"
# Optional: dot -Tsvg "$WORK/influence.dot" > "$WORK/influence.svg"
```

Inspect stdout/stderr and nonempty expected records. The test must prove H2 negotiation when claiming H2 coverage, not simply use TLS. Validate installed-wheel imports/commands in a new environment. For the separately approved consumer migration, run its actual callable importer tests, full existing suite, formatting/type checks, and dependency-installation checks; do not replace these with a source-tree import smoke test.

### README flaw disposition

| Current concern | Core release disposition / acceptance |
| --- | --- |
| Uncertain logging/API/context placement | Ownership table, explicit API evidence, root policy, immutable exchange IDs and focused regressions |
| Cross-test context correlation | Isolate runtime IDs; aggregate semantic labels across process files of the same capture, never join contexts across runs |
| Broken/machine-specific patch | Clean matching-base parse/apply/build/test evidence required |
| Append-only output and missing directories | Fresh caller-created directory, exclusive per-process file, initialization/failure diagnostics |
| Wrong Go/GOROOT, auto-toolchain, cached tests | Explicit executable and environment; compiled test-binary capture; actual emission checks |
| Duplicate H1/H2 records, interim/repeated/missing implicit headers | Native accepted-transition tests on both protocols |
| Caddy external `x/net/http2` / internal dispatch | **Not covered by this release.** Follow-up must pin Caddy/x-net versions, reproduce a small integration test with `-p 1`, and prove which hooks are bypassed before proposing an adapter. No Caddy source was inspected for this core-only plan. |
| Kubernetes build machinery / broad `./...` failures / etcd | **Integration follow-up.** Use explicit patched toolchain, scoped staging modules; integration packages require recorded prerequisites, including etcd. Do not claim broad Kubernetes coverage. |
| Kubernetes orphan 504 | Core state/timeout regression included, but actual Kubernetes reproduction remains pending. Remove advice to increase timeouts as an instrumentation repair. |
| Stale servers/port collisions | Isolated local test listeners and owned cleanup; document inspection, never kill an arbitrary PID as setup |
| Slow rebuild, mocks/custom transports | Document expected rebuild and precise unsupported boundaries; absence of events is not a success result |
| Local graphs not matching paper semantics | One tested shared-context pairing rule, isolated nodes, no co-occurrence-as-influence mode |

### Checks actually performed while preparing this plan

- Read the current producer/adapter patches, applied hook sites, local analysis scripts, downstream source/tests/contracts, README/implementation notes, and the superseded draft.
- Extracted the paper's relevant text and visually inspected rendered pages 4-5, including Figures 2-3 and the API/context labeling discussion.
- Measured the source/patch counts above; counted kinds/contexts/API omissions in both the small and larger existing captures. No source fixture was rewritten.
- Ran `git apply --numstat go-inlibrary.patch`: **failed**, corrupt patch at line 428. No repair or application attempted.
- Ran both analysis commands' `--help`, and grouping plus `--recv-sent --format dot` on the small and `all-tests.jsonl` real captures. Both completed without malformed-input warnings. Inspected groups and graph summaries: small diagnostic graph 2 nodes/1 edge; large 267 nodes/152 edges. These are legacy diagnostic counts, not v2 acceptance targets or message counts.
- Executed the Pydantic definitions extracted from this document with Pydantic 2.13.5: all five documented fixture records validated, 38 invalid shape/Unicode variants were rejected, and schema generation produced three discriminated alternatives. The documented pairing function passed four-edge, reversed-order, unknown-context and process-isolation probes using synthetic labels. All six Python blocks compiled and all six shell blocks passed `bash -n`; line-count totals and local links were checked. These are design-snippet checks, **not** an implemented shared reader, package, producer, or full validation suite.
- Graphviz `dot` was unavailable; existing DOT output was not rendered. No Go build, stdlib tests, target-module tests, new producer capture, or downstream migration was run.

### Final release gate

- [ ] Producer tests, clean patch application, real v2 emission, exact reader association, and installed-package tests pass with recorded commands and versions.
- [ ] The consumer owner has accepted the semantic change, dialect field, and package dependency; otherwise report migration pending.
- [ ] Reviewers inspect a timeout, unknown context/API, unverifiable rewrite, failed logger, and an order-reversed influence example, not only a happy-path graph.
- [ ] Source/test counts and full diffs are reviewed. No hidden v1 reader, API guess, raw-to-normalized pipeline, graph framework, or unapproved external adapter remains.
- [ ] Documentation distinguishes tested core support from unsupported paths and future work. Results are possible influence conditioned on exercised instrumentation, not proven causality, coverage, or capture completeness.
