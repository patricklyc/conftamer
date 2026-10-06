#!/usr/bin/env bash
# Prepare instrumented net/http without modifying stock GOROOT.
# Overlay mode patches copied files; --clone copies the entire toolchain.
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

Default: cache an overlay for stock $REQUIRED_GO_VERSION and print its path.
With --env, print shell exports: eval "\$(scripts/setup-go.sh --env)"
Use bin/ctgo for automatic setup.

Clone mode: patch a copy at new DEST. Use DEST/bin/go with GOROOT unset
and GOTOOLCHAIN=local.

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
    --go)
      (($# >= 2)) || die "--go needs a value"
      go_cmd=$2; shift 2 ;;
    --go=*) go_cmd=${1#--go=}; shift ;;
    --clone)
      (($# >= 2)) || die "--clone needs a value"
      mode=clone; clone_dest=$2; shift 2 ;;
    --clone=*) mode=clone; clone_dest=${1#--clone=}; shift ;;
    --env) print='env'; shift ;;
    -q | --quiet) quiet=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done
[[ $mode == overlay || $print == path ]] || die "--env applies only to overlay mode"
[[ $mode == overlay || -n $clone_dest ]] || die "--clone needs a destination"

# Query Go outside any module, without inherited GOFLAGS, and never let it
# switch toolchains (a switched toolchain has a different GOROOT).
go_query() { (cd / && env -u GOFLAGS GOTOOLCHAIN=local "$@"); }

hash_stdin() {
  if command -v sha256sum >/dev/null; then
    sha256sum | cut -c1-16
  else
    shasum -a 256 | cut -c1-16
  fi
}

# Discovery and orchestration deliberately share validated toolchain variables,
# targets/added arrays, and the prepared overlay; leaf helpers take local inputs.
# Establish go_bin, goroot, and goversion for all subsequent stages.
validate_toolchain() {
  go_bin=$(command -v -- "$go_cmd") || die "Go command not found: $go_cmd"
  [[ $go_bin == /* ]] || go_bin=$PWD/$go_bin

  { read -r goroot && read -r goversion; } < <(go_query "$go_bin" env GOROOT GOVERSION) ||
    die "could not run '$go_bin env'"
  [[ $goversion == "$REQUIRED_GO_VERSION" || $goversion == "$REQUIRED_GO_VERSION "* ]] ||
    die "go-inlibrary.patch targets $REQUIRED_GO_VERSION, but $go_bin reports $goversion" \
      "(GOROOT $goroot); set CONFTAMER_GO or --go to a $REQUIRED_GO_VERSION toolchain"
}

# Establish targets (patched files) and added (new files), relative to GOROOT.
discover_patch_inputs() {
  local relative_path
  mapfile -t targets < <(awk -v n="$PATCH_STRIP" '
    /^\+\+\+ / {
      if ($2 == "/dev/null") { print "!deleted"; next }
      k = split($2, p, "/"); rel = p[n + 1]
      for (i = n + 2; i <= k; i++) rel = rel "/" p[i]
      print rel
    }' "$PATCH_FILE")
  ((${#targets[@]})) || die "no files found in $PATCH_FILE"
  for relative_path in "${targets[@]}"; do
    [[ $relative_path != '!deleted' ]] || die "$PATCH_FILE deletes a file; not supported"
    [[ -f $goroot/$relative_path ]] ||
      die "$goroot/$relative_path not found; does the patch match this Go?"
  done

  mapfile -t added < <(cd "$ADD_DIR" && find . -type f | sed 's#^\./##' | LC_ALL=C sort)
  ((${#added[@]})) || die "no files found under $ADD_DIR"
  for relative_path in "${added[@]}"; do
    [[ ! -e $goroot/$relative_path ]] ||
      die "$goroot/$relative_path already exists; is this GOROOT already" \
        "patched? Use it directly, or point --go at a stock $REQUIRED_GO_VERSION"
  done
}

apply_patch() {
  local target_root=$1
  if patch --version 2>/dev/null | grep -q 'GNU patch'; then
    patch -d "$target_root" -p"$PATCH_STRIP" --batch --forward --fuzz=0 --silent \
      --no-backup-if-mismatch --reject-file=- -i "$PATCH_FILE"
  else
    # Stop Git from treating an enclosing repository as the patch root.
    GIT_CEILING_DIRECTORIES=$(dirname -- "$target_root") \
      git -C "$target_root" apply -p"$PATCH_STRIP" "$PATCH_FILE"
  fi
}

install_added() {
  local target_root=$1 relative_path
  for relative_path in "${added[@]}"; do
    mkdir -p -- "$target_root/$(dirname -- "$relative_path")"
    cp -- "$ADD_DIR/$relative_path" "$target_root/$relative_path"
    chmod u+w -- "$target_root/$relative_path"
  done
}

# verify GO [ENV...]: check that the added files are visible to their packages.
verify() {
  local go_command=$1 relative_path package files
  shift
  for relative_path in "${added[@]}"; do
    [[ $relative_path == src/*.go ]] || continue
    package=$(dirname -- "${relative_path#src/}")
    files=$(go_query env "$@" "$go_command" list -f '{{join .GoFiles " "}}' "$package") ||
      die "go list $package failed"
    [[ " $files " == *" $(basename -- "$relative_path") "* ]] ||
      die "instrumentation is not in effect: $package lacks $(basename -- "$relative_path")"
  done
}

prepare_clone() {
  local destination=$1 parent dest relative_path reported_root
  [[ ! -e $destination ]] || die "$destination already exists"
  parent=$(cd -- "$(dirname -- "$destination")" && pwd) ||
    die "parent directory of $destination does not exist"
  dest=$parent/$(basename -- "$destination")
  trap 'rm -rf -- "$dest"' EXIT

  say "copying $goroot to $dest"
  cp -a -- "$goroot" "$dest"
  for relative_path in "${targets[@]}"; do
    chmod u+w -- "$dest/$relative_path" "$(dirname -- "$dest/$relative_path")"
  done
  for relative_path in "${added[@]}"; do
    [[ ! -d $(dirname -- "$dest/$relative_path") ]] ||
      chmod u+w -- "$(dirname -- "$dest/$relative_path")"
  done
  apply_patch "$dest" || die "go-inlibrary.patch does not apply cleanly to $goroot"
  install_added "$dest"
  reported_root=$(go_query env -u GOROOT "$dest/bin/go" env GOROOT)
  [[ $reported_root -ef $dest ]] || die "$dest/bin/go reports GOROOT $reported_root"
  verify "$dest/bin/go" -u GOROOT

  trap - EXIT
  say "patched clone ready: $dest"
  say "use: unset GOROOT; export GOTOOLCHAIN=local; $dest/bin/go ..."
  printf '%s\n' "$dest/bin/go"
}

hash_files() {
  local root=$1 relative_path
  shift
  for relative_path in "$@"; do
    printf '%s ' "$relative_path"
    hash_stdin <"$root/$relative_path"
  done
}

# Changes to the toolchain, patch, added files, or stock sources invalidate cache.
compute_overlay_key() {
  {
    printf '%s\n' overlay-v1 "$goversion" "$goroot" "strip=$PATCH_STRIP"
    hash_stdin <"$PATCH_FILE"
    hash_files "$ADD_DIR" "${added[@]}"
    hash_files "$goroot" "${targets[@]}"
  } | hash_stdin
}

# write_overlay OVERLAY_FILE PATCHED_ROOT STOCK_ROOT RELATIVE_PATH...
write_overlay() {
  local overlay_file=$1 patched_root=$2 stock_root=$3
  shift 3
  local relative_path separator=
  {
    printf '{"Replace":{'
    for relative_path in "$@"; do
      printf '%s\n  "%s": "%s"' "$separator" "$stock_root/$relative_path" "$patched_root/$relative_path"
      separator=,
    done
    printf '\n}}\n'
  } >"$overlay_file"
}

# Establish overlay, reusing the cache or installing a verified staging tree.
prepare_overlay() {
  local cache key directory tmp relative_path
  cache=${CONFTAMER_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/conftamer}
  key=$(compute_overlay_key)
  directory=$cache/overlay-${goversion%% *}-$key
  overlay=$directory/overlay.json
  # GOFLAGS is space-separated and overlay.json is written without escaping.
  case $directory$goroot in
    *[[:space:]\"\\]*) die "paths must not contain whitespace, quotes, or backslashes:" \
      "$directory, $goroot (set CONFTAMER_CACHE_DIR)" ;;
  esac
  [[ -f $overlay ]] && return

  mkdir -p -- "$cache"
  tmp=$(mktemp -d "$cache/.tmp-overlay.XXXXXX")
  trap 'rm -rf -- "$tmp"' EXIT
  say "building overlay for $goversion ($goroot)"
  for relative_path in "${targets[@]}"; do
    mkdir -p -- "$tmp/root/$(dirname -- "$relative_path")"
    cp -- "$goroot/$relative_path" "$tmp/root/$relative_path"
    chmod u+w -- "$tmp/root/$relative_path"
  done
  apply_patch "$tmp/root" || die "go-inlibrary.patch does not apply cleanly to $goroot"
  install_added "$tmp/root"
  write_overlay "$tmp/staging.json" "$tmp/root" "$goroot" "${targets[@]}" "${added[@]}"
  verify "$go_bin" GOFLAGS="-overlay=$tmp/staging.json"
  go_query env GOFLAGS="-overlay=$tmp/staging.json" "$go_bin" build net/http ||
    die "instrumented net/http does not compile"

  rm -- "$tmp/staging.json"
  write_overlay "$tmp/overlay.json" "$directory/root" "$goroot" "${targets[@]}" "${added[@]}"
  printf 'go=%s\ngoroot=%s\npatch=%s\n' "$goversion" "$goroot" "$PATCH_FILE" >"$tmp/manifest"
  if [[ -e $directory ]]; then
    rm -rf -- "$tmp" # another run installed it first
  else
    mv -- "$tmp" "$directory"
  fi
  trap - EXIT
  [[ -f $overlay ]] || die "failed to install $directory"
}

# print_environment GO_COMMAND OVERLAY_PATH
print_environment() {
  local go_command=$1 overlay_path=$2 goflags word found=
  local -a words
  # Append the overlay to the effective GOFLAGS (environment or `go env -w`).
  goflags=$(go_query env GOFLAGS="${GOFLAGS:-}" "$go_command" env GOFLAGS)
  read -r -a words <<<"$goflags"
  for word in "${words[@]}"; do
    case $word in
      -overlay=* | --overlay=*)
        [[ ${word#*=} == "$overlay_path" ]] || die "GOFLAGS already sets a different overlay: $word"
        found=1
        ;;
    esac
  done
  [[ -n $found ]] || goflags="${goflags:+$goflags }-overlay=$overlay_path"
  printf 'export GOTOOLCHAIN=local\nexport GOFLAGS=%q\n' "$goflags"
}

# Main flow: validate → discover inputs → prepare → verify → print.
validate_toolchain
discover_patch_inputs
if [[ $mode == clone ]]; then
  prepare_clone "$clone_dest"
  exit 0
fi

prepare_overlay
verify "$go_bin" GOFLAGS="-overlay=$overlay"
if [[ $print == env ]]; then
  print_environment "$go_bin" "$overlay"
else
  printf '%s\n' "$overlay"
fi
