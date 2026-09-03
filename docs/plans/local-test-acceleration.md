# Local test acceleration implementation plan

Status: implemented
Readiness: ready to execute
Source specification: [Local test verification strategy specification](../spec/local-test-verification-strategy.md)
Implementation context: [Local test acceleration context](local-test-acceleration-context.md)
Related CI plan: [Test-suite runtime optimization implementation plan](test-suite-runtime-optimization.md)
Related CI specification: [Windows portability and CI hardening specification](../spec/windows-portability-and-ci-hardening.md)
Source of truth: [`Makefile`](../../Makefile); [`scripts/ci-test.sh`](../../scripts/ci-test.sh); [`scripts/ci-helper_test.sh`](../../scripts/ci-helper_test.sh); [`internal/testutil`](../../internal/testutil); test packages under [`internal`](../../internal) and [`cmd/wtree`](../../cmd/wtree); [`docs/ai/plan-authoring.md`](../ai/plan-authoring.md); [`docs/ai/milestone-supervision.md`](../ai/milestone-supervision.md)
Delivery style: test-first, one reviewed milestone at a time; optimize local feedback without reducing complete evidence; no product behavior change, dependency addition, commit, push, pull request, workflow dispatch, publication, or release without separate authorization

Scope amendment (2026-09-03): the user accepted the retained 8m29.01s
complete-normal result as good enough and ended further performance tuning.
The accepted budgets are therefore 8m30s for complete normal and 19m for
complete normal plus race on the reference host. Native Windows execution and
Windows test remediation remain with the separately active Windows/CI work and
do not block this local-only plan.

This 2026-09-03 authorization also supersedes earlier M00, M01, and M04 text
that described native Windows behavior or hosted evidence as a local-plan
completion gate. M04 requires approved local evidence and affected tooling
cross-compilation only. It honestly makes no native Windows acceptance claim;
that evidence and any remediation remain owned by the separate Windows/CI
work.

For M02 only, that authorization also waives a same-topology, final-source
complete-race rerun. The accepted performance reference pairs the 509.01s
physical-batch worker-4 normal with the retained 620.72s coarse-service
worker-4 race. Those executions have different scheduler topologies, so their
18m49.73s sum is an accepted policy reference, not a demonstrated
same-configuration normal-plus-race bound. This narrow waiver supersedes
conflicting M02/final-freeze wording below, but does not weaken exact
inventory/result conservation, the 1–4 owned-command cap, focused-race
contracts, failure/cancellation checks, or terminal-race requirements in any
other plan.

## Execution contract for Codex

When explicitly asked to run this plan, continue unattended until every
milestone is checked or a genuine external blocker is reached. Do not ask for
routine implementation choices; this plan fixes them below.

For each unchecked milestone, in order:

1. Read this plan, the source specification, implementation context, related
   CI plan, durable run ledger at
   `docs/ai/runs/local-test-acceleration.md`, and current worktree. Create the
   ledger before the first dispatch. On resumption, reconcile the plan, ledger,
   retained timing artifacts, owned processes, and filesystem before appending
   a reconciliation checkpoint.
2. Record a complete milestone checklist containing every scope item, test-
   first slice, exit criterion, documentation update, performance budget, and
   verification command.
3. Dispatch the complete initial packet to `implementer`. Use `implementer`
   for remediations while attempts are 0 or 1 and
   `escalation-implementer` only for a remediation starting at attempts 2.
   Require RED → GREEN → REFACTOR evidence, changed paths, exact command
   results, before/after wall and process evidence, inventory counts, and
   unresolved concerns.
4. Treat a running benchmark, partial target, incomplete coverage map, or
   invalidated prior timing as progress only. Do not request review or change
   the remediation counter until the complete packet is evidenced.
5. Send every complete submission to the read-only `reviewer`. Review covers
   the shared filesystem, lane semantics, coverage conservation, reverse-
   dependency selection, runner isolation, failure behavior, timing validity,
   cross-platform behavior, documentation, and scope.
6. Record material findings with stable IDs and return the entire unresolved
   set in one remediation packet. Apply the exact three-rejected-complete-
   remediation process in
   [`milestone-supervision.md`](../ai/milestone-supervision.md). Do not use
   escalation review as a routine second review.
7. After reviewer approval, run the milestone's main-agent verification on a
   quiescent host, update evidence/docs, check the milestone, and append its
   concise execution-log row.
8. Immediately install the next milestone's complete ledger snapshot, append
   its checkpoint and pre-dispatch row, and dispatch it. Do not send a final
   response while unchecked work remains.

Preserve unrelated changes and every other run ledger. Do not destructively
clean processes, caches, or user files. Interrupt only runner-owned processes
whose identity is proven. Ordinary test failures, flakes requiring diagnosis,
missed budgets, review findings, and milestone transitions do not permit a
stop. Native hosted evidence requiring a commit, push, pull request, or
workflow rerun is an external authorization boundary; record it rather than
performing remote mutation without authority. Commit only when separately
authorized.

