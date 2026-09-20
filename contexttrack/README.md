# ContextTrack

ContextTrack instruments Go's standard-library HTTP/1 client and server and
writes strict version 3 JSON Lines observations. The Python package validates a
capture, associates each response with its exact request exchange, and displays
possible receive-to-send influence for messages sharing a known Go context.

The record, label, and influence contract is in [OUTPUT.md](OUTPUT.md).
`event.schema.json` is generated from `src/contexttrack/events.py`; it is not a
second runtime validator. ContextTrack is an observation prototype, not proof
of causality, delivery, complete coverage, or crash-durable capture.

## Compatibility and boundaries

Version 3 deliberately does not emit or read v1 or v2. Keep historical captures
with their historical tooling; `testdata/v2-chain.jsonl` is retained only as a
rejection fixture.

Supported observations are the standard-library HTTP/1 client transport and
server dispatch paths. Bundled HTTP/2 client or server use diagnoses
`non-HTTP/1 capture is unsupported` and permanently stops that process logger
without changing HTTP negotiation or results. A capture with that diagnostic is
unacceptable even when it contains a valid HTTP/1 prefix.

External `golang.org/x/net/http2`, custom transports, mocked handlers,
pre-dispatch protocol rejection, hijacked or tunneled traffic, and bodies are
not covered. API ownership, route patterns, module discovery, PMGraph
construction, Caddy integration, and Kubernetes integration are not implemented
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

Server ingress automatically creates a context root. An autonomous client must
use the value returned by `http.ConftamerContext` to participate in influence:

```go
ctx := http.ConftamerContext(context.Background())
req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
resp, err := http.DefaultClient.Do(req)
```

The helper returns the input unchanged when capture is disabled. An unstamped
client is still recorded and associated with its response, but its context is
null and forms no influence group.

## Inspect with the sole command

Pass one process file or one capture directory:

```bash
uv run contexttrack "$CAPTURE"
uv run contexttrack testdata/v3-chain.jsonl
```

The deterministic text output includes semantic nodes, possible-influence
edges, and occurrence/node/edge/unknown-context counts. There are no
subcommands, format flags, compatibility wrappers, route summaries, or DOT
mode. Use `contexttrack.capture` when occurrence-level details are needed.
