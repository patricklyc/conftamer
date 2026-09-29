# Normalized ContextTrack event contract

This document defines the public **normalized event schema, version 1**, implemented
in [`src/contexttrack/models.py`](src/contexttrack/models.py). It is distinct from
the unversioned raw Go capture described in the [README](README.md).

**Current scope (Tasks 1–2):** typed models, validation, serialization, a typing
marker, and pure raw-record normalization. JSONL readers/writers, the
normalization CLI, the generated schema snapshot, and the package release update
are later tasks and are not implemented yet. Go instrumentation and raw captures
are unchanged. The existing `analysis/` scripts and `conftamer-cli/node-query`
importer still require raw input, not these models' normalized JSON.

## Purpose and boundaries

Events retain message/context evidence. They are neither a PMGraph nor an
AppGraph and do not establish exact per-request causality. In the
[HotNets design](../../ConfTamer_HotNets_2026.pdf), a per-module PMGraph relates
inputs (parameters and received messages) to outputs (sent messages); a separate
composition step builds an AppGraph. Those steps, parameter ingestion, and
cross-module stitching are outside this API.

A routing observation is its own event, not a message node. Responses are not
matched to requests or enriched with host, API ID, handler, or route pattern by
the models. Missing labels are accepted as missing evidence, not automatically
made into usable downstream message nodes. Repeated hooks remain separate
observations; validation does not deduplicate or order them.

## Format and validation

- Each normalized event is a JSON object with required integer
  `schema_version: 1`. This is the normalized format version, not a patch,
  package, or historical experimental format version.
- `kind` selects one of the five concrete models below. Unknown kinds,
  versions, or fields are errors, including unknown nested fields. Fields
  belonging to another payload type are forbidden even when their value is
  null. Schema additions require an explicitly supported version.
- All wire models use Pydantic's
  `ConfigDict(strict=True, extra="forbid", frozen=True, revalidate_instances="always")`.
  Ordinary assignment to an event or nested model is rejected. Adapter
  validation also revalidates model instances, including values created by
  unchecked Pydantic construction/copy helpers; validate before serialization.
- Types are not coerced: boolean/string/float values are not integers, and
  non-string values are not strings. A dedicated version validator rejects
  `true`, `1.0`, and `"1"` as schema versions; strict `Literal[1]` alone is not
  sufficient on Pydantic 2.13.5.
- Nullable fields default to `None` when omitted and serialize as explicit
  JSON nulls. Required envelope fields and the `message` object have no defaults.
  In particular, `context` must be an object, not null; `{}` represents absent
  context evidence. An empty `message` object is valid incomplete evidence.
- A present `path` must be a nonempty string. The models reject `""`; they do
  not normalize it to `"/"`. Null means unavailable evidence. Other strings may
  be empty, and strings are preserved without trimming, case conversion, URL
  decoding, query stripping, or route-syntax conversion.
- Response `status_code` is null or a strict nonnegative integer. Values such
  as `0`, `200`, and `999` are accepted; there is no 100-599 restriction. Raw
  status-string conversion is not performed by these models.

The intended normalized file format is UTF-8 JSONL, with one event object and a
newline per record. The model adapter parses a single event, not a JSONL file;
encoding, duplicate-key checks, physical-line diagnostics, and safe file
publication belong to the later I/O task.

## Envelope

Every concrete event carries these fields, plus its required typed `message`:

| Field | Type | Required / default | Meaning |
| --- | --- | --- | --- |
| `schema_version` | Exact integer `1` | Required | Normalized format version |
| `kind` | Canonical string literal | Required | Event discriminator |
| `pid` | Strict integer | Required | Producer process ID |
| `context` | `ContextInfo` object | Required | Context evidence and diagnostics |
| `api_id` | String or null | Null | Best-effort package/API association, not a PMGraph `module_id` |
| `handler` | String or null | Null | Handler evidence, when present |
| `goroutine_id` | Strict integer or null | Null | Optional Go debug metadata |
| `thread_id` | Strict integer or null | Null | Optional debug metadata; zero is preserved |
| `file` | String or null | Null | Source/debug path |
| `line` | Strict integer or null | Null | Source/debug line |

No positivity restriction is imposed on `pid`, `goroutine_id`, `thread_id`, or
`line`. Null and zero remain distinct. Reader locations and capture/occurrence
IDs are not added to this envelope.

`ContextInfo` has four nullable string fields, each defaulting to null:
`context_id`, `source`, `type`, and `error`. It preserves context diagnostics;
there is no historical `root_addr` field in the normalized contract.

### Context identity

All events expose the non-serialized property:

```python
context_key: tuple[int, str] | None
```