## Fixed implementation decisions

### Scope, ownership, and relationship to other plans

- This plan owns local test lanes, changed-area selection, bounded local
  workers, duration-balanced local assignment, local timing cache, complete
  target internals, fixture/process reductions selected by local profiling,
  and future plan-authoring guidance.
- The related CI plan owns GitHub matrix fan-out and hosted Windows critical
  path. Hosted parallelism alone is not credited as local acceleration.
- Both plans may need the same deterministic target inventory and fixture
  optimization. If either has implemented that shared capability before this
  plan runs, reconcile and reuse the current contract/evidence. Do not duplicate
  it or update the other plan's checkbox, execution log, status, or run ledger.
- Product behavior, CLI output, persistence, transaction semantics, supported
  platforms, and public schemas are out of scope.
- This plan does not amend any active or blocked plan. Its gates become the
  default only for newly authored plans after M04. An existing plan adopts them
  only through explicit user-authorized scope change recorded in that plan's
  own ledger.

### Explicit local target contract

- Add `make check-local` for the deterministic two-minute feedback gate:
  format, vet, short tests, representative real-Git/process smoke tests, runner
  harness, and build.
- Add `make test-changed BASE_REF=<commit>` for normal changed-package and
  reverse-dependency tests. A missing/invalid base fails before test execution.
- Add `make test-changed-race PACKAGES='<patterns>'` for explicit risk-selected
  race packages. It does not infer semantic race sufficiency from filenames.
- Add `make test-full` and `make test-full-race` for complete normal/race
  inventories through the bounded runner.
- Add `make check-full` for complete normal, complete race, tutorial, release,
  format, vet, runner harness, and build evidence.
- Keep `make check` as a compatibility alias for `check-full`. Make its
  internals faster through the bounded runner, but do not silently narrow its
  evidence.
- Keep `make test` and `make test-race` exhaustive aliases for the corresponding
  full targets. Existing callers retain complete semantics.

### Fast lane classification

- Use one helper in `internal/testutil` to skip before starting real Git or an
  explicitly process-heavy integration fixture when `testing.Short()` is true.
  The skip reason names the expensive capability.
- Audit direct Git/process setup outside the shared helper. Mark only the
  smallest expensive top-level entry point; do not scatter short checks through
  assertions after setup has started.
- Pure rules, parsing, planning, rendering, error contracts, serialization,
  fake adapters, and deterministic helper tests remain active in short mode
  even inside `internal/service`, `internal/cli`, and `internal/git`.
- Short mode must execute at least one meaningful non-helper test per package.
  An explicit representative smoke set covers real Git and process wiring that
  short mode intentionally omits.
- Short mode is never complete milestone, release, or terminal race evidence.

### Changed-area selection

- `BASE_REF` is mandatory and must resolve to a commit. Compare it with the
  current working tree, including staged, unstaged, and untracked paths.
- A changed Go production package selects itself plus all in-repository reverse
  dependencies obtained from `go list` dependency data.
- A package-local `_test.go` change selects its owning package. A change under
  `internal/testutil` selects every test package that imports it.
- Changes to `Makefile`, test-runner code, CI helpers, or workflow files select
  their deterministic harness plus the smallest representative command path.
- Platform-specific files select native tests when available and cross-compile
  checks for other affected platforms. Cross-compilation never substitutes for
  native behavioral evidence when that behavior is in a plan's scope; the
  explicit M04 amendment confines this local plan's Windows evidence to
  compile-only tooling checks.
- Documentation-only changes select formatting/link/documentation checks, not
  unrelated complete Go integration by default.
- Deleted/renamed packages, an unreadable dependency graph, or ambiguous module
  ownership fails closed with an actionable diagnostic.

### Runner implementation and isolation

- Implement a repository-native standard-library Go command under
  `tools/test-runner`. Product packages must not import it.
- The runner supports inventory, changed-area selection, normal/race execution,
  worker count, logical shard count, per-command timeout, timing-cache override,
  full/failed output, and explicit non-authoritative fail-fast mode.
- Use eight logical service shards and a worker pool of 1–4 processes. Default
  local workers are 4. The cap includes non-service `go test` commands owned by
  the runner.
- Normal and race modes run separately. Do not run both simultaneously by
  default because their combined compiler, filesystem, and process load would
  make timing and reliability less predictable.
- Each shard is a separate `go test` process. Mutable test state is not shared
  across shards. The runner owns only its command processes, temporary logs,
  and timing cache writes.
- Authoritative full runs start every assigned unit, accumulate failures, emit
  complete failed-unit output plus concise success summaries, and return
  nonzero if any unit or log/timing operation required for truthfulness fails.
