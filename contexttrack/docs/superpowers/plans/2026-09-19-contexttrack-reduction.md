# ContextTrack Reduction Implementation Plan

> **For agentic workers:** After explicit implementation approval, use `executing-plans`. Execute the three tasks serially and stop at their review gates. Do not delegate, commit, or push without separate authorization.
>
> **Status:** Implemented and verified on branch `reduction`. Task 1 is commit `99ccb72`, Task 2 is `c2bc64c`, and Task 3 documentation/integration is `3f0f8be`. The unchecked boxes below preserve the approved execution recipe; the execution record near the end is authoritative for completion.

**Goal:** Reduce the code and interacting behaviors a human must audit while retaining reliable HTTP/1 observations and exact shared-context influence.

**Architecture:** A v3-only producer emits four message kinds using two record shapes. One strict Python reader resolves them in one pass; one text command displays semantic nodes and possible-influence edges. Remove enrichment and compatibility machinery rather than introduce replacement frameworks.

**Tech Stack:** Go `go1.26.6`, Go standard library only; Python >=3.14; Pydantic >=2.13.5,<3; pytest >=9.1.1; Ruff and ty. No new runtime dependency.

**Spec:** The operator's reduction decisions, recorded in Sections 1–3 of this file, and `../../ConfTamer_HotNets_2026.pdf` Sections 4–5/Figures 2–3. This is intentionally not the paper's complete API/route labeling implementation.

## Global constraints

- All repository paths and commands below are relative to `contexttrack/` unless otherwise stated. Commands are future execution instructions, not claims of verification already performed. Shell blocks form one recipe; between separate tool calls, restore `SOURCE`, `WORK`, `REPO`, `PATCHED_GO`, and the `clean_go` function rather than assuming shell state persists.
- Preserve the dirty baseline: modified `README.md`, `OUTPUT.md`, and the executable mode of `apply-go-patch.sh`; untracked `AGENTS.md`, `IMPLEMENTATION.md`, and existing plans. Snapshot tracked diffs and untracked contents before execution. Use an isolated project worktree carrying that baseline, without committing, stashing, resetting, or discarding the owner's work to obtain a clean checkout.
- Sibling repositories/captures, the system toolchain, module caches, and existing experiment workspaces remain read-only. Use newly allocated Go trees and temporary captures. No consumer migration or external application test suites.
- Preserve HTTP results, errors, cancellation, bodies, trailers, flushing, retries, redirects, callbacks, and caller request/context identity. Synchronous logging may affect timing; do not promise timing equivalence.
- Behavior changes require a focused failing regression before production edits. Retain tests for retained behavior; delete only tests whose requirements are explicitly removed here.
- At each task gate run `uvx ruff check .`, `uvx ty check`, and `git diff --check`. Inspect the complete diff and untracked files. A line budget never justifies compressed code, weaker validation, or hiding code in an uncounted helper.
- Never infer completeness from successful parsing or an empty capture. Inspect workload results, stderr, record kinds, unknown-context counts, and expected activity.

## 1. Decisions and v3 contract

### Keep, remove, and compatibility

**Keep:** standard-library HTTP/1 client/server hooks; separate scoped identities; immutable exchange snapshots; server roots and `ConftamerContext`; checked process logging; strict validation; exact associations; order-independent receive × send pairing; isolated nodes.

**Remove:** API annotation helpers and bindings; all route observations/composition; the Prometheus adapter; HTTP/2 message instrumentation; metadata records and resolution; DOT/group-summary modes; `analysis/` compatibility wrappers; `RecordedEvent` and `Capture.records`.

**Break deliberately:** emit/read only `schema_version: 3`, and release the Python package as `0.3.0`. Do not accept v1/v2, silently discard their fields, add aliases, or write a converter. Keep historical captures and `testdata/v2-chain.jsonl` unchanged; the latter becomes a rejection fixture. Existing annotation callers and Python/CLI consumers must migrate separately. Keep `event.schema.json` generated from Pydantic, not an independent runtime validator.

### Records and labels

Each record has exactly this envelope:

```text
schema_version=3, capture_id, process_id, seq, exchange_id, kind
```

