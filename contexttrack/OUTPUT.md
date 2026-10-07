# Normalized ContextTrack event contract

## Boundaries and versioning

ContextTrack **0.2.0** provides typed events, pure raw-record normalization,
streaming JSONL I/O, normalize/schema CLIs, and `py.typed`. It requires Python
**>=3.14** and Pydantic **>=2.13.5,<3**. Package version is independent of the
normalized **schema version 1**; Go's current raw format remains unversioned.
[README](README.md) owns capture, installation, tests, and application caveats.

The [HotNets paper](../../ConfTamer_HotNets_2026.pdf), §§4–5/Figure 3, defines
per-module PMGraphs from inputs (parameters and received messages) to outputs
(sent messages), followed by separate AppGraph composition. This API retains
message/context evidence, **not a PMGraph/AppGraph or exact per-request causality**.
Parameter ingestion/discovery and cross-module stitching are outside it.
Routes are observations, not message nodes. Missing labels do not automatically
become usable downstream nodes. Consumer migration is separately authorized
[below](#consumer-handoff-separately-authorized-work); `analysis/` and the current
`conftamer-cli/node-query` importer still take raw captures only.

## Walkthrough: one received request

`normalize_record` validates a raw dict and returns a typed `RequestReceived`:

```json
{
  "kind": "Request received", "pid": 42,
  "context": {"context_id": "id:7"},
  "message": {"req.Method": "GET", "req.URL.Path": "/items/7", "req.URL.RawQuery": "page=1"}
}
```

`write_events` serializes it with unchanged labels and explicit nulls for missing
evidence. These synthetic examples are formatted for reading; JSONL uses one line:

```json
{
  "schema_version": 1, "kind": "receive_request", "pid": 42,
  "context": {"context_id": "id:7", "source": null, "type": null, "error": null},
  "api_id": null, "handler": null, "goroutine_id": null, "thread_id": null,
  "file": null, "line": null,
  "message": {"method": "GET", "path": "/items/7", "raw_query": "page=1"}
}
```

## Envelope and context

[`models.py`](src/contexttrack/models.py) defines each event as a JSON object:

| Field | Type | Required / default | Meaning |
| --- | --- | --- | --- |
| `schema_version` | Exact integer `1` | Required | Normalized format version, not package/patch version |
| `kind` | Canonical literal below | Required | Discriminator |
| `pid` | Strict integer | Required | Producer process ID |
| `context` | `ContextInfo` object | Required | Context evidence/diagnostics |
| `message` | Typed payload below | Required | Message/routing evidence |
| `api_id` | String or null | Null | Best-effort package/API association, **not** PMGraph `module_id` |
| `handler` | String or null | Null | Handler evidence |
| `goroutine_id` | Strict integer or null | Null | Go debug metadata |
| `thread_id` | Strict integer or null | Null | Debug metadata |
| `file` | String or null | Null | Source/debug path |
| `line` | Strict integer or null | Null | Source/debug line, not reader location |

`pid` and debug integers have no positivity restriction; zero and null differ.
No capture/occurrence ID, reader location, or wire `request_id` is added.
`ContextInfo` has these fields, all nullable strings defaulting to null:

| Field | Meaning |
| --- | --- |
| `context_id` | Process-local context label |
| `source` | Context source diagnostic |
| `type` | Context type diagnostic |
| `error` | Context lookup diagnostic |

Historical `root_addr` is not supported. `{}` represents absent context evidence;
normalized `context` cannot be omitted or null.

### Context identity

Each event's non-serialized `context_key: tuple[int, str] | None` returns
`(pid, context.context_id)` for a nonempty ID; omitted/null/empty IDs yield `None`,
not a shared invented "unknown" group. Whitespace is preserved and is not empty.
Group by **PID and ID within one capture**, never ID alone. IDs are process-local
influence heuristics, not distributed trace IDs or exact HTTP exchange identities;
reusing an append-only raw file does not establish cross-run identity.

## Payloads and variants

Every payload field defaults to null. Inheritance exposes the base fields too:

| Public payload model | Base | Own fields |
| --- | --- | --- |
| `RequestFields` | — | `method: str \| None`, `path: str \| None` (nonempty when present) |
| `RequestMessage` | `RequestFields` | `raw_query: str \| None` |
| `SentRequestMessage` | `RequestMessage` | `host: str \| None` |
| `RoutedRequestMessage` | `RequestFields` | `pattern: str \| None` |
| `ResponseMessage` | `RequestFields` | `status_code: int \| None` (strict, nonnegative) |

| Public event model | Required `kind` | Required `message` type |
| --- | --- | --- |
| `RequestSent` | `send_request` | `SentRequestMessage` |
| `RequestReceived` | `receive_request` | `RequestMessage` |
| `RequestRouted` | `request_routed` | `RoutedRequestMessage` |
| `ResponseSent` | `send_response` | `ResponseMessage` |
| `ResponseReceived` | `receive_response` | `ResponseMessage` |

`raw_query=null` is unavailable evidence; `""` is an explicitly empty query.
`pattern` stays separate from concrete `path`, preserving syntaxes such as
`GET /items/{id}`, `:name`, and `*path`. Outbound method/host/path are endpoint
labels, not occurrence/correlation IDs. Responses have no host field and are
not enriched with API ID, handler, or route pattern from other observations.

### Canonical validation

All wire models use
`ConfigDict(strict=True, extra="forbid", frozen=True, revalidate_instances="always")`:

- Unknown kinds, versions, and envelope/nested fields fail. Another payload's
  fields are forbidden even when null: requests reject `status_code`; responses
  reject `host`, `raw_query`, `pattern`; routes reject `host`, `raw_query`.
- No coercion: booleans/strings/floats are not integers; nonstrings are not strings.
  A dedicated validator rejects `true`, `1.0`, and `"1"` for `schema_version`;
  strict `Literal[1]` alone is insufficient on Pydantic 2.13.5.
- Required fields have no defaults. `message` must be an object, not null;
  `{}` is valid incomplete evidence. Nullable fields may be omitted and serialize
  as explicit nulls. Present paths must be nonempty; canonical `""` is rejected,
  not changed to `"/"`. Other strings may be empty.
- `status_code` is null or a strict nonnegative integer, without a 100–599
  restriction (`0`, `200`, `999` are valid); models do not convert status strings.
- Ordinary assignment to events/nested models fails. Adapter validation
  revalidates even unchecked Pydantic `model_construct`/copy instances;
  validate before serialization.

Strings are otherwise preserved: no trimming, method uppercasing, host lowercasing,
URL decoding/cleaning, query stripping, or route-syntax conversion.

## Raw mapping and strictness differences

[`normalize_record(record: object) -> Event`](src/contexttrack/normalize.py)
accepts one **current unversioned fixed-producer raw record**, as retained by the
overlay tooling. It does not accept normalized records, historical root-address
captures, or v2/v3/v4/v5 experiments. Private [`_raw.py`](src/contexttrack/_raw.py)
models also use strict types and forbid unknown envelope/nested fields.

| Raw kind | Canonical kind | Method/path source | Other payload evidence |
| --- | --- | --- | --- |
| `Request sent` | `send_request` | `request_id.method` / `request_id.path` | `request_id.host`, `message.req.URL.RawQuery` |
| `Request received` | `receive_request` | `message.req.Method` / `message.req.URL.Path` | `message.req.URL.RawQuery` |
| `Request routed` | `request_routed` | `message.req.Method` / `message.req.URL.Path` | `message.pattern`, unchanged |
| `Response sent` | `send_response` | `message.req.Method` / `message.req.URL.Path` | `message.code` → integer `status_code` |
| `Response received` | `receive_response` | `message.req.Method` / `message.req.URL.Path` | `message.resp.StatusCode` → integer `status_code` |

Only these raw message keys are legal per kind:

| Raw kind | Allowed `message` keys |
| --- | --- |
| `Request sent` | `req.Method`, `req.URL.Host`, `req.URL.Path`, `req.URL.RawQuery` |
| `Request received` | `req.Method`, `req.URL.Path`, `req.URL.RawQuery` |
| `Request routed` | `req.Method`, `req.URL.Path`, `pattern` |
| `Response sent` | `req.Method`, `req.URL.Path`, `code` |
| `Response received` | `req.Method`, `req.URL.Path`, `resp.StatusCode` |

- Raw `kind`, strict integer `pid`, and object `message` are required; `{}` is
  valid but missing/null/non-object messages fail. Context, request-label objects,
  API/handler strings, and optional debug metadata may be omitted/null.
- Known message/request-label fields must be **strings when present**, even
  duplicate unused outbound labels; explicit null fails. Omission becomes null
  evidence. Context fields and optional envelope metadata accept declared nulls.
- Dotted keys are validation aliases, not alternatives to internal names;
  canonical `method`, `path`, `status_code`, unknown keys/kinds, and `root_addr`
  fail rather than being ignored. This is stricter than diagnostic scripts and
  the legacy consumer's tolerance of some unconsumed fields.
- `request_id` allows only `method`, `host`, `path`. A non-null object (even `{}`)
  is legal only on `Request sent`; null is legal on all kinds.
- **Outbound precedence:** only `request_id` supplies method/host/path, even if
  duplicate message labels disagree. Missing/null/empty-object labels or omitted
  label fields stay null; there is **no fallback to message**. Query evidence
  still comes from `message`. Keep raw input to inspect disagreements.
- A present empty raw path becomes `"/"` for every kind; omission stays null.
  Empty methods/hosts and omitted-versus-empty Unicode query strings are preserved.
- Present response status is a string satisfying `str.isdecimal()`, converted
  with `int()`: leading zeros and Unicode decimal digits work (`"0200"` → `200`).
  Numbers, booleans, empty strings, signs, whitespace, decimal points, and
  nondecimal strings fail. No 100–599 restriction (`"0"`, `"103"`, `"999"` work);
  omitted status stays null.
- Envelope/debug values and all four context diagnostics retain their values;
  missing optional values become null, zero stays zero. Missing/null raw context
  becomes all-null `ContextInfo`, without fabricated identity.

Apart from path/status conversion, strings follow the unchanged-value rules in
[canonical validation](#canonical-validation); route syntax is not reconstructed.
Each accepted record yields exactly one event, validated by the final public
`EVENT_ADAPTER`, without input mutation, I/O, warnings, caching, association,
enrichment, filtering, deduplication, or sorting. Repeated/incomplete observations
remain separate; no host, API ID, handler, route pattern, or module ownership is
invented. Downstream code decides usable nodes/associations. Malformed raw shapes
raise Pydantic `ValidationError` (a `ValueError`); invalid decimal strings raise
`ValueError`. Record-only validation has no file location.

## Streaming JSONL I/O

### Readers and locations

`iter_raw_events` lazily normalizes a **completed raw capture** in memory without
writing/modifying it; stop capture first. `iter_events` lazily validates
**normalized v1 only** through `EVENT_ADAPTER`. No tailing, capture runner, format
auto-detection/mixing, or completeness/mixed-run detection is supplied.

Both share a byte-line parser: strict UTF-8 decoding per physical line, blank
lines skipped, empty/blank-only files and valid unterminated final records
accepted. Malformed JSON, non-object records, UTF-8 BOM, `NaN`/`Infinity`, and
duplicate keys at any depth fail. Model validation belongs to the appropriate
adapter, not a second handwritten schema. Events stream in file order without
association/enrichment/filtering/deduplication/sorting; this is not global causal
order across processes.

A frozen `LocatedEvent` carries `event`, `path: Path`, and one-based physical
`line: int`; `location` is `f"{path}:{line}"`. Skipped blanks count toward lines;
the Go debug line is separate. Locations name the file actually read and are
never wire metadata. Raw reading and normalizing then reading yield equal events
in equal order, but locations can differ.

`EventFileError(ValueError)` stores `path`, `line`, `reason`, with a string starting
`path:line:`. JSON/UTF-8/raw/Pydantic input failures retain their original exception
as `__cause__`. Filesystem errors remain `OSError` subclasses; programming errors
are not reclassified as bad records. Earlier valid events may already have been
yielded when a later line fails.

### Safe publication

`write_events` revalidates every outgoing value through `EVENT_ADAPTER`, including
unchecked model instances. It streams
`model_dump_json(by_alias=False, exclude_none=False, ensure_ascii=False)` plus one
newline per event: explicit nulls, literal UTF-8 Unicode, no location metadata or
raw-dict dumping. Memory is bounded per record, not capture; the count includes
repeated hooks.

Output must be a **new file in an existing directory**. Existing files,
directories, symlinks (including dangling ones), or input-equal output are rejected,
never overwritten/followed for writing. No append/overwrite, parent creation, or
raw rewrite mode exists. A same-filesystem temporary is fully validated/written
and closed before `os.link(temp_path, output_path)` publishes it atomically without
clobbering a concurrent destination. The existence precheck is only an optimization.
Temporary names are removed on success/failure; decode/validation/serialization/
write/close failures before publication leave no output prefix. Unsupported hard
links raise a filesystem error; there is no overwriting `os.replace` fallback.
This assumes supported local filesystem semantics, **not crash durability or
network-filesystem guarantees**.

`normalize_file` composes the raw reader and writer with no extra mapping/error
wrapping: `write_events((record.event for record in iter_raw_events(source)), output)`.
Empty input publishes empty output and returns zero, not evidence of useful traffic.

## CLI

```text
contexttrack normalize INPUT --output NEW_OUTPUT
python -m contexttrack normalize INPUT --output NEW_OUTPUT
contexttrack schema
```

Both entry points use [`cli.main(argv: Sequence[str] | None = None) -> int`](src/contexttrack/cli.py).
`normalize` delegates to `normalize_file` and accepts completed **raw only**.
There are no default paths, append/overwrite/skip-bad flags, stdout-output mode,
or separate mapping/association logic. The publication rules above apply.
Success returns 0 with empty stdout/stderr; expected input/filesystem failures
return 2 with stderr diagnostics and no traceback (input retains path/physical
line). Argparse argument errors exit 2; help exits 0.

`schema` prints `EVENT_ADAPTER.json_schema()` as sorted, two-space-indented UTF-8
JSON with a final newline, no other stdout, and no required checkout/snapshot.
No registry publication or consumer adoption is implied.

## Public Python API

These are package-root exports and the identical objects in the named submodule:

| Submodule | Public names / signatures | Role |
| --- | --- | --- |
| `contexttrack.models` | `ContextInfo`, `RequestFields`, `RequestMessage`, `SentRequestMessage`, `RoutedRequestMessage`, `ResponseMessage` | Context/payload models tabulated above |
| `contexttrack.models` | `RequestSent`, `RequestReceived`, `RequestRouted`, `ResponseSent`, `ResponseReceived` | Concrete event models tabulated above |
| `contexttrack.models` | `Event`, `EVENT_ADAPTER: TypeAdapter[Event]` | `Annotated` kind-discriminated union and validator |
| `contexttrack.normalize` | `normalize_record(record: object) -> Event` | Pure single-raw-record conversion |
| `contexttrack.io` | `LocatedEvent`, `EventFileError` | Frozen location wrapper / located input error |
| `contexttrack.io` | `iter_raw_events(path: str \| Path) -> Iterator[LocatedEvent]` | Raw reader |
| `contexttrack.io` | `iter_events(path: str \| Path) -> Iterator[LocatedEvent]` | Normalized reader |
| `contexttrack.io` | `write_events(events: Iterable[Event], output: str \| Path) -> int` | Safe writer; event count |
| `contexttrack.io` | `normalize_file(source: str \| Path, output: str \| Path) -> int` | Raw-file conversion; event count |

Importing the package does not import the CLI/graph code, inspect capture
environment variables, emit diagnostics, open captures, or require Go/consumer
repositories. `Event` is not a class with `model_validate_json`; use the adapter
or a concrete model:

```python
from contexttrack import EVENT_ADAPTER, RequestSent

record = {
    "schema_version": 1, "kind": "send_request", "pid": 42,
    "context": {"context_id": "id:7"},
    "message": {"method": "gEt", "host": "Höst:80", "path": "/", "raw_query": ""},
}
event = EVENT_ADAPTER.validate_python(record)
assert isinstance(event, RequestSent)
assert event.context_key == (42, "id:7")
text = event.model_dump_json(by_alias=False, exclude_none=False, ensure_ascii=False)
assert EVENT_ADAPTER.validate_json(text) == event
```

For raw dicts, use `normalize_record(record)` as in the
[walkthrough](#walkthrough-one-received-request); it needs no other records.

File use (provide a completed raw input and a new output):

```python
from contexttrack import iter_events, iter_raw_events, normalize_file

for record in iter_raw_events("raw.jsonl"):
    print(record.location, record.event.kind, record.event.context_key)
count = normalize_file("raw.jsonl", "new-normalized.jsonl")
for record in iter_events("new-normalized.jsonl"):
    print(record.location, record.event.kind, record.event.context_key)
```

Single-record serialization excludes `context_key` and needs a newline for JSONL;
use `write_events` for safe publication. Direct `EVENT_ADAPTER.validate_json`
checks model shape, **not** the readers' extra file-level checks.

## Generated JSON Schema

There is **no checked-in schema snapshot** or independently maintained validator.
`EVENT_ADAPTER.json_schema()` describes one normalized object, not a JSONL file.
Export to a new scratch path and verify CLI/model agreement:

```bash
SCHEMA_DIR=$(mktemp -d /tmp/contexttrack-schema.XXXXXX)
uv run contexttrack schema > "$SCHEMA_DIR/event-v1.schema.json"
uv run pytest -q tests/test_cli.py -k schema
```

The installed command works without a checkout. Changes require schema review;
incompatible fields/kinds/meanings and schema additions require an explicitly
supported format version, not just a package release. Do not hand-maintain another
validator. JSON Schema does not provide duplicate-key/UTF-8/BOM/physical-line
checks or every Python strict-type rule: it can treat `1.0` as an integer whereas
the public models reject it as `schema_version`. Readers/adapter remain the Python
validation boundary. CLI formatting is owned by `tests/test_cli.py`; independent
literal model tests are not replaced by generation agreement.

## Consumer handoff: separately authorized work

Reference: `conftamer-cli/node-query` at
`a173121d6cec1ebf26394714c356a0eaa17149fa`, inspected from committed README,
importer, and tests. It **has not migrated**: it parses raw itself and builds a
message-only PMGraph with caller-supplied `module_id`. Its build needs `--module-id`
and a new `--output`; export emits GraphML, query uses exact PMGraph node IDs.
Parameter ingestion, stitching, and full AppGraph composition are unimplemented.
A separately authorized migration must:

1. Pin a tested ContextTrack artifact/version in the dependency manifest/lockfile.
   Import public models and `iter_raw_events`, without copying models/schema or
   keeping a second consumer-owned raw parser.
2. Keep `load_contexttrack(path, *, module_id)` and
   `conftamer build INPUT --module-id ID --output NEW` on **one completed raw path**.
   No auto-detection, input-format option, second reader, normalized-input support,
   or required normalization pre-step belongs to this handoff.
3. Adapt `LocatedEvent` to the consumer's mutable association record, preserving
   location, `context_key`, typed payloads, and canonical kinds; public events stay
   immutable. Do not pretend responses captured a host.
4. Preserve conservative association: routes/responses need a unique earlier
   appropriate request matching `(pid, context_id)`, method, concrete path.
   Rewritten/ambiguous routes warn and fall back to concrete path; unmatched/
   ambiguous responses warn and are omitted. Retain conflicting-API-ID rejection;
   API IDs never become module IDs. Occurrences, label interning, and influence
   edges remain consumer responsibilities.
5. Document stricter raw validation: unknown kinds/keys, forbidden per-kind fields,
   explicit null string labels, non-null `request_id` outside sends, `root_addr`,
   duplicate keys, and BOM become errors instead of tolerated/warned input.
   Malformed input becomes `EventFileError` (`ValueError`) with `path:physical-line`,
   replacing the legacy `TypeError`/`ValueError` split; expected CLI errors still
   exit 2. Incomplete well-typed evidence remains for warnings/omissions.
6. Verify importer/CLI tests and a build of the committed raw fixture to a new
   PMGraph in an authorized disposable checkout. Expected four scrape nodes and
   edge `{("n1", "n2")}` are **graph results**, not normalized record counts:
   all 20 fixture records, including eight received-response hooks, are retained.

Producer wheel/capture checks imply no consumer edits, registry publication, or
adoption proof. Standard-library-only raw-analysis co-occurrence views are
not canonical PMGraphs or causality proofs.

## Privacy, trust boundaries, and human audit

Raw query strings, context diagnostics, endpoint labels, handlers, and source
paths may expose credentials or environment-specific data. Normalization is
**not redaction**.
Retain original evidence for auditing, protect both files, and do not commit real
captures/graphs. Valid JSONL, successful normalization, or nonempty captures do
not prove completeness, HTTP delivery, or correct influence attribution.
On a write error, the Go producer schedules at most one best-effort warning
worker, without event payloads; HTTP does not wait for warning delivery. Closed
stderr suppresses it safely; full stderr may block the worker, not HTTP. Process
exit may discard it, and partial capture writes may leave invalid JSONL. Startup
diagnostics are unchanged. See [README capture diagnostics](README.md#capture-setup-and-diagnostics)
for warning text and file-lifetime details.

Python/Pydantic (including pydantic-core/typing dependencies), Go/stdlib, uv/
uv_build, pytest, and type/lint/format/build tools are external trust boundaries.
Manifests, lock metadata/hashes, and artifact checks are review inputs, **not
third-party source audits**. Filesystem limits are stated under safe publication;
paper semantics and prototype-test boundaries are distinct.

Complete-project human audit/sign-off remains **pending** until humans review
existing/new source, Go hooks/patches, tests, tooling, active documentation and
plans, generated schema output, packaging/lock metadata against the final revision
or recorded hashes, record every file's status, and resolve findings. Automated
checks and agent review do not satisfy that gate.
