# Project registry inspection and lifecycle implementation plan

Status: ready to execute  
Source of truth: user-approved CLI contract (`wtree project list` and `wtree project (prune|unregister) <id>`, 2026-08-15); [`docs/spec/wtree.spec.md` §§7, 14, 23, 66–67, 76, 80, 83](../spec/wtree.spec.md); [`internal/store/store.go`](../../internal/store/store.go); [`internal/lock/lock.go`](../../internal/lock/lock.go); [`internal/service/init.go`](../../internal/service/init.go); [`internal/service/resolve.go`](../../internal/service/resolve.go); [`internal/cli/root.go`](../../internal/cli/root.go)  
Delivery style: test-first, one reviewed milestone at a time; no registry schema migration, repository deletion, publishing, or commits

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes those decisions below.

For each unchecked milestone, in order:

1. Read this plan, the relevant source-of-truth sections, the durable run
   ledger at `docs/ai/runs/project-registry-management.md`, and the current
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
   `docs/ai/milestone-supervision.md`. Do not use `escalation-reviewer` as a
   routine second review.
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

### Public command surface

- Add one root child namespace with these exact forms:

  ```text
  wtree project list [--json] [--data-dir <path>]
  wtree project prune <project-id> [--dry-run] [--json] [--data-dir <path>]
  wtree project unregister <project-id> [--dry-run] [--json] [--data-dir <path>]
  ```

- `init` remains a root command. Existing workspace commands remain at the
  root; this change does not rename `wtree list` or any other command.
- `project`, invoked without a child, renders its help. `--force` and
  `--verbose` are unsupported for all three children; `--dry-run` is
  unsupported for `project list`.
- A root `--project <path>` selector is meaningless for this global namespace
  and must be rejected with `invalid_arguments`, including when combined with
  any `project` child. `--data-dir` remains available to select the registry
  location and to keep tests hermetic.
- A project ID argument is an exact registry key. Reject empty, `.`, path
  separators, or any value that is not a single safe path component before
  constructing a project-lock path. Do not introduce a new UUID-only
  restriction because the version-one registry and domain currently accept
  non-empty stable IDs.
- `project list` is the global registry inspection command. A readable
  registry containing inconsistent entries is a successful listing (exit 0);
  an absent registry is an empty successful listing; malformed JSON, unknown
  registry versions, or I/O failures use the existing validation/internal
  error taxonomy and never produce a partial success document.

### Inventory and diagnosis contract

- A service-owned, read-only inventory model returns entries sorted by project
  ID. It does not use project/workspace resolution, mutate/reconcile registry
  data, invoke Git, or acquire exclusive locks.
- Each entry exposes these stable JSON fields; collection fields are always
  arrays, never `null`:

  ```json
  {
    "id": "project-id",
    "name": "project-name",
    "configPath": "/absolute/.wtree.yml",
    "status": "healthy|warning|error",
    "prunable": false,
    "findings": [
      {
        "code": "duplicate-config-path",
        "severity": "warning|error",
        "message": "human-readable context",
        "relatedProjectIds": ["other-id"]
      }
    ]
  }
  ```

  The top-level JSON value is `{"projects":[...]}` and the human renderer
  prints one deterministic summary row per project followed by indented
  findings. Paths and messages are data, not parsing contracts.
- Establish and contract-test these finding codes:
  `missing-config`, `unreadable-config`, `invalid-config`,
  `config-id-mismatch`, `duplicate-config-path`,
  `duplicate-repository-identity`, `missing-default-state`,
  `invalid-default-state`, and `recovery-record`.
- Status is the highest finding severity (`healthy` with none, otherwise
  `warning` or `error`). Findings and related IDs are deterministically sorted.
- Compare registry config paths using cleaned absolute paths and resolve
  symlinks when the target exists. Do not lowercase paths. Missing targets use
  their cleaned absolute representation. Repository identities are compared
  exactly as stored because they are already canonical Git common-directory
  identities.
