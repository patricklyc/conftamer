# Normalized ContextTrack event contract

This document defines the public **normalized event schema, version 1**, implemented
in [`src/contexttrack/models.py`](src/contexttrack/models.py). It is distinct from
the unversioned raw Go capture described in the [README](README.md).

**Current scope (Tasks 1–4):** typed models, validation, serialization, a typing
marker, pure raw-record normalization, strict streaming JSONL readers/writers,
normalization/schema CLIs, and package version `0.2.0`. The generated schema
snapshot and full producer/distribution acceptance remain Task 5. Go
instrumentation and raw captures are unchanged. The existing `analysis/` scripts
and `conftamer-cli/node-query` importer still require raw input, not these models'
normalized JSON.

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

The normalized file format is UTF-8 JSONL, with one event object and a newline
per serialized record. The model adapter parses a single event, not a JSONL file;
encoding, duplicate-key checks, physical-line diagnostics, and safe file
publication are provided by [`contexttrack.io`](src/contexttrack/io.py). Direct
`EVENT_ADAPTER.validate_json` validates model shape but does not provide those
additional file-level checks.

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
`EVENT_ADAPTER` validates the constructed canonical payload. The record-only API
has no file location; the JSONL readers wrap these failures at their physical
input line as described below.

## Streaming JSONL I/O

The public interfaces in [`contexttrack.io`](src/contexttrack/io.py) accept
`str` or `pathlib.Path` file paths:

```text
iter_raw_events(path: str | Path) -> Iterator[LocatedEvent]
iter_events(path: str | Path) -> Iterator[LocatedEvent]
write_events(events: Iterable[Event], output: str | Path) -> int
normalize_file(source: str | Path, output: str | Path) -> int
```

### Readers and locations

- `iter_raw_events` lazily reads a **completed raw capture** and applies
  `normalize_record` to each record in memory. It writes nothing and does not
  modify the capture. There is no tailing or capture-runner behavior; stop capture
  before reading. Normalization cannot detect mixed runs or prove completeness.
- `iter_events` lazily reads **normalized v1 only**, validating through
  `EVENT_ADAPTER`. Readers do not auto-detect or mix formats: raw input to
  `iter_events`, or normalized input to `iter_raw_events`, is an error.
- Both readers share one byte-line JSONL parser. Each physical line is decoded
  with strict UTF-8, so even decoding failures have the correct line number. Blank
  lines are skipped; empty/blank-only files and valid final records without a
  trailing newline are accepted.
- Malformed JSON, non-object records, a UTF-8 BOM, non-finite constants such as
  `NaN`/`Infinity`, and duplicate keys at any object depth are rejected. Model
  types/fields/versions are validated by the appropriate Pydantic adapter, not
  a second handwritten normalized schema.
- Records stream in file order, retaining incomplete observations and repeated
  hooks. No association, enrichment, filtering, deduplication, or sorting occurs;
  file order is not asserted to be a global causal order.

Each reader yields a frozen `LocatedEvent` dataclass with `event: Event`,
`path: Path`, and `line: int`. Its `location` property is `f"{path}:{line}"`.
The line is the one-based **physical input line**, including skipped blanks in
its count, not the event's optional Go source/debug `line`. Locations name the
file actually read and are never serialized into the captured event. For the
same capture, raw reading and normalization followed by normalized reading yield
equal events in equal order; their paths/physical lines can differ.

`EventFileError(ValueError)` stores `path`, `line`, and `reason`; its string begins
with `path:line:`. JSON, UTF-8, raw normalization, and Pydantic input failures are
wrapped with the original exception as `__cause__`. Filesystem failures remain
`OSError` subclasses; programming errors are not treated as malformed records.
A reader may already have yielded valid earlier events when a later line fails.

### Writer and file normalization

`write_events` validates every outgoing value through `EVENT_ADAPTER`, including
model instances created with unchecked Pydantic construction/copy helpers. It
then serializes the validated model with
`model_dump_json(by_alias=False, exclude_none=False, ensure_ascii=False)` and one
final newline. Output contains explicit nulls and literal UTF-8 Unicode, not raw
dict dumping or reader location metadata. Memory use is bounded per record, not
per capture. The return value counts events, including repeated hooks.

