# ContextTrack Output Cleanup Implementation Plan

> **SUPERSEDED FOR PLANNING on 2026-09-18** by [ContextTrack v2 Revision Implementation Plan](2026-09-18-contexttrack-v2-revision.md). Retained as historical draft evidence. Do not execute this plan or combine its schema/semantics with the replacement. The replacement still requires implementation approval.

> **For agentic workers:** After explicit human approval, use the `executing-plans` skill to implement this plan task by task. Stop at the human review gates. Do not delegate, commit, or push unless separately authorized.
>
> **Status: DRAFT FOR HUMAN AUDIT. Neither the contract nor implementation is approved.**

**Goal:** Make ContextTrack captures straightforward to validate and consume, without reconstructing request identity or deduplicating instrumentation hooks.

**Architecture:** Emit one typed JSONL format directly from the producer, with exact request references and one file per process. Keep late routing metadata, but attach it by request ID. Update this repository's inspection scripts; give the sibling PMGraph consumer the same contract and fixtures, not another normalization pipeline.

**Tech stack:** Patched Go `go1.26.6`; Go standard library; Python inspection scripts and `unittest`. JSON Schema validation is a test-only dependency (`jsonschema==4.25.1`).

**Spec:** The user's request for human-auditable, readable, simple output cleanup with a clean break. Section 2 of this document is the proposed specification; it requires approval before implementation.

## Global constraints

- Plan only until humans approve the decisions below.
- Prefer explicit structs, ordinary functions, and small tests over frameworks.
- One new format only: no old-format reader, dual emission, converter, or compatibility aliases.
- No separate raw/normalized streams, normalization service, or `normalize` command.
- No changes to AppGraph stitching, parameter tracking, PMGraph node-ID policy, or API-ID inference algorithms.
- No new protocols or routers beyond standard-library HTTP/1, bundled HTTP/2, ServeMux, and the existing Prometheus router adapter.
- Preserve HTTP behavior and context-influence semantics. Logging must not change HTTP return values, cancellation, or request bodies.
- Existing sibling workspaces and captures are read-only references. Execute producer changes in a fresh Go checkout, not `../../run-ctxtrk/go-conftamer`.
- The sibling consumer needs its own approved implementation change. Approval of this producer plan does not authorize editing that repository.

## 1. Human decisions to approve

Each row is a recommendation, not an already-made project decision.

| Decision | Proposed choice | Trade-off for reviewers |
| --- | --- | --- |
| D1: Output boundary | Typed events with `schema_version: 2`; reject other formats | Every consumer must change or use freshly supported tooling |
| D2: Capture storage | Capture directory containing one exclusive JSONL file per process | Directory input replaces a shared append-only file; no cross-process write interleaving |
| D3: Request identity | One local request ID per transport attempt or server request | Retries and redirect hops are distinct occurrences; IDs are not network trace IDs |
| D4: Response ownership | One final response event at the receive/commit hooks described below | Interim 1xx responses and body completion are not represented |
| D5: Routing | Exact request references; publish a full pattern only when its path mapping is known | Unknown rewrites fall back to the original concrete path, potentially producing more nodes |
| D6: API ownership | Keep the existing guess, explicitly label its source, and copy it from request state | This exposes uncertainty; it does not fix API identification |
| D7: Diagnostics | Omit query strings, goroutine/thread IDs, source file/line, and timestamps from the public event format | Smaller, less sensitive captures; those diagnostics would require a separate future proposal |

**Gate A:** Producer maintainer and downstream maintainer approve or amend D1–D7, the schema example, and the hook table. Record their names/date and decisions in the review discussion. Do not start code changes before this gate.

### Evidence behind this proposal

Inspected producer revision: `conftamer` `222c9b6`; sibling consumer: `conftamer-cli` `4b61650` on `contexttrack-import`.

- `go-inlibrary.patch` contains the producer, including two client response observations: transport and `Client.do`.
- Its bundled HTTP/2 changes cover server events, but do not currently add a wire-level client response hook. Removing the client-level hook alone would lose that coverage.
- Normal HTTP/2 dispatch also reaches `serverHandler.ServeHTTP` through Go's ALPN wrapper. The two receive entry points must reuse server exchange state, not log a second receipt.
- Its `conftamerContext` can invent an ID while logging without storing it in the context. An unknown context must instead remain unknown.
- `analysis/message_graph.py` reconstructs routes using path suffixes, attributes responses by context/method/path, and deduplicates occurrences before constructing edges.
- `../../conftamer-cli/src/conftamer/contexttrack.py` makes conservative preceding-request matches because the input lacks occurrence IDs.
- The sibling's untracked documentation describes more CLI functionality than its tracked source implements. Do not assume build/query/export commands exist.
- The Go patch uses machine-specific paths and has inconsistent hunk counts. Regenerate it against the pinned base; do not hand-edit its hunk headers.

