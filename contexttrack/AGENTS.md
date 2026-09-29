# ContextTrack agent guide

## Scope and sources of truth

This file applies to `conftamer/contexttrack/` (the Git and Go module root is
`conftamer/`, one directory up). Check `git status --short --branch` before
editing and preserve unrelated or untracked work.

Use the committed producer on `conftamer/main`, or on `contexttrack-fix-v1`
when that branch is checked out (it fixes `go-inlibrary.patch` and adds
`tests/httpcapture/`), and the explicitly selected
**`conftamer-cli/node-query`** branch for its downstream consumer. Inspect the
latter with `git -C ../../conftamer-cli show node-query:path/to/file`; its
untracked files and other branches are not evidence of implemented behavior.
Treat other sibling implementations outside these branches as out of scope. Do
not edit sibling repositories, the installed Go tree, or existing captures
unless asked.

The [HotNets paper](../../ConfTamer_HotNets_2026.pdf) is **authoritative for
ConfTamer's goals, terminology, and intended PMGraph/AppGraph semantics**
(especially §§4–5 and Figure 3). Per-module PMGraphs relate inputs (parameters
and received messages) to outputs (sent messages); a separate step composes
them into an AppGraph. Read [the ContextTrack README](README.md) and committed
producer/consumer code for the prototype's *current* capture procedure and
event format. If implementation differs from the paper, identify the gap;
do not redefine the paper's design or claim an unimplemented feature exists.

## Ownership and current pipeline

- This directory is the **message/context producer**: `go-inlibrary.patch`
  and `_goroot/src/net/http/conftamer.go` instrument Go's `net/http` (via a
  build overlay on a stock toolchain, or a patched clone) and emit HTTP events
  to an opt-in JSONL file. It does not itself produce a PMGraph or AppGraph.
- `analysis/` contains small, standard-library Python scripts for inspecting
  those traces. Their co-occurrence graphs are diagnostic views, **not** the
  canonical PMGraph or proof that one message caused another.
- The downstream `../../conftamer-cli` **`node-query` branch** consumes one
  UTF-8 capture with `src/conftamer/contexttrack.py:load_contexttrack` and
  builds a **message-only PMGraph**. Its `build` command requires a
  caller-supplied `--module-id` and a new `--output` file; `export` emits GraphML and
  `query` searches exact PMGraph node IDs. Read that branch's `README.md`,
  importer, and `tests/test_contexttrack.py` before changing producer fields.
  Parameter ingestion, cross-module stitching, and full AppGraph construction
  are **not implemented there**. Parameter/CType discovery is outside this
  directory. The legacy Delve entry point `../cmd/context/` is not the
  patched-standard-library capture workflow.

| File | Role |
| --- | --- |
| `go-inlibrary.patch` | Hook calls added to stock Go 1.26.6 `net/http` files (applied with `-p4`). |
| `_goroot/src/net/http/conftamer.go` | New `net/http` file: hook implementations and the JSONL serializer. `_` keeps it out of the `conftamer` module. |
| `scripts/setup-go.sh` | Builds/caches the `-overlay` for a stock Go 1.26.6 (never modifies GOROOT), or `--clone DEST` for a patched copy. |
| `bin/ctgo` | `go` wrapper: overlay via `GOFLAGS`, `GOTOOLCHAIN=local`, fresh `CONFTAMER_EVENTS`, `-count=1` for `test`. |
| `scripts/check.sh` | Runs capture/tooling checks (static, tooling tests, `tests/httpcapture`, upstream `net/http` with tracing, analysis smoke); does not run ty, Ruff, or Tombi. |
| `tests/test_tooling.py` | Regression tests for `setup-go.sh` and `ctgo` (stdlib `unittest`). |
| `prometheus-common-route.patch` | Optional route-pattern hook for Prometheus `common/route`, outside `net/http`'s `ServeMux`. |
| `go-inlibrary-optional.patch` | Historical heap-address context-root experiment; **not** the default ID-based format. |
| `analysis/event_io.py` | JSONL reader (blank lines skipped, malformed lines warned about). |
| `analysis/group_by_context.py` | Text grouping and summaries. |
| `analysis/message_graph.py` | Text/DOT graph of messages observed in a context group. |
| `tests/httpcapture/` | Loopback regression tests for the patched Go (on `contexttrack-fix-v1`); run as the README describes. |

## Preserve the producer contract

- Logging is disabled when `CONFTAMER_EVENTS` is unset. When set, the patched
  library appends **one JSON object per event** to that path; never assume a
  fresh file or a globally ordered trace from several processes. Keep the
  opt-in behavior, schema, and reasonably cheap disabled path intact.
