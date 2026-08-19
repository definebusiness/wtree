# Automatic nested mount ignore protection implementation plan

Status: initial
Implementation readiness: ready to execute
Source specification: [Automatic nested mount ignore protection specification](../spec/automatic-nested-mount-ignore-protection.md)
Related idea: [Automatically protect nested repository mounts](../ideas/automatic-nested-mount-ignore-protection.md)
Source of truth: [automatic nested mount ignore protection specification §§1–11](../spec/automatic-nested-mount-ignore-protection.md); [`internal/pathutil/mount.go`](../../internal/pathutil/mount.go); [`internal/domain/project.go`](../../internal/domain/project.go); [`internal/git/adapter.go`](../../internal/git/adapter.go); [`internal/fsutil/atomic.go`](../../internal/fsutil/atomic.go); [`internal/service/init.go`](../../internal/service/init.go); [`internal/service/plan.go`](../../internal/service/plan.go); [`internal/service/create.go`](../../internal/service/create.go); [`internal/service/transaction.go`](../../internal/service/transaction.go); [`internal/transaction/transaction.go`](../../internal/transaction/transaction.go); [`internal/plan/plan.go`](../../internal/plan/plan.go); [`internal/cli/root.go`](../../internal/cli/root.go); [`internal/cli/plan.go`](../../internal/cli/plan.go)
Delivery style: test-first, one reviewed milestone at a time; no staging,
committing, pushing, release publication, or public config, manifest, registry,
workspace-state, recovery-record, or workspace-plan schema change

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes those decisions below.

For each unchecked milestone, in order:

1. Read this plan, the relevant source-of-truth sections, the durable run
   ledger at
   `docs/ai/runs/automatic-nested-mount-ignore-protection.md`, and the current
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
   [`milestone-supervision.md`](../ai/milestone-supervision.md). Do not use
   `escalation-reviewer` as a routine second review.
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

### Product behavior and scope

- Automatic protection is an invariant of `wtree init` and `wtree create`.
  There is no user choice, preparatory command, or opt-in flag.
- Remove the partial `InitRequest.AddIgnore` contract and the public
  `init --add-ignore` flag. Do not register `wtree add-ignore` or add
  `create --add-ignore`. Remove those names from current help, examples,
  tutorials, and diagnostics.
- Do not extend protection to clone, checkout, import, update, sync, remove,
  delete, doctor, or submodule handling. Existing behavior of those operations
  remains outside this plan.
- Reconcile partial code and tests left by the superseded manual design. Reuse
  code only when it satisfies this specification; committed-base checks,
  opt-in behavior, duplicate-command guidance, and source rollback of completed
  ignore updates are not compatibility requirements.

### Canonical mounts and literal rules

- `internal/pathutil` remains the sole owner of mount normalization and
  containment. It must expose one validated normalized slash-separated mount
  value that both path placement and ignore protection consume. The ignore
  generator must never clean or reconstruct the raw input independently.
- Preserve the existing acceptance of legacy runtime mount spellings when the
  central normalizer can map them unambiguously to the path actually used.
  Reject CR, LF, NUL, invalid UTF-8, and any value that cannot become one
  unambiguous Git-ignore line before any mutation.
- For each normalized non-root mount, generate exactly one anchored literal
  directory rule `/<mount>/`. Escape Git-ignore metacharacters and meaningful
  whitespace; retain `/` only as the component separator. The root mount `.`
  never produces a nested-mount rule.
- Workspace plans store and render the normalized effective mount. Default and
  repeated `--mount` inputs pass through the same normalization before path
  resolution, rule generation, JSON, diagnostics, and execution.

### Git-owned ignore evidence

- Add a typed working-tree ignore inspection result in `internal/git` rather
  than reducing `git check-ignore -v --no-index` to an unqualified boolean.
  It records whether the winning pattern ignores or negates the directory and
  the reported source needed for qualification.