## 2. Proposed contract

### 2.1 A capture is a directory, not a shared file

When enabled, require both variables:

```text
CONFTAMER_EVENTS_DIR=/absolute/path/to/capture
CONFTAMER_CAPTURE_ID=scrape-smoke
```

The runner creates the directory and supplies one nonempty capture ID to its child processes. Each process generates a random 128-bit `process_id`, encoded as 32 lowercase hexadecimal characters, and exclusively creates `<process_id>.jsonl` with mode `0600`.

- No process appends to another process's file or reopens an earlier capture file.
- A process-local mutex protects sequence assignment, encoding, and the complete line write.
- `seq` starts at 1 and increases by one for each successfully written record.
- Record order is logger observation order, not a total order of execution or a proof of causality. There is no ordering claim between processes.
- If configuration, file creation, encoding, or writing fails, report `conftamer: error: ...` to stderr and disable that process's logger. Do not silently continue with gaps or fabricated data; do not change the application's exit status.
- Reject invalid UTF-8 in logged strings before encoding; Go's JSON encoder would otherwise silently replace it. Reject unpaired surrogate escapes in consumer input for the same reason.
- Check short writes as failures. A runner must inspect capture diagnostics; valid JSON alone does not prove that capture completed.
- An empty process file is allowed. There is no shutdown/completeness marker: a valid prefix of a capture may still be incomplete.

When `CONFTAMER_EVENTS_DIR` is unset, instrumentation is disabled. The old `CONFTAMER_EVENTS` setting is not part of this contract.

### 2.2 One envelope and five event kinds

Every record has all the following fields. Nullable fields use explicit JSON `null`, not omission, `"?"`, empty sentinel objects, or a generated replacement identity. Objects are closed: unrecognized fields and kinds are errors under this schema version.

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | integer, exactly `2` | Format discriminator, not a compatibility dispatcher |
| `capture_id` | nonempty string | Runner-supplied capture identity |
| `process_id` | 32-character lowercase hex string | Process instance, never an OS PID |
| `pid` | positive integer | OS process ID for human diagnostics only |
| `seq` | positive integer | Per-process record order |
| `kind` | enum below | Semantic observation or routing metadata |
| `request_id` | string matching `^r[1-9][0-9]*$` | Positive process-local occurrence counter, such as `r7` |
| `context_id` | string matching `^c[1-9][0-9]*$`, or null | Separate positive context counter, such as `c3`; null means unknown |
| `http` | object below | Immutable request label plus an optional response status |
| `api` | object below, or null | Request-origin API guess and evidence |
| `route` | object below, or null | Present only on routing metadata |

Kinds: `request_sent`, `request_received`, `request_routed`, `response_sent`, `response_received`.

Example of a server response:

```json
{
  "schema_version": 2,
  "capture_id": "scrape-smoke",
  "process_id": "0f20a187a2c84b629f469c0e3a184dab",
  "pid": 63118,
  "seq": 4,
  "kind": "response_sent",
  "request_id": "r2",
  "context_id": "c2",
  "http": {
    "method": "GET",
    "host": "127.0.0.1:9090",
    "path": "/items/7",
    "original_path": "/items/7",
    "status_code": 200
  },
  "api": null,
  "route": null
}
```

`http` fields:

- `method`: nonempty string. Use `GET` for Go's empty-method default; otherwise preserve spelling and case.
- `host`: client `URL.Host`; server `Request.Host`, or null if absent. Preserve case and port; do not resolve or canonicalize hosts. Client host must be nonempty. This does not model a client `Host` override separately.
- `original_path`: exactly the origin request's Go `URL.Path`, including an empty string. This is not `URL.RawPath` or the original bytes on the wire.
- `path`: `original_path`, except an empty string becomes `/`. No additional path cleaning, decoding, case folding, or query inclusion.
- `status_code`: integer on responses, null otherwise. Terminal statuses are `101` for HTTP/1 switching protocols, or `200`–`999`, matching Go's allowance for three-digit statuses. Do not emit ordinary informational 1xx responses.

`api`, when non-null, has exactly four nonempty strings:

```json
{
  "id": "github.com/prometheus",
  "source": "stack_heuristic",
  "package": "github.com/prometheus/prometheus/scrape",
  "symbol": "github.com/prometheus/prometheus/scrape.(*targetScraper).scrape"
}
```

