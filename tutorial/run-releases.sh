#!/usr/bin/env sh
# Exercise the documented offline release-lock journey using only local remotes.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
source_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/wtree-release-tutorial.XXXXXX")
trap 'rm -rf "$test_root"' EXIT HUP INT TERM
git_real=$(command -v git)

fail() {
	printf 'release tutorial: %s\n' "$*" >&2
	exit 1
}

expect_failure() {
	if "$@" >"$test_root/failure.stdout" 2>"$test_root/failure.stderr"; then
		fail "expected failure: $*"
	fi
}

lock_release_name() {
	awk '$1 == "name:" { print $2; exit }' "$1"
}

lock_revision() {
	awk -v repository="$2" '$1 == repository ":" { found = 1; next } found && $1 == "revision:" { print $2; exit }' "$1"
}

assert_no_product_canary() {
	canary=$1
	shift
	if grep -R -F "$canary" "$@" >/dev/null 2>&1; then
		fail 'credential canary reached command output or product durable files'
	fi
}

fixture=$test_root/fixture
project=$test_root/project
data=$test_root/data
ci=$test_root/ci
bin=$test_root/bin
mkdir -p "$bin"
"$script_dir/setup-fixture.sh" "$fixture" >/dev/null
(cd "$source_root" && go build -o "$bin/wtree" ./cmd/wtree)
export PATH="$bin:$PATH"
export WTREE_REAL_GIT=$git_real
export WTREE_DATA_HOME=$data

wtree release --help >/dev/null
wtree release lock --help >/dev/null
wtree release materialize --help >/dev/null
wtree release --how-to >/dev/null
wtree release lock --how-to >/dev/null
wtree release materialize --how-to >/dev/null

wtree clone "$fixture/project.wtree.yml" "$project" --worktree-root "$test_root/worktrees" >/dev/null
cd "$project"

# Give each non-base repository an unpublished local release commit. The later
# CI failure proves the base lock alone is insufficient; child reachability is
# then restored by explicit caller-owned publication.
for child in backend frontend; do
	git -C "$child" config user.name 'Wtree Release Tutorial'
	git -C "$child" config user.email tutorial@wtree.invalid
	printf '%s release source\n' "$child" > "$child/release-source.txt"
	git -C "$child" add release-source.txt
	git -C "$child" commit -q -m "Prepare $child release source"
done
git config user.name 'Wtree Release Tutorial'
git config user.email tutorial@wtree.invalid

sed -i.bak 's/^version: 2$/version: 3/' .wtree.yml
rm -f .wtree.yml.bak
cat >> .wtree.yml <<'EOF'
hooks:
  post-release:
    - id: tag-backend-release
      repository: backend
      command: [tag-wtree-release]
      timeout: 5m
    - id: tag-frontend-release
      repository: frontend
      command: [tag-wtree-release]
      timeout: 5m
EOF
cat > "$bin/tag-wtree-release" <<'EOF'
#!/usr/bin/env sh
set -eu
git check-ref-format "refs/tags/$WTREE_RELEASE_NAME"
expected=$(git rev-parse "$WTREE_HEAD^{commit}")
if git rev-parse -q --verify "refs/tags/$WTREE_RELEASE_NAME^{commit}" >/dev/null; then
	actual=$(git rev-parse "refs/tags/$WTREE_RELEASE_NAME^{commit}")
	test "$actual" = "$expected"
	exit 0
fi
git tag -a "$WTREE_RELEASE_NAME" "$expected" -m "Release $WTREE_RELEASE_NAME"
EOF
chmod +x "$bin/tag-wtree-release"

# RED: dry-run writes no lock and invokes no hook. A collision is isolated
# before the v1.4.0 release is generated, so the committed base lock cannot
# accidentally retain the rejected v-collision candidate.
wtree release lock v1.4.0 --dry-run --json > "$test_root/dry-run.json"
test ! -e project.wtree.lock.yml || fail 'dry-run wrote a lock'
git -C frontend tag -a v-collision HEAD~1 -m collision
collision_before=$(git -C frontend rev-parse 'v-collision^{commit}')
expect_failure wtree release lock v-collision
test "$(git -C frontend rev-parse 'v-collision^{commit}')" = "$collision_before" || fail 'collision tag moved'
test -f project.wtree.lock.yml || fail 'collision did not leave its expected fixture lock'
test ! -L project.wtree.lock.yml || fail 'collision fixture lock changed type'
test "$(lock_release_name project.wtree.lock.yml)" = v-collision || fail 'collision fixture lock has the wrong release name'
# The failed hook intentionally leaves an untracked lock. This disposable
# fixture owns that exact regular v-collision candidate, so remove it only
# after verifying its type and release name; the normal v1.4.0 path below uses
# no --force and therefore still proves the ordinary absent-target behavior.
rm project.wtree.lock.yml
test ! -e project.wtree.lock.yml || fail 'collision fixture lock was not removed'

# GREEN: the real candidate is generated after the isolated failure, tags both
# children, and an identical invocation is safe through the same helper.
wtree release lock v1.4.0 --json > "$test_root/lock.json"
test "$(lock_release_name project.wtree.lock.yml)" = v1.4.0 || fail 'v1 lock has the wrong release name'
for child in backend frontend; do
	git -C "$child" rev-parse "v1.4.0^{commit}" >/dev/null || fail "$child was not tagged"
done
git add project.wtree.lock.yml
git commit -q -m 'chore: lock release v1.4.0'
git tag -a v1.4.0 -m 'Release v1.4.0'
test "$(git show 'v1.4.0:project.wtree.lock.yml' | lock_release_name /dev/stdin)" = v1.4.0 || fail 'tagged base does not contain the v1.4.0 lock'
# A matching rerun succeeds once the prior candidate is clean and tracked;
# the same idempotent child tags are accepted by the hook helper.
wtree release lock v1.4.0 --json > "$test_root/rerun.json"

