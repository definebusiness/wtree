# Implementation context — local test acceleration

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Local test acceleration implementation plan](local-test-acceleration.md)
Source specification: [Local test verification strategy specification](../spec/local-test-verification-strategy.md)
Related CI plan: [Test-suite runtime optimization implementation plan](test-suite-runtime-optimization.md)
Captured: 2026-09-02
Investigated branch: `feat/lifecycle-hooks`
Investigated base commit: `633d5f1d1e5d57e644b127edfd48d78c45d97711`

## 1. Purpose and precedence

This document preserves the local-runtime diagnosis and the user-provided
strategy behind the plan. It is a snapshot, not authorization to reinterpret
an active plan's verification commands. The source specification owns the
future local verification contract; an already-authorized source plan and its
durable ledger remain authoritative until the user explicitly amends scope.

The companion [CI-oriented context](test-suite-runtime-optimization-context.md)
contains the broader static inventory, hosted Windows timings, fixture counts,
and cross-platform constraints. This file extracts the parts that materially
affect local feedback and adds the local execution design.

## 2. User-provided conclusions

The user distinguished recommendations that improve hosted CI from those that
also improve a local run:

| Change | CI speed | Local speed |
|---|---|---|
| Parallel GitHub matrix shards | Large | None by itself |
| Linux-only exhaustive race | Large | Only if local policy also changes |
| Fast versus exhaustive tiers | Large | Large |
| Duration-balanced shards | Large when parallel | Little when serial |
| Consolidated Git fixtures | Medium/large | Medium/large |
| Controlled test parallelism | Large | Potentially large |

The central conclusion is that hosted matrix fan-out alone does not accelerate
local `make check`. Local acceleration requires a separate verification
contract, bounded local workers, and less repeated complete-suite evidence.

The user proposed this practical evidence schedule:

1. a fast local/PR gate with unit tests, changed-area integration, and focused
   race tests;
2. full normal integration once before final review;
3. full race primarily through authoritative Linux/nightly evidence, retaining
   focused platform-specific race coverage;
4. independently runnable duration-balanced shards with local concurrency
   bounded between two and four workers; and
5. gradual consolidation of repeated Git fixture setup.

The local specification narrows item 3 to local policy only. It does not remove
hosted race coverage owned by the separate CI specification and plan.

## 3. Current Makefile cost

The current `Makefile` defines:

```make
test:
	go test -timeout=$(TEST_NORMAL_TIMEOUT) ./...

test-race:
	go test -race -timeout=$(TEST_RACE_TIMEOUT) ./...

check: fmt-check vet test test-race build tutorial-test
```

Consequences:

- `make check` runs complete normal and complete race sequentially;
- tutorial acceptance then invokes another focused set of service/CLI/config/
  store tests;
- a plan that separately requires full normal, full race, and `make check`
  may execute equivalent expensive inventories multiple times on unchanged
  source; and
- every remediation or post-review verification packet can pay the same cost
  again even when only a narrow test changed.

The compatibility name `make check` appears in many existing source plans and
run ledgers. Silently changing it to a fast subset would invalidate historical
and active evidence. The safe migration is to add explicit `check-local` and
`check-full` contracts, retain `check` as the complete alias, make its complete
internals faster through bounded sharding, and author new plans against the
tiered schedule.

## 4. Serial service bottleneck

At capture time the repository contains 1,189 top-level test/fuzz/example
functions. `internal/service` owns 710 and `internal/cli` owns 184. Only five
test sites in the repository call `t.Parallel()`, four in
`update_publication_test.go` and one in `remove_forest_internal_test.go`.

Go runs packages concurrently, but top-level tests inside a package remain
serial unless they explicitly opt into parallel execution. The dominant
service package therefore limits local wall-clock progress even when other
packages finish early.

Blanket `t.Parallel()` is unsafe here because service tests frequently mutate
environment variables, create real repositories, spawn child processes, bind
listeners, exercise shared registry/lock rules, and assert cleanup timing.
Separate `go test` processes for disjoint top-level target sets provide a safer
isolation boundary than immediately parallelizing hundreds of tests inside one
process.

## 5. Existing shards do not accelerate local runs

`scripts/ci-test.sh` discovers service targets, builds eight disjoint patterns,
and then executes:

