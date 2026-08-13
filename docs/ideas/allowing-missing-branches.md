# Idea: allow workspaces when a branch is missing from some repositories

Status: design idea; not an accepted specification or implementation plan.

## Summary

`wtree checkout <branch>` currently has a strict aggregate meaning: the named
local branch must already exist in every configured repository. If it is
missing anywhere, preflight fails before any worktree is created.

This is a good default because it makes a synchronized workspace predictable,
transactional, and easy to automate. It should remain the default. It does not,
however, need to be the only possible behavior. Real projects often contain a
feature that changes only one repository, while the other repositories should
remain on stable branches or may not need checkouts at all.

This document explores explicit ways to support those cases without weakening
the current safety contract.

## Current model

A normal workspace represents one logical branch across the full repository
hierarchy:

| Repository | Branch |
|---|---|
| `root` | `feature/customer-search` |
| `backend` | `feature/customer-search` |
| `frontend` | `feature/customer-search` |

The commands have deliberately different responsibilities:

- `create` creates a new branch and worktree in every repository.
- `checkout` uses branches that already exist and creates their worktrees.
- `checkout` does not silently create missing branches.
- Both commands preflight the complete operation and avoid leaving a partial
  workspace when one repository cannot participate.

This distinction is valuable and should not be blurred implicitly.

## Why strict checkout is a good default

If `checkout feature/x` silently substituted `main`, chose another ref, or
created `feature/x` where it was missing, the resulting workspace would be
ambiguous.

Possible problems include:

- Existing commits could be mistaken for synchronized feature work.
- A missing branch could be created from an unintended base revision.
- A spelling error could create branches instead of producing a diagnostic.
- Automation could no longer distinguish restoring existing work from
  creating new work.
- The workspace name could imply that every repository uses `feature/x` even
  when some use unrelated branches.
- A command that appears read-like could unexpectedly publish new local branch
  state.

Strict preflight also gives `wtree` a useful transactional guarantee. A branch
that exists in two of three repositories does not result in two worktrees and
one missing directory; no workspace is created at all.

## Why an explicit missing-branch mode is useful

Not every logical change spans every repository. Common examples include:

- A frontend-only visual experiment.
- A backend-only hotfix.
- Documentation changes confined to the parent repository.
- A release branch used only by repositories that publish release artifacts.
- A large project in which optional components should remain on their stable
  branches while one component is developed.

Requiring users to create semantically empty feature branches in every
repository adds noise. It may also encourage commits or branch protection
rules whose only purpose is to satisfy workspace tooling.

The goal is therefore not to relax ordinary checkout. The goal is to offer an
explicit operation whose result is accurately described as a mixed or partial
workspace.

## First distinction: local branches and remote-tracking branches

A branch may be absent locally while an unambiguous remote-tracking branch such
as `origin/feature/x` exists. This is not the same as the branch being genuinely
missing.

Users commonly expect checkout behavior to discover the remote-tracking branch
and create a corresponding local tracking branch. Supporting that behavior
does not invent a new line of development: it materializes a branch that is
already advertised by a configured remote.

A future resolution sequence could be:

1. Use the local branch when it exists.
2. Otherwise, look for remote-tracking branches with the requested name.
3. If exactly one eligible remote branch exists, create a local tracking
   branch from it as part of the transaction.
4. If multiple remotes advertise different candidates, fail and require the
   user to select one explicitly.
5. Only after those checks classify the branch as genuinely missing.

Remote materialization must still be included in the operation plan and
rollback. If a later repository fails, any local tracking branches created by
the attempted checkout must also be removed when safe.

The tool needs a defined meaning for "eligible remote." It should not assume
that a remote is named `origin` if the repository or project configuration
selects another remote.

## Possible behaviors for a genuinely missing branch

There are several materially different policies. A single vague
`--allow-missing` flag would not say which one the user intends.

### 1. Use a fallback branch

Repositories without the requested branch use an explicitly named fallback:

```sh
wtree checkout feature/ui \
  --missing use-ref \
  --fallback main
```

Conceptual result:

| Repository | Branch | Reason |
|---|---|---|
| `root` | `main` | fallback |
| `backend` | `main` | fallback |
| `frontend` | `feature/ui` | requested branch |

This creates a complete checkout tree, but it is a mixed-branch workspace.
Every repository remains present and independently usable.

A single fallback ref may not resolve in every repository because repositories
can have different default branches. A safer variant would use each
repository's configured default branch, or require per-repository mappings.

