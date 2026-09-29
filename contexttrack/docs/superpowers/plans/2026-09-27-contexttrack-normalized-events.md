# ContextTrack Normalized Events Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Delegated execution requires separate operator authorization; this planning request does not authorize implementation, delegation, commits, or edits to sibling repositories.

**Goal:** Make ContextTrack produce versioned, normalized JSONL whose public Pydantic models can be installed and imported by downstream consumers.

**Architecture:** Keep the current Go instrumentation and raw JSONL unchanged. Add a Python, record-by-record normalizer that validates raw input, constructs shared Pydantic event models, and serializes those models into a separate output file. Consumers import the models and one of two location-aware readers: `iter_raw_events` normalizes a completed raw capture in memory, and `iter_events` reads a normalized file. Request association and graph construction remain downstream.

**Tech Stack:** Python >=3.14; Pydantic >=2.13.5,<3; pytest >=9.1.1; existing uv_build packaging; Python standard library for JSONL, CLI, and file handling. Existing Go 1.26.6 instrumentation is an integration-test input, not an implementation target.

**Spec:** Sections 1-4 below define this feature's design contract. They incorporate the operator's explicit choices of **Python normalizer** and **records-only normalization**, the current [README](../../../README.md), the [agent guide](../../../AGENTS.md), and the [HotNets paper](../../../../../ConfTamer_HotNets_2026.pdf)'s separation of message/context evidence, PMGraphs, and AppGraph composition.

## Global Constraints

- Planning producer baseline: `contexttrack-fix-v1`, commit `edc254b9ff5c90972dc753776e0c39ab589ec56d`.
- Consumer reference: `conftamer-cli/node-query`, commit `a173121d6cec1ebf26394714c356a0eaa17149fa`; inspect with `git show`, not another branch or untracked implementation.
- Check `git status --short --branch` before execution. Preserve the owner's untracked `AGENTS.md`, `docs/`, and `src/` work. Historical v4/v5 documents and Python bytecode are not this feature's implementation baseline.
- Use `using-git-worktrees` at execution time to establish an isolated workspace. The source files listed as new below are new to the committed baseline; do not overwrite the original workspace's untracked `src/contexttrack/__init__.py`.
- Keep `requires-python = ">=3.14"`; change only the Pydantic constraint to `pydantic>=2.13.5,<3`, without unrelated dependency upgrades.
- Do not change `go-inlibrary.patch`, either optional patch, HTTP behavior, `CONFTAMER_EVENTS`, or its opt-in append-only raw format.
- Do not edit sibling repositories, installed Go trees, module caches, or existing captures. Native verification uses a disposable, version-matching Go copy and fresh captures.
- Preserve all accepted input records, order, and repeated hooks. No filtering, deduplication, sorting, route reconstruction, response matching, or graph generation in the normalizer.
- Context keys are `(pid, context_id)` within one capture, never `context_id` alone. They are heuristic influence groups, not distributed trace IDs or exact HTTP exchange identities.
- `request_id` is an outbound endpoint label; `api_id` is not a PMGraph `module_id`. Do not fabricate either identity.
- Keep the standard-library-only `analysis/` scripts on raw input in this release. Their diagnostic graph semantics do not become the normalization contract.
- Human auditability is a required design and completion criterion for the complete ContextTrack project, not just this feature's diff. Follow the review scope in Section 5 and the human sign-off gate in Section 9.
- Before completing each implementation task or making an authorized commit, run `uvx ty check` (Python type checking), `uvx ruff check` and `uvx ruff format --check` (lint and formatting checks), and `uvx tombi lint` and `uvx tombi format --check` (lint and formatting checks for `.toml` files only). Pass changed/in-scope paths explicitly. These supplement pytest, syntax checks, and `scripts/check.sh`, which does not run these tools. Record commands, scope, and results; report unavailable tools or pre-existing out-of-scope failures rather than silently skipping checks or editing unrelated files.
- **Do not run Tombi on `uv.lock` (lint or format), including temporary copies.** Use explicit `.toml` paths rather than directory-wide targets; for this package, run `uvx tombi lint pyproject.toml` and `uvx tombi format --check pyproject.toml`. `uv.lock` is uv-generated: regenerate it with `uv lock`, validate it with `uv sync --locked --dev`, and review its dependency/metadata diff without reformatting it. It is excluded from Tombi completion gates, not from dependency or human audit review.
- Use TDD for each behavior change. Task-end commit commands are proposed checkpoints, to run only if commits are authorized.

---

## 1. Scope and current findings

### What exists

- `pyproject.toml` already declares the `contexttrack` package, Python 3.14, Pydantic, pytest, and a console entry point. The committed baseline does not contain its Python implementation.
- The patched Go serializer emits five raw event kinds with nested `message` and `context` objects. HTTP fields use names such as `req.Method`, `code`, and `resp.StatusCode`; status codes are strings.
- The committed `node-query` importer validates selected raw fields itself, converts status strings to integers, normalizes empty paths, and conservatively associates routes/responses with unique preceding requests.
- The paper's §§4-5 and Figure 3 distinguish per-module input-to-output influence from later graph composition. A typed event stream is evidence for that pipeline, not a PMGraph or a claim of complete message attribution.

### Deliverable

```text
patched Go -> raw.jsonl --+---------------------------------------> existing raw analysis scripts
                          |
                          +-> contexttrack.io.iter_raw_events ------+  (in memory, typed)
                          |                                         +-> contexttrack.models
                          +-> contexttrack normalize                |   downstream association / PMGraph code
                                -> normalized.jsonl                 |
                                -> contexttrack.io.iter_events -----+
```

Both readers apply the same normalization code. `normalize_file` is `write_events` applied to `iter_raw_events`. A consumer that receives raw captures can use `iter_raw_events` directly, with no intermediate file and no second input format. The normalized file is for storage, sharing, and non-Python consumers.

The new command is:

```bash
uv run contexttrack normalize raw.jsonl --output normalized.jsonl
```

`raw.jsonl` must be a completed capture, not a file being actively appended to. There is no tailing or capture-runner feature. Normalization cannot prove capture completeness or recover run boundaries from an already mixed append-only file.

### Non-goals

No native schema migration, HTTP coverage expansion, new context identity mechanism, occurrence/exchange IDs, parameter ingestion, inferred module ownership, route/response enrichment, PMGraph/AppGraph construction, or broad Prometheus/Caddy/Kubernetes experiments. Actual `node-query` migration is a separately authorized follow-up described in Section 7.

## 2. Public normalized contract

### 2.1 Format and version

- UTF-8 JSONL: one event object per nonblank input record, one newline after each serialized output event.
- Every normalized event carries required integer `schema_version: 1` and one of the five canonical `kind` values below.
- Version 1 is the **normalized event format version**, not the patch revision or any historical experimental format version.
- Canonical models use `ConfigDict(strict=True, extra="forbid", frozen=True, revalidate_instances="always")`, including all nested models.
- Nullable fields default to `None`; canonical serialization includes explicit nulls. Relevant but unavailable evidence is represented, not discarded. Fields belonging to a different payload type are forbidden.
- A strict integer-before validator is required on `schema_version`: Pydantic 2.13.5 accepts `True` and `1.0` for `Literal[1]` even under strict model configuration. Both must be rejected.
- Unknown normalized fields, kinds, and versions are errors. Adding fields/kinds or changing meaning requires an explicitly supported schema version, not silently loosening `extra="forbid"`.

### 2.2 Models

All public wire models live in `src/contexttrack/models.py`.

