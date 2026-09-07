# Implementation context — companion repositories

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Companion repositories implementation plan](companion-repositories.md)
Source specification: [Companion repositories specification](../spec/companion-repositories.md)
Captured: 2026-09-04
Captured repository head: `b06d8ad0cd88` on `feat/release-lock`

## 1. Purpose and precedence

This dump preserves current-code evidence and integration hazards that an
implementer or reviewer would otherwise need to rediscover. It is not a second
specification. The source specification owns behavior and the parent plan owns
scope, sequencing, verification, and supervision. Both take precedence when
this context becomes stale or conflicts with them.

The capture includes uncommitted lifecycle-document changes for this feature.
The commit above is a baseline locator, not a claim that those documents or
future implementation are committed. Execution must inspect the current
shared filesystem and preserve unrelated user changes.

## 2. Agreed behavior in one view

| Concern | Required outcome |
|---|---|
| Membership | A companion is a full member of the existing repository forest and all normal workspace lifecycle/inspection operations. |
| Role | Per-repository `companion: true`; ordinary when absent/false. |
| Baseline | Existing `default_branch`; no second branch field. |
| Clone/create | Default clone checks out the baseline; named create makes the workspace-named branch from the baseline; `--from` applies only to ordinary repositories. |
| Writable work | Users may commit and push companion workspace branches; a valid descendant of recorded HEAD is informational `advanced`, not drift. A workspace branch may track its own ref on the configured remote. |
| Baseline edit | `wtree repo branch <id> <branch>` changes portable/local future baseline only; it does not switch existing workspaces or commit the manifest. |
| Exec | Default all, `--no-companions`, or exactly one `--repository`; no directory change and no general grouping language. |
| Cross-workspace update | `wtree companion update <id>` fetches once, fast-forwards the baseline, then best-effort fast-forwards safe present checkout branches. |
| Delete | Delete workspace-specific local branches only; preserve current baseline and every remote branch. |
| Integration | Companion update never merges, rebases, resets, forces, pushes, or implicitly updates removed workspaces. |

## 3. Configuration and domain seams

### 3.1 Current schemas

`internal/config/config.go` currently has:

- local `ProjectConfigVersion = 2`;
- `ProjectConfigVersion3 = 3` in `internal/config/hooks.go`;
- strict dispatch through a v2 wire type without hooks and the full v3 type;
- repository fields `source`, `parent`, `mount`, and `default_branch`; and
- a top-level local `hooks` map available only in v3.

`internal/config/portable_manifest.go` similarly has portable v2 plus
`PortableManifestVersion3 = 3`, with v3 adding `hooks` and `shared_hooks`.
`PortableRepository` currently carries clone/upstream/identity/parent/mount/
default-branch fields. Canonical YAML is hand-built near the bottom of that
file. Repository keys and identity commits are sorted while hook declaration
order is preserved.

V2 and v3 are deliberately strict. Adding `Companion` directly to the shared
in-memory type without separate v4 decode/wire handling would make the new
field accidentally acceptable or serializable under an older version. V4
must retain v3 hooks while v2/v3 wire structs omit the role. Audit version
guards in `internal/config/files.go`, hook share/install code, clone/update
builders, release materialization, tests, and comments that still say only
v2/v3.

Portable validation currently requires `upstream.branch == default_branch`.
The baseline command must update both portable fields. `upstream.merge` is a
separate configured remote ref and is preserved by the command.

### 3.2 Domain and snapshots

`internal/domain/project.go` defines `Repository` without a role field and
validates the repository forest. Add one normalized boolean there and ensure
all constructors/copies preserve it. Likely consumers include:

- `internal/service/resolve.go`;
- drift snapshots and comparisons;
- clone planning/execution and registry facts;
- update collect/plan/execute/publication/recovery;
- create/hook planning;
- status/doctor/fetch/push; and
- release lock/materialization.

Workspace state in `internal/store/store.go` already records repository ID,
branch, mount, resolved path, HEAD, and detached state. Do not add the role or
baseline to it. This keeps config as role authority and avoids a store-version
migration.

## 4. Workspace create and lifecycle seams

