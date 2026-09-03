# wtree

`wtree` manages synchronized Git workspaces across a project containing a
forest of independent Git repositories. The project has one logical root,
which may be an ordinary directory, and one designated top-level base
repository that owns its portable and machine-local metadata.

It was created because existing worktree tools did not support synchronized
branch management across sibling and nested repositories: creating a
`feature/login` workspace should create matching branches and Git worktrees for
every declared repository tree as one logical operation.

## What it does

`wtree` discovers independent Git repositories below an explicit logical-root
boundary, records their Git identities and parent relationships, and treats
the resulting forest as one project. It then creates, imports, restores,
inspects, and safely removes complete workspaces.

Repository identity is based on Git's common directory, not the checkout
directory name. A repository can therefore be mounted under a different name
in a workspace without losing its identity.

For example, a project like this:

```text
product/                       # logical project root, not a Git checkout
├── services/
│   └── api/                   # base repository
│       ├── .git/
│       └── components/
│           └── shared/       # child repository of api
│               └── .git/
└── clients/
    └── web/                   # sibling top-level repository
        └── .git/
```

can have a synchronized `feature/login` workspace whose nested checkout is
mounted as `api/` instead of `backend/`:

```text
feature-login/                 # logical workspace root
├── services/
│   └── api/
│       ├── .git
│       └── components/shared/
│           └── .git
└── clients/web/
    └── .git
```

All three repositories use the `feature/login` branch. Top-level mounts are
relative to the logical root; a child mount is relative to its immediate Git
parent. The base repository owns `.wtree.yml` and `project.wtree.yml`, but it
does not become the parent of sibling repositories.

## Why it exists

Common IDEs and Git worktree tools generally do not support a project made of
several independent repository trees beneath one ordinary directory. In
particular, they cannot create and manage one synchronized workspace across
all of those repositories. The traditional outer-repository layout remains
supported as the one top-level repository mounted at `.`. `wtree` was built
to fill that gap. Easy, reliable
worktree management is especially crucial for modern AI-assisted development,
where parallel agents and experiments need isolated workspaces without losing
the coherence of a multi-repository project.

## Install

Install the Go version declared in [go.mod](go.mod), then run:

```sh
go install ./cmd/wtree
export PATH="$(go env GOPATH)/bin:$PATH"
wtree --version
```

Add the `export` line to your shell startup file (for example, `~/.zshrc` or
`~/.bashrc`) to make `wtree` available in new terminal sessions.

See [docs/INSTALL.md](docs/INSTALL.md) for local release builds and checksums.

## Usage

To publish an existing project, first push every repository and connect its
current branch to the intended upstream. `wtree init` writes ignored
machine-local `.wtree.yml` and tracked `project.wtree.yml` in the selected base
repository. It updates each Git parent's `.gitignore` for its direct child
mounts; ordinary grouping directories and the logical root own no metadata or
ignore rules. Review, commit, and push the manifest and every changed
`.gitignore`.

```sh
cd ~/code/product
wtree init --base-repository api
git -C services/api add .gitignore project.wtree.yml
```

`project.wtree.yml` is a portable, reviewable authoring artifact. `init` never
stages, commits, or pushes; review and commit the portable manifest and any
automatic `.gitignore` changes yourself.

Before manually publishing a complete workspace, run `wtree push`. It only
reports whether each checkout is already at its exact configured upstream tip;
it never runs `git push`, fetches, or creates refs or tags. Publication remains
a manual workflow until a separately specified publishing command exists.

Hook-free local configuration and portable manifests use schema version 2. The `project.base_repository` field
names exactly one top-level metadata owner. It may be mounted below grouping
directories and need not be the only top-level repository:

```yaml
version: 2
project:
  id: product
  name: product
  base_repository: api
repositories:
  api:
    parent: ""
    mount: services/api
    # clone, upstream, identity, and default_branch are written by `wtree init`
  shared:
    parent: api
    mount: components/shared
    # clone, upstream, identity, and default_branch are written by `wtree init`
  web:
    parent: ""
    mount: clients/web
    # clone, upstream, identity, and default_branch are written by `wtree init`
```

## Lifecycle hooks: explicit local consent

Version 3 adds lifecycle declarations without changing any hook-free version 2
contract. A version 2 document must remain hook-free: adding `hooks` or
`shared_hooks` requires the relevant version 3 document. Local and portable
versions are independent, and loading a version 3 manifest never upgrades the
ignored local `.wtree.yml` or installs anything.

There are three intentionally separate sources:

- Local `.wtree.yml` `hooks.post-create` runs after a successful `wtree create`.
  Use `--no-hooks` when intentionally bypassing that trusted local setup.