- A duplicate config-path group has an authoritative keeper only when exactly
  one entry key equals the readable config's declared project ID. Mark other
  entries in that group as superseded and prunable; show the keeper as a
  related project. Never guess a keeper from name, map order, modification
  time, or lexical ordering.
- An entry is prunable when objective entry-local evidence shows its registry
  record is stale: its config is missing, unreadable, invalid, declares a
  different ID, or it is a superseded member of an unambiguous duplicate-path
  group. Duplicate repository identities, missing/invalid default state, or
  other ambiguity alone do not select a victim. Any unresolved recovery record
  makes the entry non-prunable until recovery is resolved.
- Inventory only diagnoses registered entries. Discovering arbitrary orphaned
  state/lock directories, repairing configs, and adding a global `doctor`
  command are deferred.

### Prune and unregister semantics

- `project prune <id>` is evidence-gated cleanup. Planning must fail with
  `project_not_found` for an absent ID, `validation` when the entry is healthy
  or ambiguous/non-prunable, and `conflict` when recovery metadata exists.
  There is no force override; the error directs intentional removal to
  `wtree project unregister <id>`.
- `project unregister <id>` intentionally removes any registered entry,
  healthy or inconsistent, but refuses with `conflict` while that project has
  unresolved recovery metadata. Exact-ID selection is the authorization; no
  interactive prompt or `--force` is added.
- Both operations remove exactly one key from `registry.json`. They never
  remove or alter `.wtree.yml`, repositories, worktrees, branches, workspace
  state, recovery records, project directories, or lock files. An empty
  registry remains a valid version-one registry file rather than being
  deleted.
- Both plan/result JSON documents expose `operation`, `projectId`, `name`,
  `configPath`, `reasons`, and a `retained` object that explicitly reports
  `projectConfig`, `workspaceState`, `recoveryData`, and `lockFile` as retained.
  Prune reasons use the inventory finding codes. Human dry-run output names
  the registry entry to remove, the reason, every retained category, and ends
  with `No changes made.` Successful human output says only the registration
  was removed and that project data was retained.
- `--dry-run` performs complete read-only planning and emits the same semantic
  plan as execution, with no lock-file creation, registry rewrite, timestamp
  change, or other mutation.
- Real mutation acquires locks in the existing order: registry lock first,
  then the target project lock. Under both locks it rereads the registry and
  replans from current state. A missing target, changed eligibility, changed
  entry, lock timeout, or recovery record appearing during the race fails
  before writing.
- Registry replacement uses `store.WriteRegistry`; do not add a second writer
  or edit JSON in place. Failures before rename preserve the prior bytes;
  failures after atomic replacement may report an operational error but must
  leave a readable complete old-or-new version, never truncated JSON.
- `unregister` is registration removal, not a permanent tombstone. Because
  local `.wtree.yml` and state are retained, a later mutating command executed
  from that project may reconcile and register it again under existing
  resolver behavior. Help and success output must disclose this.

### Duplicate prevention in `init`

- Keep the existing early refusal when `.wtree.yml` exists. When it does not,
  `init` must also diagnose the current registry before publishing a new ID.
- After discovery has established the canonical config path and repository
  identities, dry-run and real preflight reject an existing registration with
  the same canonical config path or any overlapping repository identity.
  Report every matching project ID in sorted order and direct the user to
  `wtree project list`; do not silently reuse, replace, merge, or prune it.
- Real `init` repeats this check after acquiring the registry lock and before
  acquiring/publishing the new project state, preserving the existing
  registry → project lock order. Failed duplicate preflight leaves the
  registry, `.wtree.yml`, default state, and lock layout unchanged except for
  the already-established registry lock file behavior.
- Changes to future or absent `clone` registration behavior are outside this
  plan. When clone is implemented, it should consume the same registry
  conflict policy in a separate reviewed change rather than duplicating it.

### Compatibility and scope boundaries

- Keep registry schema version 1 unchanged; findings and removal plans are
  runtime/CLI values, not persisted registry fields. No migration or tombstone
  is introduced.
- Reuse the existing exit taxonomy, JSON error envelope, atomic store, config
  decoder, runtime path resolver, and advisory lock wrapper. Add no dependency.
