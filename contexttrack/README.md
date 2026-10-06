# Context-Message Tracking

## Purpose and boundaries

ContextTrack instruments Go's `net/http` with a stock-toolchain build overlay
or a disposable patched clone. It emits opt-in, unversioned raw JSONL; the
Python package converts that evidence into a separate normalized v1 file.
The [wire/API contract](OUTPUT.md) owns fields, validation, file safety, privacy,
and the separately authorized consumer handoff.

The [HotNets paper](../../ConfTamer_HotNets_2026.pdf), §§4–5 and Figure 3,
defines per-module PMGraphs relating inputs (parameters and received messages)
to outputs (sent messages), then separate AppGraph composition. This directory
produces message/context evidence, **not either graph**. Shared contexts suggest
possible influence, not exact per-request causality; API attribution and capture
coverage are best-effort. Parameter discovery, cross-module stitching,
distributed tracing, redaction, and consumer migration are outside this package.

## Developer orientation

```text
Capture:   Go hook → conftamerLog → raw JSONL
Normalize: completed raw capture → normalize_record → typed event → write_events
```

| Start here | Responsibility |
| --- | --- |
| `go-inlibrary.patch` | Places hooks in stock Go's HTTP client, server, and transports |
| `_goroot/src/net/http/conftamer.go` | Implements those hooks and writes raw events |
| `scripts/setup-go.sh`, `bin/ctgo` | Prepare the overlay/clone and run instrumented Go |
| `src/contexttrack/_raw.py` | Validates raw fields, including the dotted message keys |
| `src/contexttrack/normalize.py`, `src/contexttrack/models.py` | Convert one record and define normalized event types |
| `src/contexttrack/io.py`, `src/contexttrack/cli.py` | Read/write JSONL safely and expose the commands |
| `tests/httpcapture/`, `tests/test_*.py` | Exercise producer behavior, tooling, and the Python package |

