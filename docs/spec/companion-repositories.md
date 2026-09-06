# Companion repositories specification

Status: planned
Source idea: [Companion repositories with independent baselines](../ideas/companion-repositories.md)
Implementation plan: [Companion repositories implementation plan](../plans/companion-repositories.md)
Traceability companion: [Companion repositories traceability](companion-repositories.traceability.md)

## 1. Purpose

This specification adds companion repositories to a multi-repository `wtree`
project. A companion is a normal managed repository used for material such as
development tools, a test harness, or agent-facing process documentation, but
its branch is based on its own configured baseline instead of the synchronized
source selected for the other repositories.

Companions remain part of one repository forest and one workspace lifecycle.
They do not introduce separate clones, unmanaged directories, automatic
integration, or a second workspace model.

## 2. Scope

This delivery includes:

- a per-repository `companion` marker in strict local and portable v4
  configuration;
- companion-aware clone, create, checkout, remove, delete, status, doctor,
  fetch, push-readiness, update, and release-lock behavior;
- `wtree repo branch <repository> <branch>` to change a companion baseline for
  future workspaces;
- `wtree exec --no-companions` and
  `wtree exec --repository <repository>` selection;
- `wtree companion update <repository>` to best-effort fast-forward the
  selected companion across present workspaces; and
- human, dry-run, JSON, help, tutorial, and migration documentation for these
  public contracts.

The existing behavior for repositories without the marker remains unchanged.

## 3. Repository and branch model

### 3.1 Role and baseline

A repository is a companion when its configuration contains
`companion: true`. Omission or `false` means an ordinary synchronized
repository. The role is independent of forest parentage and mount placement.

The existing `default_branch` is the companion baseline; there is no second
baseline field. The existing portable invariant that `upstream.branch` equals
`default_branch` remains in force. `upstream.remote` and `upstream.merge`
continue to identify the configured remote ref from which that local baseline
is fetched.

The default workspace created by `wtree clone` checks out the baseline. A
named workspace gets its normal workspace-named local branch in the companion
repository, but that branch is created from the current baseline tip. This
keeps each checkout writable and respects Git's rule that a local branch
cannot be checked out in multiple linked worktrees.

### 3.2 Create source selection

For an ordinary repository, existing create behavior is unchanged: the
workspace branch starts from the resolved command-wide source, including an
explicit `--from` when supplied.

For a companion, create always resolves `refs/heads/<default_branch>` in the
registered source repository and uses that commit as the base. `--from` does
not override a companion baseline. The complete dry-run and JSON plan expose
the resolved base, resulting branch, repository role, and steps before any
mutation.

The normal all-repository preflight remains atomic. An invalid or missing
companion baseline, an existing target branch, a conflicting linked worktree,
or any other repository failure prevents all create mutations.

### 3.3 Writable workspace branches

A developer may commit to and push a companion's workspace branch using
ordinary Git. Persisted workspace `head` remains the checkout generation last
published by `wtree`, not a prohibition on later commits.

Commands that operate on a companion checkout accept a current HEAD that is a
descendant of its persisted HEAD when identity, path, attachment, and recorded
branch still match. They report the observed HEAD. Rewritten or unrelated
history is not silently accepted. Ordinary repositories retain their existing
command-specific validation rules.

Status reports this valid difference as informational `advanced`, with
`headAdvanced: true`, the persisted `expectedHead`, and the observed `head`.
It does not set structural `headMismatch`, add local drift, or make status
fail. Dirt, attachment, identity, mount, or branch problems still take
precedence over `advanced`.

A companion workspace branch may track any branch on the companion's
configured upstream remote. Push readiness requires the actual upstream's
local branch to match the checkout branch and its remote name and fetch URL to
match the portable manifest, but it does not require the workspace branch's
remote merge ref to equal the baseline's `upstream.merge`. This changes only
readiness inspection; `wtree` still performs no push.

## 4. Versioned configuration

### 4.1 Portable manifest v4

Portable versions two and three remain strict and unchanged. In particular,
they reject `companion` as an unknown field. Portable version four retains all
v3 hook and shared-hook fields and adds the optional repository field:

```yaml
version: 4
repositories:
  tools:
    companion: true
    default_branch: main
    # Existing clone, upstream, identity, parent, and mount fields remain.
```

Canonical v4 output emits `companion: true` for companions and omits a false
value. Repository ordering, identity-root ordering, hook ordering, and all
other canonical guarantees remain unchanged. A v4 document with no companions
is valid, but ordinary writers do not upgrade a file solely because v4 exists.

