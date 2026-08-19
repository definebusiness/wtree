#!/usr/bin/env sh
# Run the all-commands tutorial as an isolated end-to-end test.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
source_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
expected=$script_dir/expected/all-commands-final-state.txt
temp_base=$(CDPATH='' cd -- "${TMPDIR:-/tmp}" && pwd -P)
test_root=$(mktemp -d "$temp_base/wtree-all-commands.XXXXXX")
test_root=$(CDPATH='' cd -- "$test_root" && pwd -P)

cleanup() {
	if [ "${WTREE_TUTORIAL_KEEP:-0}" = 1 ]; then
		printf 'Kept tutorial test directory: %s\n' "$test_root" >&2
		return
	fi
	case "$test_root" in
		"$temp_base"/wtree-all-commands.*) rm -rf "$test_root" ;;
		*) printf 'Refusing to remove unexpected test path: %s\n' "$test_root" >&2 ;;
	esac
}

trap cleanup EXIT HUP INT TERM

fail() {
	printf 'run-all-commands: %s\n' "$*" >&2
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
	expected_text=$1
	shift
	if "$@" > "$test_root/last.stdout" 2> "$test_root/last.stderr"; then
		fail "command unexpectedly succeeded: $*"
	fi
	if ! grep -F "$expected_text" "$test_root/last.stdout" "$test_root/last.stderr" >/dev/null; then
		cat "$test_root/last.stdout" >&2
		cat "$test_root/last.stderr" >&2
		fail "failure did not contain '$expected_text': $*"
	fi
}

export WTREE_DATA_HOME=$test_root/wtree-data
mkdir -p "$test_root/bin"

step "build the current wtree command"
(cd "$source_root" && go build -o "$test_root/bin/wtree" ./cmd/wtree)
wtree=$test_root/bin/wtree

step "exercise terminal help and version commands"
run_quiet "$wtree" --version
run_quiet "$wtree" --help
run_quiet "$wtree" --how-to
for command in project init clone config create checkout import list status path repo remove delete doctor; do
	run_quiet "$wtree" "$command" --help
done

step "initialize and publish a maintainer checkout"
"$script_dir/setup-init-fixture.sh" "$test_root/init-unregister" >/dev/null
init_project=$test_root/init-unregister/maintainer-app
run_json "$wtree" init "$init_project" --worktree-root "$test_root/init-worktrees" --dry-run --json
[ ! -e "$init_project/.wtree.yml" ] || fail "init dry-run wrote .wtree.yml"
run_quiet "$wtree" init "$init_project" --worktree-root "$test_root/init-worktrees"
[ -f "$init_project/.wtree.yml" ] || fail "init did not write .wtree.yml"
[ -f "$init_project/project.wtree.yml" ] || fail "init did not write the portable manifest"
grep -Fx '/library/' "$init_project/.gitignore" >/dev/null || fail "init did not protect the nested mount"
init_id=$(awk '$1 == "id:" { print $2; exit }' "$init_project/.wtree.yml")
[ -n "$init_id" ] || fail "could not read initialized project ID"
run_json "$wtree" project list --data-dir "$WTREE_DATA_HOME" --json
expect_failure 'not objectively prunable' "$wtree" project prune "$init_id" --data-dir "$WTREE_DATA_HOME" --dry-run
run_json "$wtree" project unregister "$init_id" --data-dir "$WTREE_DATA_HOME" --dry-run --json
run_quiet "$wtree" project unregister "$init_id" --data-dir "$WTREE_DATA_HOME"

step "diagnose and prune a stale project registration"
"$script_dir/setup-init-fixture.sh" "$test_root/init-prune" >/dev/null
prune_project=$test_root/init-prune/maintainer-app
run_quiet "$wtree" init "$prune_project" --worktree-root "$test_root/prune-worktrees"
prune_id=$(awk '$1 == "id:" { print $2; exit }' "$prune_project/.wtree.yml")
mv "$prune_project/.wtree.yml" "$prune_project/.wtree.yml.saved"
run_json "$wtree" project list --data-dir "$WTREE_DATA_HOME" --json
run_json "$wtree" project prune "$prune_id" --data-dir "$WTREE_DATA_HOME" --dry-run --json
run_quiet "$wtree" project prune "$prune_id" --data-dir "$WTREE_DATA_HOME"
mv "$prune_project/.wtree.yml.saved" "$prune_project/.wtree.yml"