It returns `(pid, context.context_id)` only for a nonempty context ID. An omitted,
null, or empty ID returns `None`; unrelated records must not share an invented
"unknown" group. IDs are process-local influence heuristics within one capture,
not globally unique distributed trace IDs or exact HTTP exchange identities.
Do not group by `context_id` alone or infer cross-run identity from a reused
append-only raw file. Whitespace in an ID is preserved, not treated as empty.

## Typed payloads and event variants

`RequestFields` supplies nullable strings `method` and `path`. Payload models
inherit these fields, with the additional nullable fields listed here:

| Payload model | Additional fields |
| --- | --- |
| `RequestMessage(RequestFields)` | `raw_query: str \| None` |
| `SentRequestMessage(RequestMessage)` | `host: str \| None` |
| `RoutedRequestMessage(RequestFields)` | `pattern: str \| None` |
| `ResponseMessage(RequestFields)` | `status_code: int \| None` (strict, nonnegative) |

`raw_query=None` is missing evidence; `raw_query=""` is an explicitly empty query.
`pattern` retains the handler syntax separately from the concrete `path`;
examples include `GET /items/{id}`, `:name`, and `*path`.

| Concrete event | Required `kind` | Required `message` type |
| --- | --- | --- |
| `RequestSent` | `send_request` | `SentRequestMessage` |
| `RequestReceived` | `receive_request` | `RequestMessage` |
| `RequestRouted` | `request_routed` | `RoutedRequestMessage` |
| `ResponseSent` | `send_response` | `ResponseMessage` |
| `ResponseReceived` | `receive_response` | `ResponseMessage` |

Requests reject `status_code`; responses reject `host`, `raw_query`, and
`pattern`. Routing rejects request-query and host fields. No event carries a
wire `request_id`: outbound method/host/path are endpoint labels in the typed
message, not occurrence or correlation IDs. The normalizer uses the raw
`request_id` as the sole source of those outbound endpoint labels.

## Raw-record normalization

[`contexttrack.normalize.normalize_record(record: object) -> Event`](src/contexttrack/normalize.py)
validates and converts **one current unversioned `contexttrack-fix-v1` raw
record** into an immutable normalized v1 event. The same raw format is retained
by the overlay tooling. This API is not an importer for historical root-address
or v2/v3/v4/v5 experiments, and already-normalized records are not raw input.

### Mapping

| Raw kind | Canonical kind | Method/path source | Other payload evidence |
| --- | --- | --- | --- |
| `Request sent` | `send_request` | `request_id.method` / `request_id.path` | `request_id.host` and `message.req.URL.RawQuery` |
| `Request received` | `receive_request` | `message.req.Method` / `message.req.URL.Path` | `message.req.URL.RawQuery` |
| `Request routed` | `request_routed` | `message.req.Method` / `message.req.URL.Path` | `message.pattern`, unchanged |
| `Response sent` | `send_response` | `message.req.Method` / `message.req.URL.Path` | `message.code` converted to `status_code` |
| `Response received` | `receive_response` | `message.req.Method` / `message.req.URL.Path` | `message.resp.StatusCode` converted to `status_code` |

- **Outbound-label precedence:** method, host, and path come only from
  `request_id`, even when duplicate labels in the raw `message` disagree.
  Missing/null/empty-object `request_id`, or omitted fields within it, leave
  those labels null; the normalizer never falls back to `message`. Duplicate
  raw message fields still undergo strict validation. Keep the untouched raw
  capture for auditing disagreements. `request_id` is an endpoint label, not an
  occurrence ID, and is not serialized in the normalized envelope.
- A present empty path `""` becomes `"/"` for every kind. An omitted path
  stays null. Empty methods/hosts are preserved rather than given defaults.
- Request query strings retain Unicode and the distinction between omitted
  (null) and explicitly empty (`""`). They come from the raw message, not
  `request_id`.
- A present response status must be a string satisfying `str.isdecimal()`;
  conversion uses `int()`. Leading zeros are accepted (`"0200"` becomes `200`),
  as are Unicode decimal digits. Numeric JSON values, booleans, empty strings,
  signs, whitespace, decimal points, and nondecimal strings are rejected.
  There is no 100-599 restriction: `"0"`, informational `"103"`, and
  nonstandard `"999"` are accepted. Omitted statuses remain null.
- `pid`, `api_id`, `handler`, `goroutine_id`, `thread_id`, `file`, and `line`
  retain their values; missing optional fields become null. Zero debug values
  stay zero. Context preserves all four known diagnostic fields. An omitted or
  null context object becomes an all-null `ContextInfo`, without fabricating
  identity.
- No other values are normalized: methods are not uppercased, hosts are not
  lowercased, strings/API IDs are not trimmed, and URLs/paths/query strings
  are not decoded, cleaned, or stripped. Route syntax is preserved separately
  from the concrete path, not reconstructed.

### Strict raw boundary

