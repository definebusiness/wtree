# Windows portability simplification and CI remediation implementation plan

Status: initial
Source specification: [Windows portability and CI hardening specification](../spec/windows-portability-and-ci-hardening.md)
Implementation context: [Hosted failure and simplification context](windows-portability-simplification-and-ci-remediation-context.md)
Related prior plan: [Windows portability and CI hardening implementation plan](windows-portability-and-ci-hardening.md)
Hosted RED baseline: [GitHub Actions run 33168555356](https://github.com/definebusiness/wtree/actions/runs/33168555356) for `826757565e72f7ec3620e0b500818395d0f0f480`
Source of truth: [`internal/git/ignore_committed.go`](../../internal/git/ignore_committed.go); clone/update staging and workspace identity code under [`internal/service`](../../internal/service); native path and protection tests under [`internal/config`](../../internal/config), [`internal/store`](../../internal/store), and [`internal/cli`](../../internal/cli); [`scripts/ci-test.sh`](../../scripts/ci-test.sh); [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml); [`Makefile`](../../Makefile)
Delivery style: test-first simplification, causal failures before cascades, one independently reviewed milestone at a time; no commit, push, workflow rerun, publication, dependency installation, deployment, or real-user-data change without separate authorization

## Execution contract for Codex

When this plan is explicitly authorized for implementation, follow
[`milestone-supervision.md`](../ai/milestone-supervision.md) continuously from
the first unchecked milestone. Create and maintain only this plan's durable
run ledger at
`docs/ai/runs/windows-portability-simplification-and-ci-remediation.md`; do not
edit the prior plan's ledger.

For each unchecked milestone, in order:

1. Read this plan, the source specification, the implementation context, the
   relevant current code and tests, the current worktree, and this plan's
   durable ledger. Reconcile the ledger with the filesystem before resuming.
2. Record every scope item, test-first slice, documentation requirement, exit
   criterion, and verification command in the current milestone checklist.
3. Dispatch the complete packet to the normal `implementer`. Require focused
   RED → GREEN → REFACTOR evidence, changed files, command results, and
   unresolved concerns.
4. Treat a partial submission only as progress. Send the entire completed
   milestone to the read-only `reviewer` only after every checklist item has
   evidence.
5. Record all reviewer findings with stable IDs. Return the complete unresolved
   set in one remediation packet and apply the exact remediation and escalation
   limits from the supervision process.
6. After reviewer approval, run main-agent verification, update contracts and
   evidence, check the milestone, append one execution-log row, initialize the
   next milestone checkpoint, and dispatch it immediately.

Preserve unrelated worktree changes. A final response is permitted only by the
durable-ledger final-response gate. Hosted verification that requires a commit,
push, pull request, or workflow rerun remains an external authorization gate;
record it exactly rather than performing the action.

## Problem statement

The first hosted run of the prior implementation failed on Ubuntu, macOS, and
Windows. The failures show both real portability gaps and unnecessary design
complexity:

- a POSIX device path is used as a Git exclude file;
- the clone design pre-creates the directory that Git for Windows must create;
- a path-only filesystem receipt can accept Linux inode reuse;
- stable staging inventory races with transient Git maintenance locks;
- several tests encode Unix paths or mode bits as Windows contracts; and
- the Windows CI runner may contain more sharding and diagnostic machinery
  than measured native runtime requires.

This plan repairs those failures with smaller primitives while retaining the
security and coverage outcomes that motivated the prior work.

## Fixed implementation decisions

### Replace `/dev/stdin` with a private temporary exclude file

- Compose committed `.gitignore` content exactly as today, but write the Git
  exclude input to an exclusively created, effective-user-only temporary file.
- Pass that ordinary native path to Git. Never use `/dev/stdin`, `/proc`, named
  pipes, shell process substitution, or host-global Git configuration.
- Close and remove the file on success, command failure, cancellation, and
  partial setup. Cleanup failure must be reported without hiding the primary
  operation result.
- Replace the no-temporary-storage purity test with isolation, exact-content,
  permissions, and cleanup tests. Preserve winning-negation and requested-ref
  behavior.

### Put privacy on a container around an absent Git child

- Create a unique private same-volume container under the validated
  destination parent. The Git destination child must initially be absent.
- On Windows, apply and verify the effective-user-only protection at the
  container boundary. Use the smallest native API surface required to prove
  that contract.
- Let Git create its destination child. After Git completes, validate that the
  child is an owned directory, not a symlink or reparse point, remains inside
  the expected parent/container, and inherits the required privacy.
- Retain only the container authority and identity needed across the Git
  operation. Do not keep a child handle open if its sharing mode can block Git.
- Publish and clean up only paths proven to belong to this transaction.
  Preserve rollback, recovery, stale-generation, and adversarial replacement
  checks.

### Use retained identity only across real substitution windows

- A receipt that must survive unlink-and-recreate holds an open directory
  descriptor or Windows handle until final validation, then compares the live
  object behind that authority with the current path.
- Keep handle ownership local to the transaction boundary; do not propagate a
  general retained-handle abstraction through unrelated models.
- Close every retained descriptor deterministically before removal or
  publication steps that Windows sharing semantics could block.
- Use a deterministic test seam for reused numeric identities where the host
  cannot reliably reproduce inode reuse on demand.

### Quiesce managed Git staging before stable inventory

- Run Git commands that mutate private clone/update staging with
  `maintenance.auto=false` and `gc.auto=0`, or the documented equivalent
  supported by the repository's Git invocation layer.
- Capture ownership inventory only after the synchronous command completes.
- Do not add a broad volatile-path allowlist unless a focused regression proves
  that quiescing Git is insufficient. Any exception must be narrow and must not
  permit unowned content to evade cleanup validation.

### Test Windows behavior with Windows contracts

- Use `t.TempDir()` and `filepath` for actual filesystem paths. Preserve
  literal configured spelling only in tests whose public contract requires it.
- Assert sensitive-file protection through a platform-owned observable helper,
  not exact Unix mode bits on Windows and not an unconditional skip.
- Treat push-backup persistence and status/update failures as unresolved until
  focused tests prove whether they are causal or cascading. Close handles
  before weakening any removal assertion.

### Prefer the simplest CI runner supported by measurements

- First measure the native Windows monolithic service suite in normal and race
  modes using the authoritative bounds. Prefer direct `go test` commands if
  both modes complete reliably with useful raw GitHub logs.
- If monolithic execution fits, remove the custom Windows shard orchestration
  and redundant annotation taxonomy while preserving all packages, top-level
  tests, examples, fuzz targets, race coverage, and failure exit statuses.
- If sharding remains necessary, keep only dynamic discovery, deterministic
  disjoint/exhaustive assignment, exact-once validation, accumulated shard
  status, transport status, cleanup, and basic actionable annotations. Remove
  bespoke excerpt classification that duplicates GitHub's raw logs.
- Align `make check`, local documentation, helper timeouts, and workflow
  timeouts around one authoritative contract so the aggregate local gate can
  pass rather than merely document a known timeout.
- Preserve LF checkout, NUL-safe formatting, vet, build, release-layout,
  release-reuse, manifest, tutorial, and repository checks.

## Architecture and ownership

```text
committed ignore evaluator
  └── private native temp file ──→ Git ──→ deterministic cleanup

validated destination parent
  └── private same-volume container
      └── absent child ──→ Git creates ──→ validate ──→ publish

substitution-sensitive operation
  └── retained directory authority ──→ live identity comparison ──→ close

managed staging Git command
  └── maintenance disabled ──→ command completes ──→ stable inventory

CI workflow
  └── monolithic tests when bounded
      └── minimal exact-once sharding only when measured necessity remains
```

- `internal/git` owns portable Git input construction and exclusion isolation.
- `internal/service` owns private staging, transaction authority, identity,
  inventory, publication, rollback, recovery, and cleanup.
- `internal/fsutil` owns reusable platform filesystem facts and atomic
  publication, but not service transaction policy.
- Configuration, store, and CLI tests express public native-path, privacy, and
  output contracts rather than platform implementation details.
- `.github/workflows`, `scripts`, and the `Makefile` share one bounded test
  contract and do not own product behavior.

## Global definition of done

- Every production change has focused RED → GREEN → REFACTOR evidence tied to
  one exact failure or contract.
- All failures recorded in the companion context are classified as fixed,
  cascading and eliminated, or independently reproduced and resolved.
- The prior security outcomes remain intact: old-or-new atomic publication,
  private staging, parent/child ownership, reparse/symlink rejection,
  substitution detection, rollback/recovery integrity, and owned-only cleanup.
- No POSIX-only path is passed as a portable Git input. Temporary committed-
  ignore input is private, exact, and removed on every tested exit.
- Git for Windows creates the initially absent staging child successfully
  inside the private container, and adversarial replacement tests still fail
  closed.
- Linux replacement receipts are handle-backed at the required boundary and
  cannot accept a reused path identity.
- Update staging inventory is stable under Git 2.55 maintenance behavior.
- Native Windows fixtures, protection checks, backup cleanup, status, update,
  clone, and public CLI tests pass without skips that reduce contract coverage.
- The chosen Windows CI shape is justified by recorded normal/race timing and
  exact inventory evidence. Removed helper complexity has no remaining caller
  or stale documentation.
- Run, at minimum:

  ```text
  go test ./... -count=1
  go test -race ./... -count=1
  go vet ./...
  make fmt-check
  make build
  make release-test
  make check
  sh tutorial/run-all-commands.sh
  git diff --check
  ```

- Run the selected CI-helper harness if a helper remains, and compile every
  affected platform-specific package for `windows/amd64` before hosted CI.
- A matching GitHub Actions run for the exact reviewed tree passes Ubuntu,
  macOS, and Windows through normal tests, race tests, vet, formatting, build,
  release, and repository-quality stages. Starting that run still requires
  separate authorization for any commit, push, pull request, or workflow
  action.
- The specification, both related plans, context, CI contributor guidance,
  status overview, and evidence agree. Neither plan nor the specification is
  marked implemented until all required hosted evidence exists.
- Independent review has no unresolved material findings.

## Risk and rollout

- A temporary exclude file holds repository-controlled patterns. Exclusive
  creation, restrictive access, exact bytes, no command-line content, and
  cleanup on all exits bound that exposure.
- A private container is safe only if the child cannot escape or replace its
  parent relationship. Validate identity, type, containment, and Windows
  reparse behavior after Git and before publication.
- Retained handles can themselves break Windows cleanup. Keep them local and
  close them before rename/removal boundaries.
- Disabling automatic maintenance must be scoped to private managed commands;
  do not change user or repository Git configuration persistently.
- Removing CI shards can reintroduce timeouts; retain measurements and restore
  only the minimal sharding layer if the authoritative bound is exceeded.
- Hosted matrix verification may require user-authorized repository actions.
  Local completion is not lifecycle completion.

## Milestones

### [ ] M00 — Make Git inputs and staging inventory portable

Specification coverage: [§4.1](../spec/windows-portability-and-ci-hardening.md#41-portable-git-inputs-and-stable-staging-state), [§6](../spec/windows-portability-and-ci-hardening.md#6-compatibility-and-safety-boundaries), and [§7](../spec/windows-portability-and-ci-hardening.md#7-required-verification).

Scope:

- Replace `/dev/stdin` committed-ignore evaluation with the private native
  temporary-file design.
- Preserve requested-ref, nested winning-negation, exact rule composition, and
  isolation from local/global/user excludes.
- Disable automatic maintenance and garbage collection for managed private
  clone/update staging commands before stable inventory capture.
- Add cleanup and primary/secondary error tests for success, Git failure,
  cancellation, partial setup, and inventory failure.
- Do not change clone destination creation, workspace identity, CI
  orchestration, or unrelated Git commands in this milestone.

Test-first slices:

1. Reproduce the three committed-ignore failures using a Git invocation that
   rejects `/dev/stdin`, then replace the no-temp assertion with exact private-
   file and cleanup contracts.
2. Prove requested-ref and nested winning-negation behavior while repository,
   global, and user excludes contain conflicting rules.
3. Reproduce the transient `maintenance.lock` inventory race with a controlled
   Git-command seam, then prove inventory begins only after quiescent command
   completion.
4. Verify no persistent Git configuration changes and no owned temporary
   artifacts remain on any tested exit.

Verification:

- `go test ./internal/git -run 'Committed|Gitignore|WinningNegation' -count=1`
- `go test ./internal/service -run 'Doctor.*Committed|UpdatePublication|Maintenance|StagingInventory' -count=1`
- `go test -race ./internal/git ./internal/service -run 'Committed|Gitignore|UpdatePublication|Maintenance|StagingInventory' -count=1`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/git`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/service`
- Applicable global definition-of-done commands.

Exit criteria: committed-ignore evaluation works with Git 2.55 semantics on
all target platforms without ambient excludes, and managed staging inventory
cannot race automatic Git maintenance.

### [ ] M01 — Simplify private clone and update staging

Specification coverage: [§4](../spec/windows-portability-and-ci-hardening.md#4-filesystem-identity-modes-and-path-preservation), [§6](../spec/windows-portability-and-ci-hardening.md#6-compatibility-and-safety-boundaries), and [§7](../spec/windows-portability-and-ci-hardening.md#7-required-verification).

Scope:

- Replace the pre-created final staging root with a private same-volume
  container and initially absent Git child.
- Reduce the Windows native implementation to the minimum needed to establish
  and verify container privacy and transaction authority.
- Validate the child after Git creates it: directory type, identity,
  containment, parent/container continuity, reparse/symlink rejection, and
  inherited protection.
- Close handles before operations they can block and preserve owned-only
  publication, cleanup, rollback, recovery, and error precedence.
- Delete obsolete child-root handle/DACL machinery, tests, and comments once
  no caller or security contract requires them.

Test-first slices:

1. Add a native Windows process-boundary regression showing Git can create the
   absent child inside the protected container.
2. Prove the child does not exist before Git and that unexpected pre-creation,
   replacement, reparse points, wrong parent identity, or weak inherited
   protection fail closed.
3. Exercise Git failure, cancellation, publication conflict, cleanup failure,
   rollback, and recovery without deleting unowned content.
4. Re-run local/HTTP clone and update public CLI E2E through real Git process
   boundaries.

Verification:

- `go test ./internal/service -run 'Clone|Update|Staging|Publication|Rollback|Recovery' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Clone|Update|ProcessBoundary' -count=1`
- `go test -race ./internal/service -run 'Clone|Update|Staging|Publication' -count=1`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/service`
- `GOOS=windows GOARCH=amd64 go test -c ./cmd/wtree`
- Applicable global definition-of-done commands.

Exit criteria: real Git for Windows can create and populate staging while the
transaction retains private, substitution-resistant ownership and safe
cleanup with less native handle machinery.

### [ ] M02 — Harden only the necessary identity boundary and repair native contracts

Specification coverage: [§4](../spec/windows-portability-and-ci-hardening.md#4-filesystem-identity-modes-and-path-preservation), [§6](../spec/windows-portability-and-ci-hardening.md#6-compatibility-and-safety-boundaries), and [§7](../spec/windows-portability-and-ci-hardening.md#7-required-verification).

Scope:

- Replace path-only workspace/grouping receipts with retained directory
  authority only across the unlink-and-replacement-sensitive window.
- Add deterministic coverage for numeric identity reuse and prove all handles
  close before cleanup or publication.
- Convert pseudo-Windows filesystem fixtures to native temp paths while
  retaining literal spelling assertions for genuine public contracts.
- Add a platform-owned sensitive-file protection assertion and use it for
  store and atomic publication tests.
- Preserve configuration precedence, schema validation, configured spelling,
  workspace grouping, forest recovery, and public errors.

Test-first slices:

1. Reproduce acceptance of a replacement with a reused path identity through
   a deterministic seam, then prove the retained live object rejects it.
2. Test handle close behavior on success, conflict, cancellation, recovery,
   and cleanup, including Windows removal boundaries.
3. Reproduce each native-path configuration/CLI failure before changing only
   fixtures or truly non-portable production comparisons.
4. Prove private store files satisfy the observable Windows protection
   contract without requiring Unix mode-bit equality.

Verification:

- `go test ./internal/service -run 'WorkspaceGrouping|ForestReplacement|Receipt|Identity|Recovery' -count=1`
- `go test ./internal/config ./internal/domain ./internal/store -run 'Path|Topology|WorktreeRoot|Permission|Protection' -count=1`
- `go test ./internal/cli -run 'Project|Path|Workspace|Forest' -count=1`
- `go test -race ./internal/service ./internal/store -run 'Receipt|Identity|Recovery|Permission|Protection' -count=1`
- Windows compilation of every changed platform-specific package.
- Applicable global definition-of-done commands.

Exit criteria: Linux cannot accept a replaced directory through inode reuse,
Windows tests express native path and privacy contracts, and the solution does
not spread retained handles beyond the required safety boundary.

### [ ] M03 — Resolve remaining Windows cleanup and public CLI failures

Specification coverage: [§§4–7](../spec/windows-portability-and-ci-hardening.md#4-filesystem-identity-modes-and-path-preservation).

Scope:

- Re-run the exact Windows failure set after M00–M02 and classify each item as
  eliminated cascade or independent defect.
- Reproduce and repair push-backup persistence, auditing every open handle and
  removal boundary before changing assertions or cleanup policy.
- Reproduce and repair any remaining status identity-drift and update dry-run
  JSON failures without changing stable public output.
- Run all non-service Windows packages and the relevant service suites so no
  earlier failure is hidden by fail-fast behavior.
- Update the companion context only if new durable causal evidence materially
  changes its conclusions.

Test-first slices:

1. Add the smallest focused RED test for backup persistence and prove all
   owning handles are closed before successful removal.
2. Confirm whether status drift and update dry-run failures remain; add focused
   RED cases only for independent defects.
3. Exercise cleanup error precedence, read-only snapshots, output stability,
   and no-mutation dry-run behavior.
4. Run the original named Windows failures together and record the complete
   classification in this plan's ledger.

Verification:

- `go test ./internal/service -run 'PushResolverAuthority|Backup|Cleanup|Status|UpdateDryRun' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Push|Status|Update|Clone|Project' -count=1`
- `go test -race ./internal/service ./internal/cli ./cmd/wtree -run 'Push|Backup|Cleanup|Status|Update|Clone|Project' -count=1`
- Windows compilation of every affected package.
- Native Windows execution of the focused failure set.
- Applicable global definition-of-done commands.

Exit criteria: every original Windows failure is either absent because its
upstream cause is fixed or independently repaired with focused coverage; no
cleanup, output, or dry-run contract is weakened.

### [ ] M04 — Reduce CI to the smallest measured reliable design

Specification coverage: [§5](../spec/windows-portability-and-ci-hardening.md#5-portable-and-diagnostic-ci) and [§7](../spec/windows-portability-and-ci-hardening.md#7-required-verification).

Scope:

- Measure complete native Windows normal and race execution, including the
  service package, against documented authoritative bounds.
- Select monolithic execution when it fits; otherwise retain only minimal
  dynamic exact-once sharding and essential diagnostics.
- Remove unused shard, classification, annotation, and harness code together
  with stale tests and documentation, without reducing inventory or failure
  visibility.
- Align workflow, helper, `Makefile`, and contributor guidance so the same
  bounded contract governs CI and `make check`.
- Preserve matrix coverage, LF checkout, NUL-safe formatting, vet, build,
  release, manifest, tutorial, and repository gates.

Test-first slices:

1. Record native monolithic normal/race timing and complete discovered
   inventory before choosing the CI shape.
2. For monolithic execution, prove command/test failures and timeouts remain
   fatal with useful raw logs. For retained sharding, prove empty/duplicate/
   incomplete inventories fail closed and every target executes exactly once.
3. Prove later workflow stages run after successful tests and remain skipped
   or failed only for genuine preceding gate failures.
4. Run `make check` under the same authoritative bounds and remove all stale
   references to deleted helper behavior.

Verification:

- The selected repository-native CI harness tests, if a helper remains.
- `bash -n scripts/ci-test.sh` if that script remains.
- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `make check`
- Static workflow inspection plus the applicable global definition-of-done
  commands.

Exit criteria: CI uses the least complex design that measured Windows runtime
requires, executes complete normal/race coverage, reports failures reliably,
and agrees with a passing local aggregate gate.

### [ ] M05 — Verify the simplified design on every supported platform

Specification coverage: [§§2–7](../spec/windows-portability-and-ci-hardening.md#2-baseline-and-provenance).

Scope:

- Review the complete diff against the failure context and prove each failure
  and each removed mechanism has an evidence-backed disposition.
- Run all focused, full, race, quality, release, tutorial, cross-compilation,
  and documentation gates.
- Obtain one matching GitHub Actions run for the exact reviewed tree where
  Ubuntu, macOS, and Windows all reach and pass every required stage.
- Fix ordinary implementation or CI failures within scope; do not treat a red
  matrix as completion.
- Reconcile lifecycle evidence in the specification, this plan, the prior
  plan, and `docs/status-overview.md` only after independent approval and the
  matching matrix pass. Treat the prior ledger as immutable historical
  evidence; do not edit it from this run.

Test-first slices:

1. Add any missing integration regression exposed by the combined M00–M04
   diff before changing production behavior.
2. Run automatic-ignore, clone/update, atomic publication, staging, workspace,
   rollback/recovery, push/status, and CLI E2E together.
3. Audit schemas, root `.gitignore`, public output, current clone semantics,
   unrelated changes, and deleted-code references.
4. Diagnose and repair the exact hosted path until a matching matrix is green
   and later build/release gates are proven to have executed.

Verification:

- Every focused command from M00–M04.
- Every global definition-of-done command.
- Windows compilation of all affected platform-specific packages.
- Matching successful GitHub Actions run URL and Ubuntu/macOS/Windows job
  evidence for the exact reviewed tree.
- Final independent review of implementation, tests, removals, documentation,
  and scope control.

Exit criteria: the smaller design is fully reviewed and green on Ubuntu,
macOS, and Windows; all required safety and coverage outcomes remain; every
related lifecycle document contains consistent evidence; and no unauthorized
repository action was performed.

## Execution log

Append one concise row only after a milestone is independently approved and
verified. Detailed active, remediation, resume, and blocked evidence belongs
only in this plan's durable run ledger.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
