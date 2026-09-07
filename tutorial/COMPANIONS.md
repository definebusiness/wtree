# Companion repositories: independent baseline workflow

This executable tutorial demonstrates one ordinary project with `backend` as a
companion repository. It uses a temporary directory whose paths contain a
space and a Unicode character, disposable local bare Git remotes, and an
isolated `WTREE_DATA_HOME`. It never contacts a network service, reads a user
Git configuration, publishes to an external/live repository, or changes a
real project. Fixture setup does commit and push only to those disposable local
bare origins to seed the hermetic scenario.

Run its checked counterpart from the source root:

```sh
./tutorial/run-companion-commands.sh
```

`make tutorial-test` includes this runner, the general all-command tutorial,
the hook tutorial, and the release tutorial.

## Adopt version 4 deliberately

In the tracked portable manifest, set version 4 and mark only the repository
whose independent baseline is intended:

```yaml
version: 4
repositories:
  backend:
    companion: true
    default_branch: main
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
```

Then run the ordinary tracked-manifest update workflow. `companion: true` is
strictly a v4 field; v2 and v3 reject it. Omission or `false` remains an
ordinary repository. The existing `default_branch` is the sole companion
baseline: there is no second baseline field and no role-editing command.

## Create a mixed workspace

An ordinary repository honors `--from`; a companion always starts its
workspace branch from its configured local baseline. Preview before changing
anything:

```sh
wtree create feature/search --from feature/customer-search --dry-run --json
wtree create feature/search --from feature/customer-search
```

The plan JSON remains version 1. Companion repository rows add only
`"companion":true` and `"baseline":"main"`; ordinary rows omit both fields.
The real command creates no separate companion clone. It creates the normal
workspace-named local branch for every repository, with the companion branch
based on `default_branch`.

A local commit on an attached companion workspace branch is valid. `status`
reports it as informational `advanced` and JSON adds `headAdvanced: true`,
the persisted `expectedHead`, and observed `head`. Rewritten, detached, dirty,
identity, branch, and mount failures remain failures.

## Select execution scope explicitly

```sh
wtree exec -- git status --short
wtree exec --no-companions -- go test ./...
wtree exec --repository backend -- git status --short
```

The default selects every present repository. `--no-companions` selects only
present ordinary repositories, while `--repository` selects exactly one
configured present repository. The selectors are mutually exclusive; an empty
ordinary-only selection is an invalid-arguments error before a child program
starts. `exec` uses direct argv, not an implicit shell, and does not roll back
effects made by the selected program.

## Change a future baseline and update present workspaces

Choose an existing local branch without fetching or switching a checkout:

```sh
wtree repo branch backend release/2026-q3 --dry-run --json
wtree repo branch backend release/2026-q3 --json
```

The `repo-branch` JSON v1 envelope contains `version`, `operation`, `status`,
`dryRun`, `projectId`, `repositoryId`, `previousBaseline`, `baseline`,
`portableChanged`, and `localChanged`; a settled failure adds `failure`.
Changing a baseline is atomic between local and portable configuration, is
future-facing, does not stage or commit the manifest, and does not switch,
merge, reset, delete, push, or fetch any branch.

To bring one configured companion baseline forward across its present
workspaces, use the distinct best-effort command:

```sh
wtree companion update backend --dry-run --json
wtree companion update backend --json
```

Its JSON v1 envelope identifies the selected baseline and ordered baseline or
workspace entries. Each entry reports `action` (`fast-forward`, `unchanged`,
or `none`) and `status` (`planned`, `completed`, `failed`, or `canceled`). A
valid removed workspace has no entry; malformed retained state is reported.
The command fetches only the configured companion ref once when executing. It
never creates a missing worktree, merges, rebases, resets, force-updates,
pushes, or deletes a ref. Independent safe entries may complete before a later
failure; a rollback-incomplete result records recovery and stops later work.

## Retain, restore, delete, and lock the composition

```sh
wtree remove feature/search
wtree checkout feature/search
wtree delete feature/search --dry-run --json
wtree delete feature/search
wtree release lock v1.4.0 --dry-run --json
```

`remove` retains the companion branch and workspace state. `checkout` restores
that recorded branch. `delete` only considers workspace-specific local
branches; it preserves the configured companion baseline even with `--force`,
never deletes a remote branch, and reports a retained baseline as
`preserved: true` with `reason: "companion-baseline"`. A release lock includes
the companion's exact commit like every other non-base repository. Locking is
source composition only: committing, tagging, publishing, packaging,
deploying, and notification remain explicit caller or CI work.

## Boundaries and recovery

Companions do not add automatic integration, general grouping, per-companion
clone storage, implicit update during create, checkout, fetch, or project
update, or credential handling. `wtree` stores no credentials. If
configuration publication or a per-workspace rollback cannot prove owned
restoration, the command leaves recovery evidence rather than guessing;
inspect `wtree doctor` before retrying a mutation.