| Model | Fields / responsibility |
| --- | --- |
| `ContextInfo` | Nullable strings `context_id`, `source`, `type`, `error`; preserve context diagnostics |
| `RequestFields` | Nullable strings `method`, `path`; a present canonical `path` is nonempty |
| `RequestMessage(RequestFields)` | Adds nullable `raw_query` |
| `SentRequestMessage(RequestMessage)` | Adds nullable `host` |
| `RoutedRequestMessage(RequestFields)` | Adds nullable `pattern`; retains the concrete path separately |
| `ResponseMessage(RequestFields)` | Adds nullable strict, nonnegative integer `status_code` |
| Private `_Envelope` | Required `schema_version`, `kind`, `pid`, `context`; nullable `api_id`, `handler`, `goroutine_id`, `thread_id`, `file`, `line` |
| `RequestSent` | `kind="send_request"`, `message: SentRequestMessage` |
| `RequestReceived` | `kind="receive_request"`, `message: RequestMessage` |
| `RequestRouted` | `kind="request_routed"`, `message: RoutedRequestMessage` |
| `ResponseSent` | `kind="send_response"`, `message: ResponseMessage` |
| `ResponseReceived` | `kind="receive_response"`, `message: ResponseMessage` |
| `Event` | Discriminated union of these five concrete event models, discriminator `kind` |
| `EVENT_ADAPTER` | Shared `TypeAdapter(Event)` for parsing, validation, serialization schema, and downstream use |

Envelope `pid` is a strict integer. Optional numeric debug fields are strict integers when present; preserve `thread_id=0` rather than treating it as missing. Do not introduce positivity restrictions on these existing debug fields or PIDs as part of this change.

Add a non-serialized property on `_Envelope`:

```python
@property
def context_key(self) -> tuple[int, str] | None:
    context_id = self.context.context_id
    return (self.pid, context_id) if context_id else None
```

Missing/empty context IDs never create an "unknown" group shared by unrelated records. No synthetic capture ID or source occurrence number is added to the wire format. JSONL path/physical-line information belongs to the reader's `LocatedEvent`, not the captured event.

### 2.3 Example and consumer usage

This request event intentionally has no `request_id`: its endpoint label has been moved into the typed message payload, not converted into an occurrence ID.

```json
{
  "schema_version": 1,
  "kind": "send_request",
  "pid": 42,
  "context": {
    "context_id": "id:7",
    "source": "req.Context()",
    "type": "context.Context",
    "error": null
  },
  "api_id": "example.org",
  "handler": null,
  "goroutine_id": 8,
  "thread_id": 0,
  "file": "/go/src/net/http/transport.go",
  "line": 640,
  "message": {
    "method": "GET",
    "path": "/metrics",
    "raw_query": "",
    "host": "localhost:9090"
  }
}
```

Direct parsing requires no consumer-owned schema:

```python
from contexttrack.models import EVENT_ADAPTER, RequestSent

line = '{"schema_version":1,"kind":"send_request","pid":42,"context":{"context_id":"id:7"},"message":{"method":"GET","host":"localhost:9090","path":"/metrics","raw_query":""}}'
event = EVENT_ADAPTER.validate_json(line)
assert isinstance(event, RequestSent)
assert event.message.host == "localhost:9090"
assert event.context_key == (42, "id:7")
```

For file parsing and association diagnostics:

```python
from contexttrack.io import iter_events, iter_raw_events

for record in iter_raw_events("raw.jsonl"):      # raw capture, normalized in memory
    event = record.event
    print(record.location, event.kind, event.context_key)

for record in iter_events("normalized.jsonl"):   # normalized v1 file
    ...
```

Both yield the same `LocatedEvent` values for the same capture. Only `location` differs, because it names the file actually read.

`Event` is a union, not a class with `model_validate_json`; use `EVENT_ADAPTER`, a concrete event model, or `iter_events`.

## 3. Raw-to-normalized mapping and validation

The adapter supports the **current unversioned `contexttrack-fix-v1` raw format**, including the committed consumer fixture. It is not an importer for all historical root-address or v2/v3/v4/v5 experiments.

| Raw input | Canonical output / rule |
| --- | --- |
| `Request sent` | `send_request`; method/host/path from **`request_id`**, never fallback to duplicate `message` labels |
| `Request received` | `receive_request`; method/path from `message.req.Method` / `message.req.URL.Path` |
| `Request routed` | `request_routed`; method/path plus `message.pattern`, unmodified |
| `Response sent` | `send_response`; method/path plus integer conversion of `message.code` |
| `Response received` | `receive_response`; method/path plus integer conversion of `message.resp.StatusCode` |
| `message.req.URL.RawQuery` | `message.raw_query` on request records, including an explicitly empty string |
| `pid`, `api_id`, `handler`, debug fields | Same named envelope fields; missing optional fields become null |
| Raw `context` | `ContextInfo`, preserving all four known fields; missing/null object becomes all-null `ContextInfo` |
| Present path `""` | `"/"`, consistently across all five event kinds |
| Missing method/path/host/pattern/status | Null, not a guessed default and not a reason to omit a record |
| Response metadata missing from the hook | Remains missing; no inherited host, API ID, handler, or route pattern |

Additional rules:

1. Raw `kind`, `pid`, and `message` are required. `message` must be an object, including for incomplete events.
2. Use private strict Pydantic raw models in `_raw.py`. Known raw message/request-label fields are strings when explicitly present; omitted fields are allowed. Explicit null for these string fields is invalid. Raw `context`, `request_id`, `api_id`, and optional debug metadata may be absent/null.
3. Restrict raw message keys to those emitted for its kind: sent request `{req.Method, req.URL.Host, req.URL.Path, req.URL.RawQuery}`; received request `{req.Method, req.URL.Path, req.URL.RawQuery}`; routed `{req.Method, req.URL.Path, pattern}`; sent response `{req.Method, req.URL.Path, code}`; received response `{req.Method, req.URL.Path, resp.StatusCode}`. A non-null `request_id` is legal only on `Request sent`.
4. Unknown raw kinds/keys, malformed types, and the historical `context.root_addr` format fail explicitly. This is intentionally stricter than the diagnostic scripts and the legacy consumer's tolerance of some unconsumed fields. Do not silently skip or discard unknown schema additions.
5. Sent-request labels use `request_id` even if the duplicate raw `message` labels disagree. Preserve the untouched raw file for auditing; document this precedence rather than silently falling back. Missing `request_id` leaves canonical method/host/path null even when the raw message contains them.
6. Convert a present status only when `str.isdecimal()` is true; convert with `int()`. Preserve legacy acceptance of leading zeros, e.g. `"0200" -> 200`. Reject numeric JSON statuses, booleans, signed/whitespace/empty strings, and decimal points. Do not add a 100-599 restriction: this change preserves the existing importer's nonnegative-decimal policy, including Go's nonstandard three-digit statuses.
7. Do not uppercase methods, lowercase hosts, trim strings/API IDs, decode URLs, clean paths, strip query strings, or convert route syntax. Preserve Unicode and distinctions such as missing versus empty query strings.
8. Keep duplicate client/wire response events and informational statuses. File order is preserved as observed, not asserted to be a global causal order.
9. Record normalization does not make incomplete records into valid PMGraph message nodes. Downstream code still decides whether evidence is usable and how to warn about ambiguity.

## 4. Public I/O and CLI contract

### Interfaces

```text
contexttrack.normalize.normalize_record(record: object) -> Event
contexttrack.io.iter_raw_events(path: str | Path) -> Iterator[LocatedEvent]
contexttrack.io.iter_events(path: str | Path) -> Iterator[LocatedEvent]
contexttrack.io.write_events(events: Iterable[Event], output: str | Path) -> int
contexttrack.io.normalize_file(source: str | Path, output: str | Path) -> int
contexttrack.cli.main(argv: Sequence[str] | None = None) -> int
```

`LocatedEvent` is a frozen dataclass with `event: Event`, `path: Path`, `line: int`, and a `location` property returning `f"{path}:{line}"`. `path` and `line` name the physical line of the file actually read: the raw input for `iter_raw_events`, or the normalized file for `iter_events`. It is reader metadata and is not serialized.

