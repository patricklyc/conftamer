# ContextTrack v4 Implementation Plan

> **Status:** Implemented through Task 4 on branch `contexttrack-v4`:
>
> - `32213d0` — v4 source-based influence reader;
> - `84062b7` — v4 producer source annotations;
> - `cd7317e` — v4 producer precision evidence; and
> - `c525e0e` — v4 producer and annotation documentation.
>
> Unchecked boxes preserve the approved execution recipe; they are not a current
> work list or durable verification evidence. Reproducibility, producer emission,
> reader integrity, and end-to-end acceptance claims still require fresh results
> from the verification below.
>
> **For agentic workers:** Follow the current `AGENTS.md`. Replaying this plan or
> extending the implementation still requires serial execution with review at
> each gate. No delegation, commits, pushes, consumer migration, or external
> application suites without separate authorization.

**Goal:** Record meaningful receive-to-send influence, backed by exact message
occurrences, with precision demonstrated against v1 and bounded implementation
growth.

**Architecture:** Keep v3's HTTP/1 recorder, checked logger, strict reader, and
text diagnostic. Replace context grouping with explicit source references.
Automatically record exact server request/reply relationships; require
application declarations for other data/control dependencies.

**Tech Stack:** Go `go1.26.6`; Python >=3.14; Pydantic >=2.13.5,<3; pytest
>=9.1.1; Ruff and ty. No new runtime dependency.

**Spec:** The operator's agreed edge semantics, recorded in Sections 1-3 below.
These decisions supersede v3's Cartesian-product rule and root-annotation API
in v4. All other safety, identity, and protocol-preservation requirements
remain.

## Global constraints

- Paths are relative to `contexttrack/` unless stated otherwise. The planning
  baseline is branch `reduction` at `921bfec`; the v1 comparison is local `main`
  at `222c9b6`.
- Start execution with `git status --short --branch`; preserve unrelated changes.
  Use `using-git-worktrees` to establish an isolated implementation workspace
  after approval. Do not commit, stash, reset, or discard owner changes to obtain
  a clean checkout.
- Sibling repositories/captures, the system Go tree, module caches, and existing
  experiment workspaces remain read-only. Use fresh clean Go `go1.26.6` trees
  and temporary capture directories.
- Preserve HTTP results, bodies, errors, cancellation, retries, redirects,
  trailers, flushing, callbacks, and caller request/context identity. Logging
  may affect timing.
- Add a focused failing regression before behavior changes. Retain tests for
  retained behavior; remove only assertions for explicitly retired v3 semantics.
- Invoke the patched executable explicitly with `GOROOT` unset and
  `GOTOOLCHAIN=local` on every Go command. Compile with capture disabled; enable
  it only while running the selected binary from the package directory.
- A passing test, valid JSON, or empty capture is not producer evidence. Inspect
  stdout, stderr, kinds, expected activity, source references, and unattributed
  sends. Never check real captures into `testdata/`.
- Commands below are execution requirements, not durable verification evidence.
  Restore `REPO`, `WORK`, `GO_WORK`, `PATCHED_GO`, and the `clean_go` function
  between tool calls rather than assuming shell state persists.

---

## 1. Freeze the v4 semantics

### What an edge means

A particular received message influenced a particular outgoing operation
through:

- **Data:** information from the receive was used to construct or select the
  operation.
- **Control:** the receive triggered the operation or satisfied an explicitly
  required prerequisite.

Waiting for a response can constitute control influence even if its contents
are unused. Merely sharing a context, or being logged earlier, does not.

### Evidence rules

1. **Automatic:** received server request -> its own server response, through
   exact exchange identity.
2. **Declared:** another received message -> a specifically annotated outgoing
   request or server reply.
3. Every dependency references an actual, preceding receive in the same capture
   process.
4. No context matching, nearest-event matching, URL matching, or inferred
   transitive edges.
5. Associate exact occurrences before merging semantic labels.
6. Preserve isolated messages and repeated occurrences.
7. Missing declarations mean **unknown**, not independence.
8. Declarations require reviewed placement and focused tests. Structural
   validation cannot prove that application code used an input.

The graph remains limited to the existing HTTP/1 observation boundaries.
Dependencies affecting only body output after final headers have been recorded
are outside this contract; no retroactive annotation.