`source` is `stack_heuristic` or `handler_heuristic`. Preserve the current identifier derivation; do not introduce confidence scores, registries, overrides, or a new resolver. If evidence cannot be obtained, use `api: null`. Module identity remains an explicit downstream input; neither this API guess nor a process boundary identifies the module represented by a test capture.

`route`, when non-null, has exactly:

```json
{
  "dialect": "go_serve_mux",
  "pattern": "GET /items/{id}",
  "matched_path": "/items/7",
  "full_pattern": "GET /items/{id}"
}
```

- Dialects: `go_serve_mux` for current ServeMux syntax, `go_serve_mux_121` for Go's existing compatibility router, and `httprouter` for the Prometheus adapter.
- `pattern` is the nonempty, unchanged router pattern, including any method/host syntax.
- `matched_path` is the Go `URL.Path` seen at that router; it can be empty.
- `full_pattern` is a verified pattern in the original request's path space, or null. It is never obtained by guessing a suffix relationship.
- `request_routed` requires a route object; all other kinds require `route: null`.

Do not add redundant event IDs, response IDs, context sequence counters, or observation-point fields. `(capture_id, process_id, seq)` identifies an event; the hook table defines its observation point.

### 2.3 Request identity is separate from context identity

Association key:

```python
def request_key(event: dict) -> tuple[str, str, str]:
    return event["capture_id"], event["process_id"], event["request_id"]
```

Influence key is `(capture_id, process_id, context_id)` only when `context_id` is non-null. Neither identity is transmitted in HTTP headers or used as a PMGraph semantic node ID.

Rules:

1. Assign a fresh request ID to every client transport attempt and every server request. Reusing a request object, context, method, or URL never reuses a request ID.
2. Internal retries and redirect hops get fresh request IDs. Preserve the inherited context ID across those attempts; do not add a separate logical-operation hierarchy.
3. Keep request state beside the exchange: server `Request`, HTTP/1 `transportRequest`, and HTTP/2 `http2clientStream`. Do not store occurrence IDs in `context.Value` or a global request-address map.
4. Server request copies made by `WithContext`, `Clone`, and `StripPrefix` retain the server exchange pointer. Starting an outbound attempt from such a copy must allocate a different exchange, never reuse the server's ID.
5. Snapshot method, host, original/normalized path, context ID, and API evidence at the request observation. Route/response records copy those values; the only HTTP field that changes is response status.
6. Context stamping remains at origins, including a consistent fallback for direct `Transport.RoundTrip`. Reading context metadata must never allocate an ID. An unrecognized context yields null; null contexts never form an influence group.
7. Preserve cancellation and `Response.Request` behavior. Any client bookkeeping copy must retain Go's original-request references used by cancellation and response attribution.

### 2.4 One owner for each semantic observation

Anchors below refer to the pinned Go source, not line numbers in the current patch.

| Event | Authoritative hook | Required behavior |
| --- | --- | --- |
| HTTP/1 request sent | `persistConn.roundTrip`, before publishing the attempt to transport goroutines | Create/store a new exchange and log once |
| HTTP/2 request sent | `http2ClientConn.roundTrip`, before starting `cs.doRequest` | Create/store a new exchange per client stream attempt |
| HTTP/1 request received | `serverHandler.ServeHTTP`, before handler dispatch | Attach/log only if this request has no server exchange yet |
| HTTP/2 request received | `http2serverConn.runHandler`, before handler dispatch | Attach/log first; the later `serverHandler` entry must reuse it without emission |
| HTTP/1 response received | `persistConn.readResponse`, after the informational-response loop | Log the accepted final headers using `rc.treq`'s exchange |
| HTTP/2 response received | `http2clientConnReadLoop.processHeaders`, after a non-null successful `handleResponse`, before notifying `respHeaderRecv` | Log final headers using the stream exchange; trailers are not responses |
| HTTP/1 response sent | `response.WriteHeader`, at the accepted final-header state transition | After native validation/duplicate/informational checks; cover implicit 200 |
| HTTP/2 response sent | `http2responseWriterState.writeHeader`, at the accepted final-header state transition | Move below the public wrapper so empty-handler and implicit responses are covered |

Delete response emission in `Client.do` and the old transport-entry request emission. Capture API evidence on the request goroutine before HTTP/1 or HTTP/2 dispatches work to other goroutines. Server receive entry points share a small attach-once helper keyed by the request's exchange pointer, not a deduplication cache. HTTP/2 wrapper handlers may produce `api: null`; do not guess a better API later or mutate the emitted origin snapshot. Review this limitation explicitly at Gate B.