- Preserve all current workspace resolution and command behavior except the
  intentional new `init` duplicate refusal.
- Do not edit real user registry/state during implementation or tests. All
  tests use temporary data/config/home locations and injectable stores/locks.
- Publishing, installation, commits, cleanup of the duplicate observed on the
  author's machine, and changes outside this repository are not authorized by
  this plan.

## Stable contracts to establish early

### Project registry inventory

- Owner: `internal/service`, backed only by `internal/store` and
  `internal/config` reads plus filesystem metadata.
- Consumers: `internal/cli` renderers and later prune/unregister planning.
  `internal/render` may serialize values but must not infer health or pruning
  eligibility. The domain and store packages must not depend on services.
- Invariant: the same registry/config/state snapshot always yields the same
  sorted entries, findings, keeper selection, status, and eligibility.
- Enforcement: table-driven service tests cover every finding independently,
  combined severity, deterministic order, canonical/symlink paths, ambiguous
  duplicates, non-null JSON collections, and a byte-for-byte no-mutation
  assertion.
- Versioning: additive JSON fields may be added later, but field meanings,
  status values, and finding codes introduced here remain stable. Registry v1
  remains unchanged.

### Registry removal plan and mutation boundary

- Owner: `internal/service`; CLI owns argument parsing and rendering, store
  owns serialization/atomic replacement, and lock owns advisory exclusion.
- Consumers: both `project prune` and `project unregister`; neither command
  may bypass the shared planner/executor.
- Invariant: planning is read-only; execution locks registry then project,
  replans, and atomically deletes only the selected registry key. All project
  and workspace artifacts are retained.
- Enforcement: injected lock contention, concurrent registry change,
  writer-failure, recovery-record, dry-run, and exact-target tests prove no
  unintended mutation. CLI integration tests decode JSON and inspect the
  temporary filesystem.
- Recovery rule: because the only intended mutation is one atomic registry
  replacement, no transaction recovery record is created. Existing recovery
  metadata blocks removal and is never rewritten.

### Registration conflict policy

- Owner: `internal/service`, shared by registry inventory and `init` preflight;
  it compares canonical config paths and exact stored Git identities.
- Consumers: `init` in this plan. Resolver behavior remains a consumer of the
  registry but is not rewritten to hide ambiguity.
- Invariant: an absent local config never authorizes a second global project
  for the same checkout; ambiguous global identity is reported, not guessed.
- Enforcement: initializer service and CLI tests reproduce the two-init
  scenario with a removed local config, prove RED before implementation, and
  assert all durable files remain unchanged after refusal.

## Architecture and dependency boundaries

```text
cmd/wtree
   ↓
internal/cli: project namespace, flags, human/JSON rendering
   ↓
internal/service: inventory → removal planning → locked execution
   ↓                    ↓                    ↓
internal/config     internal/store       internal/lock
strict YAML reads   registry/state I/O   registry → project order
```

- Keep filesystem and registry interpretation out of Cobra handlers.
- Keep human formatting and stdout/stderr decisions out of services.
- Keep health/prunability policy out of `internal/store`; the store continues
  to validate and atomically persist versioned values only.
- Prefer narrow injectable collaborators following existing service tests.
  Do not broaden production interfaces solely for mocks when a function seam
  or existing lock/store abstraction suffices.
- Do not route global project commands through `Resolver`: they must work when
  the current directory is not a project and when the registry itself contains
  the inconsistencies being reported.

## Global definition of done

- Every behavior change has recorded RED → GREEN → REFACTOR evidence, with
  success, invalid-input, ambiguity, no-mutation, and contention/failure cases
  appropriate to the slice.
- Focused package tests pass, followed by `go test ./...`,
  `go test -race ./...`, `go vet ./...`, `make fmt-check`, `make build`, and
  `make check`.
- New tests are hermetic and do not inspect or mutate the developer's real
  HOME, OS data directory, registry, Git configuration, or lock files. They
  cover paths with spaces and at least one symlink/canonicalization boundary
  on platforms that support symlinks.
