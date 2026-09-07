#!/usr/bin/env sh
# Run the companion-repository tutorial as an isolated public-command test.
set -eu

export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_NOSYSTEM=1
export GIT_TERMINAL_PROMPT=0
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=core.hooksPath
export GIT_CONFIG_VALUE_0=/dev/null

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
source_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
temp_base=$(CDPATH='' cd -- "${TMPDIR:-/tmp}" && pwd -P)
test_root=$(mktemp -d "$temp_base/wtree-companion.XXXXXX")
test_root=$(CDPATH='' cd -- "$test_root" && pwd -P)

cleanup() {
	if [ "${WTREE_TUTORIAL_KEEP:-0}" = 1 ]; then
		printf 'Kept companion tutorial test directory: %s\n' "$test_root" >&2
		return
	fi
	case "$test_root" in
		"$temp_base"/wtree-companion.*) rm -rf "$test_root" ;;
		*) printf 'Refusing to remove unexpected test path: %s\n' "$test_root" >&2 ;;
	esac
}
trap cleanup EXIT HUP INT TERM

fail() {
	printf 'run-companion-commands: %s\n' "$*" >&2
	exit 1
}

step() {
	printf '==> %s\n' "$*"
}

run_quiet() {
	"$@" > "$test_root/last.stdout" 2> "$test_root/last.stderr" || {
		cat "$test_root/last.stdout" >&2
		cat "$test_root/last.stderr" >&2
		fail "command failed: $*"
	}
}

run_json() {
	run_quiet "$@"
	grep -Eq '^[{[]' "$test_root/last.stdout" || fail "command did not emit JSON: $*"
}

expect_failure() {
	needle=$1
	shift
	if "$@" > "$test_root/last.stdout" 2> "$test_root/last.stderr"; then
		fail "command unexpectedly succeeded: $*"
	fi
	grep -F "$needle" "$test_root/last.stdout" "$test_root/last.stderr" >/dev/null || {
		cat "$test_root/last.stdout" >&2
		cat "$test_root/last.stderr" >&2
		fail "failure did not contain '$needle': $*"
	}
}

fixture_git() {
	GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 \
		git -c core.hooksPath=/dev/null "$@"
}

fixture_publisher() {
	origin=$1
	publisher=$2
	fixture_git clone -q "$origin" "$publisher"
	fixture_git -C "$publisher" config user.name 'Wtree Tutorial'
	fixture_git -C "$publisher" config user.email tutorial@wtree.invalid
}

fetch_branch() {
	checkout=$1
	branch=$2
	fixture_git -C "$checkout" fetch --no-tags --no-recurse-submodules -- origin "+refs/heads/$branch:refs/remotes/origin/$branch" >/dev/null
	fixture_git -C "$checkout" branch --track "$branch" "origin/$branch" >/dev/null
}

assert_ids() {
	file=$1
	shift
	for id in "$@"; do
		grep -F "\"id\":\"$id\"" "$file" >/dev/null || fail "JSON omitted repository $id"
	done
}

lock_revision() {
	awk -v repository="$2" '$1 == repository ":" { found = 1; next } found && $1 == "revision:" { print $2; exit }' "$1"
}

export WTREE_DATA_HOME="$test_root/wtree-data"
mkdir -p "$test_root/bin"

step 'build the current wtree command'
(cd "$source_root" && go build -o "$test_root/bin/wtree" ./cmd/wtree)
wtree=$test_root/bin/wtree

step 'publish a v4 manifest with one companion in a spaced Unicode fixture'
fixture="$test_root/companion fixture-δ"
"$script_dir/setup-fixture.sh" "$fixture" >/dev/null
origins="$fixture/origins"
manifest="$fixture/project.wtree.yml"
root_publisher="$test_root/root-publisher"
fixture_publisher "$origins/acme-shop.git" "$root_publisher"
awk '
	$0 == "version: 2" { print "version: 4"; next }
	$0 == "    backend:" { backend = 1 }
	backend && $0 == "        default_branch: main" {
		print $0
		print "        companion: true"
		backend = 0
		next
	}
	{ print }