A sent request means an attempt entered the transport, not that bytes reached a peer. A received response means final headers were accepted, not that its body completed. Failed or canceled attempts can have no response. Upgraded/tunneled traffic, request-body messages, mock round trippers, and external `x/net/http2` remain outside this contract.

Do not mask instrumentation bugs with a generic deduplication cache. Native lifecycle guards should ensure one final response per exchange; duplicate final responses must fail capture validation.

### 2.5 Routing without downstream reconstruction

Change the adapter API to:

```go
func ConftamerLogRouted(r *Request, dialect, pattern string)
```

It reads the already-attached server exchange. Calls for an untraced request produce no event, not an invented request identity.

For full patterns:

- Preserve a stripped-prefix string by value on server request copies. Extend it only at successful calls through the patched `http.StripPrefix`.
- Publish a full pattern only if `original_path == stripped_prefix + matched_path`, and the request method/authority still agree with the origin snapshot.
- Insert the known prefix at the first `/` in the router's pattern, preserving its method/host prefix. A pattern with no `/` cannot be verified.
- An arbitrary rewrite produces `full_pattern: null`, while retaining the actual pattern, dialect, and matched path for inspection.
- Log routing immediately; do not delay message events until handler completion or buffer an entire request lifetime.

Consumers select the last routing observation for that exact request. If its full pattern is null, use the original concrete path, not an earlier coarse pattern. Preserve the dialect alongside any pattern used as an identity. This last-observation rule supports ordinary serial routing chains; it does not claim to reconstruct arbitrary concurrent handler dispatch.

## 3. Planned file changes

All paths and commands are relative to the `contexttrack/` directory unless
marked as a consumer handoff or parent-repository path.

| File | Responsibility |
| --- | --- |
| `OUTPUT.md` | Concise approved contract copied from Section 2; examples and limitations |
| `event.schema.json` | Handwritten JSON Schema Draft 2020-12 for one record; no schema generator |
| `_stdlib/net/http/conftamer.go` | Ordinary, reviewable Go source for types, identity, writer, and hook helpers |
| `_stdlib/net/http/conftamer_internal_test.go` | Private helper/writer tests in package `http` |
| `_stdlib/net/http/conftamer_test.go` | Black-box capture tests in package `http_test` |
| `go-inlibrary.patch` | Regenerated, relative-path, hooks-only patch against `go1.26.6` |
| `apply-go-patch.sh` | Check the Go base, apply hooks, and copy the three overlay files; no downloads or builds |
| `prometheus-common-route.patch` | Update the existing adapter call; rebase against `common` `v0.70.1` |
| `analysis/event_io.py` | Strict new-format file/directory loading and trace-integrity checks |
| `analysis/group_by_context.py` | Display typed events grouped by scoped context |
| `analysis/message_graph.py` | Exact route attachment and receive-to-later-send diagnostic graph |
| `tests/test_output.py` | Schema, reader, routing, graph, and command-boundary tests |
| `testdata/basic.jsonl` | Small synthetic five-record contract fixture described below |
| `testdata/chained.jsonl` | Synthetic receive/send/receive/send influence fixture |
| `../.gitignore` | Permit tracked `contexttrack/testdata/*.jsonl`, despite the existing global JSONL ignore |
| `README.md` | New capture commands and links; one supported patch approach |
| Delete `go-inlibrary-optional.patch` | Retire the alternative producer that would emit the abandoned format |

The `_stdlib` directory is intentionally excluded by Go's recursive package discovery. Its files compile only after copying into the real `net/http` package. This avoids a pretend independently buildable Go package and keeps most implementation code out of a diff file.

**Consumer handoff, not edits authorized here:** `../../conftamer-cli/src/conftamer/contexttrack.py`, its tests, and its reviewed importer contract. That owner should plan a direct replacement reader, not a legacy adapter. No CLI/export/stitching work is required by this handoff.

## 4. Implementation tasks and review checkpoints

All checkboxes below are intentionally unchecked. Commands are future verification steps, not claims of tests already run.

### Task 1: Freeze the schema and small examples

**Files:** `OUTPUT.md`, `event.schema.json`, `testdata/{basic,chained}.jsonl`, `tests/test_output.py`, `../.gitignore`.

**Interfaces:** JSON records from Section 2. The schema checks individual record shape; the reader in Task 4 checks relationships between records.

- [ ] After Gate A, write the schema tests first. They must reject wrong versions, unknown kinds/fields, missing required fields, boolean IDs/sequences/statuses, string status codes, invalid API sources, and route objects on message events.
- [ ] Construct `basic.jsonl` with one process and these exact logical records. Give all related events identical origin metadata; use `/items/7` and the verified `/items/{id}` pattern.