- Human and JSON output, help text, unsupported-option behavior, exact exit
  categories, empty registry behavior, deterministic ordering, and writer
  errors are contract-tested. JSON tests decode structures rather than depend
  on map key order.
- Every mutation proves dry-run purity, preflight-before-write, registry then
  project lock order, locked revalidation, exact-key-only removal, atomic
  persistence, retained data, and safe handling of an existing recovery
  record.
- No persisted schema version changes and no new external dependencies occur.
- Root help, nested command help/examples, `--how-to`, README usage, and
  `docs/spec/wtree.traceability.md` accurately describe the final public
  behavior without presenting registry pruning as workspace/Git pruning.
- `git diff --check` passes, unrelated pre-existing changes remain preserved,
  and the independent reviewer reports no unresolved material finding.

## Risk and rollout

- Primary risk: deleting the wrong registry key can make workspace discovery
  ambiguous or unavailable. Mitigation is exact-ID selection, conservative
  prune eligibility, retained project/state data, registry → project locking,
  locked revalidation, and atomic exact-key replacement.
- Concurrency risk: moving or deleting lock paths can split advisory exclusion.
  This plan never removes lock files or project lock directories; mutation uses
  the existing lock manager and ordering.
- Compatibility risk: `unregister` may be mistaken for permanent deletion.
  Human output, JSON retained fields, and help must state that all data remains
  and a later project mutation may register the local config again.
- Rollout needs no persisted migration or feature flag. The commands are new;
  existing registry v1 readers remain compatible. Release, installation, and
  modification of real user data require separate authorization after this
  plan completes.
- If execution finds that the existing atomic writer or lock abstraction
  cannot uphold the fixed guarantees without a registry schema change, lock
  deletion, or a new destructive recovery decision, record the evidence as an
  external/product-scope blocker and request direction. Ordinary test or
  implementation failures are not blockers.

## Milestones

### [x] M00 — Expose deterministic global project inventory and diagnostics

Specification coverage: user-approved `wtree project list` contract;
[`docs/spec/wtree.spec.md` §§7, 76, 80, 83](../spec/wtree.spec.md);
[`internal/store/store.go`](../../internal/store/store.go)

Scope:

- Add the service-owned inventory/report/finding model and read-only registry,
  config, default-state, and recovery inspection defined above.
- Implement deterministic duplicate config-path and repository-identity
  grouping, unambiguous keeper selection, statuses, and prune eligibility
  without Git access or mutation.
- Add the `project` namespace and `project list` command with human/JSON output,
  empty-registry behavior, `--data-dir`, root-selector rejection, detailed
  nested help, and unsupported-option validation.
- Update root help, command examples, practical how-to, README, and traceability
  for inspection behavior in this same milestone.
- Do not add either mutating child yet; help may describe them only when their
  milestones land.

Test-first slices:

1. Given empty and healthy temporary registries, return a stable empty/listed
   report sorted by ID with non-null findings and no filesystem changes.
2. Reproduce duplicate config paths where exactly one key matches the live
   config ID; mark only the superseded entry prunable and relate both IDs.
   Reproduce an ambiguous group and overlapping repository identities without
   guessing a victim.
3. Diagnose missing/unreadable/invalid/mismatched configs, missing/invalid
   default state, and recovery records with the fixed codes, severity, and
   eligibility rules; reject a malformed or newer registry without partial
   output.
4. Exercise `wtree project list` human and decoded JSON output, paths with
   spaces, canonical/symlink aliases, empty registry, broken output writer,
   root `--project` rejection, and unsupported flag/argument combinations.

Verification:

- `go test ./internal/service -run 'ProjectRegistry|ProjectInventory' -count=1`
- `go test ./internal/cli -run 'ProjectList|ProjectHelp|RootHelp|EveryPrintedWTREEExample' -count=1`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `make fmt-check`
- `make build`
- `make check`
- `git diff --check`

Exit criteria: from any directory, `wtree project list` accurately and
deterministically exposes healthy, duplicate, and inconsistent registrations
without resolving a workspace or mutating any file; all public contracts and
global definition-of-done gates are satisfied.