- The five event kinds are `Request sent`, `Request received`, `Request routed`,
  `Response sent`, and `Response received`. Keep their nested `message` and
  `context` objects and the existing field names (`req.Method`,
  `req.URL.Path`, `pattern`, `code`, `resp.StatusCode`, etc.). The event also
  carries `pid`, source/debug fields, and sometimes `request_id`, `api_id`, or
  `handler`. Do not silently rename or drop fields consumed downstream.
- The primary patch stamps a monotonic, **process-local** `context.context_id`
  such as `id:7` at HTTP request origins; derived contexts can inherit it.
  Group by **`(pid, context_id)`**, not ID alone. It is an influence heuristic,
  not a unique distributed trace ID or proof of per-request causality. Reusing
  one append-only file for separate runs can also mix unrelated captures.
- Apart from logging, a traced program must behave like stock Go. Never stamp
  a caller-owned request in place (`main`'s patch still does); it races with
  the caller's readers. In the fixed patch, a request whose context lacks an ID
  is sent as a private copy linked to the caller's request, which Go must
  still expose and cancel (`Response.Request`, `CheckRedirect`'s `via`,
  `Transport.CancelRequest`). Unwrap only that exact copy, not Go's or an
  application's copies of it, and copy only when the ID is missing: copying
  every request breaks `CancelRequest`.
- `request_id` is an outbound **endpoint label** (method/host/path), not an
  occurrence or correlation ID. `api_id` is a best-effort package/API
  association, not the caller-supplied PMGraph `module_id`. The `node-query`
  importer reads outbound labels from `request_id`; a missing client host
  prevents a complete send node. In the fixed patch, `Request sent` records a
  send attempt; requests that `Transport` rejects before any attempt are not
  logged unless an alternate protocol may take them. `Response sent` may need
  attribution to the received request; HTTP client and wire hooks may both
  report a response.
- `Request routed` contributes a handler pattern rather than a message node.
  Plain `ServeMux` is instrumented by the Go patch; Prometheus' `common/route`
  needs its separate patch for precise patterns. The `node-query` importer
  associates routes and responses only when a **unique earlier request** has
  the same context, method, and concrete path. Rewritten or ambiguous routes
  warn and fall back to the concrete path; unmatched or ambiguous responses
  are omitted. Do not describe nested/rewritten routes as reconstructed by
  this consumer.
- Unlike the local `analysis/event_io.py` reader, which warns and skips bad
  JSONL lines, the `node-query` importer **raises on malformed JSON or invalid
  consumed types/values** (with path and physical line); unsupported kinds or
  incomplete/ambiguous associations warn and may be omitted. Preserve its
  required `pid`, `message`, request labels, and string status codes, and check
  the importer before changing optional fields.
- The optional root-address patch changes the correlation schema to
  `context.root_addr` and is kept for historical comparison. Do not combine it
  with the default `context_id` analysis or promise cross-run correlation.

## Capture and inspect safely

Prefer `bin/ctgo`, which overlays the instrumentation on a stock Go 1.26.6
without modifying it and checks each run that the overlay took effect. Apply
`go-inlibrary.patch` in place only to a **disposable, version-matching clone**
(`scripts/setup-go.sh --clone DEST` does this and adds `conftamer.go`); do not
patch `/usr/local/go` or a read-only Go module cache in place.
Prometheus route instrumentation likewise belongs in a writable copy of
`github.com/prometheus/common` with an appropriate `replace` directive; follow
[README.md](README.md) for the version and example. A failed patch hunk is a
version mismatch to investigate, not a reason to force an unrelated edit.

For a focused HTTP test (`ctgo` prints the fresh `CONFTAMER_EVENTS` path and
adds `-count=1`):

```bash
/path/to/conftamer/contexttrack/bin/ctgo test ./path/to/http/package
```

With a patched clone instead:

```bash
unset GOROOT                         # avoid compiling against an unpatched stdlib
export GOTOOLCHAIN=local
export CONFTAMER_EVENTS="$(mktemp /tmp/contexttrack-focused.XXXXXX.jsonl)"
/path/to/patched-go/bin/go env GOROOT  # verify it names the patched tree
/path/to/patched-go/bin/go test -count=1 ./path/to/http/package
```

Run the test from the **target module**, not this directory. Ensure the output
directory exists; inspect the `conftamer: enabled — writing ...` diagnostic
and actual events. `-count=1` prevents `go test` caching from producing an
empty capture. The patch covers standard-library HTTP hooks, not every
external router, mocked HTTP library, `golang.org/x/net/http2` path, or
`ClientConn` from `Transport.NewClientConn` (which bypasses `Request sent`);
see the README's Prometheus/Caddy/Kubernetes caveats. Keep each run's JSONL
separate.

From `conftamer/contexttrack/`, always pass an explicit trace path (the two
analysis commands have different defaults):

```bash
python3 analysis/group_by_context.py "$CONFTAMER_EVENTS"
python3 analysis/message_graph.py "$CONFTAMER_EVENTS" --format text
python3 analysis/message_graph.py "$CONFTAMER_EVENTS" --recv-sent --format dot > /tmp/contexttrack-messages.dot
# Optional, if Graphviz is installed: dot -Tsvg /tmp/contexttrack-messages.dot -o /tmp/contexttrack-messages.svg
```

`message_graph.py` normally links **consecutive distinct** messages in each
context group; `--recv-sent` instead links each received message to later sent
messages. Neither mode is an occurrence-accurate, cross-process causal graph.

## Change and verification checklist

1. Trace any changed field from its Go hook through the JSONL serializer, the
   local `analysis/` scripts, and the **`conftamer-cli/node-query`** importer
   and tests. Distinguish the paper's authoritative design from producer facts
   and consumer heuristics.
2. For behavior changes, add focused regression coverage for the affected
   case (for example, repeated hooks, route patterns, redirects, missing
   fields, or identical context IDs in different PIDs). `conftamer/main` has
   **no checked-in ContextTrack tests**; on `contexttrack-fix-v1`, add them to
   `tests/httpcapture/` and run that suite as the README describes. Do not
   claim that a local, untracked test suite is part of the baseline.
3. Run `scripts/check.sh` (see README). For Python/TOML work, also run
   `uvx ty check` for Python type checking, `uvx ruff check` and
   `uvx ruff format --check` for lint and formatting checks, and
   `uvx tombi lint` and `uvx tombi format --check` for changed/in-scope `.toml`
   files before marking a task complete or making an authorized commit.
   **Do not run Tombi (lint or format) on `uv.lock`, including temporary
   copies.** Use explicit `.toml` paths, not directory-wide targets; here, run
   `uvx tombi lint pyproject.toml` and
   `uvx tombi format --check pyproject.toml`. `uv.lock` is generated by uv:
   regenerate it with `uv lock`, validate it with `uv sync --locked --dev`,
   and review its dependency/metadata diff without reformatting it. It is
   excluded from Tombi completion gates, not from dependency or human review.
   `scripts/check.sh` does not run these tools or the consumer compatibility
   build. Pass changed/in-scope paths explicitly, record the commands, scope,
   and results, and report unavailable tools
   or pre-existing out-of-scope failures; do not silently skip checks or fix
   unrelated files. By hand: run Python syntax checks and both analysis
   commands against a small JSONL trace. Use the committed consumer fixture,
   not untracked captures:

   ```bash
   SMOKE=$(mktemp /tmp/contexttrack-smoke.XXXXXX.jsonl)
   git -C ../../conftamer-cli show node-query:examples/contexttrack/prometheus/scrape-ok.jsonl > "$SMOKE"
   python3 -m py_compile analysis/*.py
   python3 analysis/group_by_context.py "$SMOKE"
   python3 analysis/message_graph.py "$SMOKE" --format text
   ```

   For compatibility testing in a `conftamer-cli/node-query` checkout, follow
   its README to `build` this capture into a **new** PMGraph file and run its
   focused `tests/test_contexttrack.py` and `tests/test_cli.py`. It does not
   overwrite existing output files.
4. Edit `_goroot/src/net/http/conftamer.go` directly. If changing the hook
   calls, regenerate `go-inlibrary.patch` from clean and modified Go trees
   (keeping the `a/usr/local/go/` and `b/home/tcr6/go-conftamer/` prefixes that
   `-p4` strips) instead of hand-editing hunks; it must not contain
   `conftamer.go`. Dry-run it on the intended Go version (`setup-go.sh`
   applies it with no fuzz), run a focused instrumented HTTP test with `-count=1`, and inspect
   emitted events. Also run the upstream `net/http` short tests with tracing
   **on** (see README): tracing-off runs cannot catch changes the hooks make to
   request identity or cancellation. Coordinate any incompatible schema change
   with the consumer rather than updating only these exploratory scripts.
   Check `git diff --check` and review the final diff; do not commit captured
   event files or generated graphs by accident.
