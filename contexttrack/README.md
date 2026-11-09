# ContextTrack

Capture HTTP messages and context evidence from Go's `net/http`, then normalize a
completed raw JSONL capture into typed Python events.

- [Purpose and boundaries](#purpose-and-boundaries)
- [Prerequisites](#prerequisites)
- [Capture then normalize](#capture-then-normalize)
- [Capture HTTP traffic](#capture-http-traffic)
- [Normalize an existing capture](#normalize-an-existing-capture)
- [Output and safety](#output-and-safety)
- [Capture setup and diagnostics](#capture-setup-and-diagnostics)
- [Inspect raw output](#inspect-raw-output)
- [Platform and application caveats](#platform-and-application-caveats)
- [Development and verification](#development-and-verification)

## Purpose and boundaries

```text
Instrumented Go → raw JSONL → Python normalizer → normalized v1 JSONL
                      └──→ raw diagnostics / current consumer
```

Opt-in tracing writes unversioned raw JSONL; normalization writes a separate
**schema v1** file. ContextTrack produces **evidence, not graphs**: shared contexts
suggest influence, not exact causality. Parameter discovery, cross-module stitching,
and distributed tracing are outside this package's scope.

*Taming Configuration in Multi-Module Applications* (HotNets, §§4–5/Figure 3)
defines per-module PMGraphs (parameters and received messages → sent messages),
then AppGraph composition. Its [workspace-local PDF](../../ConfTamer_HotNets_2026.pdf)
is not shipped with this repository or Python packages.

- **README:** installation, capture, caveats, and checks.
- **[OUTPUT.md](OUTPUT.md):** event contracts, CLI, Python API, and pending consumer migration.
- **[MAINTAINING.md](MAINTAINING.md):** instrumentation internals and upgrades.

## Prerequisites

Bash examples; use the working directory shown. Capture is tested on
**Linux/amd64**; other environments are unverified.

| Task | Prerequisites | Working directory |
| --- | --- | --- |
| Capture | **Stock Go 1.26.10**, modern Bash and [shell utilities](#capture-setup-and-diagnostics) | Target Go module |
| Normalize | **Python >=3.14**, [uv](https://docs.astral.sh/uv/) (or an installed package) | `contexttrack/` with uv |
| Inspect raw | **Python >=3.10**; Graphviz only for SVG rendering | `contexttrack/` |

Go 1.26.10 includes the upstream [eMudhra certificate-distrust fix](https://go.dev/blog/emudhra-incident).
Installing the compiler or editing `go.mod` does **not** update existing binaries:
rebuild affected applications, verify `go version -m /path/to/binary` reports
`go1.26.10`, then replace and restart the deployed artifacts. Do not reuse an old
instrumented clone; see [upgrade verification](MAINTAINING.md#go-upgrades).

## Capture then normalize

**Start here.** From `contexttrack/`, run this block in Bash to install Python
dependencies, capture loopback HTTP traffic with the included tests, and normalize
it. uv and stock Go 1.26.10 must already be on PATH; no application checkout or
path substitutions are needed.

```bash
CT="$(pwd -P)" &&
uv python install 3.14 &&
uv sync --locked --no-dev &&
RUN="$(mktemp -d /tmp/contexttrack-run.XXXXXX)" &&
(
  cd "$CT/tests/httpcapture" &&
  CONFTAMER_EVENTS="$RUN/raw.jsonl" "$CT/bin/ctgo" test -v \
    -run '^(TestRoundTripCapture|TestRedirectLabels|TestInheritedContext)$' .
) &&
test -s "$RUN/raw.jsonl" &&
uv run --locked --no-dev contexttrack normalize "$RUN/raw.jsonl" \
  --output "$RUN/normalized.jsonl" &&
wc -l "$RUN/raw.jsonl" "$RUN/normalized.jsonl" &&
printf 'Raw: %s\nNormalized: %s\n' "$RUN/raw.jsonl" "$RUN/normalized.jsonl"
```

Expect `PASS`, equal nonzero line counts, and both paths in a fresh `$RUN`
directory. `&&` stops on failure; normalization waits for tests to finish.
See [output and safety](#output-and-safety).

**For your application**, change the subshell's `cd` to your target Go module
and replace `-run '...' .` with HTTP packages such as `./scrape/` or `./...`.
See the [application recipes](#target-module-commands-and-prerequisites).

## Capture HTTP traffic

Only need raw output? Put [`ctgo`](bin/ctgo) on PATH. For another stock toolchain,
use `export CONFTAMER_GO=/path/to/stock-go1.26.10/bin/go`.

```bash
CT=/path/to/conftamer/contexttrack
export PATH="$CT/bin:$PATH"
RUN=$(mktemp -d /tmp/contexttrack-run.XXXXXX) &&
cd /path/to/target/module &&
CONFTAMER_EVENTS="$RUN/raw.jsonl" ctgo test -v ./path/to/http/package &&
wc -l "$RUN/raw.jsonl"
# conftamer: enabled — writing "/tmp/contexttrack-run....../raw.jsonl"
```

Keep `$RUN/raw.jsonl`; see [output and safety](#output-and-safety) and
[capture diagnostics](#capture-setup-and-diagnostics).

## Normalize an existing capture

**No Go or consumer repository needed.** From a source checkout:

```bash
cd /path/to/conftamer/contexttrack &&
uv python install 3.14 &&
uv sync --locked --no-dev &&
uv run --locked --no-dev contexttrack --help
```

Stop capture; keep the raw file and choose a **new output in an existing directory**:

```bash
uv run --locked --no-dev contexttrack normalize /path/to/raw.jsonl --output /path/to/new-normalized.jsonl
```

Success is silent (exit 0); input/filesystem errors exit 2 with diagnostics and
no traceback. See [output and safety](#output-and-safety), the
[before/after walkthrough](OUTPUT.md#walkthrough-one-received-request), and
[validation/publication rules](OUTPUT.md#streaming-jsonl-io).

<details>
<summary>Alternative: install a wheel into a separate environment</summary>

### Install a wheel instead

For an existing Python 3.14+ environment, from `contexttrack/`:

```bash
DIST=$(mktemp -d /tmp/contexttrack-dist.XXXXXX) &&
uv build --wheel --out-dir "$DIST" &&
uv pip install --python /path/to/venv/bin/python "$DIST"/contexttrack-*.whl
```

Use that environment's interpreter; activation and PATH changes are unnecessary:

```bash
/path/to/venv/bin/python -m contexttrack --help
```

</details>

## Output and safety

| File | Use with |
| --- | --- |
| Raw JSONL (`$RUN/raw.jsonl` in the quick start) | `analysis/` and the current committed `conftamer-cli/node-query` consumer |
| Normalized v1 JSONL (`$RUN/normalized.jsonl`) | ContextTrack's [typed readers](OUTPUT.md#public-python-api), **not** those raw consumers |

Normalization keeps one event per raw record, including repeated hooks; empty
captures produce empty output. [Consumer migration](OUTPUT.md#consumer-handoff-separately-authorized-work)
is pending.

- **Fresh paths per run:** the Go producer appends; reuse mixes runs.
- **Stop capture before normalizing:** preserve raw input; use a new output in an
  existing directory. Normalization never overwrites.
- **No redaction:** queries, hosts, handlers, and source paths may be sensitive.
  Protect both files; never commit real captures or graphs.
- **Evidence only:** nonempty captures and successful normalization do not prove
  completeness, HTTP delivery, or correct influence attribution.

See the [privacy and audit limits](OUTPUT.md#privacy-trust-boundaries-and-human-audit).

## Capture setup and diagnostics

The scripts need modern Bash (`mapfile` and `[[ -v ... ]]`), `readlink -f`,
`sha256sum` (or `shasum`), standard shell utilities, and GNU `patch` (or Git for
setup's fallback). Rebuilds may take minutes.

`ctgo` uses [`setup-go.sh`](scripts/setup-go.sh) to apply
[`go-inlibrary.patch`](go-inlibrary.patch) to copied Go files and add
[`conftamer.go`](_goroot/src/net/http/conftamer.go) through an overlay, **without
changing installed GOROOT**. Mismatched/already-patched trees are rejected;
never force a patch. Each invocation verifies the overlay and exports
`GOTOOLCHAIN=local` and `GOFLAGS` for child Go commands. Tests default to `-count=1`
to avoid cached runs; use `-v` to expose passing-test diagnostics.

Cache: `${XDG_CACHE_HOME:-~/.cache}/conftamer/`, overridden by
`CONFTAMER_CACHE_DIR`. Toolchain, patch, or source changes invalidate it.

| `CONFTAMER_EVENTS` | Behavior |
| --- | --- |
| Unset with `ctgo test`/`run` | Creates a fresh file and prints its path; `CONFTAMER_EVENTS_DIR` selects the directory (default `$TMPDIR` or `/tmp`) |
| Explicit nonempty path | Appends one JSON object per event; the producer does not create parent directories |
| Empty | Disables tracing |
| Unset without `ctgo` | Disables tracing |

Startup reports enabled (the file opened) or `open failed`. Write failures
trigger at most one best-effort stderr warning per process:

```text
conftamer: capture write failed for "DESTINATION": ERROR; capture may be incomplete or invalid
```

Failures may leave invalid JSONL. HTTP does not wait for the write-warning worker;
[implementation details](MAINTAINING.md#capture-write-warning-implementation)
cover delivery and failed-write handling.

<details>
<summary>Advanced alternatives: plain Go or a disposable clone</summary>

### Plain Go with an overlay

From `contexttrack/`:

```bash
overlay_env=$(scripts/setup-go.sh --env) &&
  eval "$overlay_env"
```

Use the same Go executable, a fresh `CONFTAMER_EVENTS`, and `-count=1`; exported
GOROOT must name stock Go 1.26.10. Editors/gopls miss overlays. Event paths name
stock files, but line numbers refer to patched sources.

### Disposable clone fallback

If builds override `GOFLAGS`/`GOTOOLCHAIN`, use a new absolute clone path.
The command verifies Go uses the clone:

```bash
CT=/path/to/conftamer/contexttrack
CLONE=/path/to/go-conftamer
cd "$CT" &&
scripts/setup-go.sh --clone "$CLONE" &&
unset GOROOT &&
export GOTOOLCHAIN=local &&
[[ "$("$CLONE/bin/go" env GOROOT)" -ef "$CLONE" ]] &&
CONFTAMER_EVENTS=$(mktemp /tmp/contexttrack-clone.XXXXXX.jsonl) &&
export CONFTAMER_EVENTS &&
(cd /path/to/target/module &&
  "$CLONE/bin/go" test -v -count=1 ./path/to/http/package)
```

Never patch installed Go or module caches; see [maintenance](MAINTAINING.md).

</details>

## Inspect raw output

From `contexttrack/`, pass a completed **raw** path explicitly:

```bash
EV=/path/to/fresh-raw.jsonl
python3 analysis/group_by_context.py "$EV"
python3 analysis/message_graph.py "$EV" --format text
python3 analysis/message_graph.py "$EV" --recv-sent --format dot > /path/to/new-messages.dot &&
# Optional: Graphviz, another fresh destination, and a graph with edges:
if grep -q '^digraph messages {' /path/to/new-messages.dot; then
  dot -Tsvg /path/to/new-messages.dot -o /path/to/new-messages.svg
fi
```

No edges produces `(no edges found)`, not DOT; the guard skips rendering.
The raw-analysis reader rejects empty files.

[`message_graph.py`](analysis/message_graph.py) keeps each node key's first
occurrence per `(pid, context_id)` group: `A → B → A → C` becomes `A → B → C`.
It links consecutive deduplicated nodes; `--recv-sent` links receives to later
sends in that sequence. Neither preserves occurrence-level ordering. These are
diagnostic views, **not PMGraphs or causal graphs**; see the
[raw mapping](OUTPUT.md#raw-mapping-and-strictness-differences).

## Platform and application caveats

### Instrumented request and hook boundaries

This is not a complete wire capture:

- **Contexts:** IDs originate at HTTP boundaries; derived contexts and redirects
  inherit them. Unstamped contexts report `context.error`. Group by
  **`(pid, context_id)` within one capture** ([identity](OUTPUT.md#context-identity)).
  The historical [root-address patch](go-inlibrary-optional.patch) is incompatible
  with ID-based analysis and supplies no cross-run identity.
- **Requests:** Caller-owned requests are not stamped in place. Custom RoundTrippers,
  `Transport.Proxy`, and registered protocols may see private copies;
  see [request identity](MAINTAINING.md#request-identity-and-debug-metadata).
- **Client:** `Request sent` records attempts, including later dial failures.
  Pre-attempt rejects are not logged unless an alternate protocol can take them.
  `Client.Do` and wire hooks may both report a response. Bundled HTTP/2 records
  final client headers, including direct transport calls, not informational responses.
  External `golang.org/x/net/http2` is not covered; `Transport.NewClientConn`'s
  `ClientConn` bypasses send hooks.
- **Server:** Receipt precedes handler dispatch; earlier protocol rejects and
  mocked/bypassed HTTP paths are not covered. External routers need route hooks.
- **Attribution:** `request_id` labels endpoints, not occurrences; `api_id` is
  best-effort, not module identity. Sent responses have no API/handler association.
  See [debug metadata limits](MAINTAINING.md#request-identity-and-debug-metadata).

### Target-module commands and prerequisites

<details>
<summary>Optional recipes: Prometheus, Caddy, and Kubernetes</summary>

These **illustrative, version-dependent** commands have no verified application
revisions. Check your checkout's prerequisites; use writable modules and
instrumented Go with fresh captures.

| Target directory | Command | Prerequisites / limits |
| --- | --- | --- |
| Prometheus root | `ctgo test -v ./...` or `ctgo test ./scrape/ -run TestTargetScraperScrapeOK -v` | Precise routing needs the patch below; stdlib/module rebuild can be slow |
| Caddy root | `ctgo test -p 1 ./caddytest/integration/` (optionally `-run TestReverseProxySubroutes`) | `-p 1` prevents fixed-port collisions; external HTTP/2/internal `caddyhttp.(*Server).ServeHTTP` paths can miss hooks |
| Caddy root | `ctgo test ./modules/caddyhttp/reverseproxy/` | Unit tests may mock HTTP and emit no evidence |
| Kubernetes `staging/src/k8s.io/client-go` | `ctgo test ./transport/... ./rest/... ./tools/...` | No etcd; this covers selected staging packages, not all modules |
| Kubernetes root | `ctgo test ./test/integration/endpoints/` (optionally `-run TestEndpointWithMultiplePods`) | etcd on PATH; other integration packages have their own prerequisites |

Prometheus `common/route` around `julienschmidt/httprouter` can yield coarse
ServeMux patterns (`/api/v1/` for `/api/v1/query`). The
[route patch](prometheus-common-route.patch) targets **common v0.69.0**, not a
verified Prometheus release. Confirm the dependency and choose a new destination:

```bash
COMMON="$HOME/common-conftamer"
[[ ! -e "$COMMON" && ! -L "$COMMON" ]] &&
cp -r "$HOME/go/pkg/mod/github.com/prometheus/common@v0.69.0" "$COMMON" &&
chmod -R u+w "$COMMON" &&
(cd "$COMMON" && patch -p4 < /path/to/contexttrack/prometheus-common-route.patch)
```

In Prometheus's `go.mod`: `replace github.com/prometheus/common => /path/to/common-conftamer`.
Build with instrumented Go for `http.ConftamerLogRouted`. Patterns retain `{name}`,
`:name`, and `*path`; the consumer cannot reconstruct ambiguous/rewritten routing
chains. Never patch the module cache.

</details>

<details>
<summary>Application troubleshooting (check against your revision/environment)</summary>

- **Toolchain overrides:** Kubernetes `make`/`hack/` may change Go or overlay flags;
  verify them or use a clone. Root build failures do not establish hook failures.
- **etcd:** If available, run `hack/install-etcd.sh` and add **its reported directory**
  to PATH before tests.
- **Timeouts:** Kubernetes middleware can race handlers and emit orphan 504s.
  Check `RequestTimeout` in your revision (historically
  `staging/src/k8s.io/apiserver/pkg/server/config.go`); changing it alters the test
  scenario, not normalization.
- **Stale servers:** Check test ports with `ss -tlnp`; identify owners before
  stopping processes.

</details>

## Development and verification

Run from `contexttrack/`; see the [source map](MAINTAINING.md#source-map).

### Python checks and generated schema

```bash
uv sync --locked --dev
uv run --locked pytest -q
```

Schema tests compare with `EVENT_ADAPTER.json_schema()`; **no checked-in snapshot**.
Model and normalization tests own literal expectations. See
[schema export](OUTPUT.md#generated-json-schema).

<details>
<summary>Type, lint, format, and syntax checks</summary>

Check the package and pytest files; exclude unchanged tooling tests:

```bash
PYTHON_SCOPE=(src/contexttrack)
for test_file in tests/*.py; do
  [[ $test_file == tests/test_tooling.py ]] || PYTHON_SCOPE+=("$test_file")
done
uvx ty check "${PYTHON_SCOPE[@]}"
uvx ruff check "${PYTHON_SCOPE[@]}"
uvx ruff format --check "${PYTHON_SCOPE[@]}"
uvx tombi lint pyproject.toml
uvx tombi format --check pyproject.toml  # never run Tombi on generated uv.lock
PYTHONPYCACHEPREFIX="$(mktemp -d /tmp/contexttrack-pycache.XXXXXX)" \
  uv run --locked python -m py_compile src/contexttrack/*.py tests/*.py
```

</details>

### Capture checks

Capture checks also require python3 and gofmt; `-race` needs a supported platform
and C compiler:

```bash
scripts/check.sh  # --no-race without a C compiler; --clone also checks clone mode
```

The [loopback suite](tests/httpcapture/) needs no services. The quick start omits
unstamped-route and dedicated request-ownership/cancellation tests; it is not the
full regression suite.

`check.sh` runs static checks, overlay setup/vet, tooling tests, race capture tests,
**tracing-on** upstream HTTP short tests, then raw diagnostics (consumer fixture
if available). It stops at the first failure; outputs are in
`${TMPDIR:-/tmp}/contexttrack-check.*`. Package pytest, ty/Ruff/Tombi, wheel, and
consumer checks are separate.

<details>
<summary>Focused capture, tooling, and upstream checks</summary>

```bash
python3 -m unittest tests/test_tooling.py -v  # CONTEXTTRACK_TEST_CLONE=1 enables clone tests
(cd tests/httpcapture && ../../bin/ctgo test -race ./...)
(cd tests/httpcapture && ../../bin/ctgo test \
  -run '^(TestRoundTripCapture|TestRedirectLabels|TestInheritedContext)$' ./...)
CTGO="$PWD/bin/ctgo"
(cd "$("$CTGO" env GOROOT)/src/net/http" && "$CTGO" test -short .)
```

Upstream tests require **tracing enabled** for request identity and cancellation.

</details>

Automated checks do not replace [human audit](OUTPUT.md#privacy-trust-boundaries-and-human-audit).
