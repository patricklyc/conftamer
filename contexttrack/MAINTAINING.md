# Maintaining ContextTrack instrumentation

[README.md](README.md) owns installation, capture commands, tests, and application
caveats. [OUTPUT.md](OUTPUT.md) owns the raw and normalized event contracts. These
notes cover instrumentation maintenance and capture-warning implementation details,
not additional capture guarantees.

## Source map

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

Go ignores `_goroot/` when building the parent module; the overlay adds its helper
to the stock `net/http` package instead.

## Changing instrumentation

Edit `_goroot/src/net/http/conftamer.go` directly. When changing hook calls,
regenerate `go-inlibrary.patch` from clean and modified Go **1.26.10** trees rather
than hand-editing hunks. Keep `conftamer.go` out of the patch and retain the
`a/usr/local/go/` and `b/home/tcr6/go-conftamer/` prefixes that `-p4` strips.
`setup-go.sh` rebuilds its overlay cache when the patch or helper changes.

Prefer `scripts/setup-go.sh --clone DEST` for a disposable patched toolchain.
The manual equivalent is a `cp -a` of a clean Go 1.26.10 tree into a new destination,
then, in that copy, `patch --dry-run -p4 < /path/to/go-inlibrary.patch` followed by
`patch -p4 < /path/to/go-inlibrary.patch` (or `git apply -p4`). Finally copy
`_goroot/src/net/http/conftamer.go` into the clone's `src/net/http/`. Never patch an
installed tree or module cache; never force a mismatched hunk.

Run the [capture checks](README.md#capture-checks), inspect a fresh raw capture,
and trace changed fields through the helper, Python validators/normalizer/models,
OUTPUT, analysis scripts, and the committed `conftamer-cli/node-query` importer.
That consumer branch accepts raw captures, not normalized files. Raw-format
changes require coordinated validation; do not silently change consumer labels.

## Go upgrades

The current tooling deliberately rejects versions other than Go 1.26.10. An upgrade
requires a regenerated patch and an updated toolchain requirement, not forcing
an old patch onto a new tree.

Reconcile `conftamerWillReject` with `Transport.roundTrip`'s validation and
alternate-protocol ordering. Recheck best-effort stack/goroutine debug metadata,
and run **both** the race capture suite and tracing-enabled upstream `net/http`
short tests. A tracing-off upstream run cannot check instrumented request identity
or cancellation. Preserve upstream changes when rebasing, especially HTTP/2 body
ownership/flow control/header validation and HTTP/1 CONNECT handling.

Go 1.26.10 consumes upstream's [eMudhra distrust fix](https://go.dev/blog/emudhra-incident)
([CVE-2026-111943](https://go.dev/issue/82345)); do not maintain a separate
ContextTrack blocklist. Alongside `scripts/check.sh --clone`, run:

```bash
CTGO="$PWD/bin/ctgo"
(cd tests/httpcapture && "$CTGO" test -v -run '^TestEMudhraDistrust$' .)
CONFTAMER_EVENTS= "$CTGO" test -short crypto/x509
# Also verify a fresh clone explicitly (new destination in an existing directory):
CLONE_GO=$(scripts/setup-go.sh --clone /path/to/new-go-conftamer) &&
(cd tests/httpcapture && env -u GOROOT -u GOFLAGS GOTOOLCHAIN=local \
  CONFTAMER_EVENTS= "$CLONE_GO" test -count=1 -v -run '^TestEMudhraDistrust$' .)
```

The offline [regression fixture](tests/httpcapture/testdata/emudhra/README.md)
uses the fixed time `2026-10-21T12:00:00Z`, explicit roots, the affected SPKI, and a
typed `NoValidChains` failure, plus an unaffected-anchor control. It tests upstream
verification, not the host root store or current network availability. Its
vulnerable-version control must fail because the chain is accepted, not because
of expiry, DNS, or missing trust. Keep stock old-version control runs limited to
this self-contained test, with tracing disabled.

Installing a compiler, changing the module's language floor (which remains
`go 1.26.0`), or preparing an overlay/clone is **not a deployment**. With each
application owner, inventory artifacts/images, build location, owner, and the
replacement/restart mechanism. Rebuild approved targets with stock 1.26.10 or the
validated overlay/new clone; check build scripts do not select an old compiler
or discard instrumentation. Record `go version -m /path/to/new/binary`, then
verify the running executable/image matches the delivered artifact after
replacement/restart and run the target's health/TLS checks. Endpoints using the
distrusted intermediates need certificate replacement, never a TLS-verification
bypass. Repository validation and owner handoff complete this migration; only
the deployment owner can close the rollout after those separate checks. Returning
to 1.26.6 reintroduces exposure and requires an explicit security decision.

## Capture-write warning implementation

On a capture-write error, the producer schedules at most one asynchronous warning
worker per process. HTTP does not wait for warning delivery; later events still
attempt writes without retrying the failed record. The warning contains only the
destination and error, not event payloads.

The worker targets the `os.Stderr` file captured at the first write error, protecting
it against descriptor reuse. Warning errors are ignored; a closed stderr pipe does
not terminate HTTP through the warning, and a full pipe may block only this worker.
Process exit does not wait for it and may discard the warning. Startup diagnostics
are unchanged: this nonblocking behavior applies to the write-warning worker, not
the startup diagnostic or capture-file writes.

Partial writes may leave invalid JSONL. Neither a successful open nor absence of a
warning proves a complete capture. The implementation lives in
`_goroot/src/net/http/conftamer.go`; regression coverage is in
`tests/httpcapture/logging_test.go` and `tests/httpcapture/logging_linux_test.go`.
