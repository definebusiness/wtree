#!/usr/bin/env sh
set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
dist_dir="$tmp_dir/dist"

(
  cd "$root_dir"
  VERSION=0.0.0-old DIST_DIR="$dist_dir" ./scripts/release-build.sh
  printf 'keep\n' > "$dist_dir/unrelated.txt"
  printf 'keep\n' > "$dist_dir/wtree_notes"
  printf 'keep\n' > "$dist_dir/wtree_0.0.0-old_linux_arm64"
  printf 'keep\n' > "$dist_dir/wtree_0.0.0-old_windows_amd64.exe.bak"
  VERSION=0.0.0-new DIST_DIR="$dist_dir" ./scripts/release-build.sh
)

test -f "$dist_dir/unrelated.txt"
test -f "$dist_dir/wtree_notes"
test -f "$dist_dir/wtree_0.0.0-old_linux_arm64"
test -f "$dist_dir/wtree_0.0.0-old_windows_amd64.exe.bak"
test ! -e "$dist_dir/wtree_0.0.0-old_linux_amd64"
test ! -e "$dist_dir/wtree_0.0.0-old_darwin_amd64"
test ! -e "$dist_dir/wtree_0.0.0-old_windows_amd64.exe"
test -f "$dist_dir/wtree_0.0.0-new_linux_amd64"
test -f "$dist_dir/wtree_0.0.0-new_darwin_amd64"
test -f "$dist_dir/wtree_0.0.0-new_windows_amd64.exe"
test -f "$dist_dir/LICENSE"
test -f "$dist_dir/NOTICE"
test -f "$dist_dir/SHA256SUMS"