### Compatibility

- Emit and read **v4 only**; package version becomes `0.4.0`.
- Keep the v2 and v3 fixtures unchanged as rejection fixtures.
- Remove `ConftamerContext` and capture-specific root bookkeeping.
- Do not retain the shared-context graph as an alternative mode.
- Ordinary Go context behavior remains unchanged.
- Application and downstream consumer migrations remain separately authorized
  work.

## 2. Use a small, explicit representation

### Wire format

Keep the four message kinds and two request/response shapes.

Retain:

```text
schema_version, capture_id, process_id, seq, exchange_id, kind
```

Remove `context_id`. Add a required `sources` array to every record:

- Receives have `sources: []`.
- Sends list the sequence numbers of their source receives.
- References are implicitly scoped to the record's capture and process.
- Entries are strict positive integers, sorted and unique.
- Each source must precede the send and identify a received message.
- Every `send_response` must include its own request-receive sequence.

Using the same field on both record shapes avoids introducing more variants.
An unannotated client send has an empty list. Retain the existing strict
identity/counter ranges, request fields, terminal-status rules, UTF-8 handling,
and rejection of missing, unknown, or other-variant fields.

The synthetic forwarding fixture becomes:

| Sequence | Message | Sources |
| ---: | --- | --- |
| 1 | Receive incoming request A | `[]` |
| 2 | Send downstream request B | `[1]` |
| 3 | Receive downstream response C | `[]` |
| 4 | Send reply D | `[1, 3]` |

Expected edges: **A -> B, A -> D, C -> D**.

Receiving C does not inherit B's sources or create an edge back to B.

### Annotation API

Use actual received request/response objects as sources, not caller-supplied
numeric IDs:

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

The interface is implemented inside `net/http` by `*Request` and `*Response`.
A valid source must carry an observed server ingress or client response.

**Outgoing requests**

- `ConftamerWithSources` returns a shallow request copy with an immutable source
  list.
- It replaces the explicit source list; callers supply the complete set.
- It never modifies the original request or context.
- Transport attempts snapshot this list.
- Retries retain the declaration attached to their request.
- Redirect-created requests require explicit annotation in the callback if
  influence is to be recorded; do not invent dependencies through redirect
  matching.

**Server replies**

- `ConftamerSetReplySources` identifies the reply through the received request's
  existing server exchange.
- It replaces the additional source list; the incoming request is always
  included automatically.
- Sources freeze at the recorded final-header transition.
- A late declaration fails capture once, without changing the HTTP operation.
- Informational headers do not freeze the list.
- A synchronized per-reply slot handles timeout races; there is no
  shared-context dependency state.

**Failure and disabled behavior**

- Disabled or stopped capture makes the helpers no-ops; the request helper
  returns its input unchanged.
- An enabled declaration using an unobserved source or invalid reply target
  fails capture rather than silently inventing or dropping a reference.
- No new HTTP errors or changes to HTTP results.

Typical forwarding use is two declarations:

```go
outbound = http.ConftamerWithSources(outbound, incoming)

// After receiving and using the downstream response:
http.ConftamerSetReplySources(incoming, downstreamResponse)
```

## 3. Keep occurrence evidence in the reader

Replace the context field in `Occurrence` with explicit record and source
identities:

```text
RecordKey = (capture_id, process_id, seq)
ExchangeKey = (capture_id, process_id, exchange_id)

Occurrence(
    label,
    record_key,
    exchange_key,
    source_keys,
    location,
)

Capture(occurrences)

read_capture(path) -> Capture

influence_edges(capture)
    -> mapping of label pair to one exact source/target occurrence witness
```

`Occurrence` and `Capture` remain immutable. `source_keys` and `occurrences`
are tuples. `read_capture` continues to accept `str | Path`. The concrete
`influence_edges` return type is:

```python
dict[
    tuple[MessageLabel, MessageLabel],
    tuple[Occurrence, Occurrence],
]
```

The reader retains all occurrences and all recorded source references. The
label-level mapping supplies one representative witness for display; it does
not discard underlying occurrences.

Continue single-pass validation:

1. Validate the existing envelope, file identities, sequences, and exchange
   relationships.
