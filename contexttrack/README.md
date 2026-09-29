# Context-Message Tracking

Compile context and message tracing logic directly into the Go standard
library's `net/http`. This uses a build overlay on a stock toolchain
([`bin/ctgo`](bin/ctgo)) or a patched clone. Any program built this way
produces a raw `jsonl` file for the scripts in `analysis/` or the Python
normalizer below. Normalization writes a separate, versioned file; it does not
change the Go capture format.

**Goal** of this is to infer causal relationships between HTTP messages,
i.e., "receiving this request led to sending this follow-on request."
We define this control flow influence as two messages that share the same
"root" context (the definition of "root" may be a bit library-specific).

**TODO**: Not confident in a fair amount of this ("API ID" logic,
where we're assigning context IDs, and where we're logging).

**TODO**: Different approach to context ID that could allow us to correlate across tests?

## How to Use

### Use the instrumented Go

The instrumentation targets **Go 1.26.6** and is tested on Linux/amd64. It has
two parts, neither of which is applied to your Go installation:

- [`_goroot/src/net/http/conftamer.go`](_goroot/src/net/http/conftamer.go): a
  new `net/http` file with the hook implementations and JSONL serializer.
  (Go ignores `_`-prefixed directories, so the `conftamer` module does not
  build it.)
- [`go-inlibrary.patch`](go-inlibrary.patch): the hook calls added to stock
  `net/http` files.

Run [`bin/ctgo`](bin/ctgo) wherever you would run `go`, from the target module:

```bash
export PATH="$HOME/conftamer/contexttrack/bin:$PATH"   # optional
cd /path/to/target/module
ctgo test ./path/to/http/package
# ctgo: CONFTAMER_EVENTS=/tmp/contexttrack-test-20260101-120000.AbC123.jsonl
# conftamer: enabled — writing "/tmp/contexttrack-test-...jsonl"
```

`ctgo` runs `$CONFTAMER_GO` (default `go`), which must be a **stock** Go 1.26.6:

- [`scripts/setup-go.sh`](scripts/setup-go.sh) copies only the files the patch
  touches out of `go env GOROOT`, applies the patch to the copies with no fuzz,
  adds `conftamer.go`, and writes a `go build -overlay` file mapping the stock
  paths to those copies. The result is cached in
  `${XDG_CACHE_HOME:-~/.cache}/conftamer/` (override with
  `CONFTAMER_CACHE_DIR`), keyed by the Go version, GOROOT, patch, and sources,
  and is rebuilt when any of them changes. The script refuses other Go
  versions, a GOROOT that already contains `conftamer.go`, and a patch that
  does not apply exactly (a version mismatch to investigate, not force).
- Every run checks that `net/http` actually includes `conftamer.go`. An
  overlay that does not match the GOROOT in use would otherwise silently
  build stock `net/http`.
- It exports `GOTOOLCHAIN=local` and appends `-overlay=...` to `GOFLAGS`, so
  child `go` commands started by tests are instrumented too.
- For `test` and `run`, if `CONFTAMER_EVENTS` is unset, it writes events to a
  new file in `$CONFTAMER_EVENTS_DIR` (default `$TMPDIR` or `/tmp`) and prints
  its path. Set `CONFTAMER_EVENTS=/path.jsonl` to choose a file (events are
  appended), or `CONFTAMER_EVENTS=` (empty) to disable tracing.
- For `test`, it adds `-count=1` unless you give a `-count` flag, so cached
  results cannot produce an empty capture.

The stock GOROOT is never modified; `GOROOT` may be exported as long as it
names the Go 1.26.6 tree that the `go` command uses. To use plain `go` with the
overlay in the current shell, run `eval "$(scripts/setup-go.sh --env)"`.
Editors and gopls do not see the overlay. Event `file` fields name the stock
GOROOT paths.

**Fallback: a patched clone.** Some build systems override `GOFLAGS` or
`GOTOOLCHAIN`. For those, create a patched copy of the whole toolchain:

```bash
scripts/setup-go.sh --clone /path/to/go-conftamer   # DEST must not exist
unset GOROOT             # otherwise the clone compiles an exported GOROOT's stdlib
export GOTOOLCHAIN=local
/path/to/go-conftamer/bin/go env GOROOT   # must name the clone
```