- A match qualifies only when its winning source is a file named `.gitignore`
  inside the immediate parent checkout and Git reports the directory ignored.
  A root or deeper `.gitignore` may qualify when its scope governs a
  multi-component mount. `.git/info/exclude`, repository-configured or global
  excludes, an external file, malformed output, and a winning negation do not
  qualify.
- Keep committed-ref inspection used by clone separate. Init and create use
  the actual parent working tree; create planning never reconstructs or
  requires ignore content from the selected base commit.
- Git invocation remains locale-neutral, non-interactive, alias-independent,
  and optional-lock-free. Every checked path includes a trailing directory
  indication and `--no-index` so tracked or not-yet-created mounts are tested
  consistently.

### Ignore planning and file mutation

- `internal/service` owns one reusable ignore requirement/file-plan/apply
  boundary. CLI code only requests planning and renders service-owned results;
  it never parses ignore files, derives rules, or writes files.
- Group all direct-child requirements for one immediate parent into its root
  `.gitignore`, sort files parent-first then by parent repository ID, sort
  rules by child repository ID, and replace each changed file once.
- Planning uses `Lstat`-style non-following inspection and records target
  existence, regular-file type, exact bytes, and mode. Reject symlinks and all
  non-regular types. Containment is checked through the existing symlink-aware
  path utilities before access.
- Preserve every existing byte and line. Append only missing generated lines;
  insert a separator newline only for a non-empty unterminated file; use CRLF
  only for an exclusively CRLF existing file and LF otherwise; preserve an
  existing mode; create a missing file with ordinary non-executable mode under
  the process umask.
- If Git reports the mount visible and the exact generated line is already in
  the owning root file, classify an ignore-rule conflict and do not append a
  duplicate. If a newly appended line remains ineffective, retain the complete
  file, fail verification, and make a retry stop at the same conflict without
  duplication.
- Revalidate the recorded file snapshot immediately before replacement.
  A changed existence, type, bytes, or mode is `conflict`; do not overwrite it.
- Extend the shared `internal/fsutil` same-directory atomic writer as needed so
  the complete content is written, mode is set, the temporary is synced and
  closed, replacement is atomic, and the directory is synced where supported.
  The existing `golang.org/x/sys` module may become a direct dependency only
  if a Windows-native replacement boundary requires it; add no other module.
- Source-checkout writes are monotonic across files. A later failure retains
  completed replacements and reports changed files plus remaining targets.
  Retry always replans from current Git and filesystem facts.

### Init integration

- `wtree init` discovers and validates the complete graph and plans both the
  existing `/.wtree.yml` protection and every nested-mount rule before its
  first write. When they share the root file, make one coalesced replacement.
- Preflight failure writes no ignore file, temporary, lock, configuration,
  manifest, registry entry, or workspace state.
- Execution revalidates and replaces planned source `.gitignore` files,
  verifies every nested source mount with Git, and only then enters the
  existing wtree-owned config/manifest/registry/default-state publication.
- Completed source ignore replacements are excluded from init rollback.
  Existing rollback and incomplete-cleanup rules continue to own only wtree
  configuration, manifest, registry, and workspace-state publication.
- Init dry run performs all discovery, validation, Git inspection, type and
  containment checks, and change planning but creates no lock or temporary.
  Existing JSON `ignoreUpdates` contains deterministic changed files and exact
  added rules only and is `[]` when nothing changes.

### Create planning, execution, and cleanup

- Keep public `plan.WorkspacePlan` at version 1 with its existing repository
  and action schema. Do not add an ignore action, update array, inverse-file
  metadata, or version bump.
- Create planning validates and normalizes all effective mounts and derives a
  service-owned ensure list from existing `parentId`, `mount`, and `path`
  fields. Human dry run renders each parent root `.gitignore` and exact rule;
  JSON remains the unchanged version-one workspace plan.
- Remove the current committed-base ignore prerequisite for mount overrides.
  Planning does not inspect or edit a source `.gitignore` and does not claim
  that an ensure-list entry will necessarily change a future worktree.
