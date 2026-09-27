# Context-Message Tracking

Compile context and message tracing logic directly into a cloned copy of the
Go standard library. Any program built with this toolchain produces a `jsonl`
file that can be used with the scripts in `analysis/`.

**Goal** of this is to infer causal relationships between HTTP messages,
i.e., "receiving this request led to sending this follow-on request."
We define this control flow influence as two messages that share the same
"root" context (the definition of "root" may be a bit library-specific).

**TODO**: Not confident in a fair amount of this ("API ID" logic,
where we're assigning context IDs, and where we're logging).

**TODO**: Different approach to context ID that could allow us to correlate across tests?

## How to Use

### Modify Go

[`go-inlibrary.patch`](go-inlibrary.patch) is generated against **Go 1.26.6**
and tested on Linux/amd64. Apply it only to a disposable copy of that Go
version, never to an installed Go tree or a module cache:

```bash
# Run from contexttrack/; use a clean Go 1.26.6 distribution as the source.
PATCH="$(pwd)/go-inlibrary.patch"
cp -a /path/to/clean/go1.26.6 /path/to/go-conftamer
cd /path/to/go-conftamer
patch --dry-run -p4 < "$PATCH"
patch -p4 < "$PATCH"

unset GOROOT
export GOTOOLCHAIN=local
./bin/go version
./bin/go env GOROOT  # must name the disposable, patched tree
```

If `patch` is unavailable, run `git apply --check -p4 "$PATCH"` and then
`git apply -p4 "$PATCH"` from the copy's root instead. Failed hunks indicate a
version mismatch to investigate, not something to force. Regenerate this diff
from clean and modified Go sources when updating it; do not hand-edit hunk counts.

Contexts are correlated by a monotonic, process-local ID stamped at an HTTP
request's origin and inherited down the context chain. Group by `(pid,
context_id)`, not by ID alone. Outbound stamping uses internal request copies in
both `Client.Do` and direct `Transport.RoundTrip` calls; it does **not** stamp the
caller's request in place. Redirects and contexts derived from an incoming
request retain their inherited ID. An unstamped context logged outside these
origins reports `context.error` without inventing a per-event ID.
An earlier approach instead walked the context's parent
chain to a shared root heap address; it's no longer used (false positives
from heap-address reuse, more vulnerable to custom types) but is preserved as
[`go-inlibrary-optional.patch`](go-inlibrary-optional.patch).

### Modify Prometheus' `common` (Prometheus only)

`net/http`'s hooks only see `http.ServeMux` routing. Prometheus routes its real endpoints with `prometheus/common/route` (wrapping `julienschmidt/httprouter`).
Without this its API events get a very coarse mount point (e.g., `pattern: /api/v1/` vs. `/api/v1/query`).

Apply [`prometheus-common-route.patch`](prometheus-common-route.patch) to a writable copy of the module:

```bash
cp -r ~/go/pkg/mod/github.com/prometheus/common@v0.69.0 ~/common-conftamer
chmod -R u+w ~/common-conftamer
cd ~/common-conftamer && patch -p4 < ~/conftamer/contexttrack/prometheus-common-route.patch
```

Then point Prometheus at it, in `~/prometheus-src/go.mod`:

```
replace github.com/prometheus/common => /path/to/common-conftamer
```

The fork calls `http.ConftamerLogRouted`, exported from the patched `net/http`.

### Set Environment Variables

- **`GOTOOLCHAIN=local`** — Without it, Go's `auto` toolchain may download
  a new fork of Go.
- **`CONFTAMER_EVENTS=/path/to/output.jsonl`** — where events are written.

Note: when running `go test`, use `-count=1` as an argument to make sure that cached
tests get re-run. An empty output file may be caused by a missing `-count=1`.

### Output

The file is opened `O_APPEND`.
Delete (or point `CONFTAMER_EVENTS` at a fresh path) between runs you want to analyze in isolation.

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

# Running Tests

## Patch regression tests

The standalone module in [`tests/httpcapture/`](tests/httpcapture/) uses real
loopback HTTP servers and has no external dependencies. With the patched Go
1.26.6 binary, run:

```bash
# From contexttrack/; -race requires a supported platform and C compiler.
PATCHED_GO=/absolute/path/to/go-conftamer/bin/go
unset GOROOT
export GOTOOLCHAIN=local
export CONFTAMER_EVENTS="$(mktemp /tmp/contexttrack-regression.XXXXXX.jsonl)"
cd tests/httpcapture
"$PATCHED_GO" test -race -count=1 ./...
```

Always use a fresh capture for each invocation. These tests cover HTTP/1 and
bundled HTTP/2, direct transports, empty methods, redirect labels and context
inheritance, implicit/ignored/invalid HTTP/2 response headers, unstamped
contexts, request ownership, direct-transport cancellation, and a subprocess
with tracing disabled. The capture intentionally contains unstamped routing
events from that negative test; downstream importers may warn about them.

For a capture without the negative/ownership/cancellation cases, use another
fresh `CONFTAMER_EVENTS` path and run:

```bash
"$PATCHED_GO" test -count=1 -run 'TestRoundTripCapture|TestRedirectLabels|TestInheritedContext' ./...
```

`Client.Do` and transport hooks may both report a received response. Bundled
HTTP/2 logs final client response headers even when `Client.Do` is bypassed;
HTTP/2 informational client responses and external `golang.org/x/net/http2`
implementations are not covered by this hook. Server receipts are logged once
at `serverHandler.ServeHTTP`; protocol-level rejects that bypass application
handler dispatch are outside that receive hook. This is a message/context
producer, not a complete wire capture or a PMGraph/AppGraph builder.

## Prometheus

All tests:

```bash
cd ~/prometheus-src # or Prometheus directory
unset GOROOT   # a .bashrc-exported GOROOT silently reverts to vanilla stdlib
export GOTOOLCHAIN=local
export CONFTAMER_EVENTS=~/conftamer/contexttrack/events/prom_test.jsonl
rm -f "$CONFTAMER_EVENTS"

