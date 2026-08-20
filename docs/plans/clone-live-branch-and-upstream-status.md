# Live-branch clone and upstream-aware human status implementation plan

Status: implemented
Source specification: [Live-branch clone and upstream-aware human status specification](../spec/clone-live-branch-and-upstream-status.md)
Implementation context: [Investigation and product-decision context dump](clone-live-branch-and-upstream-status-context.md)
Related contracts: [Portable manifest clone specification](../spec/portable-manifest-clone.md); [`wtree` specification](../spec/wtree.spec.md); [Full multi-repository experience capability specification](../spec/full-multi-repository-experience.md)
Source of truth: [`internal/git/portable.go`](../../internal/git/portable.go); [`internal/service/clone_plan.go`](../../internal/service/clone_plan.go); [`internal/service/clone_execute.go`](../../internal/service/clone_execute.go); [`internal/service/clone_result.go`](../../internal/service/clone_result.go); [`internal/service/status.go`](../../internal/service/status.go); [`internal/cli/clone.go`](../../internal/cli/clone.go); [`internal/cli/status.go`](../../internal/cli/status.go); [`tutorial/setup-fixture.sh`](../../tutorial/setup-fixture.sh)
Delivery style: test-first, one independently reviewed milestone at a time; no fetch/update/sync command, persisted-schema migration, automatic merge/rebase, repository deletion, dependency addition, commit, push, publication, or release

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes them below.

For each unchecked milestone, in order:

1. Read this plan, the source specification, the implementation context dump,
   the relevant source-of-truth files, and the durable ledger at
   `docs/ai/runs/clone-live-branch-and-upstream-status.md`, and the current
   worktree. Create the ledger before the first implementation dispatch. On
   resumption, reconcile the plan, ledger, referenced evidence, and filesystem
   before dispatching more work.
2. Record a complete active-milestone checklist in the durable ledger. It must
   include every scope item, test-first slice, documentation requirement, exit
   criterion, and verification command from that milestone.
3. Give the complete initial packet to the normal `implementer`. Require RED
   → GREEN → REFACTOR evidence, changed files, command results, and unresolved
   concerns. Use the normal `implementer` for remediations starting with zero
   or one rejected complete submission and the `escalation-implementer` only
   when the ledger already records two.
4. Treat partial work as progress only. Request review only after every
   checklist item has complete evidence.
5. Send every complete submission to the read-only `reviewer`, which inspects
   the current shared filesystem against the full milestone, specification,
   safety, portability, compatibility, and test-quality requirements.
6. Record all material findings with stable IDs and return the entire
   unresolved set in one remediation packet. Apply the exact three-rejected-
   complete-remediation limit from
   [`milestone-supervision.md`](../ai/milestone-supervision.md). Do not use an
   escalation reviewer as a routine second review.
7. After reviewer approval, run the milestone verification as the main agent,
   update affected contracts and documentation, check the milestone, append
   its concise execution-log entry, create the next milestone ledger snapshot,
   and dispatch the next initial packet immediately.

Do not stop for ordinary test failures, reviewer findings, partial submissions,
or approved milestones. Preserve unrelated worktree changes; do not use
destructive cleanup commands; commit only when separately authorized. A final
response is permitted only by the durable-ledger gate in
`milestone-supervision.md`.

## Fixed implementation decisions

### Ordinary clone follows the selected live branch

- Keep read-only remote preflight for every repository so invalid URLs,
  missing selected refs, and aggregate remote failures are found before
  staging or destination mutation.
- Rename the preflight fact in the version-two plan/result model to
  `ObservedCommit`. It is diagnostic evidence only and must never be supplied
  as the execution checkout target.
- Advance `ClonePlanVersion` and `CloneResultVersion` to `2`. Version-two JSON
  renders `observedCommit`; clone actions must not claim `exactCommit` or
  `parentCommit`. Advance the completed clone JSON envelope to version `2` and
  include deterministic per-repository actual checked-out commits. No
  persisted schema changes are authorized.
- Replace the adapter workflow that performs `git branch -f <local> <planned>`
  with a checkout operation that fetches the manifest's selected remote ref,
  establishes the manifest local branch at the fetched tip, configures its
  declared upstream, and suppresses checkout hooks. It must not allow remote
  `HEAD` to create or retain another local branch.
- The implementation may use clone, init/fetch, or another non-shell Git
  sequence, but the Git adapter owns the sequence and its postconditions. It
  must use argument arrays, the hardened non-interactive environment, explicit
  `--` boundaries where applicable, no submodule recursion, and no user or
  repository hook execution.