Manually, this is: `cp -a` a clean Go 1.26.6, run `patch --dry-run -p4` and
then `patch -p4 < go-inlibrary.patch` (or `git apply -p4`) from the copy's
root, and copy `_goroot/src/net/http/conftamer.go` into its `src/net/http/`.
Never patch an installed Go tree or a module cache in place.

**Changing the instrumentation.** Edit `conftamer.go` directly. For the hook
calls, regenerate `go-inlibrary.patch` from clean and modified Go 1.26.6
trees. Keep the `a/usr/local/go/` and `b/home/tcr6/go-conftamer/` prefixes
that `-p4` strips, and do not hand-edit hunks. Then run `scripts/check.sh`.

Contexts are correlated by a monotonic, process-local ID stamped at an HTTP
request's origin and inherited down the context chain. Group by `(pid,
context_id)`, not by ID alone. The caller's request is never stamped in place:
when an outbound request's context has no ID, `Client.Do` or a direct
`Transport.RoundTrip` sends a private copy with a stamped context. Go still
exposes and cancels the caller's request: `Response.Request`, `CheckRedirect`'s
`via`, and `Transport.CancelRequest` use the original pointer. A `RoundTripper`
other than `Transport`, a `Transport.Proxy` function, or a protocol registered
with `Transport.RegisterProtocol` may still receive the copy. Redirects and
contexts derived from an incoming request retain their inherited ID. An
unstamped context logged outside these origins reports `context.error` without
inventing a per-event ID.
An earlier approach instead walked the context's parent
chain to a shared root heap address; it's no longer used (false positives
from heap-address reuse, more vulnerable to custom types) but is preserved as
[`go-inlibrary-optional.patch`](go-inlibrary-optional.patch).

### Modify Prometheus' `common` (Prometheus only)

`net/http`'s hooks only see `http.ServeMux` routing. Prometheus routes its real endpoints with `prometheus/common/route` (wrapping `julienschmidt/httprouter`).
Without this its API events get a very coarse mount point (e.g., `pattern: /api/v1/` vs. `/api/v1/query`).

Apply [`prometheus-common-route.patch`](prometheus-common-route.patch) to a writable copy of the module:

```bash
cp -r ~/go/pkg/mod/github.com/prometheus/common@v0.69.0 ~/common-conftamer  # a writable copy
chmod -R u+w ~/common-conftamer
cd ~/common-conftamer && patch -p4 < ~/conftamer/contexttrack/prometheus-common-route.patch
```

Then point Prometheus at it, in `~/prometheus-src/go.mod`:

```
replace github.com/prometheus/common => /path/to/common-conftamer
```

The fork calls `http.ConftamerLogRouted`, exported from the patched `net/http`,
so build Prometheus with `ctgo` (or a patched clone).

### Environment variables

`ctgo` sets these for you. With plain `go` and the overlay, or with a clone,
set them yourself:

- **`GOTOOLCHAIN=local`**: without it, Go's `auto` toolchain may download
  and switch to a different Go, which is not instrumented.
- **`CONFTAMER_EVENTS=/path/to/output.jsonl`**: where events are written.
  Tracing is off when this is unset or empty.

With plain `go test`, pass `-count=1` so that cached tests are re-run. A missing
`-count=1` can cause an empty output file.

### Raw output

The file is opened `O_APPEND`.
Use a fresh path (the `ctgo` default) for each run you want to analyze in isolation.

Each line looks something like this:

```json
{"kind":"Request sent","goroutine_id":435,"file":".../net/http/transport.go","line":599,
 "message":{"req.Method":"GET","req.URL.Host":"127.0.0.1:9090","req.URL.Path":"/metrics","req.URL.RawQuery":""},
 "context":{"source":"req.Context()","type":"context.Context","context_id":"id:7"},
 "request_id":{"method":"GET","host":"127.0.0.1:9090","path":"/metrics"},"api_id":"github.com/prometheus"}
