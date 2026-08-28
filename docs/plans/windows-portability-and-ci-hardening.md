# Windows portability and CI hardening implementation plan

Status: initial
Source specification: [Windows portability and CI hardening specification](../spec/windows-portability-and-ci-hardening.md)
Implementation context: [Investigation and porting context dump](windows-portability-and-ci-hardening-context.md)
Related implemented work: [Automatic nested mount ignore protection implementation plan](automatic-nested-mount-ignore-protection.md)
Source branch: `codex/automatic-nested-mount-ignore-protection-ci`, candidate follow-ups `c5f6b51` through `ed1365c`
Source of truth: [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml); [`internal/fsutil/atomic.go`](../../internal/fsutil/atomic.go); platform sync files under [`internal/fsutil`](../../internal/fsutil); [`internal/service/clone_execute.go`](../../internal/service/clone_execute.go); [`internal/service/clone_safety.go`](../../internal/service/clone_safety.go); [`internal/service/create.go`](../../internal/service/create.go); [`internal/config/paths.go`](../../internal/config/paths.go); current platform and end-to-end tests
Delivery style: behavior-first selective port, test-first, one independently reviewed milestone at a time; no wholesale merge/cherry-pick, schema change, dependency addition, root `.gitignore` edit, commit, push, publication, or release

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not stop at milestone
boundaries or ask for routine implementation choices fixed by this plan.

For each unchecked milestone, in order:

1. Read this plan, the source specification, the implementation context dump,
   the named source commits, current source-of-truth files, the current
   worktree, and the durable ledger at
   `docs/ai/runs/windows-portability-and-ci-hardening.md`. Create the ledger
   before the first implementation dispatch. On resumption, reconcile its
   latest checkpoint with the filesystem before doing more work.
2. Record a complete milestone checklist in the ledger, including every scope
   item, test-first slice, documentation requirement, exit criterion, and
   verification command.
3. Give the full initial packet to the normal `implementer`, requiring RED →
   GREEN → REFACTOR evidence, changed files, command results, and unresolved
   concerns. Follow the implementation-tier rules in
   [`milestone-supervision.md`](../ai/milestone-supervision.md).
4. Treat partial work as progress only. Send a submission to the read-only
   `reviewer` only after every checklist item has evidence.
5. Record all review findings with stable IDs and return the complete
   unresolved set in one remediation packet. Apply the exact three-rejected-
   complete-remediation limit; do not use escalation review routinely.
6. After reviewer approval, run verification as the main agent, update current
   contracts and documentation, check the milestone, append one execution-log
   row, initialize the next milestone checkpoint, and dispatch it immediately.

Preserve unrelated worktree changes and never edit another plan's durable run
ledger. A final response is permitted only by the durable-ledger gate in
`milestone-supervision.md`.

## Fixed implementation decisions

### Port behavior, not branch topology

- Begin with a current-main gap matrix for every candidate commit. Record the
  exact production behavior, relevant tests, present-day owner, and decision:
  port, already satisfied, obsolete, or excluded.
- Use `git show` and focused diffs as historical evidence. Do not merge the
  branch, rebase `main` onto it, or cherry-pick a commit whose patch also
  carries obsolete clone/docs/test behavior.
- Reproduce missing behavior against current `main` before production changes.
  A platform-specific contract that cannot fail on the local host may use a
  platform test or injected system-call seam, but its RED evidence must still
  demonstrate the missing contract.
- Keep newer live-branch clone, logical-root, forest, tutorial, and public
  output behavior. Adapt source tests to current models instead of restoring
  removed fields or algorithms.
- The old branch's deletion of `.DS_Store` from the root `.gitignore` is
  unrelated and must not be ported.

### Atomic publication and Windows capability boundary

- Keep regular-file flush-before-rename and Unix directory sync behavior.
- Introduce a platform-owned directory publication boundary if the gap audit
  confirms current Windows code still attempts unsupported directory flushes.
  On Windows, validate open/stat/close of the directory without treating the
  lack of directory `FlushFileBuffers` as publication failure.
- Preserve injected `dir-sync` failure semantics and distinguish failures
  before replacement from failures after replacement.
- Propagate genuine directory open, type, stat, and close errors. Do not
  convert all Windows errors to success.
- Retain compare-and-replace generation checks, symlink/type rejection,
  requested mode behavior, umask behavior, and temporary cleanup.

### Identity, permissions, and paths

- Use `os.SameFile` or the current repository identity abstraction when safety
  is about an existing filesystem object. Use string equality only when the
  public contract preserves configured spelling.