Private Pydantic models in [`_raw.py`](src/contexttrack/_raw.py) use strict types
and forbid unknown fields at every level. Raw `kind`, integer `pid`, and object
`message` are required. `message={}` is valid incomplete evidence; a missing,
null, or non-object message is invalid. Context and request-label objects, API
IDs, and optional debug metadata may be omitted/null. Existing PID/debug integer
fields have no new positivity restrictions.

Known message and `request_id` fields must be **strings when explicitly
present**; explicit null is invalid, even for unused duplicate outbound labels.
Omission is allowed and becomes null evidence. Context fields and optional
envelope metadata, by contrast, accept explicit null with their declared types.

Only these raw message keys are allowed for each kind:

| Raw kind | Allowed `message` keys |
| --- | --- |
| `Request sent` | `req.Method`, `req.URL.Host`, `req.URL.Path`, `req.URL.RawQuery` |
| `Request received` | `req.Method`, `req.URL.Path`, `req.URL.RawQuery` |
| `Request routed` | `req.Method`, `req.URL.Path`, `pattern` |
| `Response sent` | `req.Method`, `req.URL.Path`, `code` |
| `Response received` | `req.Method`, `req.URL.Path`, `resp.StatusCode` |

`request_id` permits only `method`, `host`, and `path`. A non-null `request_id`
is legal only on `Request sent`; even `{}` is forbidden on other kinds. Null
is permitted on all five kinds. Dotted message keys are validation aliases,
not alternate names: canonical/internal spellings such as `method`, `path`,
and `status_code` are not accepted as raw message keys. `context.root_addr`
and unknown raw kinds/envelope/nested keys fail explicitly; they are not
silently ignored. This boundary is intentionally stricter than the diagnostic
scripts and the legacy `node-query` importer's tolerance of some unconsumed
fields.

Each accepted raw record yields exactly one typed event. Conversion does not
mutate the input, read/write files, warn, cache requests, infer associations,
filter, deduplicate, or sort records. Repeated hooks and incomplete observations
remain evidence. Responses are not enriched from other records, and no API ID,
handler, host, route pattern, or module ownership is invented. Downstream code
still decides which evidence can become PMGraph nodes and how to associate it.

Malformed raw shapes raise Pydantic `ValidationError` (a `ValueError` subclass);
invalid decimal status strings raise `ValueError`. The final public
`EVENT_ADAPTER` validates the constructed canonical payload. Physical file/line
error wrapping is part of the later I/O task, not this record-only API.

## Public Python API

`contexttrack.models` and the package root export `ContextInfo`, `RequestFields`,
`RequestMessage`, `SentRequestMessage`, `RoutedRequestMessage`, `ResponseMessage`,
all five concrete event classes, `Event`, and `EVENT_ADAPTER`.

`Event` is an `Annotated` discriminated union, not a class with
`model_validate_json`. Use `EVENT_ADAPTER` or a concrete event model:

```python
from contexttrack.models import EVENT_ADAPTER, RequestSent

record = {
    "schema_version": 1,
    "kind": "send_request",
    "pid": 42,
    "context": {"context_id": "id:7"},
    "message": {"method": "gEt", "host": "Höst:80", "path": "/", "raw_query": ""},
}
event = EVENT_ADAPTER.validate_python(record)
assert isinstance(event, RequestSent)
assert event.context_key == (42, "id:7")

text = event.model_dump_json(by_alias=False, exclude_none=False, ensure_ascii=False)
assert EVENT_ADAPTER.validate_json(text) == event
```

For raw input, import the record normalizer; no graph code or other records are
needed, even for an incomplete response:

```python
from contexttrack.models import ResponseReceived
from contexttrack.normalize import normalize_record

event = normalize_record({
    "kind": "Response received",
    "pid": 42,
    "message": {"resp.StatusCode": "0200"},
})
assert isinstance(event, ResponseReceived)
assert event.message.status_code == 200
assert event.message.path is None
assert event.api_id is None
assert event.context_key is None
```

Serialization includes all nullable fields as explicit nulls, preserves Unicode,
and excludes `context_key`. Add a newline when writing an individual JSONL record.
`EVENT_ADAPTER.json_schema()` provides the schema derived from these same models;
there is no independently maintained normalized validator.

Imports do not read capture environment variables, emit diagnostics, open
captures, import graph code, or require Go or the consumer repository. The
package includes `py.typed` and requires Python >=3.14. Models and record
normalization are tested on Python 3.14.7 with Pydantic 2.13.5. Python/Pydantic,
the Go toolchain used to produce raw evidence, and uv/build/test tooling are
external trust boundaries; these tests do not audit their third-party source.

Query strings, context diagnostics, and source paths may contain sensitive
information. Model validation is not redaction or proof of capture completeness.
Complete-project human audit and sign-off remain pending; automated model tests
are not a substitute for that review.