step "clone the portable three-repository project"
"$script_dir/setup-fixture.sh" "$test_root/consumer" >/dev/null
manifest=$test_root/consumer/project.wtree.yml
project=$test_root/consumer/acme-shop
worktree_root=$test_root/consumer/worktrees
run_json "$wtree" clone "$manifest" "$project" --worktree-root "$worktree_root" --dry-run --json
[ ! -e "$project" ] || fail "clone dry-run created its destination"
run_quiet "$wtree" clone "$manifest" "$project" --worktree-root "$worktree_root" --verbose
expect_failure 'already exists' "$wtree" clone "$manifest" "$project" --worktree-root "$worktree_root"

step "inspect project and project-scoped configuration"
cd "$project"
run_quiet "$wtree" project list --data-dir "$WTREE_DATA_HOME"
run_json "$wtree" project list --data-dir "$WTREE_DATA_HOME" --json
run_quiet "$wtree" config get worktrees.root --project
run_json "$wtree" config list --project --json
run_quiet "$wtree" config set worktrees.root "$test_root/consumer/alternate-worktrees" --project
run_quiet "$wtree" config get worktrees.root --project
run_quiet "$wtree" config unset worktrees.root --project
run_quiet "$wtree" config set worktrees.root "$worktree_root" --project

step "inspect the default workspace from root and nested contexts"
run_quiet "$wtree" list
run_json "$wtree" list --json
run_quiet "$wtree" status
run_json "$wtree" status default --json
run_quiet "$wtree" path default
run_quiet "$wtree" repo path backend
run_json "$wtree" repo get frontend --json
cd "$project/backend"
run_quiet "$wtree" status
cd "$project"

step "exercise checkout success and missing-branch preflight"
expect_failure 'does not exist' "$wtree" checkout feature/customer-search --dry-run
for checkout in "$project" "$project/backend" "$project/frontend"; do
	git -C "$checkout" branch --track feature/customer-search origin/feature/customer-search >/dev/null
done
run_json "$wtree" checkout feature/customer-search --dry-run --json
run_quiet "$wtree" checkout feature/customer-search --verbose

for checkout in "$project" "$project/backend"; do
	git -C "$checkout" branch --track release/2026-q3 origin/release/2026-q3 >/dev/null
done
expect_failure 'repository "frontend"' "$wtree" checkout release/2026-q3 --dry-run
git -C "$project/frontend" branch --track experiment/dark-navigation origin/experiment/dark-navigation >/dev/null
expect_failure 'repository "root"' "$wtree" checkout experiment/dark-navigation --dry-run
expect_failure 'does not exist' "$wtree" checkout feature/does-not-exist --dry-run
expect_failure 'already exists' "$wtree" create feature/customer-search --dry-run

step "create, remove, restore, diagnose, and delete custom mounts"
run_json "$wtree" create tutorial/custom --from main --mount backend=api --mount frontend=web --dry-run --json
run_quiet "$wtree" create tutorial/custom --from main --mount backend=api --mount frontend=web --verbose
custom_path=$("$wtree" path tutorial/custom)
cd "$custom_path/api"
run_quiet "$wtree" repo path frontend
run_quiet "$wtree" status
cd "$project"
run_json "$wtree" remove tutorial/custom --dry-run --json
run_quiet "$wtree" remove tutorial/custom --verbose
run_quiet "$wtree" doctor tutorial/custom
run_json "$wtree" doctor tutorial/custom --fix --dry-run --json
run_json "$wtree" checkout tutorial/custom --dry-run --json
run_quiet "$wtree" checkout tutorial/custom
run_quiet "$wtree" doctor tutorial/custom --fix
run_json "$wtree" delete tutorial/custom --dry-run --json
run_quiet "$wtree" delete tutorial/custom --verbose

step "exercise dirty-worktree safety and its narrow force override"
run_quiet "$wtree" create tutorial/dirty --from main
dirty_path=$("$wtree" path tutorial/dirty)
printf '\nUncommitted tutorial change.\n' >> "$dirty_path/README.md"
expect_failure 'dirty' "$wtree" remove tutorial/dirty
run_json "$wtree" remove tutorial/dirty --force --dry-run --json
run_quiet "$wtree" remove tutorial/dirty --force
run_quiet "$wtree" checkout tutorial/dirty
run_quiet "$wtree" delete tutorial/dirty