```bash
for pattern in "${shard_patterns[@]}"; do
  go test ... -run "$pattern" ./internal/service
done
```

The loop begins near line 285 in the captured file. It improves timeout
boundaries and diagnostics but remains serial. Local normal and race shard
durations therefore add together.

The recent implementation-host evidence summarized in the CI context recorded
roughly `15m52s` for eight normal service shards and `17m22s` for eight race
service shards, or about 33 minutes combined. Four perfectly balanced workers
have an `8.25m` theoretical floor. Allowing for real Git, filesystem, CPU, and
process contention, a 10–15 minute combined result is a credible target.

Eight concurrent laptop workers are not the target. They would likely amplify
I/O contention, process pressure, antivirus/filesystem scanning on Windows,
and timing flakes. The proposed default is four with an accepted range of one
to four.

## 6. Duration imbalance

Round-robin target assignment uses inventory position, not cost. In one recent
normal run, service shard durations ranged from roughly 76 to 163 seconds; race
shards ranged from roughly 81 to 187 seconds. Sequential execution does not
care about imbalance because every duration is summed. Concurrent execution is
limited by its slowest worker, so duration-aware assignment becomes valuable.

The intended algorithm is deterministic longest-processing-time-first:

1. sort targets by descending recorded duration and stable target name;
2. place each target into the currently lightest logical shard;
3. break equal-load ties by shard index;
4. assign unknown targets the current median weight;
5. ignore stale weights; and
6. validate exact-once inventory after assignment.

Timing weights are disposable local cache data, separated by platform and race
mode. A cold first run falls back to sorted round-robin and creates evidence
for the next run. Cache failure must affect balance only, never coverage.

## 7. Real-Git fixture density

The static capture found:

- 331 call sites that create a new shared Git, pushed Git, or bare-remote
  fixture;
- 427 total shared Git-fixture references; and
- 374 `testutil.RunCommand` call sites.

Before the accepted M03 fixture change, `internal/testutil.NewGitRepository`
executed three Git processes:

1. `git init --initial-branch=main`;
2. `git config user.name ...`; and
3. `git config user.email ...`.

A pushed fixture adds bare initialization and remote configuration before any
test-specific add, commit, push, fetch, clone, status, or worktree commands.
Table rows and subtests multiply these static sites dynamically.

The accepted M03 fixture change removes the two persistent local identity
configuration commands. Ordinary repository construction now starts only
`git init --initial-branch=main`; commit operations receive the deterministic
author and committer identity through the hermetic fixture environment. That
environment also ignores global and system configuration, disables prompts and
askpass, excludes system attributes, retains the hostile-hook boundary, and
fixes locale. `TestNewGitRepositoryUsesEnvironmentIdentityWithoutLocalConfig`
proves no local `user.*` entries are written and verifies deterministic commit
metadata. `TestGitCommandIgnoresHostileGlobalConfigAndHookPath` proves the
command boundary ignores hostile global config and hook paths. The short-mode
boundary remains before the first Git process.

Duplicate service/CLI scenario matrices and unnecessary fixed waits remain
secondary candidates only after an old-to-retained coverage map proves no
behavioral loss.

## 8. Deliberate waits need classification

The captured service/CLI tests contain multi-second sleeps, including a direct
five-second aggregate wait and multiple process helpers that sleep for 10 or
30 seconds. Many long helper sleeps are designed to be canceled and do not
normally contribute their literal duration. Static search alone cannot decide
which wait is expensive.

Profiling must classify each hotspot as one of:

- contract duration: elapsed time is the behavior and remains;
- failure guard: a deadline remains but success uses an observable event;
- helper lifetime: parent cancellation should end it early;
- polling delay: reduce or replace with a readiness signal; or
- accidental serialization: remove through isolated process-level workers.

Replacing a sleep is acceptable only when the original success, timeout,
cancellation, cleanup, and leak assertions remain observable.

## 9. Local verification tiers

The target local workflow is:

```text
edit loop
  └── focused owning test → focused race when risk requires it

bounded feedback
  ├── check-local: format + vet + short + smoke + build
  └── test-changed: changed package + reverse dependents

frozen candidate
  ├── test-full: complete normal through 1–4 workers
  ├── test-full-race: complete race through 1–4 workers
  └── check-full/check compatibility: complete release evidence
```