~/go-conftamer/bin/go test -count=1 -v ./...
```

Or, just one test, e.g.:

```bash
~/go-conftamer/bin/go test ./scrape/ -run TestTargetScraperScrapeOK -count=1 -v
```

**`-count=1`**: `go test` caches results; a cached package is not re-executed by
default. This forces each test to run once.

**`-v`**: to see output from the test.

**Confirming the clone is linked**: Check for this line:

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
unset GOROOT
export GOTOOLCHAIN=local
export CONFTAMER_EVENTS=~/conftamer/contexttrack/events/caddy_test.jsonl
rm -f "$CONFTAMER_EVENTS"
~/go-conftamer/bin/go test -count=1 -p 1 ./caddytest/integration/
```

Note: use `-p 1` to disable parallelization.
Each integration test starts a Caddy server on the same port, so we can't
actually execute parallel test processes.

Or, just one test, e.g.:

```bash
~/go-conftamer/bin/go test -count=1 -p 1 ./caddytest/integration/ \
  -run TestReverseProxySubroutes
```

Unit tests:

```bash
~/go-conftamer/bin/go test -count=1 ./modules/caddyhttp/reverseproxy/
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
unset GOROOT
export GOTOOLCHAIN=local
export CONFTAMER_EVENTS=~/conftamer/contexttrack/events/k8s_test.jsonl
rm -f "$CONFTAMER_EVENTS"

~/go-conftamer/bin/go test -count=1 ./transport/... ./rest/... ./tools/...
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
unset GOROOT
export GOTOOLCHAIN=local
export CONFTAMER_EVENTS=~/conftamer/contexttrack/events/k8s_test.jsonl
rm -f "$CONFTAMER_EVENTS"

~/go-conftamer/bin/go test -count=1 ./test/integration/endpoints/
```

Or, just one test, e.g.:

```bash
~/go-conftamer/bin/go test -count=1 ./test/integration/endpoints/ -run TestEndpointWithMultiplePods
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

- **Check `GOROOT`** — if the shell exports
  `GOROOT` (e.g. `.bashrc` lines added by version managers like `g`), the patched
  `~/go-conftamer/bin/go` compiles against that stdlib instead of its own patched
  one. Run `unset GOROOT` first, and verify with
  `~/go-conftamer/bin/go env GOROOT`.
- **`GOTOOLCHAIN=local` on every invocation** — results in empty events file
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
- **Append-only output** — `rm` the file between isolated runs.
- **First run after (re)building patched `go` is slow** — any changes to the stdlib
  require a full rebuild. `go test ./...` on a large module (e.g. all of Prometheus)
  recompiles the whole stdlib + module graph from scratch before the first test binary starts,
   which can take several minutes with no output in the meantime. Subsequent runs reuse the cache.
- **Libraries:** HTTP/1.x and bundled HTTP/2 are instrumented.
  If a test uses a mocked library, it won't work.
- **`-count 1`** - run all tests, even if cached.
