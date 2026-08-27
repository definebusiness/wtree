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

fetch_fixture_branch() {
	checkout=$1
	branch=$2
	tracking_ref=refs/remotes/origin/$branch
	if git -C "$checkout" show-ref --verify --quiet "$tracking_ref"; then
		fail "clone unexpectedly fetched $tracking_ref"
	fi
	GIT_TERMINAL_PROMPT=0 git -C "$checkout" -c core.hooksPath=/dev/null fetch --no-tags --no-recurse-submodules -- origin "+refs/heads/$branch:$tracking_ref" >/dev/null
}

fixture_git() {
	GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 \
		git -c core.hooksPath=/dev/null "$@"
}

fixture_publisher() {
	origin=$1
	publisher=$2
	fixture_git clone -q "$origin" "$publisher"
	fixture_git -C "$publisher" config user.name "Wtree Tutorial"
	fixture_git -C "$publisher" config user.email "tutorial@wtree.invalid"
}

fixture_tracking_ref() {
	checkout=$1
	branch=$2
	fixture_git -C "$checkout" fetch --no-tags --no-recurse-submodules -- origin "+refs/heads/$branch:refs/remotes/origin/$branch" >/dev/null
}

snapshot_composition_authority() {
	snapshot=$1
	: > "$snapshot"
	for repository in root backend frontend; do
		case "$repository" in
			root) checkout=$project ;;
			*) checkout=$project/$repository ;;
		esac
		printf '[checkout %s]\n' "$repository" >> "$snapshot"
		fixture_git -C "$checkout" rev-parse HEAD >> "$snapshot"
		fixture_git -C "$checkout" show-ref --head >> "$snapshot"
		fixture_git -C "$checkout" status --porcelain=v1 >> "$snapshot"
		fixture_git -C "$checkout" write-tree >> "$snapshot"
		fixture_git -C "$checkout" config --list --show-origin | sort >> "$snapshot"
		index=$(fixture_git -C "$checkout" rev-parse --git-path index)
		case "$index" in
			/*) ;;
			*) index=$checkout/$index ;;
		esac
		if [ -f "$index" ]; then
			printf '[index present]\n' >> "$snapshot"
			cat "$index" >> "$snapshot"
		else
			printf '[index absent]\n' >> "$snapshot"
		fi
		fetch_head=$(fixture_git -C "$checkout" rev-parse --git-path FETCH_HEAD)
		case "$fetch_head" in
			/*) ;;
			*) fetch_head=$checkout/$fetch_head ;;
		esac
		if [ -f "$fetch_head" ]; then
			cat "$fetch_head" >> "$snapshot"
		fi
	done
	for origin in acme-shop java-backend web-frontend; do
		printf '[origin %s]\n' "$origin" >> "$snapshot"
		fixture_git -C "$origins/$origin.git" show-ref --head >> "$snapshot"
	done
}

assert_same_composition_authority() {
	before=$1
	after=$2
	if ! cmp -s "$before" "$after"; then
		diff -u "$before" "$after" >&2 || true
		fail "composition authority changed unexpectedly"
	fi
}

json_repository_objects() {
	json_file=$1
	awk '
		{
			marker = "\"repositories\":["
			start = index($0, marker)
			if (start == 0) exit 2
			value = substr($0, start + length(marker))
			depth = 0
			quoted = 0
			escaped = 0
			object = ""
			for (position = 1; position <= length(value); position++) {
				character = substr(value, position, 1)
				if (depth > 0) object = object character
				if (quoted) {
					if (escaped) escaped = 0
					else if (character == "\\") escaped = 1
					else if (character == "\"") quoted = 0
					continue
				}
				if (character == "\"") {
					quoted = 1
					continue
				}
				if (character == "{") {
					if (depth == 0) object = character
					depth++
					continue
				}
				if (character == "}") {
					depth--
					if (depth == 0) print object
					continue
				}
				if (depth == 0 && character == "]") exit
			}
			if (depth != 0 || quoted) exit 3
		}
	' "$json_file"
}

json_repository_object() {
	json_file=$1
	want_id=$2
	object=$(json_repository_objects "$json_file" | awk -v want_id="$want_id" '
		index($0, "\"id\":\"" want_id "\"") {
			matches++
			result = $0
		}
		END { if (matches != 1) exit 1; print result }
	') || fail "JSON did not contain exactly one repository object for $want_id"
	printf '%s\n' "$object"
}

assert_json_ids() {
	json_file=$1
	shift
	want=$(printf '%s ' "$@" | sed 's/ $//')
	got=$(json_repository_objects "$json_file" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | tr '\n' ' ' | sed 's/ $//')
	[ "$got" = "$want" ] || fail "JSON repository IDs = '$got', want '$want'"
}

assert_json_repository_contains() {
	json_file=$1
	repository_id=$2
	needle=$3
	object=$(json_repository_object "$json_file" "$repository_id")
	case "$object" in
		*"$needle"*) ;;
		*) fail "JSON repository $repository_id did not contain $needle" ;;
	esac
}

assert_json_repository_absent() {
	json_file=$1
	repository_id=$2
	needle=$3
	object=$(json_repository_object "$json_file" "$repository_id")
	case "$object" in
		*"$needle"*) fail "JSON repository $repository_id unexpectedly contained $needle" ;;
		*) ;;
	esac
}

assert_doctor_known_rows() {
	json_file=$1
	assert_json_ids "$json_file" root backend frontend
	assert_json_repository_contains "$json_file" root '"mount":"."'
	assert_json_repository_contains "$json_file" root '"status":"known"'
	assert_json_repository_contains "$json_file" backend '"parentId":"root"'
	assert_json_repository_contains "$json_file" backend '"mount":"backend"'
	assert_json_repository_contains "$json_file" backend '"status":"known"'
	assert_json_repository_contains "$json_file" frontend '"parentId":"root"'
	assert_json_repository_contains "$json_file" frontend '"mount":"frontend"'
	assert_json_repository_contains "$json_file" frontend '"status":"known"'
}

assert_status_current_row() {
	json_file=$1
	repository_id=$2
	mount=$3
	assert_json_repository_contains "$json_file" "$repository_id" "\"mount\":\"$mount\""
	assert_json_repository_contains "$json_file" "$repository_id" '"clean":true'
	assert_json_repository_contains "$json_file" "$repository_id" '"upstream":true'
	assert_json_repository_contains "$json_file" "$repository_id" '"status":"clean"'
	assert_json_repository_absent "$json_file" "$repository_id" '"ahead":'
	assert_json_repository_absent "$json_file" "$repository_id" '"behind":'
}

assert_status_current_rows() {
	json_file=$1
	assert_json_ids "$json_file" root backend frontend
	assert_status_current_row "$json_file" root .
	assert_json_repository_contains "$json_file" backend '"parentId":"root"'
	assert_status_current_row "$json_file" backend backend
	assert_json_repository_contains "$json_file" frontend '"parentId":"root"'
	assert_status_current_row "$json_file" frontend frontend
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
for command in project init clone config create checkout import list status path repo remove delete doctor update exec fetch push; do
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
grep -Fx '/components/library/' "$init_project/.gitignore" >/dev/null || fail "init did not protect the grouped nested mount"
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
origins=$test_root/consumer/origins
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
assert_status_current_rows "$test_root/last.stdout"
run_json "$wtree" doctor --json
assert_doctor_known_rows "$test_root/last.stdout"
run_quiet "$wtree" path default
run_quiet "$wtree" repo path backend
run_json "$wtree" repo get frontend --json
cd "$project/backend"
run_quiet "$wtree" status
cd "$project"

step "exercise the default composition update, exec, fetch, and push loop"
root_head_before=$(fixture_git -C "$project" rev-parse HEAD)
backend_head_before=$(fixture_git -C "$project/backend" rev-parse HEAD)
frontend_head_before=$(fixture_git -C "$project/frontend" rev-parse HEAD)
fixture_publisher "$origins/acme-shop.git" "$test_root/root-publisher"
printf 'Remote root update for the all-commands tutorial.\n' > "$test_root/root-publisher/tutorial-update.txt"
fixture_git -C "$test_root/root-publisher" add tutorial-update.txt
fixture_git -C "$test_root/root-publisher" commit -q -m "Advance root for tutorial update"
fixture_git -C "$test_root/root-publisher" push -q origin main
root_remote_head=$(fixture_git -C "$test_root/root-publisher" rev-parse HEAD)
# update preflight proves ancestry from local configured-ref facts. Refresh only
# the declared root tracking ref before taking the dry-run authority snapshot.
fixture_tracking_ref "$project" main
snapshot_composition_authority "$test_root/update-dry.before"
run_json "$wtree" update --dry-run --json
assert_json_ids "$test_root/last.stdout" root backend frontend
snapshot_composition_authority "$test_root/update-dry.after"
assert_same_composition_authority "$test_root/update-dry.before" "$test_root/update-dry.after"
run_json "$wtree" update --json
[ "$(fixture_git -C "$project" rev-parse HEAD)" = "$root_remote_head" ] || fail "update did not advance root to origin/main"
[ "$(fixture_git -C "$project/backend" rev-parse HEAD)" = "$backend_head_before" ] || fail "update changed backend unexpectedly"
[ "$(fixture_git -C "$project/frontend" rev-parse HEAD)" = "$frontend_head_before" ] || fail "update changed frontend unexpectedly"
run_json "$wtree" doctor --json
assert_doctor_known_rows "$test_root/last.stdout"
run_json "$wtree" status --json
assert_status_current_rows "$test_root/last.stdout"
run_json "$wtree" exec --json -- git rev-parse --is-inside-work-tree
assert_json_ids "$test_root/last.stdout" root backend frontend
for repository in root backend frontend; do
	assert_json_repository_contains "$test_root/last.stdout" "$repository" '"status":"completed"'
	assert_json_repository_contains "$test_root/last.stdout" "$repository" '"stdout":"true\n"'
done
snapshot_composition_authority "$test_root/push.before"
run_json "$wtree" push --json
assert_json_ids "$test_root/last.stdout" root backend frontend
for repository in root backend frontend; do
	assert_json_repository_contains "$test_root/last.stdout" "$repository" '"status":"ready"'
done
snapshot_composition_authority "$test_root/push.after"
assert_same_composition_authority "$test_root/push.before" "$test_root/push.after"

fixture_publisher "$origins/java-backend.git" "$test_root/backend-publisher"
printf 'Remote backend update for the all-commands tutorial.\n' > "$test_root/backend-publisher/tutorial-fetch.txt"
fixture_git -C "$test_root/backend-publisher" add tutorial-fetch.txt
fixture_git -C "$test_root/backend-publisher" commit -q -m "Advance backend for tutorial fetch"
fixture_git -C "$test_root/backend-publisher" push -q origin main
backend_remote_head=$(fixture_git -C "$test_root/backend-publisher" rev-parse HEAD)
backend_local_before=$(fixture_git -C "$project/backend" rev-parse HEAD)
backend_tracking_before=$(fixture_git -C "$project/backend" rev-parse refs/remotes/origin/main)
root_tracking_before=$(fixture_git -C "$project" rev-parse refs/remotes/origin/main)
frontend_tracking_before=$(fixture_git -C "$project/frontend" rev-parse refs/remotes/origin/main)
snapshot_composition_authority "$test_root/fetch-dry.before"
run_json "$wtree" fetch --dry-run --json
assert_json_ids "$test_root/last.stdout" root backend frontend
snapshot_composition_authority "$test_root/fetch-dry.after"
assert_same_composition_authority "$test_root/fetch-dry.before" "$test_root/fetch-dry.after"
run_json "$wtree" fetch --json
assert_json_ids "$test_root/last.stdout" root backend frontend
[ "$(fixture_git -C "$project/backend" rev-parse HEAD)" = "$backend_local_before" ] || fail "fetch moved backend HEAD"
[ "$(fixture_git -C "$project/backend" rev-parse refs/remotes/origin/main)" = "$backend_remote_head" ] || fail "fetch did not refresh backend origin/main"
[ "$backend_tracking_before" != "$backend_remote_head" ] || fail "backend publisher did not advance origin/main"
[ "$(fixture_git -C "$project" rev-parse refs/remotes/origin/main)" = "$root_tracking_before" ] || fail "fetch changed root origin/main unexpectedly"
[ "$(fixture_git -C "$project/frontend" rev-parse refs/remotes/origin/main)" = "$frontend_tracking_before" ] || fail "fetch changed frontend origin/main unexpectedly"
run_json "$wtree" status --json
assert_json_ids "$test_root/last.stdout" root backend frontend
assert_status_current_row "$test_root/last.stdout" root .
assert_json_repository_contains "$test_root/last.stdout" backend '"parentId":"root"'
assert_json_repository_contains "$test_root/last.stdout" backend '"mount":"backend"'
assert_json_repository_contains "$test_root/last.stdout" backend '"clean":true'
assert_json_repository_contains "$test_root/last.stdout" backend '"upstream":true'
assert_json_repository_contains "$test_root/last.stdout" backend '"status":"clean"'
assert_json_repository_contains "$test_root/last.stdout" backend '"behind":1'
assert_json_repository_absent "$test_root/last.stdout" backend '"ahead":'
assert_json_repository_contains "$test_root/last.stdout" frontend '"parentId":"root"'
assert_status_current_row "$test_root/last.stdout" frontend frontend
run_json "$wtree" update --json
[ "$(fixture_git -C "$project/backend" rev-parse HEAD)" = "$backend_remote_head" ] || fail "update did not consume backend origin/main"
for checkout in "$project" "$project/backend" "$project/frontend"; do
	[ -z "$(fixture_git -C "$checkout" status --porcelain)" ] || fail "update left checkout dirty: $checkout"
	[ "$(fixture_git -C "$checkout" rev-parse HEAD)" = "$(fixture_git -C "$checkout" rev-parse refs/remotes/origin/main)" ] || fail "update left checkout behind: $checkout"
done
run_json "$wtree" status --json
assert_status_current_rows "$test_root/last.stdout"
run_json "$wtree" doctor --json
assert_doctor_known_rows "$test_root/last.stdout"

step "exercise checkout success and missing-branch preflight"
expect_failure 'does not exist' "$wtree" checkout feature/customer-search --dry-run
for checkout in "$project" "$project/backend" "$project/frontend"; do
	fetch_fixture_branch "$checkout" feature/customer-search
	git -C "$checkout" branch --track feature/customer-search origin/feature/customer-search >/dev/null
done
run_json "$wtree" checkout feature/customer-search --dry-run --json
run_quiet "$wtree" checkout feature/customer-search --verbose

for checkout in "$project" "$project/backend"; do
	fetch_fixture_branch "$checkout" release/2026-q3
	git -C "$checkout" branch --track release/2026-q3 origin/release/2026-q3 >/dev/null
done
expect_failure 'repository "frontend"' "$wtree" checkout release/2026-q3 --dry-run
fetch_fixture_branch "$project/frontend" experiment/dark-navigation
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

step "exercise a plain logical-root forest with a grouped non-dot base"
publisher_data=$test_root/forest-publisher-data
consumer_data=$test_root/forest-consumer-data
forest_fixture=$test_root/forest
forest_source=$("$script_dir/setup-forest-fixture.sh" "$forest_fixture")
forest_api=$forest_source/services/api
forest_shared=$forest_api/components/shared
forest_web=$forest_source/clients/web
original_data=$WTREE_DATA_HOME
export WTREE_DATA_HOME=$publisher_data

run_json "$wtree" init "$forest_source" --base-repository api --worktree-root "$forest_fixture/publisher-worktrees" --dry-run --json
grep -F '"baseRepository":"api"' "$test_root/last.stdout" >/dev/null || fail "forest init omitted baseRepository"
grep -F '"logicalRoot":' "$test_root/last.stdout" >/dev/null || fail "forest init omitted logicalRoot"
run_quiet "$wtree" init "$forest_source" --base-repository api --worktree-root "$forest_fixture/publisher-worktrees"
[ -f "$forest_api/.wtree.yml" ] || fail "forest base does not own .wtree.yml"
[ -f "$forest_api/project.wtree.yml" ] || fail "forest base does not own project.wtree.yml"
[ ! -e "$forest_source/.wtree.yml" ] || fail "plain logical root owns unexpected metadata"
[ ! -e "$forest_web/.wtree.yml" ] || fail "sibling owns unexpected metadata"
[ ! -e "$forest_shared/.wtree.yml" ] || fail "child owns unexpected metadata"
grep -Fx '/.wtree.yml' "$forest_api/.gitignore" >/dev/null || fail "base does not ignore local config"
grep -Fx '/components/shared/' "$forest_api/.gitignore" >/dev/null || fail "base does not protect its direct child mount"
run_json "$wtree" project list --json

git -C "$forest_api" add .gitignore project.wtree.yml
git -C "$forest_api" commit -q -m "Publish forest manifest"
git -C "$forest_api" push -q origin main

export WTREE_DATA_HOME=$consumer_data
forest_clone=$forest_fixture/consumer-forest
forest_worktrees=$forest_fixture/consumer-worktrees
run_json "$wtree" clone "$forest_api/project.wtree.yml" "$forest_clone" --worktree-root "$forest_worktrees" --dry-run --json
grep -F '"baseRepository":"api"' "$test_root/last.stdout" >/dev/null || fail "forest clone plan omitted baseRepository"
run_quiet "$wtree" clone "$forest_api/project.wtree.yml" "$forest_clone" --worktree-root "$forest_worktrees"
for checkout in \
	"$forest_clone/services/api" \
	"$forest_clone/services/api/components/shared" \
	"$forest_clone/clients/web"; do
	[ -e "$checkout/.git" ] || fail "forest clone missed checkout $checkout"
done
for pair in \
	"$forest_api|$forest_clone/services/api" \
	"$forest_shared|$forest_clone/services/api/components/shared" \
	"$forest_web|$forest_clone/clients/web"; do
	source_checkout=${pair%%|*}
	cloned_checkout=${pair#*|}
	[ "$(git -C "$source_checkout" rev-parse HEAD)" = "$(git -C "$cloned_checkout" rev-parse HEAD)" ] || fail "forest clone recorded the wrong execution-time HEAD for $cloned_checkout"
done
[ -f "$forest_clone/services/api/.wtree.yml" ] || fail "cloned forest base does not own local config"
[ ! -e "$forest_clone/.wtree.yml" ] || fail "cloned plain logical root owns unexpected metadata"

cd "$forest_clone/services/api/components/shared"
run_json "$wtree" status default --json
grep -F '"baseRepository":"api"' "$test_root/last.stdout" >/dev/null || fail "nested status omitted forest base"
forest_status=$(tr -d '\n' < "$test_root/last.stdout")
case "$forest_status" in
	*'"id":"api"'*'"id":"web"'*'"id":"shared"'*) ;;
	*) fail "forest status repository order is not stable parent-first" ;;
esac
run_quiet "$wtree" path default
[ "$(wc -l < "$test_root/last.stdout" | tr -d ' ')" -eq 1 ] || fail "workspace path output is not scalar"
run_quiet "$wtree" repo path web
[ "$(wc -l < "$test_root/last.stdout" | tr -d ' ')" -eq 1 ] || fail "repository path output is not scalar"
cd "$forest_clone/clients/web"
run_quiet "$wtree" status default
run_json "$wtree" project list --json
cd "$forest_clone"

run_json "$wtree" create tutorial/forest --from main --dry-run --json
run_quiet "$wtree" create tutorial/forest --from main
forest_created=$("$wtree" path tutorial/forest)
for checkout in \
	"$forest_created/services/api" \
	"$forest_created/services/api/components/shared" \
	"$forest_created/clients/web"; do
	[ -e "$checkout/.git" ] || fail "forest create missed checkout $checkout"
done
run_json "$wtree" remove tutorial/forest --dry-run --json
run_quiet "$wtree" remove tutorial/forest
run_quiet "$wtree" doctor tutorial/forest
run_quiet "$wtree" checkout tutorial/forest
run_quiet "$wtree" delete tutorial/forest
[ ! -e "$forest_created/services/api" ] || fail "forest delete retained a managed checkout"

for checkout in \
	"$forest_clone/services/api" \
	"$forest_clone/services/api/components/shared" \
	"$forest_clone/clients/web"; do
	git -C "$checkout" branch tutorial/import main
done
forest_import=$forest_fixture/manual-forest
mkdir -p "$forest_import/services" "$forest_import/clients"
git -C "$forest_clone/services/api" worktree add -q "$forest_import/services/api" tutorial/import
mkdir -p "$forest_import/services/api/components"
git -C "$forest_clone/services/api/components/shared" worktree add -q "$forest_import/services/api/components/shared" tutorial/import
git -C "$forest_clone/clients/web" worktree add -q "$forest_import/clients/web" tutorial/import
cd "$forest_import/services/api/components/shared"
run_json "$wtree" import "$forest_import" --project "$forest_clone" --name tutorial/import --dry-run --json
run_quiet "$wtree" import "$forest_import" --project "$forest_clone" --name tutorial/import
run_quiet "$wtree" path tutorial/import
run_quiet "$wtree" repo path api
run_json "$wtree" status tutorial/import --json
cd "$forest_clone"
run_quiet "$wtree" delete tutorial/import
[ ! -e "$forest_import/services/api" ] || fail "forest import delete retained a managed checkout"

export WTREE_DATA_HOME=$original_data
cd "$project"

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