The short lane must be meaningful, not a package-level exclusion list. Real-
Git fixture constructors can skip before their first external command, while
pure tests in `internal/service`, `internal/cli`, and `internal/git` remain
eligible to run.

Changed-area selection requires an explicit base revision. Reverse dependency
closure prevents a low-level package change from testing only itself. Race
sufficiency remains a risk decision because filenames cannot reliably identify
concurrency impact.

## 10. Milestone-process impact

The repository's supervision process requires exact verification named by each
source plan. It does not itself require that every partial implementation run
full normal, full race, tutorial, release, and aggregate checks repeatedly.
Plan authors choose those commands.

After implementation, new plans should use:

- focused tests during implementation;
- `check-local`, changed normal, and focused race for a complete submission;
- one full normal result on the approved frozen milestone candidate; and
- complete race/tutorial/release evidence at the terminal boundary or when the
  risk and source specification require it earlier.

Retained results may be referenced when an unchanged source tree would execute
the identical command again. A source change invalidates affected evidence;
documentation-only changes do not automatically invalidate an unrelated
binary test result, but formatting, links, and documentation gates still run.

The discussion that prompted this plan referred to a terminally blocked run.
The repository status snapshot now records active/resumed lifecycle-hook work,
so the executing agent must read its current durable ledger rather than rely on
that historical description. In either state, this new plan cannot alter that
run without explicit user authorization.

## 11. Useful baseline commands

```bash
go test -count=1 -json ./internal/service
go test -race -count=1 -json ./internal/service
go test -count=1 -json ./internal/cli
go test -list '^(Test|Example|Fuzz)' ./internal/service
rg -n --glob '*_test.go' '\.Parallel\(\)' internal cmd
rg -n --glob '*_test.go' 'time\.Sleep\(' internal cmd
rg -o --glob '*_test.go' 'testutil\.New(PushedGitRepository|GitRepository|BareGitRemote)' internal cmd | wc -l
/usr/bin/time -p make check
```

Run benchmarks with no competing repository test process. Use an isolated or
known-warm build cache consistently, pass `-count=1`, retain JSON output, and
record platform, Go version, worker count, and source revision.

## 12. M00 reconciled runner baseline (2026-09-02)

M00 observed the current working tree at revision
`633d5f1d1e5d57e644b127edfd48d78c45d97711` on `darwin/arm64` with Go
`go1.26.5`. A process audit immediately before the runner measurements found
no matching `go test`, CI-helper, `make check`, or `test-runner` process. That
is a point-in-time quiescence observation only: unrelated active edits remain
in the worktree and were not touched.

The current static inventory is 1,189 top-level `Test`, `Example`, or `Fuzz`
functions, 34 `time.Sleep` sites, five `t.Parallel` sites, 331 Git-fixture
constructor call sites, and 374 `testutil.RunCommand` call sites. Direct
`go test -list '^(Test|Example|Fuzz)' ./internal/service` reported 689 service
targets. The canonical `tools/test-runner/service-subprocess-helpers.tsv`
contains 15 exact helper/ordinary-parent pairs, leaving 674 schedulable service
targets. The serial runner's eight sorted round-robin logical shards validate
that those 674 targets appear exactly once; shard 1 contains 85 targets.

Commands and results, using an isolated disposable cache at
`/private/tmp/wtree-m00-go-cache` and `-count=1` for execution, were:

```text
go test -list '^(Test|Example|Fuzz)' ./internal/service
# 689 listed targets

go run ./tools/test-runner inventory --mode normal --shard 1
# targetCount=674, helperCount=15, one exact reproduction command emitted

go run ./tools/test-runner run --mode normal --shard 1
# passed, 85 targets, 2m15.656s serial wall time

go run ./tools/test-runner run --mode race --shard 1
# passed, 85 targets, 2m53.206s serial wall time

TEST_RUNNER_CACHE_DIR=/private/tmp/wtree-m00-runner-cache \
  go run ./tools/test-runner inventory --mode normal --shard 1
# cold cache; targetCount=674, assigned=674, unique=674

TEST_RUNNER_CACHE_DIR=/private/tmp/wtree-m00-runner-cache \
  go run ./tools/test-runner run --mode normal --shard 1
# passed, 85 targets, 2m26.658s serial wall time; atomically wrote 85 observations

TEST_RUNNER_CACHE_DIR=/private/tmp/wtree-m00-runner-cache \
  go run ./tools/test-runner inventory --mode normal --shard 1
# loaded cache; targetCount=674, assigned=674, unique=674
```

