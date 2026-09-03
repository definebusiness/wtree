# Delete retained workspace state implementation plan

Status: initial
Source specification: [`wtree` specification](../spec/wtree.spec.md)
Source of truth: user-reported implementation gap (2026-08-31); [`docs/spec/wtree.spec.md` §§29–31, 41–43](../spec/wtree.spec.md); [`internal/service/delete.go`](../../internal/service/delete.go); [`internal/service/remove.go`](../../internal/service/remove.go); [`internal/service/remove_grouping.go`](../../internal/service/remove_grouping.go); [`internal/service/workspace.go`](../../internal/service/workspace.go); [`internal/cli/delete.go`](../../internal/cli/delete.go)
Delivery style: test-first, one reviewed milestone at a time; no state-schema migration, implicit Git pruning, publishing, or commits

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes those decisions below.

For each unchecked milestone, in order:

1. Read this plan, the relevant source-of-truth sections, the durable run
   ledger at `docs/ai/runs/delete-retained-workspace-state.md`, and the current
   worktree. Create the ledger before the first dispatch. On resumption,
   reconcile the plan, ledger, evidence, and worktree, then append a
   reconciliation checkpoint before dispatching work.
2. Derive a complete checklist for this milestone from its scope, test-first
   slices, exit criteria, documentation requirements, and verification
   commands. Record it in the current ledger entry.
3. Give the complete initial packet to `implementer`. For remediation, use
   `implementer` when the ledger attempt count is `0` or `1`, and
   `escalation-implementer` only when it is `2`. Require RED → GREEN →
   REFACTOR evidence, files changed, verification results, and unresolved
   concerns.
4. Treat partial work as progress, not a submission. Do not request review or
   change the remediation counter until every checklist item is evidenced.
5. Send each complete submission to the read-only `reviewer`, which inspects
   the current shared filesystem, applicable sources of truth, scope, safety,
   portability, test quality, and required checks.
6. If review finds material issues, record the complete stable-ID finding set
   and return all unresolved findings in one test-first remediation packet.
   Apply the three-rejected-complete-remediation limit defined by
   `docs/ai/milestone-supervision.md`. Do not use `escalation-reviewer` as a
   routine second review.
7. On reviewer approval, run the milestone verification commands as the main
   agent, update affected documentation/contracts, check the milestone, and
   append its concise execution-log row.
8. Immediately create the next milestone ledger snapshot and dispatch its
   initial packet. Do not send a final response while work remains active.

Do not stop for a failing ordinary test, reviewer finding, partial submission,
or milestone approval. Stop only for a documented external blocker that cannot
be safely resolved within this authorized scope, or the three-rejected-complete
remediation terminal condition. Preserve unrelated user changes; do not use
destructive cleanup commands; commit only when separately authorized.

## Fixed implementation decisions

### Product behavior and accepted retained state

- `wtree delete <workspace>` accepts both an active workspace and the retained
  state left by a successful earlier `wtree remove <workspace>`. The latter
  deletes the retained local branches and workspace state without recreating
  worktrees first.
- Classify each persisted repository checkout independently during delete
  planning:
  - `active`: the recorded path exists as the expected Git worktree and Git
    registers that exact path; retain the current removal preflight, dirtiness,
    identity, branch, HEAD, hierarchy, and `--force` rules;
  - `already-removed`: the recorded path does not exist, Git has no worktree
    registration for that exact path, and the retained local branch resolves
    to the state-recorded HEAD; schedule no worktree removal for that entry.
- A mixed active/already-removed forest is supported. Active worktrees are
  removed child-first; already-removed entries are no-op worktree steps and
  remain absent during rollback. This makes retries and independently removed
  repository entries safe without weakening active-worktree validation.
- Absence alone is never sufficient authority. Reject before mutation when a
  recorded path is missing but still registered by Git, when a path exists but
  is unregistered or has replacement identity, when a retained branch is
  missing or moved from the state-recorded HEAD, or when that branch is checked
  out at any other path.