### 4.1 Current planner

`internal/service/plan.go` currently applies one source selection uniformly:
the request workspace name becomes each target branch, and either HEAD or
`--from` supplies each base. `internal/plan/plan.go` exposes plan v1 with each
repository's ID, parent, base, branch, mount, and path. Its create validation
expects two steps per repository.

Implement companion base selection inside the service planner before its
existing all-repository preflight. The same immutable plan is consumed by
dry-run, create execution, result validation, and lifecycle hooks. Adding a
role/baseline fact must not cause CLI and executor to derive different plans.
Preserve the existing target branch name so retained checkout logic continues
to match workspace state.

`internal/service/create.go` replans under the project mutation lock, creates
branches and linked worktrees through reversible transaction steps, validates
the complete result, and publishes workspace state. Its rollback deletes only
the branch it created. Extend its tests rather than introducing a companion
transaction.

### 4.2 Remove, checkout, and delete

`internal/service/remove.go` removes worktrees child-first and retains branch
and state. `internal/service/workspace.go` restores retained checkouts and
requires recorded branch/path facts. Workspace-named companion branches fit
that contract without a second state model.

`internal/service/delete.go` currently creates one `DeletionBranch` for every
removed checkout and calls local `Git.DeleteBranch`; it does not delete remote
branches. It verifies branch existence, exact recorded head, other-worktree
use, and merged status before mutation. Baseline preservation needs to be
represented in planning/results so execution steps simply omit protected
branches. Do not make `--force` capable of bypassing that protection.

The default workspace and a later baseline change create the important edge
cases: the recorded checkout branch may equal the current baseline, or an old
default branch may no longer be the baseline. Protect the current configured
baseline, not every historical default branch.

## 5. Descendant HEAD and aggregate-command seams

`internal/service/exec.go` currently preflights all present checkouts before
starting any process. It requires exact repository identity, top-level path,
attached recorded branch, and `actual HEAD == checkout.Head`. Its result v1
lists every present repository parent-first and `--reverse` changes execution
order only.

Exact HEAD equality conflicts with the agreed ability to commit in a companion
and then run tools. Select repositories before preflight, and for companions
prove that persisted HEAD is reachable from observed HEAD. Continue rejecting
branch switches, detached state, rewritten/unrelated history, path mismatch,
and identity mismatch. Report observed HEAD and use it for `WTREE_COMMIT`.

Other command behavior is not uniform:

- `internal/service/status.go` observes current HEAD and currently sets
  `HeadMismatch` whenever it differs from state, which later creates local
  drift. For a valid companion descendant it must instead set additive
  `HeadAdvanced`, clear structural `HeadMismatch`, and summarize clean
  descendants as `advanced`. Dirt or structural faults retain precedence.
- `internal/service/fetch.go` currently requires exact persisted HEAD and a
  checkout-configured upstream before fetch. Apply only the narrow companion
  descendant relaxation required by the spec; do not make unrelated histories
  fetchable.
- `internal/service/push.go` already treats stored checkout HEAD as historical
  metadata: it appends it to required reachable commits and reports current
  HEAD. This is the useful model to reuse, not code to duplicate. Its current
  upstream check also requires `upstream.Merge` to equal the manifest baseline
  merge ref. For companions retain the actual local-branch, remote-name, and
  fetch-URL checks but allow the workspace branch's own merge ref on that
  remote. Ordinary repositories keep exact merge-ref equality.
- `doctor` consumes status/drift inventories and must not propose destructive
  repair merely because a companion advanced normally.

The CLI entry for exec is `internal/cli/exec.go`. It already resolves a
current or `--workspace` workspace, supports reverse/dry-run/JSON, streams
per-repository human results, and invokes direct argv without a shell. Add only
the two agreed selectors and enforce mutual exclusion before service launch.
Repeated selectors and an ordinary-only selection that resolves to no
repositories are invalid arguments, not successful no-ops.

## 6. Baseline configuration command seams

The `repo` command group is built in `internal/cli/workspace.go` and currently
contains repository inspection/path behavior. The branch subcommand belongs
there, but config mutation belongs in a service.