### 2. Map branches per repository

The most explicit interface assigns a ref to each exception:

```sh
wtree checkout feature/ui \
  --branch root=main \
  --branch backend=main \
  --branch frontend=feature/ui
```

The workspace name remains `feature/ui`, but the plan contains the exact branch
for every checkout. This is verbose, which is an advantage when correctness is
more important than convenience. A shorter form could apply the requested
branch where it exists and require mappings only for missing repositories:

```sh
wtree checkout feature/ui \
  --fallback root=main \
  --fallback backend=main
```

### 3. Create the missing branches

Another policy could check out existing branches and create only the missing
ones:

```sh
wtree checkout feature/ui \
  --missing create \
  --from main
```

This is a hybrid of `checkout` and `create`. It can be useful, but it has the
highest semantic risk because the same operation restores existing work in
some repositories and starts new work in others.

If supported, the plan must clearly mark every branch as `existing`,
`remote-tracking`, or `to be created`, including its resolved base commit.
Using `--missing create` without an explicit or well-defined base should be
rejected.

An alternative is a separately named command or mode, such as
`wtree reconcile-branch`, so ordinary checkout retains a simple contract.

### 4. Omit repositories with missing branches

A partial workspace could contain worktrees only for participating
repositories:

```sh
wtree checkout feature/ui --missing omit
```

Conceptual result:

```text
workspace/
└── frontend/    feature/ui
```

This saves space and avoids irrelevant checkouts, but it changes the expected
filesystem shape. Parent-child relationships also complicate omission: a
nested repository needs a directory in which to mount even when its configured
parent repository is absent. `wtree` must not fabricate or delete parent
content blindly.

Omission should therefore be considered separately from mixed branches. It
requires an explicitly partial workspace model and clear hierarchy rules.

## Recommended direction

An incremental design would preserve safety while addressing the most common
cases:

1. Keep strict all-repository synchronization as the default.
2. Teach `checkout` to resolve an unambiguous eligible remote-tracking branch.
3. Continue to fail transactionally when a branch is genuinely missing from
   any repository under the default mode.
4. Add an explicit mixed-branch mode based on per-repository fallback mappings.
5. Consider automatic default-branch fallback only as shorthand for a plan
   that displays every resolved branch before execution.
6. Treat "create missing branches" and "omit missing repositories" as separate
   advanced policies rather than overloading a generic flag.
7. Always present heterogeneous or partial workspace state prominently.

This sequence delivers normal Git-like remote checkout behavior first, then
adds mixed workspaces without changing what existing commands mean.

## Proposed terminology

The following terms keep distinct cases understandable:

- **Synchronized workspace:** every repository checkout uses the same logical
  branch name.
- **Mixed workspace:** every configured repository is present, but checkouts
  use different branches.
- **Partial workspace:** one or more configured repositories are intentionally
  absent.
- **Remote materialization:** creation of a local tracking branch from an
  existing, unambiguous remote-tracking branch.
- **Missing-branch creation:** creation of a genuinely new branch in a
  repository where no local or eligible remote branch exists.

`allow missing branches` is useful as an umbrella description, but the CLI and
state model should use the more precise terms.

## Planning and output requirements

Every permissive operation must retain the existing preflight-first and
transactional model. Before mutation, the plan should show at least:

| Repository | Requested ref | Resolved branch/ref | Source | Planned action |
|---|---|---|---|---|
| `root` | `feature/ui` | `main` | local fallback | add worktree |
| `backend` | `feature/ui` | `main` | local fallback | add worktree |
| `frontend` | `feature/ui` | `feature/ui` | remote `origin` | create tracking branch, add worktree |

The plan should distinguish:

- An existing local branch.
- An existing remote-tracking branch that will be materialized locally.
- A fallback branch or ref.
- A newly created missing branch and its base commit.
- An intentionally omitted repository.

`--dry-run` and JSON output must expose the same decisions. Machine-readable
output should use explicit fields rather than requiring callers to infer policy
from display strings.

## Status and inspection

`status` must never imply synchronization when the branches differ. For a mixed
workspace, human output should prominently identify the workspace type and
show the actual branch for every repository:

```text
Workspace: feature/ui
Type: mixed

REPOSITORY  REQUESTED   BRANCH      STATUS
root        feature/ui  main        clean
backend     feature/ui  main        clean
frontend    feature/ui  feature/ui  modified
```

For a partial workspace, status and JSON should list intentionally absent
repositories separately from repositories that are unexpectedly missing due
to drift. That distinction is essential for `doctor`:

- Intentionally omitted: healthy partial state.
- Expected checkout missing from disk: drift requiring diagnosis.

`list`, `repo get`, and `doctor` should also expose whether a workspace is
`synchronized`, `mixed`, or `partial`.

## State-model implications

The conceptual model already separates workspace identity from per-checkout
branch and mount. A heterogeneous workspace should use those per-checkout
branches as authoritative state rather than deriving them later from the
workspace name.

Additional durable state may include:

- Workspace kind: synchronized, mixed, or partial.
- Requested logical branch or workspace name.
- Per-repository resolved branch or detached ref.
- How each branch was resolved: local, remote materialization, fallback, or
  newly created.
- Intentionally omitted repository IDs.
- Per-repository mount and resolved path.

Restoring a removed mixed workspace with `checkout` must use the retained
per-repository mapping. It must not rerun discovery and choose different
fallbacks.

Import should infer mixed or partial state from observed checkouts and require
an explicit workspace name when one common branch name cannot be inferred.

## Removal and deletion

`remove` can retain mixed branch mappings in the same way it retains mounts.
A later checkout should restore exactly the same layout and branch selection.

`delete` needs stricter branch ownership rules:

- It may delete branches created specifically for the workspace when existing
  safeguards permit it.
- It must not delete shared fallback branches such as `main`.
- It must not assume every checkout branch equals the workspace name.
- A local branch materialized from a remote may need different deletion policy
  from a newly invented branch.

Branch provenance therefore affects safe deletion, not only presentation.

## Safety and validation rules

Any implementation should retain these rules:

- No mutation before the complete aggregate plan passes preflight.
- No silent fallback under the default checkout behavior.
- No implicit missing-branch creation from an unspecified base.
- No automatic choice between ambiguous remotes.
- No path or repository identity inference from directory names.
- No silent omission of configured repositories.
- Roll back branches and worktrees created by a failed operation when safe.
- Persist recovery information if rollback is incomplete.
- Reject incompatible parent/child omission plans before touching Git.
- Never delete fallback or pre-existing branches merely because a mixed
  workspace is deleted.

## CLI design questions

Several questions should be answered before choosing final flags:

1. Should remote materialization be the default for `checkout`, or require an
   explicit `--track-remote` option?
2. How is the eligible remote selected: project configuration, Git upstream
   configuration, a named `--remote`, or an unambiguous search?
3. Should mixed-branch behavior use `checkout` options or a separate command?
4. Is a project-wide fallback ref useful, given that repositories can have
   different default branches?
5. Should per-repository selectors use stable repository IDs exclusively?
6. Can a workspace be both mixed and partial?
7. How are descendants mounted when a parent repository is omitted?
8. Which branches does `delete` own, especially locally materialized tracking
   branches?
9. Should `create` gain a complementary option to create a branch only in a
   selected repository subset?
10. How should shells and automation distinguish an expected partial result
    from an accidental missing checkout?

## Example end-to-end mixed checkout

Given this branch availability:

| Repository | Local | Eligible remote | Desired result |
|---|---|---|---|
| `root` | `main` | no `feature/ui` | use `main` |
| `backend` | `main` | no `feature/ui` | use `main` |
| `frontend` | `main` | `origin/feature/ui` | track `feature/ui` |

An explicit command could be:

```sh
wtree checkout feature/ui \
  --fallback root=main \
  --fallback backend=main \
  --track-remote
```

Expected high-level behavior:

1. Resolve `main` locally for `root` and `backend`.
2. Resolve the sole eligible remote-tracking branch for `frontend`.
3. Render all three branch choices during dry run.
4. Create the frontend local tracking branch and all three worktrees as one
   transaction.
5. Persist the exact mixed mapping.
6. Report the workspace as mixed in `status` and `list`.
7. Restore the same mapping after `remove` followed by `checkout`.
8. On `delete`, remove only branches proven to be owned by the workspace and
   never delete either repository's `main` branch.

## Conclusion

Failing checkout when a branch is missing from any repository is a defensible,
safe, and useful default for `wtree`. It preserves the meaning of a synchronized
workspace and prevents accidental branch creation or substitution.

A mature tool can nevertheless support repositories that do not participate
in every feature. The safest path is to distinguish remote materialization,
mixed workspaces, missing-branch creation, and partial workspaces rather than
hiding all four behind `--allow-missing`. Each behavior should be explicit,
fully planned, transactionally executed, durably recorded, and unmistakable in
status output.