```

Kinds are `Request sent`, `Request received`, `Response sent`, `Response received`, plus
`Request routed` (used to find the most specific `pattern` that corresponds to a
request). Patterns therefore appear in both routers' syntaxes: `{name}` from ServeMux,
`:name` and `*path` from httprouter.

Use `analysis/group_by_context.py` to see context groups and `analysis/message_graph.py`
to generate the full, directed grah.

### Install the Python normalizer

The Python package is **ContextTrack 0.2.0**, requiring Python >=3.14 and
Pydantic >=2.13.5,<3. Its normalized event format is independently versioned as
**schema version 1**. See [OUTPUT.md](OUTPUT.md) for the authoritative wire/API
contract, field mapping, validation, and file-safety rules.

With [uv](https://docs.astral.sh/uv/), from `contexttrack/`:

```bash
uv python install 3.14
uv sync --locked --dev
uv run contexttrack --help
```

To install in a separate, existing Python 3.14+ environment, build a wheel
outside the checkout and install it there:

```bash
DIST=$(mktemp -d /tmp/contexttrack-dist.XXXXXX)
uv build --wheel --out-dir "$DIST"
uv pip install --python /path/to/venv/bin/python "$DIST"/contexttrack-0.2.0-*.whl
```

That environment provides `contexttrack`, `python -m contexttrack`, and the
public typed Python API, without needing Go or the consumer repository.
Downstream consumers should pin a tested artifact/version in their lockfile;
registry publication and consumer migration are separate work.

### Normalize a completed raw capture

Stop the captured program or tests before reading the raw file. Normalize it
into a **new output file in an existing directory**, keeping the raw capture
for auditing:

```bash
uv run contexttrack normalize /path/to/raw.jsonl --output /path/to/new-normalized.jsonl
# Equivalent module entry point, using a different new output:
uv run python -m contexttrack normalize /path/to/raw.jsonl --output /path/to/new-module.jsonl
uv run contexttrack schema     # generated normalized v1 JSON Schema on stdout
```

In an installed environment, omit `uv run`. Paths are explicit: there is no
append, overwrite, skip-bad, or stdout-output mode. Successful normalization is
silent and exits 0. Expected input/filesystem errors exit 2 with a stderr
diagnostic and no traceback; invalid input cannot publish a partial output.

Consumers can normalize a raw capture **in memory**, or read a normalized file:

```python
from contexttrack.io import iter_events, iter_raw_events
from contexttrack.models import RequestSent

for record in iter_raw_events("raw.jsonl"):  # raw input, no intermediate file
    event = record.event
    print(record.location, event.kind, event.context_key)
    if isinstance(event, RequestSent):
        print(event.message.method, event.message.host, event.message.path)

for record in iter_events("normalized.jsonl"):  # normalized v1 input only
    print(record.location, record.event.kind, record.event.context_key)