| seq | kind | request_id | context_id | status |
| --- | --- | --- | --- | --- |
| 1 | request_sent | r1 | c1 | null |
| 2 | request_received | r2 | c2 | null |
| 3 | request_routed | r2 | c2 | null |
| 4 | response_sent | r2 | c2 | 200 |
| 5 | response_received | r1 | c1 | 200 |

- [ ] Construct `chained.jsonl`: receive request `r1`, send request `r2`, receive response `r2`, send response `r1`, all in context `c1`, sequences 1–4. Use distinct paths `/front` and `/back`. This is a synthetic local influence example, not a distributed trace.
- [ ] Add an explicit `../.gitignore` exception: `!/contexttrack/testdata/*.jsonl`.
- [ ] Run the tests and observe their failures before adding the schema. Then add the closed-object schema and verify its own validity plus positive and negative fixtures. Do not treat a missing-file failure alone as proof of field validation.

Representative test content:

```python
from copy import deepcopy
import json
from pathlib import Path
import unittest
from jsonschema import Draft202012Validator, ValidationError

ROOT = Path(__file__).resolve().parents[1]

class OutputSchemaTests(unittest.TestCase):
    def test_response_status_is_an_integer(self):
        schema = json.loads((ROOT / "event.schema.json").read_text())
        Draft202012Validator.check_schema(schema)
        validator = Draft202012Validator(schema)
        events = [json.loads(line) for line in
                  (ROOT / "testdata/basic.jsonl").read_text().splitlines()]
        for event in events:
            validator.validate(event)
        response = deepcopy(events[4])
        response["http"]["status_code"] = "200"
        with self.assertRaises(ValidationError):
            validator.validate(response)
```

Run throughout this plan:

```bash
uv run --no-project --python 3.14 --with jsonschema==4.25.1 \
  python -m unittest discover -s tests -v
```

**Deliverable/review:** Humans can read five records and determine every request/response association without running a tool. Check that synthetic fixtures are labeled as synthetic.

### Task 2: Implement typed producer events and exact exchange lifetimes

**Files:** New `_stdlib/net/http/` files and `apply-go-patch.sh`; regenerated `go-inlibrary.patch`.

**Interfaces:** Define `conftamerEvent`, `conftamerHTTP`, `conftamerAPI`, and `conftamerRoute` as direct Go structs for Section 2. Use pointers for nullable JSON values, without `omitempty`. Define an immutable `conftamerExchange` holding request ID, context ID, HTTP origin fields, and API evidence.

Hook helpers:

```go
func conftamerContextID(ctx context.Context) *string
func conftamerBegin(kind string, r *Request, handler any) *conftamerExchange
func conftamerReceive(r *Request, handler any)
func conftamerResponse(kind string, exchange *conftamerExchange, code int)
```

`conftamerBegin` snapshots metadata, allocates an occurrence ID, and emits the request. Client calls infer the caller on the sending goroutine. `conftamerReceive` calls it with `request_received` only when the server request has no exchange pointer, then attaches the result; the nested HTTP/2/common-server dispatch must not allocate again. `conftamerResponse` copies immutable exchange state and adds only the status.

Writer boundary for isolated tests:

```go
func newConftamerLogger(w io.Writer, captureID, processID string, pid int) *conftamerLogger
func (logger *conftamerLogger) write(event conftamerEvent) error
```

The logger owns `w`, identity fields, a mutex, successful sequence count, and a latched error. Sequence allocation and writing occur under the same lock. After the first failure, return the latched error without attempting another write. The process-level wrapper reports that failure once and disables logging.

- [ ] Add failing `TestConftamer` unit tests for typed JSON/nulls, independent request/context counters, unstamped context returning null, concurrent sequence assignment, invalid UTF-8, short writes, and writer failure latching. Add an HTTP/2 receipt regression proving ALPN/common-server dispatch emits once. Writer tests must exercise isolated logger instances, not race with global initialization.
- [ ] Extract implementation into normal Go files and regenerate a hooks-only patch against a pristine `go1.26.6` tree. Do not maintain duplicate copies of `conftamer.go` inside the patch.
- [ ] Implement the writer and per-process file creation. Keep all JSONL free of diagnostics; send diagnostics to stderr.
- [ ] Replace all four message hooks using Section 2.4, including the new HTTP/2 wire hook and lower-level server response hook. Remove duplicate client response logging in the same change.
- [ ] Add a server exchange pointer to `Request`, an HTTP/1 attempt pointer to `transportRequest`, and an HTTP/2 attempt pointer to `http2clientStream`. Initialize them before publishing work to another goroutine. Avoid request-address maps and inherited occurrence IDs.
- [ ] Preserve origin context stamping; add the direct-transport fallback on a transport-owned request copy. Preserve Go's original cancellation/response request references. Do not introduce new mutation of caller-owned request fields.
- [ ] Implement the application script: require exactly one destination; require the first line of `VERSION` to equal `go1.26.6`; require a clean destination checkout; refuse existing overlay files; run `git apply --check` before applying; copy the three explicit files. Refuse an already-patched or wrong-version tree.
- [ ] Run focused producer tests with and without the race detector. Capture a minimal HTTP/1 and TLS HTTP/2 exchange; each must have one client request, one server request, one server final response, and one client final response, plus any routing metadata.