Three similarly named labels have different jobs: `(pid, context_id)` groups
possible influence within one capture, `request_id` labels an outbound endpoint,
and `api_id` is best-effort API attribution. None is a distributed trace ID or a
PMGraph module ID. See the [raw-to-normalized walkthrough](OUTPUT.md#walkthrough-one-received-request).

## Capture quick start: ctgo

Requires a **stock Go 1.26.6** (`go` or `$CONFTAMER_GO`); tested on Linux/amd64.
Run [`bin/ctgo`](bin/ctgo) from the target module, wherever you would run `go`:

```bash
export PATH="/path/to/conftamer/contexttrack/bin:$PATH"  # optional
cd /path/to/target/module
ctgo test ./path/to/http/package
# ctgo: CONFTAMER_EVENTS=/tmp/contexttrack-test-....jsonl
# conftamer: enabled — writing "/tmp/contexttrack-test-....jsonl"
```

[`scripts/setup-go.sh`](scripts/setup-go.sh) copies only patched files from
`go env GOROOT`, applies [`go-inlibrary.patch`](go-inlibrary.patch) without fuzz,
and adds [`conftamer.go`](_goroot/src/net/http/conftamer.go). Go ignores the
`_goroot/` directory when building the parent module. The overlay cache is
`${XDG_CACHE_HOME:-~/.cache}/conftamer/` (`CONFTAMER_CACHE_DIR` overrides it),
keyed by Go version, GOROOT, patch, and sources; changes rebuild it. Setup rejects
other versions, already-patched GOROOTs, and inexact patches: investigate a
version mismatch rather than forcing a hunk. **The installed GOROOT is untouched.**

`ctgo` checks on every invocation that `net/http` includes `conftamer.go`, sets
`GOTOOLCHAIN=local`, and appends the overlay to `GOFLAGS` (including child Go
commands). For `test`, it adds `-count=1` unless supplied, avoiding cached empty
captures. For `test`/`run`, an unset `CONFTAMER_EVENTS` selects a fresh file in
`CONFTAMER_EVENTS_DIR` (default `$TMPDIR` or `/tmp`) and prints its path.
An explicit path is **append-only**, with one JSON object per event; an empty
value disables tracing. The library logs nothing when this variable is unset or
empty. Use fresh paths per run, ensure the parent directory exists, and inspect
the enabled diagnostic and actual events (`open failed` means capture failed).
On a write error, the producer attempts this best-effort stderr warning at most
once per process:

```text
conftamer: capture write failed for "DESTINATION": ERROR; capture may be incomplete or invalid
```

It includes only the destination and error, not event payloads. HTTP does not
wait for warning delivery; later events still attempt writes without retrying
the failed record. At most one asynchronous worker targets the `os.Stderr` file
captured at the first write error, protecting it against descriptor reuse.
Warning errors are ignored; a closed stderr pipe does not terminate HTTP through
the warning, and a full pipe may block only this worker. Process exit does not
wait for it and may discard the warning. Startup diagnostics are unchanged.
Partial writes may leave invalid JSONL. Neither a successful open nor absence of
a warning proves a complete capture.

Plain `go` can use `eval "$(scripts/setup-go.sh --env)"` from this directory.
Then set `GOTOOLCHAIN=local`, a fresh `CONFTAMER_EVENTS`, and `-count=1` yourself.
An exported GOROOT is safe for the overlay only if it names the stock 1.26.6 tree
that Go uses. Editors/gopls do not see the overlay; event `file` values name stock
GOROOT paths. Changing stdlib instrumentation forces a slow first rebuild;
large module graphs may take minutes before any test output appears.

### Disposable clone fallback

For build systems that override `GOFLAGS`/`GOTOOLCHAIN`:

```bash
scripts/setup-go.sh --clone /path/to/go-conftamer  # DEST must not exist
unset GOROOT                  # otherwise the clone may use an unpatched stdlib
export GOTOOLCHAIN=local
/path/to/go-conftamer/bin/go env GOROOT  # must name the clone
export CONFTAMER_EVENTS="$(mktemp /tmp/contexttrack-clone.XXXXXX.jsonl)"
# From the target module:
/path/to/go-conftamer/bin/go test -count=1 ./path/to/http/package
```

Manual setup is `cp -a` of clean Go 1.26.6, `patch --dry-run -p4` then
`patch -p4 < go-inlibrary.patch` (or `git apply -p4`) in the copy, plus
`conftamer.go` in its `src/net/http/`. Never patch an installed tree or module
cache. Edit the helper directly; regenerate hook-call patches from clean and
modified trees, retaining `a/usr/local/go/` and `b/home/tcr6/go-conftamer/`
prefixes for `-p4`, rather than hand-editing hunks. Run the checks below.
On Go upgrades, reconcile `conftamerWillReject` with `Transport.roundTrip`'s
validation/alternate-protocol ordering, recheck best-effort stack/goroutine debug
metadata, and run **both** the race capture suite and tracing-enabled upstream
`net/http` short tests. A tracing-off upstream run cannot check instrumented
request identity or cancellation.

## Python install and normalize quick start

ContextTrack **0.2.0** requires Python **>=3.14** and Pydantic **>=2.13.5,<3**;
package and schema versions are independent. With [uv](https://docs.astral.sh/uv/),
from `contexttrack/`:

```bash
uv python install 3.14
uv sync --locked --dev
uv run contexttrack --help
```

Or build outside the checkout and install in a separate existing Python 3.14+
environment (no Go or consumer repository required):

```bash
DIST=$(mktemp -d /tmp/contexttrack-dist.XXXXXX)
uv build --wheel --out-dir "$DIST"
uv pip install --python /path/to/venv/bin/python "$DIST"/contexttrack-0.2.0-*.whl
```

Stop capture first. Keep the raw file and use **new outputs in existing directories**:

```bash
uv run contexttrack normalize /path/to/raw.jsonl --output /path/to/new-normalized.jsonl
uv run python -m contexttrack normalize /path/to/raw.jsonl --output /path/to/new-module.jsonl
uv run contexttrack schema  # model-generated normalized v1 schema on stdout
```

Omit `uv run` in an installed environment. Success is silent (exit 0); expected
input/filesystem errors exit 2 with stderr diagnostics and no traceback.
There is no overwrite/append/skip-bad/stdout-output mode or format auto-detection.
See [streaming I/O](OUTPUT.md#streaming-jsonl-io) for rollback/publication guarantees
and [public API](OUTPUT.md#public-python-api) for in-memory raw reading.

**`analysis/` and `conftamer-cli/node-query` still require raw captures.** Do not
pass normalized files to them. For conversion rules and incomplete/repeated
observations, see [raw mapping](OUTPUT.md#raw-mapping-and-strictness-differences).
Even an empty capture normalizes successfully; success is not a completeness
check. Protect both files as described in
[privacy and audit limits](OUTPUT.md#privacy-trust-boundaries-and-human-audit).

## Tests and generated schema

From `contexttrack/` in the installed development environment:

```bash
uv sync --locked --dev
uv run pytest -q
uv run pytest -q tests/test_cli.py tests/test_api.py
```

The CLI tests compare public schema output with `EVENT_ADAPTER.json_schema()`;
there is **no checked-in snapshot**. Export to a fresh scratch destination with
[these generation instructions](OUTPUT.md#generated-json-schema). Independent
model/normalization tests own literal field expectations, not schema introspection.

Python completion checks are separate from capture checks; scope excludes the
unchanged standard-library tooling tests from ty/Ruff:

```bash
PYTHON_SCOPE=(
  src/contexttrack tests/conftest.py tests/test_validation.py tests/test_models.py
  tests/test_normalize.py tests/test_readers.py tests/test_writers.py
  tests/test_roundtrip.py tests/test_api.py tests/test_cli.py
)
uvx ty check "${PYTHON_SCOPE[@]}"
uvx ruff check "${PYTHON_SCOPE[@]}"
uvx ruff format --check "${PYTHON_SCOPE[@]}"
uvx tombi lint pyproject.toml
uvx tombi format --check pyproject.toml  # never run Tombi on generated uv.lock
PYTHONPYCACHEPREFIX="$(mktemp -d /tmp/contexttrack-pycache.XXXXXX)" \
  uv run python -m py_compile src/contexttrack/*.py tests/*.py
```

With stock Go 1.26.6, Bash, python3, and gofmt:

```bash
scripts/check.sh  # --no-race without a C compiler; --clone also checks clone mode
python3 -m unittest tests/test_tooling.py -v  # CONTEXTTRACK_TEST_CLONE=1 enables clone tests
(cd tests/httpcapture && ../../bin/ctgo test -race ./...)
(cd tests/httpcapture && ../../bin/ctgo test \
  -run 'TestRoundTripCapture|TestRedirectLabels|TestInheritedContext' ./...)
```

The [loopback suite](tests/httpcapture/) has no external dependencies; `-race`
needs a supported platform/C compiler. It covers HTTP/1 and bundled HTTP/2,
client/direct transport, headers and labels, routing/redirect contexts,
body rewind/reuse, ownership/identity/cancellation, metadata, and tracing off.
It also checks concurrent capture-write failures and full/closed stderr pipes
after initialization (Linux `/dev/full`), healthy/disabled/open-failure controls,
header rejection without dialing, registered protocols, proven cached HTTP/2
attempts, and pointer/named/generic attribution.
The full capture intentionally includes negative unstamped routing evidence
that downstream importers may warn about; the filtered command avoids those
negative, ownership, and cancellation cases.

`scripts/check.sh` stops at the first failure: shell syntax (shellcheck if
available), gofmt, Python syntax and diff checks; overlay setup and `go vet`;
tooling tests; race regression; **tracing-on** upstream `net/http` short tests;
then unchanged raw-analysis smoke on the fresh capture and the committed
`node-query` scrape fixture if available. Outputs stay in a new
`/tmp/contexttrack-check.*` directory. It does not run package pytest, ty/Ruff/
Tombi, wheel checks, or consumer compatibility tests. To run upstream alone:

```bash
(cd "$(go env GOROOT)/src/net/http" && /path/to/contexttrack/bin/ctgo test -short .)
```

Automated success is not complete-project human audit/sign-off; see OUTPUT.

## Platform and application caveats

### Instrumented request and hook boundaries

IDs are stamped at HTTP origins and inherited by redirects and derived contexts;
[context identity](OUTPUT.md#context-identity) specifies their grouping rules.
Unstamped contexts logged elsewhere report `context.error`, not a fabricated
per-event ID. The historical [`go-inlibrary-optional.patch`](go-inlibrary-optional.patch)
walks heap roots; it is not the current format, risks address reuse/custom-type
errors, and cannot be combined with ID-based analysis or cross-run correlation.

The caller's request is never stamped in place. Only an outbound request missing
an ID gets a private stamped copy; inherited-ID requests are not copied again.
Only that exact private copy is unwrapped, **not Go's or an application's copies**.
`Response.Request`, `CheckRedirect`'s `via`, and `Transport.CancelRequest` preserve
the caller's pointer. Other RoundTrippers, `Transport.Proxy`, or protocols
registered with `Transport.RegisterProtocol` may still receive the private copy.
Tracing-disabled behavior otherwise follows stock Go.

`Request sent` records a transport **attempt**, including later dial failure.
Pre-attempt rejects (unsupported scheme, invalid method/header, missing host)
are not logged unless an alternate protocol, e.g. cached HTTP/2, may take them.
`Client.Do` and wire hooks can both report a response: do not assume one event
per exchange. Bundled HTTP/2 logs final client headers even for direct transport
calls; informational HTTP/2 client responses and external `golang.org/x/net/http2`
are not covered. `Transport.NewClientConn`'s `ClientConn` bypasses `Request sent`.
Server receipt is logged at `serverHandler.ServeHTTP` before arbitrary handler
dispatch. Protocol rejects before dispatch and mocked/bypassed HTTP paths are
outside those hooks. External routers need their own route-pattern instrumentation
(the Prometheus patch below supplies it).
This is not a complete wire capture. `Response sent` may need downstream
attribution to its received request, not a fabricated API/handler association.
Handler metadata uses the function name for a `HandlerFunc` and the underlying
type for a typed handler (without pointer prefixes). Stack-derived API association
and goroutine IDs parsed from runtime debug text are best-effort metadata, not
stable runtime interfaces or authoritative module ownership.

### Target-module commands and prerequisites

Use `ctgo` on PATH, a fresh capture per invocation, and run in the named writable
application checkout/module. With a clone, use its `bin/go` and the environment
above. These are application examples, not self-contained package tests:

| Target directory | Command | Prerequisites / limits |
| --- | --- | --- |
| Prometheus root | `ctgo test -v ./...` or `ctgo test ./scrape/ -run TestTargetScraperScrapeOK -v` | Precise routing needs the patch below; stdlib/module rebuild can be slow |
| Caddy root | `ctgo test -p 1 ./caddytest/integration/` (optionally `-run TestReverseProxySubroutes`) | `-p 1` prevents fixed-port collisions; external HTTP/2/internal `caddyhttp.(*Server).ServeHTTP` paths can miss hooks |
| Caddy root | `ctgo test ./modules/caddyhttp/reverseproxy/` | Unit tests may mock HTTP and emit no evidence |
| Kubernetes `staging/src/k8s.io/client-go` | `ctgo test ./transport/... ./rest/... ./tools/...` | No etcd; this covers selected staging packages, not all modules |
| Kubernetes root | `ctgo test ./test/integration/endpoints/` (optionally `-run TestEndpointWithMultiplePods`) | etcd on PATH; other integration packages have their own prerequisites |

Prometheus uses `common/route` around `julienschmidt/httprouter`, not just
ServeMux. Without its hook, `/api/v1/query` may appear as coarse `/api/v1/`.
Apply [`prometheus-common-route.patch`](prometheus-common-route.patch) to a
**writable copy of common v0.69.0**, never the read-only module cache:

```bash
cp -r ~/go/pkg/mod/github.com/prometheus/common@v0.69.0 ~/common-conftamer
chmod -R u+w ~/common-conftamer
(cd ~/common-conftamer && patch -p4 < /path/to/contexttrack/prometheus-common-route.patch)
```

In Prometheus's `go.mod`: `replace github.com/prometheus/common => /path/to/common-conftamer`.
This calls patched `http.ConftamerLogRouted`, so build with ctgo/a patched clone.
Patterns preserve ServeMux `{name}` and httprouter `:name`/`*path` syntaxes; the
consumer does not reconstruct ambiguous/rewritten routing chains.

Kubernetes `make`/`hack/` may fetch/switch Go or override overlay flags; verify the
actual toolchain or use a clone. Naive root `go test ./...` build failures are not
evidence of hook failures: scope to the modules/packages above. If etcd is absent,
run `hack/install-etcd.sh` from Kubernetes root and add
`$HOME/kubernetes/third_party/etcd` to PATH; `framework.EtcdMain` uses
`exec.LookPath("etcd")` before tests. Apiserver's
`WithTimeoutForNonLongRunningRequests` can emit orphan 504 responses while racing
a handler deadline; investigate `RequestTimeout` in
`staging/src/k8s.io/apiserver/pkg/server/config.go` (increasing it changes the test
scenario, not normalization). Check for stale servers with `ss -tlnp | grep 9090`
(Prometheus) or port `2999` (Caddy); stop the identified stale process safely.

## Inspect raw output

From `contexttrack/`, pass an explicit completed **raw** path (script defaults differ):

```bash
EV=/path/to/fresh-raw.jsonl
python3 analysis/group_by_context.py "$EV"
python3 analysis/message_graph.py "$EV" --format text
python3 analysis/message_graph.py "$EV" --recv-sent --format dot > /path/to/new-messages.dot
# Optional, requires Graphviz and another fresh destination:
dot -Tsvg /path/to/new-messages.dot -o /path/to/new-messages.svg
```

[`message_graph.py`](analysis/message_graph.py) normally links consecutive distinct
messages in each context group; `--recv-sent` links receives to later sends.
These co-occurrence views are diagnostic, not canonical PMGraphs or
occurrence-accurate/cross-process causal graphs. See OUTPUT for the five raw
kinds, dotted fields, and why normalized files are not input to these scripts.
