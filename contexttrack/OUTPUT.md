# ContextTrack v4 output contract

This is the sole human-readable contract for version 4 records, the strict
Python reader, semantic labels, and recorded influence relation. The executable
record models are in `src/contexttrack/events.py`. Version 1, version 2, and
version 3 inputs are rejected; no compatibility aliases, discarded legacy
fields, or converter are provided.

The patched Go producer emits this format and the Python package reads it.
That implementation state is not itself producer evidence: reproducibility,
emission, reader integrity, and end-to-end acceptance require fresh execution
of their separate verification gates.

## Capture and failure behavior

The v4 producer uses a checked synchronous logger. Capture is enabled only when
both settings exist before process startup:

```text
CONFTAMER_EVENTS_DIR=/absolute/path/to/an/existing-directory
CONFTAMER_CAPTURE_ID=nonempty-capture-name
```

Both absent means disabled. Partial or invalid configuration disables capture
with a diagnostic. Any set legacy `CONFTAMER_EVENTS` is an error.

Each enabled process generates a random 128-bit identity, rendered as 32
lowercase hexadecimal characters, and exclusively creates
`<process_id>.jsonl` with mode `0600`. Files are not appended or reopened.

One process-local mutex covers validation, sequence assignment, JSON encoding,
and a checked complete-line write. Successful sequences start at 1 and are
contiguous. The first validation, marshal, write, or short-write failure is
diagnosed once and permanently stops that logger. Capture has no queue,
completion record, or per-event `fsync`; a valid prefix may be incomplete.
Synchronous logging can affect timing.

Bundled HTTP/2 entry on either client or server calls the same failure path with
`non-HTTP/1 capture is unsupported`. It does not reject the HTTP operation or
force HTTP/1. Any such diagnostic invalidates that process capture.

## Encoding and envelope

A process file contains UTF-8 JSON Lines. Every nonblank physical line is one
object. Unknown fields, kinds, and schema versions are invalid. Invalid UTF-8
and escaped surrogate code points are rejected rather than repaired.

Every record has exactly this envelope plus its variant fields:

| Field | Contract |
| --- | --- |
| `schema_version` | strict integer `4` |
| `capture_id` | nonempty string |
| `process_id` | 32 lowercase hexadecimal characters |
| `seq` | strict integer in `1..2^64-1` |
| `exchange_id` | strict integer in `1..2^64-1` |
| `kind` | one of the four message kinds |
| `sources` | required array of scoped source-receive sequence numbers |

Booleans, floats, and strings are not integers. `sources` entries are strict
integers in `1..2^64-1`, sorted, and unique. Every source must precede its
target and identify a received message in the same capture process. Receives
have `sources: []`. Unknown fields and fields from another variant must be
absent. The v3 `context_id` field is removed.

A source number is shorthand for
`(target.capture_id, target.process_id, source_seq)`. It is not resolved across
process files or captures.

## Request records

Kinds: `send_request` and `receive_request`.

```text
request: {method: nonempty string, host: nonempty string or null, path: string}
```

A sent request requires a host. A received request may have a null host when no
effective authority exists. The producer snapshots the method, effective HTTP
authority, and raw `URL.Path`; an explicit empty path is valid. Authority
spelling and ports are preserved.

A request declares one exchange. A sent request may have an empty source list;
this means its influence is unknown or undeclared, not that it is independent.

## Response records

Kinds: `send_response` and `receive_response`. The response variant adds:

```text
status_code: strict integer 101 or 200..999
```

A response refers to its exact request through scoped exchange identity and
does not repeat request fields. `receive_response` must reference a preceding
`send_request`; `send_response` must reference a preceding `receive_request`.
At most one final response is allowed per exchange. A request without a
response remains valid.

A received response has `sources: []`. A sent server response must include its
own request receive sequence; this is the automatic exact request/reply
relationship. It may include other declared receive sources. Dependencies that
affect only body output after final headers have been recorded are outside this
contract; source declarations are not retroactive.

Ordinary informational responses, bodies, trailers, cancellation, send errors,
and completion have no record kind. An observation is not proof of peer
delivery.

## Source annotation API

The producer adds exactly these annotation names to `net/http`:

```go
type ConftamerSource interface {
    conftamerReceiveSeq() uint64
}

func ConftamerWithSources(
    req *Request, sources ...ConftamerSource,
) *Request

func ConftamerSetReplySources(
    req *Request, sources ...ConftamerSource,
)
```

Only `*Request` and `*Response` implement `ConftamerSource`. A valid source is a
request observed at server ingress or a response observed at client receipt by
the enabled process capture. Callers cannot supply sequence numbers directly.
Sources are copied, sorted, deduplicated, and recorded by their process-local
receive sequence.

`ConftamerWithSources` returns a shallow copy of `req` whose declaration
replaces the complete source list for subsequent client attempts. It does not
modify the original request or its context. Retries retain that declaration.
Redirect-created requests do not inherit a new declaration automatically; code
that treats a redirect response as a source must annotate the redirected
request explicitly.

`ConftamerSetReplySources` targets the server exchange already attached to a
received request. Each call replaces the complete list of additional reply
sources. The request's own receive sequence remains an automatic source and is
added when the final response freezes. Informational headers do not freeze the
list. The first terminal header does, so a later declaration fails capture once
without changing the HTTP operation. Dependencies affecting only later body
output cannot be declared retroactively.