- Interruption cancels only proven runner-owned commands, waits for them to
  terminate within a documented bound, reports survivors, and removes only an
  exactly validated runner-owned temporary directory.

### Inventory and subprocess-helper contract

- Discover packages with `go list` and top-level service targets with
  `go test -list '^(Test|Example|Fuzz)'`.
- Maintain one canonical tracked inventory of subprocess-helper entry points
  consumed by both the current CI helper and local runner until the CI plan
  removes or replaces it. Parent tests continue to invoke those helpers; the
  scheduler never runs them directly.
- Sort schedulable targets deterministically. Empty, duplicate, missing,
  unknown, or incompletely assigned inventory fails closed in authoritative
  mode.
- Validate the union of all logical shards against the discovered inventory
  before starting commands. Every target runs exactly once per mode.
- A new helper-like name is not automatically excluded. Its parent/child
  contract and canonical inventory entry must be added together with tests.

### Duration-balanced assignment and cache

- Store timing weights below a runner-owned directory under `go env GOCACHE`,
  separated by `GOOS`, `GOARCH`, normal/race mode, and format version. Accept a
  test-only/diagnostic path override.
- Store only target name, elapsed duration, sample count, and observation
  timestamp in a simple versioned text format. Never store repository content,
  paths from test output, environment values, credentials, or secrets.
- With valid weights, use deterministic longest-processing-time-first
  assignment: descending duration/name, place into the lightest shard, and
  break load ties by shard index.
- Assign unknown targets the current median weight. Ignore stale targets. A
  missing, corrupt, unsupported, or unwritable timing cache is reported and
  falls back to sorted round-robin; cache failure never changes coverage.
- Update weights atomically after a complete observation. Failed or canceled
  targets may update only with clearly marked partial samples that cannot
  displace the last complete value.
- Do not check machine-specific timing weights into the repository.

### Failure, output, and cache semantics

- Pass `-count=1` for acceptance timings. The runner may support cached
  developer feedback only when output labels it and no performance/evidence
  claim uses it.
- Preserve Go timeout, failure, panic, race, compile, and ordinary assertion
  output. Do not reduce diagnostics to a bespoke taxonomy.
- Successful shard output may be summarized after retaining target count and
  elapsed time. Failed shard output is printed in full subject only to an
  explicit bounded log-size guard that preserves the tail and artifact path.
- Timing cache and temporary-log writes are private to the current user and use
  atomic replacement. Cleanup never follows symlinks or deletes a broad path.
- An iteration-only `--fail-fast` mode may cancel queued units after the first
  failure. Its result is never accepted as complete inventory evidence.

### Fixture, sleep, and in-process parallelism policy

- Remove two persistent Git identity configuration subprocesses from ordinary
  repository construction by using the already-sanitized fixture command
  environment or command-scoped configuration. Prove hostile host config,
  credentials, hooks, and system config remain irrelevant.
- Consolidate duplicate real-Git setup only with an old-to-retained coverage
  map for success, failure, mutation, rollback/recovery, platform, and output
  assertions.
- Share immutable inputs only when every test receives isolated mutable
  repositories, worktrees, registries, environments, listeners, hooks, and
  processes.
- Replace measured fixed waits with observable synchronization only when wall
  duration is not the contract. Keep deadlines as failure guards.
- Add `t.Parallel()` only to measured pure/isolated tests, with explicit
  isolation evidence and `-parallel=4` or lower. Process-level sharding is the
  default acceleration mechanism for service integration tests.

### Verification frequency and evidence reuse

- Iteration uses the smallest focused RED/GREEN/refactor test, adding focused
  race for the risk categories in the source specification.
- A complete implementation/remediation submission uses `check-local`, changed
  normal, focused race, and milestone-specific tests.
- A reviewer-approved frozen milestone candidate runs one complete uncached
  normal suite plus milestone-specific acceptance unless its source plan
  explicitly requires more.
- A terminal plan candidate obtains complete normal, tutorial/release, and
  authoritative complete race evidence from matching CI or one local full-race
  run when matching CI is unavailable, except for the explicitly documented
  M02 scope amendment above.
- An unchanged terminal result may satisfy another identically configured gate;
  cite it rather than executing it again. A source or test change invalidates
  affected evidence and requires the applicable boundary again.
- `docs/ai/milestone-supervision.md` state transitions and review/remediation
  limits remain unchanged. M04 updates plan-authoring guidance, not historical
  ledgers or completed plans.

### Performance, stability, and authority boundaries

- On the same uncontended reference host with `-count=1`, achieve:
  `check-local` ≤2 minutes, ordinary changed-area feedback ≤5 minutes,
  complete normal ≤8 minutes 30 seconds, and complete normal plus race
  ≤19 minutes.