- `schema_version` is integer 3, not a boolean, float, or string. `capture_id` is nonempty; `process_id` is 32 lowercase hexadecimal characters.
- `seq`, `exchange_id`, and non-null `context_id` are strict integers in `1..2^64-1`. Record, exchange, and context identities remain distinct and scoped by capture/process.
- `send_request` / `receive_request` add `context_id: Counter | null` and `request: {method, host, path}`. Method is nonempty; host is nonempty or explicit null; path is a string, including an explicit empty string. A sent request requires a host.
- `send_response` / `receive_response` add only `status_code`, a strict integer 101 or 200..999. Responses reference their request by exchange ID.
- Reject missing required fields, other-variant/unknown fields, other kinds/versions, invalid UTF-8, and escaped surrogates. There is no metadata shape and no API or route field, even as null.

`MessageLabel` remains strict, frozen, and hashable, but has only the four-kind `kind`, nonempty `method`, nullable nonempty `host`, nonempty concrete `path`, and nullable terminal `status_code`. Client labels require a host; server labels have null host. Requests have null status; responses inherit their exact request's other label fields and context key, adding their response kind/status. Normalize only an explicitly empty raw path to `/` when constructing the semantic label; preserve method/authority spelling and ports. Missing API/route capabilities are not wildcards or evidence for stitching.

The reader retains:

```text
Occurrence(label, context_key, exchange_key, seq, location)  # frozen
Capture(occurrences: tuple[Occurrence, ...])                 # frozen
read_capture(path: str | Path) -> Capture
shared_context_pairs(occurrences: Iterable[Occurrence]) -> set[tuple[MessageLabel, MessageLabel]]
```

Read binary physical lines, decode UTF-8 strictly, and validate each nonblank record exactly once. Keep location-bearing errors, blank-line numbering, empty-input behavior, and filesystem errors. Enforce one capture/process per nonempty file, one capture across a directory, one file per process, sequences contiguous from 1, unique request declarations, preceding response origins, correct response direction, and at most one final response. Requests without responses remain valid.

Use one exchange table containing the origin occurrence and optional final-response location. Resolve and append each occurrence immediately; do not retain another list of raw records or a later label-finalization pass. Retain all occurrences until association; deduplicate only semantic labels and final edges. Directory filename ordering is presentation, not cross-process chronology.

For each known scoped context `c`, form `R(c) × S(c)`, where receives are `receive_request` and `receive_response`, and sends are `send_request` and `send_response`. No chronology, same-exchange exclusion, or URL matching. Null contexts never group; isolated labels remain visible.

### Producer and HTTP/2 boundary

Keep `CONFTAMER_EVENTS_DIR` (absolute existing directory) and `CONFTAMER_CAPTURE_ID` (nonempty run name), both valid UTF-8 and set before startup. Keep exclusive random process files with mode `0600`, mutex-protected sequence/write assignment, UTF-8 validation, checked writes, and first-error latching. Both variables absent disables capture; partial/invalid configuration and any set legacy `CONFTAMER_EVENTS` diagnose and disable it. Do not add queues, completion records, or durability promises.

Keep these HTTP/1 observation owners:

| Kind | Native owner |
|---|---|
| `receive_request` | `serverHandler.ServeHTTP`, before dispatch |
| `send_request` | `persistConn.roundTrip`, per attempt before worker handoff |
| `receive_response` | `persistConn.readResponse`, after accepting final headers |
| `send_response` | `response.WriteHeader`, at the accepted final transition |

`ConftamerContext` is the only remaining exported annotation helper. Clients read roots without mutating requests; server-owned requests receive fresh roots. Store client exchanges on `transportRequest`, not caller requests. Remove the separate origin/API-binding structures; an exchange needs only ID, context ID, and its immutable request snapshot.

Remove HTTP/2 exchange fields and message hooks, but retain **two explicit rejection guards**: at entry to bundled `http2ClientConn.roundTrip` and `http2serverConn.runHandler`. Each calls the existing `conftamerFailCapture` with a shared `errConftamerUnsupportedProtocol` error. Also reject a non-HTTP/1 request before attaching state at common server dispatch. Disabled capture remains a no-op.