### [x] M01 — Safely prune only evidence-backed stale registrations

Specification coverage: user-approved `wtree project prune <id>` contract;
[`docs/spec/wtree.spec.md` §§23, 66–67, 76, 80](../spec/wtree.spec.md);
[`internal/lock/lock.go`](../../internal/lock/lock.go)

Scope:

- Add the shared registry-removal plan structure and prune planning rules,
  including exact safe-ID validation, finding-code reasons, retained-artifact
  reporting, and recovery blocking.
- Implement locked prune execution with registry → project lock ordering,
  current-state replan, exact-entry comparison, and one atomic registry write.
- Add `project prune` dry-run, JSON, human rendering, error classification,
  help/examples, and documentation. Do not add a force escape hatch.
- Preserve every non-registry artifact and every non-target registry entry;
  retain the target lock file and state directories even after success.

Test-first slices:

1. Plan the known two-init duplicate shape and select only the superseded ID;
   reject the keeper, a healthy entry, an ambiguous duplicate, an unsafe ID,
   and an unknown ID without mutation.
2. Prove dry-run returns the complete removal/retention plan while preserving
   registry bytes, timestamps, directory trees, and lock-file nonexistence.
3. Execute under injected locks, revalidate, and atomically remove only the
   target; prove registry-lock and project-lock contention, target/eligibility
   changes, and recovery records all fail before a write.
4. Inject writer failures before replacement and verify prior registry bytes;
   exercise post-replacement failure boundaries to prove the registry is
   always complete readable v1 JSON and retained artifacts are unchanged.
5. Contract-test human/JSON success and failures, exit codes, broken writers,
   unsupported flags, and paths containing spaces.

Verification:

- `go test ./internal/service -run 'ProjectPrune|ProjectRegistryRemoval' -count=1`
- `go test ./internal/cli -run 'ProjectPrune|ProjectHelp|EveryPrintedWTREEExample' -count=1`
- `go test ./internal/store -run 'Registry|Atomic' -count=1`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `make fmt-check`
- `make build`
- `make check`
- `git diff --check`

Exit criteria: prune can remove only an objectively prunable exact registry
entry through a locked, revalidated, atomic update; it cannot select an
ambiguous victim or affect project/workspace/Git data, and all gates pass.

### [x] M02 — Support explicit unregister while retaining all project data

Specification coverage: user-approved
`wtree project unregister <id>` contract; [`docs/spec/wtree.spec.md` §§23,
66–67, 76](../spec/wtree.spec.md)

Scope:

- Extend the shared removal planner/executor with intentional unregister mode;
  accept healthy or inconsistent exact entries but retain recovery blocking,
  safe-ID validation, locked revalidation, and atomic exact-key removal.
- Add `project unregister` dry-run, JSON, human output, help/examples, and
  documentation that disclose retained data and possible future automatic
  re-registration from the still-present local config.
- Prove prune and unregister retain distinct eligibility/error behavior while
  sharing one mutation boundary; do not copy the lock/write implementation.
- Keep permanent tombstones, local-config deletion, state cleanup, and bulk
  unregister outside scope.

Test-first slices:

1. Plan unregister for healthy and inconsistent entries and reject unknown or
   unsafe IDs and recovery-bearing entries, always reporting retained data.
2. Prove dry-run purity and successful removal of only the selected key while
   config, all state, recovery directories, repositories, and lock files are
   byte-for-byte or filesystem-equivalent retained.
3. Race target changes and lock contention against unregister, proving locked
   revalidation and no mutation on conflict; reuse the tested atomic writer
   failure boundary.
4. Contract-test human/JSON results, error envelopes, help, unsupported flags,
   and the explicit re-registration warning; regression-test that prune still
   refuses the same healthy entry unregister accepts.

Verification:

- `go test ./internal/service -run 'ProjectUnregister|ProjectRegistryRemoval|ProjectPrune' -count=1`
- `go test ./internal/cli -run 'ProjectUnregister|ProjectPrune|ProjectHelp|EveryPrintedWTREEExample' -count=1`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `make fmt-check`
- `make build`
- `make check`
- `git diff --check`

