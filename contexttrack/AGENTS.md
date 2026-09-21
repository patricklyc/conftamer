# ConfTamer agent guide

## Scope and sources

ContextTrack v4 is an HTTP/1 producer-side prototype with one strict Python
reader and one text diagnostic. The exact v4 record, label, source, and
influence rules are in [OUTPUT.md](OUTPUT.md). The paper
`../../ConfTamer_HotNets_2026.pdf`, especially Sections 4–5 and Figures 2–3,
provides conceptual terminology; current source, tests, and fresh captures
determine implemented behavior.

Task 1 is an explicit intermediate state: the Python package reads v4 while the
patched Go producer remains v3 until Task 2 of
`docs/contexttrack-v4-plan.md`. Do not claim a working v4 producer/reader pair or
v4 producer evidence before that gate passes.

Do not claim ParamTrack, CType analysis, AppGraph stitching, execution replay,
module ownership, API/route labeling, causality, delivery, completeness, Caddy,
or Kubernetes support. Version 1/v2/v3 captures and historical plans are not
the v4 contract. Consumer and application migration belongs to separately
owned repositories or tasks.

## Semantic invariants

Keep record and exchange identities distinct and scoped by capture and process.
Associate responses only by exact exchange identity. Resolve each source only
to an actual preceding receive in the same capture process. Associate exact
occurrences before merging semantic labels, and retain repeated occurrences and
isolated labels.

Create edges only from recorded source references. The one automatic edge is a
received server request to its own server response; every server response must
name that exact receive. Other data or control dependencies require explicit
source declarations. Do not add context matching, chronology, URL matching,
nearest-event matching, inferred transitivity, or source inheritance from a
sent request to its received response. An empty source list means unknown or
undeclared influence, not independence.

Version 4 removes `context_id`, capture-specific root bookkeeping, and the
shared-context graph. The completed producer API exports only
`ConftamerSource`, `ConftamerWithSources`, and `ConftamerSetReplySources` for
annotation; these helpers are Task 2 work and are not implemented by the Task 1
producer overlay. Do not infer roots, routes, APIs, organizations, modules, or
missing messages.

Preserve HTTP results, bodies, errors, cancellation, retries, redirects,
trailers, flushing, callbacks, and caller request/context identity. Logging may
affect timing. Bundled HTTP/2 must fail capture once at each guarded boundary
without changing protocol behavior. External HTTP/2, custom transports, mocks,
pre-dispatch rejection, hijacked/tunneled traffic, and bodies remain outside
coverage.

## Safety and workflow

- Start with `git status --short --branch`; preserve unrelated changes.
- Keep sibling repositories/captures read-only unless explicitly authorized.
- Never modify the system Go tree, module cache, or an existing experiment.
- Use fresh clean Go `go1.26.6` trees and temporary capture directories.
- Apply the patch twice to independent clean trees before reproducibility claims.
- Invoke the patched executable explicitly with `GOROOT` unset and
  `GOTOOLCHAIN=local` on every Go command.
- Compile with capture disabled; enable it only while running the selected
  binary from the package directory.
- Inspect stdout, stderr, kinds, expected activity, source references, and
  sends without sources.
- A passing test, valid JSON, or empty capture is not producer evidence.
- Add a focused failing regression before behavior changes.
- Do not delegate, commit, push, migrate consumers, or run broad external suites
  without explicit authorization.
- Never check real captures into `testdata/`; they can expose URLs and paths.

## Required verification

```bash
uv sync --dev
uv run pytest -q
uvx ruff check .
uvx ty check
git -C "$(git rev-parse --show-toplevel)" apply --numstat "$PWD/go-inlibrary.patch"
git diff --check
```

In freshly patched trees run focused producer tests, focused race tests, and
full `net/http`, all with capture disabled. Compile a test binary first, then
run each selected capture example with capture enabled and parse it with
`uv run contexttrack CAPTURE`. Also run the synthetic v4 fixture and confirm the
unchanged v3 fixture is rejected. Before completion inspect the complete diff
and untracked files, report exact commands and results, and separate patch
application, build, producer emission, reader integrity, and unsupported
consumer work.
