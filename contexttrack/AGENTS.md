# ConfTamer agent guide

## Scope and sources

ContextTrack v3 is a producer-side prototype: patched Go `net/http` HTTP/1
observations, one strict Python reader, and one text diagnostic. The exact
record/label/influence rules are in [OUTPUT.md](OUTPUT.md). The paper
`../../ConfTamer_HotNets_2026.pdf`, especially Sections 4–5 and Figures 2–3,
provides conceptual terminology; current source, tests, and fresh captures
determine implemented behavior.

Do not claim ParamTrack, CType analysis, AppGraph stitching, execution replay,
module ownership, API/route labeling, causality, delivery, completeness, Caddy,
or Kubernetes support. Version 1/v2 captures and
`docs/history/contexttrack-v2-implementation.md` are historical evidence, not
the v3 contract. Consumer migration belongs to its separately owned repository.

## Semantic invariants

Keep record, exchange, and context identities distinct and scoped by capture and
process. Associate responses only by exact exchange identity. For each known
context form every received-label × sent-label pair, independent of order. Do
not add chronology, URL matching, or same-exchange exclusion. Null contexts do
not group; preserve occurrences through association and isolated labels in
output.

Only `ConftamerContext` is exported for annotation. Server ingress creates a
root; client hooks read roots and never mutate caller requests. Do not infer
roots, routes, APIs, organizations, modules, or missing messages. Preserve HTTP
results, bodies, errors, cancellation, retries, redirects, trailers, flushing,
callbacks, and caller request/context identity. Logging may affect timing.

Bundled HTTP/2 must fail capture once at each guarded boundary without changing
protocol behavior. External HTTP/2, custom transports, mocks, pre-dispatch
rejection, hijacked/tunneled traffic, and bodies remain outside coverage.

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
- Inspect stdout, stderr, kinds, expected activity, and unknown-context counts.
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

In a freshly patched tree run focused producer tests, focused race tests, and
full `net/http`, all with capture disabled. Compile a test binary first, then
run the capture example with capture enabled and parse it with
`uv run contexttrack CAPTURE`. Also run the synthetic v3 fixture. Before
completion inspect the complete diff and untracked files, report exact commands
and results, and separate patch application, build, producer emission, reader
integrity, and unsupported consumer work.
