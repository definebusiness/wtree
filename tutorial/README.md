# `wtree` hands-on tutorial

This tutorial uses local bare Git repositories and portable manifests to
explore the core `wtree` workflow without contacting a real remote server. The
main fixture represents the root-Git-compatible Acme Shop layout. The
executable all-command runner additionally initializes and clones a plain
logical-root forest with a grouped non-dot base, a sibling top-level tree, and
a nested child.

For every command, registry cleanup, partial imports, and additional safety
paths, continue with the [all-commands tutorial](ALL-COMMANDS.md).

Run the commands in order in one terminal. Paths are stored in environment
variables so the examples work regardless of where the `wtree-go` repository
is checked out.

## Prerequisites

You need Git and the version of Go declared in `go.mod`:

```sh
git --version
go version
```

Start in the root of the `wtree-go` source repository:

```sh
cd /path/to/wtree-go
export WTREE_SOURCE_ROOT="$PWD"
```

## 1. Install `wtree` and add it to `PATH`

Install the development version of the command:

```sh
go install ./cmd/wtree
```

Find Go's binary directory and add it to `PATH` for this terminal:

```sh
export WTREE_BIN_DIR="$(go env GOBIN)"
if [ -z "$WTREE_BIN_DIR" ]; then
  export WTREE_BIN_DIR="$(go env GOPATH)/bin"
fi
export PATH="$WTREE_BIN_DIR:$PATH"
```

Confirm that the shell finds the installed command:

```sh
command -v wtree
wtree --version
wtree --help
```

To make the change permanent, add the resulting `WTREE_BIN_DIR` path to the
`PATH` assignment in your shell startup file, such as `~/.zshrc` or
`~/.bashrc`.

The concise workflow guide is also available from the installed command:

```sh
wtree --how-to
```

## 2. Create the tutorial fixture

Create the fixture next to the `wtree-go` repository:

```sh
cd "$WTREE_SOURCE_ROOT"
./tutorial/setup-fixture.sh ../wtree-tutorial
export WTREE_TUTORIAL="$(cd ../wtree-tutorial && pwd)"
export WTREE_MANIFEST="$WTREE_TUTORIAL/project.wtree.yml"
export WTREE_PROJECT="$WTREE_TUTORIAL/acme-shop"
```

If you already ran the fixture script successfully, skip the script command
and set the variables to the existing fixture instead.

Keep `wtree` registry and worktree data inside the disposable tutorial
directory, and redirect Linux global-configuration lookup there as well:

```sh
export WTREE_DATA_HOME="$WTREE_TUTORIAL/wtree-data"
export XDG_CONFIG_HOME="$WTREE_TUTORIAL/xdg-config"
```

On macOS, `wtree` intentionally ignores `XDG_CONFIG_HOME` and continues to
look for global configuration at
`~/Library/Application Support/wtree/config.yml`. The tutorial only mutates
project-scoped configuration, so it does not write that global file;
`WTREE_DATA_HOME` still isolates its registry, state, and default worktree
storage.

The fixture initially has only this distribution layout:

```text
project.wtree.yml            portable v2 project manifest
origins/
├── acme-shop.git/           bare parent origin
├── java-backend.git/        bare backend origin
└── web-frontend.git/        bare frontend origin
```

The manifest mounts `java-backend.git` as `backend/`. This demonstrates that
`wtree` identifies repositories by Git identity, not by matching repository
and directory names.

The fake origins and their branches are:

| Branch | Parent origin | Backend origin | Frontend origin |
|---|---:|---:|---:|
| `main` | yes | yes | yes |
| `feature/customer-search` | yes | yes | yes |
| `release/2026-q3` | yes | yes | no |
| `chore/structured-logging` | yes | yes | no |
| `experiment/dark-navigation` | no | no | yes |
| `hotfix/customer-timeout` | no | yes | no |

## 3. Clone the complete project from its portable manifest

First preflight the clone. `--dry-run` reads and validates the manifest and all
deterministic destination rules without creating a checkout, while `--json`
produces machine-readable output. `--worktree-root` keeps this operation
independent of ambient global configuration:

```sh
wtree clone "$WTREE_MANIFEST" "$WTREE_PROJECT" \
  --worktree-root "$WTREE_TUTORIAL/worktrees" \
  --dry-run --json
```

Now reconstruct and register the complete project:

```sh
wtree clone "$WTREE_MANIFEST" "$WTREE_PROJECT" \
  --worktree-root "$WTREE_TUTORIAL/worktrees"
cd "$WTREE_PROJECT"
```