The normal/race pair is a focused, reproducible serial sample, not a new
whole-package benchmark. The current command executor terminates a parent
process at roughly 30 seconds; an attempted direct full service command left
empty output/time files and no surviving owned child. It therefore cannot
truthfully supply a new full serial package timing in this run. The historical
uncontended eight-shard aggregate remains approximately 15m52s normal and
17m22s race, as recorded in the CI context. M02 must collect the final
same-host serial/parallel comparison using an execution environment that can
retain the complete process tree.

The runner's timing cache is now a live lifecycle, not only a formatter. On
Darwin/POSIX it requires an absolute overrideable cache root, creates and
validates a private `0700` runner-owned subtree beneath it, and uses a
versioned `GOOS`, `GOARCH`, and mode path. The observed Darwin `weights.tsv`
is `0600`; its live normal write had 86 lines (one header and 85 target
observations) and reopening reported the `loaded` state without changing the
674-target union. POSIX writes synchronize the private directory before and
after same-directory replacement; no cache contents were emitted in runner
output or retained in this document.

The format contains only target name, duration, sample count, and RFC3339
observation timestamp. Target names that describe secret-handling behavior are
valid Go identifiers; value- or path-shaped input such as
`TestToken=secret` is rejected. Missing, corrupt, unsupported,
secret-value-shaped, unreadable, unsafe-root/target, and write failures produce
a non-sensitive `timing cache fallback` diagnostic and retain the cold sorted
round-robin inventory and process-status semantics. M00's deterministic POSIX
tests exercise those fallbacks, same-directory replacement, and private modes.

Windows has a deliberately different, safe contract: cache persistence is
unavailable and the runner reports the same non-sensitive fallback without
creating or trusting a timing target. Windows Go reports directories with
POSIX-incompatible writable mode bits, and the authoritative `go doc os.Rename`
contract states that even same-directory replacement is not atomic on non-Unix
platforms. The runner therefore makes no Windows privacy, sync, durability, or
atomic-replacement claim. A Windows `amd64` test-binary cross-compile selected
the fallback implementation, where the build-tagged persistence capability is
false, and compiled the shared tests that branch on that capability. The
cache-free branches require the unavailable diagnostic, preserve a successful
serial inventory/status path with a nil cache, and assert that no runner-owned
cache destination is created. This is compile-time evidence only;
an earlier M04 native-hosted expectation is superseded by the 2026-09-03 user
amendment. This local plan records cross-compilation only and makes no native
Windows acceptance claim; native evidence remains with the separate Windows/CI
work. The only retained cache artifact is beneath the isolated Darwin
`/private/tmp` override; the repository contains no machine timing weights.

## 13. M01 local lane contract

M01 adds named local lanes without narrowing existing compatibility names:

- `make check-local` is the bounded fast gate: formatting, vet, all-package
  short tests, a real-Git/CLI/process smoke selection, runner tests, target
  contract capture, and a build. It is feedback evidence, not release or full
  race evidence.
- `make test-changed BASE_REF=<commit>` requires a commit-valued base and
  collects committed, staged, unstaged, and untracked paths. It selects a Go
  owner and its in-repository reverse dependency closure. It fails closed for
  absent/invalid bases, deleted or renamed paths, module boundaries, unreadable
  graphs, and ambiguous ownership. A shared `internal/testutil` change also
  selects every test-package consumer. Make, runner, helper, and workflow
  changes select the deterministic runner harness; platform-specific paths
  are surfaced in the selection for later native/cross-platform verification.
- `make test-changed-race PACKAGES='...'` is deliberately explicit. Use it
  when a change touches cancellation, processes, locks, shared state,
  filesystem mutation, atomic publication, rollback, recovery, or another
  race-sensitive boundary. File names do not establish race sufficiency.
- `make test-full`, `make test-full-race`, and `make check-full` remain
  exhaustive serial lanes in M01. `test`, `test-race`, and `check` are their
  complete compatibility aliases. M02 alone may turn `TEST_JOBS` into bounded
  concurrency; M01 reports the requested value but keeps one process at a
  time.