- Internally augment execution, not the public plan: after adding a parent
  worktree, inspect all direct-child mounts, atomically coalesce missing rules
  in that parent, verify every direct child with Git, and only then add any
  direct child worktree. Repeat at each depth.
- Track exact ignore changes in an internal create result so human success can
  list only files actually changed. JSON success continues to emit only the
  unchanged workspace plan.
- On rollback, a transaction-created parent worktree may be force-removed when
  it is clean or its only dirty state is the exact automatic `.gitignore`
  result. Any unrelated tracked, staged, or untracked change preserves the
  worktree and flows through the existing rollback-incomplete recovery record
  with exact paths and errors. Source checkouts are never modified by create.

### Errors, output, and authority boundaries

- Preserve the specification's existing error mapping: unrepresentable or
  escaping mounts and unsafe target types are `validation`; stale snapshots,
  changed locked plans, and lock contention are `conflict`; Git execution or
  parse failures are `git`; write/sync/close/replace failures are `internal`;
  failed cleanup uses existing incomplete-cleanup or rollback-incomplete
  contracts.
- Human diagnostics go to stderr. JSON failures remain one error envelope on
  stdout with no human stderr. No new error or result schema is authorized.
- Success and dry-run output reports exact paths/rules deterministically and
  says that wtree neither staged nor committed them. No implementation step
  may stage, commit, push, publish a release, or remove stale rules.
- A reviewer finding outside this specification is recorded for user decision
  rather than expanding a milestone. Missing credentials or services are not
  expected; an irreconcilable platform inability to provide the specified
  atomic replacement is a genuine external blocker only after the repository's
  supported OS facilities and CI evidence are exhausted.

## Stable contracts to establish early

| Contract | Owner and consumers | Observable invariant and enforcement |
|---|---|---|
| Normalized mount | `internal/pathutil`; consumed by domain path resolution, service planning, workspace-plan creation, and ignore generation | One validated slash-separated value determines both the checkout path and literal rule. Unit/fuzz and real-Git round-trip tests reject ambiguous or line-breaking values. |
| Working-tree ignore evidence | `internal/git`; consumed by the service ignore planner and verifier | The result preserves winning ignore/negation and source facts; only an effective in-checkout `.gitignore` qualifies. Real Git fixtures cover root/deeper files, negation, local/global/external excludes, spaces, and Unicode. |
| Ignore file plan | `internal/service`; consumed by init and create | Requirements are grouped and ordered deterministically; each target snapshot, exact new bytes, mode, rules, and changed/already-safe state is immutable until compare-and-replace. Hermetic planning tests enforce no mutation. |
| Durable compare-and-replace | `internal/fsutil` plus the service snapshot boundary; consumed by ignore application and existing durable writers | A successful target is complete old-or-new, existing mode is preserved, missing mode honors umask, stale targets are rejected, and temporaries are cleaned. Failure-injection and supported-OS CI tests enforce it. |
| Init publication phases | `internal/service.Initializer`; consumed by init CLI/rendering | All source mounts verify before wtree-owned publication; source updates are retained while only wtree-owned artifacts roll back. Boundary-failure and retry tests enforce the phase split. |
| Create execution invariant | `internal/service.WorkspaceCreator` and existing transaction runner; consumed by create CLI | A direct child checkout cannot exist before its parent reports that effective mount ignored by a qualifying `.gitignore`; cleanup removes only transaction-owned dirt. Ordered-event, failure, recovery, and real-Git tests enforce it. |
| Public compatibility | `internal/plan`, `internal/cli`, and `internal/render` | Workspace plan stays version 1 and JSON shapes/error streams remain stable; only human ensure/change reporting is added. Decoded JSON and CLI black-box tests enforce it. |

## Architecture and dependency boundaries

```text
CLI parsing/rendering
        ↓
service init/create orchestration → transaction and recovery ownership
        ↓                              ↓
service ignore planner/applier → fsutil atomic replacement
        ↓
pathutil normalized mounts + Git ignore evidence
```

- `internal/pathutil` knows paths and mount representation, not Git patterns,
  repository graphs, files, or CLI output.