Output must be a **new file in an existing directory**. Existing files,
directories, and symlinks (including dangling symlinks) are never overwritten or
followed for writing. Output equal to the input is rejected. No append, overwrite,
parent-directory creation, or raw-file rewrite mode is provided.

The writer uses a temporary file on the output filesystem, validates and writes
the entire stream, closes it, then publishes with `os.link(temp_path,
output_path)`. This is atomic no-clobber publication: an existence precheck is
only an optimization, and a destination created by another writer before the
link is preserved. The temporary name is removed on success or failure. Decode,
validation, serialization, write, or close failure before publication leaves no
output file, rather than a success-looking prefix. If hard links are unsupported,
the filesystem error is reported; there is no `os.replace` fallback that could
overwrite user data. These guarantees assume a supported local filesystem and
are not crash-durability or network-filesystem guarantees.

`normalize_file` is exactly `write_events((record.event for record in
iter_raw_events(source)), output)`: it has no separate normalization or error
wrapping logic. Empty input publishes a valid empty output with count zero;
that is not evidence of useful instrumented traffic.

## Command line

```text
contexttrack normalize INPUT --output NEW_OUTPUT
contexttrack schema
python -m contexttrack normalize INPUT --output NEW_OUTPUT
```

The installed console command and module command share
[`contexttrack.cli.main(argv: Sequence[str] | None = None) -> int`](src/contexttrack/cli.py).
The CLI delegates normalization to `normalize_file`; it has no separate field
mapping, validation, or request-association logic. `normalize` accepts completed
**raw captures only**, never normalized data or mixed/auto-detected formats.
There are no default paths, append/overwrite flag, lenient/skip-bad option, or
stdout-output mode. The existing-directory, new-file and atomic-publication
rules above apply unchanged.

Successful normalization returns 0 with empty stdout/stderr. Expected input or
filesystem failures return 2 with a useful stderr diagnostic and no traceback;
input diagnostics retain the raw path and physical line. Argument errors use
argparse's stderr diagnostics and exit 2. Help exits 0.

`schema` prints `EVENT_ADAPTER.json_schema()` as sorted, two-space-indented UTF-8
JSON with a final newline and no other stdout content. It works from the
installed package without a checkout or schema snapshot. Package version
`0.2.0` and normalized schema version `1` are independent; no registry
publication or downstream consumer migration is implied.

## Public Python API

`contexttrack.models` and the package root export `ContextInfo`, `RequestFields`,
`RequestMessage`, `SentRequestMessage`, `RoutedRequestMessage`, `ResponseMessage`,
all five concrete event classes, `Event`, and `EVENT_ADAPTER`. The package root
also exports `normalize_record` and all documented `contexttrack.io` interfaces:
`LocatedEvent`, `EventFileError`, `iter_raw_events`, `iter_events`, `write_events`,
and `normalize_file`. Importing the package root does not import the CLI.

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

For file-based use, import the I/O API from `contexttrack.io`; raw and normalized
files have explicit, separate entry points:

```python
from contexttrack.io import iter_events, iter_raw_events, normalize_file

for record in iter_raw_events("raw.jsonl"):
    print(record.location, record.event.kind, record.event.context_key)

count = normalize_file("raw.jsonl", "new-normalized.jsonl")
for record in iter_events("new-normalized.jsonl"):
    print(record.location, record.event.kind, record.event.context_key)
```

Serialization includes all nullable fields as explicit nulls, preserves Unicode,
and excludes `context_key`. Use `write_events` for safe JSONL publication; add a
newline when serializing an individual record yourself.
`EVENT_ADAPTER.json_schema()` provides the schema derived from these same models;
there is no independently maintained normalized validator.

Imports do not read capture environment variables, emit diagnostics, open
captures, import graph code, or require Go or the consumer repository. The
package includes `py.typed` and requires Python >=3.14 and Pydantic >=2.13.5,<3.
Models, record normalization, I/O, and CLI are tested on Python 3.14.7 with
Pydantic 2.13.5.
Python/Pydantic, the Go toolchain used to produce raw evidence, and uv/build/test
tooling are external trust boundaries; these tests do not audit their third-party
source.

Query strings, context diagnostics, and source paths may contain sensitive
information. Model validation is not redaction or proof of capture completeness.
Complete-project human audit and sign-off remain pending; automated checks are
not a substitute for that review.