- Preserve the existing conservative branch rule: an unmerged retained branch
  requires `--force`; `--force` permits only the existing dirty-worktree and
  unmerged-branch overrides and never bypasses identity, registration, HEAD,
  state, recovery, or concurrent-change checks.
- Partial and detached workspace state remains ineligible. An unresolved
  recovery record remains a hard conflict and must be resolved through the
  existing recovery path rather than interpreted as successful removal.

### Plan, output, and compatibility contract

- Keep `RemovalPlan` as the worktree portion of `DeletionPlan`, but add an
  additive, deletion-relevant `alreadyRemoved` boolean to each repository
  entry, omitted when false. `PlanRemove` never emits it as true and retains
  its existing strict behavior.
- Delete dry-run and result JSON remain versionless additive documents. Active
  entries preserve their current shape; already-removed entries expose
  `"alreadyRemoved": true`. No workspace-state schema field or migration is
  introduced.
- Human dry-run renders active entries as `remove` actions and retained entries
  as already removed/no worktree action, followed by the unchanged branch
  deletion actions. Successful output must truthfully cover both paths rather
  than claim that absent worktrees were removed by this invocation.
- Existing active-workspace delete behavior, ordering, exit-code taxonomy,
  JSON field names, progress events, and force-override reporting remain
  compatible. No new flag or separate command is added.

### Mutation, locking, and recovery

- Delete planning owns delete-specific classification; do not relax or reuse
  `WorkspaceRemover.PlanRemove` for already-removed entries. Shared helpers may
  be extracted only where they preserve `remove`'s strict missing-path error.
- Preserve unlocked planning followed by project-lock acquisition and exact
  locked re-planning. A transition between active and already-removed, a path
  replacement, registration change, branch movement, state generation change,
  or branch checkout elsewhere must make revalidation fail before an
  unowned effect.
- Capture worktree and grouping receipts only for active removal effects.
  Grouping validation must tolerate legitimately absent managed paths while
  continuing to reject symlinks, non-directories, escapes, substitutions, and
  concurrent replacements around every active path.
- Build transaction steps in this order: active worktree removals child-first,
  branch deletions in existing deterministic order, then exact workspace-state
  deletion. Already-removed entries produce no Git remove call and no progress
  event claiming one.
- Existing exact-generation rollback remains authoritative. On a later
  failure, restore only worktrees removed by the current delete attempt,
  restore only branches deleted by the current attempt at their planned HEADs,
  and restore only the owned workspace-state generation. Never recreate an
  already-removed worktree. Retain recovery metadata when exact rollback cannot
  be completed.

### Scope and authority boundaries

- Do not change `wtree remove`: it must continue to reject missing or stale
  checkout paths and retain branches plus state after successful removal.
- Do not run `git worktree prune`, repair arbitrary manually deleted
  worktrees, infer ownership from directory absence, delete remote branches,
  change workspace-state version 1, or broaden `doctor`.
- Do not add dependencies, commit, publish, release, or modify user/global Git
  configuration. Product-code authorization begins only when the user later
  authorizes execution of this plan.
- A reviewer finding that materially expands these boundaries must be recorded
  for user direction and does not consume a remediation attempt.

## Stable contracts to establish early

| Contract | Owner and consumers | Invariant and enforcement |
|---|---|---|
| Delete worktree classification | `internal/service/delete.go`; consumed by delete dry-run and execution | Every repository is exactly `active` or `already-removed`; ambiguous path/registration/identity combinations fail before mutation. Table-driven fake-adapter and real-Git tests enforce the matrix. |
| Planned repository representation | `internal/service` deletion/removal plan models; consumed by `internal/cli/delete.go` | `alreadyRemoved` is additive and omitted for active entries; remove plans never set it. Structural JSON and renderer tests enforce compatibility and truthful actions. |
| Selective removal transaction | `internal/service/delete.go`, using bounded removal helpers | Only active entries receive receipts, remove steps, rollback restoration, and removal progress. Failure-injection and mixed-forest tests enforce ordering and ownership. |
| Retained branch authority | Workspace state plus local `refs/heads/<branch>`, corroborated through the Git adapter | Every branch exists at the state-recorded HEAD and is not checked out elsewhere before deletion; locked revalidation detects drift. Real-Git race tests enforce it. |

