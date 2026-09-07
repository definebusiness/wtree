#!/usr/bin/env bash
# Offline contract test for the GitHub release publisher. Git operations use
# temporary local repositories and gh is a recording fake, so this test cannot
# contact GitHub or publish a release.
set -euo pipefail

source_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/wtree-github-release.XXXXXX")
trap 'rm -rf -- "$test_root"' EXIT

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
require_line() { grep -Fx -- "$2" "$1" >/dev/null || fail "missing argument [$2]"; }
reject_line() { ! grep -Fx -- "$2" "$1" >/dev/null || fail "unexpected argument [$2]"; }

remote="$test_root/remote.git"
repository="$test_root/repository"
fake_bin="$test_root/bin"
capture="$test_root/gh-arguments"
mkdir -p "$repository/scripts" "$repository/dist" "$fake_bin"
cp "$source_root/scripts/github-release.sh" "$repository/scripts/github-release.sh"
chmod +x "$repository/scripts/github-release.sh"
cat > "$repository/scripts/release-build.sh" <<'EOF'
#!/usr/bin/env sh
set -eu
: "${VERSION:?}"
: "${DIST_DIR:?}"
EOF
chmod +x "$repository/scripts/release-build.sh"

git init -q --bare "$remote"
git -C "$repository" init -q -b main
git -C "$repository" config user.name tester
git -C "$repository" config user.email tester@example.invalid
printf '/dist/\n' > "$repository/.gitignore"
printf 'test\n' > "$repository/source.txt"
git -C "$repository" add .gitignore source.txt scripts/github-release.sh \
  scripts/release-build.sh
git -C "$repository" commit -q -m release
git -C "$repository" remote add origin "$remote"
git -C "$repository" tag -a v1.2.3 -m 'v1.2.3'
git -C "$repository" push -q origin main v1.2.3

for asset in \
  wtree_1.2.3_linux_amd64 \
  wtree_1.2.3_darwin_amd64 \
  wtree_1.2.3_windows_amd64.exe \
  LICENSE NOTICE; do
  printf '%s\n' "$asset" > "$repository/dist/$asset"
done
(
  cd "$repository/dist"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum wtree_1.2.3_linux_amd64 wtree_1.2.3_darwin_amd64 \
      wtree_1.2.3_windows_amd64.exe LICENSE NOTICE > SHA256SUMS
  else
    shasum -a 256 wtree_1.2.3_linux_amd64 wtree_1.2.3_darwin_amd64 \
      wtree_1.2.3_windows_amd64.exe LICENSE NOTICE > SHA256SUMS
  fi
)

cat > "$fake_bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1 $2" in
  'auth status')
    exit 0
    ;;
  'repo view')
    printf '%s\n' "${GH_FAKE_VISIBILITY:-PUBLIC}"
    ;;
  'release create')
    : > "$GH_CAPTURE"
    shift 2
    printf '%s\n' "$@" >> "$GH_CAPTURE"
    ;;
  *)
    printf 'unexpected gh invocation: %s\n' "$*" >&2
    exit 64
    ;;
esac
EOF
chmod +x "$fake_bin/gh"

run_release() {
  (
    cd "$repository"
    PATH="$fake_bin:$PATH" GH_CAPTURE="$capture" \
      GH_REPO=definebusiness/wtree VERSION=1.2.3 "$@" \
      ./scripts/github-release.sh >/dev/null
  )
}

run_release env
for expected in \
  v1.2.3 \
  dist/wtree_1.2.3_linux_amd64 \
  dist/wtree_1.2.3_darwin_amd64 \
  dist/wtree_1.2.3_windows_amd64.exe \
  dist/SHA256SUMS dist/LICENSE dist/NOTICE \
  --verify-tag --generate-notes --draft; do
  require_line "$capture" "$expected"
done
require_line "$capture" definebusiness/wtree

run_release env PUBLISH=1
reject_line "$capture" --draft

printf 'dirty\n' > "$repository/untracked.txt"
if run_release env 2>/dev/null; then
  fail 'dirty repository was accepted'
fi
rm "$repository/untracked.txt"

if run_release env GH_FAKE_VISIBILITY=PRIVATE 2>/dev/null; then
  fail 'private repository was accepted for an open release'
fi

printf 'next\n' >> "$repository/source.txt"
git -C "$repository" add source.txt
git -C "$repository" commit -q -m next
if run_release env 2>/dev/null; then
  fail 'tag not identifying HEAD was accepted'
fi

printf 'github release publisher contract passed\n'
