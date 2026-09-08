# Workspace name shorthand implementation plan

Status: implemented
Source specification: [Workspace name shorthand specification](../spec/workspace-name-shorthand.md)
Source of truth: [Selection](../spec/workspace-name-shorthand.md#2-inputs-and-selection), [eligibility](../spec/workspace-name-shorthand.md#3-removed-workspace-eligibility), [checkout](../spec/workspace-name-shorthand.md#4-checkout-resolution-and-execution), and [output contracts](../spec/workspace-name-shorthand.md#5-output-and-error-contract); [workspace inventory and checkout preparation](../../internal/service/workspace.go); [transaction revalidation](../../internal/service/transaction.go); [CLI workspace commands](../../internal/cli/workspace.go); [status command](../../internal/cli/status.go); [error rendering](../../internal/render/render.go); [verification targets](../../Makefile)
Delivery style: test-first, one reviewed milestone at a time

## Execution contract for Codex

This document plans implementation; its creation does not start a plan run.
When authorized to run it, continue through M00–M03 without pausing between
milestones. Follow [milestone supervision](../ai/milestone-supervision.md) and
the [run-ledger layout](../ai/run-ledger-layout.md), which are normative.
Lifecycle `Status: initial` follows [AGENTS.md](../../AGENTS.md), irrespective
of the older readiness labels in plan-authoring guidance.

1. Read the specification, plan, relevant source, worktree changes, and any
   existing run ledger. Create `docs/ai/runs/workspace-name-shorthand.md` only
   when execution is authorized, before the first dispatch. On resumption,
   reconcile recorded evidence with the filesystem and append a checkpoint.
2. The main agent records every scope item, test-first slice, exit criterion,
   documentation obligation, and check in the current milestone checklist.
   Capture the starting commit for changed-area verification in the ledger.
3. Dispatch the complete bounded packet to `implementer`, with explicit file
   ownership, authoritative decisions, and RED → GREEN → REFACTOR evidence
   requirements. Preserve unrelated edits and coordinate shared files.
4. Treat partial work as progress. Send only complete submissions to the
   read-only `reviewer`, which independently inspects the shared filesystem,
   specification, scope, negative tests, portability, and verification evidence.
5. Record the complete stable-ID finding set. Remediate all unresolved findings
   in one packet. Initial review rejection is not a remediation attempt; only
   rejection of a complete remediation increments the counter. Use normal
   `implementer` at attempts 0/1 and `escalation-implementer` at attempts 2.
   The normal reviewer reviews the third and final complete remediation.
6. Use `escalation-reviewer` only for the bounded adjudication triggers in the
   supervision document, never as routine extra review. It cannot reset or
   increment remediation attempts.
7. After approval, the main agent runs the required checks, records evidence,
   updates documentation and the overview, checks the milestone, and appends
   its execution-log row. Immediately replace the current ledger snapshot with
   the next milestone checklist and dispatch its initial packet.
8. Do not end an active run for ordinary test failures, findings, partial work,
   or milestone transitions. Stop only after the third rejected complete
   remediation or a concrete external blocker that cannot be safely resolved
   within authorized scope. Record evidence and safe continuation conditions.

Before a final response during execution, apply this gate verbatim:

```text
[ ] Read the current durable run ledger.
[ ] Verify `Final response permitted: yes`.
[ ] Verify every milestone is approved, or valid blocking evidence is recorded.
[ ] Otherwise perform the exact `Resume from` action.
```

Creating this plan authorizes no implementation, commit, push, PR, publication,
or release. During a later authorized implementation run, local test/build
artifacts are in scope; Git publication still needs separate authorization.
Never edit another run's ledger. A finding already required by this plan joins
the current checklist; a material expansion beyond it requires user direction
and does not count as a rejected remediation.

## Fixed implementation decisions

1. Add command-local `--exact` to `path`, `status`, and `checkout` only. Default
   mode searches full logical workspace names using literal case-sensitive
   substring containment, including names identical to the query without
   priority. No chooser, `--match`, configuration, or TTY-dependent behavior.
2. Preserve `FindWorkspace` and `RequireWorkspace` as exact helpers for existing
   consumers. Add a dedicated service selector with explicit mode and command
   eligibility policy, returning a resolved workspace rather than a string.
3. Exact mode supports full names and persisted IDs, with existing conflicts.
   Empty explicit input is invalid; omitted `status` keeps current-workspace
   behavior. Names are never matched against filesystem or Git branch aliases.
4. For explicit `path`/`status`, exclude states proved absent across all recorded
   checkout paths and Git registrations in either mode. Retain damaged and
   partially missing candidates. Unknown observation fails closed. Checkout
   includes removed states without performing an absence filter. No schema
   change or cleanup is needed.
5. Path additionally validates the selected logical root as an accessible
   directory, rejecting a root symlink or wrong node type. This is not a full
   health check; damaged checkout detail belongs to `status`/`doctor`.
6. Default unmatched checkout fails. `checkout --exact <branch>` preserves
   existing local-branch checkout when exact workspace state does not exist.
   Never create/fetch a branch or relax partial/detached/companion validation.
7. Freeze canonical target identity and selected state generation before
   planning. Add an internal optional checkout precondition for selected state
   or exact-branch state absence. Check it before registry publication and
   under the existing project mutation lock before worktree effects. Do not
   repeat substring selection inside the planner or transaction.
8. Use existing `workspace_not_found`/exit 4 and `conflict`/exit 8. Add optional
   `error.selection` to the existing JSON error envelope exactly as specified;
   retain all other output shapes, exits, and schema versions. Human errors
   remain process-boundary stderr output; successful path remains one line.
9. Use existing dependencies, local Git adapter facts, filesystem abstractions,
   and platform path comparisons. No new persistent removal state, network
   activity, locking scheme, plugin, or terminal dependency.
10. Adapt current executable examples and tests deliberately: unregistered
    branch checkout gains `--exact`; matching tests keep default invocation.
    Do not hide all regressions by mechanically adding `--exact` everywhere.

## Stable contracts to establish early

| Owner | Contract and consumers | Enforcement |
|---|---|---|
| `internal/service` selector | CLI passes project/data directory, query, substring/exact mode, and path/status/checkout policy; service returns canonical workspace or typed lookup failure | M00 table tests and bounded real worktree fixtures |
| `internal/service` eligibility observation | Absence requires all recorded paths absent and no matching Git registrations; nonexistence, damage, and observation errors are distinct | M00 filesystem/Git fakes plus remove/manual-removal/partial fixtures |
| `internal/service` selection error detail | Query, mode, sorted candidate ID/name records; zero-match candidates are an empty slice | M00 service assertions; M01 JSON structural assertions |
| `internal/render` error envelope | Optional `selection` only on lookup failures; rollback/setup/unrelated error shapes preserved | M01 renderer and executable process tests |
| `internal/service` checkout boundary | Canonical name plus selected state/absence precondition; normal planner and transaction remain effect owners | M02 injected state-change, lock, dry-run, and rollback tests |
| `internal/cli` command allowlist | Only the three commands invoke the selector; exact helpers retain their meaning everywhere else | M01/M02 excluded-command regression tests |

No persisted schema migration accompanies these contracts. Additive Go types
must remain internal; normal service callers keep explicit exact requests.
Selection cannot grant authorization to mutate or bypass normal preflight.

## Architecture and dependency boundaries

```text
CLI flags and argument validation
  -> service workspace selector
       -> existing validated workspace inventory
       -> local filesystem and Git worktree observations
  -> existing status/path handling or checkout plan and transaction

service typed selection error -> render JSON detail / process human error
```

The service layer owns selection and eligibility, not output streams or Cobra
flags. The renderer owns JSON serialization. `cmd/wtree` retains human stderr
and exit handling. Leave the planner free of substring semantics.

Expected implementation seams are `internal/service/workspace_selection.go`
and its tests, existing `workspace.go`, `create.go`, `transaction.go`, and
`resolve.go` for narrowly scoped checkout precondition plumbing, plus CLI
`workspace.go`, `status.go`, `root.go`, `howto.go`, render tests, and process
tests. Reuse `ListWorktrees`, `sameCheckoutPath`, and missing-leaf
canonicalization where their contracts fit; do not weaken path comparison to
raw string equality. Cache observation only within one invocation.

Documentation owners are README, affected tutorial prose/runners, the source
idea/specification/plan, and the status overview. Existing specifications may
receive a focused link to this extension when needed, with lifecycle metadata
maintained; historical execution ledgers remain untouched.

## Global definition of done

Every approved milestone has meaningful RED/GREEN evidence for its behaviors,
independent reviewer approval with no unresolved material findings, and
main-agent verification against the same source tree. Tests use isolated temp
directories, repository fixtures, controlled Git configuration, and injected
observation errors; they must not depend on user workspaces or credentials.

Run the milestone's focused commands while iterating. Every complete submission
and main-agent approval must also include:

- `make check-local` for formatting, vet, short suite, bounded integrations,
  runner contracts, and build.
- `make test-changed BASE_REF=<captured-start-commit>` for normal changed-area
  coverage, replacing the placeholder with the actual ledger commit.
- `git diff --check` for all pending whitespace defects.
- The milestone's explicitly named focused race check. Service/CLI race
  coverage is selected because this change crosses filesystem observation,
  Git processes, cancellation, and locked checkout revalidation boundaries.

Retain reusable results only when source, flags, environment, mode, and test
inventory are materially unchanged. Tests that conflict with the new behavior
must be amended with a documented reason while retaining their original safety
intent. Public behavior and its help/examples ship within the same milestone.

M03 requires a reviewer-approved frozen candidate with `make test-full`, then
terminal `make test-full-race`, `make tutorial-test`, and `make release-test`.
Record exact source identity and evidence. No test waiver is implicit; an
exception needs explicit user authorization in this plan and its run ledger.

The existing [CI workflow](../../.github/workflows/ci.yml) must pass for the
delivered source on `ubuntu-latest`, `macos-latest`, and `windows-latest`,
including normal/race tests, formatting, vet, build, and release layout. Native
Windows path semantics cannot be proved by cross-compilation alone. CI has
push/PR triggers and no manual trigger. If publication is not authorized and
there is no matching run, complete local work first, then record that concrete
external blocker and the publication/matching-CI continuation requirement;
do not check M03 or mark the plan implemented on local evidence alone.

## Milestones

### [x] M00 — Establish selection and removed-workspace observation

Specification coverage: [sections 2–3](../spec/workspace-name-shorthand.md#2-inputs-and-selection), AC01–AC03.

Scope:

- Add the service selector, typed result/failure detail, explicit modes and
  policies, and deterministic candidate ordering. Preserve exact helpers.
- Add read-only absence observation using recorded checkout paths and cached
  Git lists; distinguish removed, damaged, partial, and uncertain candidates.
- Provide fake observation seams and real nested/forest worktree fixtures.
  Document internal contracts in code; no public command changes yet.

Test-first slices:

1. Table tests prove unique prefix/middle/suffix, literal punctuation, case
   differences, identical-name overlap, no-match, empty input, ID exclusion,
   exact ID/name resolution, cross-identity conflict, and stable ordering.
2. Real fixtures prove normal removal with retained state is excluded only by
   path/status policies, including a leftover forest grouping root. Prove a
   present partial import ignores intentionally omitted repositories.
3. Inject all-missing-but-still-registered, one-missing, dangling symlink,
   wrong-node, permission/I/O/Git errors, zero recorded checkouts, and malformed
   state. Prove none silently narrows an ambiguous candidate set incorrectly.
4. Verify platform-aware registration matching, including missing leaves under
   canonical ancestor aliases and Windows case handling. Snapshot persistent
   state and Git refs/config to prove selection has no writes or network calls.

Verification:

- `go test ./internal/service -run '^TestWorkspaceSelection' -count=1`
- `go test -race ./internal/service -run '^TestWorkspaceSelection' -count=1`
- Global complete-submission checks.

Exit criteria: AC01–AC03 service behaviors are evidenced; existing exact lookup
tests still pass; no public command has changed and no state schema changed.

### [x] M01 — Deliver shorthand for path and status with deterministic errors

Specification coverage: [sections 2–3](../spec/workspace-name-shorthand.md#2-inputs-and-selection), [section 5](../spec/workspace-name-shorthand.md#5-output-and-error-contract), AC01–AC04 and AC07.

Scope:

- Wire the selector into explicit path/status arguments and add their local
  `--exact` flags. Preserve implicit current status and read-only resolution.
- Add selected-root validation for path and optional JSON selection detail in
  the shared renderer without changing unrelated errors.
- Update help, embedded how-to, README, and path/status tutorial examples.
  Add excluded-command regressions for destructive lookup and unsupported flags.

Test-first slices:

1. Exercise default shorthand and exact names/IDs through the CLI; assert
   `--exact=false`, omitted status, empty arguments, project scoping, removed
   exclusion, partial status, and root validation behavior.
2. At the executable boundary, prove human stderr-only lookup errors and exact
   exit codes; stdin EOF must not alter selection or cause a prompt. Assert
   stdout is exactly one path on success and empty on human failure.
3. Decode JSON ambiguity/no-match and assert candidates, order, mode, single
   document, empty stderr, and unchanged rollback/setup/unrelated error shapes.
   Test escaped names and ensure `--json` does not imply exact selection.
4. Supply a unique substring to `remove` and `delete` and prove rejection with
   worktrees/branches/state unchanged. Assert excluded commands reject `--exact`
   and retained exact workflows still pass. Update existing tests only where
   the documented path/status behavior deliberately changes.

Verification:

- `go test ./internal/cli ./internal/render ./cmd/wtree -run 'Test(WorkspaceSelection|Execute(Path|Status|Remove|Delete)|.*(Help|HowTo|JSONError))' -count=1`
- `go test -race ./internal/cli ./cmd/wtree -run '^TestWorkspaceSelection' -count=1`
- Global complete-submission checks.

Exit criteria: path/status and their docs obey the specification, errors are
machine-safe, and destructive command selection remains exact. Checkout still
uses its existing interface until M02.

### [x] M02 — Restore canonical workspace targets through checkout

Specification coverage: [section 4](../spec/workspace-name-shorthand.md#4-checkout-resolution-and-execution), AC02, AC04–AC07.

Scope:

- Add checkout default matching and `--exact`; distinguish selected workspace
  from exact branch-only targets. Normalize IDs to full workspace names.
- Carry selected-state generation or exact-state absence preconditions into
  reconciliation and locked transaction revalidation; preserve lock ordering,
  retained mounts, existing planner validation, and rollback ownership.
- Update checkout help/how-to, README, tutorial prose/runners and affected
  acceptance fixtures for explicit unregistered-branch checkout.

Test-first slices:

1. Create/remove a multi-repository workspace with custom retained mounts,
   restore it by substring, and compare dry-run and execution canonical names,
   root, branches, and mounts with exact selection. Include a forest and a
   companion fixture, plus existing override and partial/detached failures.
2. Default zero-match fails even with a local branch of that name; `--exact`
   checks out that existing branch. Missing branches and multiple workspace
   matches fail before reconciliation or worktree/state effects, in human,
   JSON, dry-run, and verbose modes.
3. Inject selected-state removal/replacement and exact-branch state appearance
   before reconciliation and locked execution. Assert conflict, no retargeting,
   no substring branch creation, and no worktree effects from stale authority.
4. Exercise existing rollback/recovery and lock/journal failures. Confirm no
   change to destructive helpers or branch creation policy. Demonstrate all
   altered tutorial examples still test their original preflight/safety intent.

Verification:

- `go test ./internal/service ./internal/cli -run 'Test(WorkspaceSelection|.*Checkout|.*Transaction)' -count=1`
- `go test -race ./internal/service ./internal/cli -run '^TestWorkspaceSelection' -count=1`
- `make tutorial-test`
- Global complete-submission checks.

Exit criteria: checkout resolves once to the canonical target, preserves exact
branch-only access, rejects stale authority without retargeting, and all three
commands and their public examples agree with the specification.

### [x] M03 — Verify complete acceptance and close lifecycle documentation

Specification coverage: [section 6](../spec/workspace-name-shorthand.md#6-delivery-and-acceptance), AC01–AC08.

Scope:

- Consolidate black-box acceptance across all three commands, exact overrides,
  removed/partial/damaged states, errors, and excluded destructive commands.
- Audit help/how-to/tutorial consistency and add an acceptance-evidence table
  mapping AC01–AC08 to concrete tests and recorded command/CI results in this
  plan. Update focused contract links where needed, not historical ledgers.
- Run final local and platform gates. Resolve all in-scope regressions before
  lifecycle completion; preserve unrelated user edits.

Test-first slices:

1. Add any missing cross-command acceptance scenario before remediation;
   prove shorthand path/status/removal/checkout progression using isolated
   real repositories and the executable output boundary.
2. Audit selectors in executable tutorials, help tests, and existing negative
   fixtures. Add a failing contract assertion for any uncovered discrepancy,
   then fix the behavior/example and rerun its owning test.
3. Validate native platform path and absent-registration cases through the
   existing CI matrix; fix any newly exposed in-scope portability regression
   with an owning test before rerunning affected and terminal checks.

Verification:

- `go test ./internal/service ./internal/cli ./internal/render ./cmd/wtree -run '^TestWorkspaceSelection' -count=1`
- `go test -race ./internal/service ./internal/cli ./cmd/wtree -run '^TestWorkspaceSelection' -count=1`
- Global complete-submission checks.
- Frozen candidate: `make test-full`.
- Terminal: `make test-full-race`, `make tutorial-test`, `make release-test`.
- Matching-source Ubuntu/macOS/Windows CI evidence described in the global
  definition of done; `git diff --check` and lifecycle/link consistency audit.

Exit criteria: all acceptance criteria have concrete test/evidence mappings,
all required local and matching-source CI gates pass, and independent review
has no unresolved material findings. Then mark M03 checked, this plan and the
specification `implemented`, retain the idea as `specified`, update the status
overview, and complete this run's ledger under its final-response gate.

## Acceptance evidence

The [run ledger](../ai/runs/workspace-name-shorthand.md) records source identities,
exact commands, independent reviews, and the local evidence directory. All
milestones are approved. The final implementation and test source is commit
`04f5a89b640f29a9d88907c11bb1edfed0cb7813`, source digest
`7fb2991aaa0ec192a15535ce2f2b59275f05249b0e6934210b74110c05f290aa`.
Independent normal review approved the complete candidate and the test-only
Windows corrections with no material findings. Main-agent verification passed
owning and focused normal/race tests, `make check-local`, changed-area tests
with the recorded 30-minute package bound, `make test-full` (845 service
targets, 9m11.274s), `make test-full-race` (845 targets, 10m25.683s),
`make tutorial-test`, `make release-test`, and `git diff --check`.
Exact commands and exit statuses are in `main-m03-ci-0.log` through
`main-m03-ci-10.log`; `m03-ci-main-verified.json` records the source manifest.
Matching-source [push CI](https://github.com/definebusiness/wtree/actions/runs/34178035627)
and [PR CI](https://github.com/definebusiness/wtree/actions/runs/34178037716)
passed on Ubuntu, macOS, and native Windows, including normal/race tests,
formatting, vet, build, release layout, safe directory reuse, and manifest checks.

`TestWorkspaceSelectionExecutableProgressionAcrossPathStatusRemoveAndCheckout`
adds a real-repository command-boundary progression for AC01–AC05: shorthand
and exact inspection, exact-only removal, removed-state exclusion, canonical
shorthand restoration, and restored inspection. A temporary equality matcher
caused the shorthand path assertion to fail; restoring substring matching
passed (`m03-progression-semantic-red.log` and `-green.log`).

| Criterion | Concrete acceptance tests | Recorded verification |
|---|---|---|
| AC01 | `TestWorkspaceSelectionMatchingAndDetails`; `TestWorkspaceSelectionPathAndExplicitStatus` | M00/M01 focused normal/race and changed-area gates passed; M03 focused/global/frozen full normal gates passed; main focused, full normal/race, and terminal verification passed. |
| AC02 | `TestWorkspaceSelectionMatchingAndDetails`; `TestWorkspaceSelectionPathAndExplicitStatus`; `TestWorkspaceSelectionCheckoutCanonicalShorthandAndExactID`; `TestWorkspaceSelectionCheckoutEmptySelectorBeatsCorruptInventory` | M00–M02 independently approved; M02 R1 corrupt-inventory empty-selector RED/GREEN and `main-m02-0..5.log` passed. |
| AC03 | `TestWorkspaceSelectionRemovedWorkspacePoliciesAndObservation`; `TestWorkspaceSelectionRetainsPersistedPartialAndDamagedCandidates`; `TestWorkspaceSelectionRealForestRemovalIsReadOnly`; `TestWorkspaceSelectionUncertainObservationsDoNotNarrowCandidates`; `TestWorkspaceSelectionPlatformPathComparison` | M00 corrected candidate-observation and read-only snapshots approved; M02 focused normal/race passed. Native Ubuntu/macOS/Windows normal and race matrix passed at `04f5a89`. |
| AC04 | `TestWorkspaceSelectionErrorsStayAtTheProcessBoundary`; `TestWorkspaceSelectionEOFProcessKeepsScalarPathOutput`; `TestWorkspaceSelectionPathAndExplicitStatus`; `TestJSONErrorIncludesOnlyTypedWorkspaceSelectionDetails` and stable-envelope tests | M01 process-boundary/JSON review and required gates passed; M03 cross-command acceptance and frozen full normal suite passed. |
| AC05 | `TestWorkspaceSelectionCheckoutCanonicalShorthandAndExactID`; `TestWorkspaceSelectionCheckoutRestoresForestCompanionCanonicalTarget`; `TestWorkspaceSelectionCheckoutRestoresPlainMultiTopLevelForest`; `TestWorkspaceSelectionCheckoutOverlayRetainsUnspecifiedMounts` | M02 normal/race, tutorial, local, and changed-area gates passed; `main-m02-0..5.log`. |
| AC06 | `TestWorkspaceSelectionCheckoutPreconditions`; `TestWorkspaceSelectionCheckoutExactBranchAbsencePrecondition`; `TestWorkspaceSelectionCheckoutExactBranchAppearanceUnderLock`; `TestWorkspaceSelectionCheckoutLookupFailuresHaveNoEffectsAcrossModes`; `TestWorkspaceSelectionCheckoutRefusesPersistedPartialDetachedAndDivergentStateWithoutMutation` | M02 locked-precondition mutation RED, restored GREEN, normal/race/transaction gates and independent review passed. |
| AC07 | `TestWorkspaceSelectionExactFlagAllowlist`; destructive no-effect cases in `TestWorkspaceSelectionPathAndExplicitStatus` | M01 complete excluded-command matrix and destructive snapshots approved; M02 checkout-only allowlist extension verified. |
| AC08 | `TestDetailedCommandHelpAndUnsupportedOptionMatrix`, existing how-to tests and executable tutorial runners; M03 consistency audit and final gates | M02 tutorials passed; M03 focused normal/race and frozen full normal passed; main terminal full-race, release, and final tutorial gates passed. Matching-source Ubuntu/macOS/Windows push and PR CI passed at `04f5a89`; see links above. |

## Delivery and portability verification

The reviewed implementation is published in
[PR #5](https://github.com/definebusiness/wtree/pull/5), from
`feat/workspace-match` to `main`. Native Windows exposed two test-fixture issues:
a quoted physical path and scheduling-dependent clone publication contention.
The independently reviewed correction uses portable paths while preserving
quoted logical-name escaping assertions, and proves concurrent remote callbacks
before releasing their final publications in order. Production behavior and
production timeouts are unchanged. All local and native gates passed on the
corrected source. The final lifecycle-only documentation update retains these
results because implementation, tests, workflows, flags, and test inventory are
unchanged. Unrelated worktree edits are preserved; the PR is not merged.

## Execution log

Approved milestones are recorded below. Detailed active state and findings
belong in this plan's [durable run ledger](../ai/runs/workspace-name-shorthand.md).

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-09-07 | M00 | Focused normal/race, exact regression, check-local, changed-area (30m package bound), diff check passed; see durable ledger | Independent normal reviewer approved; material findings resolved | Uncommitted; base `ef8e14e` |
| 2026-09-07 | M01 | Focused normal/race, exact regression, check-local, changed-area (30m package bound), diff check passed; see durable ledger | Independent normal reviewer approved; material findings resolved | Uncommitted; base `ef8e14e` |
| 2026-09-07 | M02 | Focused normal/race, tutorial, check-local, changed-area (30m package bound), diff check passed; see durable ledger | Independent normal reviewer approved; material findings resolved | Uncommitted; base `ef8e14e` |
| 2026-09-08 | M03 | Main owning/focused normal/race, local/changed, full normal/race (845 service targets each), tutorial, release, diff and lifecycle checks passed; matching-source push 34178035627 and PR 34178037716 passed Ubuntu/macOS/Windows | Independent normal reviewers approved complete acceptance and Windows test-only correction; no unresolved findings | `04f5a89` implementation/test source; final documentation-only closure retains verified source |