The manifest declares version 2 and names `root` as its base repository. The
fake origins use their ordinary `main` heads, and clone follows the explicit
manifest contract while recording the execution-time checked-out commits.

The clone contains an ignored local configuration, its ignored persistent
configuration lock, and the tracked portable manifest from the parent
repository:

```sh
sed -n '1,200p' .wtree.yml
sed -n '1,200p' project.wtree.yml
git check-ignore .wtree.yml
git status --short
```

`.wtree.yml` records the absolute path in `WTREE_MANIFEST` as its original
manifest source. Both configurations should contain repository IDs named
`root`, `backend`, and `frontend`, but only the local file contains checkout
paths and machine-specific settings.

The cloned checkout is registered as the `default` workspace:

```sh
wtree list
wtree status
wtree status default --json
wtree push --json
```

`wtree push` is a readiness report, not a publisher. It verifies that the
complete workspace is already available at each configured upstream, but never
pushes, fetches, or creates refs or tags. Publish manually after reviewing its
report; coordinated publication remains a future workflow.

The human table's `STATUS` column reports working-tree and structural state.
Its `UPSTREAM` column reports the last-fetched local upstream relationship.
When the manifest tracked by the local base checkout is available, an additive
`Local drift` table reports manifest/state/disk differences. `wtree status`
does not fetch or contact remotes.

To run a direct command across every verified checkout, use `exec`. Arguments
are not interpreted by an implicit shell, and effects from the invoked command
are not rolled back:

```sh
wtree exec -- git status --short
wtree exec -- sh -c 'go test ./... | tee test.log'
```

Use `fetch` when you deliberately want to refresh those remote-tracking facts.
It contacts only the configured remote and ref for each present repository,
updates no local branch, HEAD, or worktree, and is non-transactional: a fetch
that succeeded before a later failure remains visible. `status` continues to
use only the last-fetched local facts and never contacts a remote.

```sh
wtree fetch --dry-run --json
wtree fetch
```

Inspect the remote-tracking branches created by the clone:

```sh
git -C "$WTREE_PROJECT" branch --remotes
git -C "$WTREE_PROJECT/backend" branch --remotes
git -C "$WTREE_PROJECT/frontend" branch --remotes
```

## 4. Inspect project configuration and understand future directions

The worktree root stored by `clone` is project-scoped:

```sh
wtree config get worktrees.root --project
wtree config list --project
```

Exercise configuration mutation without changing the user's global settings:

```sh
wtree config set worktrees.root "$WTREE_TUTORIAL/alternate-worktrees" --project
wtree config get worktrees.root --project
wtree config list --project --json
wtree config unset worktrees.root --project
wtree config set worktrees.root "$WTREE_TUTORIAL/worktrees" --project
```

Without `--project`, `config get`, `set`, `unset`, and `list` operate on global
configuration. Project scope takes precedence over global scope, which takes
precedence over the platform default. The final command above restores the
clone's original project value:

```sh
wtree config get worktrees.root --project
```

Use `wtree update --dry-run` to inspect a compatible revision from the stored
manifest source, then use `wtree update` to apply it. The update transaction
may fast-forward configured default branches and add verified repositories; it
never relocates or deletes existing checkouts, and records removed repositories
as retained unmanaged evidence. `sync` and release-lock manifests remain
future work; today's portable manifest follows movable default branches while
dry-run observations remain diagnostic and clone uses the selected branch tip
fetched during execution.

## 5. Understand remote and local branches

`wtree checkout` uses branches that already exist locally in every configured
repository. It does not create a local branch from an `origin/...`
remote-tracking ref. The fresh clones initially have only the
manifest-selected `main` local branch, while the fake origins advertise
several additional branches that are not fetched by clone.

Compare local and remote-tracking branches:

```sh
git -C "$WTREE_PROJECT" branch
git -C "$WTREE_PROJECT" branch --remotes
```

This distinction is exercised in the following checkout examples.

## 6. Checkout a branch present on all three origins

`feature/customer-search` exists on all three fake origins. Before creating
local tracking branches, this checkout is expected to fail safely:

```sh
wtree checkout feature/customer-search --dry-run
```

The error identifies `root` as the first repository without the required
local branch. No workspace or worktree is created.

Create a local branch from the corresponding fake-origin branch in every
source repository:

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

Using `create` is still wrong for an existing branch. This command is expected
to report a conflict and make no changes:

```sh
wtree create feature/customer-search --dry-run
```

Use `checkout` instead. Preview the complete operation, then perform it:

```sh
wtree checkout feature/customer-search --dry-run
wtree checkout feature/customer-search --verbose
```