2. Resolve each source against preceding receives in that process file.
3. Require a server reply's own incoming request among its sources.
4. Construct the resolved occurrence.
5. Construct graph edges only from recorded references.

Keep `MessageLabel`'s existing five fields unchanged.

The CLI remains:

```text
contexttrack INPUT
```

It should:

- identify request/reply versus declared edges;
- show a source/target record location for each displayed edge;
- retain isolated nodes;
- replace `unknown_context` with `sends_without_sources`;
- avoid describing an empty source list as independence.

## 4. Establish the precision oracle before implementation

Use **v1 `--recv-sent` at `main` commit `222c9b6`** as the relevant comparison,
not its default consecutive-message visualization.

Specify the true dependencies independently of either implementation. Do not
merely bless whatever v1 emits.

| Case | Required v4 result |
| --- | --- |
| Direct server reply | Incoming request -> exact reply |
| Forwarding handler | Incoming request -> downstream call; downstream response -> reply when used or explicitly required |
| Data-dependent reply | Changing the source input changes the relevant outgoing decision/value |
| Completion-dependent reply | Reply cannot proceed before the required response completes |
| Unrelated background response | No edge to an independent send, regardless of logging order |
| Parallel branches | No sibling dependency unless explicitly used |
| Fan-in | Exactly the declared inputs used by the outgoing operation |
| Repeated request label | Preserve response C1 -> later request B2; do not lose B2 through early deduplication |
| Backward reference | Reject it |
| Identical counters in different processes | Never join them |
| Unannotated autonomous client | Retain observations, emit no invented influence |

**Acceptance:** v4 emits every required edge and no unexpected edge in these
reviewed cases. An empty graph cannot pass the positive cases.

Record v1's results for the same synthetic scenarios. Do not ship a legacy
reader or require access to `main` during ordinary package tests.

The resulting precision claim must be scoped to the tested workloads, not
presented as a universal proof.

## 5. Implementation tasks

### Task 1 - V4 contract, strict reader, and diagnostic

**Files**

- Modify `src/contexttrack/events.py`
- Modify `src/contexttrack/capture.py`
- Modify `src/contexttrack/inspect.py`
- Modify `src/contexttrack/__init__.py`
- Modify `tests/test_events.py`, `tests/test_capture.py`, `tests/test_inspect.py`
- Create `tests/test_influence.py`
- Create `testdata/v4-chain.jsonl`
- Update `event.schema.json`, `pyproject.toml`, `uv.lock`
- Update the v4 contract portions of `OUTPUT.md` and `AGENTS.md`

**Interfaces:** Consumes the record contract in Section 2. Produces the immutable
values and `read_capture`/`influence_edges` interfaces in Section 3, plus the
existing `inspect.main()` entry point.

**Steps**

- [ ] Add the four-record v4 fixture and a failing exact-edge regression.
- [ ] Add negative cases for nonexistent, non-receive, duplicate, future, and
  wrongly scoped sources.
- [ ] Add rejection tests for unchanged v2/v3 fixtures and the removed
  `context_id` field.
- [ ] Implement the strict `sources` field and retained exchange checks.
- [ ] Replace `shared_context_pairs` with `influence_edges`.
- [ ] Update the text output and witness assertions.
- [ ] Regenerate the schema from Pydantic; refresh the lock without dependency
  upgrades.

The main positive regression should assert exact occurrence witnesses:

```python
capture = read_capture(FIXTURE)
edges = influence_edges(capture)

assert len(capture.occurrences) == 4
assert {
    (source.record_key[2], target.record_key[2])
    for source, target in edges.values()
} == {(1, 2), (1, 4), (3, 4)}
```

Here `FIXTURE` is `testdata/v4-chain.jsonl`, resolved relative to the repository
as in the existing tests.

**Gate:** Python tests and static checks pass. Review all changed integrity
checks and confirm that no edge is produced from context or chronology alone.

At this task gate the producer remains v3 until Task 2; this intermediate state
is not a release.

### Task 2 - Producer sources and annotation helpers

**Files**

- Modify `_stdlib/net/http/conftamer.go`
- Modify `_stdlib/net/http/conftamer_log.go`
- Modify both `_stdlib/net/http/conftamer*_test.go` files
- Regenerate `go-inlibrary.patch`

