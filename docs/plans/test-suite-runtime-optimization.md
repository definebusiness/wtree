# Test-suite runtime optimization implementation plan

Status: initial
Readiness: ready to execute
Source specification: [Windows portability and CI hardening specification](../spec/windows-portability-and-ci-hardening.md), especially [§5](../spec/windows-portability-and-ci-hardening.md#5-portable-and-diagnostic-ci) and [§7](../spec/windows-portability-and-ci-hardening.md#7-required-verification)
Implementation context: [Test-suite runtime optimization context](test-suite-runtime-optimization-context.md)
Related plans: [Implemented Windows portability and CI hardening plan](windows-portability-and-ci-hardening.md); [unstarted Windows portability simplification and CI remediation plan](windows-portability-simplification-and-ci-remediation.md), especially M04; [local test acceleration implementation plan](local-test-acceleration.md)
Related local specification: [Local test verification strategy specification](../spec/local-test-verification-strategy.md)
Source of truth: [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml); [`scripts/ci-test.sh`](../../scripts/ci-test.sh); [`scripts/ci-helper_test.sh`](../../scripts/ci-helper_test.sh); [`scripts/ci-helper.md`](../../scripts/ci-helper.md); [`Makefile`](../../Makefile); [`internal/testutil`](../../internal/testutil); current tests under [`internal`](../../internal) and [`cmd/wtree`](../../cmd/wtree)
Delivery style: test-first, one reviewed milestone at a time; preserve complete normal/race coverage and product behavior; no dependency addition, commit, push, pull request, workflow dispatch, publication, or release without separate authorization

## Execution contract for Codex

When explicitly asked to run this plan, continue unattended until every
milestone is checked or a genuine external blocker is reached. Do not ask for
routine implementation decisions; the decisions below are fixed.

For each unchecked milestone, in order:

1. Read this plan, its implementation context, the source-specification
   sections, the durable run ledger at
   `docs/ai/runs/test-suite-runtime-optimization.md`, and the current worktree.
   Create that ledger before the first dispatch. On resumption, reconcile the
   plan, ledger, retained performance evidence, and worktree, then append a
   reconciliation checkpoint before dispatching more work.
2. Derive and record the complete milestone checklist from every scope item,
   test-first slice, exit criterion, documentation requirement, and
   verification command.
3. Give the complete initial packet to `implementer`. For remediation, use
   `implementer` while the ledger attempt count is 0 or 1 and
   `escalation-implementer` only when it is 2. Require RED → GREEN → REFACTOR
   evidence, changed paths, exact command results, before/after runtime or
   process-count evidence, and unresolved concerns.
4. Treat partial implementation or a still-running performance gate only as
   progress. Do not request review or change the remediation counter until
   every checklist item has evidence.
5. Send each complete submission to the read-only `reviewer`. Review must
   inspect the current shared filesystem, inventory conservation, test quality,
   fixture isolation, portability, workflow failure semantics, runtime claims,
   scope, and applicable sources of truth.
6. If review finds material issues, record the complete stable-ID finding set
   and return all unresolved findings in one test-first remediation packet.
   Apply the three-rejected-complete-remediation limit in
   [`milestone-supervision.md`](../ai/milestone-supervision.md). Do not use an
   escalation reviewer as a routine second review.
7. On reviewer approval, run the milestone verification as the main agent,
   update affected documentation and evidence, check the milestone, and append
   its concise execution-log row.
8. Immediately replace the ledger's current snapshot with the next milestone,
   append its checkpoint and pre-dispatch row, and dispatch its initial packet.
   Do not send a final response while any milestone remains unchecked.

Preserve unrelated worktree changes and every other plan's run ledger. Do not
use destructive cleanup. A failing test, missed performance target, reviewer
finding, or milestone transition is ordinary work, not a stop condition.
Hosted acceptance that requires a commit, push, pull request, or workflow
rerun is an external authorization boundary: prepare and verify the candidate,
then record the exact blocker if that authority is unavailable. Commit only
when separately authorized.

## Fixed implementation decisions

### Scope and precedence

- This plan optimizes test execution and CI orchestration. It does not change
  `wtree` product behavior, public CLI output, schemas, persistence, mutation
  semantics, supported platforms, or security boundaries.
- It is the focused owner for hosted test-suite runtime and parallel Windows CI
  partition execution. The related local plan owns developer lanes, bounded
  local workers, and local verification frequency.
- Fixture process cost, duplicate-scenario consolidation, and short-mode
  classification are shared prerequisites. Whichever authorized plan delivers
  them first must satisfy both specifications; the later run reconciles and
  reuses the implementation and evidence instead of repeating it.
- The unstarted Windows simplification plan contains overlapping M04 scope but
  is not superseded or abandoned. If either plan has already delivered an
  overlapping result when this plan is authorized, reconcile current behavior
  and reuse valid evidence instead of reimplementing it. Never update the
  other plan's checkbox, execution log, status, or run ledger from this run.
- Product-portability work from M00–M03 of that related plan is out of scope.
  A product defect exposed by profiling remains an ordinary test failure for
  its owning plan unless the smallest correction is necessary to preserve an
  existing test contract while optimizing the harness.

### Coverage is conserved, not traded for speed

- Required CI continues to discover and execute every top-level test, example,
  and fuzz target exactly once in normal mode and exactly once in race mode on
  Windows. Ubuntu and macOS retain their complete normal and race inventories.
- No required test moves to nightly-only, manual-only, or short-only coverage.
  This plan adds a developer fast lane without narrowing required full gates.
- A test or table row may be removed or consolidated only when a durable
  mapping names every assertion, failure window, and platform behavior it
  covered and identifies an equivalent or stronger retained test. Unique
  coverage must remain.
- Do not weaken timing, cancellation, process-tree cleanup, atomicity,
  durability, identity, authorization, privacy, rollback, recovery,
  concurrency, or adversarial assertions to meet a runtime target.
- Failing to meet a performance budget requires further profiling and
  optimization; it never authorizes skipped inventory or a larger timeout as
  the sole correction.

### Windows partitions become real CI shards

- Retain deterministic service partitioning because the measured monolithic
  Windows service inventory does not fit the authoritative bounded path.
- Move concurrency ownership from the shell loop to GitHub Actions. A Windows
  service matrix cell runs one mode (`normal` or `race`) and one shard index,
  not all shard indices sequentially.
- Use eight service shard indices initially and never increase beyond the
  existing eight without explicit user approval. Run non-service normal and
  race inventories once each in separate matrix cells or jobs.
- Set `strategy.fail-fast: false`. A failed cell remains fatal to the required
  workflow but does not cancel other cells and their diagnostics.
- Every service cell independently discovers the same lexicographically sorted
  inventory and selects targets by deterministic index modulo shard count.
  Empty, duplicate, unknown, missing, or incompletely assigned inventory fails
  closed in the deterministic harness.
- Keep modulo assignment unless measured uncached cells show the slowest shard
  exceeds 1.5 times the median. Only that evidence permits a deterministic
  duration-balanced assignment. Such an assignment must define behavior for
  new/stale targets, remain reproducible without network access, and retain an
  exact-once proof.
- Simplify helper diagnostics when one matrix cell owns one direct `go test`
  command. Raw logs, direct exit status, an elapsed/count summary, and a small
  GitHub annotation are sufficient; bespoke excerpt classification or log
  transport stays only when a test proves the hosting platform cannot provide
  equivalent evidence.

### Test-layer ownership

- Pure domain, parsing, planning, and rendering permutations belong in fast
  table-driven tests in the owning package.
- Real Git semantics belong at `internal/git` or the smallest service boundary
  that owns the behavior. They must not be replaced solely by mocks.
- `internal/service` owns business-state, transaction, authority, rollback,
  recovery, and failure-window matrices.
- `internal/cli` owns argument parsing, command composition, help/how-to,
  rendering, stdout/stderr, exit behavior, and a bounded representative set of
  end-to-end real-Git cases. It should not rebuild an exhaustive service-state
  matrix already proven below the CLI boundary.
- `cmd/wtree` retains process-boundary acceptance and executable wiring, not a
  third exhaustive copy of service behavior.
- Platform-specific tests retain divergent Windows/Unix filesystem and process
  contracts. Compile-only evidence is not a substitute for required native
  Windows runtime evidence.

### Fixture optimization and concurrency

- Preserve per-test mutable state. Never share a mutable working repository,
  worktree, registry, environment, listener, hook state, or process between
  tests.
- Reduce subprocesses before introducing in-process parallelism. In
  particular, configure hermetic Git author/committer identity through the
  fixture command environment or command-scoped configuration rather than two
  persistent `git config` subprocesses per repository, while retaining tests
  that prove no host configuration or credentials are consulted.
- Immutable seed objects or bare repositories may be reused only if tests prove
  that each consumer receives an isolated mutable namespace, cleanup remains
  deterministic on Windows, and Git behavior under test is unchanged.
- Do not add blanket `t.Parallel()`. Selective parallel tests require explicit
  isolation proof and must run under a measured `-parallel` bound. CI matrix
  isolation is preferred for the real-Git service inventory.
- Replace fixed sleeps with observable synchronization only when elapsed wall
  time is not itself the contract. Retain bounded deadlines as failure guards.

### Shared fast classification consumed by CI

- Consume the local plan's `make test-fast`/short-mode contract when it is
  already implemented. If this CI plan runs first, it may establish that shared
  prerequisite, but it does not own `check-local`, changed-area selection,
  bounded local workers, or verification-frequency policy.
- Real-Git fixture constructors and explicitly process-heavy helpers use one
  shared short-mode classification boundary. Tests skipped in short mode must
  state the expensive capability they require.
- Short mode must continue to run meaningful tests in every package. It must
  not skip pure error, rendering, parser, planner, contract, or fake-adapter
  tests merely because their package also contains integration tests.
- `make test` and `make test-race` remain exhaustive. `make check` remains the
  complete aggregate gate; it must not silently switch to short mode.

### Performance and cost budgets

- Use the hosted Windows baseline recorded in the context: `1h13m43s` overall,
  with `35m56s` normal and `35m24s` race inventories.
- On an uncached matching candidate, the Windows required-test execution
  critical path is at most 20 minutes and the complete Windows workflow
  execution critical path is at most 25 minutes, excluding runner queue time.
- Aggregate Windows normal/race test execution is at most 90 runner-minutes.
  The solution cannot claim success by multiplying runner consumption without
  bound.
- Ubuntu and macOS execution do not regress by more than 10% against a
  same-revision control without a documented runner-level explanation.
- `make test-fast` completes within two minutes on the same implementation host
  used for the complete-suite comparison and executes at least one meaningful
  non-helper test in every package.
- Every performance claim uses `-count=1` or otherwise proves the Go result
  cache was not credited. Record target count, command elapsed time, execution
  time, and queue time separately.

### Dependencies, authority, and non-goals

- Add no third-party profiling, test, sharding, YAML, or reporting dependency.
  Use Go, Bash, Git, and GitHub Actions primitives already required by the
  repository.
- Do not change production APIs merely to make tests faster. A narrow existing
  dependency seam may be reused; a new seam must improve ownership and remain
  unexported unless product code already requires it.
- Do not modify release artifacts, persisted data, real user repositories,
  root `.gitignore`, credentials, secrets, or host-global Git configuration.
- Do not commit, push, open a pull request, dispatch/rerun CI, publish, or
  release without separate authority.
- Queue delay, unavailable hosted runners, or lack of remote-mutation authority
  can be genuine external blockers for M04. Ordinary runtime variance, test
  failure, or additional fixture work is not.

## Stable contracts to establish early

### Test inventory identity

Owner: `scripts/ci-test.sh` and its deterministic harness.

Consumers: GitHub Actions Windows non-service/service jobs and local diagnostic
invocations.

Invariant: for a given source tree and mode, the discovered package inventory
and sorted top-level service target inventory are deterministic. Known helper
entry points are excluded from direct scheduling because their parent tests
invoke them explicitly. Every other target maps to exactly one shard index.

Evidence: fake-tool inventory tests plus an actual `go test -list` comparison;
empty, duplicate, unknown, missing, fewer-than-shards, and inventory-growth
cases fail or map exactly as specified.

Migration: the old sequential `normal|race` entry point may remain only during
M00 compatibility. M03 removes it from workflow use and deletes obsolete
branches/tests/docs once the matrix interface is green.

### Partition invocation and result

Owner: `.github/workflows/ci.yml` for fan-out; `scripts/ci-test.sh` for one-cell
selection and command construction.

Consumers: required checks and developers reproducing a failed cell.

Invariant: one invocation identifies mode, partition kind, shard index/count,
target count, timeout, start/end, and final command status. Invalid arguments
fail before `go test`. Test and output-transport failures remain distinguishable
only if transport is retained.

Evidence: deterministic command-capture tests and one native hosted matrix in
which every cell reports a unique identity and the aggregate inventory is
exact-once.

### Coverage-consolidation map

Owner: the current run ledger during implementation; stable conclusions are
summarized in the context document before M01 approval.

Consumers: implementer, reviewer, and future test maintainers.

Invariant: every removed/merged test row maps to retained evidence for all
success, error, mutation, rollback/recovery, platform, and output assertions.
Unmapped unique behavior blocks removal.

Evidence: reviewer audit of the mapping, focused RED/GREEN evidence, and full
normal/race inventory passes after consolidation.

### Fast/full lane boundary

Owner: `internal/testutil` plus explicit expensive helpers outside that package;
`Makefile` owns public developer targets.

Consumers: repository developers and CI verification.

Invariant: short mode omits real-Git/process-heavy integration setup but keeps
pure and fake-boundary tests. Full modes ignore the short classification and
remain exhaustive.

Evidence: deterministic helper classification tests, `go test -short -json`
package counts, `make test-fast`, and unchanged full normal/race inventory.

### Runtime evidence

Owner: workflow cell summaries and the durable run ledger.

Consumers: M01 optimization, M03 shard assessment, M04 acceptance, and future
CI maintenance.

Invariant: evidence distinguishes queue time, setup time, command elapsed time,
target count, cache state, mode, and partition. Warm cached reruns are labeled
and never used for acceptance.

Evidence: retained GitHub cell timestamps/logs and uncached local JSON output.

## Architecture and dependency boundaries

```text
Makefile developer lanes
├── test-fast → short-mode classifier → pure/fake tests
└── test/test-race/check → complete repository inventory

GitHub Actions
├── Ubuntu/macOS complete jobs
└── Windows matrix owner
    ├── non-service normal/race cell
    └── service mode × shard-index cell
        └── ci-test inventory/selection → one go test command

test layers
CLI contracts → service contracts → Git adapter → real Git
       └─ representative E2E ──────────────────┘
```

- Workflow YAML owns concurrency, matrix completeness, and aggregate required
  status. Shell code must not recreate a sequential workflow scheduler.
- `scripts/ci-test.sh` owns only discovery, deterministic selection, bounded
  command construction, and concise result metadata.
- `internal/testutil` owns hermetic reusable fixtures. Product packages must not
  import test utilities, and test utilities must not acquire product policy.
- Test packages own assertion placement according to the layer decisions above.
- Performance evidence is observational. It must not become a runtime product
  dependency or a checked-in machine-specific scheduler unless the 1.5×
  imbalance trigger is met and reviewed.

## Global definition of done

- Each behavior change has recorded RED → GREEN → REFACTOR evidence. Pure
  workflow rearrangement has a failing deterministic harness case or static
  contract check before the correction.
- Static and runtime inventories prove no loss or duplication per mode.
- Any consolidated test has a complete retained-coverage mapping with no
  unresolved unique assertion.
- Git fixtures remain hermetic against host configuration, credentials, hooks,
  maintenance, and environment leakage; each test owns mutable state.
- Failure, timeout, cancellation, race, command, and retained transport errors
  remain fatal and diagnosable.
- Windows path, process, sharing, cleanup, and filesystem semantics are proven
  by native Windows CI, not inferred solely from cross-compilation.
- No required full gate uses short mode, cached results, skipped inventory, or
  raised timeouts as performance evidence.
- Documentation describes the fast/full lanes, one-cell reproduction command,
  runtime budget, and helper maintenance contract.
- The applicable exact commands pass on the final source state:

  ```bash
  bash -n scripts/ci-test.sh
  bash scripts/ci-helper_test.sh
  make fmt-check
  go vet ./...
  make test-fast
  go test -timeout=30m ./... -count=1
  go test -race -timeout=45m ./... -count=1
  make build
  make release-test
  make tutorial-test
  make check
  git diff --check
  ```

- Do not rerun an identical expensive command merely for a differently named
  gate when the source, environment, flags, and inventory are unchanged;
  reference the retained terminal evidence. `make check` remains required at
  final acceptance because its orchestration is itself in scope.
- One matching GitHub Actions revision passes every Ubuntu, macOS, and Windows
  required cell and meets the performance/cost budgets. If starting that run
  requires unauthorized remote mutation, M04 records an external blocker and
  neither the plan nor source specification becomes `implemented`.
- An independent reviewer approves every milestone with no unresolved material
  finding. This plan changes to `implemented` only after M04 and the entire
  plan are approved and verified. The source specification remains `planned`
  while any other plan required for its full scope remains unimplemented.

## Milestones

### [ ] M00 — Establish measurable inventory and one-partition contracts

Specification coverage: [§5 portable and diagnostic CI](../spec/windows-portability-and-ci-hardening.md#5-portable-and-diagnostic-ci) and [§7 required verification](../spec/windows-portability-and-ci-hardening.md#7-required-verification).

Scope:

- Reconcile the context snapshot with the execution tree and record fresh
  top-level inventory, fixture/process call-site counts, test/production lines,
  local package durations, and available hosted baseline evidence.
- Add a deterministic one-partition interface for non-service mode and service
  mode/index/count while retaining the old sequential interface temporarily so
  CI behavior does not change in this milestone.
- Make each invocation report mode, kind, index/count, selected target count,
  timeout, elapsed time, and exit status without parsing human test output for
  success.
- Extend the fake-tool harness for invalid modes/indices/counts, exact command
  construction, sorted deterministic selection, empty/duplicate/missing/
  unknown inventory, helper exclusion, fewer targets than shards, inventory
  growth, failure status, and cleanup for any retained temporary artifacts.
- Update the context with the reconciled baseline and exact reproduction
  commands. Do not optimize tests or switch workflow fan-out yet.

Test-first slices:

1. Add failing harness cases showing that the current all-shards interface
   cannot execute exactly one requested service partition or one non-service
   mode with a stable identity, then implement the narrow interface.
2. Add invalid index/count and changed-order inventories; prove selection is
   deterministic, disjoint, exhaustive, and fail-closed.
3. Inject command and optional transport failures and prove the direct status
   and summary remain truthful without relying on fragile excerpt matching.
4. Run the unchanged workflow-equivalent sequential path and compare its
   aggregate discovered inventory with the union of all one-partition calls.

Verification:

- `bash -n scripts/ci-test.sh`
- `bash scripts/ci-helper_test.sh`
- One fake-tool union-of-eight normal run and one race run from the harness.
- `go test -list '^(Test|Example|Fuzz)' ./internal/service`
- `make fmt-check`
- `git diff --check`

Exit criteria: current runtime and inventory are durably measured; one-cell
selection is deterministic and testable; the union exactly matches the old
complete inventory; and required CI still uses its prior behavior pending M03.

### [ ] M01 — Remove avoidable Git-fixture and duplicated scenario cost

Specification coverage: [§6 compatibility and safety boundaries](../spec/windows-portability-and-ci-hardening.md#6-compatibility-and-safety-boundaries) and the [context fixture/test-layer analysis](test-suite-runtime-optimization-context.md#5-why-windows-amplifies-the-cost).

Scope:

- Profile uncached `internal/service`, `internal/cli`, and `internal/git` normal
  and race execution using JSON output; rank packages, top-level tests, fixture
  construction, deliberate waits, and spawned-process counts.
- Remove the two persistent identity-configuration subprocesses from the
  canonical Git repository fixture. Prove author/committer identity remains
  hermetic and commits cannot read hostile user/system configuration.
- Replace fixed waits in measured hotspots with channels, process-ready files,
  wait groups, polling of observable state, or injected clocks only where the
  delay itself is not under test. Preserve deadlines as bounded failure guards.
- Consolidate measured duplicate service/CLI/cmd scenario matrices according
  to the fixed ownership rules. Record every removed test or row in the
  coverage-consolidation map before deletion.
- Reduce repeated expensive setup inside retained table tests using immutable
  inputs or isolated per-case builders. Do not share mutable repositories or
  weaken failure-window independence.
- Keep changes within test files and `internal/testutil` unless a narrow
  unexported seam is necessary to observe existing behavior deterministically.

Test-first slices:

1. Instrument the fixture command boundary and add a failing expectation that
   ordinary repository construction uses one initialization command rather
   than initialization plus two config writes; retain hostile-config commit
   success and credential/hook isolation tests.
2. For each selected sleep hotspot, demonstrate the same readiness or cleanup
   transition through observable synchronization and prove the existing
   timeout failure still terminates within its bound.
3. For each duplicate matrix, record the old assertions, introduce or identify
   the retained owning-layer case, then delete only the mapped duplicate and
   run both owning and consumer contract suites.
4. Compare uncached before/after command counts and elapsed distributions on
   the same host with no competing test process.

Verification:

- `go test ./internal/testutil -count=1`
- `go test ./internal/git -count=1`
- `go test ./internal/service -count=1 -timeout=30m`
- `go test ./internal/cli ./cmd/wtree -count=1 -timeout=30m`
- `go test -race ./internal/git ./internal/service ./internal/cli ./cmd/wtree -count=1 -timeout=45m`
- `make fmt-check`
- `go vet ./...`
- `git diff --check`

Exit criteria: canonical repository initialization drops from three Git
processes to one without losing hermetic identity; every removed wait or test
has observable replacement evidence and a complete coverage map; mutable
fixtures remain isolated; and the same-host uncached service-plus-CLI normal
elapsed total improves by at least 15% without a race regression or inventory
loss.

### [ ] M02 — Establish or consume the shared fast-test boundary

Specification coverage: [§5 portable and diagnostic CI](../spec/windows-portability-and-ci-hardening.md#5-portable-and-diagnostic-ci) and the [context target execution model](test-suite-runtime-optimization-context.md#8-target-execution-model).

Scope:

- Reconcile the related local plan. Reuse its approved short classifier and
  `make test-fast` target when present; otherwise establish only this shared
  prerequisite under the local verification specification.
- Add one shared `testing.Short()` classification helper for real-Git fixture
  creation and explicit process-heavy integration helpers when it is absent;
  use it at the smallest setup boundary.
- Audit direct Git/process setup not routed through the shared helpers and mark
  only genuinely expensive integration entry points with an explicit reason.
- Add `make test-fast` with an explicit two-minute Go timeout. Keep `test`,
  `test-race`, and `check` exhaustive and unchanged in coverage.
- Add deterministic tests proving short mode skips expensive setup before the
  first external command while normal mode still executes it.
- Capture a short-mode JSON inventory. Prove each package runs meaningful
  non-helper coverage and pure/fake-boundary tests in mixed integration
  packages remain active.
- Document intended developer use, the fact that short mode is not release
  evidence, and the local plan's ownership of broader local verification
  policy.

Test-first slices:

1. Add a fixture-boundary test that fails because short mode would currently
   spawn Git, then skip before command construction with a precise reason.
2. Run representative mixed-package short selections and prove parser,
   planner, error, rendering, and fake-adapter tests still execute while real
   repository setup does not.
3. Add the Make target and prove its timeout, flags, failure propagation, and
   separation from complete targets with a controlled command shim or Make
   dry-run contract.
4. Run short and complete inventories back-to-back and prove short mode is an
   additive lane rather than a source of full-suite skips.

Verification:

- `go test ./internal/testutil -count=1`
- `make test-fast`
- `go test -short -json ./... -count=1`
- `make -n test test-race check`
- `go test -timeout=30m ./... -count=1`
- `make fmt-check`
- `git diff --check`

Exit criteria: the documented fast lane completes within two minutes on the
baseline host, exercises meaningful coverage in every package, starts no
shared-fixture Git process, and leaves the exhaustive normal/race/check paths
unchanged.

### [ ] M03 — Execute Windows test partitions as parallel required matrix cells

Specification coverage: [§5 portable and diagnostic CI](../spec/windows-portability-and-ci-hardening.md#5-portable-and-diagnostic-ci), [§7 required verification](../spec/windows-portability-and-ci-hardening.md#7-required-verification), and the [context target architecture](test-suite-runtime-optimization-context.md#8-target-execution-model).

Scope:

- Split Windows quality/build/release work, non-service normal/race work, and
  service normal/race shard work into explicit GitHub Actions jobs or matrix
  cells with `fail-fast: false`.
- Use the M00 one-partition interface so each service cell runs one of eight
  indices and each non-service mode runs exactly once.
- Preserve LF checkout, NUL-safe formatting, vet, build, release layout,
  release-directory reuse, manifest checks, tutorial coverage, timeouts, and
  every current required outcome.
- Provide one direct local reproduction command in the cell name/log summary.
  Keep raw Go output and direct fatal status; remove sequential-loop,
  accumulated-failure, temporary-log, or excerpt-taxonomy code that no longer
  has a consumer.
- Update the deterministic harness and contributor guidance together with each
  removed helper branch. Do not retain dead compatibility behavior for
  historical symmetry.
- Verify the workflow matrix enumerates every mode/index pair and has one
  aggregate required outcome. Queue delay must be distinguishable from command
  execution time.
- Measure shard distribution. Retain modulo selection unless the documented
  1.5× imbalance trigger is met.

Test-first slices:

1. Add a workflow/harness contract that fails because one Windows job currently
   invokes all service shards sequentially; make each generated cell construct
   exactly one command.
2. Inject one failed cell and prove the aggregate fails while other cells are
   not canceled; inject full success and prove build/release/manifest outcomes
   remain required.
3. Compare the normal/race union of matrix commands with the M00 authoritative
   inventory and prove exact-once package/target coverage.
4. Remove obsolete helper machinery and prove raw failure, timeout, panic,
   race, compile, and ordinary assertion output remains visible and fatal.
5. Run a local controlled matrix simulation with at most the measured safe
   concurrency and record target counts, elapsed time, and aggregate status.

Verification:

- `bash -n scripts/ci-test.sh`
- `bash scripts/ci-helper_test.sh`
- The repository-native workflow structure check introduced by this milestone.
- Controlled one-cell commands for non-service normal/race and service indices
  `0` through `7` in both modes, with union validation.
- `go test -timeout=30m ./... -count=1`
- `go test -race -timeout=45m ./... -count=1`
- `make fmt-check`
- `go vet ./...`
- `make build`
- `make release-test`
- `git diff --check`

Exit criteria: workflow concurrency, rather than a shell loop, owns eight-way
service fan-out; every normal/race target and non-service package executes
exactly once; failures remain visible and fatal without canceling siblings;
obsolete orchestration code is removed; and controlled evidence projects the
Windows required-test critical path within 20 minutes without exceeding the
90-runner-minute budget.

### [ ] M04 — Prove bounded cross-platform runtime and close documentation

Specification coverage: [§§5–7](../spec/windows-portability-and-ci-hardening.md#5-portable-and-diagnostic-ci).

Scope:

- Freeze the final candidate and audit the complete diff, coverage map,
  inventory union, short/full boundary, removed helper behavior, documentation,
  workflow requirements, and runtime evidence.
- Run the final local definition-of-done commands once on unchanged source,
  retaining terminal output so identical expensive evidence is not needlessly
  repeated.
- Compile affected platform-specific packages for Windows before hosted
  execution, but do not treat cross-compilation as native acceptance.
- Obtain one matching GitHub Actions revision with all Ubuntu, macOS, and
  Windows cells green. Record per-cell queue/setup/test times, target counts,
  aggregate runner-minutes, slowest/median shard ratio, and complete critical
  paths.
- Meet the 20-minute Windows test, 25-minute complete Windows, 90-runner-minute,
  two-minute fast-lane, exact-once, and non-Windows non-regression budgets.
- If a budget misses, continue measured remediation within M01–M03 scope; do
  not drop tests, raise shard count above eight, or raise timeouts as the sole
  correction.
- Update the plan execution evidence, context, specification relationship,
  helper guidance, contributor-facing commands, and status overview. Change
  this plan to `implemented` after its full scope is approved and verified.
  Keep the source specification `planned` while another plan required for its
  remaining scope is still unimplemented.

Test-first slices:

1. Run the final static inventory comparison and fail on any missing,
   duplicated, short-only, or unmapped target before accepting runtime data.
2. Exercise representative test failure, timeout, race, helper-process, and
   matrix-cell failure paths and prove the required aggregate remains red with
   actionable raw evidence.
3. Run the complete local normal/race/check sequence on frozen source and
   reconcile every command with the global definition of done.
4. Run the matching hosted matrix, distinguish queue time from execution, and
   compare every budget with the implemented baseline.

Verification:

- `bash -n scripts/ci-test.sh`
- `bash scripts/ci-helper_test.sh`
- `make fmt-check`
- `go vet ./...`
- `make test-fast`
- `go test -timeout=30m ./... -count=1`
- `go test -race -timeout=45m ./... -count=1`
- `make build`
- `make release-test`
- `make tutorial-test`
- `make check`
- `git diff --check`
- Cross-compilation of every affected Windows-specific test package into an
  isolated temporary directory, followed by verified artifact cleanup.
- One matching required GitHub Actions run with the retained per-cell timing
  and exact-inventory evidence.

Exit criteria: all local and hosted gates pass on one frozen revision; Windows
test execution is at most 20 minutes and its complete path at most 25 minutes;
aggregate runner consumption is at most 90 minutes; no inventory or unique
coverage is lost; other platforms do not materially regress; documentation and
lifecycle metadata agree; and independent review has no unresolved material
finding. Lack of authority to create the matching hosted revision is recorded
as an exact external blocker rather than bypassed.

## Execution log

Append entries only after a milestone is independently approved and verified;
do not rewrite earlier evidence. Active packets, findings, attempts, and resume
instructions belong in
`docs/ai/runs/test-suite-runtime-optimization.md`, created only after execution
is explicitly authorized.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