Inspect the workspace and its committed branch-specific files:

```sh
wtree list
wtree status feature/customer-search
export WTREE_SEARCH_WORKSPACE="$(wtree path feature/customer-search)"
git -C "$WTREE_SEARCH_WORKSPACE" diff main..HEAD --name-only
git -C "$WTREE_SEARCH_WORKSPACE/backend" diff main..HEAD --name-only
git -C "$WTREE_SEARCH_WORKSPACE/frontend" diff main..HEAD --name-only
```

`wtree path` is the supported way to find a workspace. Do not reconstruct its
sanitized directory name yourself.

Jump into the branch workspace, back to the original clone (registered as the
`default` workspace), and forth again with the same lookup command:

```sh
cd "$(wtree path feature/customer-search)"
cd "$(wtree path default)"
cd "$(wtree path feature/customer-search)"
```

## 7. Attempt checkouts for branches present on only some origins

### Present on two origins

`release/2026-q3` exists on the parent and backend origins, but not on the
frontend origin. Materialize the two branches that do exist:

```sh
for checkout in "$WTREE_PROJECT" "$WTREE_PROJECT/backend"
do
  git -C "$checkout" branch --track \
    release/2026-q3 origin/release/2026-q3
done
```

The aggregate checkout is expected to fail because `frontend` has no such
branch:

```sh
wtree checkout release/2026-q3 --dry-run
```

Preflight is transactional: no release workspace is created even though the
branch exists in two repositories. Confirm that with:

```sh
wtree list
```

### Present on one origin

`experiment/dark-navigation` exists only on the frontend origin:

```sh
git -C "$WTREE_PROJECT/frontend" branch --track \
  experiment/dark-navigation origin/experiment/dark-navigation
wtree checkout experiment/dark-navigation --dry-run
```

This is also expected to fail without creating a workspace, because the
parent and backend repositories do not have the branch.

## 8. Attempt a checkout for a branch present on no origin

No repository has `feature/does-not-exist`. The following command is expected
to fail at preflight:

```sh
wtree checkout feature/does-not-exist --dry-run
```

Check again that failed aggregate operations did not create partial workspace
state:

```sh
wtree list --json
```

## 9. Create a synchronized new branch and workspace

Unlike `checkout`, `create` makes a new local branch in every repository. Use
`--from main` to resolve `main` independently in all three repositories. This
example also changes the workspace-specific mounts from `backend/` to `api/`
and from `frontend/` to `web/`. `wtree` validates those effective mounts and
the exact literal rules, then its create execution ensures the rules in the
new parent worktrees without changing source checkouts:

```sh
wtree create tutorial/new-workspace \
  --from main \
  --mount backend=api \
  --mount frontend=web \
  --dry-run
```

The dry run prints the branches, mounts, paths, Git base commits, and the
parent `.gitignore` rules that execution will ensure. It ends with an explicit
no-mutation message. Perform the operation with progress output:

```sh
wtree create tutorial/new-workspace \
  --from main \
  --mount backend=api \
  --mount frontend=web \
  --verbose
```

Resolve and enter the new workspace:

```sh
export WTREE_NEW_WORKSPACE="$(wtree path tutorial/new-workspace)"
cd "$WTREE_NEW_WORKSPACE"
pwd
```

The repository IDs remain `backend` and `frontend` even though their mounts
are now `api/` and `web/`:

```sh
wtree repo path backend
wtree repo path frontend
wtree repo get backend --json
```

Commands also resolve project and workspace context from inside a nested
repository:

```sh
cd "$(wtree repo path backend)"
wtree status
cd "$WTREE_NEW_WORKSPACE"
```

## 10. Inspect changes and removal safety

Create an uncommitted change in the parent checkout:

```sh
printf '\nLocal tutorial note.\n' >> README.md
wtree status
wtree status tutorial/new-workspace --json
```

A normal removal is expected to fail because it would discard a dirty
worktree:

```sh
wtree remove tutorial/new-workspace
```

Preview the narrowly scoped `--force` override without applying it:

```sh
wtree remove tutorial/new-workspace --force --dry-run
```

Restore the file so the tutorial does not discard work, then return to the
cloned default checkout:

```sh
git restore README.md
cd "$WTREE_PROJECT"
```

## 11. Remove and restore a workspace

`remove` deletes the physical worktrees but retains their branches, mounts,
and workspace state:

```sh
wtree remove tutorial/new-workspace --dry-run
wtree remove tutorial/new-workspace --verbose
wtree list
```