These guards stop only the logger, diagnose once, and never return an HTTP error, change negotiation, or prevent handler execution. Any such diagnostic makes that process's capture unacceptable, even if it contains a valid HTTP/1 prefix. The reader alone cannot detect that condition. External `x/net/http2`, custom transports, mocks, pre-dispatch rejection, hijacked/tunneled traffic, bodies, Caddy, and Kubernetes remain outside the coverage claim; do not claim universal bypass detection.

The sole CLI becomes `contexttrack INPUT`. Keep a text graph with deterministic node/edge ordering and occurrence/node/edge/unknown-context counts. No subcommands, format flags, or compatibility scripts. The library retains occurrence details for programmatic inspection.

## 2. Baseline comparison and size budgets

Measured baseline: local `main` and `origin/main` = `222c9b6`; current implementation = `22bfb6c` plus the preserved working-tree documents. Physical lines include blanks/comments; count Go overlay source and patch-added source once, not patch headers/context. Main's malformed patch was counted textually, not successfully applied.

| Area | Main | Current | Proposed estimate |
|---|---:|---:|---:|
| Go producer/writer/native additions | 435 | 576 | 350–410 |
| Prometheus adapter additions | 5 | 3 | 0 |
| Python models/reader/analysis/CLI | 683 | 644 | 300–390 |
| Patch application script | 0 | 66 | 66 |
| **Production** | **1,123** | **1,289** | **716–866** |
| Go tests | 0 | 1,582 | 1,000–1,200 |
| Python tests, including script tests | 0 | 1,109 | 600–750 |
| **Code plus tests** | **1,123** | **3,980** | **2,316–2,816** |

Zeros describe tests under `contexttrack/`, not the entire repository. Production reduction is estimated at 33–44%; code-plus-tests reduction at 29–42%. These are estimates, not verified results or instructions to delete necessary tests.

Estimate basis: Go helpers/records 130–160, checked writer 200–220, native additions 20–30; Python models 80–100, reader/pairing 160–210, exports/text CLI 60–80. If an area's upper bound is exceeded, stop at its gate and ask for a scope/budget decision rather than silently expanding it.

Documentation is separate: main README 252 lines; current README/OUTPUT/IMPLEMENTATION/AGENTS 1,205; existing historical plans 1,425 before this plan. Target 250–400 active guide/contract lines across README/OUTPUT/AGENTS. Preserve the 403-line v2 implementation document under `docs/history/`; retain historical plans, including this one. Report archived and generated files separately: moving a file is not a repository-size reduction. Current schema/fixture/lockfile total 484 lines; main's optional legacy patch is 220 diff lines and excluded from its default implementation.

| Feature | Actual main source | Reduced v3 |
|---|---|---|
| Protocol observations | HTTP/1 plus partial/overlapping bundled-H2 hooks | HTTP/1 final-transition/attempt ownership; H2 failure guards only |
| Context roots | Caller mutation in `Client.do`; unretained fallback IDs | Server roots, explicit autonomous roots, unknown remains null |
| Response association | Context/method/path attribution | Exact scoped exchange identity |
| API ownership | Stack/handler package guesses | Removed, not inferred |
| Server endpoints | ServeMux/Prometheus routes with suffix reconstruction | Original concrete paths |
| Capture storage | Shared append file/PID; unchecked writes | Separate process files, sequences, checked writes |
| Validation | Malformed JSON warned about and skipped | Strict two-shape and relational checks |
| Edges | Consecutive by default; later-send filtering with `--recv-sent` | Order-independent receive × send |
| Labels/nodes | Client host omitted; some fallback keys omit kind; isolates lost | Explicit kind/client host; isolates retained |
| Diagnostics | Group summaries, text/DOT, clusters, runtime provenance | One text graph |

Main source references: `go-inlibrary.patch`; `analysis/event_io.py:31`; `analysis/message_graph.py:25,72,133,302,320`. Main's Go patch fails `git apply --numstat` at line 428; do not describe its source-level features as newly verified coverage.

## 3. File ownership