`EventFileError(ValueError)` stores `path`, `line`, and `reason`; its string contains the input path and physical line. JSON/Pydantic/UTF-8 input failures are wrapped with their original exception as the cause. Filesystem errors remain `OSError` subclasses.

### Reader behavior

- Read bytes line by line and decode each line with strict UTF-8, so even decoding failures have accurate physical line numbers.
- Skip blank lines; accept an empty/blank-only file and a valid last record without a final newline.
- Reject malformed JSON, non-object records, non-finite constants, and duplicate object keys. Reject a UTF-8 BOM rather than silently changing encodings.
- JSON syntax/line handling is shared by raw normalization and normalized reading. Validate record shape through the appropriate Pydantic adapter, not a second handwritten normalized schema.
- `iter_events` reads **normalized v1 only**. `iter_raw_events` and `normalize_file` read **raw only**. Do not auto-detect or mix formats. Feeding canonical data to a raw reader, or raw data to `iter_events`, is an error.
- `iter_raw_events` is lazy and streams records: the shared JSONL reader followed by `normalize_record`. `normalize_record` failures are wrapped as `EventFileError` at the raw physical line. It writes nothing and does not modify the input. `normalize_file(source, output)` is `write_events((r.event for r in iter_raw_events(source)), output)`; it has no normalization or error-wrapping code of its own.
- Direct `EVENT_ADAPTER.validate_json` validates model shape. The file reader additionally supplies the JSONL-level duplicate-key, encoding, and location checks.

### Writer behavior

- Stream records with bounded per-record memory; never load the complete capture to write it.
- Validate every outgoing value with `EVENT_ADAPTER`, then serialize with `model_dump_json(by_alias=False, exclude_none=False, ensure_ascii=False)` and a final newline. Do not use raw dict dumping as the output schema boundary.
- Output must be a new file in an existing directory. Never append, overwrite, or rewrite the raw input.
- Write to a temporary file on the output filesystem. Publish only after complete validation and close, using `os.link(temp_path, output_path)` for atomic no-clobber publication, then remove the temporary name. An existence precheck is only an optimization; `os.link` supplies race-safe no-overwrite behavior.
- On decoding/validation/write failure before publication, clean up the temporary file and leave no output file. If hard-link publication is unsupported, report the filesystem error; do not fall back to `os.replace`, which could overwrite user data.
- This is atomic publication on the supported local filesystem, not a crash-durability or network-filesystem guarantee. Empty input produces a valid empty output and count zero, not evidence that instrumentation captured useful traffic.

### CLI

```text
contexttrack normalize INPUT --output NEW_OUTPUT
contexttrack schema
python -m contexttrack normalize INPUT --output NEW_OUTPUT
```

`normalize` has no default paths, overwrite flag, lenient/skip-bad mode, or stdout output mode in this release. Success returns 0 with empty stdout/stderr. Expected validation/filesystem failures return 2, emit a useful stderr diagnostic, and have no traceback. `schema` prints the generated normalized v1 JSON Schema as sorted, indented UTF-8 JSON plus a newline; no other output goes to stdout.

### Packaging and compatibility

- Release this new Python API/CLI as package version `0.2.0`; normalized schema version remains independently `1`.
- Export stable public names from `contexttrack.models`, and the documented I/O API from `contexttrack.io`. Include `py.typed`. Keep imports side-effect free and independent of `conftamer`, Go installations, and capture environment variables.
- Generate `schemas/contexttrack-event-v1.schema.json` from `EVENT_ADAPTER.json_schema()`. It is a reviewable snapshot, not a second hand-maintained validator. `contexttrack schema` makes the schema available from an installed wheel too.
- Build/install the existing package as a wheel; do not create a new schema repository or copy models into each consumer. Consumers should pin a tested package artifact/version in their lockfile. Registry publication is a separate authorization.
- Raw producer output remains compatible with current tools. Normalized output is an explicit new interface; the existing `node-query` importer does **not** already support it.

---

## 5. File boundaries

Paths are relative to `contexttrack/` in the isolated implementation workspace.

| File | Change and responsibility |
| --- | --- |
| `src/contexttrack/models.py` | Create public canonical Pydantic models and shared adapter |
| `src/contexttrack/_raw.py` | Create private strict models/aliases for the existing raw format |
| `src/contexttrack/normalize.py` | Create pure, record-local field/type conversion |
| `src/contexttrack/io.py` | Create strict shared JSONL reader, public raw (`iter_raw_events`) and normalized (`iter_events`) readers, location errors, atomic writer, file normalization |
| `src/contexttrack/cli.py` | Create thin argparse CLI and schema export |
| `src/contexttrack/__init__.py` | Create public exports in the committed-baseline worktree; preserve the owner's original untracked file |
| `src/contexttrack/__main__.py`, `src/contexttrack/py.typed` | Create module execution and typing marker |
| `tests/conftest.py` | Create small synthetic Python fixtures shared by unit tests |
| `tests/test_models.py`, `tests/test_normalize.py` | Create wire-contract and conversion regressions |
| `tests/test_io.py`, `tests/test_cli.py` | Create stream/error/file-safety and subprocess CLI tests |
| `tests/test_schema.py` | Create generated-schema drift check |
| `schemas/contexttrack-event-v1.schema.json` | Create generated public schema snapshot |
| `pyproject.toml`, `uv.lock` | Update entry point, description, package version, Pydantic bound, and matching lock metadata |
| `OUTPUT.md` | Create authoritative normalized wire/API contract and mapping rules |
| `README.md` | Add normalization/install/import workflow; clearly distinguish raw versus normalized consumers |

Leave `analysis/`, `tests/httpcapture/`, all patches, and sibling repositories unchanged. Use synthetic fixtures in tests; extract the committed consumer capture only into temporary storage for integration verification.

### Size guidance — non-binding

Estimated new handwritten Python: **1,150–1,700 physical lines**, including blanks and docstrings:

- Production package: **450–650 lines**
- Tests and fixtures: **700–1,050 lines**

Prefer shared reader/error-handling code, straightforward model definitions, and parameterized tests. These estimates are review guidance, not limits. Do not sacrifice validation, diagnostics, filesystem safety, public API clarity, or test coverage to meet them. There are no per-file caps or line-count completion gates; the existing behavioral acceptance gates remain authoritative.

Generated schema, documentation, lockfile changes, and the separately authorized consumer migration are outside this Python estimate. Substantial growth should prompt a duplication review, not automatic rejection.

### Human auditability — required

Humans must be able to read and audit the complete ContextTrack project, not only the new Python package or changed files. The review scope includes all existing and new ContextTrack-owned source, Go instrumentation and patches, tests and fixtures, scripts/tooling, documentation (including this plan), generated schemas, and packaging/lock metadata. The Python estimates above describe additions, not the size of this complete review scope; project files excluded from the estimate are not exempt from review.

- Optimize the total human review surface, not production line count alone. Keep functions focused, field mappings explicit, and test cases easy to trace to the contract.
- Do not introduce dynamic model factories, schema/test DSLs, generated project source, or compressed syntax solely to meet a line-count estimate. The generated JSON Schema remains a model-derived, readable artifact.
- Keep `OUTPUT.md` the authoritative wire/API contract; use concise README workflows and links rather than duplicating the rules.
- Provide a complete-project file inventory and record human review status, findings/resolutions, and sign-off against the final revision or file hashes. Leave this gate pending until humans have read and audited every in-scope file. Automated checks and agent reviews do not constitute human sign-off.

Document external dependency and runtime/toolchain trust boundaries, including Pydantic, Python, Go, and build/test tooling. Reviewing the project and its dependency manifests must not be presented as auditing third-party source; any such audit needs an explicitly declared additional scope.

## 6. Implementation tasks

### Task 1: Public Pydantic event contract

**Files:** Create `models.py`, `__init__.py`, `py.typed`, `tests/conftest.py`, `tests/test_models.py`, and the contract portions of `OUTPUT.md`.