```

Both readers yield the same immutable event models in capture order, preserving
incomplete observations and repeated hooks; locations refer to the file actually
read. They do not auto-detect formats. Normalization does not associate requests,
reconstruct routes, or build a PMGraph/AppGraph; those remain downstream steps.
It is not redaction or proof of capture completeness. An empty capture produces
an empty normalized file, not evidence of useful instrumented traffic.

**Existing `analysis/` commands and the `conftamer-cli/node-query` importer still
require raw captures. Do not pass normalized files to them.** Their raw examples
below remain unchanged; migrating those consumers is not part of this release.

# Running Tests

## All checks

From `contexttrack/`, with a stock Go 1.26.6 as `go` or `$CONFTAMER_GO`:

```bash
scripts/check.sh              # add --no-race without a C compiler; --clone to test clone mode
```

This runs these steps and stops at the first failure:

1. Static checks: `bash -n` (and `shellcheck` if installed), `gofmt`,
   `py_compile`, and `git diff --check`.
2. Overlay setup and `go vet net/http` with the overlay.
3. The tooling tests ([`tests/test_tooling.py`](tests/test_tooling.py)).
4. The patch regression suite with `-race`.
5. The upstream `net/http` short tests with tracing on.
6. Both analysis scripts on the new capture and, if `../../conftamer-cli` has
   a `node-query` branch, on its `scrape-ok.jsonl` fixture.

Captures and outputs are kept in a new `/tmp/contexttrack-check.*` directory.
The steps are described below for running them individually.

## Tooling tests

```bash
python3 -m unittest tests/test_tooling.py -v   # CONTEXTTRACK_TEST_CLONE=1 to include clone mode
```

These cover:

- building the overlay once, reusing it, and leaving GOROOT untouched;
- rejecting other Go versions, patched GOROOTs, and patches that do not apply;
- `ctgo`'s `-count=1`, capture-file, and `GOFLAGS` handling;
- an end-to-end `ctgo run` that emits events.

## Patch regression tests

The standalone module in [`tests/httpcapture/`](tests/httpcapture/) uses real
loopback HTTP servers and has no external dependencies:

```bash
# From contexttrack/; -race requires a supported platform and C compiler.
cd tests/httpcapture
../../bin/ctgo test -race ./...   # or: CONFTAMER_EVENTS="$(mktemp ...)" /path/to/go-conftamer/bin/go test -race -count=1 ./...
```

Always use a fresh capture for each invocation. These tests cover HTTP/1 and
bundled HTTP/2, direct transports, empty methods, redirect labels and context
inheritance, implicit/ignored/invalid HTTP/2 response headers, unstamped
contexts, request ownership, body-bearing requests, `Client.Timeout` redirects,
caller-visible request identity (`Response.Request`, `CheckRedirect`'s `via`,
cached HTTP/2 connections), `Transport.CancelRequest` and `Request.Cancel`,
requests rejected before sending, and a subprocess with tracing disabled. The
capture intentionally contains unstamped routing events from that negative
test; downstream importers may warn about them.

When changing the patch, also run the upstream `net/http` tests with tracing
on; they check request identity and cancellation that the hooks could change:

```bash
cd "$(go env GOROOT)/src/net/http"   # or the clone's src/net/http
/path/to/contexttrack/bin/ctgo test -short .
```

For a capture without the negative, ownership, and cancellation cases, run
this from `tests/httpcapture` (it gets a fresh capture):

```bash
../../bin/ctgo test -run 'TestRoundTripCapture|TestRedirectLabels|TestInheritedContext' ./...
```

`Request sent` records a send attempt at `Transport.RoundTrip`, even if dialing
fails later. Requests that `Transport` rejects before any attempt (for example,
an unsupported scheme, invalid method or header, or missing host) are not
logged, unless an alternate protocol such as a cached HTTP/2 connection may take
them.

`Client.Do` and transport hooks may both report a received response. Bundled
HTTP/2 logs final client response headers even when `Client.Do` is bypassed;
HTTP/2 informational client responses and external `golang.org/x/net/http2`
implementations are not covered by this hook. Server receipts are logged once
at `serverHandler.ServeHTTP`; protocol-level rejects that bypass application
handler dispatch are outside that receive hook. This is a message/context
producer, not a complete wire capture or a PMGraph/AppGraph builder.

## Prometheus

These examples assume `ctgo` is on `PATH` (see *Use the instrumented Go*). With
a patched clone, use its `bin/go` with `-count=1`, `GOROOT` unset,
`GOTOOLCHAIN=local`, and `CONFTAMER_EVENTS` set. Setting `CONFTAMER_EVENTS` is
optional with `ctgo`, which otherwise creates a fresh file per invocation.

All tests:

```bash
cd ~/prometheus-src # or Prometheus directory
export CONFTAMER_EVENTS=~/conftamer/contexttrack/events/prom_test.jsonl
rm -f "$CONFTAMER_EVENTS"

ctgo test -v ./...
```

Or, just one test, e.g.:

```bash
ctgo test ./scrape/ -run TestTargetScraperScrapeOK -v
```

**`-count=1`** (added by `ctgo`): `go test` caches results; a cached package is
not re-executed by default. This forces each test to run once.

**`-v`**: to see output from the test.

**Confirming instrumentation is active**: `ctgo` fails if the overlay is not in
effect. The test output also contains this line:

```
conftamer: enabled — writing "/.../prom_test.jsonl"
```

(or `conftamer: ... open failed: ...` if the output directory is missing).

## Caddy

**TODO**. Caddy seems to configure HTTP/2 via external `golang.org/x/net/http2`
module cache ad sends requests through its internal `caddyhttp.(*Server).ServeHTTP`.
So, the current hooks aren't firing.

All integration tests:

```bash
cd ~/caddy
export CONFTAMER_EVENTS=~/conftamer/contexttrack/events/caddy_test.jsonl
rm -f "$CONFTAMER_EVENTS"
ctgo test -p 1 ./caddytest/integration/
```

Note: use `-p 1` to disable parallelization.
Each integration test starts a Caddy server on the same port, so we can't
actually execute parallel test processes.

Or, just one test, e.g.:

```bash
ctgo test -p 1 ./caddytest/integration/ \
  -run TestReverseProxySubroutes