| Files | Final responsibility/action |
|---|---|
| `src/contexttrack/events.py` | Two v3 variants and the reduced immutable label; retain Pydantic as authority |
| `src/contexttrack/capture.py`, `src/contexttrack/__init__.py` | Single-pass associations, occurrences, pairing; remove raw-record API |
| `src/contexttrack/inspect.py` | One text command using the shared reader |
| `tests/test_events.py`, `tests/test_capture.py`, `tests/test_inspect.py` | V3 contract, integrity, pairing, and actual CLI behavior |
| `testdata/v3-chain.jsonl` | New four-record synthetic fixture; do not rewrite the v2 fixture |
| `event.schema.json`, `pyproject.toml`, `uv.lock` | Regenerated schema, package 0.3.0, lock refresh without dependency upgrades |
| `_stdlib/net/http/conftamer.go`, `_stdlib/net/http/conftamer_log.go`, `_stdlib/net/http/conftamer*_test.go` | Reduced producer and its retained/new regressions |
| `go-inlibrary.patch` | Regenerated native diff, including H2 guards |
| `apply-go-patch.sh`, `tests/test_apply_go_patch.py` | Keep clean/versioned application and four-overlay copy |
| `analysis/group_by_context.py`, `analysis/message_graph.py`, `prometheus-common-route.patch` | Delete after their replacements/removals are verified |
| `README.md`, `OUTPUT.md`, `AGENTS.md` | Concise execution guide, sole contract explanation, safety instructions |
| `IMPLEMENTATION.md` | Move unchanged to `docs/history/contexttrack-v2-implementation.md`; remove active links claiming it describes v3 |

## 4. Task 1 — v3 reader and one diagnostic

**Consumes:** Section 1. **Produces:** `EVENT_ADAPTER`, `MessageLabel`, the four library interfaces listed there, and `inspect.main() -> None` invoked as `contexttrack INPUT`.

- [ ] Snapshot/isolate the ContextTrack baseline, including this plan, and record its ref, dirty paths, and counts. Read the current source/tests before editing. The following preserves the original checkout and applies its existing changes to a detached worktree; `.venv` is recreated with `uv sync --dev`, not copied. No producer source changes in this task.

```bash
SOURCE=$PWD
WORK=$(mktemp -d)
git status --short --branch
git rev-parse HEAD
git diff --binary HEAD -- . > "$WORK/baseline.patch"
git ls-files --others --exclude-standard -z -- . > "$WORK/untracked.paths"
tar --null -T "$WORK/untracked.paths" -cf "$WORK/untracked.tar"
git worktree add --detach "$WORK/project" HEAD
if test -s "$WORK/baseline.patch"; then
  git -C "$WORK/project" apply "$WORK/baseline.patch"
fi
tar -xf "$WORK/untracked.tar" -C "$WORK/project/contexttrack"
cd "$WORK/project/contexttrack"
uv sync --dev
```

- [ ] Add `testdata/v3-chain.jsonl` with these literal records:

```jsonl
{"schema_version":3,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":1,"exchange_id":1,"kind":"receive_request","context_id":7,"request":{"method":"GET","host":"module-b.test","path":"/items/7"}}
{"schema_version":3,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":2,"exchange_id":2,"kind":"send_request","context_id":7,"request":{"method":"GET","host":"module-c.test","path":"/backend"}}
{"schema_version":3,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":3,"exchange_id":2,"kind":"receive_response","status_code":204}
{"schema_version":3,"capture_id":"synthetic-chain","process_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":4,"exchange_id":1,"kind":"send_response","status_code":200}
```

- [ ] Add this regression to `tests/test_capture.py` using its existing `Path`, `read_capture`, and `shared_context_pairs` imports. Run `uv run pytest -q tests/test_capture.py::test_v3_chain`; expect rejection of the otherwise-valid v3 fixture by the current v2 reader, not an import/setup error.

```python
def test_v3_chain():
    capture = read_capture(Path(__file__).parents[1] / "testdata/v3-chain.jsonl")
    items = capture.occurrences
    assert len(items) == 4
    assert [item.exchange_key[2] for item in items] == [1, 2, 2, 1]
    assert [item.label.status_code for item in items] == [None, None, 204, 200]
    assert items[2].label.host == "module-c.test"
    assert items[2].label.path == "/backend"
    assert items[3].label.host is None
    assert items[3].label.path == "/items/7"
    indices = {item.label: index for index, item in enumerate(items)}
    edges = {(indices[a], indices[b]) for a, b in shared_context_pairs(items)}
    assert edges == {(0, 1), (0, 3), (2, 1), (2, 3)}
```