- `internal/git` runs and parses Git and returns evidence; it does not decide
  owning files, append rules, or perform filesystem mutation.
- `internal/service` owns repository relationships, generated-rule policy,
  file snapshots, deterministic grouping, phase ordering, results, and error
  classification.
- `internal/fsutil` owns the low-level durable replacement protocol but not
  `.gitignore` content, service errors, or repository semantics.
- `internal/plan` remains ignorant of automatic ignore effects. `internal/cli`
  derives neither patterns nor paths independently and never changes files.
- Reuse the existing transaction, state, and recovery machinery. Extend only
  internal execution steps and recovery evidence; do not create a parallel
  transaction engine or durable schema.

## Global definition of done

Every approved milestone has complete RED → GREEN → REFACTOR evidence for
its changed behavior, focused hermetic tests for success and rejection or
no-mutation paths, independent reviewer approval with no unresolved material
findings, and these main-agent checks unless a milestone explicitly names a
strict subset before later integration exists:

- `gofmt -w` only on files changed by the authorized milestone, followed by
  `make fmt-check`.
- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`
- `make build`
- `make release-test`
- `git diff --check`

Tests use temporary repositories, local remotes, injected adapters, and
temporary filesystem/data roots. They require no credentials or network,
never read or write user Git configuration, and assert bytes, modes, refs,
index state, locks, recovery records, registry data, and workspace state where
the specification requires absence or preservation. The GitHub Actions Linux,
macOS, and Windows jobs must pass; Windows runtime tests, not cross-compilation
alone, provide the platform evidence for existing-file replacement and missing
file creation. No test may weaken unrelated clone, checkout, transaction,
configuration, or state compatibility.

## Risk and rollout boundaries

- This is an immediate behavior replacement, not a feature-flag rollout.
  Current init/create calls become automatic once the complete plan is
  implemented; there is no dual manual/automatic mode.
- The highest-risk boundaries are literal rule correctness, symlink-safe
  target ownership, stale-snapshot rejection, source publication ordering,
  and preservation versus removal of dirty transaction-created worktrees.
  Each is established before or in the first consuming milestone and covered
  by injected failure plus real Git evidence.
- Existing partial manual-design changes may make the starting worktree differ
  from the repository baseline. Execution must reconcile current files and
  preserve unrelated work; it must not restore the superseded design merely to
  obtain a clean diff.
- There is no production deployment, migration, backfill, release, or commit
  authority in this plan. Existing ignore lines are never removed or rewritten.

## Milestones

### [x] M00 — Establish normalized mounts and Git-owned ignore evidence

Specification coverage: [§§2–4](../spec/automatic-nested-mount-ignore-protection.md#2-terms-and-ownership), [§9](../spec/automatic-nested-mount-ignore-protection.md#9-safety-and-portability)

Scope:

- Establish the central normalized-mount contract in `internal/pathutil` and
  make domain/workspace planning consume it for actual path placement and
  serialized effective mounts.
- Move literal nested-directory rule generation to a focused service-owned
  pure contract fed only by the normalized mount.
- Add typed working-tree `check-ignore` evidence in `internal/git`, including
  winning negation/source parsing and in-parent `.gitignore` qualification.
  Preserve committed-ref APIs used by clone without broadening this feature.
- Add hermetic and real-Git fixtures needed by later milestones. Do not mutate
  `.gitignore` or change init/create command behavior in this milestone.

Test-first slices:

1. Normalize accepted slash/backslash and cleanable legacy runtime forms once,
   prove `ResolveMount` and workspace plan mounts use that exact value, and
   reject absolute, escaping, CR, LF, NUL, invalid UTF-8, and ambiguous input.
2. Generate one anchored directory rule for spaces, tabs, Unicode, `#`, `!`,
   `*`, `?`, brackets, and multi-component mounts; use real Git to prove it
   matches the intended directory and not a sibling or prefix path.
3. Parse root and deeper `.gitignore` winning rules and qualify them only when
   their resolved source lies inside the immediate parent and their pattern is
   effective for the directory.