```

Unit tests:

```bash
ctgo test ./modules/caddyhttp/reverseproxy/
```

## Kubernetes

**TODO**. I think that `make`/`hack1/*` builds and fetches a new(?) Go.
I'm not totally clear if this messes us up.

**TODO**. Running `go tests ./...` from the k8s root results in a lot of build erros.
I'm not totally clear why. I think that it's unrelated to us.

I've gotten events from the following subdirectories; I'm sure I'm missing some other
modules that we should run on.

### Staging modules (no etcd)

```bash
cd ~/kubernetes/staging/src/k8s.io/client-go
export CONFTAMER_EVENTS=~/conftamer/contexttrack/events/k8s_test.jsonl
rm -f "$CONFTAMER_EVENTS"

ctgo test ./transport/... ./rest/... ./tools/...
```

### B. Integration packages (need etcd)

Note: `etcd` must be on `PATH` — the integration test framework calls
`exec.LookPath("etcd")` and fails hard otherwise. If missing:

```bash
cd ~/kubernetes && hack/install-etcd.sh
export PATH="$PATH:$HOME/kubernetes/third_party/etcd"
```

All tests in the endpoints integration package:

```bash
cd ~/kubernetes
export CONFTAMER_EVENTS=~/conftamer/contexttrack/events/k8s_test.jsonl
rm -f "$CONFTAMER_EVENTS"

ctgo test ./test/integration/endpoints/
```

Or, just one test, e.g.:

```bash
ctgo test ./test/integration/endpoints/ -run TestEndpointWithMultiplePods
```

`./test/integration/endpoints` (apiserver) is the primary package.

**TODO**: other packages under `./test/integration/` might have other prereqs.

# Analyzing Output

Run scripts from `analysis/`

```bash
cd /home/tcr6/conftamer
EV=contexttrack/events/caddy_test.jsonl   # or k8s_test.jsonl, prom.jsonl, ...

# Text summary
python3 contexttrack/analysis/group_by_context.py "$EV"

# Graph
python3 contexttrack/analysis/message_graph.py "$EV" --format dot | dot -Tsvg > graph.svg
```

See [`message_graph`](analysis/message_graph.py) for node/edge details.

## Notes & gotchas

- **Check `GOROOT` (patched clone only)** — if the shell exports
  `GOROOT` (e.g. `.bashrc` lines added by version managers like `g`), a patched
  clone's `bin/go` compiles against that stdlib instead of its own patched
  one. Run `unset GOROOT` first, and verify with
  `/path/to/go-conftamer/bin/go env GOROOT`. `ctgo` instead overlays whichever
  Go 1.26.6 GOROOT is in use and checks that the overlay took effect.
- **`GOTOOLCHAIN=local` on every invocation** — without it, the events file can
  be empty (`ctgo` sets it).
- **Build systems that override `GOFLAGS` or `GOTOOLCHAIN`** (possibly
  Kubernetes' `make`/`hack/` scripts) drop `ctgo`'s overlay; use a patched
  clone for them.
- **Stale server** (Prometheus, Caddy) — Check with `ss -tlnp | grep 9090` (Prometheus)
  or `2999` (Caddy) and kill the stale PID.
- **`-p 1` for Caddy integration tests** — to avoid port collisions
- **`etcd` on `PATH`** (Kubernetes) — `framework.EtcdMain` calls
  `exec.LookPath("etcd")` before any test runs.
- **k8s `./...` "failures" are build failures, not test failures** — expected under
  naive `go test ./...` and unrelated to instrumentation; scope to staging modules
  or integration packages instead (see *Running Kubernetes tests*).
- **504 orphan events** (Kubernetes) — the
  `WithTimeoutForNonLongRunningRequests` filter in apiserver races the request
  handler against a wall-clock deadline. If you see a `resp sent ... 504` with no matching `req received`, up `RequestTimeout` in
  `staging/src/k8s.io/apiserver/pkg/server/config.go`.
- **Append-only output** — use a fresh file (the `ctgo` default) or `rm` it
  between isolated runs.
- **First run after changing the instrumentation is slow** — any changes to the stdlib
  require a full rebuild. `go test ./...` on a large module (e.g. all of Prometheus)
  recompiles the whole stdlib + module graph from scratch before the first test binary starts,
   which can take several minutes with no output in the meantime. Subsequent runs reuse the cache.
- **Libraries:** HTTP/1.x and bundled HTTP/2 are instrumented.
  If a test uses a mocked library, it won't work.
- **`-count=1`** - run all tests, even if cached (added by `ctgo test`).