' "$root_publisher/project.wtree.yml" > "$root_publisher/project.wtree.yml.next"
mv "$root_publisher/project.wtree.yml.next" "$root_publisher/project.wtree.yml"
fixture_git -C "$root_publisher" add project.wtree.yml
fixture_git -C "$root_publisher" commit -q -m 'Adopt backend companion baseline'
fixture_git -C "$root_publisher" push -q origin main
cp "$root_publisher/project.wtree.yml" "$manifest"

destination_parent="$test_root/clone destination-δ"
mkdir -p "$destination_parent"
project="$destination_parent/acme-shop"
worktree_root="$fixture/worktrees"
run_json "$wtree" clone "$manifest" "$project" --worktree-root "$worktree_root" --dry-run --json
grep -F '"companion":true' "$test_root/last.stdout" >/dev/null || fail 'clone dry run omitted companion role'
run_json "$wtree" clone "$manifest" "$project" --worktree-root "$worktree_root" --json
grep -F 'companion: true' "$project/project.wtree.yml" >/dev/null || fail 'clone did not retain v4 companion manifest'
[ "$(fixture_git -C "$project/backend" rev-parse --abbrev-ref HEAD)" = main ] || fail 'default companion did not use its baseline'

step 'create independent ordinary and companion branches, then commit locally'
fetch_branch "$project" feature/customer-search
fetch_branch "$project/frontend" feature/customer-search
run_json "$wtree" create tutorial/companion --project "$project" --from feature/customer-search --dry-run --json
grep -F '"companion":true' "$test_root/last.stdout" >/dev/null || fail 'create plan omitted companion role'
run_quiet "$wtree" create tutorial/companion --project "$project" --from feature/customer-search
workspace=$("$wtree" path tutorial/companion --project "$project")
[ "$(fixture_git -C "$workspace" rev-parse --abbrev-ref HEAD)" = tutorial/companion ] || fail 'ordinary workspace branch was not created'
[ "$(fixture_git -C "$workspace/backend" rev-parse tutorial/companion)" = "$(fixture_git -C "$project/backend" rev-parse main)" ] || fail 'companion branch did not use its configured baseline'
[ "$(fixture_git -C "$workspace/frontend" rev-parse tutorial/companion)" = "$(fixture_git -C "$project/frontend" rev-parse feature/customer-search)" ] || fail 'ordinary branch ignored --from'
fixture_git -C "$workspace/backend" config user.name 'Wtree Tutorial'
fixture_git -C "$workspace/backend" config user.email tutorial@wtree.invalid
printf 'local companion commit\n' > "$workspace/backend/local-companion.txt"
fixture_git -C "$workspace/backend" add local-companion.txt
fixture_git -C "$workspace/backend" commit -q -m 'Keep a local companion commit'
run_json "$wtree" status tutorial/companion --project "$project" --json
grep -F '"headAdvanced":true' "$test_root/last.stdout" >/dev/null || fail 'status did not report local companion advancement'
run_quiet "$wtree" create tutorial/updateable --project "$project" --from feature/customer-search
updateable=$("$wtree" path tutorial/updateable --project "$project")
updateable_head_before=$(fixture_git -C "$updateable/backend" rev-parse HEAD)

step 'select every, ordinary-only, and one companion checkout with direct argv'
run_json "$wtree" exec --workspace tutorial/companion --project "$project" --json -- git rev-parse --is-inside-work-tree
assert_ids "$test_root/last.stdout" root backend frontend
run_json "$wtree" exec --workspace tutorial/companion --project "$project" --no-companions --json -- git rev-parse --is-inside-work-tree
assert_ids "$test_root/last.stdout" root frontend
if grep -F '"id":"backend"' "$test_root/last.stdout" >/dev/null; then
	fail 'ordinary-only exec included companion'
