# Companion repositories implementation plan

Status: implemented
Source specification: [Companion repositories specification](../spec/companion-repositories.md)
Implementation context: [Companion repositories implementation context](companion-repositories-context.md)
Source idea: [Companion repositories with independent baselines](../ideas/companion-repositories.md)
Authoritative existing contracts: [`wtree` specification](../spec/wtree.spec.md), [full multi-repository experience specification](../spec/full-multi-repository-experience.md), and [local/shared lifecycle hooks specification](../spec/local-workspace-lifecycle-hooks.md)
Delivery style: test-first, one independently reviewed milestone at a time; no new dependency, network-dependent test, automatic commit/stage, push, pull request, release, or deployment

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan and its source specification fix them.

For each unchecked milestone, in order:

1. Read this plan, the focused implementation context, the exact specification
   sections named by the milestone, the current files in scope, and the
   durable ledger at `docs/ai/runs/companion-repositories.md`. Create the
   ledger before the first implementation dispatch. On resumption, reconcile
   plan, ledger, evidence, and worktree, then append a reconciliation
   checkpoint before dispatching work.
2. Derive and record one complete milestone checklist from every scope item,
   test-first slice, documentation obligation, exit criterion, and
   verification command.
3. Give the complete initial packet to `implementer`. Use `implementer` for
   remediation when the ledger attempt count is zero or one, and
   `escalation-implementer` only when it is two. Require RED → GREEN → REFACTOR
   evidence, changed files, verification results, and unresolved concerns.
4. Treat partial work as progress, not a submission. Do not request review or
   change remediation counters until every checklist item is evidenced.
5. Send each complete submission to the read-only `reviewer`, which inspects
   the current shared filesystem against the whole milestone, source
   specification, compatibility, safety, portability, tests, and checks.
6. Record every material finding with a stable ID and return the complete
   unresolved set in one test-first remediation packet. Apply the exact
   three-rejected-complete-remediation limit in
   [milestone supervision](../ai/milestone-supervision.md). Do not use an
   escalation reviewer as a routine second opinion.
7. After reviewer approval, run milestone verification as the main agent,
   update affected documentation/contracts, check the milestone, append its
   execution-log row, create the next milestone ledger snapshot, and dispatch
   the next packet immediately.

Do not stop for ordinary test failures, reviewer findings, partial submissions,
rollback-fixture failures, or approved milestones. Preserve unrelated changes
and never use destructive cleanup. Commit only when separately authorized. A
final response is permitted only by the durable-ledger gate in
[milestone supervision](../ai/milestone-supervision.md).

An exact-tree hosted Linux/macOS/Windows CI run can be a final external blocker
when the tree cannot be represented to CI without separately authorized
commit, push, or pull-request activity. Record all local and reviewer evidence
and the precise continuation condition; do not silently waive that gate.

## Fixed implementation decisions

### Product and branch behavior

- `companion: true` marks the role; omission or false means ordinary. The
  existing `default_branch` is the only companion baseline field.
- Clone checks out the baseline in the default workspace. Named create uses
  the workspace name as its local branch but resolves its base from the
  companion baseline. `--from` continues to affect only ordinary repositories.
- Companion workspace branches are normal writable branches. A valid observed
  descendant of the persisted checkout HEAD is accepted where the
  specification requires; rewritten or unrelated history is not.
- Status renders a valid companion descendant as informational `advanced`,
  sets additive `headAdvanced`, and does not set structural `headMismatch` or
  local drift. Dirt and structural failures still take precedence.
- Push readiness permits a companion workspace branch to track any branch on
  the manifest-configured remote. It still requires the checkout branch plus
  configured remote name and fetch URL to match; it does not compare that
  workspace upstream merge ref with the baseline merge ref.
- Existing forest membership, mount, identity, partial-workspace, locking,
  rollback, recovery, and hook behavior remains authoritative.
- Release lock includes a companion exactly like any other non-base
  repository. This plan adds no release publication or tagging behavior.

### Configuration and migration