The shared `testutil.RequireIntegration` classifier sits before the first
shared real-Git fixture command. In `-short` it skips with a capability-named
reason before a fixture starts; outside short mode the same fixture executes.
This preserves pure, parser, planner, fake-adapter, rendering, and
serialization coverage inside each package while reserving a bounded smoke
selection for real fixture wiring, CLI composition, and process cleanup.

The deterministic `scripts/local-test-targets_test.sh` harness captures Make
recipes with `make -n`. It verifies lane composition, mode labels, timeouts,
inputs, alias distinction, and that no recipe masks child failures, without
repeating integration suites. These named lanes apply to new work after plan
completion only; active plans keep their recorded verification contract until
their own user-authorized scope change.

### M01 expensive-boundary audit

The short classifier has two deliberately test-only entry points. Shared
`NewGitRepository` and `NewBareGitRemote` classify every consumer of the
hermetic fixture before its first Git command. `GitCommand` is the equivalent
boundary for discovery tests that intentionally construct nested repositories
with direct `git init` calls. The service direct-process parent tests classify
their process-heavy child setup before creating a child test binary. Their
helper entry points remain excluded from the full runner's direct schedule and
are reached only by their ordinary parent tests.

The audit covers service real-Git and process boundaries, CLI real-fixture
composition through the shared fixture, `internal/git` fixture consumers, and
discovery's formerly direct Git setup. `go test -short -json ./...` records
capability-named skips at those boundaries while retaining non-helper passing
tests in every package. Normal-mode smoke retains one shared real-Git fixture,
one CLI composition test, and two service process-cleanup tests, so short
classification is never treated as a replacement for integration evidence.

## 14. M01 accepted evidence snapshot

M01 evidence was collected on `darwin/arm64` with Go `go1.26.5` at source
revision `633d5f1d1e5d57e644b127edfd48d78c45d97711`, using isolated runner
and build caches below `/private/tmp` and a quiescent owned-process boundary.

`go test -short -json -count=1 ./...` completed in approximately 22 seconds:
all 16 packages emitted a meaningful non-helper passing target, 748 classified
fixture/process skips named their capability, and no JSON failure event was
observed. Normal smoke passed in approximately 0.36s for real Git, 14.27s for
CLI composition, and 0.94s for process cleanup. Warm isolated `make
check-local` exited zero in 39 seconds.

A disposable uncommitted clone below `/private/tmp` received only the current
Makefile and runner tree. `make test-changed BASE_REF=HEAD` selected bounded
runner/tool paths and completed in under three seconds; the clone was removed.
The broad user worktree was not used as changed-area evidence. Focused race
passed in approximately 2.47s and 1.83s for testutil and runner.

The retained serial `TEST_JOBS=1 make test-full` result took approximately
22 minutes and wrote 675 cache lines: one header plus 674 service targets.
Later `-short=false` capture and focused ambient `GOFLAGS=-short` smoke prove
the default full mode remains non-short without repeating that exhaustive run.
The former M04 native-Windows expectation is superseded by the 2026-09-03 user
amendment: M04 records compile-only tooling evidence and no native acceptance;
the separate Windows/CI work owns native execution and remediation.

Changed-area execution derives a deterministic runner plan from selection
metadata: documentation validation, named harness, deduplicated native package
tests, and foreign-platform compile-only actions. Foreign actions use `go test
-c` in a private temporary directory and are compile evidence, never a claim
of native foreign execution. Unsupported platform markers fail before action
execution with an identifier-only diagnostic.

## 15. M02 bounded-runner contract

M02 changes only the test runner and local test-target composition. `test-full`
and `test-full-race` now pass `TEST_JOBS` to the runner; the default is four
and the accepted range is one through four. `test` and `test-race` retain
their exhaustive compatibility meaning. Normal and race are separate runner
invocations and are never combined by the scheduler.

Canonical discovery fixes the ordinary package inventory, the 183 top-level
CLI targets, and the 674 schedulable service targets before planning either
mode. Worker 1 runs the CLI inventory as one anchored command. Workers 2, 3,
and 4 split that same sorted CLI inventory into two deterministic anchored
commands. Workers 1 and 2 run one command for each of the eight logical service
shards. Workers 3 and 4 retain those eight logical identities but split each
non-singleton logical shard into at most two physical parts, for at most 16
service commands. Physical parts are the executable scheduling units only:
`--shard N` still reproduces the complete logical shard, and complete cache and
result semantics aggregate the exact logical target union rather than treating
a part as a new logical target.