- Prime Windows file identity before a path-owning rename/quarantine only at
  boundaries that retain a pre-rename `FileInfo` for post-rename comparison.
- Reconcile Windows rename metadata narrowly and retain detection of identity,
  type, content, permission, size, timestamp, parent, and concurrent changes
  supported by the current contract.
- Validate private staging and cleanup against the actual parent identity.
  Never weaken absolute/clean path, symlink, ownership, inventory, or recovery
  checks.
- Prefer native `t.TempDir()` and `filepath` construction in Windows tests.
  Preserve configured values verbatim where required and compare emitted
  existing paths by filesystem identity where spelling may vary.

### CI execution and diagnostics

- Configure Windows LF checkout before `actions/checkout` and format-check
  tracked Go files using NUL-delimited enumeration.
- Use explicit normal/race timeouts on every platform. Partition Windows only
  where needed to bound slow service test binaries; do not omit packages or
  tests.
- Build a deterministic partition helper with fail-closed inventory,
  disjoint/exhaustive shard assignment, accumulated failures, correct pipeline
  status, temporary-log cleanup, and escaped GitHub annotations.
- Test the helper's logic without requiring a live Actions runner. Use
  controlled fake commands or a dry-run/inventory mode rather than assertions
  that merely duplicate its implementation.
- Keep the existing matrix and all current vet, build, release, tutorial, and
  repository checks. CI runtime improvements must not reduce coverage.

## Architecture and dependency boundaries

```text
atomic caller ──→ fsutil compare/replace ──→ platform directory boundary
                                                ├── Unix: directory sync
                                                └── Windows: validate handle lifecycle

clone/create safety ──→ filesystem identity + current transaction ownership

GitHub Actions ──→ portable format/vet/build gates
       └─────────→ bounded test runner ──→ complete normal/race inventory
```

- `internal/fsutil` owns atomic file publication and OS-specific sync
  capability decisions. Service packages must not duplicate OS syscalls.
- `internal/service` owns clone/create path ownership, identity, rollback, and
  recovery policy. It consumes filesystem facts without redefining atomic
  publication.
- `internal/config` owns configured-path semantics. Tests must not force
  canonicalization that the public contract does not require.
- `.github/workflows` owns matrix orchestration; `scripts` may own a reusable
  test runner but not product behavior.

## Global definition of done

- Every production behavior change has recorded RED → GREEN → REFACTOR
  evidence using focused current-main tests.
- The candidate-commit gap matrix is complete and proves why excluded or
  obsolete patches were not applied.
- Windows-specific files compile for Windows, and native Windows tests cover
  real directory handles, file identity, path spelling, requested permission
  behavior, rename metadata, cleanup, and rollback as applicable.
- The CI helper has deterministic tests for inventory, exact-once assignment,
  normal/race argument construction, failure accumulation, pipeline statuses,
  annotations, temporary cleanup, and invalid invocation.
- Automatic nested mount ignores, live-branch clone, logical roots, repository
  forests, configuration, registry, workspace, recovery, transaction,
  tutorial, and public output regressions remain covered.
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

- Run any new CI-helper test command documented by M02.
- A matching GitHub Actions run passes on Ubuntu, macOS, and Windows. If
  starting that run requires unauthorized commit/push/PR activity, record an
  external blocker and do not mark M03, the plan, or specification implemented.
- Current specification, plan, workflow comments, contributor-facing CI
  guidance, lifecycle overview, and implementation evidence agree.
- Independent review has no unresolved material findings.

## Risk and rollout

- Windows filesystem semantics cannot be proven by Unix-only mocks. Cross-
  compilation catches build-tag errors; the Windows matrix remains required
  for runtime acceptance.
- Treating directory flush errors too broadly as unsupported could hide real
  publication failures. The platform boundary must distinguish unsupported
  capability from open/stat/close and injected failures.
- Relaxing Unix-style mode equality on Windows could hide a writable sensitive
  file. Tests must assert the observable protection property instead of
  accepting arbitrary modes.
- File identity may be resolved lazily on Windows. Capture it before rename,
  but do not use identity priming as permission to accept later metadata or
  parent changes.
- Test partitioning can silently lose coverage. Exact-once inventory tests and
  fail-closed runtime checks are release gates.
- A direct cherry-pick could revert newer clone behavior. The gap matrix and
  independent review explicitly reject textual convergence as a goal.
- No feature flag or persisted migration is required. Commits, pushes, pull
  requests, releases, and publication require separate authorization.

## Milestones

