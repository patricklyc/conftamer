# ContextTrack

Capture HTTP messages and context evidence from Go's `net/http`, then normalize a
completed raw JSONL capture into typed Python events.

- [Capture HTTP traffic](#capture-http-traffic)
- [Normalize an existing capture](#normalize-an-existing-capture)
- [Inspect raw output](#inspect-raw-output)
- [Development and verification](#development-and-verification)
- [Platform and application caveats](#platform-and-application-caveats)

## Purpose and boundaries

```text
Instrumented Go → raw JSONL → Python normalizer → normalized v1 JSONL
                      └──→ raw diagnostics / current consumer
```

Opt-in tracing writes unversioned raw JSONL; normalization writes a separate **schema v1** file.
[OUTPUT.md](OUTPUT.md) owns the contract and pending consumer migration.

ContextTrack produces **evidence, not graphs**. The
[HotNets paper](../../ConfTamer_HotNets_2026.pdf) (workspace-local, outside this repository),
§§4–5/Figure 3, defines per-module PMGraphs (parameters and received messages →
sent messages), then AppGraph composition. Shared contexts suggest influence,
not exact causality. Parameter discovery, stitching, distributed tracing, and
redaction are outside this package's scope.

## Quick starts

Bash examples; use the working directory shown:

| Task | Prerequisites | Working directory |
| --- | --- | --- |
| Capture | **Stock Go 1.26.6**, Bash; tested on Linux/amd64 | Target Go module |
| Normalize | **Python >=3.14**, uv (or an installed package) | `contexttrack/` with uv |
| Inspect raw | **Python >=3.10**; Graphviz only for SVG rendering | `contexttrack/` |

### Capture HTTP traffic

**Recommended: `ctgo` overlays the hooks without modifying installed Go.**
Put [`ctgo`](bin/ctgo) on PATH; select another stock Go 1.26.6 with
`export CONFTAMER_GO=/path/to/stock-go1.26.6/bin/go`.

```bash
CT=/path/to/conftamer/contexttrack
export PATH="$CT/bin:$PATH"
RUN=$(mktemp -d /tmp/contexttrack-run.XXXXXX) &&
cd /path/to/target/module &&
CONFTAMER_EVENTS="$RUN/raw.jsonl" ctgo test -v ./path/to/http/package &&
wc -l "$RUN/raw.jsonl"
# conftamer: enabled — writing "/tmp/contexttrack-run....../raw.jsonl"
```

The diagnostic confirms the file opened; a nonzero count confirms events,
**not completeness or correct influence attribution**. `ctgo` defaults tests to
`-count=1`; `-v` exposes passing-test diagnostics. Keep `$RUN/raw.jsonl`.
See [setup](#capture-setup-and-diagnostics) for file handling and alternatives.

### Normalize an existing capture

Already have a completed raw capture? **No Go or consumer repository is needed.**
The recommended source-checkout workflow uses [uv](https://docs.astral.sh/uv/):

```bash
cd /path/to/conftamer/contexttrack &&
uv python install 3.14 &&
uv sync --locked --no-dev &&
uv run --no-dev contexttrack --help
```

Stop capture first. Keep the raw file and choose a **new output in an existing directory**:

```bash
uv run --no-dev contexttrack normalize /path/to/raw.jsonl --output /path/to/new-normalized.jsonl
```

Success is silent (exit 0); input/filesystem errors exit 2 with diagnostics,
no traceback. Empty captures succeed. OUTPUT provides a
[before/after walkthrough](OUTPUT.md#walkthrough-one-received-request),
[CLI](OUTPUT.md#cli), [Python API](OUTPUT.md#public-python-api), and
[validation/publication rules](OUTPUT.md#streaming-jsonl-io).

**`analysis/` and `conftamer-cli/node-query` require raw captures, not normalized files.**
Normalization is **not redaction**; protect both files and never commit real captures
or generated graphs. See [privacy limits](OUTPUT.md#privacy-trust-boundaries-and-human-audit).

### Capture then normalize

After Python setup:

```bash
CT=/path/to/conftamer/contexttrack
RUN=$(mktemp -d /tmp/contexttrack-run.XXXXXX) &&
(cd /path/to/target/module && CONFTAMER_EVENTS="$RUN/raw.jsonl" \
  "$CT/bin/ctgo" test -v ./path/to/http/package) &&
(cd "$CT" && uv run --no-dev contexttrack normalize "$RUN/raw.jsonl" \
  --output "$RUN/normalized.jsonl")
```

`$RUN` holds raw output for diagnostics and the current consumer, plus normalized
output for typed readers.

<details>
<summary>Alternative: install a wheel into a separate environment</summary>

### Install a wheel instead

For an existing Python 3.14+ environment, from `contexttrack/`:

```bash
DIST=$(mktemp -d /tmp/contexttrack-dist.XXXXXX) &&
uv build --wheel --out-dir "$DIST" &&
uv pip install --python /path/to/venv/bin/python "$DIST"/contexttrack-0.2.0-*.whl
```

Use that environment's interpreter; activation and PATH changes are unnecessary:

```bash
/path/to/venv/bin/python -m contexttrack --help
```

</details>

## Capture setup and diagnostics

[`setup-go.sh`](scripts/setup-go.sh) copies affected Go files, applies
[`go-inlibrary.patch`](go-inlibrary.patch) without fuzz, and adds
[`conftamer.go`](_goroot/src/net/http/conftamer.go), **never changing installed GOROOT**.
Mismatched/already-patched trees are rejected; never force a patch.
The cache is `${XDG_CACHE_HOME:-~/.cache}/conftamer/` (`CONFTAMER_CACHE_DIR` overrides
it); toolchain, patch, or source changes invalidate it. `ctgo` verifies the overlay
each invocation and exports `GOTOOLCHAIN=local` and the overlay in `GOFLAGS`
for child Go commands.

| `CONFTAMER_EVENTS` | Behavior |
| --- | --- |
| Unset with `ctgo test`/`run` | Creates a fresh file and prints its path; `CONFTAMER_EVENTS_DIR` selects the directory (default `$TMPDIR` or `/tmp`) |
| Explicit nonempty path | Appends one JSON object per event; the producer does not create parent directories |
| Empty | Disables tracing |
| Unset without `ctgo` | Disables tracing |

Use fresh paths per run. Startup reports enabled or `open failed`; passing tests
without `-v` can hide diagnostics. Write failures trigger at most one best-effort
stderr warning per process:

```text
conftamer: capture write failed for "DESTINATION": ERROR; capture may be incomplete or invalid
```

Failures may leave invalid JSONL. HTTP does not wait for warnings;
[implementation details](MAINTAINING.md#capture-write-warning-implementation)
cover delivery and failed-write handling.

<details>
<summary>Advanced alternatives: plain Go or a disposable clone</summary>

### Plain Go with an overlay

From `contexttrack/`, export the overlay and `GOTOOLCHAIN=local`:

```bash
overlay_env=$(scripts/setup-go.sh --env) &&
  eval "$overlay_env"
```

After successful setup, use the same Go executable, fresh `CONFTAMER_EVENTS`, and
`-count=1`. An exported GOROOT must name stock Go 1.26.6. Editors and gopls miss overlays;
event paths name stock files, but line numbers refer to patched sources.
Rebuilds may take minutes.

### Disposable clone fallback

For build systems that override `GOFLAGS`/`GOTOOLCHAIN`, use an absolute clone
path that does not exist. This stops on failure and checks that Go uses the clone:

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

Never patch installed Go or module caches. [Maintainer notes](MAINTAINING.md)
cover manual patching and upgrades.

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

When no edges exist, graph output is `(no edges found)`, not DOT; the guard above
skips rendering. Empty files are rejected by the raw-analysis reader.

[`message_graph.py`](analysis/message_graph.py) keeps each node key's first
occurrence per `(pid, context_id)` group. It links consecutive deduplicated nodes:
`A → B → A → C` becomes `A → B → C`. `--recv-sent` links receives to later sends
in that same sequence. Neither mode preserves occurrence-level ordering.
These views are diagnostic, **not PMGraphs or causal graphs**; see the
[raw mapping](OUTPUT.md#raw-mapping-and-strictness-differences).

## Development and verification

Run from `contexttrack/`; see the [source map](MAINTAINING.md#source-map).

### Python checks and generated schema

```bash
uv sync --locked --dev
uv run pytest -q
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
  uv run python -m py_compile src/contexttrack/*.py tests/*.py
```

</details>

### Capture checks

Capture checks also require python3 and gofmt; `-race` needs a supported platform
and C compiler:

```bash
scripts/check.sh  # --no-race without a C compiler; --clone also checks clone mode
```

The [loopback suite](tests/httpcapture/) needs no services. Full captures contain
unstamped routes that may warn downstream; the focused example excludes these
and request-ownership/cancellation cases. The focused command is a self-contained
capture check; no target application is needed.

`check.sh` stops at the first failure: static checks, overlay setup and vet,
tooling tests, Go capture tests with the race detector, **tracing-on** upstream
HTTP short tests, then raw-analysis checks (consumer fixture if available).
Outputs: `${TMPDIR:-/tmp}/contexttrack-check.*`.
Package pytest, ty/Ruff/Tombi, wheel, and consumer checks are separate.

<details>
<summary>Focused capture, tooling, and upstream checks</summary>

```bash
python3 -m unittest tests/test_tooling.py -v  # CONTEXTTRACK_TEST_CLONE=1 enables clone tests
(cd tests/httpcapture && ../../bin/ctgo test -race ./...)
(cd tests/httpcapture && ../../bin/ctgo test \
  -run 'TestRoundTripCapture|TestRedirectLabels|TestInheritedContext' ./...)
CTGO="$PWD/bin/ctgo"
(cd "$("$CTGO" env GOROOT)/src/net/http" && "$CTGO" test -short .)
```

Upstream tests need **tracing enabled** to check instrumented request identity
and cancellation.

</details>

Automated checks do not replace [human audit](OUTPUT.md#privacy-trust-boundaries-and-human-audit).

## Platform and application caveats

### Instrumented request and hook boundaries

This is not a complete wire capture:

- **Contexts:** IDs originate at HTTP boundaries and are inherited by derived contexts
  and redirects. Unstamped contexts report `context.error`. Group by
  **`(pid, context_id)` within one capture** ([identity](OUTPUT.md#context-identity)). The historical
  [root-address patch](go-inlibrary-optional.patch) is incompatible with ID-based
  analysis and supplies no cross-run identity.
- **Ownership:** Only outbound requests missing IDs get private stamped copies,
  never in-place stamps. Only the exact copy is unwrapped to preserve
  `Response.Request`, redirect `via`, and `CancelRequest`. Other RoundTrippers,
  `Transport.Proxy`, and registered protocols may receive the copy.
  Tracing-disabled behavior otherwise follows stock Go.
- **Client:** `Request sent` records attempts, including later dial failures.
  Pre-attempt rejects are not logged unless an alternate protocol can take them.
  `Client.Do` and wire hooks may both report a response. Bundled HTTP/2 records
  final client headers, including direct transport calls, not informational responses.
  External `golang.org/x/net/http2` is not covered; `Transport.NewClientConn`'s
  `ClientConn` bypasses send hooks.
- **Server:** Receipt precedes handler dispatch; earlier protocol rejects and
  mocked/bypassed HTTP paths are not covered. External routers need route hooks.
- **Attribution:** `request_id` labels outbound endpoints, not occurrences;
  `api_id` is best-effort attribution, not module identity. Sent responses have
  no API/handler association; consumers may associate them with received requests.
  Handler labels use function/underlying type names (no pointer prefixes).
  Stack-derived API IDs and debug goroutine IDs are not stable runtime interfaces.

### Target-module commands and prerequisites

<details>
<summary>Optional recipes: Prometheus, Caddy, and Kubernetes</summary>

These **illustrative, version-dependent** commands have no recorded verified
application revisions. Check your checkout's prerequisites; use a writable target
module, fresh capture paths, and `ctgo` or the clone's `bin/go`.

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
verified Prometheus release. Confirm the dependency and use a writable copy:

```bash
cp -r ~/go/pkg/mod/github.com/prometheus/common@v0.69.0 ~/common-conftamer
chmod -R u+w ~/common-conftamer
(cd ~/common-conftamer && patch -p4 < /path/to/contexttrack/prometheus-common-route.patch)
```

In Prometheus's `go.mod`: `replace github.com/prometheus/common => /path/to/common-conftamer`.
Build with ctgo/a patched clone for `http.ConftamerLogRouted`. Patterns retain
ServeMux `{name}` and httprouter `:name`/`*path`; the consumer cannot reconstruct
ambiguous/rewritten routing chains. Never patch the read-only module cache.

</details>

<details>
<summary>Application troubleshooting (check against your revision/environment)</summary>

- **Toolchain overrides:** Kubernetes `make`/`hack/` may change Go or overlay flags;
  verify them or use a clone. Scope tests correctly; root build failures do not
  establish hook failures.
- **etcd:** Run `hack/install-etcd.sh` if your checkout provides it; add
  **its reported directory** to PATH before tests.
- **Timeouts:** Kubernetes middleware can race handlers and emit orphan 504s.
  Check your revision's `RequestTimeout` (historically
  `staging/src/k8s.io/apiserver/pkg/server/config.go`); increasing it changes the
  test scenario, not normalization.
- **Stale servers:** Use `ss -tlnp` with configured test ports. Identify owners
  before stopping processes.

</details>