- Compare against the captured approximately 33-minute service normal-plus-
  race baseline. Record Go version, platform, source revision, workers, target
  counts, wall time, and aggregate process time when available.
- Four workers must not use more than 1.5 times the serial aggregate process
  time or produce a new flake/leak in five repeated focused stress runs. If it
  does, lower the default to the highest stable measured value within 1–4 and
  document the evidence; never increase the cap above four in this plan.
- Add no third-party dependency. Do not change product APIs for the runner.
- Do not modify user caches outside the runner-owned subtree, host-global Git
  configuration, credentials, real repositories, persisted product data, root
  `.gitignore`, release artifacts, or unrelated worktree changes.
- Do not commit, push, open a PR, dispatch CI, publish, or release without
  separate authority.

## Stable contracts to establish early

### Runner inventory model

Owner: `tools/test-runner` with canonical subprocess-helper data shared by the
current CI helper.

Consumers: Make full targets, changed-area selection, CI-plan reconciliation,
and developers reproducing one shard.

Invariant: package and top-level target identity is deterministic; helper
children are excluded only by an explicit parent-tested entry; every remaining
target belongs to one logical unit per mode.

Evidence: fake command executor tests, actual inventory comparison, and union
validation across empty/duplicate/growing inventories.

### Worker scheduler

Owner: an internal package under `tools/test-runner`; it accepts executable
units and does not understand product behavior.

Consumers: complete normal/race and diagnostic one-shard runs.

Invariant: active commands never exceed the configured 1–4 cap; authoritative
mode drains all units and accumulates status; fail-fast is explicitly labeled;
interruption affects owned commands only.

Evidence: deterministic fake units with barriers, failure/cancellation cases,
process leak checks, and repeated real focused trials.

### Timing weight format and assignment

Owner: `tools/test-runner` timing package.

Consumers: local scheduler only.

Invariant: versioned, non-sensitive, platform/mode-specific observations yield
deterministic LPT assignment; invalid cache falls back without inventory loss.

Evidence: golden round trips, corrupt/unknown version, atomic-write failure,
unknown/stale target, equal-duration tie, and exact-once property tests.

### Verification lane contract

Owner: `Makefile` and contributor/agent plan-authoring guidance.

Consumers: developers, implementers, reviewers, and main-agent verification.

Invariant: target names state their evidence strength; compatibility targets
remain exhaustive; fast/changed results cannot be mislabeled complete.

Evidence: Make command-capture tests, short/full inventory comparisons, and
source-plan template review.

### Coverage-consolidation map

Owner: the active run ledger while M03 executes; stable conclusions are copied
to the context before approval.

Consumers: implementer, reviewer, and future maintainers.

Invariant: no test/row is removed until all unique assertions and failure
windows map to equivalent or stronger retained evidence.

Evidence: mapping audit plus owning/consumer focused and complete normal/race
passes.

## Architecture and dependency boundaries

```text
Makefile
├── check-local ──→ short + smoke + harness + static/build
├── test-changed ─→ explicit base → reverse dependency closure
├── test-full ──→ local runner normal
├── test-full-race → local runner race
└── check/check-full → complete compatibility evidence

tools/test-runner (standard library only)
├── inventory → packages + service top-level targets - helper children
├── assignment → timing weights → eight logical shards
├── scheduler → bounded 1–4 go test processes
└── evidence → labeled output + elapsed/count + atomic timing update

test ownership
pure/fake tests → fast lane
real Git/process tests → isolated full shards
focused risk selection → local race
complete race → terminal local or matching authoritative CI
```

- `tools/test-runner` may invoke `go`, read Git diff/inventory, and manage its
  own cache/logs. It must not import product `internal` packages or encode
  product semantics.
- `internal/testutil` owns hermetic fixture construction and short-mode
  classification. Product code never imports test utilities.
- Make owns user-facing lane names and composition, not scheduling algorithms.
- Plan-authoring guidance owns future evidence frequency. Milestone supervision
  continues to own orchestration state and review/remediation rules.
- The related CI plan may reuse inventory/assignment primitives only after
  review confirms their hosted failure and logging semantics; local adoption
  does not silently change CI.

## Global definition of done

- Every changed behavior has RED → GREEN → REFACTOR evidence; scheduler and
  cache behavior use deterministic fake executors/clocks/filesystems where
  real timing would be flaky.
- Static and dynamic inventory comparisons prove exact-once complete normal and
  race coverage, and short mode proves meaningful per-package execution.
- Changed-area selection covers reverse dependencies, testutil consumers,
  deletes/renames, untracked files, invalid bases, scripts/workflows, and
  platform files without guessing silently.
- Worker cap, failure aggregation, optional fail-fast labeling, interrupt
  cleanup, output preservation, timing atomicity, cache fallback, and secret
  exclusion are tested.
- Git fixtures remain hermetic, mutable state remains per-test, and every
  consolidated test has a complete coverage map.