fi
run_json "$wtree" exec --workspace tutorial/companion --project "$project" --repository backend --json -- git rev-parse --is-inside-work-tree
assert_ids "$test_root/last.stdout" backend
if [ "$(grep -o '"id":"' "$test_root/last.stdout" | wc -l | tr -d ' ')" -ne 1 ]; then
	fail 'single-repository exec selected more than one checkout'
fi

step 'retain a local branch, update the active companion baseline once, then restore and delete'
run_quiet "$wtree" remove tutorial/companion --project "$project"
[ ! -e "$workspace/backend" ] || fail 'remove retained companion worktree'
backend_publisher="$test_root/backend-publisher"
fixture_publisher "$origins/java-backend.git" "$backend_publisher"
printf 'remote companion update\n' > "$backend_publisher/companion-update.txt"
fixture_git -C "$backend_publisher" add companion-update.txt
fixture_git -C "$backend_publisher" commit -q -m 'Advance companion baseline'
fixture_git -C "$backend_publisher" push -q origin main
remote_head=$(fixture_git -C "$backend_publisher" rev-parse HEAD)
run_json "$wtree" companion update backend --project "$project" --dry-run --json
grep -F '"operation":"companion-update"' "$test_root/last.stdout" >/dev/null || fail 'companion update dry run used the wrong envelope'
grep -F '"dryRun":true' "$test_root/last.stdout" >/dev/null || fail 'companion update dry run was not marked dry'
run_json "$wtree" companion update backend --project "$project" --json
[ "$(fixture_git -C "$project/backend" rev-parse HEAD)" = "$remote_head" ] || fail 'companion update did not advance active baseline'
grep -F '"kind":"baseline"' "$test_root/last.stdout" >/dev/null || fail 'companion update omitted baseline entry'
baseline_entry='"kind":"baseline"'
updateable_entry="\"kind\":\"workspace\",\"workspace\":\"tutorial/updateable\",\"branch\":\"tutorial/updateable\",\"previousHead\":\"$updateable_head_before\",\"resultingHead\":\"$remote_head\",\"action\":\"fast-forward\",\"status\":\"completed\""
case "$(cat "$test_root/last.stdout")" in
	*"$baseline_entry"*"$updateable_entry"*) ;;
	*) fail 'companion update did not order the exact clean workspace result after baseline' ;;
esac
[ "$(fixture_git -C "$updateable/backend" rev-parse HEAD)" = "$remote_head" ] || fail 'clean workspace backend did not advance'
run_json "$wtree" status tutorial/updateable --project "$project" --json
grep -F "\"expectedHead\":\"$remote_head\"" "$test_root/last.stdout" >/dev/null || fail 'workspace state did not persist updated head'
run_quiet "$wtree" checkout tutorial/companion --project "$project"
[ -f "$workspace/backend/local-companion.txt" ] || fail 'checkout did not restore the companion branch'
run_json "$wtree" delete tutorial/companion --project "$project" --force --json
[ ! -e "$workspace/backend" ] || fail 'delete retained the companion worktree'
fixture_git -C "$project/backend" show-ref --verify --quiet refs/heads/main || fail 'delete removed the configured companion baseline'

step 'include the companion in a release lock, then change only a future baseline'
companion_head=$(fixture_git -C "$project/backend" rev-parse HEAD)
run_json "$wtree" release lock companion-v1 --project "$project" --dry-run --json
assert_ids "$test_root/last.stdout" backend frontend
grep -F "\"id\":\"backend\",\"revision\":\"$companion_head\"" "$test_root/last.stdout" >/dev/null || fail 'release-lock dry run omitted backend exact companion revision'
run_json "$wtree" release lock companion-v1 --project "$project" --json
[ "$(lock_revision "$project/project.wtree.lock.yml" backend)" = "$companion_head" ] || fail 'release lock did not persist exact companion revision'
rm "$project/project.wtree.lock.yml"
fixture_git -C "$project/backend" branch tutorial/baseline main
baseline_head_before=$(fixture_git -C "$project/backend" rev-parse HEAD)
run_json "$wtree" repo branch backend tutorial/baseline --project "$project" --dry-run --json
grep -F '"operation":"repo-branch"' "$test_root/last.stdout" >/dev/null || fail 'repo branch dry run used the wrong envelope'
run_json "$wtree" repo branch backend tutorial/baseline --project "$project" --json
[ "$(fixture_git -C "$project/backend" rev-parse HEAD)" = "$baseline_head_before" ] || fail 'baseline change switched an existing checkout'
grep -A20 '^    backend:' "$project/project.wtree.yml" | grep -F 'default_branch: tutorial/baseline' >/dev/null || fail 'baseline change did not publish the requested future baseline'

step 'exercise documented invalid selector and non-companion boundaries'
expect_failure 'selectors are mutually exclusive' "$wtree" exec --project "$project" --no-companions --repository backend -- git status
expect_failure 'is not a companion' "$wtree" companion update frontend --project "$project" --json

printf 'Companion tutorial completed in hermetic fixture: %s\n' "$test_root"
