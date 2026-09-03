# Local test verification strategy specification

Status: implemented
Source idea: none (created directly)
Implementation plan: [Local test acceleration implementation plan](../plans/local-test-acceleration.md)
Implementation context: [Local test acceleration context](../plans/local-test-acceleration-context.md)
Related specification: [Windows portability and CI hardening specification](windows-portability-and-ci-hardening.md)

Scope amendment (2026-09-03): the user accepted the measured local performance
status quo. The complete-normal budget is now 8 minutes 30 seconds and the
complete normal-plus-race budget is 19 minutes on the reference host. Native
Windows execution remains owned by the related Windows/CI work and is not an
acceptance gate for this local-only implementation.

For the associated local acceleration plan's M02 only, the user also
authorized a narrow performance-evidence waiver: the retained 620.72s
coarse-service worker-4 race may be paired with the 509.01s physical-batch
worker-4 normal instead of rerunning a same-topology final-source full race.
Their 18m49.73s sum is an accepted policy reference, not a demonstrated
same-configuration bound. This exception does not weaken exact
inventory/result, command-cap, focused-race, cancellation, or failure-output
contracts, and it does not amend other plans' terminal-race requirements.

## 1. Purpose

This specification defines a fast, deterministic local test workflow for
developers and milestone-driven agents without weakening the repository's
complete verification evidence. It separates iteration feedback from frozen-
candidate acceptance, adds bounded local concurrency for the real-Git service
suite, and prevents repeated full normal/race/tutorial/release execution after
every small remediation.

The strategy changes local verification policy. It does not retroactively
change an already-authorized plan or its durable run ledger. Applying the new
policy to an active or blocked plan requires an explicit user-authorized scope
amendment to that plan. New plans may use this strategy after its implementation
is approved and verified.

## 2. Goals and budgets

The local workflow must provide four explicit outcomes:

| Outcome | Required budget on the reference implementation host |
|---|---:|
| Fast deterministic feedback | at most 2 minutes |
| Changed-area normal plus focused race feedback | at most 5 minutes for an ordinary bounded change |
| Complete normal suite with bounded local workers | at most 8 minutes 30 seconds |
| Complete normal plus race suites with bounded local workers | at most 19 minutes |

The reference comparison uses the same host, source revision, Go version,
uncontended process state, and uncached `-count=1` execution. The baseline
captured for this work is approximately 33 minutes for service normal plus
race execution. Performance evidence must record wall time, aggregate process
time when available, worker count, platform, target count, and cache state.

Speed is not allowed to come from omitting required evidence, increasing
timeouts as the sole change, unbounded worker fan-out, or crediting the Go test
result cache.

## 3. Test lanes

### 3.1 Focused iteration

During RED → GREEN → REFACTOR work, run the smallest owning-package test
selection that proves the behavior. Add focused race execution when the change
touches concurrency, processes, locks, cancellation, shared state, filesystem
mutation, atomic publication, rollback, recovery, or another race-sensitive
boundary.

Focused evidence is progress evidence. It does not replace the milestone or
final-candidate gates below.

### 3.2 Fast local gate

`make check-local` is the deterministic local feedback gate. It includes:

- tracked-source formatting;
- `go vet ./...`;
- `go test -short` across every package with an explicit short timeout;
- a bounded representative real-Git/process smoke inventory;
- build verification; and
- the deterministic local-runner/helper harness.

Short mode skips only expensive real-Git or process-heavy integration setup.
It continues to execute meaningful pure, parser, planner, rendering, error,
serialization, fake-adapter, and contract tests in every package. It is not
complete release evidence.

### 3.3 Changed-area gate

`make test-changed BASE_REF=<commit>` executes normal tests for changed Go
packages and their in-repository reverse dependency closure. Test-only changes
include the owning package; shared test-helper changes include all test
consumers. Non-Go harness/workflow changes select their named deterministic
harnesses. Missing or invalid `BASE_REF` fails closed rather than guessing an
authoritative comparison point.

Focused changed-area race coverage is explicit:
`make test-changed-race PACKAGES='<package patterns>'`. The caller selects the
race-sensitive package closure from the risk categories in §3.1; an automatic
filename heuristic must not claim semantic race sufficiency.

### 3.4 Complete local gates

`make test-full` runs the complete normal inventory through the bounded local
runner. `make test-full-race` runs the complete race inventory. `make
check-full` performs complete normal/race, tutorial, release, formatting, vet,
and build evidence.

The existing `make check` remains a compatibility alias for `check-full` until
every active and initial source plan that relies on its current meaning is
explicitly migrated. `make check` must not silently become a fast gate.

## 4. Verification frequency

The default evidence schedule for new milestone plans is:

1. implementation iterations: focused RED/GREEN/refactor selections;
2. complete implementation or remediation submission: `make check-local`,
   changed-area normal tests, and focused race tests;
3. reviewer approval candidate on frozen source: one complete uncached normal
   run plus milestone-specific gates;
4. terminal plan candidate: complete normal evidence, complete tutorial/release
   evidence, and a complete race result from the designated authoritative CI
   job or one local `test-full-race` run when matching CI evidence is not
   available, except for the documented M02 local-plan waiver above; and