A shared bounded scheduler admits at most `TEST_JOBS` (1–4) runner-owned
commands across CLI, ordinary packages, and service units. The deterministic
unit order preserves canonical non-service package order. If that non-service
list exceeds the worker cap, its first `TEST_JOBS` units are admitted before
the service units and its remaining units follow; otherwise all non-service
units precede service units. Within the service suffix, physical parts remain
in logical-shard/part order. Each part is formed by the valid per-target LPT
weights (or the deterministic round-robin fallback); no global reordering of
physical parts is used. Normal and race have separate plans and are never
scheduled together. Authoritative mode drains every admitted inventory unit
after a failure, collects an aggregate nonzero status, and emits summaries and
full failed raw output in unit order rather than completion order. `--fail-fast`
is available only for iteration; it stops further admission after failure and
labels its result incomplete, so it cannot be used as complete-inventory
evidence.

The runner uses a valid same-platform, same-mode complete timing cache for
deterministic longest-processing-time-first assignment over the eight service
shards. Unknown targets receive the median valid weight; stale observations
are ignored; equal loads select the lower shard index. A cold, corrupt,
unreadable, unsupported, or wholly stale cache falls back to sorted
round-robin while preserving the exact-once target union. Successful complete
observations atomically replace only complete weights. Failed or cancelled
target observations, when available, are atomically written to a separate
`.partial` sidecar and never replace a complete weight.

On POSIX, each runner-owned command starts in a private process group.
Interrupt handling cancels that group, waits for the command boundary, and
reports the completed owned-process drain. It never signals a process outside
the group. The runner creates no broad log directory; its only persistent
artifact is the already private timing-cache subtree. Windows and Plan 9 retain
the safe command-termination/cache-free fallback and require native hosted
process-group evidence before any stronger portability claim.

## 16. M02 local measurement snapshot

On the same darwin/arm64 host, with `-count=1`, shared retained build cache,
private runner timing roots, and no concurrent M02 workload, complete normal
inventories passed with the exact 674-target union at workers 1/2/4. Recorded
runner/external walls were 23m20.516s/1403.82s (cold round-robin, worker 1),
11m40.132s/703.43s (LPT, worker 2), and 7m57.426s/480.22s (LPT, worker 4).
The required adaptive worker-4 confirmation was slower at
8m18.033s/500.17s, so the strict <=8 minute complete-command budget is not
met despite the earlier internal runner measurement. Full race passed at
23m53.844s/1436.24s (worker 1) and 10m19.343s/620.72s (worker 4), each with
the same exact 674-target union. Thus normal plus race also exceeds the
15-minute budget on this host. Peak measured runner RSS was approximately
226--617 MB; no scheduler stress flake or owned survivor was observed.

The later worker-4 physical-batch confirmation passed the exact 183-target CLI
and 674-target service inventories in 8m29.01s external with peak runner RSS of
225,837,056 bytes and no owned survivor. On 2026-09-03 the user explicitly
accepted that result as the local performance status quo, ended further tuning,
and waived a same-topology/final-source complete-race rerun for this local plan.
The amended acceptance budgets are <=8m30s complete normal and <=19m complete
normal plus race. The retained 620.72s external worker-4 race used the earlier
coarse-service topology, whereas the 509.01s normal used physical service
batches. Their 18m49.73s sum is therefore an accepted policy reference rather
than a demonstrated same-configuration bound. The waiver does not relax the
exact inventory/result, focused-race, owned-command-cap, failure, or
cancellation contracts, and native Windows execution and remediation remain
outside this local plan and with the separate Windows/CI work.

## 17. M03 accepted fixture and creator-isolation evidence

This section consolidates the accepted test-only M03 slices. It records their
evidence and rejected alternatives; it does not authorize further performance
tuning beyond the user-accepted M02 status quo.

### Fixture identity command map

