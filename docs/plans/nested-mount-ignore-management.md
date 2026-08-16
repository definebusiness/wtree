# Nested mount ignore management implementation plan

Status: initial
Source specification: [Nested mount ignore management specification](../spec/nested-mount-ignore-management.md)
Source of truth: [`docs/spec/nested-mount-ignore-management.md`](../spec/nested-mount-ignore-management.md); [`docs/spec/wtree.spec.md` §18](../spec/wtree.spec.md#18-mount-overrides); [`internal/discovery/discovery.go`](../../internal/discovery/discovery.go); [`internal/git/adapter.go`](../../internal/git/adapter.go); [`internal/service/init.go`](../../internal/service/init.go); [`internal/service/plan.go`](../../internal/service/plan.go); [`internal/service/create.go`](../../internal/service/create.go); [`internal/transaction/transaction.go`](../../internal/transaction/transaction.go); [`internal/cli/root.go`](../../internal/cli/root.go); [`internal/cli/plan.go`](../../internal/cli/plan.go)
Delivery style: test-first, one reviewed milestone at a time; no staging,
committing, publishing, dependency additions, or persistent config/state schema
migration

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes those decisions below.

For each unchecked milestone, in order:

1. Read this plan, the relevant source-of-truth sections, the durable run
   ledger at `docs/ai/runs/nested-mount-ignore-management.md`, and the current
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
   Apply the three rejected complete-remediation limit defined by
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

### Product behavior and command surface

- Add exactly this root command surface:

  ```text
  wtree add-ignore
    [--project <directory-or-.wtree.yml>]
    [--mount <repository-id>=<mount>]...
    [--dry-run] [--json] [--data-dir <path>]
  ```

- `add-ignore` accepts no positional argument and rejects `--from`, `--force`,
  and `--verbose` as invalid arguments. Repeated `--mount` reuses the create
  parser and validation contract.
- Add `--add-ignore` to `wtree init` and `wtree create` only. Do not add it to
  checkout, clone, sync, update, import, remove, delete, or doctor.
- Plain `init` checks every discovered non-root default mount against the
  immediate parent's `HEAD` and fails before all mutation when any committed
  `.gitignore` rule is missing.
- Plain `create` checks every effective non-root mount, including unchanged
  defaults and overrides, against the immediate parent's independently
  resolved base commit. One missing rule rejects the complete plan before
  mutation.
- Both failures list all deterministically known missing rules and include an
  actionable `wtree add-ignore` hint. Override hints retain the required
  `--mount id=mount` values. They also mention `--add-ignore` on the original
  operation.
- `init --add-ignore` writes missing rules into the discovered source parent
  checkouts as part of init's reversible publication boundary.
- `create --add-ignore` never edits source checkouts. It creates a parent
  worktree, changes that new worktree's `.gitignore`, verifies the mount is
  ignored, and only then adds direct child worktrees. Successful ignore edits
  remain intentionally uncommitted and are reported to the user.
- `add-ignore` operates on configured source checkouts for initialized
  projects. Without `.wtree.yml`, it discovers from the selected outer root;
  nested uninitialized context is rejected rather than guessing an outer
  project.
- `add-ignore` and both flags never stage or commit. Success output tells the
  user exactly which parent repositories require review and commits.

### Ignore ownership and matching

- Each non-root mount is owned by its immediate parent repository. Generated
  rules go into that parent's root `.gitignore`, not a single project-wide
  file and not the child repository.
- Generate literal anchored directory rules of the form `/<mount>/`, escaping
  accepted Git-ignore metacharacters without changing path separators.
- Git is authoritative for ignore evaluation. Committed-base validation must
  reconstruct relevant committed `.gitignore` files and use `git check-ignore`
  or an equivalently complete Git-owned mechanism.
- Only `.gitignore` rules qualify. Uncommitted source edits do not satisfy
  committed-base checks; `.git/info/exclude`, `core.excludesFile`, and global
  ignore configuration never qualify.
- Rule addition checks the target working tree, respects an already-effective
  `.gitignore` rule, and does not append a duplicate.
- The existing partial `IsIgnoredAt` adapter and custom-override planner check
  in the current worktree are baseline evidence, not an approved milestone.
  Reconcile and refactor them into the complete specification; do not discard
  unrelated user changes or assume the partial behavior is sufficient.

### File mutation and transaction semantics

- Introduce one service-owned ignore plan/result model shared by standalone
  `add-ignore`, init, and create. CLI code parses and renders; it does not own
  ignore matching, path selection, or file mutation.
- Preflight every target before the first write: graph/mount validity,
  containment, target path, symlink/non-regular rejection, readability,
  permissions/parent feasibility, current bytes, and effective-rule status.
- Preserve existing bytes, order, permissions, and unambiguous newline style.
  Append only missing generated rules, coalesced per parent file in stable
  parent-first/repository-ID order.
- Use same-directory temporary files, sync/close, atomic replacement, and
  directory sync where supported. Never edit `.gitignore` in place.
- Standalone initialized mutation holds the existing project lock and
  revalidates under it. Uninitialized mode creates no persistent lock; it uses
  optimistic unchanged-file/type checks before replacement.
- Multi-file standalone and init writes snapshot exact prior existence, bytes,
  mode, and relevant metadata. Failure restores changed files in reverse order
  or reports every incomplete restoration through the existing rollback error
  taxonomy.
- Create expresses ignore updates as reversible transaction effects after the
  owning parent worktree exists and before its first child worktree is added.
  Rollback may force-remove only worktrees created by that transaction and may
  discard only its known ignore changes.
- Dry-run performs all deterministic reads and safety checks but creates no
  target, temporary file, directory, lock, branch, worktree, state, recovery
  record, or metadata/timestamp change.

### Public data and compatibility

- `add-ignore` JSON follows the exact shape in the source specification.
  Collections render as deterministic arrays, never `null`.
- Init JSON gains additive `ignoreUpdates`; create plans gain deterministic
  `ignoreUpdates` and explicit `update_gitignore` steps.
- Advance only the workspace plan schema from version 1 to version 2 because
  a new public action and validation contract are introduced. Keep config,
  registry, workspace state, and recovery schemas at version 1.
- Keep the executable version at `0.2.0`; this plan does not authorize another
  product version change or release publication.
- Use existing error kinds and exits: invalid arguments, validation, Git,
  conflict, internal, and rollback incomplete. JSON errors remain one object
  on stdout with empty human stderr.
- `init` continues to ensure `/.wtree.yml` in the root `.gitignore`; the new
  nested rules are additional. `project.wtree.yml` remains tracked/visible.

### Scope and authority boundaries

- Do not remove stale ignore rules, edit `.git/info/exclude`, alter global Git
  configuration, modify `.gitmodules`, support submodules, or edit arbitrary
  non-checked-out refs.
- Do not add dependencies. Use the standard library, existing Git adapter,
  filesystem utilities, locks, transactions, renderers, and test fixtures.
- Do not commit, push, publish, deploy, create a release, or rewrite user
  history. Code-change authorization does not grant any of those actions.
- A finding that materially requires behavior outside the source
  specification is a scope question for the user, not a remediation attempt.
- Ordinary platform differences, test failures, or implementation complexity
  are not external blockers. A genuine blocker is limited to unavailable
  required tooling/CI evidence or an irreconcilable conflict with preserved
  user work that cannot be safely isolated.

## Stable contracts to establish early

### Ignore inspection and generation

- Owner: `internal/git` owns Git-authoritative ignore facts;
  `internal/pathutil` or a narrow service helper owns literal rule generation.
- Consumers: ignore planning in `internal/service`; CLI must not invoke Git or
  interpret ignore syntax directly.
- Invariant: a committed check evaluates exactly the chosen commit and accepts
  only a `.gitignore` source; a working-tree check accepts only effective
  `.gitignore` sources and excludes local/global configuration.
- Evidence: hermetic real-Git tests covering root/ancestor rules, negation,
  missing files, uncommitted changes, local/global excludes, old refs, spaces,
  Unicode, and metacharacters.
- Migration: extend the `git.Git` interface and embedded test doubles without
  adding a dependency or changing Git identity semantics.

### Ignore plan/result

- Owner: a focused `internal/service` component owns discovery/config graph
  input, effective mounts, target files, prior snapshots, generated rules,
  changed/already-safe classification, and deterministic ordering.
- Consumers: standalone add-ignore execution, initializer, workspace planner,
  creator, and renderers.
- Invariant: the complete plan is immutable after locked revalidation; every
  planned write has an exact inverse; no mutation occurs during planning.
- Evidence: table-driven service tests and injected read/write/rename/sync
  failures, including byte-for-byte no-mutation assertions.
- Migration: no durable schema; JSON plan/result fields are public contracts.

### Workspace plan v2

- Owner: `internal/plan` owns action names, plan version, validation, and JSON
  structure; `internal/service` builds plans; `internal/transaction` executes
  reversible effects.
- Consumers: create dry-run, JSON clients, human rendering, creator,
  transaction/recovery reporting, and tests.
- Invariant: every `update_gitignore` step follows its owning parent's
  `add_worktree` and precedes every direct child's `add_worktree`; inverse
  metadata restores/removes only the transaction-owned file state.
- Evidence: plan validation/order tests, three-level create integration,
  failure injection at every effect, and JSON decoding contracts.
- Migration: emitted workspace plans advance to version 2; persisted workspace
  and recovery records remain version 1 and continue to round-trip unchanged.

### CLI and diagnostic contract

- Owner: `internal/cli` owns Cobra flags, option compatibility, human text,
  hints, and orchestration; `internal/render` owns generic output primitives.
- Consumers: users, shell scripts, tutorial, and JSON clients.
- Invariant: dry-run is read-only, human hints are actionable, JSON is one
  deterministic document, and unsupported option combinations fail with exit
  2.
- Evidence: black-box CLI tests, decoded JSON assertions, output-stream tests,
  help/how-to topic tests, and end-to-end workflows.

## Architecture and dependency boundaries

```text
internal/cli ──parse/render──> internal/service ignore planner/executor
                                      │
                     ┌────────────────┼────────────────┐
                     ▼                ▼                ▼
               internal/git    internal/fsutil   internal/lock
                     │                │                │
                     └────────> Git/filesystem <───────┘

workspace service ──> internal/plan v2 ──> internal/transaction
initializer ─────────> shared ignore plan/write transaction
```

- Domain/config/discovery supply repository hierarchy and validated mounts;
  they do not write `.gitignore`.
- Git adapter methods are read-only facts. File creation/replacement belongs
  to the shared service/filesystem transaction boundary.
- Standalone add-ignore, init, and create must consume the same generated rule
  and safety model; three separate append implementations are forbidden.
- Renderers receive completed plans/results and never infer filesystem state.
- Existing registry/workspace writers and lock ordering remain authoritative.

## Global definition of done

Every milestone must satisfy all applicable items below before approval:

- Each behavior is developed RED → GREEN → REFACTOR with recorded focused
  failure/pass evidence. Tests must prove success, negative/no-mutation paths,
  and relevant rollback or concurrency behavior.
- Filesystem/Git tests are hermetic: temporary repositories, test-only Git
  identity, disabled system/global configuration, no network, deterministic
  locale, and cleanup owned by the test framework.
- Existing user changes remain intact; no unrelated file is rewritten and no
  file under `docs/ai/runs/` is touched except the ledger for this authorized
  plan run.
- Public human, JSON, help, how-to, README, tutorial, specification, and
  traceability contracts agree by the milestone that introduces the behavior.
- No new dependency, persistent schema migration, stage, commit, push,
  publish, or release occurs.
- Run the milestone's focused commands plus:

  ```sh
  gofmt -w <changed-go-files>
  git diff --check
  go vet ./...
  go test ./...
  go test -race ./...
  make check
  ./scripts/release-build_test.sh
  ```

- Linux, macOS, and Windows CI must remain green. Where local execution cannot
  prove a platform-specific file-mode or symlink behavior, named CI evidence
  is required before final acceptance.
- The independent reviewer approves the complete milestone with no unresolved
  material findings, and the main agent repeats the required verification.

## Milestones

### [ ] M00 — Establish authoritative ignore planning and file contracts

Specification coverage: [§§2–4](../spec/nested-mount-ignore-management.md#2-terms-and-ownership), [§8](../spec/nested-mount-ignore-management.md#8-standalone-write-transaction), [§10](../spec/nested-mount-ignore-management.md#10-safety-and-portability)

Scope:

- Reconcile the current partial committed-ignore adapter into a complete
  Git-authoritative API for committed-base and working-tree `.gitignore`
  checks, excluding local/global sources.
- Implement one literal anchored-directory rule generator and relevant
  ancestor `.gitignore` handling for safe, validated mounts.
- Introduce the immutable service ignore plan/result and file snapshot model,
  including initialized/discovered graphs, overrides, deterministic grouping,
  symlink/type/containment/permission checks, and changed/already-safe status.
- Implement the reusable atomic replace/restore transaction with injected
  filesystem seams, exact byte/mode preservation, optimistic concurrency, and
  complete rollback reporting. Do not expose a CLI command in this milestone.
- Establish the internal ignore-update ordering model needed by create without
  changing the public workspace plan version or output before M03 wires the
  complete consumer behavior.

Test-first slices:

1. RED then GREEN committed checks for root and ancestor `.gitignore` rules at
   `HEAD` and an older ref; prove uncommitted, info-exclude, and hostile global
   rules do not qualify.
2. RED then GREEN working-tree checks and literal generation for simple,
   multi-component, space, Unicode, `#`, `!`, bracket, wildcard, backslash,
   and trailing-space mount inputs accepted by central validation.
3. Plan a three-level default/overridden graph and prove immediate-parent file
   ownership, stable grouping/order, duplicate suppression, and complete
   missing-rule reporting.
4. Reject escaping mounts, symlink/non-regular `.gitignore`, unreadable paths,
   and concurrent snapshot changes before overwrite.
5. Inject create/write/sync/rename failures across multiple files; prove exact
   reverse restoration, no truncation, mode preservation, and explicit
   incomplete rollback evidence.
6. Validate legal/illegal ignore-update ordering in the internal service model
   and prove all existing public plan/config/store versions remain unchanged.

Verification:

- `go test ./internal/git ./internal/pathutil ./internal/plan ./internal/service -run 'Ignore|WorkspacePlan' -count=1`
- `go test ./internal/git ./internal/pathutil ./internal/plan ./internal/service -run 'Ignore|WorkspacePlan' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: one reviewed service/Git/filesystem contract can completely and
read-only plan ignore requirements and safely apply/revert planned file
changes; all public behavior remains compatible and no consumer has a private
ignore parser or writer.

### [ ] M01 — Deliver `wtree add-ignore`

Specification coverage: [§5](../spec/nested-mount-ignore-management.md#5-wtree-add-ignore), [§8](../spec/nested-mount-ignore-management.md#8-standalone-write-transaction), [§9](../spec/nested-mount-ignore-management.md#9-error-and-compatibility-rules)

Scope:

- Add the root command, exact flags/argument rejection, normal initialized
  resolution, uninitialized outer-root discovery, and repeated mount overlay.
- Wire dry-run, real initialized locking/revalidation, uninitialized optimistic
  concurrency, multi-file atomic mutation, and rollback through the shared M00
  service rather than CLI filesystem access.
- Implement stable human and JSON plans/results, including changed files,
  added rules, already-safe repositories, no-change output, `No changes made.`,
  and commit guidance.
- Add root help, command help, command-specific `--how-to`, global how-to topic,
  option compatibility, exit-code, stderr/stdout, and shell-safe hint coverage
  in this milestone.
- Preserve `.wtree.yml`, manifests, repository contents, Git index, branches,
  worktrees, registry, and workspace state byte-for-byte.

Test-first slices:

1. From an uninitialized three-level root, dry-run and execution create the two
   owning `.gitignore` files with correct rules; invocation from an inner
   uninitialized repository refuses ambiguity without mutation.
2. From an initialized project, add only missing defaults and repeated mount
   overrides to configured source parents; reject unknown/duplicate IDs and
   collisions before mutation.
3. Existing effective root/ancestor rules produce no duplicate and a clean
   no-op result; local/global excludes do not suppress required additions.
4. JSON decodes to the specified non-null deterministic shape; human output
   lists exact changed parents and commit guidance; JSON errors never leak
   human stderr.
5. Dry-run creates no temp files or locks and preserves bytes, modes, mtimes,
   index, refs, registry, and state.
6. Inject concurrent modification and each multi-file write/rollback failure;
   prove conflict classification, old-or-new complete files, exact restoration
   when possible, and complete unrestored-path evidence otherwise.

Verification:

- `go test ./internal/service ./internal/cli ./cmd/wtree -run 'AddIgnore|HowTo|Help' -count=1`
- `go test ./internal/service ./internal/cli ./cmd/wtree -run 'AddIgnore' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: `wtree add-ignore` is independently usable before or after
initialization, fully documented in installed help/how-to, dry-run safe,
transactional across parent repositories, and contract-tested in human and
JSON modes.

### [ ] M02 — Enforce safe init and integrate `init --add-ignore`

Specification coverage: [§6](../spec/nested-mount-ignore-management.md#6-wtree-init), [§8](../spec/nested-mount-ignore-management.md#8-standalone-write-transaction)

Scope:

- Extend init planning to check every discovered default nested mount against
  the immediate parent's current committed `HEAD` before registry locks or any
  publication/mutation.
- On plain-init failure, report every missing tuple in stable order with the
  exact `wtree add-ignore`, commit, retry, and `init --add-ignore` guidance.
- Add `--add-ignore` parsing, dry-run/JSON fields, and service request/result
  contracts; keep unsupported options and root `--project` behavior stable.
- Integrate missing mount rules and the existing `/.wtree.yml` rule with init's
  publication transaction, exact snapshots, rollback, and duplicate
  registration revalidation.
- Update init help/how-to and focused README snippets in the same milestone so
  no unsafe init workflow is advertised.

Test-first slices:

1. Plain init with one- and three-level missing rules fails validation, lists
   every parent/child/mount plus hint, and leaves configs, manifests, registry,
   state, locks, and `.gitignore` bytes/metadata untouched.
2. Rules committed in each parent `HEAD` allow plain init; rules only in the
   working tree, info exclude, global exclude, or another branch still fail.
3. `init --add-ignore` creates/appends all mount rules plus `/.wtree.yml`,
   initializes successfully, and reports every uncommitted file to review.
4. `init --add-ignore --dry-run` returns the same proposed ignore changes in
   human/JSON form with zero filesystem or registry mutation.
5. Inject failure after each ignore/config/manifest/registry/state publication
   boundary and cancellation between steps; prove exact reverse rollback or a
   complete rollback-incomplete diagnostic.
6. Preserve existing unrelated `.gitignore` bytes, modes, newline style,
   portable manifest visibility, duplicate-registration protections, and
   discovery ignore/submodule semantics.

Verification:

- `go test ./internal/service ./internal/cli -run 'Init.*Ignore|Ignore.*Init|Initializer' -count=1`
- `go test ./internal/service ./internal/cli -run 'Init.*Ignore|Ignore.*Init' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: unsafe default mounts cannot be initialized silently; the
standalone hinted workflow and explicit one-command init workflow both work,
and every new init mutation is fully preflighted and reversible.

### [ ] M03 — Enforce every create mount and integrate `create --add-ignore`

Specification coverage: [§7](../spec/nested-mount-ignore-management.md#7-wtree-create), [§3](../spec/nested-mount-ignore-management.md#3-ignore-safety-invariant), [§9](../spec/nested-mount-ignore-management.md#9-error-and-compatibility-rules)

Scope:

- Replace the partial override-only check with complete validation for every
  effective non-root mount at the immediate parent's independently resolved
  base commit, both during initial planning and locked revalidation.
- Accumulate deterministic missing-rule findings rather than returning a
  nondeterministic first map entry; render exact default/override add-ignore
  hints and selected parent bases.
- Add create-only `--add-ignore`, immutable v2 ignore updates/actions,
  dry-run/human/JSON rendering, and success commit guidance.
- Execute parent-worktree creation, atomic ignore update, working-tree ignore
  verification, and child-worktree creation in the specified parent-first
  order through reversible transaction steps.
- Preserve concurrency, cancellation, state commit, result validation,
  recovery records, clean/incomplete rollback classification, and verbose
  progress semantics for the expanded action set.
- Update create help/how-to and focused README examples in the same milestone.

Test-first slices:

1. Plain root-only create remains unchanged; a nested create with an ignored
   default succeeds, while one missing default among several fails before any
   branch/worktree/state/lock mutation and lists the full stable finding set.
2. Overrides check the actual parent base selected by `HEAD` or `--from`; an
   uncommitted rule, local/global exclude, or rule on another ref is rejected
   with the exact `add-ignore --mount` hint.
3. `create --add-ignore --dry-run` emits v2 steps and workspace-target
   `.gitignore` paths/rules without creating anything.
4. Real `create --add-ignore` handles a three-level renamed graph, updates each
   new parent before mounting children, persists valid state, leaves only
   listed `.gitignore` changes dirty, and never changes source checkout bytes.
5. Existing effective rules produce no ignore step or dirty edit; mixed
   existing/missing rules add only the missing subset deterministically.
6. Inject failure/cancellation before and after every branch, worktree, ignore
   update, ignore verification, result validation, and state commit; prove
   complete rollback or exact recovery evidence, including safe force removal
   of only transaction-created dirty worktrees.
7. Concurrent same/different workspace creation retains existing winner and
   consistency guarantees; verbose events, human success, and JSON remain
   deterministic and correctly separated by stream.

Verification:

- `go test ./internal/plan ./internal/service ./internal/cli -run 'WorkspacePlan|Create.*Ignore|Ignore.*Create' -count=1`
- `go test ./internal/service ./internal/cli -run 'Create.*Ignore|Ignore.*Create' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: no create operation can mount an unignored nested repository;
plain create fails with a complete actionable hint, and `--add-ignore` safely
creates a protected, accurately reported, transactionally recoverable
workspace without touching or committing source checkout files.

### [ ] M04 — Complete documentation, tutorial, traceability, and acceptance

Specification coverage: [§§1–12](../spec/nested-mount-ignore-management.md), [`docs/spec/wtree.spec.md`](../spec/wtree.spec.md), [`docs/spec/wtree.traceability.md`](../spec/wtree.traceability.md)

Scope:

- Audit and align root/per-command help, global and command how-to text,
  README, installation guidance where applicable, tutorial prose, and every
  advertised example with the implemented command/flag/error behavior.
- Change the tutorial fixture so it demonstrates plain init failure/hint,
  `add-ignore` dry-run/application, required commits, successful init, custom
  mount handling, and `create --add-ignore` without relying on pre-seeded
  custom ignore rules that hide the feature.
- Extend `wtree.spec.md` and `wtree.traceability.md` with command, safety,
  JSON, transaction, and test mappings; keep this feature specification as the
  detailed authority and remove contradictory claims.
- Add black-box acceptance coverage that executes the documented initialized
  and uninitialized workflows with real nested repositories and verifies
  outer status protection against accidental embedded-repository staging.
- Perform a final clean-room review of every acceptance scenario, plan-v2
  compatibility, executable version `0.2.0`, release layout, and repository
  quality gates. Do not publish artifacts.

Test-first slices:

1. Every advertised `add-ignore`, `init --add-ignore`, and
   `create --add-ignore` example parses and behaves as documented in hermetic
   fixtures; unsupported combinations fail consistently.
2. Run a complete three-level workflow from unsafe uninitialized tree through
   hinted repair, commits, init, default create, renamed create with
   `--add-ignore`, status, remove, checkout, and delete.
3. Prove each parent `git status --porcelain` omits its mounted child and
   `git add .` cannot stage a `160000` entry for managed mounts after the
   documented workflows.
4. Decode every public JSON document/error, verify stable arrays/action order,
   and map each source-spec acceptance scenario to enforcing code and tests.
5. Build local release artifacts and verify the binary reports `wtree 0.2.0`;
   inspect Linux/macOS/Windows CI evidence for portability-sensitive cases.

Verification:

- `go test ./internal/cli ./internal/service -run 'AddIgnore|Init.*Ignore|Create.*Ignore|EndToEnd|Help|HowTo' -count=1`
- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`
- `make check`
- `./scripts/release-build_test.sh`
- `git diff --check`

Exit criteria: all source-spec acceptance scenarios and traceability rows have
automated or named CI evidence; every public document agrees; the reviewer
approves a clean-room pass; all prior milestones remain green; and no release,
commit, or run-ledger change beyond this plan's authorized ledger occurred.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
