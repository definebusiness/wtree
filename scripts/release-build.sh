#!/usr/bin/env sh
# Build deterministic, unpublished release artifacts. VERSION is intentionally
# required so a caller chooses the release identity rather than deriving it
# from mutable Git state.
set -eu

version=${VERSION:?VERSION is required, for example VERSION=1.2.3}
dist_dir=${DIST_DIR:-dist}
ldflags="-s -w -X github.com/marcel/wtree/internal/cli.Version=${version}"

mkdir -p "$dist_dir"
parent_dir=$(dirname "$dist_dir")
stage_dir=$(mktemp -d "$parent_dir/.wtree-release.XXXXXX")
trap 'rm -rf "$stage_dir"' EXIT HUP INT TERM
artifacts="LICENSE NOTICE"
cp LICENSE "$stage_dir/LICENSE"
cp NOTICE "$stage_dir/NOTICE"
for target in linux/amd64 darwin/amd64 windows/amd64; do
  os=${target%/*}
  arch=${target#*/}
  suffix=""
  if [ "$os" = "windows" ]; then suffix=".exe"; fi
  artifact="wtree_${version}_${os}_${arch}${suffix}"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$stage_dir/$artifact" ./cmd/wtree
  artifacts="$artifacts $artifact"
done

(
  cd "$stage_dir"
  : > SHA256SUMS
  for artifact in $artifacts; do
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$artifact" >> SHA256SUMS
    else
      shasum -a 256 "$artifact" >> SHA256SUMS
    fi
  done
)

# Only artifacts named by this release contract are owned by wtree. Build in
# staging first so a failed cross-build leaves the existing release intact.
find "$dist_dir" -maxdepth 1 -type f \( \
  -name 'wtree_*_linux_amd64' -o \
  -name 'wtree_*_darwin_amd64' -o \
  -name 'wtree_*_windows_amd64.exe' -o \
  -name SHA256SUMS -o -name LICENSE -o -name NOTICE \
\) -delete
for artifact in $artifacts SHA256SUMS; do
  mv "$stage_dir/$artifact" "$dist_dir/$artifact"
done
trap - EXIT HUP INT TERM
rmdir "$stage_dir"