- Capture the actual checked-out `HEAD` immediately after branch establishment
  and use it for all subsequent identity, parent-ignore, tracked-manifest,
  clean-state, submodule, result, and workspace-state verification.
- Branch movement after planning is accepted. A test seam must advance the
  selected branch after the plan is returned and before its execution fetch;
  successful execution must record the new tip. Deletion or replacement with
  unverifiable content fails without falling back to the observed commit or
  remote `HEAD`.
- A successful repository has exactly one local branch, named by
  `default_branch`, even when the remote symbolic `HEAD` names another branch
  or the local and remote branch names differ. Normal remote-tracking refs are
  not local branches and remain allowed.
- Preserve private staging, parent-first assembly, atomic destination publish,
  registry/state compare-and-swap behavior, exact cleanup ownership,
  rollback/recovery evidence, credential redaction, and cancellation.

### Human status exposes existing upstream facts

- Keep service-owned `Ahead`, `Behind`, and `Upstream` facts and their JSON
  fields unchanged. Do not redefine `RepositoryStatus.Status` or make
  `clean` imply upstream currency.
- Add an `UPSTREAM` column to the deterministic human table while retaining
  the current `REPOSITORY`, `BRANCH`, `MOUNT`, and `STATUS` columns and their
  meanings.
- Render `none`, `up-to-date`, `ahead N`, `behind N`,
  `diverged (ahead N, behind M)`, or `n/a` exactly as specified. Keep counts in
  base-10 without localization.
- Use `n/a` for missing, stale-state, unknown-repository, and detached
  checkouts. For any other attached checkout, use the existing upstream facts,
  including when another structural status is also present.
- Status remains network-free and non-mutating. Do not add a flag that fetches,
  invoke `ls-remote`, or update remote-tracking refs. Help and documentation
  must state that results reflect the last fetch.

### Tutorial and current documentation

- Remove the tutorial fixture's `fixture/clone-bootstrap` branches and remote
  `HEAD` rewrites. Bare origins retain ordinary `refs/heads/main` symbolic
  heads.
- Update tutorial prose and expected branch lists so they no longer teach or
  depend on the workaround or claim that clone creates the transport-default
  branch locally.
- Update clone help, how-to text, README, and current specification
  traceability to describe observed-branch preflight and execution-time branch
  checkout. Do not rewrite historical run ledgers or completed plan execution
  logs.
- Update status help/tutorial examples to distinguish working-tree `STATUS`
  from the last-fetched `UPSTREAM` relationship without claiming that status
  contacts remotes.

## Architecture and dependency boundaries

```text
internal/cli clone ──→ service clone planner ──→ remote observation
         │                     │
         │                     └──→ clone executor ──→ Git live-branch checkout
         │                                              │
         └── versioned human/JSON output ← actual HEAD ─┘

internal/cli status ──→ service status facts ──→ Git local refs only
         │
         └── human STATUS + UPSTREAM rendering
```

- `internal/git` owns Git command construction and parsing. It must not know
  about portable manifests, workspace state, CLI rendering, or JSON versions.
- `internal/service` owns the distinction between observed plan facts and
  actual execution facts, verification, transaction behavior, status facts,
  and public service models.
- `internal/cli` owns column labels, exact human strings, help, and the
  completed-command envelope. It must not re-run Git or infer ahead/behind
  independently.
- `internal/config`, `internal/store`, and `internal/domain` schemas remain
  unchanged. Do not add a second upstream parser or branch-name grammar.

## Stable contracts to establish early

| Contract | Owner and consumers | Invariant and enforcement |
|---|---|---|
| Live selected branch | `internal/git`; consumed by clone executor | Fetch and check out only the manifest-selected ref, create exactly the selected local branch, preserve declared tracking, suppress hooks, and ignore remote `HEAD` for local branch selection. Hermetic adapter tests cover same-name, different-HEAD, and different-local/remote-name cases. |
| Observed versus actual commit | `internal/service`; rendered by CLI | Planning records a version-two observation; execution never pins to it and reports/verifies the actual fetched checkout. Planner/executor tests move and delete refs at the plan/execute seam. |
| Upstream status presentation | service facts consumed by `internal/cli` | Existing JSON facts remain stable; the human table deterministically exposes their relationship without network access. CLI table tests cover every mapping and a no-fetch fake. |

## Global definition of done

- Every changed behavior has recorded RED → GREEN → REFACTOR evidence with
  focused hermetic tests before implementation changes.
- Clone tests use temporary local bare remotes and isolated Git configuration;
  status tests use local repositories or injected fakes and never require
  network access, credentials, or the developer's real registry/configuration.
