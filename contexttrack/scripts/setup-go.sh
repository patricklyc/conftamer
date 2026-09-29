#!/usr/bin/env bash
# Prepare ContextTrack's instrumented net/http for a stock Go toolchain.
#
# Default (overlay) mode never modifies the Go installation. It copies only the
# files that go-inlibrary.patch touches out of `go env GOROOT`, patches the
# copies, adds the new files under _goroot/, and writes a `go build -overlay`
# file mapping the stock paths to those copies. The result is cached under a
# key covering the Go version, GOROOT, patch, added files, and stock sources,
# so repeated runs reuse it. Every run verifies that the overlay takes effect.
#
# Clone mode (--clone DEST) is the fallback for tools that cannot pass the
# overlay: it copies the whole GOROOT to DEST and patches it in place.
set -euo pipefail

REQUIRED_GO_VERSION=go1.26.6
PATCH_STRIP=4
CT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
PATCH_FILE=$CT_DIR/go-inlibrary.patch
ADD_DIR=$CT_DIR/_goroot

usage() {
  cat <<EOF
usage: scripts/setup-go.sh [--go GO] [--env] [--quiet]
       scripts/setup-go.sh --clone DEST [--go GO] [--quiet]

Overlay mode (default): build or reuse a cached -overlay for the stock
$REQUIRED_GO_VERSION toolchain and print the overlay.json path. With --env,
print shell lines instead:  eval "\$(scripts/setup-go.sh --env)"
Most users should run bin/ctgo, which does this automatically.

Clone mode: copy that toolchain's GOROOT to DEST (which must not exist) and
patch it in place. Use DEST/bin/go with GOROOT unset and GOTOOLCHAIN=local.

  --go GO       Go command to use (default: \$CONFTAMER_GO, else 'go').
  --env         Print export lines for GOTOOLCHAIN and GOFLAGS (overlay mode).
  --quiet, -q   Only print errors.

Overlays are cached in \$CONFTAMER_CACHE_DIR
(default: \${XDG_CACHE_HOME:-\$HOME/.cache}/conftamer).
EOF
}

die() { printf 'setup-go: %s\n' "$*" >&2; exit 1; }
say() { [[ -n $quiet ]] || printf 'setup-go: %s\n' "$*" >&2; }