- Add strict, independently versioned portable and local v4. V4 retains all
  v3 hook fields and adds optional repository `companion`; false is omitted in
  canonical output.
- V2 and v3 continue to reject the field and retain exact parse, validation,
  marshal, and behavior contracts. Do not repurpose the existing v2 constants
  or weaken strict decoding.
- Project configuration is role authority; propagate the role into
  `domain.Repository`. Do not version or add role fields to registry or
  workspace state.
- Clone/update writes local v4 when representing a portable companion. Hook
  management and every config-copy/publication/rollback path preserve v4 role
  and v3-equivalent hooks. Hook-free non-companion writers do not upgrade
  gratuitously.
- Existing projects opt in through a tracked v4 manifest followed by
  `wtree update`. Init does not infer companions and no role-editing command is
  added.

### Commands and output

- Add `wtree repo branch <repository> <branch> [--dry-run] [--json]`. It
  accepts one companion and one valid existing local branch, updates portable
  `default_branch`/`upstream.branch` plus matching local `default_branch`, and
  never fetches, creates/switches a branch, stages, commits, or changes
  existing workspace state.
- Add mutually exclusive exec selectors `--no-companions` and
  `--repository <repository>`. The default stays all present repositories;
  selection limits both preflight and execution/result membership. An empty
  `--no-companions` selection is an invalid-arguments error before launch.
- Add `wtree companion update <repository> [--dry-run] [--json]`. It fetches
  the configured upstream once, requires a safe fast-forwarded baseline, then
  processes currently present workspaces best-effort in deterministic order.
- New commands use the exact command-owned JSON v1 envelopes in specification
  §9. Existing workspace-plan, exec, fetch, and push-readiness versions remain
  v1; status remains unversioned. Add only the specified optional companion,
  baseline, advancement, and preserved-branch fields, omitted for ordinary
  repositories. Human and JSON failure paths use the existing typed error
  taxonomy and companion-update reason vocabulary.

### Mutation and safety

- Baseline configuration uses project mutation authority, exact generation
  revalidation, atomic/CAS publication, and existing recovery conventions so
  local and portable files cannot silently split.
- Create remains all-repository atomic and rollback-capable.
- Delete removes only workspace-specific local branches and state. It never
  deletes the current companion baseline or any remote branch; force does not
  override this rule.
- Companion update has no global rollback after the baseline succeeds. Each
  branch update uses observed old/new commits and atomic compare-and-swap
  semantics; successful workspace state is atomically advanced. An active
  baseline is correlated to one workspace and its ref/worktree/state update is
  rollback-capable before any later branch attempt. It is never processed
  twice.
- Invalid individual workspace-state files and other safely isolated
  workspace failures are reported and do not stop valid later entries. A
  failed workspace state write rolls back that entry and continues; an
  incomplete rollback records recovery and stops later mutation.
- Companion update never merges, rebases, resets, force-updates, pushes,
  deletes refs, creates missing worktrees, or updates removed workspaces.

### Delivery boundaries

- Add no general grouping language, companion-specific clone storage,
  implicit update-on-create/checkout/fetch, or extra baseline/role commands.
- Add no third-party dependency unless existing standard-library and project
  boundaries cannot meet a specification requirement; that is a material
  scope change requiring user direction.
- Implementation authorization does not authorize commits, pushes, pull
  requests, CI dispatch through repository publication, releases, or remote
  branch changes.
- A reviewer finding outside the source specification is recorded for user
  direction and does not consume a remediation attempt unless required by the
  existing plan scope.

## Stable contracts to establish early