- Success, branch movement, missing ref, verification failure, cancellation,
  rollback, no-publication, secret redaction, output writer failure, JSON
  versioning, deterministic ordering, and paths with spaces remain covered in
  proportion to the touched boundary.
- Existing portable-manifest v2, nested-mount ignore, repository identity,
  registry, workspace, transaction, recovery, and destination safety tests
  remain green.
- Run `gofmt -w` only on Go files changed by an authorized milestone, followed
  by:

  ```text
  go test ./... -count=1
  go test -race ./... -count=1
  go vet ./...
  make fmt-check
  make build
  make release-test
  make check
  git diff --check
  ```

- Current README, CLI help/how-to, tutorial, focused specification,
  traceability, lifecycle status, and execution evidence agree with delivered
  behavior. Files under `docs/ai/runs/` other than this plan's authorized
  ledger remain untouched.
- Independent review has no unresolved material findings.

## Risk and rollout

- Git portability risk: clone and checkout behavior differs across Git
  versions when remote `HEAD` is unborn, invalid, or aliases the selected
  branch. Mitigate with explicit selected-ref fetching, postcondition checks,
  no reliance on default initial-branch configuration, and hermetic adapter
  tests on supported CI platforms.
- Race risk: a moving branch may advance again after execution fetch. This is
  accepted ordinary branch behavior. Verification and state bind to the
  commit actually checked out; no compatibility or atomic-snapshot claim is
  made.
- Output compatibility risk: exact-commit plan semantics cannot be silently
  changed. Version-two clone plan/result and completed envelopes make the
  change explicit; status JSON stays compatible while the human table gains
  one column.
- Cleanup risk: failures after cloning but before actual-HEAD capture must not
  weaken staging ownership. Existing inventory, identity, compare-and-swap,
  rollback, and recovery tests remain mandatory.
- Tutorial risk: removing `fixture/clone-bootstrap` exposes the exact normal
  path that currently fails. The tutorial E2E is a release gate for the fix.
- No feature flag or persisted migration is required. Release, installation,
  publication, commits, and pushes require separate authorization.

## Milestones

### [x] M00 — Expose upstream drift in human status

