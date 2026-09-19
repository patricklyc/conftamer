# ContextTrack v2 output contract

This document describes the executable v2 record contract in
`src/contexttrack/events.py`. `event.schema.json` is generated from
`EVENT_ADAPTER`; it is an interoperability artifact, not a second source of
truth. Task 1 defines and tests the format but does not update the Go producer
to emit it.

## Encoding and record variants

A capture is UTF-8 JSON Lines with one complete object per nonblank physical
line. Unknown fields, kinds, and schema versions are invalid. Invalid UTF-8 and
escaped surrogate code points are invalid rather than repaired.

Every record has these fields:

| Field | Contract |
| --- | --- |
| `schema_version` | the integer `2` (not `2.0`, a string, or a boolean) |
| `capture_id` | nonempty string |
| `process_id` | 32 lowercase hexadecimal characters |
| `seq` | positive uint64 record sequence |
| `exchange_id` | positive uint64 request/attempt identity, scoped by capture and process |
| `kind` | one of the five kinds below |

Counters are strict integers from 1 through `2^64 - 1`. Required nullable
fields must be present with an explicit JSON `null` when their value is
unknown. Fields belonging to another variant must be absent.

### Request records

Kinds: `send_request` and `receive_request`.

Additional fields:

```text
context_id: positive uint64 or null
request: {method: nonempty string, host: nonempty string or null, path: string}
api_id: nonempty string or null
```

A `send_request` requires a non-null host. An explicitly empty path is valid.

### Response records

Kinds: `send_response` and `receive_response`.

The only additional field is `status_code`, a strict integer. It must be the
terminal status `101` or be in the range `200..999`. Responses refer to their
request through the envelope's exact `exchange_id`; they do not repeat a
request snapshot.

### Metadata records

Kind: `request_metadata`.

Both additional fields are required and nullable, but at least one must be
non-null:

```text
route: {dialect, pattern, matched_path, full_pattern} or null
api_id: nonempty string or null
```

A route dialect is exactly `go_serve_mux`, `go_serve_mux_121`, or
`httprouter`. `pattern` is nonempty, `matched_path` may be empty, and
`full_pattern` is a nonempty string or null. Metadata describes a server
exchange and is not a semantic message.

## Semantic message labels

`MessageLabel` is the immutable, hashable semantic key. Its fields are:

```text
kind, api_id, method, host, path, pattern, pattern_dialect, status_code
```

Client messages (`send_request`, `receive_response`) have a host and path and
no route pattern. Server messages (`receive_request`, `send_response`) have no
host and have exactly one of a concrete path or a pattern paired with its
route dialect. Requests have null status; responses have a terminal status.
Different hosts, kinds, route dialects, and concrete-path versus pattern
endpoints remain different keys.

## Cross-record invariants

JSON Schema cannot express all Pydantic after-validator rules, including the
terminal-status rule, nonempty metadata, client-host requirement, and
`MessageLabel` endpoint combinations. It also cannot establish cross-record
integrity such as contiguous sequences, unique exchange declarations, valid
response/metadata references, or consistent API bindings. Python consumers
must validate lines with `EVENT_ADAPTER`; the shared reader added in a later
task will enforce cross-record relationships.

## Synthetic chain fixture

`testdata/v2-chain.jsonl` is the auditable five-record example:

1. receive server exchange 1 in context 7;
2. attach its ServeMux route and API binding;
3. send client exchange 2 in the same context;
4. receive exchange 2's final response; and
5. send exchange 1's final response.

The fixture contains four message occurrences. The metadata record is not a
fifth message. Both responses associate through `exchange_id`; no context,
path, URL, or ordering heuristic is needed.