- Performance evidence is same-host, uncached, uncontended, and records required
  metadata. Five focused stress repetitions introduce no flake, leaked process,
  or runner-owned artifact.
- Documentation explains target choice, race-risk selection, timing cache,
  worker tuning, one-shard reproduction, compatibility aliases, and adoption
  limits for existing plans.
- The final frozen source passes:

  ```bash
  go test ./tools/test-runner -count=1
  bash scripts/ci-helper_test.sh
  make fmt-check
  go vet ./...
  make check-local
  make test-changed BASE_REF=<recorded-valid-base>
  make test-changed-race PACKAGES='<recorded-risk-packages>'
  TEST_JOBS=1 make test-full
  TEST_JOBS=4 make test-full
  TEST_JOBS=1 make test-full-race
  TEST_JOBS=4 make test-full-race
  make build
  make release-test
  make tutorial-test
  make check-full
  git diff --check
  ```

- Serial and four-worker full inventories match exactly; target unions and
  status agree. Do not rerun identical expensive commands after unchanged
  source merely because another target composes the same command; reference the
  retained result.
- Affected platform-specific packages compile for Windows. Native Windows
  execution and remediation are evidence owned by the related Windows/CI work;
  this plan neither runs nor claims that platform acceptance.
- Each milestone has independent reviewer approval with no unresolved material
  finding. This plan becomes `implemented` only after M04; the specification
  becomes `implemented` only when its full plan scope is delivered and verified.

## Milestones

### [x] M00 — Establish exact inventory, timing, and runner foundations

