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

# The host-native built binary is an installed-artifact acceptance surface. It
# is never installed or published; command help proves the release-facing hook
# entry points and flags remain available to a user.
host_binary="$tmp_dir/wtree-host"
(
  cd "$root_dir"
  go build -o "$host_binary" ./cmd/wtree
)
"$host_binary" hooks --help | grep -F 'explicit consent operations' >/dev/null
"$host_binary" hooks retry --help | grep -F 'never starts a fresh run' >/dev/null
"$host_binary" clone --help | grep -F -- '--run-hooks' >/dev/null
"$host_binary" create --help | grep -F -- '--no-hooks' >/dev/null
"$host_binary" hooks --how-to | grep -F 'sanitized environment' >/dev/null