Prepare an isolated test tree once during execution:

```bash
repo="$PWD"
work="$(mktemp -d)"
git clone --depth 1 --branch go1.26.6 https://go.googlesource.com/go "$work/go"
bash apply-go-patch.sh "$work/go"
(cd "$work/go/src" && env -u GOROOT -u CONFTAMER_EVENTS_DIR \
  -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local ./make.bash)
export PATCHED_GO_ROOT="$work/go"
env -u GOROOT -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID \
  GOTOOLCHAIN=local "$PATCHED_GO_ROOT/bin/go" \
  test -count=1 net/http -run '^TestConftamer'
env -u GOROOT -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID \
  GOTOOLCHAIN=local "$PATCHED_GO_ROOT/bin/go" \
  test -race -count=1 net/http -run '^TestConftamer'
```

Keep the chosen workspace path in execution notes. Reapply later patch revisions to a new pristine tree, not on top of a previous patch. The existing sibling clone is not this test tree.

**Gate B:** A human reviews field ownership and exchange lifetime at each hook, including retries, redirect copies, HTTP/2 handoff/receipt reuse, nullable API evidence, and logging-disabled behavior. Do not accept “the output looks cleaner” as evidence of correct identity.

### Task 3: Attach routing facts by request ID

**Files:** `_stdlib/net/http/conftamer.go` and both Go test files; hook patch; Prometheus route patch.

**Interfaces:** `ConftamerLogRouted` from Section 2.5 and this pure helper:

```go
func conftamerFullPattern(originalPath, strippedPrefix, matchedPath, pattern string) (string, bool)
```

Minimal path-composition rule:

```go
if originalPath != strippedPrefix+matchedPath {
    return "", false
}
slash := strings.IndexByte(pattern, '/')
if slash < 0 {
    return "", false
}
return pattern[:slash] + strippedPrefix + pattern[slash:], true
```

The caller separately checks unchanged method/authority. Prefix state belongs to each request copy, not shared mutable exchange state.

- [ ] Add failing tests for direct mux routes, method/host-qualified patterns, multiple nested strips, Prometheus-style `:name`/`*path` patterns, empty stripped paths, and a request copy retaining its exchange ID.
- [ ] Add negative cases for arbitrary path rewrites, changed method/authority, missing exchange state, and a later unverifiable route following a valid outer route. Never infer full patterns from suffix matching.
- [ ] Add the prefix field and hook only successful `StripPrefix` transformations. Emit all routing records with the original HTTP snapshot and the actual local route object.
- [ ] Update both ServeMux branches with their explicit dialect. Rebase the Prometheus adapter patch to relative `route/route.go` paths for `common` `v0.70.1` and call `http.ConftamerLogRouted(req, "httprouter", r.prefix+handlerName)`.
- [ ] Run the focused Go tests and a patched `common/route` compile/test in a separate `common` checkout, using the patched Go executable. Do not test against the mutable module cache or edit the existing sibling copy.

```bash
git clone --depth 1 --branch v0.70.1 \
  https://github.com/prometheus/common.git "$work/common"
git -C "$work/common" apply --check "$repo/prometheus-common-route.patch"
git -C "$work/common" apply "$repo/prometheus-common-route.patch"
(cd "$work/common" && env -u GOROOT GOTOOLCHAIN=local \
  "$PATCHED_GO_ROOT/bin/go" test -count=1 ./route)
```

**Deliverable/review:** A human follows `/api/v1/items/7` through a known prefix strip and sees a verified full pattern. An arbitrary rewrite remains explicitly unverifiable; its data is not silently changed or dropped.

### Task 4: Simplify local consumers, without a normalization layer

**Files:** The three `analysis/*.py` files and `tests/test_output.py`.

**Interfaces:** Keep `load_events(path: str | Path) -> Iterator[dict]`, adding directory input. Add `build_graph(events: list[dict]) -> tuple[dict, set]` in `message_graph.py`; it returns semantic node-key representatives and a set of directed key pairs. Keep the text and DOT output modes. Tests can add `ROOT / "analysis"` to `sys.path` before importing these existing script modules; no Python packaging refactor is needed.