When capture is disabled or stopped, both helpers are no-ops and
`ConftamerWithSources` returns its input unchanged. With capture enabled, a nil
request target, an unobserved or nil source, an invalid reply target, or a late
reply declaration fails capture rather than silently dropping or inventing a
reference.

Declarations are reviewed application assertions. Validation proves that each
reference names a preceding receive in the process; it cannot prove that the
application actually used that receive as data or control input.

## Identities and exact association

The two scoped identities have separate purposes:

| Identity | Meaning |
| --- | --- |
| `(capture_id, process_id, seq)` | one record, source reference, and diagnostic location |
| `(capture_id, process_id, exchange_id)` | one server request or client attempt |

Counters may repeat across processes and captures. They are not semantic graph
nodes, transmitted trace headers, operating-system PIDs, goroutine IDs, heap
addresses, or stable cross-run identifiers. Client and server IDs are not
forced to match for network correlation.

The reader keeps exact record and exchange tables. Each request occurrence is
resolved and appended immediately; a response copies the exact exchange
origin's request-label fields and adds response kind and status. Each source is
resolved only against preceding receive occurrences in its process. There is no
URL, FIFO, context, chronology, stack, transitive, or nearest-record search.

## Semantic labels and occurrences

`MessageLabel` is strict, frozen, and hashable, with exactly five fields:

```text
kind, method, host, path, status_code
```

- `kind` is one of the four message kinds.
- Client labels (`send_request`, `receive_response`) require a host.
- Server labels (`receive_request`, `send_response`) have a null host.
- Request labels have a null status; response labels have a terminal status.
- An explicitly empty raw path becomes `/`; no other normalization occurs.

Labels contain no capture, process, exchange, source, sequence, line, module,
API, or route identity. Equal labels can therefore deduplicate while their
runtime occurrences remain distinct.

The retained immutable library values are:

```text
RecordKey = (capture_id, process_id, seq)
ExchangeKey = (capture_id, process_id, exchange_id)
Occurrence(label, record_key, exchange_key, source_keys, location)
Capture(occurrences: tuple[Occurrence, ...])
read_capture(path: str | Path) -> Capture
influence_edges(capture)
    -> dict[(MessageLabel, MessageLabel), (Occurrence, Occurrence)]
```

`source_keys` and `occurrences` are tuples. `read_capture` preserves every
occurrence and every resolved source reference. `influence_edges` associates
occurrences first, then maps each semantic label pair to one deterministic exact
source/target witness for display. Equal label pairs can share one displayed
edge without erasing the retained occurrences.

## Reader integrity

`read_capture` accepts one process file or a directory of `*.jsonl` files.
Directory filenames are sorted for deterministic presentation, not chronology.
The reader decodes binary physical lines as strict UTF-8, ignores blank lines
without renumbering locations, and validates each nonblank record exactly once.
Shape errors include `path:physical-line`; filesystem errors are preserved.
Records are never warned-and-skipped or repaired.

For nonempty input it enforces:

1. one capture and process identity per process file;
2. one capture ID across a directory;
3. one file per process identity;
4. sequences contiguous from 1;
5. unique request declarations;
6. preceding response origins and correct response direction;
7. at most one final response per exchange;
8. sorted, unique source counters resolving to preceding receives in the same
   process; and
9. inclusion of the exact incoming request in every server reply's sources.

Empty files and directories, requests without responses, empty send source
lists, and reused counters in different processes are valid. Passing validation
proves internal consistency of consumed records, not capture completeness,
workload purity, correct annotation placement, or exercised coverage.

## Recorded influence

An edge exists only when a send record names a particular preceding receive in
its `sources` array. The edge is from that exact receive occurrence to that
exact send occurrence. The incoming request to its own server response is the
one automatic source; other data or control influences require declarations at
the outgoing operation.

No edge is created from shared context, logging order, URL equality, exchange
proximity, or inferred transitivity. Receiving a response does not inherit the
sources of the corresponding request. An empty source list means influence is
unknown or undeclared, never proven independence.

`testdata/v4-chain.jsonl` contains:

| Sequence | Message | Sources |
| ---: | --- | --- |
| 1 | received incoming request A | `[]` |
| 2 | sent downstream request B | `[1]` |
| 3 | received downstream response C | `[]` |
| 4 | sent server reply D | `[1, 3]` |

It produces exactly **A -> B**, **A -> D**, and **C -> D**. The first is a
declared edge, the second is the automatic request/reply edge, and the third is
a declared edge. The unchanged v2 and v3 fixtures are rejection fixtures.

## Diagnostic output

`contexttrack INPUT` renders one node per distinct semantic label and one edge
per distinct label pair. Every edge includes a representative source and target
record location and is marked `request/reply` or `declared`. Isolated labels
remain visible. The summary reports `sends_without_sources`; it does not call
those sends independent.

## Schema and coverage limits

`event.schema.json` is generated with `EVENT_ADAPTER.json_schema()`. JSON Schema
does not express every Pydantic after-validator or any cross-record integrity
rule. Python consumers should use `EVENT_ADAPTER` and `read_capture`.

The graph remains limited to selected standard-library HTTP/1 observation
boundaries. External HTTP/2, custom transports, mocks, pre-dispatch failures,
tunnels, and bodies may bypass it. API ownership and route patterns are absent,
and application or downstream-consumer migration is outside this contract. A
clean diagnostic and valid records still do not prove completeness. Declared
sources are reviewed application assertions; structural validation cannot prove
that application code actually used an input.