**Consumes:** Sections 2-3; no Go/runtime or downstream dependency.

**Produces:** `ContextInfo`, the four public payload models plus `RequestFields`, the five concrete event models, `Event`, `EVENT_ADAPTER`, and `.context_key`.

- [ ] Establish the package directory in the isolated worktree: create `src/contexttrack/__init__.py` containing only the module docstring below, and an empty `src/contexttrack/py.typed`. This lets uv build the package before the first red test; it does not implement the models. Add public exports only after the models exist.

```python
"""Public API for normalized ContextTrack events."""
```

- [ ] Add this synthetic fixture to `tests/conftest.py` and the tests below to `tests/test_models.py` before implementing the models.

```python
# tests/conftest.py
import pytest


@pytest.fixture
def normalized_sent():
    return {
        "schema_version": 1,
        "kind": "send_request",
        "pid": 42,
        "context": {"context_id": "id:7"},
        "message": {
            "method": "gEt",
            "host": "Höst:80",
            "path": "/",
            "raw_query": "",
        },
    }
```

```python
# tests/test_models.py
import pytest
from pydantic import ValidationError

from contexttrack.models import EVENT_ADAPTER, RequestSent


def test_public_model_round_trip(normalized_sent):
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    assert isinstance(event, RequestSent)
    assert event.message.method == "gEt"
    assert event.message.host == "Höst:80"
    assert event.context_key == (42, "id:7")
    assert EVENT_ADAPTER.validate_json(event.model_dump_json()) == event


@pytest.mark.parametrize("version", [True, 1.0, "1", 2])
def test_version_is_exact_integer_one(normalized_sent, version):
    normalized_sent["schema_version"] = version
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)


@pytest.mark.parametrize("pid", [True, "42", 42.0])
def test_pid_is_strict(normalized_sent, pid):
    normalized_sent["pid"] = pid
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)


def test_pid_scopes_context_identity(normalized_sent):
    first = EVENT_ADAPTER.validate_python(normalized_sent)
    second = EVENT_ADAPTER.validate_python(normalized_sent | {"pid": 43})
    assert first.context_key != second.context_key


def test_unknown_fields_are_not_ignored(normalized_sent):
    normalized_sent["message"]["req.Method"] = "GET"
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)


def test_canonical_path_cannot_be_empty(normalized_sent):
    normalized_sent["message"]["path"] = ""
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)
```

- [ ] Run `uv run pytest -q tests/test_models.py`; confirm the initial failure is the missing public models, not an unrelated environment failure.
- [ ] Implement the field definitions in Section 2 with strict nested models. Use this envelope/version/union pattern, with all named concrete classes defined as specified in the model table:

```python
from typing import Annotated, Literal
from pydantic import BaseModel, ConfigDict, Field, TypeAdapter, field_validator


class _StrictModel(BaseModel):
    model_config = ConfigDict(
        strict=True, extra="forbid", frozen=True, revalidate_instances="always"
    )


class ContextInfo(_StrictModel):
    context_id: str | None = None
    source: str | None = None
    type: str | None = None
    error: str | None = None


class _Envelope(_StrictModel):
    schema_version: Literal[1]
    kind: str
    pid: int
    context: ContextInfo
    api_id: str | None = None
    handler: str | None = None
    goroutine_id: int | None = None
    thread_id: int | None = None
    file: str | None = None
    line: int | None = None

    @field_validator("schema_version", mode="before")
    @classmethod
    def exact_version_type(cls, value: object) -> object:
        if type(value) is not int:
            raise ValueError("schema_version must be an integer")
        return value

    @property
    def context_key(self) -> tuple[int, str] | None:
        context_id = self.context.context_id
        return (self.pid, context_id) if context_id else None
```

```python
class RequestFields(_StrictModel):
    method: str | None = None
    path: Annotated[str, Field(min_length=1)] | None = None


class RequestMessage(RequestFields):
    raw_query: str | None = None


class SentRequestMessage(RequestMessage):
    host: str | None = None


class RoutedRequestMessage(RequestFields):
    pattern: str | None = None


class ResponseMessage(RequestFields):
    status_code: Annotated[int, Field(ge=0)] | None = None


class RequestSent(_Envelope):
    kind: Literal["send_request"]
    message: SentRequestMessage


class RequestReceived(_Envelope):
    kind: Literal["receive_request"]
    message: RequestMessage


class RequestRouted(_Envelope):
    kind: Literal["request_routed"]
    message: RoutedRequestMessage


class ResponseSent(_Envelope):
    kind: Literal["send_response"]
    message: ResponseMessage


class ResponseReceived(_Envelope):
    kind: Literal["receive_response"]
    message: ResponseMessage


Event = Annotated[
    RequestSent | RequestReceived | RequestRouted | ResponseSent | ResponseReceived,
    Field(discriminator="kind"),
]
EVENT_ADAPTER: TypeAdapter[Event] = TypeAdapter(Event)
```

- [ ] Extend the contract tests to all five kinds; request payloads reject statuses, response payloads reject host/query/pattern, and routing is its own event type. Test missing required envelope fields; null/empty context IDs; unknown kinds; nested extras; null versus zero debug fields; status values `None`, `0`, `200`, and `999`; rejection of boolean/string/float/negative canonical statuses; and model immutability.
- [ ] Run `uv run pytest -q tests/test_models.py`. Record red/green results and publish the corresponding field/null/version rules in `OUTPUT.md`.
- [ ] If commits are authorized, checkpoint:

```bash
git add src/contexttrack/models.py src/contexttrack/__init__.py src/contexttrack/py.typed tests/conftest.py tests/test_models.py OUTPUT.md
git commit -m "feat(contexttrack): define normalized event models"
```

**Gate:** A consumer can directly import and round-trip all five model variants without importing any graph code. Invalid canonical data is rejected rather than coerced.

### Task 2: Pure raw-record normalization

**Files:** Create `_raw.py`, `normalize.py`, `tests/test_normalize.py`; extend `tests/conftest.py` and the mapping section of `OUTPUT.md`.

**Consumes:** Task 1's models and `EVENT_ADAPTER`.

**Produces:** `normalize_record(record: object) -> Event`; private `_raw.RawEvent`, `RawContext`, `RawRequestID`, and `RawMessage`.

- [ ] Add the `raw_sent` fixture and focused failing tests:

```python
# Add to tests/conftest.py.
@pytest.fixture
def raw_sent():
    return {
        "kind": "Request sent",
        "pid": 42,
        "thread_id": 0,
        "context": {"context_id": "id:7"},
        "api_id": " API\n",
        "message": {
            "req.Method": "gEt",
            "req.URL.Host": "Höst:80",
            "req.URL.Path": "",
            "req.URL.RawQuery": "",
        },
        "request_id": {"method": "gEt", "host": "Höst:80", "path": ""},
    }
```

```python
# tests/test_normalize.py
from copy import deepcopy

from contexttrack.normalize import normalize_record


def test_outbound_label_precedence_and_no_input_mutation(raw_sent):
    raw_sent["message"]["req.URL.Path"] = "/different"
    original = deepcopy(raw_sent)
    event = normalize_record(raw_sent)
    assert (event.message.method, event.message.host, event.message.path) == (
        "gEt", "Höst:80", "/"
    )
    assert event.api_id == " API\n"
    assert event.thread_id == 0
    assert raw_sent == original


def test_missing_request_id_is_not_invented_from_message(raw_sent):
    del raw_sent["request_id"]
    event = normalize_record(raw_sent)
    assert (event.message.method, event.message.host, event.message.path) == (
        None, None, None
    )


def test_response_is_normalized_without_association():
    event = normalize_record({
        "kind": "Response received",
        "pid": 42,
        "context": {"error": "missing context ID"},
        "message": {"req.Method": "gEt", "req.URL.Path": "", "resp.StatusCode": "0200"},
    })
    assert event.kind == "receive_response"
    assert event.message.path == "/"
    assert event.message.status_code == 200
    assert event.api_id is None
    assert event.context_key is None
    assert event.context.error == "missing context ID"
```