5. reruns after a test-only or source correction: only invalidated evidence,
   followed by the applicable frozen-candidate boundary.

An identical expensive command need not be repeated under a second target name
when source, environment, flags, mode, and inventory are unchanged. The durable
run ledger references the retained terminal result.

Existing source plans remain authoritative for their runs. This schedule may
replace their gates only through an explicit scope amendment, never by editing
a historical run ledger or silently reinterpreting a command.

## 5. Bounded local concurrency

The complete service inventory is partitioned into eight logical shards and
scheduled by a repository-native local runner with a worker pool:

- default workers: 4;
- accepted worker range: 1–4;
- worker count includes every concurrently active `go test` command owned by
  the runner;
- normal and race modes run separately by default;
- authoritative complete runs finish every assigned shard and accumulate
  failure status;
- an explicit non-authoritative fail-fast option may cancel queued work for
  iteration feedback; and
- interruption cancels owned commands and removes only runner-owned temporary
  logs.

Separate `go test` processes provide isolation between service shards. Do not
add blanket in-process `t.Parallel()` calls. Selective parallel tests require
proof of independent repositories, directories, environment, listeners,
registries, hooks, and processes, and remain subject to `-parallel=4` unless a
lower measured bound is required.

## 6. Duration-balanced assignment

The local runner maintains disposable per-platform, per-mode timing weights
under a runner-owned directory below `go env GOCACHE`. The format is a simple,
versioned text format with test identity, observed elapsed time, sample count,
and source timestamp; it contains no repository content or secrets.

When weights exist, use deterministic longest-processing-time-first assignment
across eight logical shards. Unknown targets receive the current median weight;
stale targets are ignored. When no weights exist, use deterministic sorted
round-robin assignment and write observations for the next run. Timing cache
corruption or version mismatch falls back safely and is reported; it cannot
omit or duplicate inventory.

The assignment contract is deterministic for a given inventory and weight
file. Empty, duplicate, missing, unknown, and incompletely assigned inventory
fails closed in authoritative mode. The union of logical shards must equal the
discovered inventory exactly once.

## 7. Race strategy

Local iteration and ordinary remediation use focused race selections based on
the risk categories in §3.1. Full local race is reserved for terminal evidence,
explicit investigation, or lack of matching authoritative CI evidence.

The repository must retain a complete race command and designated exhaustive
race CI evidence. This local strategy does not itself remove race coverage from
any operating system's hosted matrix; that belongs to the related CI strategy
and source specification. Platform-specific concurrency and process behavior
continues to require focused native-platform race or equivalent behavioral
evidence.

## 8. Fixture and wait optimization

The local runtime owner may reduce avoidable work while preserving semantics:

- configure hermetic Git author/committer identity without two persistent
  `git config` subprocesses per new repository;
- consolidate duplicated service/CLI/process scenarios only with an explicit
  old-to-retained coverage map;
- reuse immutable inputs only when every test retains isolated mutable state;
- replace fixed sleeps with observable readiness/cleanup synchronization when
  elapsed time is not the behavior under test; and
- keep real Git at boundaries whose subject is Git behavior.

No mutable working repository, worktree, registry, environment, listener,
hook state, or process may be shared across tests. Timing, cancellation,
cleanup, atomicity, rollback, recovery, durability, identity, and adversarial
coverage cannot be weakened for speed.

## 9. Compatibility and adoption

- The local runner and timing collector are repository-native Go code using
  only the standard library. Product packages must not depend on them.
- Existing `test`, `test-race`, and `check` behavior remains available during
  migration. New explicit target names communicate fast, changed, full, and
  full-race semantics.
- Plan-authoring guidance may adopt the tiered schedule for newly authored
  plans after implementation. Milestone-supervision state transitions,
  independent review, remediation limits, and durable-ledger rules do not
  change.
- Active and completed run ledgers are immutable outside their authorized run.
- No dependency addition, product behavior change, schema change, commit,
  push, pull request, workflow dispatch, publication, or release is authorized
  by this specification.

## 10. Required acceptance evidence

Acceptance requires:

- deterministic unit tests for inventory, reverse-dependency selection,
  duration balancing, unknown/stale weights, exact-once assignment, bounded
  workers, failure aggregation, interruption, and owned cleanup;
- fast/full classification tests proving no external Git command starts in
  short mode and full mode remains complete;
- fixture hermeticity and process-count evidence;
- before/after uncached same-host normal and race timings;
- at least five repeated focused concurrency-runner trials without a new flake,
  leaked process, or leftover temporary artifact;
- `check-local`, changed-area, `test-full`, `test-full-race`, and `check-full`
  target-contract tests;
- complete normal and race suite passes, formatting, vet, build, tutorial,
  release, and diff checks on the final source, except for the documented
  M02 local-plan same-topology race waiver;
- native-platform evidence for affected platform-specific behavior; and
- documentation of lane choice, risk-based race selection, timing cache,
  worker tuning, failure reproduction, and adoption boundaries.