- [ ] Change `Envelope.schema_version` to strict integer 3; remove `Route`, `Dialect`, and `MetadataEvent`. The variant declarations become the following, retaining the existing client-host and terminal-status validators. Remove API/pattern fields from `MessageLabel` and replace its endpoint validator with client-host/server-null-host checks; retain its request/response status check.

```python
class RequestEvent(Envelope):
    kind: Literal["send_request", "receive_request"]
    context_id: Counter | None
    request: RequestLabel

class ResponseEvent(Envelope):
    kind: Literal["send_response", "receive_response"]
    status_code: Annotated[int, Field(strict=True, ge=100, le=999)]

Event = Annotated[RequestEvent | ResponseEvent, Field(discriminator="kind")]
EVENT_ADAPTER = TypeAdapter(Event)
```

- [ ] Refactor `capture.py` to the Section 1 single-pass design: `_Exchange` stores `origin: Occurrence` and `response_location: str | None`; validate before adding an occurrence. Delete `_bind_api`, route resolution, raw-record accumulation, and the later `_occurrences` pass. Keep the small pairing function, adapting only its types if necessary. Remove `RecordedEvent` exports, not a compatibility alias.
- [ ] Add a CLI regression using the existing `run_cli` helper: `stdout, stderr = run_cli(monkeypatch, capsys, str(ROOT / "testdata/v3-chain.jsonl"))`; assert `Nodes (4):`, `Edges (4):`, `unknown_context=0`, and empty stderr. Run it before changing `inspect.py`; expect the old subcommand parser to reject the invocation. Then keep text rendering only, make INPUT the sole positional argument, and remove the two wrappers.
- [ ] Migrate retained tests to v3 and literal expectations. Reject valid former metadata shapes with their version changed to 3, as well as v1/v2 records. Retain UTF-8/surrogate, strict-number, missing/extra-field, sequence, file/process identity, duplicate/orphan/direction, reversed-response association, missing-response, empty-path, unknown-context, cross-process, repeated-label, earlier-send, and isolate cases. Remove route/API/DOT/wrapper tests, not integrity tests. Test the installed console command as well as the direct entry function.
- [ ] Set package version `0.3.0`; regenerate the schema with `json.dumps(EVENT_ADAPTER.json_schema(), indent=2) + "\n"`; refresh the lock without broad upgrades. Run `uv sync --dev`, `uv run pytest -q`, both static checks, and `uv run contexttrack testdata/v3-chain.jsonl`.

**Gate A:** Human review of the two raw shapes, five-field label, one exchange table, exact four-edge fixture, CLI removals, and measured Python/test size. End-to-end v3 capture is not ready yet: the unchanged producer still emits v2. Do not add transitional compatibility to conceal that dependency.

## 5. Task 2 — HTTP/1-only producer

**Consumes:** Section 1 and Gate A's contract. **Produces:** v3 records at the four HTTP/1 owners, `ConftamerContext`, and explicit unsupported-protocol capture failure. Keep private logging function signatures `conftamerWriteRecord(*conftamerEnvelope, any)` and `conftamerFailCapture(error)`; keep request/response emission by `*conftamerExchange`.

- [ ] Allocate a fresh pinned base and development tree; apply/build the current producer first so new Go regressions can fail against working v2 code. Define the command wrapper below once; update `PATCHED_GO` when switching verification trees. Every Go command uses this wrapper or the equivalent explicit environment.

```bash
REPO=$PWD
git clone --depth 1 --branch go1.26.6 https://go.googlesource.com/go "$WORK/go-base"
git -C "$WORK/go-base" worktree add --detach "$WORK/go-dev" go1.26.6
GO_DEV=$WORK/go-dev
bash "$REPO/apply-go-patch.sh" "$GO_DEV"
(cd "$GO_DEV/src" && env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local ./make.bash)
PATCHED_GO=$GO_DEV/bin/go
clean_go() {
  env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local "$PATCHED_GO" "$@"
}
```