- [ ] Run `uv run pytest -q tests/test_normalize.py` and verify the new interface is missing.
- [ ] Implement private strict raw models. Use Pydantic `validation_alias` for dotted raw keys, no `populate_by_name`, and `extra="forbid"`. Optional raw message and request-label fields have default `None`, but a before-validator rejects an explicitly supplied `None`; defaults remain unvalidated so omission is distinct. Apply the per-kind allowlists in Section 3 using `RawMessage.model_fields_set` (internal field names). Reject non-null `request_id` on non-send-request events. `RawContext` has the same four nullable fields as the public context; forbid `root_addr`.

```python
# Pattern for RawMessage aliases and present-value validation in _raw.py.
from pydantic import BaseModel, ConfigDict, Field, field_validator


class RawMessage(BaseModel):
    model_config = ConfigDict(strict=True, extra="forbid")
    method: str | None = Field(default=None, validation_alias="req.Method")
    host: str | None = Field(default=None, validation_alias="req.URL.Host")
    path: str | None = Field(default=None, validation_alias="req.URL.Path")
    raw_query: str | None = Field(default=None, validation_alias="req.URL.RawQuery")
    pattern: str | None = None
    code: str | None = None
    status_code: str | None = Field(default=None, validation_alias="resp.StatusCode")

    @field_validator("*", mode="before")
    @classmethod
    def reject_explicit_null(cls, value: object) -> object:
        if value is None:
            raise ValueError("present raw message fields must be strings")
        return value
```

`RawRequestID` has `method`, `host`, `path` with the same omitted-versus-null rule. `RawEvent` requires the five-kind literal, strict integer `pid`, and `RawMessage`; its optional fields mirror the existing Go envelope, with `context: RawContext | None` and `request_id: RawRequestID | None`.

- [ ] Implement explicit conversion, not global Pydantic coercion. The only value conversions are:

```python
def _path(value: str | None) -> str | None:
    return "/" if value == "" else value


def _status(value: str | None) -> int | None:
    if value is None:
        return None
    if not value.isdecimal():
        raise ValueError("status code must be a decimal string")
    return int(value)
```

Build a new dict from the validated raw envelope. The core conversion below uses the `_path` and `_status` helpers above; raw shape validation remains exclusively in `_raw.py`.

```python
from contexttrack._raw import RawContext, RawEvent, RawRequestID
from contexttrack.models import EVENT_ADAPTER, Event

_KINDS = {
    "Request sent": "send_request",
    "Request received": "receive_request",
    "Request routed": "request_routed",
    "Response sent": "send_response",
    "Response received": "receive_response",
}


def normalize_record(record: object) -> Event:
    raw = RawEvent.model_validate(record)
    message = raw.message
    normalized_message: dict[str, object]
    if raw.kind == "Request sent":
        label = raw.request_id or RawRequestID()
        normalized_message = {
            "method": label.method,
            "host": label.host,
            "path": _path(label.path),
            "raw_query": message.raw_query,
        }
    else:
        normalized_message = {"method": message.method, "path": _path(message.path)}
        if raw.kind == "Request received":
            normalized_message["raw_query"] = message.raw_query
        elif raw.kind == "Request routed":
            normalized_message["pattern"] = message.pattern
        else:
            status = message.code if raw.kind == "Response sent" else message.status_code
            normalized_message["status_code"] = _status(status)
    payload = raw.model_dump(exclude={"kind", "message", "context", "request_id"})
    payload.update(
        schema_version=1,
        kind=_KINDS[raw.kind],
        context=(raw.context or RawContext()).model_dump(),
        message=normalized_message,
    )
    return EVENT_ADAPTER.validate_python(payload)
```

The final adapter call enforces the output contract rather than raw input aliases. Keep conversion free of file I/O, warnings, request caches, and mutation.

- [ ] Add parameterized mapping tests for every kind and empty-path normalization. Add negative cases for unknown kind/key, malformed `pid`, null/string-invalid labels, forbidden per-kind fields, historical root addresses, invalid status values, and already-normalized input. Add positive cases for incomplete `message={}`, missing/null context, exact pattern syntax (`GET /items/{id}`, `:name`, `*path`), Unicode, and zero debug values.
- [ ] Run `uv run pytest -q tests/test_models.py tests/test_normalize.py`; update `OUTPUT.md` with the strict raw boundary and outbound-label precedence.
- [ ] If commits are authorized, checkpoint:

```bash
git add src/contexttrack/_raw.py src/contexttrack/normalize.py tests/conftest.py tests/test_normalize.py OUTPUT.md
git commit -m "feat(contexttrack): normalize raw HTTP event records"
```

**Gate:** One valid raw record becomes one typed canonical event. No test needs another record in order to normalize it; ambiguous or absent association evidence remains unassociated.

### Task 3: Strict streaming I/O and safe normalized output

**Files:** Create `io.py`, `tests/test_io.py`; document reader/writer behavior in `OUTPUT.md`.

**Consumes:** `normalize_record` and `EVENT_ADAPTER`.

**Produces:** `LocatedEvent`, `EventFileError`, `iter_raw_events`, `iter_events`, `write_events`, and `normalize_file` with Section 4's signatures.

- [ ] Add these failing tests first:

```python
import json
import pytest

from contexttrack.io import EventFileError, iter_events, iter_raw_events, normalize_file


def test_raw_reader_normalizes_in_memory(tmp_path, raw_sent):
    source = tmp_path / "raw.jsonl"
    source.write_text("\n" + json.dumps(raw_sent, ensure_ascii=False) + "\n", encoding="utf-8")
    before = source.read_bytes()
    [record] = iter_raw_events(source)
    assert record.event.kind == "send_request"
    assert record.event.message.path == "/"
    assert record.location == f"{source}:2"
    assert source.read_bytes() == before
    assert sorted(path.name for path in tmp_path.iterdir()) == ["raw.jsonl"]


def test_raw_record_error_has_raw_physical_line(tmp_path, raw_sent):
    source = tmp_path / "raw.jsonl"
    bad = raw_sent | {"kind": "unknown"}
    source.write_text(json.dumps(raw_sent) + "\n\n" + json.dumps(bad), encoding="utf-8")
    records = iter_raw_events(source)
    assert next(records).line == 1
    with pytest.raises(EventFileError, match=rf"{source}:3:"):
        next(records)


def test_count_order_duplicates_and_physical_lines(tmp_path, raw_sent):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    raw_line = json.dumps(raw_sent, ensure_ascii=False)
    source.write_text("\n" + raw_line + "\n\n" + raw_line, encoding="utf-8")
    before = source.read_bytes()
    assert normalize_file(source, output) == 2
    records = list(iter_events(output))
    assert len(records) == 2
    assert records[0].event == records[1].event
    assert [record.line for record in records] == [1, 2]
    assert records[0].location == f"{output}:1"
    assert source.read_bytes() == before
    assert output.read_bytes().endswith(b"\n")


def test_bad_later_record_does_not_publish_prefix(tmp_path, raw_sent):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent) + "\n\n{\n", encoding="utf-8")
    with pytest.raises(EventFileError, match=rf"{source}:3:"):
        normalize_file(source, output)
    assert not output.exists()
    assert sorted(path.name for path in tmp_path.iterdir()) == ["raw.jsonl"]


def test_never_overwrite_existing_file(tmp_path, raw_sent):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    output.write_bytes(b"keep")
    with pytest.raises(FileExistsError):
        normalize_file(source, output)
    assert output.read_bytes() == b"keep"
```

- [ ] Run `uv run pytest -q tests/test_io.py` and verify missing I/O interfaces are the failure.
- [ ] Implement the private byte-line JSON iterator with strict decoding and exact path/line errors. Use these `json.loads` hooks so normalization and normalized reading share the syntax rules:

