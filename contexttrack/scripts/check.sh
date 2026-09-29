#!/usr/bin/env bash
# Run ContextTrack's checks: static checks, tooling tests, the patch
# regression suite, upstream net/http tests with tracing on, and the analysis
# scripts. Captures and outputs are kept in a fresh directory printed at the end.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/check.sh [--no-race] [--no-upstream] [--clone]

  --no-race      Run tests/httpcapture without -race (no C compiler needed).
  --no-upstream  Skip the upstream net/http short tests.
  --clone        Also test clone mode (copies the whole GOROOT).

Uses the Go command in $CONFTAMER_GO (default 'go'), which must be a stock
Go 1.26.6. Requires python3, gofmt, and bash.
EOF
}

race=-race
upstream=1
clone=
while (($#)); do
  case $1 in
    --no-race) race= ;;
    --no-upstream) upstream= ;;
    --clone) clone=1 ;;
    -h | --help) usage; exit 0 ;;
    *) printf 'check: unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

CT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
CTGO=$CT_DIR/bin/ctgo
out=$(mktemp -d "${TMPDIR:-/tmp}/contexttrack-check.XXXXXX")
export PYTHONPYCACHEPREFIX=$out/pycache # keep __pycache__ out of the tree
step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'check: FAILED: %s (outputs in %s)\n' "$*" "$out" >&2; exit 1; }
trap 'fail "exit status $? at line $LINENO"' ERR
cd "$CT_DIR"

step "static checks"
bash -n bin/ctgo scripts/*.sh
if command -v shellcheck >/dev/null; then shellcheck bin/ctgo scripts/*.sh; fi
unformatted=$(gofmt -l _goroot tests/httpcapture)
[[ -z $unformatted ]] || fail "gofmt needed: $unformatted"
python3 -m py_compile analysis/*.py tests/*.py
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then git diff --check HEAD -- .; fi

step "overlay setup"
scripts/setup-go.sh
"$CTGO" vet net/http

step "tooling tests"
CONTEXTTRACK_REQUIRE_GO=1 CONTEXTTRACK_TEST_CLONE=${clone:+1} \
  python3 -m unittest tests/test_tooling.py

step "patch regression tests (tests/httpcapture)"
capture=$out/httpcapture.jsonl
(cd tests/httpcapture && CONFTAMER_EVENTS=$capture "$CTGO" test $race -count=1 ./...)
[[ -s $capture ]] || fail "tests/httpcapture wrote no events"

if [[ -n $upstream ]]; then
  step "upstream net/http short tests, tracing on"
  goroot=$("$CTGO" env GOROOT)
  (cd "$goroot/src/net/http" && CONFTAMER_EVENTS=$out/nethttp.jsonl "$CTGO" test -short -count=1 .)
fi

step "analysis scripts"
traces=("$capture")
fixture=$CT_DIR/../../conftamer-cli
spec=node-query:examples/contexttrack/prometheus/scrape-ok.jsonl
if git -C "$fixture" cat-file -e "$spec" 2>/dev/null; then
  git -C "$fixture" show "$spec" >"$out/scrape-ok.jsonl"
  traces+=("$out/scrape-ok.jsonl")
else
  printf 'skipping consumer fixture: %s not found in %s\n' "$spec" "$fixture"
fi
for trace in "${traces[@]}"; do
  name=$(basename -- "$trace" .jsonl)
  python3 analysis/group_by_context.py "$trace" >"$out/$name.groups.txt" 2>"$out/$name.groups.err"
  python3 analysis/message_graph.py "$trace" --format text >"$out/$name.graph.txt" 2>"$out/$name.graph.err"
  python3 analysis/message_graph.py "$trace" --recv-sent --format dot >"$out/$name.dot" 2>"$out/$name.dot.err"
  printf '%s: %s events, %s graph lines\n' "$name" "$(wc -l <"$trace")" "$(wc -l <"$out/$name.graph.txt")"
done

trap - ERR
printf '\ncheck: all passed; outputs in %s\n' "$out"