### 4.2 Local project configuration v4

Local versions two and three likewise remain strict and unchanged. Local v4
retains v3 local hooks and adds the same optional `companion` repository field.
Clone and update propagate the portable role into local configuration. Local
and portable versions remain independently validated, but a local
configuration representing a portable companion must use v4.

The domain repository carries the resolved role so services do not repeatedly
interpret wire configuration. Workspace-state and registry formats do not
gain a role field: project configuration is the role authority, while existing
checkout state remains the authority for workspace branch, path, and last
published HEAD.

### 4.3 Existing-project adoption

Existing projects opt in by changing the tracked manifest to v4 and marking
the repository, then running the existing `wtree update` workflow. `init` does
not infer companions. This feature adds no role-editing command.

Update must propagate role and baseline changes through its existing
generation checks, journaling, rollback, and recovery rules. A changed
companion baseline or role affects future creates; update does not switch,
rewrite, merge, or delete existing named workspace branches.

## 5. Companion baseline command

The command is:

```text
wtree repo branch <repository> <branch> [--dry-run] [--json]
```

It has these rules:

- `<repository>` must identify a configured companion.
- `<branch>` must be a valid, existing local branch in that repository's
  registered Git common directory and must satisfy existing repository
  identity requirements. The command performs no fetch and creates no branch.
- The planned mutation changes `default_branch` and `upstream.branch` for the
  repository in the portable manifest and matching local configuration.
  Existing remote names, merge refs, identities, topology, mounts, hooks, and
  unrelated bytes or semantic values are preserved according to their
  canonical-format contracts.
- Dry-run validates and renders the complete change without writing.
- Real execution takes normal project mutation authority, revalidates exact
  source generations and Git facts, and publishes both configuration files
  with the existing atomic/CAS and recovery guarantees. A failure cannot leave
  portable and local configuration silently disagreeing.
- The command neither stages nor commits the tracked manifest and does not
  switch any checkout. It never fetches, pushes, deletes, merges, rebases, or
  resets a branch.

Changing the baseline applies to later creates and later companion-update
invocations. Existing workspace state and branches remain unchanged.

## 6. Workspace lifecycle

Companions participate in the entire existing lifecycle:

- `clone` acquires the repository and checks out its baseline in the default
  workspace.
- `create` creates its workspace branch from that baseline.
- `remove` removes its worktree but retains its branch and workspace state.
- `checkout` restores the recorded companion branch and recorded checkout.
- `delete` removes its workspace-specific local branch when safe and removes
  workspace state.
- `status`, `doctor`, `fetch`, and push-readiness inspect companions with the
  same identity, topology, remote-identity, and recovery protections, adjusted
  for valid companion branch advancement and branch-specific companion
  upstreams as described in §3.3.
- `release lock` records the companion's exact commit like every other
  non-base repository.

Delete never deletes a remote branch. It also never deletes the companion's
currently configured baseline, even when that branch is named by retained
state; the deletion plan and results report the preserved branch explicitly.
Existing force semantics remain limited to local dirty/unmerged workspace
cleanup and do not override baseline preservation.

Changing a baseline does not by itself make an otherwise valid existing
companion workspace drifted. Status and doctor report configured baseline and
observed checkout facts without prescribing or performing integration.

## 7. Exec repository selection

The unchanged default executes in every repository present in the selected
workspace:

```text
wtree exec [--workspace <workspace>] -- <program> [arguments...]
```

Two mutually exclusive selectors are added:

```text
wtree exec [--workspace <workspace>] --no-companions -- <program> [arguments...]
wtree exec [--workspace <workspace>] --repository <repository> -- <program> [arguments...]
```

`--no-companions` selects all present ordinary repositories in forest order.
`--repository` selects exactly one configured, present repository regardless
of role. An unknown, absent, or duplicate/empty selection is an argument or
validation error before process launch. Supplying both selectors is invalid.
If `--no-companions` selects no repositories, exec fails before process launch
with an invalid-arguments error; it is not a successful no-op.

Only selected repositories are preflighted and included in `repositories` and
`executionOrder`; an unselected broken checkout does not block execution.
Existing `--reverse`, `--dry-run`, direct-argv, environment, cancellation,
streaming, and fail-fast behavior applies to the selected set. Reverse has no
observable ordering effect for a single repository.

## 8. Updating one companion across present workspaces

The command is:

```text
wtree companion update <repository> [--dry-run] [--json]
```