4. Prove a winning negation, `.git/info/exclude`, repository-configured or
   global external excludes, out-of-parent `.gitignore`, malformed output,
   and Git failure never report protection.
5. Run pathutil fuzz/regression coverage and existing clone ignore tests to
   prove the new working-tree contract does not change committed-base clone
   behavior.

Verification:

- `go test ./internal/pathutil ./internal/domain ./internal/git ./internal/service -run 'Mount|Ignore|Gitignore' -count=1`
- `go test ./internal/pathutil ./internal/git -race -count=1`
- Global definition-of-done commands.

Exit criteria: one normalized value drives placement and rule generation, and
service consumers can distinguish effective in-parent `.gitignore` protection
from every non-qualifying Git result without parsing Git output themselves.

### [x] M01 — Deliver safe deterministic ignore file planning and replacement

Specification coverage: [§§5](../spec/automatic-nested-mount-ignore-protection.md#5-file-inspection-and-update-rules), [§8](../spec/automatic-nested-mount-ignore-protection.md#8-error-and-compatibility-rules), [§9](../spec/automatic-nested-mount-ignore-protection.md#9-safety-and-portability)

Scope:

- Introduce the reusable service requirement, file-plan, snapshot, update, and
  partial-progress result contracts shared by init and create.
- Plan direct-child rules by immediate parent; detect already-effective rules,
  exact generated lines, conflicts, unsafe types, containment violations, and
  deterministic coalescing/order without mutation.
- Implement byte-preserving line-ending-aware content construction and
  compare-and-replace application through `internal/fsutil` with exact mode,
  umask, sync, close, atomic replacement, directory-sync, and temporary cleanup
  behavior on supported platforms.
- Preserve completed source updates after later failure and expose exact
  changed/remaining evidence for retry. Do not wire init/create yet.

Test-first slices:

1. Plan a three-level hierarchy and siblings into the correct immediate-parent
   root files, parent-first/parent-ID/child-ID order, and one replacement per
   parent; an effective root or deeper rule suppresses the generated line.
2. Preserve empty, LF, exclusive-CRLF, mixed-ending, and unterminated content
   byte-for-byte except for the required separator and appended rules; preserve
   existing permissions and honor umask for a missing file.
3. Reject symlink, directory, device/socket where supported, unreadable or
   escaping target, and exact-line-but-visible conflict before mutation.
4. Change existence, type, bytes, and mode after planning and prove each stale
   snapshot is `conflict` and no target is overwritten or followed.
5. Inject temporary creation, write, sync, close, replace, and directory-sync
   failures for existing and missing files; prove each target is complete old
   or new, modes are correct, and temporary files are cleaned.
6. Fail after the first of multiple files, report completed and remaining
   targets deterministically, then retry from fresh facts and prove no line is
   duplicated. Run replacement behavior on Linux, macOS, and Windows CI.

Verification:

- `go test ./internal/fsutil ./internal/service -run 'Atomic|Ignore|Gitignore|Snapshot|Umask' -count=1`
- `go test ./internal/fsutil ./internal/service -race -run 'Atomic|Ignore|Gitignore|Snapshot' -count=1`
- Global definition-of-done commands and the named three-OS CI jobs.

Exit criteria: a reviewed internal service can plan without mutation and apply
coalesced ignore updates with safe target ownership, durable old-or-new files,
stale-write rejection, deterministic partial-progress evidence, and
duplicate-free retry on every supported OS.

### [x] M02 — Make init protect and verify every source mount before publication

Specification coverage: [§§6](../spec/automatic-nested-mount-ignore-protection.md#6-wtree-init), [§8](../spec/automatic-nested-mount-ignore-protection.md#8-error-and-compatibility-rules), acceptance scenarios 1–5 and 9–11 in [§11](../spec/automatic-nested-mount-ignore-protection.md#11-acceptance-scenarios)

Scope:

- Replace opt-in/committed-base init logic with automatic complete-graph
  working-tree inspection and the M01 plan, coalescing `/.wtree.yml` with root
  nested rules.
- Remove `InitRequest.AddIgnore`, `init --add-ignore`, and manual-command
  diagnostics; update affected test fixtures to call plain init.
- Split execution into monotonic source ignore replacement, verification of
  every nested source mount, then existing reversible wtree-owned publication.
  Retain source updates across later source, verification, or publication
  failure and roll back only wtree-owned artifacts.
- Render deterministic dry-run and success output and preserve the existing
  `ignoreUpdates` JSON shape with non-null arrays. Report retained and
  remaining files on failure without changing the JSON error schema.
- Update current init help, README/how-to, and conflicting current guidance in
  the same milestone. Do not alter clone behavior or historical run ledgers.

Test-first slices:

1. Plain init of a three-level hierarchy creates or updates the root and
   intermediate parent files, verifies both mounts with Git, and publishes
   config/manifest/registry/default state only afterward.
2. Existing effective root or deeper rules cause no duplicate; local/global
   excludes do not qualify; exact-line conflicts and post-write negations fail
   before unsafe publication and retry does not duplicate.
3. Missing/regular/symlink/non-regular files, literal special-character mounts,
   unrepresentable mounts, containment, permissions, line endings, and modes
   produce the M00/M01 behavior across the complete preflight.
4. Init dry run reports exact changed files/rules in stable human and JSON
   output, returns `ignoreUpdates: []` when already protected, and leaves
   temporary files, locks, config, manifest, registry, and state absent.
5. Inject failure/cancellation at each source replace, verification, and
   config/manifest/registry/state boundary. Prove completed source files remain
   complete and reported, remaining work is named, wtree-owned artifacts obey
   existing cleanup contracts, and retry converges without duplicates.
6. Prove success output lists each changed file with review/commit guidance,
   the no-change case says every nested mount was already protected, and the
   index/refs remain unchanged because init never stages or commits.

Verification:

- `go test ./internal/service -run 'Init.*Ignore|Init.*Publication|Init.*Manifest|Initializer' -count=1`
- `go test ./internal/cli -run 'Init|Help|HowTo' -count=1`
- `go test ./internal/service ./internal/cli -race -run 'Init' -count=1`
- Global definition-of-done commands.

Exit criteria: plain init automatically makes and verifies every required
source protection before publishing any wtree-owned state, remains dry-run
safe and retryable after partial progress, and exposes no manual ignore option
or guidance.

### [x] M03 — Keep create plan v1 while exposing the automatic ensure list

Specification coverage: [§7.1](../spec/automatic-nested-mount-ignore-protection.md#71-planning), [§8](../spec/automatic-nested-mount-ignore-protection.md#8-error-and-compatibility-rules), [§10](../spec/automatic-nested-mount-ignore-protection.md#10-explicit-non-goals)

Scope:

- Remove committed-base `.gitignore` rejection from create planning while
  preserving branch, identity, collision, containment, target, and selected
  base preflight unrelated to ignore protection.
- Normalize configured and override mounts once, validate and generate rules for
  every non-root repository, and add a service-owned projection from the
  version-one repository entries to deterministic parent-file/rule ensures.
- Keep `plan.Version == 1`, existing JSON fields/actions/order, checkout JSON,
  and transaction/recovery/state versions byte-compatible.
- Extend human create dry run with the ensure list and explicit no-mutation
  message. Do not inspect nonexistent future parent files or claim they will
  change; do not add public actions or fields.
- Update create help and current dry-run documentation for automatic ensure
  semantics; do not advertise an opt-in flag.

Test-first slices:

1. Plan default, multi-component, legacy-normalized, and repeated override
   mounts into correct parent root `.gitignore` paths and literal rules in
   parent-first/ID order; reject duplicate, unknown, escaping, and
   unrepresentable overrides before mutation.
2. Plan create from a selected base whose committed `.gitignore` lacks the
   rule, and prove the source checkout and base commit are not inspected or
   modified for ignore content.
3. Decode JSON for root-only, nested-default, nested-override, and checkout
   plans and assert version 1, the exact existing field/action schema, and no
   ignore update/action metadata.
4. Render human dry run with the deterministic ensure list while two repeated
   dry runs leave paths, mtimes, locks, refs, worktrees, recovery, registry,
   and workspace state unchanged.
5. Preserve existing error kinds and human/JSON stream separation for mount
   parsing, semantic validation, Git fact failure, and planning conflict.

Verification:

- `go test ./internal/plan ./internal/service -run 'Plan|Mount|Ignore|Create' -count=1`
- `go test ./internal/cli -run 'Create|WorkspacePlan|DryRun|JSON' -count=1`
- `go test ./internal/plan ./internal/service ./internal/cli -race -run 'Plan|Create' -count=1`
- Global definition-of-done commands.

Exit criteria: create dry run completely validates and explains automatic
protection using the unchanged public workspace-plan v1 contract, without
requiring committed rules or causing any mutation.

### [x] M04 — Enforce parent-first protection during create and safe cleanup

Specification coverage: [§§7.2–7.3](../spec/automatic-nested-mount-ignore-protection.md#72-parent-first-execution), [§§8–9](../spec/automatic-nested-mount-ignore-protection.md#8-error-and-compatibility-rules), acceptance scenarios 6–10 in [§11](../spec/automatic-nested-mount-ignore-protection.md#11-acceptance-scenarios)

Scope:

- Expand internal create step construction so each newly added parent
  worktree runs one coalesced inspect/apply/verify phase for all direct
  children before any child worktree addition, without changing public plan
  actions.
- Revalidate target snapshots at replacement and verify every direct-child
  mount afterward, including requirements that needed no write. Prevent child
  `.git` creation on any inspection, write, conflict, or verification failure.
- Return internal changed-file evidence for human success while keeping JSON
  success as the original workspace plan. Never edit source checkouts.
- Make worktree rollback distinguish clean/exact automatic dirt from unrelated
  tracked, staged, or untracked content; force-remove only the former and
  preserve the latter with existing rollback-incomplete recovery evidence.
- Preserve project locking, locked plan revalidation, branch cleanup,
  cancellation, result validation, workspace-state commit, verbose events, and
  recovery-record behavior for all existing actions.
- Update current create success/failure and recovery guidance in the same
  milestone.

Test-first slices:

1. Execute a three-level create and assert ordered effects: parent branch and
   worktree, direct-child coalesced update, Git verification, then child
   worktree; repeat at the child-parent level. At no point may an unverified
   child mount contain `.git`.
2. Create with default and overridden mounts from bases with missing,
   partially present, already-effective root, and effective deeper rules;
   update only missing files in new worktrees and leave all source checkout
   bytes, modes, index, and refs unchanged except planned branches.
3. Make an exact generated line ineffective with a negation, fail before the
   child is added, preserve or remove the parent according to transaction
   cleanup, and prove retry never appends a duplicate.
4. Inject failure/cancellation before and after every branch, worktree,
   inspection, replacement step, verification, result validation, and state
   commit; prove complete rollback or exact recovery evidence and correct
   changed/removed/unverified file diagnostics.
5. During rollback, exercise a clean parent, only the exact automatic file
   change, an independently changed `.gitignore`, another tracked edit, a
   staged edit, and an untracked file. Remove only transaction-owned cases and
   preserve every unexpected-dirt case.
6. Prove human success lists actual changed files and commit guidance, no-change
   success is truthful, JSON remains the v1 plan, verbose progress stays on its
   existing stream, and no path is staged or committed.
7. Preserve same-workspace and different-workspace concurrency guarantees,
   including changed locked-plan conflict and existing recovery-record gate.

Verification:

- `go test ./internal/service ./internal/transaction -run 'Create|Transaction|Rollback|Recovery|Ignore|Worktree' -count=1`
- `go test ./internal/cli -run 'Create|Rollback|JSON|Verbose' -count=1`
- `go test ./internal/service ./internal/transaction ./internal/cli -race -run 'Create|Transaction' -count=1`
- Global definition-of-done commands.

Exit criteria: every direct child is added only after real Git verification in
its newly created immediate parent, successful output identifies actual edits,
and failure cleanup cannot discard unrelated work.

### [ ] M05 — Prove end-to-end protection and documentation consistency

Specification coverage: [§§8–11](../spec/automatic-nested-mount-ignore-protection.md#8-error-and-compatibility-rules), all acceptance scenarios in [§11](../spec/automatic-nested-mount-ignore-protection.md#11-acceptance-scenarios)

Scope:

- Add black-box CLI acceptance coverage spanning plain init, create with
  default/overridden mounts, dry runs, retry, failure, and no-change paths in a
  three-level real-Git hierarchy.
- After successful init and create, run `git add .` in every parent fixture and
  prove no managed nested repository is staged as mode `160000`; restore test
  indexes within disposable repositories only.
- Audit help, built-in how-to, README, install/troubleshooting guidance, current
  specifications, and traceability for automatic behavior and absence of
  `wtree add-ignore`, `init --add-ignore`, and `create --add-ignore`. Preserve
  historical ideas, superseded documents, and completed run ledgers except for
  required lifecycle links already maintained by this plan.
- Run all supported-OS CI evidence and a final compatibility audit confirming
  no unrelated command, schema version, staging/commit behavior, or source
  checkout changed.
- On final approval, update this plan and its source specification to
  `implemented`, update `docs/status-overview.md`, and record implementation
  evidence according to repository lifecycle rules.

Test-first slices:

1. Drive plain init then default and override create through the public CLI in
   a three-level hierarchy; verify parent ownership, exact rules, ordering,
   success output, and no `160000` index entry after `git add .`.
2. Repeat successful operations and already-protected cases and prove stable
   output plus byte-for-byte no duplicate rules.
3. Run both dry runs with snapshots of files, mtimes, locks, branches,
   worktrees, recovery, registry, and state and prove total non-mutation.
4. Exercise representative validation, conflict, Git, internal I/O, clean
   rollback, and rollback-incomplete failures in human and JSON modes and
   assert exact stream/error classification and recovery evidence.
5. Assert public help/current docs contain only automatic workflows, while
   clone, checkout, import, update, sync, remove, delete, doctor, and submodule
   behavior remains unchanged.

Verification:

- `go test ./internal/cli -run 'Init|Create|Ignore|Nested|E2E|Help|HowTo' -count=1`
- `go test ./internal/cli ./internal/service -race -run 'Init|Create|Ignore|Nested|E2E' -count=1`
- `rg -n 'add-ignore|--add-ignore' README.md docs/INSTALL.md docs/TROUBLESHOOTING.md internal/cli --glob '*.go' --glob '*.md'` returns only intentional rejection assertions or historical/superseded references allowed by this milestone.
- Global definition-of-done commands and successful Linux, macOS, and Windows CI jobs.

Exit criteria: every specification acceptance scenario has automated or named
platform evidence, public guidance is consistent with the automatic invariant,
no unsupported command/flag or schema change escaped, lifecycle metadata is
current, and the complete implementation has independent approval.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-18 | M00 | Focused M00 and race checks passed; full suite/race, vet, fmt, build, release, and diff checks passed | Approved after reset-counter remediation; R1–R5 resolved | Not committed |
| 2026-08-18 | M01 | Focused atomic/ignore checks passed; full quality gates and platform cross-compilation recorded | Approved after R1–R5 remediation | Not committed |
| 2026-08-18 | M02 | Focused init verification and full check/release gates passed | Approved after R1/R2 remediation | Not committed |
| 2026-08-18 | M03 | Focused v1 JSON contract and plan/service checks passed | Approved after JSON contract coverage remediation | Not committed |
| 2026-08-19 | M04 | Exact service/transaction and CLI gates, focused race, full normal/race, fmt, vet, build, release, diff, and three-platform builds passed | Approved after reset-counter R1 correction; safe cleanup and exact recovery evidence verified | Not committed |