# Publish the v1.4.0 base release before child reachability only to prove that
# a clean CI checkout fails safely. This is a separate negative fixture, never
# reused for the successful publication sequence.
git push -q origin HEAD refs/tags/v1.4.0
ci_negative=$test_root/ci-negative
data_negative=$test_root/ci-negative-data
git clone -q "$fixture/origins/acme-shop.git" "$ci_negative"
git -C "$ci_negative" checkout -q --detach v1.4.0
export WTREE_DATA_HOME=$data_negative
expect_failure sh -c "cd '$ci_negative' && wtree release materialize project.wtree.lock.yml --json"
for child in backend frontend; do
	test ! -e "$ci_negative/$child" || fail "unavailable child published a destination: $child"
done
test ! -e "$data_negative/registry.json" || fail 'unavailable materialization published a registry'
test ! -e "$data_negative/state" || fail 'unavailable materialization published complete workspace state'

# Create a fresh v1.4.1 lock after the isolated failure. Its child commits and
# tags are published before the base tag, so the positive fixture proves the
# documented caller-owned child-first/base-last order rather than retrying a
# partially published workspace.
export WTREE_DATA_HOME=$data
cd "$project"
wtree release lock v1.4.1 --json > "$test_root/lock-v1.4.1.json"
test "$(lock_release_name project.wtree.lock.yml)" = v1.4.1 || fail 'v1.4.1 lock has the wrong release name'
git add project.wtree.lock.yml
git commit -q -m 'chore: lock release v1.4.1'
git tag -a v1.4.1 -m 'Release v1.4.1'
for child in backend frontend; do
	git -C "$child" push -q origin HEAD refs/tags/v1.4.1
done
git push -q origin HEAD refs/tags/v1.4.1

# A deterministic fake Git transport sees both inherited authentication
# channels on the successful fetch and invokes the supplied fake askpass
# helper. Its capture files are harness evidence, not product state.
auth_log=$test_root/fake-git-auth.log
askpass_log=$test_root/fake-askpass.log
auth_canary=release-auth-canary
auth_socket=$test_root/fake-ssh-agent.sock
: > "$auth_socket"
cat > "$bin/askpass" <<'EOF'
#!/usr/bin/env sh
set -eu
printf '%s\n' "$1" >> "$WTREE_ASKPASS_LOG"
printf '%s\n' "$ASKPASS_REQUIRED_SECRET"
EOF
cat > "$bin/git" <<EOF
#!/usr/bin/env sh
set -eu
case " \$* " in
  *' fetch '*)
    printf 'SSH_AUTH_SOCK=%s\nGIT_ASKPASS=%s\nGIT_TERMINAL_PROMPT=%s\n' "\$SSH_AUTH_SOCK" "\$GIT_ASKPASS" "\$GIT_TERMINAL_PROMPT" >> "\$WTREE_AUTH_LOG"
    "\$GIT_ASKPASS" 'release tutorial authentication' >/dev/null
    ;;
esac
exec "$git_real" "\$@"
EOF
chmod +x "$bin/askpass" "$bin/git"

ci=$test_root/ci-success
data_success=$test_root/ci-success-data
git clone -q "$fixture/origins/acme-shop.git" "$ci"
git -C "$ci" checkout -q --detach v1.4.1
export WTREE_DATA_HOME=$data_success
export WTREE_AUTH_LOG=$auth_log
export WTREE_ASKPASS_LOG=$askpass_log
export SSH_AUTH_SOCK=$auth_socket
export GIT_ASKPASS=$bin/askpass
export ASKPASS_REQUIRED_SECRET=$auth_canary
base_before=$(git -C "$ci" rev-parse HEAD)
(cd "$ci" && wtree release materialize project.wtree.lock.yml --json > "$test_root/materialize.json")
grep -F "SSH_AUTH_SOCK=$auth_socket" "$auth_log" >/dev/null || fail 'fake SSH-agent channel did not reach Git'
grep -F "GIT_ASKPASS=$bin/askpass" "$auth_log" >/dev/null || fail 'askpass channel did not reach Git'
grep -F 'GIT_TERMINAL_PROMPT=0' "$auth_log" >/dev/null || fail 'Git prompt suppression was not retained'
test -s "$askpass_log" || fail 'fake askpass helper was not invoked'
assert_no_product_canary "$auth_canary" "$ci" "$data_success" "$test_root/materialize.json"
test "$(git -C "$ci" rev-parse HEAD)" = "$base_before" || fail 'materialization changed base HEAD'
for child in backend frontend; do
	test -z "$(git -C "$ci/$child" symbolic-ref -q HEAD)" || fail "$child is not detached"
	expected=$(lock_revision "$ci/project.wtree.lock.yml" "$child")
	test -n "$expected" || fail "missing locked revision for $child"
	test "$(git -C "$ci/$child" rev-parse HEAD)" = "$expected" || fail "$child HEAD does not equal its committed lock revision"
done
(cd "$ci" && wtree exec -- git rev-parse HEAD > "$test_root/exec.txt")
test -s "$data_success/state"/*/default.json || fail 'successful materialization did not publish complete workspace state'
grep -F '"projects"' "$data_success/registry.json" >/dev/null || fail 'successful materialization did not publish registry state'

# The normal next release replaces a clean tracked prior lock without --force.
export WTREE_DATA_HOME=$data
cd "$project"
wtree release lock v1.4.2 --no-hooks > "$test_root/next-release.txt"
grep -F 'Release lock: replaced' "$test_root/next-release.txt" >/dev/null || fail 'next release did not replace clean lock'