- Portable `project.wtree.yml` `hooks.post-clone` runs only for a real clone
  invoked with `--run-hooks`. It is never implied by reading, cloning, or
  updating a manifest.
- Portable `shared_hooks.post-create` is distribution metadata only. It never
  executes from a manifest; inspect it with `wtree hooks list` and explicitly
  copy it into local configuration with `wtree hooks install`.

Hook commands use direct argument arrays, not an implicit shell. Review every
literal argument before sharing a definition. A portable executable with a
path separator must be source-relative, tracked, and contained; a bare command
name resolves through the sanitized effective `PATH` (and `PATHEXT` on Windows).
Durable hook-run records and execution-result/error JSON never carry ambient
environment values, credentials, command output, literal arguments, or
executable paths. Those facts are intentionally visible only on inspection
surfaces such as `hooks list` and create/clone plan or dry-run output.

```sh
# Author and inspect trusted local post-create setup.
wtree hooks list
wtree hooks share post-create

# On another checkout, opt in to the shared declaration.
wtree hooks install --missing

# A portable post-clone hook needs per-invocation authority.
wtree clone ./project.wtree.yml ./product --run-hooks

# Resume only one matching, incomplete setup run.
wtree hooks retry feature/login
```

Hook failure never rolls back a published workspace. Inspect its bounded setup
diagnostic with `wtree status <workspace>` and `wtree doctor <workspace>`, then
use the printed retry command after fixing the external cause. Retry validates
the exact source, plan, and workspace state and never starts a fresh run or
reruns a durably completed hook. Make hook programs idempotent: an interruption
after a child side effect but before durable success recording can require that
same hook to run again. Hooks are not sandboxed; local hooks are trusted
programs and may access the resources available to their process.

Clone a published project from a local manifest file or an HTTP(S) URL. The
optional destination defaults to the manifest's safe project name. A dry run
reads the manifest, checks every remote, and renders observed-commit
preflight evidence without creating directories or local state. Execution
then checks out each selected branch tip fetched at that time:

```sh
wtree clone ./project.wtree.yml ./product --dry-run
wtree clone ./project.wtree.yml ./product
```

The clone is verified and registered as the `default` workspace. Use `wtree
update` to safely apply a compatible portable-manifest revision to that default
workspace. `--dry-run` shows the classified plan first; execution never
relocates or deletes existing checkouts and retains removed repositories as
unmanaged local evidence.

Choose where newly created workspaces live (this is optional; otherwise the
platform default is used):

```sh
wtree config set worktrees.root ~/code/worktrees
```

Create matching branches and worktrees for every repository in deterministic
parent-first order:

```sh
wtree create feature/login
cd "$(wtree path feature/login)"
```

The original clone is the `default` workspace. Jump back to it—and later back
to the branch workspace—through `wtree path` rather than reconstructing either
location:

```sh
cd "$(wtree path default)"
cd "$(wtree path feature/login)"
```

Create from another ref, or change a nested repository's mount in this one
workspace. `create` validates the effective mounts first, then automatically
ensures their literal rules in the new parent worktrees. It never changes a
source checkout, stages, or commits an ignore file:

```sh
wtree create feature/from-main --from main

wtree create feature/login --mount backend=api
```

Inspect the workspace, including expected branches, mounts, and checkout
state. `STATUS` reports working-tree and structural state; `UPSTREAM` reports
the last-fetched local upstream relationship. `wtree status` does not fetch or
contact remotes:

```sh
wtree status feature/login
wtree status feature/login --json
```

Run one direct executable in every verified repository checkout with `exec`.
It preflights the complete workspace before starting anything, passes arguments
literally, and does not add an implicit shell or roll back effects made by your
program. Use an explicit shell only when shell syntax is actually wanted:

```sh
wtree exec -- go test ./...
wtree exec -- sh -c 'make test | tee test.log'
```

Refresh the configured upstream facts explicitly with `fetch`. It contacts only
the configured remote and ref for each present checkout, updating that one
remote-tracking ref in parent-first order. It never moves a local branch, HEAD,
or worktree. Fetch is non-transactional: an earlier refresh remains if a later
repository fails. `status` remains network-free and reports last-fetched facts.

```sh
wtree fetch
wtree fetch --dry-run --json
```

Topology-bearing JSON results expose `logicalRoot`, `baseRepository`, and an
ordered `repositories` list. Each repository entry carries its declared
`parentId`, effective `mount`, and `resolvedPath` where applicable. Scalar
commands stay scalar: `wtree path` and `wtree repo path` print only one path.
When a project is stale or a failure happens before topology is validated,
those unproven topology fields are omitted rather than guessed.

Inspect the global project registry from any directory. This is read-only and
reports inconsistent registrations without pruning repositories, worktrees, or
configuration:

```sh
wtree project list
wtree project list --json
```

When an entry is objectively stale, preview a registry-only cleanup before
applying it. `project prune` removes only that one registry registration; it
does not prune Git worktrees or delete repositories, project configuration,
workspace state, recovery data, or lock files.

```sh
wtree project prune stale-project-id --dry-run
wtree project prune stale-project-id --json
```

To intentionally remove an exact project registration even when it is not
stale, use `project unregister`. This remains registry-only: it retains all
project configuration, workspace state, recovery data, locks, repositories,
and Git worktrees. The retained local configuration can register the project
again if a later mutating command is run from that project.

```sh
wtree project unregister project-id --dry-run
wtree project unregister project-id --json
```

If `.wtree.yml` is later removed while its checkout remains registered, `wtree
init` refuses to publish a second registration for the same configuration path
or Git repository identity. Inspect the conflict with `wtree project list`,
then use `project prune` only when its stale evidence allows it or explicitly
`project unregister` the intended registration before retrying `wtree init`.
Both cleanup commands remain registry-only and retain Git and project data.
See the
[duplicate-project troubleshooting steps](docs/TROUBLESHOOTING.md#duplicate-project-after-reinitializing)
for the complete recovery sequence.

If a workspace was created manually, record its verified Git identities and
checkout layout without rewriting it:

```sh
wtree import /work/feature-login --name feature/login
```

Remove worktrees while retaining the branches and workspace state, then restore
them later:

```sh
wtree remove feature/login
wtree checkout feature/login
```

Permanently remove the worktrees, branches, and retained state:

```sh
wtree delete feature/login
```

Use `--dry-run` on mutating commands to validate and preview an operation, and
use `--force` only when you explicitly intend to override the reported safety
checks. `wtree doctor feature/login` diagnoses drift; `--fix` applies only its
listed safe repairs.

If rollback cannot prove that a path still belongs to the failed operation,
`wtree` preserves the path instead of risking data loss, reports
`rollback_incomplete`, and writes a recovery record. The failure is immediate
and later mutations remain blocked until the retained work is inspected and
reconciled. Start with `wtree doctor <workspace>` and follow the
[incomplete-rollback guidance](docs/TROUBLESHOOTING.md#an-operation-reports-an-incomplete-rollback).

Local project configuration is strictly schema version 2 or version 3 when it
contains lifecycle hooks. A version 1
`.wtree.yml` is rejected with reinitialization guidance; it is never silently
rewritten. Hook-free portable manifests remain version 2; hook-bearing portable
manifests are explicitly version 3. Global configuration,
workspace state/plans, registry, and recovery records retain their established
versions.

Run `wtree --how-to` for the installed workflow guide, or
`wtree <command> --help` for the full command reference.

## Learn and contribute

- The [hands-on tutorial](tutorial/README.md) walks through the portable clone
  workflow and the existing workspace lifecycle commands.
- The [all-commands tutorial](tutorial/ALL-COMMANDS.md) covers publisher,
  consumer, registry, recovery, safety, and partial-import situations and has
  an executable end-to-end check.
- See [Troubleshooting](docs/TROUBLESHOOTING.md) for duplicate project
  registrations, workspace drift, dirty worktrees, and incomplete rollbacks.
- See the [AI-assisted delivery process](docs/ai/README.md) for this
  repository's planning, implementation, independent-review, and verification
  loop.

### Testing locally

Use `make check-local` for fast local feedback: it formats and vets the tree,
runs the short suite, bounded integration smoke checks, runner contracts, and
a build. For a changed area, use `make test-changed BASE_REF=<commit>`; select
race-sensitive packages deliberately with
`make test-changed-race PACKAGES='./path/...'`. The race lane is for process,
shared-state, filesystem, lock, cancellation, or similar concurrency risk; a
path name alone is not enough to select it.

`make test-full` and `make test-full-race` are exhaustive normal and race
lanes. `make test`, `make test-race`, `make check`, and `make check-full`
retain exhaustive compatibility meanings. `TEST_JOBS` bounds runner-owned
commands from 1 through 4 and defaults to 4.

Iterate with focused owning tests and use `check-local`, changed-area normal,
and a selected focused race check for a complete milestone submission. A frozen
candidate needs a complete normal run; terminal evidence also includes complete
race, tutorial, and release checks. An exception requires explicit user
authorization recorded in that plan and its durable ledger. See
[plan-authoring guidance](docs/ai/plan-authoring.md#tiered-verification-for-local-test-plans)
for the complete verification and amendment rules.

## License

`wtree` is licensed under the [MIT License](LICENSE). Copyright © 2026 Define
Business LTD. See [NOTICE](NOTICE) for attribution.