## Architecture and dependency boundaries

```text
delete CLI / dry-run rendering
            ↓
WorkspaceDeleter delete-specific planning and locked revalidation
            ↓
path + Git registration classification → retained branch verification
            ↓
selective active-worktree steps → branch steps → exact state step
            ↓
existing Git adapter, project lock, transaction runner, recovery store
```

- The CLI consumes a complete immutable deletion plan and does not inspect the
  filesystem or Git directly.
- `WorkspaceDeleter` owns acceptance of already-removed retained state.
  `WorkspaceRemover` remains the strict owner of ordinary remove preflight and
  active-worktree receipt/rollback mechanics.
- The Git adapter remains the only Git process boundary. The store remains the
  only workspace-state and recovery persistence boundary; no schema authority
  moves into the CLI or plan JSON.

## Global definition of done

- Each behavior is implemented test-first with recorded RED → GREEN → REFACTOR
  evidence, including success, ambiguity/refusal, locked races, destructive
  failure, rollback, and incomplete-recovery cases.
- Tests use hermetic temporary repositories and data directories, do not
  depend on global Git configuration or network access, and verify filesystem,
  refs, registrations, state bytes, recovery records, emitted effects, and
  output rather than only error strings.
- Existing active delete, remove/checkout restoration, nested/grouped forest,
  `--force`, dry-run, JSON, verbose progress, and concurrency tests remain
  green. No unrelated behavior or dependency changes are introduced.
- Public help, how-to guidance, README/specification wording, traceability, and
  `docs/status-overview.md` describe direct delete-after-remove behavior and
  the refusal boundary for ambiguous missing paths. Lifecycle metadata and
  reciprocal plan/specification links are updated during implementation.
- The main agent runs and records all milestone-specific commands plus these
  final gates on the reviewed filesystem:
  - `go test ./...`
  - `go test -race ./...`
  - `go vet ./...`
  - `make fmt-check`
  - `make build`
  - `make check`
  - `git diff --check`
- Independent review approves each milestone with no unresolved material
  finding. The plan becomes `Status: implemented` only after every milestone
  and final gate is approved; the source specification changes only if its
  full lifecycle conditions are satisfied.

## Milestones

### [ ] M00 — Classify already-removed worktrees during delete planning

Specification coverage: [`docs/spec/wtree.spec.md` §§29–31, 41–43](../spec/wtree.spec.md); [`internal/service/delete.go`](../../internal/service/delete.go); [`internal/service/remove.go`](../../internal/service/remove.go)

Scope:

- Introduce the delete-specific active/already-removed classification and the
  additive `alreadyRemoved` plan field without weakening `PlanRemove`.
- Populate planned repository HEADs from verified active checkouts or from
  retained state corroborated against the local branch, while preserving
  child-first repository order and existing branch/force planning.
- Reject every ambiguous path/registration/identity/branch combination and
  every branch checked out elsewhere before mutation, including mixed forests
  and path aliases handled by the existing path-equivalence rules.
- Update delete human and JSON dry-run rendering to describe no-op worktree
  entries truthfully while keeping active output compatible.

Test-first slices:

1. Create and remove a real single-repository workspace, then prove
   `PlanDelete` and CLI `delete --dry-run` succeed without checkout recreation,
   report `alreadyRemoved`, schedule the retained branch and state, and leave
   paths, refs, registrations, and state byte-for-byte unchanged.
2. Exercise active, already-removed, and mixed multi-level/grouped forests;
   assert deterministic child-first repository order, exact classifications,
   branch HEAD corroboration, active dirty/unmerged force separation, and no
   `alreadyRemoved` field in unchanged active/remove JSON.
3. Table-drive missing-but-registered, present-but-unregistered, replacement
   directory/symlink/identity, missing or moved branch, branch checked out at
   another path, detached/partial state, and Git inspection failures; assert
   typed errors and zero mutation.