go_cmd=${CONFTAMER_GO:-go}
mode=overlay
print=path
quiet=
clone_dest=
while (($#)); do
  case $1 in
    --go) (($# >= 2)) || die "--go needs a value"; go_cmd=$2; shift 2 ;;
    --go=*) go_cmd=${1#--go=}; shift ;;
    --clone) (($# >= 2)) || die "--clone needs a value"; mode=clone; clone_dest=$2; shift 2 ;;
    --clone=*) mode=clone; clone_dest=${1#--clone=}; shift ;;
    --env) print='env'; shift ;;
    -q | --quiet) quiet=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done
[[ $mode == overlay || $print == path ]] || die "--env applies only to overlay mode"
[[ $mode == overlay || -n $clone_dest ]] || die "--clone needs a destination"

go_bin=$(command -v -- "$go_cmd") || die "Go command not found: $go_cmd"
[[ $go_bin == /* ]] || go_bin=$PWD/$go_bin

# Query Go outside any module, without inherited GOFLAGS, and never let it
# switch toolchains (a switched toolchain has a different GOROOT).
go_query() { (cd / && env -u GOFLAGS GOTOOLCHAIN=local "$@"); }

hash_stdin() {
  if command -v sha256sum >/dev/null; then sha256sum | cut -c1-16
  else shasum -a 256 | cut -c1-16; fi
}

apply_patch() { # apply_patch DIR: apply go-inlibrary.patch exactly, no fuzz.
  if patch --version 2>/dev/null | grep -q 'GNU patch'; then
    patch -d "$1" -p"$PATCH_STRIP" --batch --forward --fuzz=0 --silent \
      --no-backup-if-mismatch --reject-file=- -i "$PATCH_FILE"
  else
    # Stop Git from treating an enclosing repository as the patch root.
    GIT_CEILING_DIRECTORIES=$(dirname -- "$1") \
      git -C "$1" apply -p"$PATCH_STRIP" "$PATCH_FILE"
  fi
}

install_added() { # install_added DIR
  local rel
  for rel in "${added[@]}"; do
    mkdir -p -- "$1/$(dirname -- "$rel")"
    cp -- "$ADD_DIR/$rel" "$1/$rel"
    chmod u+w -- "$1/$rel"
  done
}

verify() { # verify GO [ENV...]: the added files must be in their packages.
  local go=$1 rel pkg files
  shift
  for rel in "${added[@]}"; do
    [[ $rel == src/*.go ]] || continue
    pkg=$(dirname -- "${rel#src/}")
    files=$(go_query env "$@" "$go" list -f '{{join .GoFiles " "}}' "$pkg") ||
      die "go list $pkg failed"
    [[ " $files " == *" $(basename -- "$rel") "* ]] ||
      die "instrumentation is not in effect: $pkg lacks $(basename -- "$rel")"
  done
}

{ read -r goroot && read -r goversion; } < <(go_query "$go_bin" env GOROOT GOVERSION) ||
  die "could not run '$go_bin env'"
[[ $goversion == "$REQUIRED_GO_VERSION" || $goversion == "$REQUIRED_GO_VERSION "* ]] ||
  die "go-inlibrary.patch targets $REQUIRED_GO_VERSION, but $go_bin reports $goversion" \
    "(GOROOT $goroot); set CONFTAMER_GO or --go to a $REQUIRED_GO_VERSION toolchain"

# Files the patch modifies, relative to GOROOT.
mapfile -t targets < <(awk -v n="$PATCH_STRIP" '
  /^\+\+\+ / {
    if ($2 == "/dev/null") { print "!deleted"; next }
    k = split($2, p, "/"); rel = p[n + 1]
    for (i = n + 2; i <= k; i++) rel = rel "/" p[i]
    print rel
  }' "$PATCH_FILE")
((${#targets[@]})) || die "no files found in $PATCH_FILE"
for rel in "${targets[@]}"; do
  [[ $rel != '!deleted' ]] || die "$PATCH_FILE deletes a file; not supported"
  [[ -f $goroot/$rel ]] || die "$goroot/$rel not found; does the patch match this Go?"
done
# Files ContextTrack adds, relative to GOROOT.
mapfile -t added < <(cd "$ADD_DIR" && find . -type f | sed 's#^\./##' | LC_ALL=C sort)
((${#added[@]})) || die "no files found under $ADD_DIR"
for rel in "${added[@]}"; do
  [[ ! -e $goroot/$rel ]] || die "$goroot/$rel already exists; is this GOROOT already" \
    "patched? Use it directly, or point --go at a stock $REQUIRED_GO_VERSION"
done

if [[ $mode == clone ]]; then
  [[ ! -e $clone_dest ]] || die "$clone_dest already exists"
  parent=$(cd -- "$(dirname -- "$clone_dest")" && pwd) ||
    die "parent directory of $clone_dest does not exist"
  dest=$parent/$(basename -- "$clone_dest")
  trap 'rm -rf -- "$dest"' EXIT
  say "copying $goroot to $dest"
  cp -a -- "$goroot" "$dest"
  for rel in "${targets[@]}"; do chmod u+w -- "$dest/$rel" "$(dirname -- "$dest/$rel")"; done
  for rel in "${added[@]}"; do
    [[ ! -d $(dirname -- "$dest/$rel") ]] || chmod u+w -- "$(dirname -- "$dest/$rel")"
  done
  apply_patch "$dest" || die "go-inlibrary.patch does not apply cleanly to $goroot"
  install_added "$dest"
  got=$(go_query env -u GOROOT "$dest/bin/go" env GOROOT)
  [[ $got -ef $dest ]] || die "$dest/bin/go reports GOROOT $got"
  verify "$dest/bin/go" -u GOROOT
  trap - EXIT
  say "patched clone ready: $dest"
  say "use: unset GOROOT; export GOTOOLCHAIN=local; $dest/bin/go ..."
  printf '%s\n' "$dest/bin/go"
  exit 0
fi

cache=${CONFTAMER_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/conftamer}
key=$(
  {
    printf '%s\n' overlay-v1 "$goversion" "$goroot" "strip=$PATCH_STRIP"
    hash_stdin <"$PATCH_FILE"
    (cd "$ADD_DIR" && for rel in "${added[@]}"; do printf '%s ' "$rel"; hash_stdin <"$rel"; done)
    (cd "$goroot" && for rel in "${targets[@]}"; do printf '%s ' "$rel"; hash_stdin <"$rel"; done)
  } | hash_stdin
)
dir=$cache/overlay-${goversion%% *}-$key
overlay=$dir/overlay.json
# GOFLAGS is space-separated and overlay.json is written without escaping.
case $dir$goroot in
  *[[:space:]\"\\]*) die "paths must not contain whitespace, quotes, or backslashes:" \
    "$dir, $goroot (set CONFTAMER_CACHE_DIR)" ;;
esac

write_overlay() { # write_overlay FILE ROOT: map stock paths to files in ROOT.
  local rel sep=
  {
    printf '{"Replace":{'
    for rel in "${targets[@]}" "${added[@]}"; do
      printf '%s\n  "%s": "%s"' "$sep" "$goroot/$rel" "$2/$rel"
      sep=,
    done
    printf '\n}}\n'
  } >"$1"
}

if [[ ! -f $overlay ]]; then
  mkdir -p -- "$cache"
  tmp=$(mktemp -d "$cache/.tmp-overlay.XXXXXX")
  trap 'rm -rf -- "$tmp"' EXIT
  say "building overlay for $goversion ($goroot)"
  for rel in "${targets[@]}"; do
    mkdir -p -- "$tmp/root/$(dirname -- "$rel")"
    cp -- "$goroot/$rel" "$tmp/root/$rel"
    chmod u+w -- "$tmp/root/$rel"
  done
  apply_patch "$tmp/root" || die "go-inlibrary.patch does not apply cleanly to $goroot"
  install_added "$tmp/root"
  write_overlay "$tmp/staging.json" "$tmp/root"
  verify "$go_bin" GOFLAGS="-overlay=$tmp/staging.json"
  go_query env GOFLAGS="-overlay=$tmp/staging.json" "$go_bin" build net/http ||
    die "instrumented net/http does not compile"
  rm -- "$tmp/staging.json"
  write_overlay "$tmp/overlay.json" "$dir/root"
  printf 'go=%s\ngoroot=%s\npatch=%s\n' "$goversion" "$goroot" "$PATCH_FILE" >"$tmp/manifest"
  if [[ -e $dir ]]; then
    rm -rf -- "$tmp" # another run installed it first
  else
    mv -- "$tmp" "$dir"
  fi
  trap - EXIT
  [[ -f $overlay ]] || die "failed to install $dir"
fi
verify "$go_bin" GOFLAGS="-overlay=$overlay"

if [[ $print == path ]]; then
  printf '%s\n' "$overlay"
  exit 0
fi

# Append the overlay to the effective GOFLAGS (environment or `go env -w`).
goflags=$(go_query env GOFLAGS="${GOFLAGS:-}" "$go_bin" env GOFLAGS)
read -r -a words <<<"$goflags"
found=
for w in "${words[@]}"; do
  case $w in
    -overlay=* | --overlay=*)
      [[ ${w#*=} == "$overlay" ]] || die "GOFLAGS already sets a different overlay: $w"
      found=1 ;;
  esac
done
[[ -n $found ]] || goflags="${goflags:+$goflags }-overlay=$overlay"
printf 'export GOTOOLCHAIN=local\nexport GOFLAGS=%q\n' "$goflags"
