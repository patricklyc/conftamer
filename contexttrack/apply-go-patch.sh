#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 /path/to/clean-go1.26.6" >&2
  exit 2
}

if [[ $# -ne 1 ]]; then
  usage
fi

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
go_work=$1
patch=$repo/go-inlibrary.patch
overlay_source=$repo/_stdlib/net/http
overlay_names=(
  conftamer.go
  conftamer_log.go
  conftamer_internal_test.go
  conftamer_test.go
)

if [[ ! -d $go_work ]]; then
  echo "apply-go-patch: destination is not a directory: $go_work" >&2
  exit 1
fi
if ! git -C "$go_work" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "apply-go-patch: destination is not a Go Git tree: $go_work" >&2
  exit 1
fi
if [[ ! -f $go_work/VERSION ]]; then
  echo "apply-go-patch: destination has no VERSION file: $go_work" >&2
  exit 1
fi
IFS= read -r version < "$go_work/VERSION"
if [[ $version != go1.26.6 ]]; then
  echo "apply-go-patch: expected Go go1.26.6, found ${version:-<empty>}" >&2
  exit 1
fi

for name in "${overlay_names[@]}"; do
  if [[ ! -f $overlay_source/$name ]]; then
    echo "apply-go-patch: missing overlay source: $overlay_source/$name" >&2
    exit 1
  fi
  if [[ -e $go_work/src/net/http/$name ]]; then
    echo "apply-go-patch: overlay destination already exists (patch already applied?): $go_work/src/net/http/$name" >&2
    exit 1
  fi
done

if [[ -n $(git -C "$go_work" status --porcelain --untracked-files=all) ]]; then
  echo "apply-go-patch: destination must be a clean Go Git tree: $go_work" >&2
  exit 1
fi
if [[ ! -f $patch ]]; then
  echo "apply-go-patch: missing patch: $patch" >&2
  exit 1
fi

git -C "$go_work" apply --check "$patch"
git -C "$go_work" apply "$patch"
for name in "${overlay_names[@]}"; do
  cp "$overlay_source/$name" "$go_work/src/net/http/$name"
done
