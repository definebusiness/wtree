# `wtree` all-commands tutorial

This tutorial exercises every `wtree` command in the situations users are
most likely to encounter: publishing with `init`, consuming with `clone`,
inspecting configuration and registry state, creating and checking out
workspaces, importing complete and partial workspaces, handling dirty
worktrees, retaining and restoring state, diagnosing it, and deleting it.

The commands form one ordered scenario. The automated counterpart is
[`run-all-commands.sh`](run-all-commands.sh); it runs the same lifecycle in an
isolated temporary directory and compares the normalized end result with
[`expected/all-commands-final-state.txt`](expected/all-commands-final-state.txt).

## Command coverage

| Command | Situations covered |
|---|---|
| `wtree init` | dry-run, JSON, nested discovery, generated ignore protection, publication output |
| `wtree clone` | dry-run, JSON, execution, explicit destination/root, destination conflict |
| `wtree project list` | healthy, empty, and stale registries; text and JSON |
| `wtree project prune` | refusal for a healthy registration, dry-run, stale removal |
| `wtree project unregister` | dry-run and intentional registry-only removal |
| `wtree config get/set/unset/list` | project scope, precedence, text and JSON |
| `wtree create` | explicit base, custom mounts, dry-run, JSON, progress, name conflict |
| `wtree checkout` | missing local branch, existing branch, retained-state restoration |
| `wtree import` | custom mounts by Git identity, complete import, rejected and allowed partial import |
| `wtree list/status/path` | current and named workspace lookup, text and JSON, explicit project context |
| `wtree repo path/get` | root and nested context, text and JSON |
| `wtree remove` | dry-run, retained state, dirty refusal, narrow `--force` override |
| `wtree delete` | dry-run, complete deletion, partial-workspace refusal |
| `wtree doctor` | healthy checkout, retained checkout, partial checkout, fix dry-run |
| root/help commands | `--version`, `--help`, `--how-to`, and command-specific help |

`--data-dir` is shown where a command must be independent of ambient user
state. `--project` is shown when the current directory is outside the selected
project. Unsupported flag combinations are intentional errors and are best
checked with the relevant command's `--help` output.

## 1. Build an isolated tutorial command

Start at the source repository root:

```sh
export WTREE_SOURCE_ROOT="$PWD"
export WTREE_ALL_COMMANDS="$(mktemp -d "${TMPDIR:-/tmp}/wtree-all-commands.XXXXXX")"
export WTREE_ALL_COMMANDS="$(cd "$WTREE_ALL_COMMANDS" && pwd -P)"
export WTREE_DATA_HOME="$WTREE_ALL_COMMANDS/wtree-data"
mkdir -p "$WTREE_ALL_COMMANDS/bin"
go build -o "$WTREE_ALL_COMMANDS/bin/wtree" ./cmd/wtree
export PATH="$WTREE_ALL_COMMANDS/bin:$PATH"
```

Inspect the installed entry points:

```sh
wtree --version
wtree --help
wtree --how-to
wtree clone --help
wtree project --help
wtree config --help
```

Use `wtree <command> --help` for each remaining command as needed. Help and
how-to flags are terminal: do not combine them with an operation.

## 2. Publisher situation: initialize existing repositories

Create a root checkout with an independently pushed nested library:

```sh
cd "$WTREE_SOURCE_ROOT"
./tutorial/setup-init-fixture.sh "$WTREE_ALL_COMMANDS/init-unregister"
export WTREE_INIT_PROJECT="$WTREE_ALL_COMMANDS/init-unregister/maintainer-app"
```

Preview discovery. The dry run must not create either configuration file:

```sh
wtree init "$WTREE_INIT_PROJECT" \
  --worktree-root "$WTREE_ALL_COMMANDS/init-worktrees" \
  --dry-run --json
test ! -e "$WTREE_INIT_PROJECT/.wtree.yml"
```

Initialize the project:

```sh
wtree init "$WTREE_INIT_PROJECT" \
  --worktree-root "$WTREE_ALL_COMMANDS/init-worktrees"
sed -n '1,200p' "$WTREE_INIT_PROJECT/.wtree.yml"
sed -n '1,240p' "$WTREE_INIT_PROJECT/project.wtree.yml"
git -C "$WTREE_INIT_PROJECT" diff -- .gitignore project.wtree.yml
```

`init` writes local `.wtree.yml`, portable manifest version 2, and the exact
`/library/` protection needed by the parent checkout. It does not stage,
commit, or push. A maintainer should review those changes before publishing
them. Use `--ignore <glob>` to omit an intentionally unrelated Git tree,
`--clone-url id=url` when the fetch URL must differ from the attached
upstream, and `--manifest-source` when consumers will obtain the manifest from
a stable URL or path.