step "exercise unmerged-branch delete safety and force override"
run_quiet "$wtree" create tutorial/unmerged --from main
unmerged_path=$("$wtree" path tutorial/unmerged)
git -C "$unmerged_path" config user.name "Wtree Tutorial"
git -C "$unmerged_path" config user.email "tutorial@wtree.invalid"
printf 'unmerged tutorial commit\n' > "$unmerged_path/unmerged.txt"
git -C "$unmerged_path" add unmerged.txt
git -C "$unmerged_path" commit -q -m "Create an unmerged tutorial commit"
expect_failure 'not merged' "$wtree" delete tutorial/unmerged
run_json "$wtree" delete tutorial/unmerged --force --dry-run --json
run_quiet "$wtree" delete tutorial/unmerged --force

step "import complete and partial manually assembled workspaces"
for checkout in "$project" "$project/backend" "$project/frontend"; do
	git -C "$checkout" branch manual/full main
done
manual_full=$test_root/consumer/manual-full
git -C "$project" worktree add -q "$manual_full" manual/full
git -C "$project/backend" worktree add -q "$manual_full/api" manual/full
git -C "$project/frontend" worktree add -q "$manual_full/web" manual/full
run_json "$wtree" import "$manual_full" --project "$project" --name manual/full --dry-run --json
run_quiet "$wtree" import "$manual_full" --project "$project" --name manual/full
cd "$manual_full/api"
run_quiet "$wtree" repo get backend
cd "$project"
run_quiet "$wtree" delete manual/full

for checkout in "$project" "$project/backend"; do
	git -C "$checkout" branch manual/partial main
done
manual_partial=$test_root/consumer/manual-partial
git -C "$project" worktree add -q "$manual_partial" manual/partial
git -C "$project/backend" worktree add -q "$manual_partial/api" manual/partial
expect_failure 'missing frontend' "$wtree" import "$manual_partial" --project "$project" --name manual/partial --dry-run
run_json "$wtree" import "$manual_partial" --project "$project" --name manual/partial --allow-partial --dry-run --json
run_quiet "$wtree" import "$manual_partial" --project "$project" --name manual/partial --allow-partial
run_json "$wtree" status manual/partial --json
run_quiet "$wtree" doctor manual/partial
expect_failure 'is partial' "$wtree" delete manual/partial

step "compare the normalized final state"
actual=$test_root/all-commands-final-state.txt
{
	printf '%s\n' '[projects]'
	"$wtree" project list --data-dir "$WTREE_DATA_HOME"
	printf '%s\n' '[workspaces]'
	"$wtree" list --project "$project"
	printf '%s\n' '[default-status]'
	"$wtree" status default --project "$project"
	printf '%s\n' '[feature-status]'
	"$wtree" status feature/customer-search --project "$project"
	printf '%s\n' '[branches]'
	for repository in root backend frontend; do
		case "$repository" in
			root) checkout=$project ;;
			*) checkout=$project/$repository ;;
		esac
		printf '%s:' "$repository"
		git -C "$checkout" for-each-ref --format='%(refname:short)' refs/heads | sort | tr '\n' ',' | sed 's/,$//'
		printf '\n'
	done
	printf '%s\n' '[paths]'
	printf 'default=%s\n' "$(test -d "$project" && printf present || printf missing)"
	search_path=$("$wtree" path feature/customer-search --project "$project")
	printf 'feature/customer-search=%s\n' "$(test -d "$search_path" && printf present || printf missing)"
	printf 'tutorial/custom=%s\n' "$(test -e "$custom_path" && printf present || printf absent)"
	printf 'tutorial/unmerged=%s\n' "$(test -e "$unmerged_path" && printf present || printf absent)"
	printf 'manual/full=%s\n' "$(test -e "$manual_full" && printf present || printf absent)"
	printf 'manual/partial=%s\n' "$(test -e "$manual_partial" && printf present || printf absent)"
} | sed "s|$test_root|<TEST_ROOT>|g; s|/private<TEST_ROOT>|<TEST_ROOT>|g" > "$actual"

if ! diff -u "$expected" "$actual"; then
	fail "final state differs from $expected"
fi

printf 'All-command tutorial end-to-end test passed.\n'