```python
def _reject_constant(value: str) -> None:
    raise ValueError(f"invalid JSON constant {value}")


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key {key!r}")
        result[key] = value
    return result
```

Pass them as `parse_constant=_reject_constant` and `object_pairs_hook=_unique_object`. Wrap parsing/model errors at the input physical line; do not catch programming errors or `OSError` as malformed records.

- [ ] Implement `iter_raw_events` as the shared reader plus `normalize_record`, and `iter_events` as typed, location-aware normalized parsing. Implement `normalize_file(source, output)` as `write_events((record.event for record in iter_raw_events(source)), output)`. Use the following publication core; keep JSONL parsing/location wrapping in the shared reader rather than in this writer.

```python
import errno
import os
import tempfile
from collections.abc import Iterable
from pathlib import Path

from contexttrack.models import EVENT_ADAPTER, Event


def write_events(events: Iterable[Event], output: str | Path) -> int:
    output = Path(output)
    if os.path.lexists(output):
        raise FileExistsError(errno.EEXIST, "output already exists", str(output))
    temporary = None
    count = 0
    try:
        with tempfile.NamedTemporaryFile(
            mode="w", encoding="utf-8", newline="\n", dir=output.parent,
            prefix=".contexttrack-", suffix=".tmp", delete=False,
        ) as target:
            temporary = Path(target.name)
            for value in events:
                event = EVENT_ADAPTER.validate_python(value)
                target.write(event.model_dump_json(
                    by_alias=False, exclude_none=False, ensure_ascii=False,
                ) + "\n")
                count += 1
        os.link(temporary, output)
        return count
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
```

- [ ] Add failure/safety tests: empty/blank input; final line without newline; invalid UTF-8 on physical line 3; BOM; nested duplicate keys; `NaN`/`Infinity`; non-object JSON; normalized schema/type errors; wrong input format; a late model error; output equal to input; an existing or dangling output symlink; missing parent directory; simulated write failure; and another writer creating the destination immediately before `os.link`. Existing destination bytes must remain unchanged.
- [ ] Add a valid multi-kind synthetic stream, including an incomplete routed event and duplicate response events. Assert exact count/kind order and no enrichment of `api_id`, response host, or route pattern. Assert that `iter_raw_events(raw)` yields the same events, in the same order, as `iter_events` applied to `normalize_file(raw)`'s output. Assert that each reader rejects the other's format with `EventFileError` at the first nonblank physical line. Test `write_events` with model instances made invalid through `model_construct`, ensuring the public adapter revalidates them before serialization.
- [ ] Run `uv run pytest -q tests/test_models.py tests/test_normalize.py tests/test_io.py`.
- [ ] If commits are authorized, checkpoint:

```bash
git add src/contexttrack/io.py tests/test_io.py OUTPUT.md
git commit -m "feat(contexttrack): add strict JSONL normalization I/O"
```

**Gate:** Every published output is a complete stream of validated models; invalid input cannot leave a success-looking prefix. Every input failure has the correct path and physical line.

### Task 4: Installable normalization CLI

**Files:** Create `cli.py`, `__main__.py`, `tests/test_cli.py`; extend public exports; modify `pyproject.toml`, `uv.lock`, and `README.md`.

**Consumes:** Task 3's I/O API and `EVENT_ADAPTER.json_schema()`.

**Produces:** Installed `contexttrack normalize`, `contexttrack schema`, `python -m contexttrack`, and package version `0.2.0`.

- [ ] Add a subprocess test that exercises the actual Python entry point:

```python
import json
import subprocess
import sys

from contexttrack.models import EVENT_ADAPTER


def test_normalize_cli(tmp_path, raw_sent):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    result = subprocess.run(
        [sys.executable, "-m", "contexttrack", "normalize", str(source),
         "--output", str(output)],
        capture_output=True, text=True, check=False,
    )
    assert result.returncode == 0, result.stderr
    assert result.stdout == result.stderr == ""
    event = EVENT_ADAPTER.validate_json(output.read_text(encoding="utf-8"))
    assert event.kind == "send_request"
    assert event.message.path == "/"
```

- [ ] Run `uv run pytest -q tests/test_cli.py`; confirm the new CLI is absent.
- [ ] Implement the CLI in `cli.py` with no normalization logic of its own:

```python
import argparse
import json
import sys
from collections.abc import Sequence
from pathlib import Path

from contexttrack.io import EventFileError, normalize_file
from contexttrack.models import EVENT_ADAPTER


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="contexttrack")
    commands = parser.add_subparsers(dest="command", required=True)
    normalize = commands.add_parser("normalize", help="Normalize one completed raw capture")
    normalize.add_argument("input", type=Path)
    normalize.add_argument("--output", type=Path, required=True)
    commands.add_parser("schema", help="Print the normalized event JSON Schema")
    args = parser.parse_args(argv)
    try:
        if args.command == "schema":
            print(json.dumps(
                EVENT_ADAPTER.json_schema(), ensure_ascii=False, indent=2, sort_keys=True,
            ))
        else:
            normalize_file(args.input, args.output)
    except (EventFileError, OSError) as error:
        print(f"contexttrack: {error}", file=sys.stderr)
        return 2
    return 0
```

The module entry point is:

```python
from contexttrack.cli import main

if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] Update packaging narrowly:

```toml
# Values to replace in the existing pyproject.toml sections.
version = "0.2.0"
description = "Typed normalization of ContextTrack HTTP captures"
# Keep requires-python = ">=3.14".
dependencies = ["pydantic>=2.13.5,<3"]

[project.scripts]
contexttrack = "contexttrack.cli:main"
```

Regenerate the lockfile with `uv lock` and review it for unrelated upgrades. Do not run Tombi on `uv.lock`; packaging lint/format commands are `uvx tombi lint pyproject.toml` and `uvx tombi format --check pyproject.toml`. Export the documented models/functions without importing the CLI or reading environment variables at import time.

- [ ] Add subprocess tests for help, missing arguments, malformed JSON with physical line, unsupported version/format, missing input, output already existing, missing output directory, and schema JSON. Errors must have exit 2, empty stdout, useful stderr, and no traceback. Existing output must not change.
- [ ] Update `README.md` with install/sync commands, the separate raw-to-normalized workflow, consumer import examples for both `iter_raw_events` (raw capture, in memory) and `iter_events` (normalized file), and the explicit warning that current `node-query` and `analysis/` commands still expect raw captures. Do not replace their raw examples with normalized files.
- [ ] Run `uv sync --locked --dev`, `uv run pytest -q`, `uv run contexttrack --help`, and `uv run contexttrack schema`; parse the latter as JSON.
- [ ] If commits are authorized, checkpoint:

```bash
git add src/contexttrack/cli.py src/contexttrack/__main__.py src/contexttrack/__init__.py tests/test_cli.py pyproject.toml uv.lock README.md
git commit -m "feat(contexttrack): expose normalized JSONL CLI"
```

**Gate:** Both the installed console command and module command produce only the canonical format and share the library implementation. No Typer or other runtime dependency is added.

### Task 5: Contract publication and producer/distribution acceptance

**Files:** Create `schemas/contexttrack-event-v1.schema.json`, `tests/test_schema.py`; finalize `OUTPUT.md` and `README.md`. No Go or sibling source changes.

**Consumes:** The complete Python package, the committed consumer fixture, and the existing HTTP regression suite.

**Produces:** A generated schema, installed-wheel proof, raw-producer compatibility evidence, and a documented consumer migration boundary.

- [ ] Add the schema drift test before creating the snapshot:

```python
import json
from pathlib import Path

from contexttrack.models import EVENT_ADAPTER


def test_committed_schema_is_generated_from_models():
    path = Path(__file__).parents[1] / "schemas/contexttrack-event-v1.schema.json"
    assert json.loads(path.read_text(encoding="utf-8")) == EVENT_ADAPTER.json_schema()