- [ ] First add failing reader tests for unsupported/unversioned input, malformed JSON/UTF-8, missing fields, duplicate/noncontiguous sequences, reused request IDs, orphan or wrong-direction responses, conflicting origin snapshots, and duplicate final responses. Errors must contain filename and physical line. Empty captures are allowed; a request without a response is valid.
- [ ] For directory input, read sorted `*.jsonl` files, require one process/PID per nonempty file and one capture ID across the directory, and reject the same process in multiple files. Preserve sequence order within each process; file order does not establish cross-process influence. Make the input path required on both inspection commands instead of keeping old implicit file locations.
- [ ] Implement explicit field and relationship checks in the shared reader using the standard library. JSON Schema remains a test oracle, not a new runtime parser framework. Null API/context values are valid. Display unknown-context events separately; never invent a shared null-context group.
- [ ] Replace dotted-map field extraction with `http`, `api`, and scoped IDs. Delete path-based response matching, API recovery from neighboring events, suffix reconstruction, and wire/client response deduplication.
- [ ] Build a last-route lookup by exact request key before creating semantic labels. Use only its verified full pattern and dialect; otherwise use the origin path. Response labels already have their own request snapshot.
- [ ] Keep every message occurrence through influence projection. Intern labels independently; never discard a later occurrence merely because its label was seen earlier. Semantic keys contain kind, API ID (not its source/package/symbol), method, status, and either the client host/path or the server's tagged pattern-or-path endpoint plus dialect. Exclude occurrence IDs and `original_path`; never alias a literal path with a pattern merely because their strings are equal.
- [ ] Make the diagnostic graph receive-to-later-send only. Remove the obsolete `--recv-sent` switch and the consecutive-message graph mode. Keep any consecutive-pair summaries in `group_by_context.py` explicitly labeled as co-occurrence, not influence.

Projection rule after route attachment and label interning:

```python
RECEIVES = {"request_received", "response_received"}
SENDS = {"request_sent", "response_sent"}
prior_receives = {}
edges = set()
for event, node_key in occurrences:
    if event["context_id"] is None:
        continue
    group = event["capture_id"], event["process_id"], event["context_id"]
    received = prior_receives.setdefault(group, set())
    if event["kind"] in RECEIVES:
        received.add(node_key)
    elif event["kind"] in SENDS:
        edges.update((source, node_key) for source in received)
```

`occurrences` is the ordered list of non-routing `(event, semantic_node_key)` pairs, including repeated node keys. Retain isolated nodes in the result.

- [ ] Assert `basic.jsonl` produces four message nodes and only the server receive-to-response edge. Assert `chained.jsonl` produces exactly `receive_request -> send_request`, `receive_request -> send_response`, and `receive_response -> send_response`.
- [ ] Add same-context/same-URL concurrent requests with reversed response order: references must still associate exactly. Add repeated semantic sends before/after a receive, unknown contexts, independent processes with equal local counters/PIDs, unknown API IDs, Unicode/quoted labels, and empty-graph DOT output.
- [ ] Run the Python suite, both inspection commands, and DOT parsing if Graphviz is installed. The graph command must emit valid DOT even for zero edges or isolated nodes.

**Consumer handoff checkpoint:** Give the downstream owner the approved `OUTPUT.md`, schema, fixtures, and a fresh producer capture. Ask them to demonstrate direct ID association, preserved occurrences, and unchanged receive-to-send semantics in the callable importer. This is a separate approval/change, not authorization to modify that checkout or its untracked plans.

### Task 5: Validate real capture behavior and document the new entry point

**Files:** Go capture tests, Python tests, `README.md`; remove the optional patch.

Use subprocesses for automated capture assertions so environment-based initialization is real and does not depend on resetting global `sync.Once` state. Put integration tests in `http_test`, allowing `httptest` without an import cycle; private writer/helper tests remain in package `http`.

- [ ] Add `TestConftamerCaptureExample` in `conftamer_test.go` as the direct, single-process manual example below. Unlike the automated assertions, it does not create child capture directories. Run it with the environment in the README command; it also works as an ordinary HTTP test with logging disabled. Imports are `io`, `net/http`, `net/http/httptest`, and `testing`.