- [ ] Add `TestConftamerV3RequestShape` to the internal test overlay, using its existing logger, pointer, and field-assertion helpers:

```go
func TestConftamerV3RequestShape(t *testing.T) {
    var output bytes.Buffer
    log, _ := conftamerTestLogger(&output)
    event := conftamerRequestEvent{Request: conftamerRequestLabel{
        Method: "GET", Host: stringPointer("example.test"), Path: "/",
    }}
    event.Kind, event.ExchangeID = "send_request", 1
    if err := log.write(&event.conftamerEnvelope, &event); err != nil {
        t.Fatal(err)
    }
    var fields map[string]json.RawMessage
    if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
        t.Fatal(err)
    }
    assertConftamerJSONFields(t, fields, "schema_version", "capture_id",
        "process_id", "seq", "exchange_id", "kind", "context_id", "request")
    if string(fields["schema_version"]) != "3" || string(fields["context_id"]) != "null" {
        t.Fatalf("unexpected envelope: %s", output.String())
    }
}
```

- [ ] Add `TestConftamerHTTP2Boundary` to the black-box overlay. Its test-only helper `runConftamerH2Boundary(t *testing.T, side string) ([]conftamerCapturedRecord, string)` returns child records and stderr. For `side="client"`, run a capture-enabled client child against a capture-disabled parent TLS/H2 server. For `side="server"`, run only the server in the captured child and the H2 client in the disabled parent. Reuse the existing subprocess/environment machinery; exchange the server URL through stdout and synchronize shutdown through stdin/channels, with bounded waits. Use test-fixture TLS configuration only. Each helper must independently assert HTTP/2, status 200, and normal completion, then exercise this test:

```go
func TestConftamerHTTP2Boundary(t *testing.T) {
    http.CondSkipHTTP2(t)
    for _, side := range []string{"client", "server"} {
        t.Run(side, func(t *testing.T) {
            records, stderr := runConftamerH2Boundary(t, side)
            if len(records) != 0 || strings.Count(stderr, "capture failed:") != 1 ||
                !strings.Contains(stderr, "non-HTTP/1") {
                t.Fatalf("records=%v stderr=%q", records, stderr)
            }
        })
    }
}
```

- [ ] Copy the changed test overlays into `$GO_DEV/src/net/http/`; run `clean_go test -count=1 net/http -run '^TestConftamer(V3RequestShape|HTTP2Boundary)$'`. Observe v2's extra API/version fields and H2 observations/no rejection as the expected failures. Do not proceed on helper deadlocks, skipped H2 cases, or compilation failures.
- [ ] Remove API/route helpers, metadata structures, validation branches, client binding/prefix fields, ServeMux/StripPrefix hooks, and H2 exchange/message hooks. Emit schema version 3. Keep request snapshots and HTTP/1 hook placement/publication. Add `errConftamerUnsupportedProtocol = errors.New("non-HTTP/1 capture is unsupported")` beside the logger's existing errors; call `conftamerFailCapture(errConftamerUnsupportedProtocol)` at both H2 entries. At server attachment, after the disabled/existing-exchange checks and before any root/state allocation, fail capture and return if `req.ProtoMajor != 1`. Do not return early from either native H2 function.
- [ ] Retain the existing H1/configuration/writer/concurrency/multiprocess/body/trailer/cancellation/retry/redirect/implicit-response/panic tests. Preserve the channel-synchronized timeout/orphan regression but remove its late-metadata expectations. Reduce former H2 positive-coverage cases to the independent boundary tests. Convert the capture example to one simple client/server round trip with an explicit client root and no removed annotations. Delete `prometheus-common-route.patch` and its application instructions; no replacement adapter.
- [ ] Format normal overlay/native Go sources using the patched tree's `bin/gofmt`, copy the four overlays into the development tree, and regenerate only the native diff:

```bash
git -C "$GO_DEV" diff -- src/net/http/request.go src/net/http/server.go \
  src/net/http/transport.go src/net/http/h2_bundle.go > "$REPO/go-inlibrary.patch"
clean_go test -count=1 net/http -run '^TestConftamer'
clean_go test -race -count=1 net/http -run '^TestConftamer'
```