| Owner | Contract and consumers | Enforced invariant |
|---|---|---|
| `internal/config` | Local/portable decoding and all config writers | Strict v2/v3 compatibility; canonical v4 role plus retained hook semantics; role/baseline validation and deep-copy safety |
| `internal/domain` and resolver | All services consuming repository role | One normalized `Companion` fact per repository; config remains authority and workspace state remains checkout-generation authority |
| `internal/plan` and create services | Dry-run, create transaction, hooks, CLI rendering | Workspace-plan v1 additively exposes companion/baseline; every repository has one resolved base and target branch; companion base ignores `--from`; plan validation prevents ambiguous execution |
| `internal/git` | Baseline configuration/update services | Read-only ancestry/ref facts and CAS fast-forward primitives never move an unexpected ref or invoke hooks |
| `internal/store` and project locks | Configuration and companion-update publishers | Existing state version; atomic successful-head publication; no split config or unowned recovery mutation |
| Command-owned service results | CLI human/JSON renderers and tests | Exact §9 versions/fields, deterministic membership/order, explicit planned/completed/failed/canceled status and stable reasons, one JSON document on stdout |

Configuration and state dependencies remain:

```text
CLI → resolver/config authority → application service/immutable plan
                                      ↓
                      Git, store, lock, transaction adapters
                                      ↓
                      command-owned result → human or JSON
```

CLI code must not interpret companion branch ancestry or perform mutations.
Git code exposes facts/effects but does not decide project policy. Update,
create, delete, exec, and companion-update services consume the shared role
contract without creating competing role sources.

## Architecture and dependency boundaries

- Extend existing version dispatch and canonical YAML wire structs; do not
  create a parallel companion configuration file.
- Extend the current workspace planner/transaction so hooks and dry-run keep
  consuming one authoritative plan. Do not add a second companion create
  transaction.
- Reuse resolver, project lock, workspace enumeration, aggregate result/error,
  configured-ref fetch, fast-forward, atomic store, and recovery boundaries.
  Add only the narrow Git ref/ancestry operation absent from the current
  interface.
- Keep `wtree update` responsible for manifest reconciliation and
  `wtree companion update` responsible only for baseline/workspace branch
  advancement.
- Keep exec's existing direct-process and environment behavior. Selection is a
  service request concern and companion descendant validation is a preflight
  policy, not a process-runner change.
- Extend existing hermetic fake Git and real temporary-repository fixtures;
  do not introduce network or user-global configuration dependencies.

## Global definition of done

Every checked milestone requires:

- recorded RED → GREEN → REFACTOR evidence for its behavioral slices;
- focused owning-package tests, including invalid input, no-mutation,
  cancellation, rollback/recovery, and compatibility cases applicable to the
  slice;
- independent reviewer approval with no unresolved material finding;
- deterministic, hermetic fixtures isolated from user Git/config/data roots;
- public JSON, human output, help, and documentation updated in the milestone
  that exposes or changes behavior; and
- these complete submission checks:

  - `make check-local`
  - `go test ./... -count=1`
  - `go test -race -timeout=45m ./... -count=1`
  - `go vet ./...`
  - `make fmt-check`
  - `make build`
  - `git diff --check`

The final milestone additionally runs `make tutorial-test`,
`make release-test`, and obtains a matching hosted Linux/macOS/Windows test
run for the exact delivered tree. Retained evidence may be reused only when
the source, tree, flags, environment, and relevant inventory are unchanged.

## Risk and rollout boundaries

- The highest-risk surfaces are strict schema evolution, all-repository create
  rollback, deletion protection, dual-file baseline publication, and partial
  multi-workspace Git ref mutation. Each is established behind tests before a
  public command consumes it.
- V4 is explicit opt-in; no background migration, backfill, feature flag, or
  dual interpretation of older versions exists.
- The safe outcome for stale or incomplete authority is no mutation. The one
  intentional partial-success boundary is companion update after a successful
  baseline acquisition, where each settled result is retained and reported.
- A required exact-tree hosted platform run without authority to publish the
  tree is a genuine external blocker. An ordinary implementation or test
  failure is not.

## Milestones

### [x] M00 — Establish strict v4 authority with correct create semantics