Exit criteria: an exact registered project can be intentionally unregistered
without implying or performing data deletion, all concurrency/failure behavior
uses the shared safe boundary, user-facing re-registration semantics are
unambiguous, and all gates pass.

### [x] M03 — Prevent duplicate registration during repeated initialization

Specification coverage: [`docs/spec/wtree.spec.md` §§14, 67, 76,
80](../spec/wtree.spec.md); [`internal/service/init.go`](../../internal/service/init.go)

Scope:

- Reuse the registry conflict policy to reject `init` when the local config was
  removed but the same canonical config path or any discovered repository
  identity remains registered.
- Apply the check to dry-run and repeat it under the real registry lock before
  publication, reporting every conflicting ID deterministically and directing
  users to `wtree project list`.
- Preserve existing already-initialized behavior, rollback/publication order,
  error taxonomy, registry/project lock order, and valid initialization.
- Complete README, how-to, help, traceability, and regression coverage for the
  end-to-end discover → inspect → prune/unregister → retry workflow.

Test-first slices:

1. Initialize a temporary project, remove only its local config, and prove a
   second init is refused by config-path and repository-identity evidence with
   registry/default state unchanged and no new project ID published.
2. Exercise dry-run and real init with path aliases, multiple conflicting IDs,
   unrelated healthy projects, missing stale config paths, and registry-lock
   contention; assert deterministic diagnostics and no partial artifacts.
3. Prove valid first-time init and the existing `.wtree.yml` refusal remain
   unchanged, then use project prune/unregister in isolated CLI integration
   tests to show that explicit registry cleanup makes the intended retry
   possible.
4. Run a final public-contract audit: root/nested help, all printed examples,
   JSON structures, exit codes, terminology distinguishing project registry
   pruning from Git worktree pruning, and specification traceability.

Verification:

- `go test ./internal/service -run 'Initializer|ProjectRegistry|RegistrationConflict' -count=1`
- `go test ./internal/cli -run 'Init|Project|Help|HowTo|EveryPrintedWTREEExample' -count=1`
- `go test ./internal/config ./internal/store ./internal/lock -count=1`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `make fmt-check`
- `make build`
- `make check`
- `git diff --check`

Exit criteria: the duplicate-registration scenario is prevented before any
new project publication, users can inspect and explicitly remediate existing
duplicates through the agreed namespace, all retained-data and locking
guarantees are documented and tested, and the full repository gates plus
independent review pass with no unresolved material findings.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-15 | M00 — Expose deterministic global project inventory and diagnostics | Focused service/CLI, full, race, vet, fmt-check, build, make-check, and diff-check all pass under main-agent controlled verification. | Approved after R1/R3 narrowed remediation; R1-R3 resolved and no material findings remain. | Not committed (per plan). |
| 2026-08-15 | M01 — Safely prune only evidence-backed stale registrations | Focused service/CLI/store, full, race, vet, fmt-check, build, make-check, and diff-check all pass under main-agent controlled verification. | Approved after R1-R3 remediation; exact safe IDs, conservative structured eligibility, lock/revalidation/atomic retention contracts verified with no material findings. | Not committed (per plan). |
| 2026-08-15 | M02 — Support explicit unregister while retaining all project data | Focused service/CLI, full, race, vet, fmt-check, build, make-check, and diff-check all pass under main-agent controlled verification. | Approved after R1 remediation; shared removal boundary, exact retention, explicit re-registration warning, and unsupported-flag presence contracts verified with no material findings. | Not committed (per plan). |
| 2026-08-15 | M03 — Prevent duplicate registration during repeated initialization | Focused service/CLI/config/store/lock, full, race, vet, fmt-check, build, make-check, and diff-check all pass under main-agent controlled verification. | Approved on initial review; shared canonical/exact conflict policy, unlocked/locked duplicate refusal, no-publication, cleanup retry, and final public contracts verified with no material findings. | Not committed (per plan). |
