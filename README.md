# wtree

`wtree` manages synchronized Git workspaces across a project containing
independent, nested Git repositories.

It was created because existing worktree tools did not support the combination
of nested repositories and synchronized branch management: creating a
`feature/login` workspace should create matching branches and Git worktrees for
the parent repository and each nested repository, as one logical operation.

## What it does

`wtree` discovers the root repository and independent Git repositories nested
inside it, records their Git identities, and treats them as one project. It
then creates, imports, restores, inspects, and safely removes complete
workspaces.

Repository identity is based on Git's common directory, not the checkout
directory name. A nested repository can therefore be mounted under a different
name in a workspace without losing its identity.

For example, a project like this:

```text
product/
├── .git/
└── backend/
    └── .git/
```

can have a synchronized `feature/login` workspace whose nested checkout is
mounted as `api/` instead of `backend/`:

```text
feature-login/
├── .git
└── api/
    └── .git
```

Both the parent repository and `backend` use the `feature/login` branch.

## Why it exists

Common IDEs and Git worktree tools generally do not support a project made of
an outer repository plus independent repositories nested inside it. In
particular, they cannot create and manage one synchronized worktree across all
of those repositories. `wtree` was built to fill that gap. Easy, reliable
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
current branch to the intended upstream. `wtree init` then discovers the
independent nested repositories, automatically protects every nested mount in
its immediate parent `.gitignore`, writes ignored local `.wtree.yml`, and
writes portable `project.wtree.yml`.

```sh
cd ~/code/product
wtree init
git add .gitignore project.wtree.yml
```

`project.wtree.yml` is a portable, reviewable authoring artifact. `init` never
stages, commits, or pushes; review and commit the portable manifest and any
automatic `.gitignore` changes yourself.

Portable manifests use schema version 2. The `project.base_repository` field
names the sole root checkout, which continues to contain the tracked manifest
and local `.wtree.yml` metadata:

```yaml
version: 2
project:
  id: product
  name: product
  base_repository: root
repositories:
  root:
    parent: ""
    mount: .
    # clone, upstream, identity, and default_branch are written by `wtree init`
```

Clone a published project from a local manifest file or an HTTP(S) URL. The
optional destination defaults to the manifest's safe project name. A dry run
reads the manifest, checks every remote, and renders the exact-commit plan
without creating directories or local state:

```sh
wtree clone ./project.wtree.yml ./product --dry-run
wtree clone ./project.wtree.yml ./product
```

The clone is verified and registered as the `default` workspace. `update`,
`sync`, and release-lock manifests remain future ideas; they are not commands
in this release.

Choose where newly created workspaces live (this is optional; otherwise the
platform default is used):

```sh
wtree config set worktrees.root ~/code/worktrees
```

Create matching branches and worktrees for the parent and nested repositories:

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
state:

```sh
wtree status feature/login
wtree status feature/login --json
```

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

Run `wtree --how-to` for the installed workflow guide, or
`wtree <command> --help` for the full command reference.

## Learn and contribute

- The [hands-on tutorial](tutorial/README.md) walks through the portable clone
  workflow and the existing workspace lifecycle commands.
- See [Troubleshooting](docs/TROUBLESHOOTING.md) for duplicate project
  registrations, workspace drift, dirty worktrees, and incomplete rollbacks.
- See the [AI-assisted delivery process](docs/ai/README.md) for this
  repository's planning, implementation, independent-review, and verification
  loop.

## License

`wtree` is licensed under the [MIT License](LICENSE). Copyright © 2026 Define
Business LTD. See [NOTICE](NOTICE) for attribution.