```

- [ ] Run `uv run pytest -q tests/test_schema.py`, observe the missing artifact, then generate and retest it:

```bash
mkdir -p schemas
uv run contexttrack schema > schemas/contexttrack-event-v1.schema.json
uv run pytest -q tests/test_schema.py
```

- [ ] Run the read-only committed-fixture integration recipe in Section 8. Assert exactly 20 canonical records in the original order, including eight received-response hooks. Assert that `iter_raw_events` on the raw fixture yields identical events. Missing response `api_id` remains null. Do not assert deduplicated message/PMGraph counts as normalizer counts.
- [ ] Build a wheel and install it in a fresh environment **outside the repository**, using Pydantic 2.13.5 to test the advertised minimum. Confirm imports, `py.typed`, `contexttrack --help`, `contexttrack schema`, and parsing of the normalized committed fixture. The isolated install must not depend on `PYTHONPATH`, the checkout's editable install, `conftamer-cli`, or Go.
- [ ] Run the existing HTTP regression suite with the unchanged patch on a disposable Go 1.26.6 tree, then normalize and parse that fresh capture as in Section 8. Include unstamped-context records and HTTP/1, bundled HTTP/2, redirects, and repeated hooks; do not filter inconvenient records. If the native environment is unavailable, record this gate as blocked rather than claiming live producer compatibility.
- [ ] Run all Python tests, `uvx ty check`, `uvx ruff check`, `uvx ruff format --check`, `uvx tombi lint pyproject.toml`, `uvx tombi format --check pyproject.toml`, syntax checks, both unchanged raw analysis commands, and `git diff --check`. Pass changed/in-scope paths explicitly to the uvx tools; Tombi targets `.toml` files only and must never include `uv.lock` or copies of it. Record tool commands, scope, results, and any unavailable tools or out-of-scope failures. Review the generated schema and all changed/untracked files, including the uv-generated lockfile's dependency/metadata diff without reformatting it. Captures, wheel output, temporary environments, and generated diagnostic graphs stay outside Git.
- [ ] Finish `OUTPUT.md` with all mapping/error/version rules and the consumer handoff below. Document that normalized output can contain raw query strings and source paths; this release is not a redaction mechanism or proof of capture completeness.
- [ ] Prepare the complete-project audit inventory described in Section 5 and request human review. Record the reviewed revision or file hashes, per-file review status, findings/resolutions, dependency trust boundaries, and actual human sign-off in the execution handoff. Report this gate as pending/blocked if human review is unavailable; do not substitute test results or agent review.
- [ ] If commits are authorized, checkpoint:

```bash
git add schemas/contexttrack-event-v1.schema.json tests/test_schema.py OUTPUT.md README.md
git commit -m "test(contexttrack): verify normalized producer contract"
```

**Gate:** The installed package parses its own actual normalized output, the schema is generated from the same models, and verification distinguishes synthetic/committed-fixture evidence from native producer evidence.

## 7. Separately authorized `node-query` handoff

Do not implement these sibling changes as part of this plan's producer work. Once a tested ContextTrack wheel is available, move the consumer to **one raw-input path** whose parsing is done by the producer package. The consumer keeps request association and PMGraph construction; it deletes its own raw parser.

1. Add and pin the tested `contexttrack` artifact in the consumer's `pyproject.toml` and lockfile. Import the models and readers rather than reproducing them. Registry upload or a repository dependency source requires its own release decision.
2. Keep the public interface unchanged: `load_contexttrack(path, *, module_id)` and `conftamer build INPUT --module-id ID --output NEW` still take one completed **raw** capture. Add no `input_format` parameter, `--input-format` option, format auto-detection, second reader, or required `contexttrack normalize` pre-step. Reading normalized files is a later, separately justified feature, if ever needed.
3. Replace `_read_events` and its helpers (`_object`, `_string`, `_normalized_path`, `_reject_constant`, the `json` import, and the unknown-kind warning) with an adapter over `contexttrack.io.iter_raw_events`. It copies typed fields into the internal mutable `_Event`, using `record.location` for warnings and `event.context_key` for grouping:

   ```python
   from contexttrack.io import iter_raw_events


   def _read_events(path: Path) -> Iterator[_Event]:
       for record in iter_raw_events(path):
           event, message = record.event, record.event.message
           yield _Event(
               kind=event.kind,
               context_key=event.context_key,
               method=message.method,
               path=message.path,
               host=getattr(message, "host", None),
               api_id=event.api_id,
               location=record.location,
               pattern=getattr(message, "pattern", None),
               status_code=getattr(message, "status_code", None),
           )
   ```

   Change `_correlate`'s kind literals and `_REQUEST_KIND_BY_EVENT` to the canonical names (`send_request`, `receive_request`, `request_routed`, `send_response`, `receive_response`). These names already match `Message.kind`. Use `event.kind` directly and delete `_KINDS`. The public Pydantic events stay immutable; association state (`pattern`, `api_id`, `matched_request`, `routing_ambiguous`) lives only in the consumer's `_Event`.
4. Leave conservative association rules unchanged: unique earlier request with matching `(pid, context_id)`, method, and concrete path; no rewritten-path reconstruction; ambiguous routes fall back; unmatched/ambiguous responses are omitted with warnings. `request_id` endpoint precedence now arrives through the already-normalized `send_request.message` fields. Do not try to read a host from a response model. Incomplete-evidence warnings stay as they are (for example, a null `request_id` or missing path still warns and drops the node).
5. Continue requiring caller-supplied `module_id` and new output files. Graph node identity, occurrence handling, and receive-to-later-send influence stay consumer concerns. API IDs never become module IDs.
6. Accept and document the **input-validation behavior change** in the consumer README. Under Sections 3-4, the producer's raw boundary is stricter than the legacy importer. These now raise instead of warning or being ignored: unknown kinds, unknown/extra raw keys (envelope or message), per-kind forbidden message keys, explicit null for string fields, non-null `request_id` outside `Request sent`, `context.root_addr`, duplicate JSON keys, and a UTF-8 BOM. All malformed-input failures surface as `EventFileError` (a `ValueError`) naming `path:line`. The legacy `TypeError`/`ValueError` split is dropped. `conftamer build` already maps `ValueError` to exit 2, so CLI exit status is unchanged.
7. Update consumer tests instead of adding parity tests:
   - Rewrite `test_bad_input_diagnostics`: malformed/type-invalid cases expect `EventFileError` matching `path:3:`. The unknown-kind case moves from warning to error. Keep the two incomplete-evidence warning cases (`req.URL.Path` missing; `request_id=None`). Keep only a few error-wrapping smoke cases; exhaustive raw validation is covered by the producer's `tests/test_normalize.py` and `tests/test_io.py`.
   - In `test_context_scoped_occurrence_influence`, replace `sent["message"] = {"unconsumed": False}` with `sent["message"] = {}`. The unknown key is now an error, while an empty message still exercises "labels come from `request_id`".
   - Leave the raw `event()` helper, routing, response-matching, missing-label, context-scoping, UTF-8, blank-capture, `module_id`, and scrape-fixture tests otherwise unchanged. They must pass on the migrated code: four scrape nodes and edges `{("n1", "n2")}`. This is a consumer result, not a reduction of the normalizer's 20 events.
8. Expected size: `src/conftamer/contexttrack.py` goes from 271 to roughly 165-175 lines (an estimate; report the actual `wc -l`), with `_correlate` and graph construction unchanged. Run `tests/test_contexttrack.py`, `tests/test_cli.py`, and a `build` of the committed scrape fixture into a **new** output file in an authorized disposable `node-query` checkout. Migrating the exploratory `analysis/` scripts remains a separate follow-up.

## 8. Verification recipes for execution

These commands are planned acceptance steps, not claims that the new implementation currently exists. Run from the implementation worktree's `contexttrack/` directory.

### 8.1 Committed fixture and raw-analysis regression

Set `CONSUMER_REPO` to the existing consumer repository path before entering a differently located worktree; only read it with Git. The command fails clearly if it is not supplied.

```bash
set -euo pipefail
: "${CONSUMER_REPO:?Set CONSUMER_REPO to the existing conftamer-cli repository}"
WORK=$(mktemp -d /tmp/contexttrack-normalization-check.XXXXXX)
export WORK
git -C "$CONSUMER_REPO" show a173121d6cec1ebf26394714c356a0eaa17149fa:examples/contexttrack/prometheus/scrape-ok.jsonl > "$WORK/raw.jsonl"