Specification coverage: [§3](../spec/companion-repositories.md#3-repository-and-branch-model), [§4](../spec/companion-repositories.md#4-versioned-configuration), and [§10](../spec/companion-repositories.md#10-safety-and-compatibility-requirements)

Scope:

- Add local and portable v4 wire dispatch, optional canonical companion field,
  validation, copying, comparison, and round-trip behavior while preserving
  strict v2/v3 bytes and errors.
- Carry companion role through `domain.Repository`, config-to-domain
  conversion, resolver snapshots, clone/update/release materialization,
  rollback/recovery, and hook share/install/update paths.
- Extend immutable workspace-plan v1, create planning, locked revalidation,
  execution, result validation, hooks, and rendering so a named companion
  branch starts from its configured baseline while `--from` continues to
  select only ordinary repository bases.
- Keep registry and workspace state versions unchanged and prove that no
  config writer drops v4 role or hooks.
- Update schema/create/migration reference documentation so the first public
  v4 checkpoint is coherent; do not expose the later management commands yet.

Test-first slices:

1. Decode, validate, deep-copy, and deterministically re-encode v4 with mixed
   roles, forest topology, hooks, and shared hooks.
2. Reject `companion` in v2/v3 and malformed v4 while proving existing golden
   bytes, errors, init defaults, and hook-only v3 behavior unchanged.
3. Resolve, clone/update-plan, publish, roll back, recover, share/install hooks,
   and release-materialize a v4 project without losing or inventing roles.
4. Fuzz/round-trip all accepted versions and prove state/registry formats do
   not change.
5. Plan, dry-run, and create a mixed-role forest from distinct bases,
   including explicit `--from`, and prove companion/ordinary base, branch,
   state, plan-v1, hook-environment, and output facts.
6. Reject a missing baseline, existing branch, cross-worktree branch conflict,
   stale plan, and identity/path attack before mutation; inject every create
   failure and prove complete rollback.

Verification:

- `go test ./internal/config ./internal/domain ./internal/plan ./internal/service ./internal/cli -run 'Test.*(Config|Manifest|Resolve|Clone|Update|Release|Hook|Plan|Create)' -count=1`
- `go test ./internal/config -run 'Fuzz' -count=1`
- Global definition-of-done commands.

Exit criteria: V4 is a strict, canonical, fully propagated source of role
truth; create already honors every accepted companion declaration; every
existing version and non-companion path remains compatible; no management
command is exposed prematurely.

### [x] M01 — Make workspace lifecycle companion-aware and deletion-safe

Specification coverage: [§3](../spec/companion-repositories.md#3-repository-and-branch-model), [§6](../spec/companion-repositories.md#6-workspace-lifecycle), and [§9](../spec/companion-repositories.md#9-results-and-diagnostics)

Scope:

- Make remove/checkout round trips retain and restore companion branches.
- Protect the current companion baseline during delete, expose retained versus
  deleted branches through the specified additive deletion fields, and
  preserve local-only/force/recovery semantics.
- Adjust status, doctor, fetch, and push-readiness only as required to accept
  valid descendant HEADs on attached companion branches while continuing to
  diagnose dirt, mismatch, detached, missing, or rewritten history.
- Render valid companion descendants as non-drifting `advanced` status with
  exact additive fields, and allow their workspace branch to track any ref on
  the manifest-configured remote without weakening remote identity checks.

Test-first slices:

1. Commit on a companion branch, then status, doctor, fetch, push-readiness,
   remove, and checkout it without treating a valid descendant as corruption;
   prove `advanced` is informational and non-drifting.
2. Configure that workspace branch to track its same-named ref on the
   manifest-configured remote and prove push readiness accepts it while still
   rejecting a different remote name/URL or mismatched local branch.
3. Delete named workspaces locally while preserving the configured baseline
   and every remote ref, including force, baseline-name collision, failure,
   rollback, recovery, and locally advanced companion cases; assert the exact
   preserved-branch result fields.
4. Prove ordinary repository status, upstream validation, remove/checkout, and
   delete behavior and existing result bytes remain unchanged.

Verification:

- `go test ./internal/plan ./internal/service ./internal/cli -run 'Test.*(Plan|Create|Remove|Checkout|Delete|Status|Doctor|Fetch|Push)' -count=1`
- Global definition-of-done commands.

Exit criteria: A mixed project has one atomic lifecycle, companion commits are
usable, deletion cannot remove a baseline or remote branch, and ordinary
repository behavior is unchanged.

### [x] M02 — Deliver atomic future-facing baseline configuration

Specification coverage: [§5](../spec/companion-repositories.md#5-companion-baseline-command) and [§10](../spec/companion-repositories.md#10-safety-and-compatibility-requirements)

Scope:

- Add an immutable service plan/result for changing one companion baseline,
  including exact portable/local generations and Git branch facts.
- Atomically publish portable `default_branch`/`upstream.branch` and matching
  local `default_branch` under project mutation authority with rollback or
  actionable recovery on injected partial failure.
- Add `wtree repo branch` argument validation, dry-run, JSON v1, human output,
  help, and typed errors.
- Prove no-op/rejection byte preservation and no branch creation, switching,
  fetch, state rewrite, staging, commit, or other workspace mutation.

Test-first slices:

1. Plan and dry-run a valid existing branch change while preserving remotes,
   merge ref, identity, topology, hooks, and existing workspace state.
2. Reject an ordinary/unknown repository, invalid or missing branch, stale
   config/Git facts, split authority, recovery conflict, and concurrent change
   before publication.
3. Publish both files atomically, inject each write/rollback failure, and prove
   either complete success or recorded recoverable ownership without silent
   disagreement.
4. Exercise exact planned/completed/unchanged/failed human and JSON-v1 output
   and show a later create uses the new baseline while existing branches
   remain unchanged.

Verification:

- `go test ./internal/service ./internal/cli -run 'Test.*(RepositoryBranch|RepoBranch|Baseline)' -count=1`
- Global definition-of-done commands.

Exit criteria: The focused command safely changes only future baseline
selection and is fully observable before mutation.

### [x] M03 — Add exact exec selection without directory changes

Specification coverage: [§7](../spec/companion-repositories.md#7-exec-repository-selection), [§3.3](../spec/companion-repositories.md#33-writable-workspace-branches), and [§9](../spec/companion-repositories.md#9-results-and-diagnostics)

Scope:

- Extend exec service requests/plans with mutually exclusive ordinary-only and
  exact-repository selection.
- Select before preflight so unselected missing or invalid repositories do not
  block a command; retain forest/reverse/fail-fast/cancellation behavior for
  the selected subset.
- Accept a valid companion descendant HEAD and report its observed value;
  reject rewritten/unrelated history or branch/identity/path mismatch.
- Add flags, help, human/dry-run/JSON rendering and compatibility tests; add no
  grouping language or role-only selector.

Test-first slices:

1. Prove default all, `--no-companions`, and exact ordinary/companion
   repository membership and order in normal and reverse dry-run/results.
2. Prove an unselected broken checkout is ignored while selected absent,
   unknown, detached, rewritten, or mismatched checkouts fail before any
   process starts.
3. Commit locally in a companion and execute there by ID from another current
   directory/workspace selection with correct `WTREE_*`, HEAD, stdout/stderr,
   exit, and cancellation behavior.
4. Reject conflicting or repeated selectors and an empty
   `--no-companions` selection before launch; preserve existing exec-v1 JSON
   and no-shell argument/environment guarantees.

Verification:

- `go test ./internal/service ./internal/cli -run 'Test.*Exec' -count=1`
- Global definition-of-done commands.

Exit criteria: Exec selects exactly the requested present repositories,
preflights only them, and remains safe and compatible for both repository
roles.

### [x] M04 — Best-effort fast-forward one companion across present workspaces

Specification coverage: [§8](../spec/companion-repositories.md#8-updating-one-companion-across-present-workspaces), [§9](../spec/companion-repositories.md#9-results-and-diagnostics), and [§10](../spec/companion-repositories.md#10-safety-and-compatibility-requirements)

Scope:

- Add the minimum Git ancestry/ref CAS capability needed to fast-forward a
  checked-out or inactive local branch without force and with hooks disabled;
  reuse configured-ref authenticated fetch and existing fast-forward facts.
- Add a companion-update planner/result that enumerates present workspaces
  and invalid state files independently and deterministically, distinguishes
  deferred remote facts in dry-run, and records exact §9 baseline and
  per-workspace outcomes.
- Implement one locked fetch/baseline phase followed by immediately
  revalidated per-workspace best-effort updates and atomic state-head writes.
  Correlate an active baseline to one valid workspace, update its checkout and
  state in the baseline phase, roll it back on publication failure, and omit it
  from the later workspace loop.
- Continue after dirty, detached, missing, identity-mismatched,
  recovery-blocked, invalid-state, rewritten, or diverged entries and after a
  safely rolled-back state-publication failure. Stop new work on cancellation
  or incomplete rollback; never globally roll back settled successes.
- Add `wtree companion update` CLI group/command, dry-run, JSON v1, human
  output, help, and stable failure reasons.

Test-first slices:

1. Prove the Git adapter observes ancestry and CAS-fast-forwards only the
   expected local ref for active and inactive branches, rejecting divergence,
   stale refs, hooks, and force behavior in real temporary repositories.
2. Dry-run mixed present/removed/invalid-state workspaces with deterministic
   entries, deferred fetch facts, exact reason codes, and zero
   ref/state/network effects.
3. Fetch once and fast-forward an inactive baseline, then update behind
   branches, leave branches that contain it unchanged, persist successful
   heads, and continue after invalid, diverged, or otherwise unsafe entries.
4. Fast-forward a baseline checked out in the default and in a named
   workspace, update that checkout's state exactly once even when no ref move
   is needed, and exclude it from later processing. Reject an active baseline
   without valid state authority.
5. Inject configured-ref, baseline, branch, state-write, output-callback, and
   cancellation failures; roll back baseline or per-workspace publication as
   specified and prove exact partial results, no unintended refs, no remote
   operation other than the single fetch, and no global rollback. Prove an
   incomplete rollback records recovery and cancels later entries.
6. Reject a non-companion/unknown repository and unresolved project-level
   authority before fetch or mutation.
7. Publish the observed descendant HEAD for a completed unchanged workspace;
   inject its state failure and prove the branch remains unchanged while later
   entries continue.

Verification:

- `go test ./internal/git ./internal/service ./internal/cli -run 'Test.*(FastForward|CompanionUpdate|Companion)' -count=1`
- `go test -race ./internal/git ./internal/service ./internal/cli -run 'Test.*(FastForward|CompanionUpdate|Companion)' -count=1`
- Global definition-of-done commands.

Exit criteria: One command safely brings every eligible present checkout up
to its companion baseline, retains successful work across independent
failures, and makes every non-action explicit.

### [x] M05 — Complete end-to-end documentation, compatibility, and platform acceptance

Specification coverage: [§2](../spec/companion-repositories.md#2-scope), [§11](../spec/companion-repositories.md#11-explicit-non-goals), and [§12](../spec/companion-repositories.md#12-acceptance-criteria)

Scope:

- Add an executable tutorial path covering v4 adoption, clone/create from
  independent baselines, local companion commits, exec scopes, baseline
  change, best-effort companion update, remove/checkout/delete, and release
  lock inclusion.
- Update README/reference/help/migration and specification traceability for
  exact command, schema, output, safety, and non-goal behavior.
- Add integrated black-box and regression coverage spanning hooks, update,
  recovery, partial forests, nested mounts, spaces/Unicode, and all public
  human/JSON paths.
- Audit canonical fixtures, result versions, error taxonomy, Windows path/ref
  behavior, and exact absence of network/global-state dependence.
- Obtain final independent review and matching exact-tree hosted platform
  evidence; do not publish the tree without separate authorization.

Test-first slices:

1. Run the documented everyday workflow in a hermetic fixture and assert each
   user-visible branch, commit, scope, failure/non-action, and cleanup result.
2. Exercise malformed/old schemas, incompatible selectors, stale authority,
   update/rollback/recovery, hooks, and release locking through public CLI
   boundaries with exact human/JSON assertions.
3. Run complete normal/race/tutorial/release gates locally and the exact-tree
   Linux/macOS/Windows CI matrix with no unexplained failure or flake.
4. Trace every specification acceptance criterion to code, focused tests,
   tutorial evidence, and final reviewer approval.

Verification:

- `make tutorial-test`
- `make release-test`
- `go test ./... -count=1`
- `go test -race -timeout=45m ./... -count=1`
- `go vet ./...`
- `make fmt-check`
- `make build`
- `git diff --check`
- Matching hosted Linux, macOS, and Windows jobs for the exact delivered tree.

Exit criteria: Every acceptance criterion is traced and independently
approved, public documentation matches executable behavior, all local gates
pass, and the exact delivered tree passes supported-platform CI. The plan and
specification statuses may then move to `implemented` with synchronized status
overview and complete durable ledger.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-09-04 | M00 | Exact focused config/domain/plan/service/CLI and config Fuzz passed; `make check-local`, full normal with the repository-required 45-minute timeout, full race, vet, format, build, and whitespace all passed in both frozen implementer and main-agent post-review runs | Normal reviewer approved after R1–R2 remediation fixed local-v2 companion marshal rejection and non-gratuitous local-version selection; no material findings remain | Not committed; commit was not authorized |
| 2026-09-05 | M01 | Exact focused plan/service/CLI, `make check-local`, preserved-GOFLAGS full normal, full race, vet, format, build, and whitespace passed in frozen implementation and main-agent post-review runs | Normal reviewer approved after R1–R2 normal remediation and final-opportunity R3 escalation bound baseline-only non-drift to exact unstaged dual-file authority, persisted checkout facts, degraded status metadata, canonical Git identity, and an existing local branch; no material findings remain | Not committed; commit was not authorized |
| 2026-09-05 | M02 | Exact focused service/CLI, `make check-local`, preserved-GOFLAGS full normal, full race, vet, format, build, and whitespace passed in frozen implementation and main-agent post-review runs; Windows fsutil cross-compilation also passed | Normal reviewer approved at remediation attempt 1 after R1–R3 added expected-identity CAS, post-replacement ownership, exact receipt-loss recovery, and regular-only Lstat authority, and R4 added cross-platform auxiliary-generation receipts with truthful recovery for conditional-restore and displaced-cleanup failures; no material findings remain | Not committed; commit was not authorized |
| 2026-09-05 | M03 | Exact focused Exec, `make check-local`, 775-target full normal, 775-target full race, vet, format, build, and whitespace passed in frozen implementation and main-agent post-review runs | Normal reviewer approved at remediation attempt 0 after R1 restored matching-detached default compatibility while scoped selectors reject detached and R2 constrained path authority to selected repositories plus required present/absent ancestors without unselected sibling collisions; no material findings remain | Not committed; commit was not authorized |
| 2026-09-06 | M04 | Exact focused normal/race, `make check-local`, preserved-GOFLAGS full normal, full race, vet, format, build, and whitespace passed in frozen implementation and main-agent post-review runs | Normal reviewer approved at remediation attempt 1 after R1–R4 established owned/uncertain state rollback, authenticated exact-ref fetch, cancellation-safe invalid-state reporting, and consistent adoption documentation; no material findings remain | Not committed; commit was not authorized |
| 2026-09-07 | M05 | Tutorial, release, full normal/race, vet, format, build, whitespace, Windows cross-compilation, and complete release-materialize verification passed locally. Exact SHA `f7254b6` passed both hosted PR run `34076324241` and push run `34076322381` on Ubuntu, macOS, and native Windows, including every normal/race shard and release gate | Normal reviewer approved the complete M05 tree after R1–R7 and hosted H1–H6 remediation; no material findings remain and the durable ledger records attempt count 1 | `f7254b6` is the exact hosted implementation tree on PR #4 targeting `feat/release-lock`; final lifecycle metadata follows as a documentation-only checkpoint |