The workspace remains listed because it can be restored. `checkout` recognizes
the retained state and restores the same `api/` and `web/` mounts:

```sh
wtree checkout tutorial/new-workspace --dry-run
wtree checkout tutorial/new-workspace
wtree status tutorial/new-workspace
cd "$(wtree path tutorial/new-workspace)"
wtree repo get backend --json
cd "$WTREE_PROJECT"
```

You can also run a command outside the project with explicit project
selection:

```sh
wtree list --project "$WTREE_PROJECT"
```

## 12. Diagnose workspace drift

Inspect the restored, healthy workspace:

```sh
wtree doctor tutorial/new-workspace
wtree doctor tutorial/new-workspace --json
```

Preview any verified safe repairs. For this healthy workspace there should be
no findings or repairs:

```sh
wtree doctor tutorial/new-workspace --fix --dry-run
```

`doctor --fix` is intentionally limited to repairs reported as safe; it is not
a general-purpose force option.

## 13. Delete a workspace permanently

Unlike `remove`, `delete` removes the worktrees, their local branches, and the
retained workspace state:

```sh
wtree delete tutorial/new-workspace --dry-run
wtree delete tutorial/new-workspace --verbose
wtree list
```

The `tutorial/new-workspace` entry should no longer be listed. Its branches
were never pushed, so the fake origins are unchanged.

## 14. Import manually created worktrees

`import` records existing worktrees without creating branches or rewriting
their layout. Create a branch in each repository manually:

```sh
for checkout in \
  "$WTREE_PROJECT" \
  "$WTREE_PROJECT/backend" \
  "$WTREE_PROJECT/frontend"
do
  git -C "$checkout" branch manual/import-demo main
done
```

Create the outer worktree, then mount the nested repositories under custom
names:

```sh
export WTREE_MANUAL_WORKSPACE="$WTREE_TUTORIAL/manual/import-demo"
git -C "$WTREE_PROJECT" worktree add \
  "$WTREE_MANUAL_WORKSPACE" manual/import-demo
git -C "$WTREE_PROJECT/backend" worktree add \
  "$WTREE_MANUAL_WORKSPACE/server" manual/import-demo
git -C "$WTREE_PROJECT/frontend" worktree add \
  "$WTREE_MANUAL_WORKSPACE/web" manual/import-demo
```

Preview and import the manual workspace. Explicit project selection tells
`wtree` which configured repository hierarchy the new checkout belongs to:

```sh
wtree import "$WTREE_MANUAL_WORKSPACE" --project "$WTREE_PROJECT" \
  --name manual/import-demo \
  --dry-run
wtree import "$WTREE_MANUAL_WORKSPACE" --project "$WTREE_PROJECT" \
  --name manual/import-demo
```

Repository identity allows `wtree` to map `server/` back to `backend` and
`web/` back to `frontend`:

```sh
cd "$WTREE_MANUAL_WORKSPACE"
wtree status
wtree repo path backend
wtree repo path frontend
```

Delete this imported demonstration workspace:

```sh
cd "$WTREE_PROJECT"
wtree delete manual/import-demo --dry-run
wtree delete manual/import-demo
```

## 15. Plain logical roots and repository forests

The root-Git project above is the compatible one-top-level-repository form of
the general model. A logical root may instead be an ordinary directory:

```text
workspace/
├── services/api/                 base repository
│   └── components/shared/        child of api
└── clients/web/                  sibling top-level repository
```

Top-level mounts are relative to `workspace/`; `components/shared` is relative
to its immediate parent, `api`. Only the base owns `.wtree.yml` and
`project.wtree.yml`. The logical root and grouping directories own neither.
When more than one top-level repository is discovered, initialization requires
an explicit top-level base:

```sh
wtree init /path/to/workspace --base-repository api --dry-run --json
wtree init /path/to/workspace --base-repository api
```

Run `./tutorial/run-all-commands.sh` for an executable local-remote example
that publishes this layout, clones it, creates and restores a workspace,
imports a manual forest, inspects topology from nested and sibling contexts,
and deletes only managed worktrees.

## 16. Review the final state

The cloned default checkout and the checked-out `feature/customer-search` workspace
remain. The failed partial and missing-branch checkouts did not leave any
workspace state:

```sh
wtree list
wtree list --json
wtree status feature/customer-search
```

For the complete reference at any point, use:

```sh
wtree <command> --help
wtree --how-to
```

When finished, the entire scenario—including fake origins, registry data, and
generated worktrees—is contained under `$WTREE_TUTORIAL` and can be archived
or removed as one disposable directory.