4. Change path presence, Git registration, branch HEAD/location, or workspace
   state between unlocked planning and locked re-planning; prove the immutable
   plan comparison or exact revalidation rejects the race before deletion.

Verification:

- `go test ./internal/service -run 'WorkspaceDeleter|DeletePlan|PlanDelete|WorkspaceRemover' -count=1`
- `go test ./internal/cli -run 'Delete|Remove' -count=1`
- `go test ./internal/service ./internal/cli`
- `go vet ./internal/service ./internal/cli`
- `make fmt-check`
- `git diff --check`

Exit criteria: dry-run can distinguish and fully validate active,
already-removed, and mixed forests without mutation; every ambiguous or stale
combination is refused; remove behavior and existing public output remain
compatible; focused gates and independent review pass.

### [ ] M01 — Delete branches and state without recreating retained worktrees

Specification coverage: [`docs/spec/wtree.spec.md` §§29–31, 41–43](../spec/wtree.spec.md); approved M00 classification contract; [`internal/service/delete.go`](../../internal/service/delete.go); [`internal/service/remove_grouping.go`](../../internal/service/remove_grouping.go); [`internal/cli/delete.go`](../../internal/cli/delete.go)

Scope:

- Execute only active worktree removals, then delete every verified retained
  branch and the exact workspace-state generation; skip worktree receipts,
  removal calls, progress events, and rollback recreation for already-removed
  entries.
- Adapt grouping inventory and removal helper boundaries narrowly for mixed
  forests, preserving exact receipt validation, containment, child-first
  active removal, deterministic branch order, and concurrent-replacement
  refusal.
- Preserve transaction rollback and recovery semantics across failures before,
  during, and after active worktree removal, each branch deletion, and state
  deletion. Already-removed paths must remain absent on clean and incomplete
  rollback.
- Complete CLI integration, help/how-to/README/specification/traceability
  documentation, lifecycle metadata, reciprocal links, and status-overview
  evidence for the direct `remove` → `delete` workflow.

Test-first slices:

1. Through the public CLI, create a real nested/grouped workspace, run
   `remove`, then run `delete` without checkout; assert no add/remove-worktree
   call occurs during delete, all local branches and state disappear, ordinary
   unrelated logical-root/grouping content is preserved, and human/JSON output
   reports the actual effects.
2. Execute a mixed forest and prove only active paths are removed child-first,
   all branches are deleted in deterministic order, already-removed paths are
   untouched, progress contains no false removal event, and final state is
   absent. Repeat with unmerged branches to prove only `--force` authorizes
   their deletion.
3. Inject failure or cancellation at every active removal, branch deletion,
   and state-deletion boundary, including mutate-then-error cases; assert exact
   clean rollback restores current-attempt worktrees/branches/state but never
   recreates already-removed worktrees, and uncertain inverses retain complete
   recovery metadata with no false success.
4. Substitute active paths/grouping directories, recreate a previously absent
   path, move/recreate branches, or replace workspace state during progress
   callbacks and publication boundaries; prove unowned generations are
   preserved and the command fails with the existing conflict/rollback
   taxonomy.
5. Prove the documented workaround remains valid but unnecessary: both
   `remove → checkout → delete` and direct `remove → delete` finish with the
   same branches/state absent, while repeated delete reports the existing
   workspace-not-found result and ordinary `remove → checkout` behavior remains
   unchanged.

Verification:

- `go test ./internal/service -run 'WorkspaceDeleter|Delete.*Removed|Remove.*Delete|RemovalGrouping|Rollback|Recovery' -count=1`
- `go test ./internal/cli -run 'Delete|Remove|Checkout|Help|HowTo|EveryPrintedWTREEExample' -count=1`
- `go test ./internal/service ./internal/cli ./internal/store ./internal/transaction`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `make fmt-check`
- `make build`
- `make check`
- `git diff --check`

Exit criteria: direct delete after successful remove safely deletes only the
verified retained branches and owned workspace state, active and mixed forests
retain all existing destructive-operation guarantees, rollback never recreates
pre-existing absence, public guidance and output are accurate, all focused and
repository-wide gates pass, and independent review has no unresolved material
finding.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
