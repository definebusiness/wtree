#!/usr/bin/env sh
# Publish the exact local release artifact set through GitHub CLI. The script
# never creates or pushes a tag or changes repository visibility. It creates a
# draft by default and publishes immediately only when PUBLISH=1.
set -eu

fail() {
  printf 'github release: %s\n' "$*" >&2
  exit 1
}

version=${VERSION:?VERSION is required, for example VERSION=1.2.3}
dist_dir=${DIST_DIR:-dist}
publish=${PUBLISH:-0}
remote=${REMOTE:-origin}
github_host=${GH_HOST:-github.com}
tag="v$version"

if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
  fail "VERSION must be a semantic version without a leading v"
fi
case "$publish" in
  0|1) ;;
  *) fail "PUBLISH must be 0 or 1" ;;
esac

for command in git gh awk; do
  command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

root_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root_dir"

if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
  fail "refusing to release from a dirty worktree"
fi

head=$(git rev-parse --verify HEAD)
tag_commit=$(git rev-parse --verify "$tag^{commit}") ||
  fail "local tag $tag does not exist"
if [ "$tag_commit" != "$head" ]; then
  fail "$tag does not identify the current HEAD"
fi

remote_tags=$(git ls-remote --tags "$remote" \
  "refs/tags/$tag" "refs/tags/$tag^{}") ||
  fail "cannot inspect $tag on remote $remote"
remote_commit=$(printf '%s\n' "$remote_tags" | awk -v tag="$tag" '
  $2 == "refs/tags/" tag { direct = $1 }
  $2 == "refs/tags/" tag "^{}" { peeled = $1 }
  END { if (peeled != "") print peeled; else print direct }
')
if [ -z "$remote_commit" ]; then
  fail "$tag is not pushed to remote $remote"
fi
if [ "$remote_commit" != "$head" ]; then
  fail "remote tag $tag does not identify the current HEAD"
fi

gh auth status -h "$github_host" >/dev/null 2>&1 ||
  fail "GitHub CLI is not authenticated for $github_host"

repository=${GH_REPO:-}
if [ -z "$repository" ]; then
  repository=$(gh repo view --json nameWithOwner --jq '.nameWithOwner') ||
    fail "cannot determine the GitHub repository"
fi
visibility=$(gh repo view "$repository" --json visibility --jq '.visibility') ||
  fail "cannot inspect GitHub repository $repository"
if [ "$visibility" != "PUBLIC" ]; then
  fail "$repository is not public; refusing an open release"
fi

VERSION="$version" DIST_DIR="$dist_dir" ./scripts/release-build.sh

linux_asset="$dist_dir/wtree_${version}_linux_amd64"
darwin_asset="$dist_dir/wtree_${version}_darwin_amd64"
windows_asset="$dist_dir/wtree_${version}_windows_amd64.exe"
checksums="$dist_dir/SHA256SUMS"
license="$dist_dir/LICENSE"
notice="$dist_dir/NOTICE"

for asset in "$linux_asset" "$darwin_asset" "$windows_asset" \
  "$checksums" "$license" "$notice"; do
  [ -f "$asset" ] || fail "missing release asset: $asset"
done

(
  cd "$dist_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c SHA256SUMS
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c SHA256SUMS
  else
    fail "sha256sum or shasum is required"
  fi
) || fail "release checksum verification failed"

set -- "$tag" "$linux_asset" "$darwin_asset" "$windows_asset" \
  "$checksums" "$license" "$notice" \
  --repo "$repository" --verify-tag --generate-notes
if [ "$publish" = "0" ]; then
  set -- "$@" --draft
fi

gh release create "$@"

if [ "$publish" = "0" ]; then
  printf 'Created draft GitHub release %s in %s.\n' "$tag" "$repository"
else
  printf 'Published GitHub release %s in %s.\n' "$tag" "$repository"
fi
