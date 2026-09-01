# Local and shared workspace lifecycle hooks implementation plan

Status: initial
Source specification: [Local and shared workspace lifecycle hooks specification](../spec/local-workspace-lifecycle-hooks.md)
Implementation context: [Local and shared workspace lifecycle hooks context](local-workspace-lifecycle-hooks-context.md)
Source idea: [Machine-local and shared workspace lifecycle hooks](../ideas/local-workspace-lifecycle-hooks.md)
Related capability contract: [Full multi-repository experience §8.2](../spec/full-multi-repository-experience.md#82-lifecycle-hooks) and [§10.5](../spec/full-multi-repository-experience.md#105-portable-manifest-and-local-configuration-evolution)
Authoritative existing code: [`internal/config`](../../internal/config), [`internal/service/create.go`](../../internal/service/create.go), [`internal/service/clone_execute.go`](../../internal/service/clone_execute.go), [`internal/service/process_unix.go`](../../internal/service/process_unix.go), [`internal/service/process_windows.go`](../../internal/service/process_windows.go), [`internal/store/store.go`](../../internal/store/store.go), and [`internal/cli/root.go`](../../internal/cli/root.go)
Delivery style: test-first, one independently reviewed milestone at a time; no new dependency, network-dependent test, automatic staging or commit, push, pull request, deployment, or release publication

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan and its source specification fix them.

For each unchecked milestone, in order:

1. Read this plan, the focused implementation context, the exact specification
   sections named by the milestone, the current files in scope, the durable ledger at
   `docs/ai/runs/local-workspace-lifecycle-hooks.md`, and the current worktree.
   Create the ledger before the first implementation dispatch. On resumption,
   reconcile the plan, ledger, referenced evidence, and filesystem and append
   a reconciliation checkpoint before dispatching more work.
2. Derive and record one complete milestone checklist from every scope item,
   test-first slice, documentation obligation, exit criterion, and
   verification command.
3. Give the complete initial packet to the normal `implementer`. Use the
   normal `implementer` for remediation when the ledger attempt count is zero
   or one and `escalation-implementer` only when it is two. Require RED → GREEN
   → REFACTOR evidence, changed files, verification results, and unresolved
   concerns.
4. Treat partial work as progress rather than a submission. Do not request
   review or change remediation counters until every checklist item has
   evidence.
5. Send each complete submission to the read-only `reviewer`, which inspects
   the shared filesystem against the entire milestone, specification,
   compatibility, security, portability, test quality, and required checks.
6. Record every material finding with a stable ID and return the full
   unresolved set in one test-first remediation packet. Apply the exact
   three-rejected-complete-remediation limit in
   [milestone supervision](../ai/milestone-supervision.md). Do not use an
   escalation reviewer as a routine second opinion.
7. After reviewer approval, run the milestone verification as the main agent,
   update affected contracts and documentation, check the milestone, append
   its execution-log row, create the next milestone ledger snapshot, and
   dispatch the next initial packet immediately.

Do not stop for ordinary test failures, reviewer findings, partial
submissions, hook fixture failures, or approved milestones. Preserve unrelated
changes and never use destructive cleanup. Commit only when separately
authorized. A final response is permitted only by the durable-ledger gate in
[milestone supervision](../ai/milestone-supervision.md).

An exact-tree hosted Linux/macOS/Windows CI run can be a final external blocker
when the current tree cannot be represented to CI without separately
authorized commit, push, or pull-request activity. Record the local and
reviewer evidence plus the precise continuation condition; do not weaken or
silently waive the CI requirement.

## Fixed implementation decisions

### Product surface and event scope

- Deliver only local `post-create`, portable `post-clone`, and portable inert
  `shared_hooks.post-create`.
- Add `wtree hooks list`, `wtree hooks share <event> [--force]`,
  `wtree hooks install [--force | --missing]`, and
  `wtree hooks retry <workspace>`. Do not add aliases, implicit fresh runs,
  named-hook runs, record deletion, or force retry.
- Local create hooks run automatically after core publication unless the user
  supplies `--no-hooks`. Portable clone hooks run only with `--run-hooks`.
  Shared hooks never execute from the manifest under any flag.
- `post-checkout`, `post-update`, pre-operation, and removal hooks remain
  unsupported. Existing checkout and update commands execute no hook in this
  plan.
- Omitted `repository` means the configured base repository. Timeout defaults
  to `60s` and must be positive and no greater than `24h`. IDs are unique per
  event and command arrays are literal direct-process arguments.

### Versioning and migration

- Keep local and portable version-two schemas strict, readable, and
  byte-compatible. Unknown hook fields remain invalid in version two.
- Add independent local and portable version-three schemas. Local v3 adds
  `hooks`; portable v3 adds `hooks` and `shared_hooks`.
- Init and clone continue to write local v2 when no local v3 content exists.
  Merely reading or cloning a portable v3 file does not upgrade local config
  and does not install shared hooks.
- Share upgrades a portable v2 manifest to v3 only when it performs a real
  write. Install upgrades local v2 to v3 only when it performs a real write.
  Identical/no-content operations preserve exact bytes and version.
- Update accepts portable v2/v3, preserves an existing local v3 hook mapping
  semantically, and never reconciles it from `shared_hooks`. Workspace state,
  registry, and rollback-recovery formats do not change.

### Trust, portability, and secrets

- Treat local hooks as trusted unsandboxed programs. Installing a shared event
  is explicit local consent. Obtaining a portable manifest is never consent.
- Resolve local source-relative executables beneath the registered source
  checkout, portable clone executables beneath private staged content,
  absolute local executables as written, and bare names through inherited
  `PATH`. Reject containment escape and use the existing physical-path safety
  model.
- Portable hooks and shared hooks reject absolute/home paths, file URLs, URL
  user information, and controls. Share additionally proves a relative
  executable with a separator is tracked by its selected source repository.
- Inherit the full invocation environment only for trusted local
  `post-create`. For portable `post-clone`, retain only the specification's
  PATH, locale, temporary-directory, and Windows launch allowlist. In both
  cases replace the complete reserved `WTREE_` set with authoritative values
  and never put ambient values in plans, JSON, errors, tests, or persisted
  state.
- Stream child output to command stderr with hook context. Keep command stdout
  valid for normal rendering or exactly one JSON document. Persist no output
  tail, command element, executable path, environment value, or arbitrary
  child error text.

### Transaction, locking, and retry

- Keep hooks outside `WorkspaceTransaction` and clone publication steps. They
  start only after authoritative publication, validation, and release of the
  project mutation lock. Core failure runs no hook; hook failure never invokes
  core rollback.
- Use one dedicated project/workspace/event hook-run lock while user code
  executes. Do not hold the project mutation lock during execution.
- Store strict version-one records under
  `<data-dir>/projects/<project>/hooks/<workspace>/<event>.json` with private
  permissions and atomic durable replacement.
- Bind retry to exact source bytes, canonical hook plan, exact workspace-state
  bytes, ordered IDs, and current checkout facts. Any mismatch rejects without
  execution or mutation. Resume at the first not-durably-completed index.
- Write a running record before launch, update it after each completed hook,
  record bounded failures, write `finalizing` after the last success, and then
  remove it. A finalizing retry runs no hook.
- `--no-hooks` and clone without `--run-hooks` are intentional skips, not
  incomplete setup, and create no record.

### Public output and diagnostics

- Add hook fields only when applicable so hook-free v2 human and JSON fixtures
  remain stable.
- Dry-run exposes source, event, ID, effective repository, working directory,
  configured/resolved executable or deferred availability, literal arguments,
  timeout, and execution policy. It exposes no environment.
- A failed hook returns a typed application error that says the core operation
  completed and setup is incomplete, with stable event/ID/repository/failure
  facts and the retry command.
- Status retains its existing successful-observation exit behavior and
  unchanged normal table. It adds setup data only when a record exists.
- Doctor adds stable non-fixable findings for incomplete, stale, and invalid
  records. `doctor --fix` never changes hook state.

### Delivery boundaries

- Add no third-party dependency. Reuse Cobra, YAML, process, filesystem,
  locking, hashing, redaction, and atomic-write capabilities already present.
- Do not stage, commit, push, publish, deploy, release, edit user-global Git
  configuration, or access credentials or production systems.
- Tests are hermetic, offline, and cross-platform. POSIX-shell fixtures are
  supplemental only; required behavior uses Go helper processes or generated
  platform-native executables.
- A finding that materially expands event scope, execution authority,
  migration behavior, output persistence, or sandbox claims is outside this
  plan unless the source specification already requires it. Record it for user
  direction without consuming a remediation attempt.

## Stable contracts to establish early

| Contract | Owner and consumers | Invariant and enforcement |
|---|---|---|
| Local and portable hook schemas | `internal/config`; resolver, management, create, clone, update | v2 meaning is unchanged; only v3 accepts the exact event/source combinations. Strict decode, canonical marshal, table, fuzz, and v2 golden tests enforce it. |
| Canonical event comparison | `internal/config`; list/share/install and plan fingerprinting | Defaults are applied, order is retained, presentation is ignored, and equality is deterministic. Permutation/default/no-op tests enforce it. |
| Hook execution plan | `internal/service`; create, clone, retry, CLI renderers | One immutable plan contains authoritative source/target facts and no ambient environment. Validation and decoded JSON tests enforce it. |
| Process launch boundary | existing platform process adapters plus focused hook service; runner | Argument-array launch, direct working directory, reserved environment replacement, timeout/cancel tree termination, and stderr routing are platform tested. |
| Hook-run record | `internal/store`; runner, retry, status, doctor | Strict versioned minimal state is atomically durable and contains no command/output/environment data. Round-trip, malformed, permission, and failure-injection tests enforce it. |
| Management mutation plan | `internal/service`; hooks CLI | Share/install compare captured generations, preflight completely, lock, CAS, and atomically replace exactly one file or none. Byte-inventory and concurrent-change tests enforce it. |
| Workspace publication boundary | `internal/service`; create and clone integrations | Hooks cannot start before public state exists and the project lock is released; their failure cannot trigger rollback. Ordered event and injected-failure tests enforce it. |
| Setup diagnostics | `internal/service` and `internal/cli`; status/doctor callers | Valid, stale, and invalid records are distinguishable without changing clean legacy output or exposing sensitive fields. Contract tests enforce it. |

## Architecture and dependency boundaries

```text
internal/cli
    │ parse/render only
    ▼
internal/service
    ├── hook management plans ───────────┐
    ├── immutable execution plan         │
    ├── durable sequential runner        │
    └── create/clone/retry/diagnostics   │
             │                           │
             ├──────────► internal/config (wire schemas + canonical equality)
             ├──────────► internal/store  (minimal hook-run record)
             ├──────────► internal/git    (tracked/executable facts)
             ├──────────► internal/lock   (project and event locks)
             └──────────► process/fs adapters
```

- `internal/config` owns YAML shape, version dispatch, pure validation,
  defaulting, canonical equality, and canonical serialization. It performs no
  process execution or filesystem mutation.
- `internal/store` owns strict JSON encoding/decoding and atomic persistence
  for the hook-run record. It does not interpret hook plans or decide retry.
- `internal/service` owns portability checks requiring Git/filesystem facts,
  immutable planning, source/target correlation, management CAS operations,
  execution sequencing, fingerprints, and diagnostics.
- `internal/cli` owns flags, project/workspace resolution, progress/output
  routing, stable human/JSON rendering, and error classification. It does not
  compare YAML or launch hooks directly.
- `internal/domain.Project` and `domain.Workspace` remain topology authority;
  trusted hook declarations are passed separately and are not persisted in
  domain or workspace state.
- Extend existing process and atomic-write seams instead of creating a second
  command runner or file-publication mechanism. Pure packages must not import
  CLI or OS-process adapters.

## Global definition of done

Every approved milestone has complete RED → GREEN → REFACTOR evidence for its
changed behavior, focused hermetic success and rejection/no-mutation tests,
independent reviewer approval with no unresolved material finding, and these
main-agent checks unless a milestone names a strict focused subset before all
integration exists:

- `gofmt -w` only on Go files changed by the milestone, then `make fmt-check`.
- `go test -timeout=30m ./... -count=1`
- `go test -race -timeout=45m ./... -count=1`
- `go vet ./...`
- `make build`
- `make release-test`
- `git diff --check`

The final milestone also runs `make tutorial-test` and obtains a matching
GitHub Actions Linux, macOS, and Windows run for the exact delivered tree.
Windows runtime tests must cover launch, timeout/cancellation, atomic record
replacement, and executable resolution rather than relying on
cross-compilation.

All tests use temporary repositories, data roots, configurations,
environments, clocks, and helper processes. They require no network or
credentials, never inspect or modify user Git configuration, never use the
real user config/data directories, and assert exact bytes, modes, locks,
records, state, registry, worktrees, branches, and child-process observations
where absence or preservation matters. Version-two compatibility fixtures,
JSON keys, and error kinds are treated as public contracts.

## Risk and rollout boundaries

- This is an additive opt-in schema rollout with automatic execution only
  after local v3 consent. There is no implicit migration, feature flag, or
  dual interpretation of version two.
- Highest-risk boundaries are arbitrary program execution consent, secret
  exposure, process-tree termination, post-commit error semantics, stale retry,
  concurrent source changes, and atomic YAML/state publication. Each is
  established before its first public consumer and covered by adversarial and
  failure-injection tests.
- Existing create and clone transaction rollback remains unchanged. Hook
  tests must prove that no execution callback is reachable from a rollback
  step.
- Hook-free users must observe no output or persisted-state churn. No-op
  share/install must preserve exact bytes, timestamps where testable, and
  lifecycle versions.
- No production migration or backfill is required. A user opts in by writing
  local v3, sharing into portable v3, installing shared content, or passing
  clone `--run-hooks`.

## Milestones

### [x] M00 — Establish strict version-three hook schemas without changing version two

Specification coverage: [§4](../spec/local-workspace-lifecycle-hooks.md#4-versioned-configuration-contracts), [§5](../spec/local-workspace-lifecycle-hooks.md#5-command-and-path-resolution), and [§13](../spec/local-workspace-lifecycle-hooks.md#13-safety-and-compatibility-requirements)

Scope:

- Add local and portable version dispatch that retains exact strict v2
  behavior and adds only the specified v3 fields and event/source matrix.
- Define ordered hook declarations, pure defaults, validation, canonical event
  equality, and canonical v3 serialization in `internal/config`.
- Define pure portable command validation, leaving tracked-file and physical
  executable facts to a later service boundary.
- Preserve v2 marshal bytes and all existing init/clone/update fixtures. Do not
  change a command or execute/mutate anything in this milestone.

Test-first slices:

1. Decode, default, compare, and deterministically re-encode valid local v3
   `post-create` and portable v3 `post-clone`/`shared_hooks.post-create` across
   multiple repositories and declaration orders.
2. Reject hooks in v2; wrong-source events; reserved/unknown events; empty
   lists/commands; duplicate IDs; unknown repositories; invalid elements; and
   zero, malformed, or over-limit timeouts.
3. Prove explicit and implicit defaults compare identically, reordered hooks
   conflict, YAML presentation does not affect equality, and inputs are not
   mutated by canonicalization.
4. Run fuzz/round-trip tests over v2 and v3 and prove all existing v2 golden
   bytes and unsupported-version errors remain unchanged.

Verification:

- `go test ./internal/config -run 'Test(Local|Project|Portable|Hook|Manifest)' -count=1`
- `go test ./internal/config -run 'Fuzz' -count=1`
- Global definition-of-done commands.

Exit criteria: Both v3 schemas and canonical equality are complete pure
contracts, v2 has no second meaning or output drift, and no executable or file
mutation is reachable from configuration decoding.

### [x] M01 — Deliver atomic hook inspection, sharing, and installation

Specification coverage: [§4.4](../spec/local-workspace-lifecycle-hooks.md#44-canonical-equality), [§5](../spec/local-workspace-lifecycle-hooks.md#5-command-and-path-resolution), and [§10](../spec/local-workspace-lifecycle-hooks.md#10-hook-management-commands)

Scope:

- Add service plans for grouped list comparison, portability/tracked-file
  validation, one-event share, and all-events install with deterministic
  results.
- Add the `hooks` command group and exact list/share/install flag surfaces,
  human output, versioned JSON, help, and stable typed errors.
- Implement project-locked, exact-generation CAS, canonical serialization,
  atomic durable replacement, no-op byte preservation, and v2-to-v3 upgrade
  only on real mutation.
- Preserve unrelated manifest/local fields and local-only events. Never stage,
  commit, execute, or install from observation alone.

Test-first slices:

1. List portable, shared, and local groups deterministically and classify
   missing, identical, and conflicting definitions in human and decoded JSON
   without changing any file or lock state.
2. Share a portable event into v2/v3, prove tracked relative executable and
   portability rules, identical no-op, differing conflict, selected-event
   force replacement, and unsupported `post-clone` refusal.
3. Install absent/identical/conflicting events under default, `--force`, and
   `--missing`; prove flag exclusion, all-or-nothing default behavior, unrelated
   event preservation, and no-op version/byte preservation.
4. Inject source changes before lock, under lock, and at replacement CAS;
   inject sync/close/rename failures; prove exact old bytes and permissions or
   one complete valid new generation with no temporary artifacts.
5. Exercise nested source executables, spaces, Unicode, separators, symlink
   escape, untracked content, absolute/home/file/userinfo rejection, writer
   failures, cancellation, and JSON/stdout separation on supported platforms.

Verification:

- `go test ./internal/config ./internal/git ./internal/service ./internal/cli -run 'Test(HookList|HookShare|HookInstall|HookPortab|HooksCommand)' -count=1`
- `go test -race ./internal/service ./internal/cli -run 'Test(HookShare|HookInstall)' -count=1`
- Global definition-of-done commands.

Exit criteria: Users can inspect, share, and install definitions with exact
consent and conflict semantics; every rejected/no-op path is byte-for-byte
non-mutating and no management path can execute a hook.

### [x] M02 — Build the secret-minimal durable sequential hook runner

Specification coverage: [§6](../spec/local-workspace-lifecycle-hooks.md#6-hook-environment), [§7.3](../spec/local-workspace-lifecycle-hooks.md#73-ordering-cancellation-and-concurrency), [§8](../spec/local-workspace-lifecycle-hooks.md#8-dry-run-and-public-results), and [§9.1](../spec/local-workspace-lifecycle-hooks.md#91-record-contract)

Scope:

- Define and validate the immutable service hook plan, authoritative
  environment builder, executable availability facts, stable failure kinds,
  and deterministic public result projections.
- Extend the existing injected platform process boundary for direct working
  directory, environment replacement, stderr streaming, timeout,
  cancellation, exit status, and process-tree termination.
- Add strict minimal `internal/store` hook-run records, safe paths, private
  directory/file modes, atomic transitions, fingerprints, and dedicated
  event-lock ownership.
- Implement a reusable sequential runner with before-each-hook generation
  revalidation and finalizing cleanup. Do not connect it to create or clone in
  this milestone.

Test-first slices:

1. Build plans for plain, sibling, nested, overridden-mount, base-defaulted,
   absolute-local, source-relative, and `PATH` executables and prove plans have
   no ambient environment values.
2. Launch Go helper processes sequentially with exact literal arguments,
   target working directory, fully inherited local ordinary variables,
   allowlisted portable variables, absent portable credential/Git/HOME
   variables, overwritten reserved variables, and hook-context stderr while
   stdout remains valid.
3. Distinguish non-zero exit, missing executable, timeout, parent
   cancellation, output-writer failure, and platform child-tree termination;
   prove later hooks never start.
4. Round-trip running/failed/finalizing records, reject malformed versions,
   path IDs, inconsistent indexes/IDs, and oversized diagnostics, and assert
   that encoded bytes contain no command, path, environment, stdout, or stderr.
5. Inject every record write/sync/rename/removal and lock failure; prove a hook
   never starts before its record is durable, completed IDs advance only after
   success, finalizing retry runs no process, and concurrent runners cannot
   duplicate execution.

Verification:

- `go test ./internal/store ./internal/service -run 'Test(HookPlan|HookEnvironment|HookRunner|HookRunRecord|HookProcess)' -count=1`
- `go test -race ./internal/store ./internal/service -run 'Test(HookRunner|HookRunLock|HookProcess)' -count=1`
- Global definition-of-done commands.

Exit criteria: An injected caller can safely execute one immutable hook plan
with bounded cross-platform process behavior and minimal crash-consistent
resume state, without any core workspace integration.

### [x] M03 — Integrate trusted `post-create` after workspace publication

Specification coverage: [§7.1](../spec/local-workspace-lifecycle-hooks.md#71-local-post-create), [§8](../spec/local-workspace-lifecycle-hooks.md#8-dry-run-and-public-results), and acceptance criteria [1–5](../spec/local-workspace-lifecycle-hooks.md#15-acceptance-criteria)

Scope:

- Load local declarations separately from domain topology and extend create
  planning with fully resolved ordered `post-create` hook entries.
- Add `--no-hooks`, hook-aware dry-run human/JSON rendering, intentional-skip
  output, completed output, and typed setup-incomplete errors.
- Invoke the durable runner only after existing result validation, workspace
  state publication, and project lock release. Preserve the core create result
  on hook failure and never enter rollback.
- Keep hook-free v2 create output, transaction steps, rollback evidence, and
  existing callers compatible.

Test-first slices:

1. Dry-plan source/target pairs and environment facts for plain roots,
   siblings, nested repositories, base defaulting, mount overrides, branches,
   and HEADs; assert total filesystem/state/process non-mutation.
2. Prove missing/invalid executables and invalid local v3 fail before branch,
   worktree, ignore, state, lock-record, or hook side effects.
3. Execute multiple hooks after published state is readable and the project
   lock is acquirable by an observer; prove order, working directories,
   ignored-file setup, durable progress, and successful record cleanup.
4. Inject every core failure and rollback path and prove zero hook callbacks;
   inject every hook failure class and prove branches, worktrees, ignore
   updates, registry, and workspace state remain committed while the CLI is
   non-zero and retry evidence is present.
5. Prove `--no-hooks` validates schema but skips availability/execution,
   reports intent, creates no record, and rejects irrelevant flag
   combinations; prove hook-free v2 human and JSON goldens do not change.

Verification:

- `go test ./internal/service ./internal/cli -run 'Test(Create.*Hook|Hook.*Create|CreateNoHooks|CreateDryRun)' -count=1`
- `go test -race ./internal/service ./internal/cli -run 'Test(Create.*Hook|Hook.*Create)' -count=1`
- Global definition-of-done commands.

Exit criteria: Local post-create is a complete public vertical slice with
authoritative topology, explicit bypass, full dry-run, durable failure, and
provable separation from core transaction and rollback.

### [x] M04 — Add exact retry plus status and doctor visibility

Specification coverage: [§9.2](../spec/local-workspace-lifecycle-hooks.md#92-wtree-hooks-retry-workspace) and [§11](../spec/local-workspace-lifecycle-hooks.md#11-status-and-doctor-integration)

Scope:

- Implement `hooks retry <workspace>` resolution, exact three-digest/ID/fact
  validation, next-index resume, finalizing cleanup, and stable human/JSON
  results for local and portable record sources.
- Add read-only record inventory and validation shared by retry, status, and
  doctor without broadening authoritative workspace decoding.
- Add conditional setup projection to status and stable non-fixable doctor
  findings for resumable, stale, and invalid records.
- Preserve normal status table/JSON, exit behavior, and `doctor --fix` repair
  allowlist when no record exists.

Test-first slices:

1. Fail a later local hook, retry from its exact next index, and prove earlier
   durably completed IDs do not run; retry finalizing state performs cleanup
   only.
2. Reject missing, locked, malformed, unsupported, reordered, source-changed,
   plan-changed, state-changed, path/identity/branch/HEAD-changed, and
   concurrently changed records before process or record mutation.
3. Render valid incomplete setup in status human/JSON without non-zero drift
   semantics, and prove hook-free status output is byte-identical.
4. Render resumable, stale, and invalid doctor codes with bounded data; prove
   `--fix` performs no hook, record, config, manifest, or workspace mutation.
5. Exercise ambiguous/multiple records, partial workspaces, nested command
   invocation, writer failure, cancellation, and Windows path/case behavior.

Verification:

- `go test ./internal/store ./internal/service ./internal/cli -run 'Test(HookRetry|Status.*Hook|Doctor.*Hook|HookRunInventory)' -count=1`
- `go test -race ./internal/service ./internal/cli -run 'Test(HookRetry|HookRunInventory)' -count=1`
- Global definition-of-done commands.

Exit criteria: Every incomplete local run is either safely resumable from its
first unfinished hook or precisely rejected as stale/invalid, and read-only
diagnostics expose that distinction without changing legacy workspace health
contracts.

### [x] M05 — Deliver authorized portable `post-clone` and update preservation

Specification coverage: [§3](../spec/local-workspace-lifecycle-hooks.md#3-trust-and-source-model), [§7.2](../spec/local-workspace-lifecycle-hooks.md#72-portable-post-clone), [§12](../spec/local-workspace-lifecycle-hooks.md#12-update-preservation), and acceptance criteria [9–10](../spec/local-workspace-lifecycle-hooks.md#15-acceptance-criteria)

Scope:

- Extend clone planning with portable post-clone entries and inert shared
  entries, including deferred dry-run availability and staged tracked
  executable validation.
- Add clone `--run-hooks`, explicit unauthorized-skip output, post-publication
  runner integration, portable record fingerprints, typed incomplete setup,
  and retry compatibility.
- Prove `--run-hooks` selects only portable `hooks.post-clone`; never run or
  install `shared_hooks`, and never accept local/shared post-clone.
- Extend update v2/v3 handling so portable hook content is published as
  manifest data while existing local v3 hooks remain semantically identical
  and no hook executes or installs.

Test-first slices:

1. Dry-run local and HTTP-source v3 manifests without network-dependent
   fixtures, list portable executable plans as deferred, list shared hooks as
   inert, and prove zero process/staging/publication mutation.
2. In a real local-remote clone, validate relative tracked executables in
   private staging, publish/register first, then run authorized hooks with
   default workspace source/target equality and exact branch/HEAD facts.
3. Prove no authorization skips with no record, authorization never selects
   shared hooks, invalid staged executables prevent public publication, and
   core clone failures run nothing.
4. Fail and retry a portable hook without rerunning completed IDs or affecting
   the published project; reject exact-manifest and workspace generation
   changes.
5. Update across v2/v3 current/candidate combinations, portable hook
   additions/removals/conflicts, and local v2/v3 files; prove local hook
   semantics are preserved, shared content is never installed, no process
   starts, and rollback restores exact pre-update bytes.

Verification:

- `go test ./internal/config ./internal/service ./internal/cli -run 'Test(Clone.*Hook|Hook.*Clone|Update.*Hook|Hook.*Update|RunHooks)' -count=1`
- `go test -race ./internal/service ./internal/cli -run 'Test(Clone.*Hook|Update.*Hook)' -count=1`
- Global definition-of-done commands.

Exit criteria: Portable post-clone has an explicit one-invocation trust gate
and the same durable failure semantics as local hooks, shared hooks remain
provably inert, and update preserves the separation of portable distribution
from local consent.

### [ ] M06 — Complete documentation and cross-platform lifecycle acceptance

Specification coverage: [§§13–15](../spec/local-workspace-lifecycle-hooks.md#13-safety-and-compatibility-requirements) and the complete specification

Scope:

- Update root/command help, installed how-to, README/installation and
  troubleshooting material with v2/v3 migration, trust, direct command,
  environment, bypass, sharing, retry, idempotence, and secret limitations.
- Add one executable lifecycle tutorial/acceptance flow covering local author,
  share, clone observation, install, create, failure/status/doctor, retry,
  intentional skip, and portable authorized/unauthorized clone.
- Add decoded public-contract matrices and repository-wide no-regression
  coverage for hook-free v2, all v3 source/event combinations, and platform
  process/atomic behavior.
- Audit specification traceability and update lifecycle/status documents only
  after all implementation and hosted verification evidence is complete.

Test-first slices:

1. Exercise the complete flow with temporary local Git remotes and generated
   helper processes, asserting files, branches, HEADs, state, records, output,
   and zero network/global-config dependence at every boundary.
2. Run adversarial markers through environment, URL credentials, command
   output, literal arguments, child errors, and failing writers; prove ambient
   and credential-derived values never enter durable records or JSON, literal
   arguments appear only on the documented list/plan surfaces, and streamed
   output never enters records or structured results.
3. Run interruption and concurrency matrices at preflight, publication,
   between hooks, record finalization, share/install CAS, update rollback, and
   retry on Linux, macOS, and Windows.
4. Prove installed help/how-to examples and release artifacts expose the new
   commands and flags while every existing v2 tutorial and CLI contract still
   passes.

Verification:

- `go test ./internal/... ./cmd/... -run 'Test(Hook|LifecycleHook|Hooks|VersionTwo)' -count=1`
- `go test -timeout=30m ./... -count=1`
- `go test -race -timeout=45m ./... -count=1`
- `go vet ./...`
- `make fmt-check`
- `make build`
- `make release-test`
- `make tutorial-test`
- `git diff --check`
- Matching GitHub Actions Linux, macOS, and Windows jobs for the exact
  delivered tree.

Exit criteria: The complete specification is traceable to reviewed automated
evidence; public documentation matches delivered behavior; hook-free v2 and
hook-bearing v3 workflows pass locally and on all supported hosted platforms;
and only then are this plan and its source specification eligible for
`implemented` lifecycle status.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-29 | M00 | Strict local/portable v3 hook schemas, canonical equality/serialization, portable syntax validation, v2 compatibility, focused/fuzz, full normal/race, vet, format, build, release, and scoped diff checks passed; repository-wide diff check reports only a preserved unrelated immutable-ledger EOF blank | Approved by the normal reviewer after R1–R4 remediation; no material findings remain | None (not authorized) |
| 2026-08-30 | M01 | Atomic grouped hook inspection, tracked portable sharing, conflict-aware installation, exact-generation project authority, deterministic human/JSON contracts, no-op byte preservation, focused normal/race, full normal/race, vet, format, build, release, and scoped diff checks passed; repository-wide diff check reports only the preserved unrelated immutable-ledger EOF blank | Approved by the normal reviewer after R1–R6 remediation; no material findings remain | None (not authorized) |
| 2026-08-31 | M02 | Immutable topology-aware plans, secret-minimal local/portable environments, strict private durable run records and event locks, sequential generation-revalidating execution, bounded Unix process groups and hook-only suspended Windows Job ownership, focused normal/race, Windows test compile, full normal/race, vet, format, build, release, and scoped diff checks passed; repository-wide diff check reports only the preserved unrelated immutable-ledger EOF blank | Approved by the normal reviewer after R1–R3 remediation and final escalation of Windows pre-assignment ownership; no material findings remain | None (not authorized) |
| 2026-08-31 | M03 | Trusted local post-create planning/execution, lifecycle-owned registry CAS authority, physical source-relative executable containment, post-publication durable sequencing, explicit no-hooks bypass, bounded setup-incomplete results, escaped dry-run arguments, exact hook-free v2 compatibility, focused normal/race, full normal/race, vet, format, build, release, and scoped diff checks passed; one transient cwd-loss full-suite run was superseded by a clean retained rerun, and repository-wide diff check reports only the preserved unrelated immutable-ledger EOF blank | Approved by the normal reviewer after R1–R4 remediation; no material findings remain | None (not authorized) |
| 2026-08-31 | M04 | Exact local/portable retry authority, strict read-only run inventory, next-index/finalizing resume, byte-identical no-mutation rejection, conditional status setup projection, bounded non-fixable doctor findings, production cancellation propagation, platform-scoped path identity, focused normal/race, retained full normal/race, vet, format, build, release, and scoped diff checks passed; repository-wide diff check reports only the preserved unrelated immutable-ledger EOF blank | Approved by the normal reviewer after R1–R5 remediation and final Escalation Implementer correction of the two remaining R4 executable-check cancellation boundaries; no material findings remain | None (not authorized) |
| 2026-09-01 | M05 | Invocation-scoped portable post-clone consent, exact manifest-bound hook projection, private-stage and under-lock tracked/physical authority, post-publication durable setup recovery and retry, inert shared declarations, v2/v3 update/local-consent preservation, strict runner-result prefixes, focused normal/race, retained full normal/race, vet, format, build, release, and scoped diff checks passed; repository-wide diff check reports only the preserved unrelated immutable-ledger EOF blank | Approved by the normal reviewer after R1–R3 remediation and a narrowed R3 structured-prefix correction; no material findings remain | None (not authorized) |