The mutation changes two files with different data:

- tracked `project.wtree.yml`: repository `default_branch` and
  `upstream.branch`; and
- ignored `.wtree.yml`: repository `default_branch`.

Use the same manifest-path authority, exact-byte generation capture, project
lock, atomic write, rollback, and recovery patterns already used by update and
hook management. The command must preserve v4 companion fields, hook maps,
remote/merge/identity/topology values, and unrelated local settings. It should
validate an existing local branch through the registered source/common Git
directory and must not contact the network or alter any ref.

No-op behavior deserves explicit tests: identical baseline input should not
rewrite bytes, timestamps where observable, or recovery/state files.

## 7. Companion-update Git and state seams

### 7.1 Existing Git capabilities

`internal/git/adapter.go` exposes configured-ref observation/fetch/restore and
`FastForward`/`RestoreFastForward`. `FetchConfiguredRef` can return an owned
receipt with an error because Git may update the remote-tracking ref before a
late failure or cancellation; callers must consume that receipt.

`internal/git/aggregate.go` `FastForward` is intentionally limited to the
currently attached clean branch. It validates the expected old/new commits,
proves ancestry, compare-and-swaps the ref, materializes the worktree without
reset, suppresses hooks, and has ownership-sensitive cleanup.

Companion update must also advance a baseline or workspace branch that may be
inactive in the registered source checkout. Add the narrowest ref-only CAS
operation for inactive branches rather than weakening `FastForward`'s attached
worktree contract. For present checked-out branches, retain clean worktree and
materialization protection. Both paths must prove old-generation ownership and
ancestry and must never force.

### 7.2 Workspace enumeration and partial success

`internal/service/workspace.go` `ListWorkspaces` loads strict state and returns
default first, then named workspaces lexically, but aborts at the first
unreadable, malformed, or invalid file. Companion update needs a narrow
independent inventory that retains that strict per-file validation while
turning each bad file into a failed result and continuing with valid records.
When decode cannot supply a workspace name, use the storage filename as its
stable display identifier. Do not weaken `ListWorkspaces` for existing
consumers merely to obtain best effort here.

Use valid state as workspace authority, then inspect the selected repository's
recorded path and on-disk facts. Removed valid workspaces retain state but have
no checkout and are excluded from results. Invalid state files remain failed
entries because absence cannot be established safely.

The command should fetch once, settle the baseline, then process present
workspace checkouts in deterministic order. If Git reports the baseline as
checked out, correlate that path to exactly one valid state record. Its branch,
materialized checkout, and stored HEAD are one rollback-capable baseline unit;
state failure restores the branch/worktree and aborts before later workspace
updates. The fetched tracking ref stays fetched. A missing/invalid state owner
for an active baseline is a baseline-level failure. An inactive baseline has
no state update. The active owner's stored HEAD is synchronized even when its
branch already equals the fetched baseline.

After baseline success, each remaining checkout is an independent best-effort
unit and the active-baseline owner is excluded. Update workspace state HEAD
after a successful fast-forward so normal state consumers see the last
wtree-published generation. If state publication fails, restore that entry's
branch/worktree and continue after a clean rollback. Record recovery and stop
later mutation after an incomplete rollback. A later user commit may again
make current HEAD a valid descendant.

An unchanged workspace branch can still have an older persisted HEAD. Publish
the observed descendant HEAD for that completed entry; if publication fails,
the branch already remains unchanged and the command can report failure and
continue.

Do not retrofit companion update into `internal/service/update_*`.
Project update owns configuration/membership reconciliation and a rollback
journal; companion update owns one repository's branch advancement and
intentionally retains earlier per-workspace successes.

## 8. Clone, update, hooks, and release integration

- Clone planning/execution in `internal/service/clone_plan.go` and
  `clone_execute.go` checks out each portable default branch and writes local
  configuration/default workspace state. Carry v4 role through those existing
  paths; the companion clone branch selection already matches default-branch
  behavior.
- Update spans `update_collect.go`, `update_plan.go`, `update_execute.go`, and
  `update_publication.go`. Role/baseline changes must appear in drift and
  publication while existing named branches remain untouched. Audit every
  baseline/snapshot copy and rollback comparison.