## 3. Registry situations: list, unregister, and prune

Read the initialized project ID and inspect its healthy registration:

```sh
export WTREE_INIT_ID="$(awk '$1 == "id:" { print $2; exit }' \
  "$WTREE_INIT_PROJECT/.wtree.yml")"
wtree project list --data-dir "$WTREE_DATA_HOME"
wtree project list --data-dir "$WTREE_DATA_HOME" --json
```

A healthy registration is not objectively prunable. This is expected to
fail without mutation:

```sh
wtree project prune "$WTREE_INIT_ID" \
  --data-dir "$WTREE_DATA_HOME" --dry-run
```

Use `unregister` when removal is intentional even though the project remains
healthy. It removes only the registry entry:

```sh
wtree project unregister "$WTREE_INIT_ID" \
  --data-dir "$WTREE_DATA_HOME" --dry-run --json
wtree project unregister "$WTREE_INIT_ID" \
  --data-dir "$WTREE_DATA_HOME"
```

To demonstrate evidence-backed pruning, initialize an independent fixture and
make only its registered config path unavailable:

```sh
./tutorial/setup-init-fixture.sh "$WTREE_ALL_COMMANDS/init-prune"
export WTREE_PRUNE_PROJECT="$WTREE_ALL_COMMANDS/init-prune/maintainer-app"
wtree init "$WTREE_PRUNE_PROJECT" \
  --worktree-root "$WTREE_ALL_COMMANDS/prune-worktrees"
export WTREE_PRUNE_ID="$(awk '$1 == "id:" { print $2; exit }' \
  "$WTREE_PRUNE_PROJECT/.wtree.yml")"
mv "$WTREE_PRUNE_PROJECT/.wtree.yml" \
  "$WTREE_PRUNE_PROJECT/.wtree.yml.saved"
wtree project list --data-dir "$WTREE_DATA_HOME" --json
wtree project prune "$WTREE_PRUNE_ID" \
  --data-dir "$WTREE_DATA_HOME" --dry-run --json
wtree project prune "$WTREE_PRUNE_ID" --data-dir "$WTREE_DATA_HOME"
mv "$WTREE_PRUNE_PROJECT/.wtree.yml.saved" \
  "$WTREE_PRUNE_PROJECT/.wtree.yml"
```

Neither registry cleanup command deletes repositories, worktrees, project
configuration, workspace state, recovery data, or lock files.

## 4. Consumer situation: clone a published project

Create the portable Acme Shop distribution fixture:

```sh
./tutorial/setup-fixture.sh "$WTREE_ALL_COMMANDS/consumer"
export WTREE_MANIFEST="$WTREE_ALL_COMMANDS/consumer/project.wtree.yml"
export WTREE_PROJECT="$WTREE_ALL_COMMANDS/consumer/acme-shop"
export WTREE_WORKTREES="$WTREE_ALL_COMMANDS/consumer/worktrees"
```

Preflight the complete clone and prove it did not create its destination:

```sh
wtree clone "$WTREE_MANIFEST" "$WTREE_PROJECT" \
  --worktree-root "$WTREE_WORKTREES" --dry-run --json
test ! -e "$WTREE_PROJECT"
```

Execute it with progress output:

```sh
wtree clone "$WTREE_MANIFEST" "$WTREE_PROJECT" \
  --worktree-root "$WTREE_WORKTREES" --verbose
cd "$WTREE_PROJECT"
```

Repeating the same clone is an expected destination conflict and leaves the
first checkout unchanged:

```sh
wtree clone "$WTREE_MANIFEST" "$WTREE_PROJECT" \
  --worktree-root "$WTREE_WORKTREES"
```

The fixture's transport default branch is deliberately
`fixture/clone-bootstrap`, while the manifest selects `main`. This confirms
that clone follows the explicit portable upstream contract rather than
guessing from remote `HEAD`.

## 5. Configuration situations

Clone stores the selected worktree root in project scope. Read it in scalar
and structured forms:

```sh
wtree config get worktrees.root --project
wtree config list --project
wtree config list --project --json
```

Set, inspect, unset, and finally restore a project override:

```sh
wtree config set worktrees.root \
  "$WTREE_ALL_COMMANDS/consumer/alternate-worktrees" --project
wtree config get worktrees.root --project
wtree config unset worktrees.root --project
wtree config set worktrees.root "$WTREE_WORKTREES" --project
```