**Gate B:** Review every retained native hook and its surrounding Go control flow, immutable state publication, H2 guard independence, removed exports, and measured Go/test size. H2 tests must show working HTTP/2 with failed capture, not forced HTTP/1 negotiation. A passing synthetic reader test is not producer evidence.

## 6. Task 3 — clean reproduction, emission, and concise documentation

**Consumes:** Gates A/B. **Produces:** two clean v3 applications, one freshly built/tested producer, a validated real capture, and documentation matching the reduced scope. No new runtime abstraction belongs in this task.

- [ ] Parse the patch from the repository root, then apply it to two new clean worktrees from the untouched pinned base. These are not copies of the applied development working tree:

```bash
git -C "$(git rev-parse --show-toplevel)" apply --numstat "$REPO/go-inlibrary.patch"
for name in go-verify-a go-verify-b; do
  git -C "$WORK/go-base" worktree add --detach "$WORK/$name" go1.26.6
  git -C "$WORK/$name" apply --check "$REPO/go-inlibrary.patch"
  bash "$REPO/apply-go-patch.sh" "$WORK/$name"
done
GO_VERIFY=$WORK/go-verify-a
(cd "$GO_VERIFY/src" && env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local ./make.bash)
PATCHED_GO=$GO_VERIFY/bin/go
clean_go version
clean_go env GOROOT
clean_go test -count=1 net/http -run '^TestConftamer'
clean_go test -race -count=1 net/http -run '^TestConftamer'
clean_go test -count=1 net/http
clean_go test -c -o "$WORK/http.test" net/http
```

- [ ] Run the precompiled binary with capture enabled only during execution, from its package directory. Inspect both output streams and parse the fresh files through the shared reader:

```bash
CAPTURE=$(mktemp -d "$WORK/capture.XXXXXX")
(cd "$GO_VERIFY/src/net/http" && \
  env -u GOROOT -u CONFTAMER_EVENTS GOTOOLCHAIN=local \
    CONFTAMER_EVENTS_DIR="$CAPTURE" CONFTAMER_CAPTURE_ID=reduction-smoke \
    "$WORK/http.test" -test.run '^TestConftamerCaptureExample$' -test.count=1 -test.v \
    >"$WORK/capture.stdout" 2>"$WORK/capture.stderr")
uv run contexttrack "$CAPTURE"
uv run contexttrack testdata/v3-chain.jsonl
uv run pytest -q
uvx ruff check .
uvx ty check
git diff --check
```

- [ ] Require the real example to emit exactly four v3 message records, four semantic nodes, two context-local edges, and zero unknown-context occurrences; stderr must show enablement and no capture/configuration failure. Its independently rooted client and server are **not** one context. The synthetic forwarding fixture has four nodes/four edges instead; never force runtime IDs to match to obtain that result. Retained nested-request tests must also demonstrate server-root inheritance by outbound requests. API/route enrichment is unavailable by design, not a measured zero-unknown result.
- [ ] Rewrite README as setup/capture/one-command instructions, OUTPUT as the sole v3 contract explanation, and AGENTS as short safety/verification guidance linking to OUTPUT. Retain clean-tree, explicit executable/environment, compile-before-capture, race/full-test, unknown/unsupported, and read-only-sibling rules. Move IMPLEMENTATION unchanged to the historical path in Section 3. Do not present old acceptance captures as v3 evidence or add another implementation walkthrough.
- [ ] Recount every production/test area using Section 2's method. Separately report active documentation, historical documentation, generated artifacts, and physical patch size. Inspect complete diffs/untracked files and real captures before sharing. If a budget is exceeded, stop for an explicit scope decision; do not claim this plan's estimates as achieved.

**Final gate:** Present the smaller source and tests for human audit, the actual main/current/final counts, exact commands/results, both patch-application identities, the build/test tree identity, and capture counts/unknowns. Distinguish patch parsing/application, build, producer emission, reader integrity, and consumer integration. Report consumer migration and complete API/route labeling as unsupported, not completed. Return the project worktree path and a reduction-only delta relative to the preserved dirty baseline; do not automatically copy changes back over `SOURCE`. Integration, committing, and pushing require the owner's approval.

## Execution record