It accepts exactly one configured companion ID. It is distinct from
`wtree update`, which reconciles project configuration and repository
membership.

### 8.1 Planning and order

The command strictly resolves the project, manifest, configured upstream, and
project-level recovery authority. It inspects workspace-state files
independently so one unreadable, malformed, or project-incompatible workspace
record becomes a failed workspace entry without preventing valid workspaces
from being considered. When such a file cannot supply a workspace name, its
storage filename identifies the result entry.

Only checkouts currently present on disk are candidates for branch mutation.
Removed workspaces are otherwise outside the operation. Invalid state files
remain visible as failures even when their checkout presence cannot be safely
established. Results are deterministic: baseline first, then workspace entries
with `default` first and named or storage-file identifiers lexically.

Dry-run performs all local observations and remote-ref planning available
without fetching or mutating. It clearly identifies facts that require the
real fetch, and writes no ref, worktree, state, configuration, or recovery
data.

### 8.2 Baseline acquisition

Real execution acquires project mutation authority, revalidates the plan, and
fetches the configured `upstream.remote` and `upstream.merge` once through the
existing authenticated configured-ref boundary. It then fast-forwards the
local baseline from its observed old commit to the fetched commit.

If the baseline is missing, checked out dirty, rewritten, or cannot be
fast-forwarded, the command reports failure and performs no workspace-branch
updates. A fetched remote-tracking ref is not rolled back merely because a
later local fast-forward cannot proceed.

When the baseline is checked out, the command must correlate that Git
worktree to exactly one valid workspace record before mutation. The baseline
ref, materialized checkout, and that workspace's stored HEAD are one
rollback-capable operation and produce one baseline result entry, not a second
workspace attempt. Its stored HEAD is published as the resulting baseline HEAD
even when the branch already needed no ref movement. If state publication
fails, the command restores the baseline ref and checkout and stops before
other workspace updates. If that restoration is incomplete, it writes the
existing form of actionable recovery metadata and stops. The fetched
remote-tracking ref remains at the fetched generation. A checked-out baseline
with missing or invalid workspace authority is a baseline failure, not an
independently skippable workspace.

When the baseline is inactive, only its local branch ref is fast-forwarded and
no workspace state is changed.

### 8.3 Per-workspace best effort

After the baseline is current, each present companion checkout is independently
revalidated immediately before its attempted update:

- correct repository identity, top-level path, attached recorded branch, and
  absence of unresolved recovery are required;
- the working tree must be clean;
- the observed HEAD must contain its persisted HEAD;
- if the observed HEAD is behind the baseline, its branch is fast-forwarded to
  the baseline;
- if it already contains the baseline, it is reported unchanged; and
- if neither commit contains the other, it is reported diverged and unchanged.

Dirty, detached, missing, identity-mismatched, recovery-blocked, rewritten,
or diverged checkouts are failed with a stable reason while later eligible
workspaces continue. The workspace that owned an active baseline is excluded
because the baseline phase already settled it. Each remaining branch,
materialized checkout, and workspace-state HEAD update uses compare-and-swap
facts as one rollback-capable per-workspace operation. A state-write failure
restores that workspace branch and checkout, marks the entry failed, and then
continues. An incomplete per-workspace rollback writes actionable recovery
metadata, cancels remaining entries, and stops further mutation. There is no
global rollback: a later error does not undo earlier successful workspace
updates.

A completed `unchanged` workspace entry still publishes its observed HEAD when
that HEAD validly descended from the older persisted generation. Thus running
the command also synchronizes safe historical state without moving a branch.

Cancellation stops new attempts, marks remaining eligible entries canceled,
and preserves completed work. The overall command succeeds only when the
baseline and every considered present checkout are current or were safely
updated. Any failed or canceled entry produces a non-zero result.

The command never creates a missing worktree, updates a removed workspace,
performs a merge or rebase, resets or force-updates a branch, pushes, or
deletes a local or remote ref.

## 9. Results and diagnostics

Existing result versions are retained. Additive optional fields are omitted
for ordinary repositories so existing non-companion JSON remains unchanged:

- workspace plan v1 repository rows add `companion: true` and `baseline` for a
  companion;
- exec v1, fetch v1, and push-readiness v1 repository rows add
  `companion: true`;
- status repository rows add `companion: true`, `baseline`, and
  `headAdvanced: true` as applicable; status has no version field; and
- deletion branch rows add `preserved: true` and
  `reason: "companion-baseline"` for a protected baseline. Such a row is
  reported but produces no delete-branch step.

