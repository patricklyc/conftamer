# ContextTrack

ContextTrack instruments Go's standard-library HTTP/1 client and server and
writes strict version 4 JSON Lines observations. The Python package validates a
capture, associates each response with its exact request exchange, and displays
only receive-to-send influence named by recorded source references.

The record, label, and influence contract is in [OUTPUT.md](OUTPUT.md).
`event.schema.json` is generated from `src/contexttrack/events.py`; it is not a
second runtime validator. ContextTrack is an observation prototype, not proof
of causality, delivery, complete coverage, or crash-durable capture.

## Compatibility and boundaries

Version 4 deliberately does not emit or read v1, v2, or v3. Keep historical
captures with their historical tooling; `testdata/v2-chain.jsonl` and
`testdata/v3-chain.jsonl` are retained only as rejection fixtures. Migration of
applications and downstream consumers is separate from this producer and reader.

Supported observations are the standard-library HTTP/1 client transport and
server dispatch paths. Bundled HTTP/2 client or server use diagnoses
`non-HTTP/1 capture is unsupported` and permanently stops that process logger
without changing HTTP negotiation or results. A capture with that diagnostic is
unacceptable even when it contains a valid HTTP/1 prefix.

External `golang.org/x/net/http2`, custom transports, mocked handlers,
pre-dispatch protocol rejection, hijacked or tunneled traffic, and bodies are
not covered. API ownership, route patterns, module discovery, AppGraph
stitching, Caddy integration, and Kubernetes integration are not implemented
here.

## Requirements and setup

- a clean Go `go1.26.6` Git checkout;
- Python 3.14 or newer;
- [`uv`](https://docs.astral.sh/uv/); and
- Pydantic 2.13.5 or newer, below version 3 (installed by `uv`).

Install the local package and development dependencies:

```bash
uv sync --dev
```

Never patch the system Go installation, a module cache, a sibling checkout, or
an existing experiment workspace. Build in a fresh tree:

```bash
REPO=$PWD
WORK=$(mktemp -d)
git clone --depth 1 --branch go1.26.6 https://go.googlesource.com/go "$WORK/go"
GO_WORK=$WORK/go

bash "$REPO/apply-go-patch.sh" "$GO_WORK"
(
  cd "$GO_WORK/src"
  env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR \
    -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local ./make.bash
)
PATCHED_GO=$GO_WORK/bin/go

env -u GOROOT GOTOOLCHAIN=local "$PATCHED_GO" version
env -u GOROOT GOTOOLCHAIN=local "$PATCHED_GO" env GOROOT
```

`apply-go-patch.sh` requires a clean `go1.26.6` Git tree, checks the native
patch before mutation, rejects repeat application, and copies the four reviewed
files from `_stdlib/net/http/`. Always invoke the resulting executable
explicitly with `GOROOT` unset and `GOTOOLCHAIN=local`.

## Capture one workload

Capture requires both variables before process startup:

```text
CONFTAMER_EVENTS_DIR=/absolute/path/to/an/existing-directory
CONFTAMER_CAPTURE_ID=nonempty-run-name
```

Both absent disables capture. Partial or invalid configuration, or any set
legacy `CONFTAMER_EVENTS`, diagnoses an error and leaves capture disabled. Each
process exclusively creates one mode-`0600` `<process_id>.jsonl` file.

Compile with capture disabled. Enable it only while executing the selected
binary, so toolchain and module-download traffic is excluded:

```bash
CAPTURE=$(mktemp -d "$WORK/capture.XXXXXX")

env -u GOROOT -u CONFTAMER_EVENTS -u CONFTAMER_EVENTS_DIR \
  -u CONFTAMER_CAPTURE_ID GOTOOLCHAIN=local "$PATCHED_GO" \
  test -c -o "$WORK/http.test" net/http

(
  cd "$GO_WORK/src/net/http"
  env -u GOROOT -u CONFTAMER_EVENTS GOTOOLCHAIN=local \
    CONFTAMER_EVENTS_DIR="$CAPTURE" CONFTAMER_CAPTURE_ID=reduction-smoke \
    "$WORK/http.test" -test.run '^TestConftamerCaptureExample$' \
    -test.count=1 -test.v \
    >"$WORK/capture.stdout" 2>"$WORK/capture.stderr"
)
```

Inspect both output streams, the process files, record kinds, and expected
workload activity. Successful initialization prints `conftamer: enabled` to
stderr. A passing test, valid JSON, or an empty capture does not establish that
capture succeeded or completed.

## Declare recorded sources

The producer exports three annotation names: `http.ConftamerSource`,
`http.ConftamerWithSources`, and `http.ConftamerSetReplySources`. A source must
be an `*http.Request` observed at server ingress or an `*http.Response` observed
at client receipt by the enabled capture.

Annotate an outgoing request with the complete set of receives that influenced
that operation:

```go
outbound, err := http.NewRequestWithContext(
    incoming.Context(), http.MethodGet, targetURL, nil,
)
if err != nil {
    http.Error(w, "bad gateway", http.StatusBadGateway)
    return
}
outbound = http.ConftamerWithSources(outbound, incoming)
downstream, err := http.DefaultClient.Do(outbound)
if err != nil {
    http.Error(w, "bad gateway", http.StatusBadGateway)
    return
}
defer downstream.Body.Close()
```

`ConftamerWithSources` returns a shallow request copy, replaces any previous
source declaration, and leaves the original request and its context unchanged.
Retries retain the declaration. Redirect-created requests require a new
explicit declaration, normally in `CheckRedirect`.

Before writing final response headers, declare the complete set of additional
receives that influenced the server reply:

```go
http.ConftamerSetReplySources(incoming, downstream)
w.WriteHeader(downstream.StatusCode)
```

`ConftamerSetReplySources` replaces the previous additional list. The incoming
server request is always included automatically. Informational headers do not
freeze the declaration, but final headers do; a later declaration fails capture
without changing the HTTP operation.

Both helpers copy, sort, and deduplicate valid sources. When capture is disabled
or already stopped they are no-ops, and `ConftamerWithSources` returns its input.
With capture enabled, an unobserved source or invalid reply target fails capture
rather than silently dropping a reference. An unannotated autonomous client is
still recorded, but its send has an empty source list. Empty means unknown or
undeclared influence, not independence.

Annotations are application assertions. Review their placement and test the
control or data dependency they represent; structural validation cannot prove
that application code actually used a declared source.

## Inspect with the sole command

Pass one v4 process file or one v4 capture directory:

```bash
uv run contexttrack "$CAPTURE"
uv run contexttrack testdata/v4-chain.jsonl
```

The unchanged v2 and v3 fixtures are expected to be rejected. The deterministic
text output includes semantic nodes, recorded-source edges with exact occurrence
witnesses, and occurrence/node/edge/`sends_without_sources` counts. There are no
subcommands, format flags, compatibility wrappers, route summaries, or DOT
mode. Use `contexttrack.capture` when occurrence-level details are needed.
