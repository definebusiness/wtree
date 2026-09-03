# Implementation context — test-suite runtime optimization

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Test-suite runtime optimization implementation plan](test-suite-runtime-optimization.md)
Source specification: [Windows portability and CI hardening specification](../spec/windows-portability-and-ci-hardening.md)
Related plans: [Windows portability and CI hardening implementation plan](windows-portability-and-ci-hardening.md); [Windows portability simplification and CI remediation implementation plan](windows-portability-simplification-and-ci-remediation.md)
Captured: 2026-09-02
Investigated branch: `feat/lifecycle-hooks`
Investigated base commit: `633d5f1d1e5d57e644b127edfd48d78c45d97711`

## 1. Purpose and authority

This document preserves the measurements, repository structure, and design
constraints behind the focused runtime-optimization plan. It is evidence and
implementation context, not authority to reduce product behavior or test
coverage. If a fact in this snapshot differs from the tree when the plan is
executed, the executor must remeasure it and record the updated result in the
plan's durable run ledger before changing the design.

The source specification remains authoritative for complete cross-platform
normal/race coverage, bounded execution, exact-once partitioning, useful
failure reporting, and the prohibition on weakening identity, rollback,
durability, cleanup, or portability tests merely to improve runtime.

## 2. Reported and recorded symptom

The user reported that the Windows CI path takes roughly 60 minutes. The
implemented Windows-portability plan contains a precise hosted baseline that
is slightly worse:

- GitHub Actions run `33229459008` completed Windows in `1h13m43s`.
- The Windows normal inventory took `35m56s`.
- The Windows race inventory took `35m24s`.
- Ubuntu completed in `7m24s`; macOS completed in `25m40s`.