Without `--project`, the same four subcommands operate on user-global
configuration. Project scope takes precedence over global scope, which takes
precedence over the platform default. The all-commands test intentionally
uses project scope so it never writes a developer's real global config.

## 6. Read-only workspace and repository inspection

Inspect the registered default workspace:

```sh
wtree list
wtree list --json
wtree status
wtree status default --json
wtree path default
wtree repo path backend
wtree repo get frontend --json
```

Context resolution also works inside a nested repository:

```sh
cd "$WTREE_PROJECT/backend"
wtree status
cd "$WTREE_PROJECT"
```

Outside the project, select it explicitly:

```sh
wtree list --project "$WTREE_PROJECT"
wtree status default --project "$WTREE_PROJECT"
```

Use `path` and `repo path` for shell composition. Do not reconstruct sanitized
workspace directories.

## 7. Existing-branch checkout situations

The fake remotes advertise `feature/customer-search`, but a fresh clone does
not yet have that local branch in any repository. This preflight is expected
to fail and create nothing:

```sh
wtree checkout feature/customer-search --dry-run
```

Materialize the local tracking branch in every source repository:

```sh
for checkout in \
  "$WTREE_PROJECT" \
  "$WTREE_PROJECT/backend" \
  "$WTREE_PROJECT/frontend"
do
  git -C "$checkout" branch --track \
    feature/customer-search origin/feature/customer-search
done
```

Now preflight and execute checkout:

```sh
wtree checkout feature/customer-search --dry-run --json
wtree checkout feature/customer-search --verbose
wtree status feature/customer-search
```

Aggregate preflight also rejects a branch that exists in only part of the
repository hierarchy. Materialize the remote branches that exist, then verify
that neither attempt creates partial workspace state:

```sh
for checkout in "$WTREE_PROJECT" "$WTREE_PROJECT/backend"
do
  git -C "$checkout" branch --track \
    release/2026-q3 origin/release/2026-q3
done
wtree checkout release/2026-q3 --dry-run

git -C "$WTREE_PROJECT/frontend" branch --track \
  experiment/dark-navigation origin/experiment/dark-navigation
wtree checkout experiment/dark-navigation --dry-run
wtree list
```

Checkout never creates a branch. A wholly missing branch therefore fails, and
`create` is also wrong once the synchronized branch already exists:

```sh
wtree checkout feature/does-not-exist --dry-run
wtree create feature/customer-search --dry-run
```

## 8. Create with custom mounts

Create a new branch from each repository's local `main` and override the child
mounts:

```sh
wtree create tutorial/custom --from main \
  --mount backend=api --mount frontend=web \
  --dry-run --json
wtree create tutorial/custom --from main \
  --mount backend=api --mount frontend=web \
  --verbose
export WTREE_CUSTOM="$(wtree path tutorial/custom)"
```

The repository IDs do not change with their mounts:

```sh
cd "$WTREE_CUSTOM/api"
wtree repo path frontend
wtree repo get backend --json
wtree status
cd "$WTREE_PROJECT"
```

Without `--from`, create uses each repository's current `HEAD`. Use `--path`
for one explicit destination or `--worktree-root` to override configured
storage for one operation.

## 9. Remove, retained checkout, doctor, and delete

`remove` deletes physical worktrees but retains branches, mounts, and state:

```sh
wtree remove tutorial/custom --dry-run --json
wtree remove tutorial/custom --verbose
wtree list
```

The retained workspace is a valid doctor situation. `--dry-run` is accepted
with `--fix`, not by itself:

```sh
wtree doctor tutorial/custom
wtree doctor tutorial/custom --fix --dry-run --json
```

Checkout restores the retained `api/` and `web/` mounts:

```sh
wtree checkout tutorial/custom --dry-run --json
wtree checkout tutorial/custom
wtree doctor tutorial/custom --fix
```

Delete removes worktrees, synchronized local branches, and retained state:

```sh
wtree delete tutorial/custom --dry-run --json
wtree delete tutorial/custom --verbose
```

## 10. Dirty-worktree safety and `--force`

Create a disposable workspace and make its parent checkout dirty:

```sh
wtree create tutorial/dirty --from main
export WTREE_DIRTY="$(wtree path tutorial/dirty)"
printf '\nUncommitted tutorial change.\n' >> "$WTREE_DIRTY/README.md"
wtree status tutorial/dirty
```

Normal removal is expected to fail. Inspect the exact force override before
using it:

```sh
wtree remove tutorial/dirty
wtree remove tutorial/dirty --force --dry-run --json
wtree remove tutorial/dirty --force
```

Here the uncommitted line is intentionally disposable. Checkout restores the
retained branch cleanly, after which delete is safe:

```sh
wtree checkout tutorial/dirty
wtree delete tutorial/dirty
```

For `remove`, `--force` overrides dirty-worktree removal only. For `delete`, it
also permits deletion of an unmerged named branch. It does not bypass other
validation.

Exercise the distinct unmerged-branch override with a committed, clean
workspace:

```sh
wtree create tutorial/unmerged --from main
export WTREE_UNMERGED="$(wtree path tutorial/unmerged)"
git -C "$WTREE_UNMERGED" config user.name "Wtree Tutorial"
git -C "$WTREE_UNMERGED" config user.email "tutorial@wtree.invalid"
printf 'unmerged tutorial commit\n' > "$WTREE_UNMERGED/unmerged.txt"
git -C "$WTREE_UNMERGED" add unmerged.txt
git -C "$WTREE_UNMERGED" commit -m "Create an unmerged tutorial commit"
wtree delete tutorial/unmerged
wtree delete tutorial/unmerged --force --dry-run --json
wtree delete tutorial/unmerged --force
```

## 11. Import a complete manual workspace

Create synchronized branches and worktrees outside `wtree`, using mounts that
still identify the configured repositories by their common Git directories:

```sh
for checkout in \
  "$WTREE_PROJECT" \
  "$WTREE_PROJECT/backend" \
  "$WTREE_PROJECT/frontend"
do
  git -C "$checkout" branch manual/full main
done
export WTREE_MANUAL_FULL="$WTREE_ALL_COMMANDS/consumer/manual-full"
git -C "$WTREE_PROJECT" worktree add "$WTREE_MANUAL_FULL" manual/full
git -C "$WTREE_PROJECT/backend" worktree add \
  "$WTREE_MANUAL_FULL/api" manual/full
git -C "$WTREE_PROJECT/frontend" worktree add \
  "$WTREE_MANUAL_FULL/web" manual/full
```

Preview, import, inspect from a nested directory, and delete it:

```sh
wtree import "$WTREE_MANUAL_FULL" --project "$WTREE_PROJECT" \
  --name manual/full --dry-run --json
wtree import "$WTREE_MANUAL_FULL" --project "$WTREE_PROJECT" \
  --name manual/full
cd "$WTREE_MANUAL_FULL/api"
wtree repo get backend
cd "$WTREE_PROJECT"
wtree delete manual/full
```

If all observed branches share a usable name, `--name` can be omitted. Supply
it for divergent branches, detached heads, or when a different logical name
is desired.

## 12. Import a partial manual workspace

Create only the parent and backend worktrees:

```sh
for checkout in "$WTREE_PROJECT" "$WTREE_PROJECT/backend"
do
  git -C "$checkout" branch manual/partial main
done
export WTREE_MANUAL_PARTIAL="$WTREE_ALL_COMMANDS/consumer/manual-partial"
git -C "$WTREE_PROJECT" worktree add \
  "$WTREE_MANUAL_PARTIAL" manual/partial
git -C "$WTREE_PROJECT/backend" worktree add \
  "$WTREE_MANUAL_PARTIAL/api" manual/partial
```

The default import rejects an incomplete repository mapping. Use
`--allow-partial` only when retaining that incompleteness is intentional:

```sh
wtree import "$WTREE_MANUAL_PARTIAL" --project "$WTREE_PROJECT" \
  --name manual/partial --dry-run
wtree import "$WTREE_MANUAL_PARTIAL" --project "$WTREE_PROJECT" \
  --name manual/partial --allow-partial --dry-run --json
wtree import "$WTREE_MANUAL_PARTIAL" --project "$WTREE_PROJECT" \
  --name manual/partial --allow-partial
wtree status manual/partial --json
wtree doctor manual/partial
```

`remove` and `delete` intentionally refuse a partial workspace. Repair its
repository mapping before destructive lifecycle operations. The following is
therefore an expected safe failure:

```sh
wtree delete manual/partial
```

## 13. Verify the final state

The default clone, checked-out customer-search workspace, and explicitly
partial manual import remain. Temporary complete workspaces and their branches
are gone:

```sh
wtree project list --data-dir "$WTREE_DATA_HOME"
wtree list --project "$WTREE_PROJECT"
wtree status default --project "$WTREE_PROJECT"
wtree status feature/customer-search --project "$WTREE_PROJECT"
```

Run the exact end-to-end version from the source root:

```sh
./tutorial/run-all-commands.sh
```

Set `WTREE_TUTORIAL_KEEP=1` to retain the temporary directory after the test
for inspection. Otherwise the runner removes only the validated temporary
directory it created.