Specification coverage: [§4](../spec/clone-live-branch-and-upstream-status.md#4-human-status-upstream-reporting) and [§5](../spec/clone-live-branch-and-upstream-status.md#5-required-verification).

Scope:

- Add the deterministic `UPSTREAM` column and exact mapping while preserving
  existing service facts, JSON, status meanings, repository order, and error
  behavior.
- Cover valid attached branches with no upstream, up-to-date, ahead, behind,
  and divergence, plus detached and unavailable structural cases.
- Update focused status help/tests and current user guidance to say that
  comparisons use the last-fetched upstream and status does not fetch.
- Do not add fetch/update commands, network access, service-side presentation
  strings, or a JSON schema change.

Test-first slices:

1. Extend CLI status tests with a behind clean repository and demonstrate RED
   because the human table says only `clean`; add decoded JSON assertions that
   existing ahead/behind/upstream facts are unchanged.
2. Add table-driven rendering tests for every exact `UPSTREAM` value and for
   simultaneous working-tree/structural status plus upstream drift.
3. Use an injected Git fake or command wrapper to prove status never calls a
   remote fact or fetch operation and leaves refs, index metadata, workspace
   state, and registry bytes unchanged.
4. Exercise deterministic spacing/order, paths and mounts with spaces, JSON
   mode, broken writers, and existing status failure classification.

Verification:

- `go test ./internal/service -run 'Status|AheadBehind' -count=1`
- `go test ./internal/cli -run 'Status' -count=1`
- `go test ./internal/git -run 'AheadBehind|Status' -count=1`
- Global definition-of-done commands.

Exit criteria: human status makes upstream drift visible without fetching or
changing the existing status JSON contract.

### [x] M01 — Establish live selected-branch clone execution

Specification coverage: [§§3.1–3.3](../spec/clone-live-branch-and-upstream-status.md#3-ordinary-clone-branch-semantics) and [§5](../spec/clone-live-branch-and-upstream-status.md#5-required-verification).

Scope:

- Replace the planned-commit force-branch adapter flow with explicit selected-
  ref fetch and live checkout semantics.
- Make remote `HEAD` irrelevant to local branch creation and leave exactly one
  selected local branch with the declared tracking configuration.
- Thread actual checked-out commits through executor verification, nested
  parent-ignore checks, tracked-manifest checks, workspace state, cleanup
  identities, and execution results.
- Preserve hook suppression, no-submodule behavior, hardened Git environment,
  cancellation, secret safety, staging ownership, rollback, and recovery.
- Do not change CLI output or tutorial fixtures in this milestone beyond test
  helpers required to establish the service and adapter contract.

Test-first slices:

1. Reproduce the real failure with a bare remote whose symbolic `HEAD`, remote
   selected branch, and manifest local branch are all `main`; require a clean
   successful checkout with correct tracking and one local branch.
2. Set remote `HEAD` to a different branch and prove it neither selects the
   checkout nor becomes a local branch. Repeat with different manifest local
   and remote branch names.
3. Move the selected ref after planning and before execution fetch; require the
   actual newer tip in `HEAD`, verification, state, and result. Delete the ref
   in the same seam and require complete cleanup without stale-commit fallback.
4. Re-run hostile hook/template/configuration, malformed ref/output,
   cancellation, missing object, verification-failure, nested ignore,
   rollback, and secret-canary tests against the new sequence.

Verification:

- `go test ./internal/git -run 'Clone|CheckoutTrackingBranch|Portable' -count=1`
- `go test ./internal/service -run 'CloneExecute|ClonePlan' -count=1`
- `go test ./internal/git ./internal/service -race -count=1`
- Global definition-of-done commands.

Exit criteria: clone execution follows the selected branch tip fetched during
execution, succeeds for normal `main`/`main`, creates no transport-default
local branch, and verifies and records only actual checkout facts.

### [x] M02 — Version clone output and close public regressions

Specification coverage: [§3.4](../spec/clone-live-branch-and-upstream-status.md#34-planning-and-output-terminology), [§5](../spec/clone-live-branch-and-upstream-status.md#5-required-verification), and [§6](../spec/clone-live-branch-and-upstream-status.md#6-explicit-non-goals).

Scope:

- Introduce clone plan/result version two with observed-commit terminology,
  remove exact-commit execution claims, and expose deterministic actual
  checked-out commits in completed JSON.
- Update clone human dry-run/success text, root and command help, practical
  how-to output, README, current traceability, and relevant specifications so
  all current documentation distinguishes remote observation from execution.
- Remove the tutorial `fixture/clone-bootstrap` workaround and update tutorial
  prose and expected branch state for ordinary remote `main` heads.
- Exercise local and HTTP manifest sources, explicit and default destinations,
  nested repositories, JSON, dry-run, verbose progress, errors, rollback,
  registration, immediate status, and subsequent workspace commands through
  public CLI/process boundaries.
- Preserve every explicit non-goal; do not implement fetch/update/sync,
  release locks, automatic merge/rebase, or manifest-driven deletion.

Test-first slices:

1. Update clone plan/result contract tests to fail against version one and
   exact-commit fields, then establish deterministic version-two observed and
   actual representations with mutation-safe copies and tamper rejection.
2. Add CLI/process tests proving dry-run labels observations, completed JSON
   reports actual heads, errors remain redacted and correctly classified, and
   normal `main`/`main` clone succeeds.
3. Run the tutorial E2E with all bare remote heads left on `main`; assert no
   `fixture/clone-bootstrap` local branch exists and all documented commands
   still work.
4. Audit current documentation and generated/help examples for stale phrases
   such as `exact commit`, immutable branch pinning, or the fixture bootstrap
   branch; retain such text only where it explicitly describes lock/release
   behavior or historical material that must not be rewritten.

Verification:

- `go test ./internal/service -run 'ClonePlan|CloneResult|CloneExecute' -count=1`
- `go test ./internal/cli -run 'Clone|Status|Help|HowTo' -count=1`
- `go test ./cmd/wtree -run 'Clone|Process' -count=1`
- `sh tutorial/run-all-commands.sh`
- Global definition-of-done commands.

Exit criteria: every public clone surface truthfully describes live selected-
branch execution, the normal tutorial path covers `main`/`main`, actual commits
are auditable, and the complete status/clone change passes independent review
and all release gates.

## Execution log

Append one concise row only after a milestone is independently approved and
verified. Detailed active/resume/remediation evidence belongs exclusively in
the durable run ledger.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-20 | M00 | Focused service/CLI/Git, full normal/race, vet, fmt, build, release, check, tutorial, and diff gates passed | Approved after R1–R2 remediation | Not committed |
| 2026-08-20 | M01 | Focused Git/service/race, full normal/race, vet, fmt, build, release, check, tutorial, and diff gates passed | Approved after R1–R3 remediation | Not committed |
| 2026-08-20 | M02 | Focused service/CLI/process, tutorial, full normal/race, vet, fmt, build, release, check, documentation, and diff gates passed | Approved after DOC-M02-001 remediation | Not committed |