The approved reduction was completed serially without delegation. The final
implementation is v3-only and consists of commits:

- `99ccb72` — strict v3 reader, exact one-pass association, reduced labels, and
  the sole text diagnostic;
- `c2bc64c` — HTTP/1-only v3 producer, retained behavior/integrity tests, and
  independent bundled-HTTP/2 capture-failure guards; and
- `3f0f8be` — concise active documentation, archived v2 implementation notes,
  retained plans, and the executable patch application script.

Final reproduction used clean Go `go1.26.6` commit
`1ea5a71ad8ceb7b9f16b4b6f8ea4739a4327dd6e`. The native patch parsed as 15
additions and 3 deletions and passed `git apply --check` plus
`apply-go-patch.sh` in two independent clean worktrees. Both applications had
the same tracked diff hash
`1bc7b5ca1a9505aeb3b5633d9abd3719a2e2016fb7531e1ca252c9b2af872309`.
One application was built with `./make.bash`; the resulting executable reported
Go 1.26.6 and its fresh-tree `GOROOT`.

Final producer verification passed:

```text
go test -count=1 net/http -run '^TestConftamer'
go test -race -count=1 net/http -run '^TestConftamer'
go test -count=1 net/http
```

The independent HTTP/2 client/server boundary cases and the nested
server-root-to-outbound-request inheritance case also passed explicitly. A test
binary compiled with capture disabled, then ran the capture example with capture
enabled only during execution. The fresh mode-0600 process file contained
exactly four schema-v3 records, one of each message kind. Its diagnostic had
four occurrences, four nodes, two context-local edges, and
`unknown_context=0`; stderr contained one enablement line and no failure. The
synthetic forwarding fixture had four nodes, four edges, and
`unknown_context=0`. Client and server runtime roots remained distinct; no IDs
were forced to match. API/route enrichment is absent by design, not an observed
zero-unknown metric.

Final repository verification passed with 88 Python tests, Ruff, ty, patch
parsing, and `git diff --check`. Physical-line counts were:

| Area | Main | Pre-reduction v2 | Final v3 |
| --- | ---: | ---: | ---: |
| Go producer/writer/native additions | 435 | 576 | 391 |
| Prometheus adapter additions | 5 | 3 | 0 |
| Python models/reader/diagnostic | 683 | 644 | 386 |
| Patch application script | 0 | 66 | 66 |
| **Production** | **1,123** | **1,289** | **843** |
| Go tests | 0 | 1,582 | 1,200 |
| Python tests | 0 | 1,109 | 748 |
| **Code plus tests** | **1,123** | **3,980** | **2,791** |

Production fell 34.6% and code plus tests fell 29.9% relative to v2. Active
documentation is 399 lines; historical documentation is 2,235 lines; generated
schema/fixtures/lockfile are 357 lines; and the native patch is 90 physical
lines. All approved budgets were met. `README.md`, `OUTPUT.md`, and `AGENTS.md`
are the active guides; `docs/history/contexttrack-v2-implementation.md` and the
plans are historical. The v2 implementation document's unchanged SHA-256 is
`a427621dd0cf90c2a55c6cfce266611529ef929bc5cfe0290cb69b956081a39f`.

The reduction was copied into the `reduction` branch and committed. Consumer
migration, v1/v2 conversion, complete API/route labeling, bundled-HTTP/2
capture, external HTTP/2, custom transports, Caddy, and Kubernetes remain
unsupported or separate work.

## Planning evidence and residual risks

This plan was prepared by reading the current overlays, reader/models/diagnostics/tests, the actual `main` source, and Go 1.26.6 H2 entry points in a read-only historical workspace. That workspace was not used as clean-application evidence. Main/current source counts and main's patch-parser failure were measured during the preceding comparison; production code was not changed while planning.

The size estimates remain unverified. Removing H2 observations saves little hook text but removes a second observation lifecycle; the two rejection guards still require review. Native hooks always require reading surrounding Go code, not merely counting inserted lines. Captures remain workload-dependent and may be incomplete. Concrete server paths can produce more semantic nodes than routes, and absent API ownership prevents claims of complete stitchable labels. The implementation may proceed only after the operator approves this plan and its deliberate compatibility/coverage losses.
