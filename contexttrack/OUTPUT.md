# ContextTrack v3 output contract

This is the sole human-readable contract for the version 3 producer, reader,
semantic labels, and possible-influence relation. The strict executable models
are in `src/contexttrack/events.py`. Version 1 and version 2 inputs are rejected;
no compatibility aliases, discarded legacy fields, or converter are provided.

## Capture and failure behavior

Capture is enabled only when both settings exist before process startup:

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
| `schema_version` | strict integer `3` |
| `capture_id` | nonempty string |
| `process_id` | 32 lowercase hexadecimal characters |
| `seq` | strict integer in `1..2^64-1` |
| `exchange_id` | strict integer in `1..2^64-1` |
| `kind` | one of the four message kinds |

Booleans, floats, and strings are not integers. Required nullable fields must be
present as JSON `null`; fields from another variant must be absent.

## Request records

Kinds: `send_request` and `receive_request`.

```text
context_id: strict integer in 1..2^64-1, or null
request: {method: nonempty string, host: nonempty string or null, path: string}
```

A sent request requires a host. A received request may have a null host when no
effective authority exists. The producer snapshots the method, effective HTTP
authority, and raw `URL.Path`; an explicit empty path is valid. Authority
spelling and ports are preserved.

A request declares one exchange. Server ingress creates a fresh context root on
the server-owned request. Client attempts read an inherited root or record null;
they do not mutate caller-owned requests or create implicit roots.

## Response records

Kinds: `send_response` and `receive_response`. The sole variant field is:

```text
status_code: strict integer 101 or 200..999
```

A response refers to its exact request through scoped exchange identity and does
not repeat context or request fields. `receive_response` must reference a
preceding `send_request`; `send_response` must reference a preceding
`receive_request`. At most one final response is allowed per exchange. A request
without a response remains valid.

Ordinary informational responses, bodies, trailers, cancellation, send errors,
and completion have no record kind. An observation is not proof of peer
delivery.

## Identities and exact association

The three identities have separate purposes:

| Identity | Meaning |
| --- | --- |
| `(capture_id, process_id, seq)` | one record and diagnostic location |
| `(capture_id, process_id, exchange_id)` | one server request or client attempt |
| `(capture_id, process_id, context_id)` | one known inherited context root |

Counters may repeat across processes and captures. They are not semantic graph
nodes, transmitted trace headers, operating-system PIDs, goroutine IDs, heap
addresses, or stable cross-run identifiers. Client and server IDs are not
forced to match for network correlation.

The reader keeps one exchange table. Each request occurrence is resolved and
appended immediately; a response copies the exact origin's context and request
label fields and adds response kind and status. There is no URL, FIFO, context,
chronology, stack, or nearest-record search for an origin.

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

Labels contain no capture, process, exchange, context, sequence, line, module,
API, or route identity. Equal labels can therefore deduplicate while their
runtime occurrences remain distinct.

The retained immutable library values are:

```text
Occurrence(label, context_key, exchange_key, seq, location)
Capture(occurrences: tuple[Occurrence, ...])
read_capture(path: str | Path) -> Capture
shared_context_pairs(occurrences) -> set[(MessageLabel, MessageLabel)]
```

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
6. preceding response origins and correct response direction; and
7. at most one final response per exchange.

Empty files and directories, requests without responses, null contexts, and
reused counters in different processes are valid. Passing validation proves
internal consistency of consumed records, not capture completeness, workload
purity, or exercised coverage.

## Possible influence

For every known scoped context `c`, collect received and sent semantic labels:

```text
R(c) = {receive_request, receive_response}
S(c) = {send_request, send_response}
E = union over c of R(c) x S(c)
```

The relation is order-independent. It has no chronology filter, URL matching,
or same-exchange exclusion. A received response may therefore point to the
earlier request send in its context. This overapproximates possible influence;
it is not temporal causality.

Null contexts never group. Occurrences are retained through association, then
final labels and edges are deduplicated. Isolated labels remain visible.

`testdata/v3-chain.jsonl` contains four message occurrences in one known
context: a received server request, a sent downstream request, its received
response, and the server's sent response. Both receives pair with both sends,
producing exactly four edges. The unchanged v2 fixture must be rejected.

## Schema and coverage limits

`event.schema.json` is generated with `EVENT_ADAPTER.json_schema()`. JSON Schema
does not express every Pydantic after-validator or any cross-record integrity
rule. Python consumers should use `EVENT_ADAPTER` and `read_capture`.

The producer observes selected standard-library HTTP/1 transitions only.
External HTTP/2, custom transports, mocks, pre-dispatch failures, tunnels, and
bodies may bypass it. API ownership and route patterns were deliberately
removed. A clean diagnostic and valid records still do not prove completeness.