- Hook v3 implementation is spread across `internal/config/hooks.go`, hook
  management services, `create_hooks.go`, and `clone_hooks.go`. V4 must retain
  the same hook trust/execution model and must survive share/install/retry and
  update without field loss.
- Release locking in `internal/service/release_lock.go` already walks project
  repositories and excludes only the base where the release contract says so.
  Do not add a companion exclusion. Release materialization must preserve role
  when it emits local config/state.

## 9. Public results and test seams

Use the exact public contracts fixed in specification §9:

- keep workspace-plan v1 and add optional companion/baseline repository facts;
- keep exec/fetch/push-readiness v1 and add optional companion facts;
- keep status unversioned and add companion/baseline/head-advanced facts;
- add preserved/reason facts to existing deletion branch rows;
- use JSON v1 for `repo-branch` and `companion-update`; and
- use only the specified companion-update actions, statuses, and reason codes.

Ordinary rows omit every additive companion field so existing JSON remains
unchanged. Do not defer wire-version or status vocabulary choices to an
implementer.

High-value existing test locations include:

- `internal/config/*test.go` for strict versions, canonical bytes, fuzzing, and
  hook preservation;
- `internal/service/plan_test.go`, `create_test.go`, forest/internal rollback
  tests, and CLI create tests;
- `remove_test.go`, `delete_test.go`, `workspace_test.go`, and forest lifecycle
  tests;
- `status*_test.go`, `doctor*_test.go`, `fetch_test.go`, `push*_test.go`, and
  `exec_test.go` in service and CLI packages;
- `internal/git/aggregate*_test.go` for real/fake fast-forward, stale-ref,
  hook-suppression, and cleanup behavior;
- clone/update/release/hook tests for propagation; and
- `scripts/tutorial-test.sh`, `scripts/release-test.sh`, and README/reference
  examples for public acceptance.

Use temporary bare/local remotes for fetch behavior. Do not require network,
credentials, user Git identity, global config, or real user state/data roots.

## 10. Hazards and review checklist

- Do not allow older strict schemas to accept `companion` because the shared Go
  struct gained a field.
- Do not lose companion fields when hook share/install or update reconstructs
  configuration.
- Do not let CLI dry-run and locked execution derive different companion
  bases.
- Do not apply `--from` to a companion or the companion baseline to an
  ordinary repository.
- Do not reject ordinary local companion commits through exact stored-HEAD
  checks, but do not accept rewritten history.
- Do not classify a valid descendant as `headMismatch`/local drift, hide it as
  ordinary clean, or let it override dirt and structural errors; report
  informational `advanced`.
- Do not require a companion workspace branch's upstream merge ref to equal
  the baseline merge ref. Do continue to require its configured remote name
  and fetch URL.
- Do not preflight unselected exec repositories.
- Do not treat an empty `--no-companions` selection as successful execution.
- Do not delete the current baseline, even with force; never invoke remote
  deletion.
- Do not fetch more than once per companion update or use checkout-configured
  upstream as a substitute for manifest authority.
- Do not let one invalid workspace-state file prevent valid independent
  updates, except when it prevents ownership of the checked-out baseline from
  being established.
- Do not use a checked-out-worktree fast-forward primitive for an inactive
  branch, or a ref-only primitive for an active dirty worktree.
- Do not update an active baseline without atomically publishing its workspace
  state, or process its owning workspace a second time.
- Do not roll back successful workspace updates after a later independent
  failure; do not hide partial failure behind exit zero.
- Do not mutate removed workspaces or implicitly restore them.
- Do not turn a baseline config change into an existing-workspace branch
  switch or drift repair.
- Preserve forest parent/child order, path containment, symlink/identity
  checks, project locks, cancellation, output failure semantics, and recovery
  ownership.

## 11. Explicit exclusions

No work in this plan should add automatic integration, conflict resolution,
push/publication, remote deletion, independent clones, arbitrary repository
groups, role inference, extra branch fields, or implicit companion updates in
another command. Commit, push, pull-request, release, and deployment activity
remains separately authorized.