`wtree repo branch` uses a command-owned JSON v1 envelope containing
`version`, `operation: "repo-branch"`, `status`, `dryRun`, `projectId`,
`repositoryId`, `previousBaseline`, `baseline`, `portableChanged`, and
`localChanged`, plus structured `failure` when a settled result fails. Status
is `planned` for a changing dry-run, `completed` for a completed change,
`unchanged` for an exact no-op, or `failed` for a settled failure.

`wtree companion update` uses a command-owned JSON v1 envelope containing
`version`, `operation: "companion-update"`, overall `status`, `dryRun`,
`projectId`, `repositoryId`, `baseline`, ordered `entries`, and structured
failure data. Each entry identifies `kind` (`baseline` or `workspace`), an
optional workspace, branch, previous and resulting HEADs, `action`
(`fast-forward`, `unchanged`, or `none`), and `status` (`planned`,
`completed`, `failed`, or `canceled`). Failed entries use the applicable
stable reason:

- `invalid-state`, `missing`, `identity-mismatch`, `detached`,
  `branch-mismatch`, `dirty`, `recovery-blocked`, `rewritten`, `diverged`,
  `stale-generation`, or `state-publication-failed` for workspace facts; and
- `missing-baseline`, `dirty-baseline`, `diverged-baseline`, `fetch-failed`,
  `state-publication-failed`, or `rollback-incomplete` for baseline authority.

The companion-update overall status is `completed` only when the baseline and
every considered entry completed. Any failed or canceled entry makes it
`failed` and the command exits non-zero. Removed valid workspaces do not create
entries; invalid state files do.

Every new mutating command has matching human, `--dry-run`, and `--json`
surfaces. Existing result fields are not reinterpreted.

Human output identifies companions and their baseline where that distinction
explains planned or observed behavior. Errors use the existing typed error
taxonomy and keep stdout valid as a single JSON document in JSON mode.

Status follows §3.3 for valid companion advancement. It still reports dirt,
detached state, branch mismatch, missing/unrelated history, upstream state,
and ahead/behind facts.

## 10. Safety and compatibility requirements

- Portable and local v2/v3 parsing, canonical bytes, and behavior remain
  unchanged; v4 is explicit and strict.
- All project-wide mutations use the existing project lock, generation
  revalidation, identity/path containment checks, atomic writes, and recovery
  conventions appropriate to their effects.
- A failed create has no repository branch or worktree side effects. A failed
  baseline-configuration publication does not leave split configuration.
- Companion update is intentionally best-effort only after baseline
  acquisition; its per-entry output is the durable explanation of partial
  success, not a promise of global rollback.
- The checked-out baseline and each later workspace update individually couple
  ref/worktree mutation to state publication with safe rollback. An incomplete
  rollback stops later mutations and creates actionable recovery metadata.
- Tests are hermetic and never require user credentials, global Git
  configuration, or a network remote.
- Behavior is portable across supported Linux, macOS, and Windows environments.

## 11. Explicit non-goals

This feature does not add:

- automatic merges, rebases, conflict resolution, resets, or force updates;
- automatic pushes or local/remote branch publication or deletion;
- one clone per companion checkout;
- arbitrary repository groups or a general exec-selection language;
- a new baseline field, role-management command, or implicit role inference;
- implicit companion updating during create, checkout, fetch, or project
  update; or
- changes to release tagging or publishing behavior.

## 12. Acceptance criteria

The feature is complete when hermetic public tests demonstrate that:

1. strict v4 round trips companion roles and v3 hooks while v2/v3 reject the
   new field without byte or behavior regressions;
2. clone and named create select the baseline exactly as specified, including
   `--from`, forest ordering, rollback, and branch-conflict cases;
3. local companion commits remain usable through informational `advanced`
   status, exec, fetch, branch-specific same-remote push readiness,
   remove/checkout, and safe local-only delete behavior;
4. baseline changes are atomic, uncommitted, future-facing, and preserve
   existing workspaces and hook configuration;
5. exec default, ordinary-only, and single-repository scopes select and
   preflight exactly the documented repositories;
6. companion update fetches once, atomically settles an active baseline,
   advances only safe branches, continues past invalid state and other
   recoverable per-workspace failures, persists successful heads, and reports
   partial failure without duplicate processing, merges, force, push, or
   global rollback; and
7. update, recovery, release locking, hooks, help, migration documentation,
   and the executable tutorial preserve the full end-to-end contract on all
   supported platforms.