```go
func TestConftamerCaptureExample(t *testing.T) {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    server := httptest.NewServer(mux)
    t.Cleanup(server.Close)
    response, err := http.Get(server.URL + "/items/7")
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

- [ ] Complete the executable test matrix below. Add each missing case as a failing test before changing its behavior.

| Case | Required evidence |
| --- | --- |
| HTTP/1 and bundled HTTP/2 | One request and one final response on each observed side; exact ID references; no duplicate HTTP/2 receipt through ALPN |
| Implicit 200 / empty handler / repeated `WriteHeader` | One accepted final response, not an event per method invocation |
| `103` followed by `200`; HTTP/1 `101` | No informational duplicate; one terminal event; no tunneled-body events |
| Redirect and transport retry | Fresh request ID per hop/attempt; inherited context preserved; response points to its actual attempt |
| Derived context, background replacement, direct `RoundTrip` | Expected root continuity or separation; no per-log invented context ID |
| Repeated URL, shared context, concurrent completion | No collisions; immutable snapshots; race-detector pass |
| Nested router / prefix strip / arbitrary rewrite | Exact association and verified-pattern versus concrete-path fallback |
| Multiple child processes in one capture directory | Separate valid files, unique process IDs, contiguous per-file sequences |
| Disabled logging, bad directory, missing capture ID, injected short/error write | No HTTP behavior changes; no silent capture success claim after a logging failure |
| Client request copies and server-to-client clones | No server occurrence ID escapes into a client attempt; normal cancellation and `Response.Request` behavior |

- [ ] Validate freshly emitted records against the JSON Schema and trace-integrity reader. Inspect unknown API/context and unverifiable-route cases rather than expecting zero unknowns.
- [ ] Exercise a fresh small Prometheus capture only if the owner authorizes running that producer. Do not add fields to old JSONL and call it a new capture, or require equality with old graph/event counts.
- [ ] Update the README with this new invocation and both inspection commands:

```bash
capture="$(mktemp -d)"
env -u GOROOT GOTOOLCHAIN=local \
  CONFTAMER_EVENTS_DIR="$capture" \
  CONFTAMER_CAPTURE_ID="$(basename "$capture")" \
  "$PATCHED_GO_ROOT/bin/go" test -count=1 -v net/http -run '^TestConftamerCaptureExample$'

python3 analysis/group_by_context.py "$capture"
python3 analysis/message_graph.py "$capture" --format dot
```

The manual example writes directly to this capture directory. The README must distinguish it from the automated subprocess tests and from an application's test run. Application examples replace the test target while retaining the new environment, patched Go executable, and `-count=1`.

- [ ] Remove the optional old producer patch and its documentation. State plainly that old captures are unsupported; do not supply a conversion recipe.
- [ ] Run focused tests, the full patched `net/http` suite with logging disabled, schema/analysis tests, and final diff checks. Record exact commands, toolchain versions, fixture provenance, and remaining limitations.

```bash
env -u GOROOT -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID \
  GOTOOLCHAIN=local "$PATCHED_GO_ROOT/bin/go" test -count=1 net/http
env -u GOROOT -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID \
  GOTOOLCHAIN=local "$PATCHED_GO_ROOT/bin/go" \
  test -race -count=1 net/http -run '^TestConftamer'
uv run --no-project --python 3.14 --with jsonschema==4.25.1 \
  python -m unittest discover -s tests -v
git diff --check
git status --short
```

Do not use the parent repository's `go test ./...` (run from `..`) as the sole gate. At the inspected revision it has unrelated stale command failures (`../cmd/context` and `../cmd/parse`); report those separately, without expanding this change to repair them.

## 5. Final human audit

**Gate C: Approval to publish the new output contract and producer.**

- [ ] Producer reviewer confirms every event has one owner, and retries/redirects do not reuse occurrence IDs.
- [ ] Consumer reviewer can read the two small fixtures and explain their associations and influence edges without URL or stack heuristics.
- [ ] Reviewer confirms IDs serve separate purposes: events, local requests, and local influence contexts; none imply distributed tracing or semantic graph identity.
- [ ] Reviewer inspects a real HTTP/2 capture, an ambiguous rewrite, and a failed-capture diagnostic—not only the happy-path fixture.
- [ ] Reviewer verifies that removing old fields/hooks and the optional patch leaves one supported format, not a hidden compatibility branch.
- [ ] Reviewer checks readability of the ordinary Go sources and the now-smaller patch; rejects speculative frameworks or redundant fields.
- [ ] Downstream owner records acceptance of the contract. If their new reader is not yet implemented, report integration as pending; do not claim end-to-end readiness.
- [ ] Execution evidence is attached. Passing tests establish the tested contract, not capture completeness or proven causality.

**Stop condition:** A reviewer changing identifier scope, response timing, routing trust, or the capture storage boundary sends the corresponding contract and tests back for revision. Do not quietly compensate in downstream heuristics.
