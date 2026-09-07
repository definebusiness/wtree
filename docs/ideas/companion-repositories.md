# Idea: companion repositories with independent baselines

Status: specified
Resulting specification: [Companion repositories specification](../spec/companion-repositories.md)

## Summary

A multi-repository project may contain a companion repository for development
tools, a test harness, or documentation for developers and coding agents. The
companion is a full project member: `wtree` clones it, creates a checkout for it
in every workspace, includes it in normal inspection and lifecycle commands,
and removes it with the rest of the workspace.

Unlike synchronized repositories, a companion has its own configured baseline
branch. Each named workspace receives a workspace-specific local branch created
from that baseline. This keeps every checkout writable without attempting to
check out the same Git branch in multiple linked worktrees.

## Repository model

The portable manifest marks a repository as a companion. Its existing
`default_branch` is its baseline; no second branch setting is needed.

```yaml
repositories:
  tools:
    companion: true
    default_branch: main
    # Existing clone, upstream, identity, parent, and mount fields remain.
```

The default cloned workspace checks out the configured companion baseline.
For a named workspace such as `feature/login`, `wtree create` creates the
companion's workspace branch from `tools/main`. The ordinary repositories keep
their existing synchronized-branch behavior. A command-wide `--from` applies
to those repositories and does not replace the companion baseline.

Developers can commit and push the companion workspace branch normally. After
those changes reach the configured baseline, later workspaces start from the
new baseline tip.

A workspace branch may track its own branch on the companion's configured
remote; it does not have to push directly to the baseline ref. Status reports
local companion commits as informational advancement rather than structural
drift.

Checking out the baseline branch itself in every linked worktree is not part of
the design. Git does not safely support one local branch being active in
multiple linked worktrees, and using independent full clones only for
companions would add a second storage and lifecycle model.

## Configuration

Wtree should provide a focused project command to change a companion baseline:

```sh
wtree repo branch tools next-tools-version
```

The command updates the portable manifest and matching local project
configuration safely but does not commit the manifest. The new baseline applies
to later `wtree create` operations. It does not silently switch or rewrite
existing workspaces.

## Workspace lifecycle

Companions participate in the existing commands as ordinary project members,
with only their branch provenance differing:

- `clone` checks out the configured baseline in the default workspace.
- `create` creates the companion workspace branch from that baseline.
- `remove` removes the companion worktree while retaining its workspace branch
  and state.
- `checkout` restores the retained companion workspace branch.
- `delete` removes the companion's workspace-specific local branch and state.
  It never deletes the configured baseline or any remote branch.
- `status`, `doctor`, `fetch`, and push-readiness inspection include companions
  and validate them against their recorded branch provenance.
- `release lock` includes the exact companion commit like any other non-base
  project repository.

Changing the configured baseline affects future workspaces only. Existing
workspace branches remain valid until explicitly updated.

## Command execution scopes

The existing default continues to execute in every present repository:

```sh
wtree exec -- make test
```

Two focused selections cover the companion use case without introducing a
general repository grouping language:

```sh
wtree exec --no-companions -- make test
wtree exec --repository tools -- make docs
```

`--repository` uses the current workspace, or the workspace selected by the
existing `--workspace` option, and does not require changing directory.
`--no-companions` fails before execution when it selects no repository rather
than reporting success after running nothing.

## Updating companions across workspaces

A dedicated command updates one companion in every currently present
workspace:

```sh
wtree companion update tools
wtree companion update tools --dry-run
wtree companion update tools --json
```

The command fetches the configured upstream, fast-forwards the local baseline,
then visits every present checkout of `tools` and fast-forwards its workspace
branch to include that baseline. A branch already containing the baseline is
left unchanged. When the baseline is checked out in a workspace, its checkout
and stored HEAD are updated atomically in the baseline phase and that workspace
is not processed twice.

This operation is deliberately best-effort. A dirty, detached, missing,
identity-mismatched, recovery-blocked, diverged, or individually invalid-state
workspace is reported and left unchanged while other eligible workspaces
continue. A safely rolled-back state-publication failure also does not stop
later workspaces; an incomplete rollback does. Successful earlier updates are
not rolled back. The overall command reports failure when any workspace could
not be safely updated.

A workspace branch with unique commits can be fast-forwarded only while the
new baseline remains its descendant. If both sides have advanced, the command
reports divergence; the developer performs the required merge or rebase
manually.

Removed workspaces have no present checkout and are not updated. Restoring one
with `wtree checkout` does not implicitly merge the baseline; the companion
update can be run again afterwards.

## Boundaries

The companion feature does not add automatic merges, rebases, resets, forced
updates, pushes, remote-branch deletion, or independent per-workspace clones.
It does not weaken the existing preflight and identity checks. Companion update
is separate from `wtree update`, whose responsibility remains reconciling the
project with its portable manifest.