uv sync --locked --dev
uv run pytest -q
PYTHONPYCACHEPREFIX="$WORK/pycache" python3 -m py_compile analysis/*.py
python3 analysis/group_by_context.py "$WORK/raw.jsonl"
python3 analysis/message_graph.py "$WORK/raw.jsonl" --format text

uv run contexttrack normalize "$WORK/raw.jsonl" --output "$WORK/normalized.jsonl"
uv run python - <<'PY'
import os
from collections import Counter
from pathlib import Path
from contexttrack.io import iter_events, iter_raw_events

path = Path(os.environ["WORK"]) / "normalized.jsonl"
events = [record.event for record in iter_events(path)]
assert [r.event for r in iter_raw_events(Path(os.environ["WORK"]) / "raw.jsonl")] == events
assert len(events) == 20
assert Counter(event.kind for event in events) == {
    "send_request": 4, "receive_request": 4,
    "send_response": 4, "receive_response": 8,
}
assert [event.kind for event in events] == [
    "send_request", "receive_request", "send_response", "receive_response", "receive_response"
] * 4
assert all(event.message.path == "/" for event in events)
assert sum(event.kind == "receive_response" and event.api_id is None for event in events) == 4
PY
```

### 8.2 Installed-wheel verification

Continue in the same shell with `WORK` from Section 8.1:

```bash
set -euo pipefail
uv build --wheel --out-dir "$WORK/dist"
uv venv --python 3.14 "$WORK/venv"
uv pip install --python "$WORK/venv/bin/python" 'pydantic==2.13.5' "$WORK"/dist/*.whl
(
  cd "$WORK"
  env -u PYTHONPATH "$WORK/venv/bin/contexttrack" --help
  env -u PYTHONPATH "$WORK/venv/bin/contexttrack" schema > "$WORK/installed-schema.json"
  env -u PYTHONPATH "$WORK/venv/bin/python" - <<'PY'
import json
from importlib.resources import files
from contexttrack.io import iter_events, iter_raw_events
from contexttrack.models import EVENT_ADAPTER

assert files("contexttrack").joinpath("py.typed").is_file()
assert len(list(iter_events("normalized.jsonl"))) == 20
assert len(list(iter_raw_events("raw.jsonl"))) == 20
with open("installed-schema.json", encoding="utf-8") as source:
    assert json.load(source) == EVENT_ADAPTER.json_schema()
PY
)
```

### 8.3 Fresh native capture with the unchanged patch

`CLEAN_GO` must name a clean Go 1.26.6 distribution directory. The command copies it; it never patches that source directory. No Go source edit is part of this task.

```bash
set -euo pipefail
: "${CLEAN_GO:?Set CLEAN_GO to a clean Go 1.26.6 distribution directory}"
REPO=$PWD
NATIVE=$(mktemp -d /tmp/contexttrack-normalization-native.XXXXXX)
VERSION=$(env -u GOROOT GOTOOLCHAIN=local "$CLEAN_GO/bin/go" version)
printf '%s\n' "$VERSION"
case "$VERSION" in
  "go version go1.26.6 "*) ;;
  *) printf 'Expected Go 1.26.6; refusing to patch.\n' >&2; exit 1 ;;
esac
mkdir "$NATIVE/go-conftamer"
# Copy directory contents, not a root symlink that might lead back to an installed Go.
cp -a "$CLEAN_GO/." "$NATIVE/go-conftamer/"
(
  cd "$NATIVE/go-conftamer"
  patch --dry-run -p4 < "$REPO/go-inlibrary.patch"
  patch -p4 < "$REPO/go-inlibrary.patch"
)
PATCHED_GO="$NATIVE/go-conftamer/bin/go"
ACTUAL_GOROOT=$(env -u GOROOT GOTOOLCHAIN=local "$PATCHED_GO" env GOROOT)
printf '%s\n' "$ACTUAL_GOROOT"
test "$ACTUAL_GOROOT" = "$NATIVE/go-conftamer"
(
  cd "$REPO/tests/httpcapture"
  env -u GOROOT GOTOOLCHAIN=local CONFTAMER_EVENTS="$NATIVE/raw.jsonl" \
    "$PATCHED_GO" test -race -count=1 -v ./...
)
uv run contexttrack normalize "$NATIVE/raw.jsonl" --output "$NATIVE/normalized.jsonl"
CONTEXTTRACK_CHECK_FILE="$NATIVE/normalized.jsonl" uv run python - <<'PY'
import os
from collections import Counter
from contexttrack.io import iter_events

events = [record.event for record in iter_events(os.environ["CONTEXTTRACK_CHECK_FILE"])]
assert events
assert {event.kind for event in events} == {
    "send_request", "receive_request", "request_routed", "send_response", "receive_response"
}
assert any(event.context.error == "missing context ID" for event in events)
print(Counter(event.kind for event in events))
PY
```

Verify the printed Go version is exactly the README's Go 1.26.6 and GOROOT is the copied tree before running the tests; inspect the enabled-capture diagnostic. Stop on a version mismatch or failed patch hunk. Because this feature leaves the patch unchanged, upstream `net/http` short tests are not a new mandatory change gate; if execution unexpectedly needs any patch change, stop and revise the plan, regenerate from clean/modified trees, and apply the agent guide's tracing-on upstream test requirement.

## 9. Completion checklist and planning evidence

- [ ] Five public typed event variants; strict nested validation and exact version handling.
- [ ] Explicit per-field mapping, no inferred associations, complete repeated/incomplete evidence retention.
- [ ] Model-based JSONL serialization and a shared, location-aware reader.
- [ ] Public in-memory raw reader `iter_raw_events`, producing the same events as `normalize_file` followed by `iter_events`.
- [ ] Safe new-file CLI output; no changed raw capture behavior.
- [ ] Installable wheel and stable public imports; schema generated from the models.
- [ ] Synthetic unit tests, committed-fixture round trip, and native capture gate reported separately.
- [ ] `uvx ty`, `uvx ruff` (lint and formatting), and `uvx tombi` (lint and formatting for `.toml` files only; `uv.lock` excluded) checks run; commands, scope, results, and any blocked/out-of-scope findings recorded.
- [ ] Consumer migration explicitly remains unimplemented until its separate authorized work passes.
- [ ] Final diff contains no unrelated source edits, real captures, temporary environments, wheels, or graphs.
- [ ] Humans have read and audited every file in the complete-project inventory against the final revision or file hashes; findings are resolved, dependency trust boundaries are explicit, and human sign-off is recorded. Automated checks or agent reviews alone cannot satisfy this gate.

Planning-time checks actually performed on the existing baseline:

- Read the current producer patch, README, analysis scripts, HTTP capture tests, and the committed `node-query` README/importer/models/tests/fixture.
- Read the paper's relevant §§4-5 and visually inspected Figures 2-3.
- Python syntax checks and both raw analysis commands succeeded on the committed 20-record scrape fixture. The raw scripts reported eight context groups; their graph output remains diagnostic, not the normalized format.
- Confirmed with the installed Pydantic 2.13.5 that strict `Literal[1]` alone accepts boolean `True` and float `1.0`; the plan includes a specific regression and validator.
- No new models, normalizer, CLI, consumer migration, or native capture test were implemented or executed during planning.