Native changes belong in the existing hook owners plus private fields in
`Request` and `Response`. Keep the four-file overlay arrangement and application
script.

**Interfaces:** Consumes the wire format and Go API in Section 2. Produces v4
records at the existing HTTP/1 observation owners and implements
`ConftamerSource`, `ConftamerWithSources`, and `ConftamerSetReplySources`.

**Steps**

- [ ] Add failing regressions for the v4 record shape and removal of server
  context stamping.
- [ ] Make successful record writes return their assigned sequence; failure
  returns no usable source identity.
- [ ] Store the ingress sequence before handler dispatch.
- [ ] Store the response-receive sequence before publishing the response to its
  caller.
- [ ] Implement the two helpers and immutable client source snapshots.
- [ ] Implement the per-reply source slot and final-header freeze, including
  late-annotation rejection.
- [ ] Remove root counters, context grouping state, and `ConftamerContext`.
- [ ] Preserve per-attempt exchange allocation, request snapshots, logger failure
  behavior, and HTTP/2 guards.
- [ ] Regenerate the native patch from the pinned development Go tree rather
  than hand-editing hunk counts.

**Required tests**

- Disabled helper identity and no-op behavior.
- Original request/context unchanged after annotation and transport use.
- Sources survive request copies without cross-request contamination.
- Received responses get their own identities, not inherited request sources.
- Fan-in sources are copied, canonicalized, and immutable.
- Late declarations and timeout races.
- Informational versus final headers.
- Existing retries, redirects, cancellation, bodies, trailers, flushing,
  callbacks, and exchange ownership.

**Gate:** Focused producer and race tests pass in the development Go tree.
Review the synchronization and publication boundaries before proceeding.

### Task 3 - Real producer evidence for the precision cases

**Files**

- Extend `_stdlib/net/http/conftamer_test.go`
- Extend `tests/test_influence.py` only where needed for corresponding reader
  assertions

Reuse the existing subprocess/capture machinery.

**Interfaces:** Consumes the annotation API from Task 2 and reader/witness API
from Task 1. Retains `TestConftamerCaptureExample` and adds
`TestConftamerForwardingCaptureExample` as separately runnable capture examples.

**Steps**

- [ ] Turn the nested-request workload into an explicitly annotated forwarding
  example.
- [ ] Add bounded workloads for data influence, required completion, independent
  branches, fan-in, and repeated requests.
- [ ] Review each annotation against the actual branch, value use, or wait it
  represents.
- [ ] Use channels to exercise different schedules; do not depend on sleeps or
  infer dependencies from event timing.
- [ ] Compare enabled and disabled HTTP outcomes.
- [ ] Check actual emitted references, not just graph counts.
- [ ] Compare the reviewed semantic cases with the pinned v1 baseline.

**Expected fresh-capture examples**

| Example | Records | Edges | Sends without sources |
| --- | ---: | ---: | ---: |
| Existing simple client/server round trip | 4 | 1 | 1 |
| Annotated forwarding through a second local server | 8 | 4 | 1 |

The forwarding capture includes both local server exchanges. Do not force
client and server identities to match or fabricate network-correlation edges.

**Gate:** The producer, not merely a hand-authored fixture, demonstrates all
positive and negative precision cases.

### Task 4 - Reproducibility, documentation, and final audit

**Files**

- Finalize `README.md`, `OUTPUT.md`, and `AGENTS.md`
- Keep historical plans and captures unchanged

**Interfaces:** Consumes the completed producer, reader, diagnostic, and named
capture examples. Produces the final acceptance record with commands, results,
size counts, and residual limitations.

**Steps**

- [ ] Document the two helpers, their replacement semantics, reply cutoff, and
  annotation trust boundary.
- [ ] Document v4-only compatibility and the separate application/consumer
  migration work.
- [ ] Remove active claims about context grouping and root requirements.
- [ ] Apply the final patch to two independent clean `go1.26.6` trees.
- [ ] Build and run the verification below.
- [ ] Inspect the complete diff and every untracked file.
- [ ] Report source size, test size, generated files, and documentation separately.

## 6. Verification

All commands below are **verification requirements**, not persistent results.
Run them afresh before making the corresponding acceptance claims.

Run from the implementation checkout:

```bash
uv sync --dev
uv run pytest -q
uvx ruff check .
uvx ty check

git -C "$(git rev-parse --show-toplevel)" \
  apply --numstat "$PWD/go-inlibrary.patch"

git diff --check
uv run contexttrack testdata/v4-chain.jsonl
```

Also run the unchanged v3 fixture and verify that rejection is specifically due
to its old schema.

Allocate independent clean Go acceptance trees:

```bash
REPO=$PWD
WORK=$(mktemp -d)

for name in go-a go-b; do
  git clone --depth 1 --branch go1.26.6 \
    https://go.googlesource.com/go "$WORK/$name"

  bash "$REPO/apply-go-patch.sh" "$WORK/$name"
done
```

Build each tree with capture disabled. Set `GO_WORK` to each acceptance tree
before running the following block. For all Go commands use the explicit
patched executable:

```bash
PATCHED_GO="$GO_WORK/bin/go"

clean_go() {
  env -u GOROOT -u CONFTAMER_EVENTS \
    -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID \
    GOTOOLCHAIN=local "$PATCHED_GO" "$@"
}

(
  cd "$GO_WORK/src"
  env -u GOROOT -u CONFTAMER_EVENTS \
    -u CONFTAMER_EVENTS_DIR -u CONFTAMER_CAPTURE_ID \
    GOTOOLCHAIN=local ./make.bash
)

clean_go version
clean_go env GOROOT
clean_go test net/http -run '^TestConftamer' -count=1
clean_go test -race net/http -run '^TestConftamer' -count=1
clean_go test net/http -count=1
clean_go test -c -o "$GO_WORK/http.test" net/http
```

Run each capture example separately from `"$GO_WORK/src/net/http"` using the
compiled binary, a fresh temporary capture directory, and capture enabled
**only for that execution**:

```bash
for example in TestConftamerCaptureExample TestConftamerForwardingCaptureExample; do
  CAPTURE=$(mktemp -d "$WORK/capture.XXXXXX")
  (
    cd "$GO_WORK/src/net/http"
    env -u GOROOT -u CONFTAMER_EVENTS GOTOOLCHAIN=local \
      CONFTAMER_EVENTS_DIR="$CAPTURE" CONFTAMER_CAPTURE_ID="v4-$example" \
      "$GO_WORK/http.test" -test.run "^${example}$" -test.count=1 -test.v
  ) >"$CAPTURE.stdout" 2>"$CAPTURE.stderr"

  (
    cd "$REPO"
    uv run contexttrack "$CAPTURE"
  )
done
```

Inspect:

- stdout and stderr;
- enablement and failure diagnostics;
- process files and record kinds;
- expected workload activity;
- exact source references and witnesses;
- expected graph counts.

Never put these real captures in `testdata/`.

## 7. Size and scope gates

Measured current baseline:

| Area | V3 baseline | V4 budget |
| --- | ---: | ---: |
| Handwritten production | 843 lines | **<=993** |
| Tests | 1,948 lines | Target **<=2,248** |

Counts include comments and blanks. Count overlays once and native patch
additions once. Report annotation lines in test applications separately as part
of the integration cost.

The production ceiling implements the earlier **+150-line budget**. The test
target allows roughly 300 net new lines through reuse and replacement of
obsolete context tests.

**If a budget is exceeded, stop for review.** Do not compress code, remove
necessary checks, hide logic in generated files, or quietly expand scope.

Explicitly excluded:

- automatic taint or goroutine tracking;
- shared mutable dependency contexts;
- route/API enrichment;
- additional graph modes;
- cross-process stitching;
- HTTP/2 capture;
- consumer or external application migration.

## Completion criteria

V4 is ready only when:

1. Every emitted edge has an exact occurrence witness and an allowed
   justification.
2. The reviewed precision suite retains required dependencies and rejects
   spurious ones.
3. Real producer captures agree with the reader's interpretation.
4. Protocol-preservation and race tests pass.
5. Independent patch applications and builds succeed.
6. The size audit passes or receives explicit approval.
7. The report separates verified behavior from annotation assumptions and
   unsupported coverage.

The implementation commits record completed work, not permanent acceptance
evidence. Apply the current `AGENTS.md` verification rules before claiming that
these completion criteria hold in a new checkout or environment.