Those results are recorded in the [implemented plan](windows-portability-and-ci-hardening.md#execution-log)
and [documentation status overview](../status-overview.md). They prove that
the Windows critical path is a repository-level problem, not merely a slow
local machine or one isolated test.

Recent local final-candidate evidence in the active lifecycle-hook run also
shows the shape of the cost. One uncached normal service pass recorded shard
durations of `162.705s`, `116.328s`, `145.513s`, `97.233s`, `76.283s`,
`90.387s`, `140.619s`, and `123.410s`, totaling about `15m52s`. Its race
counterpart recorded `186.867s`, `124.997s`, `158.502s`, `108.035s`,
`81.319s`, `103.682s`, `156.085s`, and `122.684s`, totaling about `17m22s`.
The same evidence recorded a normal non-service batch dominated by
`internal/cli` at `255.509s`. These are implementation-host observations, not
native Windows acceptance evidence, but they show that the sequential service
inventory and CLI integration tests dominate even off Windows.

## 3. Static inventory snapshot

The investigated tree contains:

| Metric | Observed value |
|---|---:|
| Go test files | 172 |
| Test-source lines | 52,743 |
| Non-test Go-source lines | 35,667 |
| Test-to-production line ratio | 1.48:1 |
| Top-level `Test`, `Fuzz`, and `Example` functions | 1,189 |
| Top-level service functions | 710 |
| Top-level CLI functions | 184 |
| Top-level Git-adapter functions | 86 |
| Static `testutil.RunCommand` call sites | 374 |
| Static new Git/bare/pushed fixture call sites | 331 |
| All static shared Git-fixture references | 427 |
| `t.Parallel()` calls | 5 |

The top-level function count does not include subtests, table rows, helper
processes, or commands spawned inside a test, so it understates the dynamic
work. The largest concentration is `internal/service`, whose test files alone
include multiple files between roughly 1,000 and 2,500 lines.

Top-level functions by package at capture time:

| Package | Count |
|---|---:|
| `internal/service` | 710 |
| `internal/cli` | 184 |
| `internal/git` | 86 |
| `internal/fsutil` | 54 |
| `internal/store` | 34 |
| `internal/config` | 34 |
| `internal/discovery` | 22 |
| `internal/domain` | 21 |
| `internal/pathutil` | 15 |
| `internal/lock` | 10 |
| `internal/transaction` | 6 |
| `cmd/wtree` | 5 |
| `internal/render` | 4 |
| `internal/testutil` | 2 |
| `internal/plan` | 2 |

## 4. Current execution architecture

The workflow has one job per operating system. Ubuntu and macOS each execute a
monolithic normal command followed by a monolithic race command. Windows calls
`scripts/ci-test.sh normal`, then calls the same script in race mode.

For each Windows mode, the helper:

1. discovers all packages;
2. runs all non-service packages together once;
3. discovers top-level service tests, examples, and fuzz targets;
4. removes known subprocess-helper entry points;
5. assigns the remaining service targets round-robin to eight patterns;
6. validates the in-memory assignment as disjoint and exhaustive; and
7. executes every non-empty pattern in one shell `for` loop.

The eight units are therefore sequential partitions, not parallel CI shards.
They bound individual `go test` processes and improve failure localization,
but their elapsed times add together. The workflow then repeats the complete
shape under the race detector.

The helper also owns package inventory, target inventory, annotation escaping,
failure-excerpt classification, command/transport status reconciliation,
temporary logs, cleanup traps, and a large deterministic fake-tool harness.
Those behaviors were appropriate remediation for earlier opaque timeouts, but
the machinery does not shorten the critical path.

## 5. Why Windows amplifies the cost

The suite intentionally uses real Git repositories and real command/process
boundaries. `internal/testutil.NewGitRepository` currently launches separate
commands for repository initialization and two identity configuration writes.
A pushed fixture additionally creates a bare repository and configures a
remote; ordinary commits add `git add`, `git commit`, and often `git push`.
One static fixture call can therefore expand into many filesystem-heavy child
processes, and table-driven subtests multiply that work.

Windows pays more for this shape because process creation, executable and file
inspection, antivirus scanning, directory deletion, and many small filesystem
operations are generally more expensive on hosted Windows runners. Running
almost all 710 service tests serially inside each partition leaves that cost
on the critical path. The five current `t.Parallel()` calls do not materially
change it.

Several tests also contain deliberate bounded sleeps or timeout helpers. Many
long sleeps belong to child processes that the parent is expected to cancel,
so static duration literals are not automatically elapsed-time defects.
However, real five-second waits in aggregate tests and multi-second
process-cleanup assertions must appear in the runtime profile and be replaced
with observable synchronization when their wall-clock delay is not itself the
contract.

## 6. Coverage layers and likely duplication

The current suite mixes four kinds of evidence in the same default path:

1. pure domain, parsing, rendering, and planning behavior;
2. service behavior using real Git repositories and real filesystem state;
3. CLI behavior that often reconstructs equivalent repositories and then
   calls the same services; and
4. black-box process, platform, cancellation, cleanup, and end-to-end behavior.

The service and CLI layers both need representative integration coverage, but
they do not both need every business-state permutation. The intended ownership
for optimization is:

- pure rules remain fast table-driven tests in their owning package;
- Git semantics remain real-Git tests at the Git adapter or smallest service
  boundary that owns the contract;
- service tests own business-state, transaction, rollback, recovery, and
  authority matrices;
- CLI tests own parsing, help, rendering, stdout/stderr, exit behavior, and a
  bounded representative end-to-end set; and
- native platform/process tests retain the divergent Windows/Unix behavior
  they uniquely prove.

Removing a test is acceptable only when a recorded mapping identifies an
equivalent or stronger retained test for every assertion and failure window.
Line count alone is not a removal criterion.

## 7. Constraints that must survive optimization

- Required CI continues to execute every discovered normal and race target on
  Ubuntu, macOS, and Windows unless the source specification is explicitly
  changed by the user.
- Partition assignment remains deterministic, disjoint, exhaustive, and
  fail-closed when inventory is empty, duplicated, missing, or unassigned.
- Matrix failure aggregation uses `fail-fast: false`, so one failed partition
  does not cancel evidence from the remaining partitions.
- No required full-suite test may be hidden behind `testing.Short()` or a
  nightly-only workflow. Short mode is an additional developer lane.
- Real Git remains mandatory where Git behavior is the subject. Fakes may
  replace orchestration work only when a separate retained integration test
  proves the adapter boundary.
- Test fixtures remain hermetic. Do not share a mutable repository, working
  tree, registry, environment, listener, or process across tests.
- Do not add blanket `t.Parallel()` calls. Parallel work must have explicit
  resource isolation and a measured concurrency bound.
- Do not weaken timing, cancellation, process-tree cleanup, atomicity,
  durability, identity, rollback, recovery, privacy, or adversarial tests.
- No new third-party dependency is justified for profiling or sharding; Go,
  Bash, and GitHub Actions already provide the necessary primitives.

## 8. Target execution model

The smallest useful target design is:

```text
required workflow
├── Ubuntu complete normal/race job (unchanged inventory)
├── macOS complete normal/race job (unchanged inventory)
├── Windows quality/build/release job
├── Windows non-service matrix: normal, race
├── Windows service matrix: normal/race × shard 0..7
└── required aggregate result over every Windows matrix cell

developer workflow
├── fast: go test -short with no real-Git/process-heavy fixtures
└── complete: unchanged exhaustive normal/race gates
```

Each Windows service matrix cell discovers the same sorted top-level inventory
and selects exactly one index modulo the configured shard count. The workflow,
not a shell loop, supplies concurrency. Eight is a cap, not a minimum: the
implementation must not increase fan-out beyond the existing eight
partitions. Keep simple modulo assignment unless recorded shard evidence shows
the slowest shard exceeds 1.5 times the median; only then may a deterministic
duration-balanced assignment be introduced, with its timing data and stale/new
target behavior tested.

## 9. Performance budgets

The implemented hosted baseline is the comparison point. On an uncached
matching revision:

- the Windows required-test critical path, measured from the first test cell
  starting to the last required test cell ending and excluding queue time,
  must be at most 20 minutes;
- the complete Windows workflow critical path should be at most 25 minutes;
- aggregate Windows normal/race test execution across all partition cells
  must be at most 90 runner-minutes, preventing unbounded speed-by-fan-out;
- no discovered target may be omitted or executed more than once per mode;
- Ubuntu and macOS execution must not regress by more than 10% against a
  same-revision control without an explained runner-level cause; and
- the developer `-short` lane must complete within two minutes on the same
  implementation host where the complete normal suite is measured, while
  continuing to run meaningful tests in every package.

The hosted acceptance run must publish per-cell target counts and elapsed
times so the thresholds can be audited. Queue time is reported separately and
does not count against an execution budget.

## 10. Reproduction and investigation commands

These commands produced or validate the static snapshot without modifying the
repository:

```bash
rg --files -g '*_test.go' | wc -l
rg -n '^func (Test|Fuzz|Example)' -g '*_test.go' | wc -l
wc -l $(rg --files -g '*_test.go') | tail -1
wc -l $(rg --files -g '*.go' -g '!*_test.go') | tail -1
rg --glob '*_test.go' -c '\.Parallel\(\)' internal cmd
rg -o --glob '*_test.go' 'testutil\.RunCommand' internal cmd | wc -l
rg -o --glob '*_test.go' 'testutil\.New(PushedGitRepository|GitRepository|BareGitRemote)' internal cmd | wc -l
go test -count=1 -json ./...
go test -race -count=1 -json ./...
go test -list '^(Test|Example|Fuzz)' ./internal/service
```

On Windows, retain the GitHub per-step and per-matrix-cell timestamps together
with the `go test -json` artifacts. Do not treat a warm Go test-cache result as
runtime evidence.