Specification coverage: [§§5–6](../spec/local-test-verification-strategy.md#5-bounded-local-concurrency) and [§10](../spec/local-test-verification-strategy.md#10-required-acceptance-evidence).

Scope:

- Reconcile and update the context snapshot with current top-level inventory,
  subprocess-helper names, Git fixture/process call sites, sleeps, `t.Parallel`
  sites, serial normal/race package times, Go version, platform, and source
  revision. Ensure no competing repository test process skews the baseline.
- Create `tools/test-runner` with testable command-execution, inventory, JSON
  event parsing, unit/result, and output interfaces. It runs serially in this
  milestone; M02 owns concurrency.
- Move known subprocess-helper identities into one canonical tracked input
  consumed by the runner and current CI helper. Prove every entry has a parent
  test and no ordinary test is excluded.
- Implement deterministic package/service inventory, helper exclusion, exact-
  once logical shard union validation, one-shard reproduction, and truthful
  elapsed/target/status summaries.
- Implement the versioned non-sensitive timing-weight parser/formatter and
  atomic cache boundary below an overrideable runner-owned cache directory.
- Do not change Make targets, default verification policy, test fixtures, or
  workflow behavior yet.

Test-first slices:

1. Add fake inventory cases that fail on duplicates, missing service package,
   empty/unknown targets, helper children entering direct scheduling, and union
   gaps; implement deterministic exact-once discovery.
2. Feed success, failure, panic, race, compile, timeout, subtest, example, and
   fuzz JSON events into the parser; preserve target timing and failed output
   without treating human text as exit status.
3. Add corrupt/unknown timing format, atomic-write failure, permissions, and
   secret-shaped output cases; prove safe fallback and non-sensitive storage.
4. Compare one serial runner normal/race inventory with direct `go test -list`
   and existing helper assignment on unchanged source.

Verification:

- `go test ./tools/test-runner -count=1`
- `go test -list '^(Test|Example|Fuzz)' ./internal/service`
- `bash -n scripts/ci-test.sh`
- `bash scripts/ci-helper_test.sh`
- One serial runner normal inventory and one focused serial race inventory.
- `make fmt-check`
- `go vet ./...`
- `git diff --check`

Exit criteria: current local cost is durably measured; the runner discovers and
validates complete inventory serially; helper identities have one tested owner;
timing data is truthful, atomic, and non-sensitive; and no user-facing target
or required gate has changed.

### [x] M01 — Add fast, changed-area, and explicit full local lanes

Specification coverage: [§3 test lanes](../spec/local-test-verification-strategy.md#3-test-lanes), [§4 verification frequency](../spec/local-test-verification-strategy.md#4-verification-frequency), and [§9 compatibility](../spec/local-test-verification-strategy.md#9-compatibility-and-adoption).

Scope:

- Add the shared short-mode integration classifier to `internal/testutil` and
  audit direct expensive setup across service, CLI, Git, discovery, and command
  process-boundary tests.
- Add a bounded representative real-Git/process smoke selection outside short
  mode and prove it covers fixture wiring, CLI composition, and process cleanup.
- Implement changed-file collection including staged, unstaged, and untracked
  paths; explicit base validation; owning-package and reverse-dependency
  selection; testutil consumers; script/workflow mapping; platform files; and
  fail-closed deletes/renames or graph errors.
- Add `check-local`, `test-changed`, `test-changed-race`, `test-full`,
  `test-full-race`, and `check-full` targets with documented inputs/timeouts.
  Keep `test`, `test-race`, and `check` exhaustive compatibility aliases.
- Add deterministic Make command-capture tests or an equivalent repository-
  native harness so target composition and failure propagation do not depend on
  running the expensive suite in every unit case.
- Document lane strength, race-risk triggers, explicit base/package inputs, and
  adoption limits for active plans.

Test-first slices:

1. Prove short mode currently reaches the first external Git/process command,
   then skip at the common setup boundary while normal mode still executes it.
2. Build a synthetic dependency graph and changed-path inventory covering
   production/test/testutil/script/platform/delete/untracked cases; implement
   the exact package closure and actionable fail-closed errors.
3. Capture each Make target command and prove fast/changed/full/race semantics,
   timeouts, variables, and compatibility aliases are distinct and fatal on
   child failure.
4. Run short JSON inventory and prove every package retains meaningful tests;
   run smoke/full comparison and prove no required full target is short.

Verification:

- `go test ./internal/testutil ./tools/test-runner -count=1`
- `make check-local`
- `go test -short -json -count=1 ./...`
- `make test-changed BASE_REF=<recorded-valid-base>` on synthetic and live
  bounded changes.
- `make test-changed-race PACKAGES='./internal/testutil ./tools/test-runner'`
- Make target command-capture harness.
- `TEST_JOBS=1 make test-full`
- `make fmt-check`
- `git diff --check`

Exit criteria: fast and changed-area gates are deterministic and correctly
labeled; short mode starts no classified expensive fixture command while
retaining meaningful package coverage; explicit full compatibility targets
remain exhaustive; and `check-local` meets the two-minute budget on the
baseline host.

### [x] M02 — Run complete local inventories with bounded duration-balanced workers

Specification coverage: [§5 bounded concurrency](../spec/local-test-verification-strategy.md#5-bounded-local-concurrency), [§6 duration balancing](../spec/local-test-verification-strategy.md#6-duration-balanced-assignment), and [§7 race strategy](../spec/local-test-verification-strategy.md#7-race-strategy).

Scope:

- Implement the 1–4 worker scheduler, default 4, including command admission,
  per-unit context/timeout, authoritative drain-all behavior, optional labeled
  fail-fast, deterministic output order, and aggregate status.
- Implement LPT assignment from valid platform/mode weights, median weight for
  new targets, stale-target exclusion, stable tie-breaking, round-robin cold
  fallback, and atomic complete/partial observation updates.
- Schedule non-service work and eight logical service shards through the same
  worker cap. Run normal and race modes separately.
- Implement signal/interruption handling that cancels and waits only for owned
  processes, reports survivors, and cleans only validated runner-owned logs.
- Wire `test-full`, `test-full-race`, and their compatibility aliases to the
  bounded runner. Preserve `-count=1`, full raw failed output, and exact command
  reproduction.
- Compare serial, two-worker, and four-worker inventories, statuses, resource
  use, and durations. Lower the default only if four violates the fixed
  stability/resource bound; never exceed four.

Test-first slices:

1. Use barrier-controlled fake units to prove active commands never exceed the
   cap, all units drain in authoritative mode, fail-fast is incomplete/labeled,
   and one failure makes the aggregate fail.
2. Add weighted assignment cases for equal loads, skew, unknown/stale weights,
   corrupt cache, and deterministic ties; prove exact-once union every time.
3. Spawn controlled child processes, interrupt the runner, and prove bounded
   owned termination, survivor reporting, unrelated-process preservation, and
   exact cleanup.
4. Run actual normal and focused race inventories at workers 1, 2, and 4; prove
   identical targets/results and record contention/resource evidence.

Verification:

- `go test ./tools/test-runner -count=1`
- `go test -race ./tools/test-runner -count=1`
- Five repeated scheduler/process cleanup stress runs.
- `TEST_JOBS=1 make test-full`
- `TEST_JOBS=2 make test-full`
- `TEST_JOBS=4 make test-full`
- Focused `TEST_JOBS=1,2,4` race comparisons before the complete race runs.
- `TEST_JOBS=1 make test-full-race`
- `TEST_JOBS=4 make test-full-race`, unless the documented M02 scope amendment
  accepts the retained coarse-service worker-4 race reference instead.
- Exact serial/parallel target-union and result comparison.
- `make fmt-check`
- `go vet ./...`
- `git diff --check`

Exit criteria: bounded execution never exceeds four owned commands, inventory
and result semantics match serial execution, duration balancing is deterministic
and safe on cache failure, interruption leaks nothing owned, complete normal is
at most eight minutes 30 seconds, and normal plus race is at most 19 minutes without
exceeding the resource/flake bounds.

### [x] M03 — Reduce measured fixture, wait, and duplicate-scenario cost

Specification coverage: [§8 fixture and wait optimization](../spec/local-test-verification-strategy.md#8-fixture-and-wait-optimization) and [§10 acceptance](../spec/local-test-verification-strategy.md#10-required-acceptance-evidence).

Scope:

- Profile runner JSON/timing evidence and select the smallest fixture, wait,
  CLI/service duplication, or safe pure-test parallelism changes that dominate
  remaining local wall time. Record selection criteria before editing.
- Remove the two persistent Git identity configuration commands from ordinary
  repository creation while preserving hostile host config, credentials,
  hooks, maintenance, locale, and identity isolation.
- Replace only measured non-contract sleeps with observable synchronization;
  retain bounded failure deadlines and all success/cancel/timeout/cleanup/leak
  assertions.
- Consolidate measured duplicate scenario matrices according to owning-layer
  responsibility. Record every old test/row and retained assertion mapping
  before deletion.
- Reuse immutable setup only with isolated mutable namespaces and deterministic
  Windows cleanup. Add selective `t.Parallel()` only for measured pure or fully
  isolated cases and retain a maximum `-parallel=4`.
- Reprofile serial and four-worker normal/race execution after each accepted
  category so regressions are attributable.

Test-first slices:

1. Instrument fixture commands and fail on three-process repository creation;
   reduce it to one initialization process and prove hermetic commits.
2. For each wait hotspot, prove readiness/cleanup via an observable event and
   preserve the original failure boundary without fixed success delay.
3. For each duplicate, establish the retained owning-layer assertion first,
   complete the coverage map, then remove the consumer duplicate and run both
   layers.
4. For any selective in-process parallel case, run repeated race/isolation/leak
   stress before accepting its timing improvement.

Verification:

- `go test ./internal/testutil -count=1`
- Focused owning/consumer tests named by the coverage map.
- Focused normal/race tests for every synchronization or parallelism change.
- Five repeated affected process/isolation stress runs.
- `TEST_JOBS=1 make test-full`
- `TEST_JOBS=4 make test-full`
- `TEST_JOBS=1 make test-full-race`
- `TEST_JOBS=4 make test-full-race`
- `make fmt-check`
- `go vet ./...`
- `git diff --check`

Exit criteria: canonical Git fixture creation uses one initialization process
without losing hermetic behavior; every removed wait/test has stronger
observable or mapped evidence; no mutable state is shared; serial intrinsic
service-plus-CLI evidence records each retained measured improvement and rejects
insufficient candidates; no further percentage reduction is required after the
user-accepted M02 status quo, and the amended M02 local budgets remain green.

### [x] M04 — Adopt tiered verification and prove final local portability

Specification coverage: [§§4 and 9–10](../spec/local-test-verification-strategy.md#4-verification-frequency).

Scope:

- Freeze the candidate and audit the complete inventory, target aliases,
  changed-area closure, short/full boundary, helper list, timing cache,
  scheduler, coverage map, performance evidence, docs, and owned artifacts.
- Update contributor-facing documentation and
  `docs/ai/plan-authoring.md` so new plans use focused iteration,
  complete-submission local/changed/focused-race gates, one frozen full normal
  result, and terminal complete race/tutorial/release evidence. Do not weaken
  milestone supervision, reviewer independence, remediation limits, ledger
  invariants, or existing source plans.
- Document how an existing active/blocked plan may adopt the strategy only
  through explicit user-authorized scope amendment in its own plan/ledger.
- Run final serial/four-worker normal comparisons on one frozen source and
  retain terminal race evidence. The documented M02 scope amendment permits
  its retained coarse-service worker-4 race reference in place of a
  same-topology final-source rerun; do not repeat identical commands through
  alias targets merely for naming symmetry.
- Verify affected Windows-specific packages compile. Record native hosted
  runner/helper/Make evidence as pending in the separate Windows/CI work; do
  not run or remediate native Windows tests in this local plan.
- Update context measurements, reciprocal lifecycle links, status overview,
  plan checkbox/log evidence, and lifecycle statuses. This plan and its source
  specification become `implemented` only after their full approved local scope
  and compile-only Windows tooling evidence are approved and verified; native
  Windows execution is explicitly not a completion gate here.

Test-first slices:

1. Audit a synthetic new plan against old and tiered guidance; prove the new
   template does not require repeated full suites during partial work and still
   requires terminal complete evidence.
2. Exercise every Make lane and failure path, verifying its label, timeout,
   cache state, inventory strength, and reproduction command.
3. Run final deterministic inventory/coverage/cache/artifact audits and fail on
   any duplicate, omission, unowned artifact, sensitive cache field, or stale
   documentation.
4. Reconcile the final same-host performance comparison and document that
   native-hosted Windows evidence remains in the separate Windows/CI work; do
   not import it as a local gate.

Verification:

- `go test ./tools/test-runner -count=1`
- `go test -race ./tools/test-runner -count=1`
- `bash scripts/ci-helper_test.sh`
- `make fmt-check`
- `go vet ./...`
- `make check-local`
- Recorded changed-area normal and focused race commands.
- Retained `TEST_JOBS=1` and `TEST_JOBS=4` complete normal/race comparisons.
- `make build`
- `make release-test`
- `make tutorial-test`
- `make check-full` only when it adds evidence not already retained, otherwise
  a command-expansion proof plus references to identical terminal commands.
- `git diff --check`
- Isolated Windows cross-compilation for affected packages; native execution
  remains outside this plan.

Exit criteria: all lanes are accurately documented and reproducible; new plan
guidance adopts tiered evidence without changing supervision semantics or
historical runs; complete serial/parallel inventories match; `check-local`,
changed-area, normal, and normal-plus-race budgets pass; no flake/leak/artifact
or sensitive timing data remains; affected Windows packages cross-compile while
native evidence remains explicitly separate; lifecycle metadata agrees; and
independent review reports no unresolved material finding.

## Execution log

Append entries only after a milestone is independently approved and verified;
do not rewrite earlier evidence. Active packets, findings, attempts, benchmark
state, and resume instructions belong in
`docs/ai/runs/local-test-acceleration.md`, created only after execution is
explicitly authorized.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-09-02 | M00 | Runner tests/vet; direct `689`/`15`/`674` service inventory; identical 674-target normal/race eight-shard unions; Windows and unsupported-platform test-binary cross-compilation; CI helper, formatting, full vet, and diff checks passed. POSIX private atomic timing persistence and Windows/unsupported cache-free fallback are covered. The earlier expectation that M04 would require native Windows behavior is superseded by the explicit 2026-09-03 local-plan amendment; this row never claims native acceptance. | Approved after two rejected remediation submissions and a third submission from the Escalation Implementer resolved `R1`; no production-code or unrelated-path changes. | Not committed (per user instruction) |
| 2026-09-02 | M01 | Runner/testutil tests, `check-local` in 61.22s, all-package short JSON in 21.0s with 16 meaningful packages and no failure events, explicit ambient-short smoke, focused changed race, Make/docs/shell harnesses, disposable live changed execution in 15.0s, formatting, vet, and diff checks passed. The retained serial exhaustive normal run covered all 674 service targets in about 22m. | Approved after two rejected remediation submissions and a third submission from the Escalation Implementer resolved the repository-wide Markdown selector finding; all `R1`–`R6` are resolved and no production-code or unrelated-path change was attributed. | Not committed (per user instruction) |
| 2026-09-03 | M02 | Runner/testutil normal, runner race, five scheduler/process stress repetitions, docs, formatting, scoped vet, and diff checks passed. Bounded workers retain exact 183 CLI and 674 service targets, eight logical service reproduction identities, up to 16 physical parts, cap four, deterministic cache/failure/drain/cancellation semantics, and no owned survivor. Retained complete worker-4 normal is 8m29.01s; the user-authorized different-topology 620.72s race policy reference gives 18m49.73s combined under the amended budgets. | Approved after two rejected docs/ledger remediations and a third escalation-ledger remediation resolved `M02-R1`–`M02-R3`; no production-code, Windows-behavior, workflow, or unrelated change was attributed. | Not committed (per user instruction) |
| 2026-09-03 | M03 | The ordinary repository fixture retains one `git init` process and hermetic author/committer identity; exactly 21 isolated creator rows retain the hard-four gate. Focused normal/race improved from 75.92/95.78s to 32.36/40.70s with five retained repeats and no survivor. Fresh testutil, runner normal/race, focused 21-row normal/race, five repetitions, formatting, scoped vet, docs, and diff checks passed. Rejected fixture/wait/duplicate/scheduling experiments are documented and absent. | Independently approved with no material finding; no M03 production, Windows, workflow, or unrelated change was attributed. | Not committed (per user instruction) |
| 2026-09-03 | M04 | Frozen 183 CLI/674 service/15-helper inventories, lane/topology/cache contracts, tiered plan-authoring and README contributor guidance, and the narrow different-topology race waiver are documented. Runner/testutil normal, runner race, helper/local-target harnesses, formatting, full vet, `check-local` in 54s, build, release, tutorial in 99s, check-full expansion, docs, diff, Windows-amd64 test-tool cross-compilation, and terminal artifact/process audits passed. | Approved after one docs-only remediation resolved stale native-Windows gate language and missing contributor lane guidance; no production, Windows-behavior/test, workflow, native-platform, or unrelated change was attributed. | Not committed (per user instruction) |