### [x] M00 — Port the Windows atomic publication boundary

Specification coverage: [§3](../spec/windows-portability-and-ci-hardening.md#3-windows-atomic-publication) and [§7](../spec/windows-portability-and-ci-hardening.md#7-required-verification).

Scope:

- Produce the complete candidate-commit gap matrix, with special attention to
  `4f42a82` and the current `internal/fsutil` implementation.
- Reproduce the unsupported Windows directory-flush behavior through a native
  Windows test and an injectable cross-platform unit seam.
- Establish the platform-owned directory publication boundary while retaining
  regular-file flushing, Unix directory sync, injected failure semantics,
  compare-and-replace safety, modes, umask, and temporary cleanup.
- Add tests for open/stat/type/close failures and supported/unsupported sync
  behavior without weakening genuine errors.
- Do not change clone/create behavior or CI orchestration in this milestone.

Test-first slices:

1. Add a platform-neutral boundary test that is RED because current atomic
   publication cannot distinguish directory validation from directory sync.
2. Add native Windows tests reproducing directory flush rejection after an
   otherwise successful replacement and proving the final target generation.
3. Cover injected pre/post-replacement failures, non-directory handles,
   open/stat/close failures, existing/missing targets, modes, umask, and temp
   cleanup.
4. Re-run Unix atomic tests to prove directory durability behavior is retained.

Verification:

- `go test ./internal/fsutil -run 'Atomic|Sync|Directory' -count=1`
- `go test ./internal/fsutil -race -run 'Atomic|Sync|Directory' -count=1`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/fsutil`
- Global definition-of-done commands applicable before CI changes.

Exit criteria: Windows atomic replacement no longer fails solely on an
unsupported directory flush, while real failures and the old-or-new target
contract remain enforced on every platform.

### [x] M01 — Adapt Windows identity, permission, and path invariants

Specification coverage: [§4](../spec/windows-portability-and-ci-hardening.md#4-filesystem-identity-modes-and-path-preservation), [§6](../spec/windows-portability-and-ci-hardening.md#6-compatibility-and-safety-boundaries), and [§7](../spec/windows-portability-and-ci-hardening.md#7-required-verification).

Scope:

- Complete the gap-matrix decisions for `fda4874`, `92d1cc1`, `5726c4e`, and
  `ed1365c` against current clone, create, config, domain, store, and CLI code.
- Add native Windows fixtures and identity-aware assertions before changing
  production code.
- Port only still-missing identity priming, parent identity, rename metadata,
  requested-permission, and configured-path behavior.
- Preserve current live-branch clone, logical-root/forest topology, cleanup,
  rollback, recovery, schemas, and output.
- Add regression tests for automatic ignore application and rollback on paths
  whose native spelling differs but filesystem identity is equal.

Test-first slices:

1. Replace pseudo-Windows fixture assumptions with native temp paths and show
   the exact current failures in configuration, workspace, plan, and CLI tests.
2. Exercise pre/post-rename identity on an owned directory and reject parent,
   symlink, type, content, timestamp, and unrelated metadata mutations.
3. Exercise requested sensitive-file writability and atomic replacement modes
   on Windows without requiring impossible Unix-bit equality.
4. Re-run clone/create failure boundaries, rollback/recovery, automatic-ignore
   public E2E, configured-path spelling, and emitted-path identity cases.

Verification:

- `go test ./internal/config ./internal/domain ./internal/store -run 'Path|Windows|Workspace|Config' -count=1`
- `go test ./internal/service -run 'Clone|Create|Identity|Rollback|Ignore|Windows' -count=1`
- `go test ./internal/cli -run 'AutomaticIgnore|Clone|Path|Plan' -count=1`
- `go test ./internal/fsutil ./internal/service -race -count=1`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/service`
- Global definition-of-done commands applicable before CI changes.

Exit criteria: current-main ownership and path invariants work with native
Windows identity and mode semantics without reverting newer behavior or
weakening cleanup and rollback safety.

### [x] M02 — Make CI portable, bounded, and diagnostic

Specification coverage: [§5](../spec/windows-portability-and-ci-hardening.md#5-portable-and-diagnostic-ci) and [§7](../spec/windows-portability-and-ci-hardening.md#7-required-verification).

Scope:

- Complete the gap-matrix decisions for `c5f6b51` through `c86e45b` against
  the current workflow and current test inventory.
- Make checkout and formatting portable, with exact tracked-byte checkout and
  NUL-safe tracked-Go-file enumeration.
- Add explicit suite timeouts and deterministic Windows normal/race
  partitioning without losing test coverage.
- Add fail-closed inventory and exact-once assignment, accumulated failures,
  pipeline-status preservation, temporary cleanup, and escaped annotations.
- Add a deterministic local test harness for the CI helper and document its
  maintenance contract.
- Retain every current matrix, vet, build, release, tutorial, and repository
  quality gate.

Test-first slices:

1. Reproduce formatting enumeration and line-ending failures with controlled
   filenames/bytes, then establish tracked-file and pipeline-error behavior.
2. Test empty/duplicate inventories, disjoint exhaustive assignment, fewer
   tests than shards, and inventory growth without manual shard lists.
3. Test command failure, log transport failure, multiple shard failures,
   timeout/panic/race/compile excerpts, annotation escaping, and temp cleanup.
4. Run normal/race helper modes locally with controlled fake inventory, then
   execute the real non-Windows full suites through their workflow commands.

Verification:

- The new documented CI-helper test command.
- `bash -n scripts/ci-test.sh` if that path remains the selected design.
- `git ls-files -z -- '*.go'` piped through the selected formatting check.
- `go test ./... -count=1`
- `go test -race ./... -count=1`
- Global definition-of-done commands.

Exit criteria: the workflow is portable and bounded, every normal/race test is
assigned exactly once on Windows, and failures remain actionable and fatal.

### [ ] M03 — Verify the integrated port on all supported platforms

Specification coverage: [§§2–7](../spec/windows-portability-and-ci-hardening.md#2-baseline-and-provenance).

Scope:

- Review the final gap matrix and diff to prove every candidate was consciously
  ported, already satisfied, obsolete, or excluded.
- Run focused and full local quality gates, tutorial E2E, cross-compilation,
  and documentation/traceability audits.
- Obtain a matching GitHub Actions run for the exact reviewed tree on Ubuntu,
  macOS, and Windows, including normal/race partitions and all build/release
  gates.
- Fix implementation or CI defects exposed by the matrix within this plan's
  scope; do not stop at ordinary failures.
- Update lifecycle statuses and evidence only after independent approval and
  the matching matrix passes.

Test-first slices:

1. Add any missing integration regression exposed by the combined M00–M02
   diff before repairing the behavior.
2. Run the automatic-ignore public E2E, clone/create/rollback/recovery suites,
   atomic suites, CI-helper tests, and all-command tutorial together.
3. Audit root `.gitignore`, public schemas, current clone behavior, and
   unrelated docs to prove excluded scope remained unchanged.
4. Exercise and diagnose the exact Windows CI path until the matching matrix
   passes without skipped or silently lost test inventory.

Verification:

- All focused commands from M00–M02.
- Every global definition-of-done command.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/fsutil`
- `GOOS=windows GOARCH=amd64 go test -c ./internal/service`
- Matching successful GitHub Actions run URL and job evidence for Ubuntu,
  macOS, and Windows.

Exit criteria: the selective port is fully reviewed and verified on all
supported platforms, all newer `main` contracts remain intact, and the source
branch can be retired without losing required behavior or CI evidence.

## Execution log

Append one concise row only after a milestone is independently approved and
verified. Detailed active, remediation, and resume evidence belongs only in
this plan's durable run ledger.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-27 | M00 | Focused fsutil normal/race and isolated Windows compilation pass; vet, format, build, release, tutorial, and diff gates pass. Full normal/race and `make check` consistently reach only the pre-existing long `internal/service` Git-test timeout while fsutil and all other packages pass; M02 owns bounded suite execution. | Approved after R1 durable-evidence remediation; no material implementation findings remain. | Not committed (not authorized). |
| 2026-08-28 | M01 | Main focused config/domain/store, service (213.141s), CLI (81.642s), fsutil, and staging/rename regressions pass; five affected packages compile for Windows; vet, format, build, release, tutorial, and diff gates pass. Final full/race/check evidence reaches only the established unrelated long service Git-test limit that M02 owns. | Approved after R1–R5 remediation and one rejected complete submission; no material finding remains. Native Windows runtime remains M03. | Not committed (not authorized). |
| 2026-08-28 | M02 | Deterministic helper harness, shell syntax, exact NUL formatting, vet, format, build, release, tutorial, and diff gates pass; full bounded normal/race suites pass with service at 1035.818s/1122.016s. Main reverified the harness, workflow helpers, quality gates, and live 549-target exact-once eight-shard assignment. | Approved on initial submission with no findings; native Windows workflow runtime remains M03. | Not committed (not authorized). |
