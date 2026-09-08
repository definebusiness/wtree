# Idea: workspace name shorthand

Status: specified
Resulting specification: [Workspace name shorthand specification](../spec/workspace-name-shorthand.md)

## Summary

Allow a substring of a registered workspace name to select that workspace
when exactly one eligible workspace matches. Once resolved, proceed as if
the user supplied its full workspace name. For example,
`wtree path harden` resolves `feat/harden-loops` when it is the only match.

Substring matching is the default for `path`, `status`, and `checkout`.
Use `--exact` to explicitly select an exact workspace name instead.

## Agreed behavior

- Only `path`, `status`, and `checkout` support workspace shorthand.
  Destructive commands, including `remove` and `delete`, retain exact lookup.
- Matching is enabled by default. `--exact` disables substring matching for
  that invocation. `--json` does not change the selected matching mode.
- Search registered workspace names in the resolved project, not Git branches,
  checkout branch names, filesystem paths, or partial workspace IDs.
- Match case-sensitive, literal substrings anywhere in the full workspace
  name. Do not rank candidates or give an exact name priority.
- Exactly one eligible match selects that workspace and proceeds through the
  command's normal validation and execution.
- Multiple eligible matches fail without performing the requested action.
  The error lists the available workspace names so the user can rerun with
  an unambiguous selector or exact mode.
- There is no interactive chooser or prompt in any mode. This includes human
  terminal use, `path`, shell interpolation, redirected streams, and `--json`.
  Machine-oriented invocation still accepts a unique match.
- Removed workspaces participate in matching for `checkout` only. Exclude
  them from candidates for `path` and `status`.
- This feature does not introduce Git branch discovery. Exact checkout of an
  unregistered local branch is available through `checkout --exact`; default
  matching does not fall back to branch checkout.

For example, both `feat/foo` and `feat/foo-tests` match the input `feat/foo`.
Matching mode must report ambiguity even though one name is identical to
the input. Retyping that full name does not resolve this case; exact mode is
needed.

## Default matching and explicit exact selection

Everyday shorthand should require no additional flag:

```sh
wtree path harden
wtree status harden
wtree checkout harden
```

Use `--exact` when a full name also matches another workspace or when the
caller wants explicit exact selection:

```sh
wtree path --exact feat/foo
wtree status --exact feat/foo --json
wtree checkout --exact feat/foo
```

At the time of this decision, the project has one user and no existing scripts
to preserve. Default matching therefore prioritizes the intended everyday
convenience. Ambiguity from overlapping names is an accepted tradeoff, resolved
explicitly with `--exact`. There is no opt-in `--match` requirement.

## Specification decisions

The [specification](../spec/workspace-name-shorthand.md) resolves the design
boundaries needed for implementation:

- Default unmatched checkout fails; `checkout --exact <branch>` preserves
  checkout of an existing local branch without workspace state.
- Removed workspaces are excluded from both default and exact `path`/`status`
  selection. Absence of every recorded checkout path and its Git worktree
  registration establishes removal without a state schema change. Damaged or
  partially missing workspaces remain available for diagnosis.
- Ambiguity lists candidates deterministically. Path errors leave stdout empty;
  supported JSON commands add structured selection details to their error
  envelope. No new JSON flag is added to `path`.
- Omitted `status` retains current-workspace inspection. Required arguments
  remain required, and explicitly empty selectors fail.
- Exact mode supports full workspace names and persisted IDs with conflict
  detection. Matching mode searches names only.

## Implementation status

This idea has produced the linked specification and its
[implementation plan](../plans/workspace-name-shorthand.md). Workspace shorthand
has not been implemented, and no plan run has started.