| Construction stage | Before M03 | Accepted behavior and retained proof |
|---|---|---|
| Ordinary `NewGitRepository` | `git init --initial-branch=main`, then persistent local `git config user.name` and `git config user.email` | Only `git init --initial-branch=main` remains. `runGit`, `runGitPanic`, and `GitCommand` use the hermetic environment, including deterministic author/committer name and email. |
| Commit identity | Local repository config supplied identity | `TestNewGitRepositoryUsesEnvironmentIdentityWithoutLocalConfig` commits successfully, proves no local `user.*` entry, and checks `wtree test <wtree@example.invalid>` for both author and committer. |
| Host boundary | Host configuration, hooks, credentials, maintenance, prompt, and locale must not affect fixtures | `gitFixtureEnvironment` fixes `PATH`, Windows command variables, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`, prompt/askpass, system attributes, author/committer identity, and `LC_ALL`/`LANG`. `TestGitCommandIgnoresHostileGlobalConfigAndHookPath` characterizes hostile global-config and hook-path exclusion. `RequireIntegration` still runs before the first Git process, so short mode starts none. |

### Creator-row coverage and isolation map

All 21 accepted rows call `parallelM07RealGitTest(t)` as the first statement
inside their `t.Run` closure. The helper calls `t.Parallel()` and takes one of
four package-wide real-Git slots; the runner also passes `-parallel=4`. Every
row creates its own `t.TempDir` sources, data directory, target, branch name,
recovery record, and lock namespace through `forestWorkspaceProject` or
`rootWorkspaceProject`. The audit found no shared process-global working
directory, environment, listener/port, `PATH`, hook process, or mutable
filesystem root. Shared code is immutable test code and the hard-four channel
only; Git directories, remotes, worktrees, registry/state files, and cleanup
paths are row-private.

| Matrix and row | Retained injection/variant | Retained assertions and cleanup contract |
|---|---|---|
| Forest rollback: `branch-base` | `CreateBranch` failure at `api` | Exact injected failure and `assertForestWorkspaceAbsent`: no logical root or workspace state. |
| Forest rollback: `branch-sibling` | `CreateBranch` failure at `web` | Same absence/rollback contract at the sibling branch position. |
| Forest rollback: `branch-deep` | `CreateBranch` failure at `gamma` | Same absence/rollback contract at the deep branch position. |
| Forest rollback: `add-base` | `AddWorktree` failure at `api` | Exact add-worktree failure and no target/state residue. |
| Forest rollback: `add-sibling` | `AddWorktree` failure at `web` | Same contract at the sibling add-worktree position. |
| Forest rollback: `add-deep` | `AddWorktree` failure at `gamma` | Same contract at the deep add-worktree position. |
| Forest rollback: `ignore-first` | Ignore mutation failure on call 1 | Exact early ignore failure and no target/state residue. |
| Forest rollback: `ignore-middle` | Ignore mutation failure on call 2 | Exact middle ignore failure and no target/state residue. |
| Forest rollback: `ignore-last` | Ignore mutation failure on call 3 | Exact late ignore failure and no target/state residue. |
| Linked replacement: `create-root-receipt` | Create, root, receipt boundary | Preserves replacement Git-file bytes, Git directory, HEAD, branch/worktree registration, absent state, and exact rollback recovery. |
| Linked replacement: `checkout-root-receipt` | Checkout, root, receipt boundary | Same preservation/recovery contract for checkout. |
| Linked replacement: `create-forest-receipt` | Create, forest, receipt boundary | Same preservation plus forest grouping recovery steps. |
| Linked replacement: `checkout-forest-receipt` | Checkout, forest, receipt boundary | Same forest checkout preservation/recovery contract. |
| Linked replacement: `create-root-return-boundary` | Create, root, post-add return boundary | Preserves replacement and exact root rollback steps. |
| Linked replacement: `checkout-root-return-boundary` | Checkout, root, post-add return boundary | Preserves replacement and exact checkout rollback steps. |
| Linked replacement: `create-forest-return-boundary` | Create, forest, post-add return boundary | Preserves replacement and required forest grouping rollback steps. |
| Linked replacement: `checkout-forest-return-boundary` | Checkout, forest, post-add return boundary | Preserves replacement and forest checkout rollback steps. |
| Linked replacement: `create-root-completed` | Create, root, post-completion state failure | Requires rollback-incomplete recovery while preserving both clean linked worktrees. |
| Linked replacement: `checkout-root-completed-detached` | Checkout, root, detached replacement, post-completion failure | Also preserves detached status while retaining rollback recovery. |
| Linked replacement: `create-forest-completed` | Create, forest, post-completion state failure | Retains both worktrees and the exact forest recovery sequence. |
| Linked replacement: `checkout-forest-completed` | Checkout, forest, post-completion state failure | Retains both worktrees and exact forest checkout recovery. |

The retained normal focused comparison improved from 75.92s at `-parallel=1`
to 32.36s at `-parallel=4`; retained focused race improved from 95.78s to
40.70s. Retained `time -l` peak RSS was 646,152,192/218,333,184 bytes for the
normal serial/parallel pair and 616,513,536/260,636,672 bytes for the race
pair. Five retained parallel normal repeats were 27.54–33.74s, with no flake,
owned-process survivor, or retained test artifact. The focused correctness
reruns are recorded separately from these retained timing measurements.

### Rejected M03 categories

- The grouping/preflight row gates were reverted after the full-run contention
  regression; only the 21 isolation-audited rows remain parallel.
- Published-clone fixture characterization found at most 19.14 seconds of
  possible saving, below the required forecast threshold, so no template/clone
  rewrite was retained.
- No measured fixed wait was shown to be a non-contract success-path delay, and
  no duplicate scenario matrix was removed without a complete assertion map.
- The speculative one-CLI and global-weight admission-order experiments were
  reverted. The accepted M02 configuration remains two deterministic CLI
  shards at workers 2–4 with its prior deterministic admission order and
  physical service batching.

## 18. M04 finalization snapshot

The final bounded inventory audit on 2026-09-03 discovered 183 canonical
top-level CLI targets (`679b76b8a7508e80e8cb59e5e211baa13d544e55b71d411a3e7bdae65487c09d`)
and 674 schedulable service targets
(`5c5ddd501e494d16b77eecd22a099dcdef4582c3332e0c7076ea77dbc7682114`).
The service inventory was exactly eight logical shards with target counts
85/85/84/84/84/84/84/84. The helper TSV contains one header and 15 helper
exclusions; both normal and race discovery were cold-cache and produced the
same 674-target canonical service union. The audit used a mode-0700 private
`TEST_RUNNER_CACHE_DIR`; cold discovery created no timing observation file.

The frozen local lane contract is: `test` and `test-race` remain exhaustive
aliases for `test-full` and `test-full-race`; normal and race use separate
30-minute and 45-minute per-command defaults; and `TEST_JOBS` defaults to
four with a bounded 1–4 runner cap. `check-local` remains the fast short-mode
gate with format, vet, short suite, bounded real-Git smoke, runner tests,
target-harness, and build checks. `test-changed` requires an explicit base and
uses deterministic changed ownership; `test-changed-race` requires explicit
race-sensitive package selection. `check-full` expands to format, vet, full
normal, full race, build, tutorial, release, and target-harness lanes, but is
not used as a substitute for recording their individual terminal evidence.
The retained changed-area normal evidence used a disposable clone containing
only the Makefile and runner tree, completed the bounded selected action in
under three seconds, and avoided treating the broad dirty worktree as a
changed-area sample. The target-lane harness also captures changed normal
composition and exercises focused-race child-failure propagation with a
bounded fake command; retained focused race covered testutil and runner in
approximately 2.47 and 1.83 seconds.

The accepted topology remains canonical: one anchored CLI command at worker
one and two deterministic CLI commands at workers two through four; eight
logical service commands at workers one and two; and up to 16 weighted physical
parts at workers three and four. The global cap covers every runner-owned CLI,
ordinary, and service command. Logical `--shard` reproduction, cache
observations, and result accounting remain defined over the complete logical
service union, not a physical part. Timing-cache data is private to the
runner-owned cache root and keyed by schema/platform/architecture/mode; only a
complete successful service observation can replace its complete weight file.

The retained terminal measurement is the 509.01-second physical-batch normal
run with exact 183 CLI and 674 service results, peak runner RSS of 225,837,056
bytes, and no owned survivor. Per the explicit 2026-09-03 authorization, the
620.72-second coarse-service race remains a different-topology policy
reference, not a matching final-source race bound; its sum with normal is the
accepted 18m49.73s policy reference under the amended <=8m30 normal and <=19m
combined budgets. M03's identity and 21-row creator evidence remains the map
in section 17. Native Windows execution is excluded from this local plan and
belongs to the separate Windows/CI work; compile-only foreign checks never
claim native acceptance.
