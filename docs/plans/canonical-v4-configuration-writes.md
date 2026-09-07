# Canonical v4 configuration writes implementation plan

Status: initial
Source specification: [Canonical v4 configuration writes specification](../spec/canonical-v4-configuration-writes.md)
Source idea: none (created directly)
Delivery style: test-first, one independently reviewed milestone at a time; preserve legacy read compatibility, exact Git-owned manifest bytes, and existing transaction boundaries; no commit, push, pull request, publication, or release without separate authorization
Execution readiness: ready to execute

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not stop at milestone
boundaries or ask for routine implementation decisions fixed below.

For each unchecked milestone, in order:

1. Read this plan, the source specification, the relevant current files, the
   durable ledger at `docs/ai/runs/canonical-v4-configuration-writes.md`, and
   the current worktree. Create the ledger before the first implementation
   dispatch and reconcile it on every resume.
2. Record the complete active-milestone checklist in the ledger, including all
   scope, test-first slices, documentation, exit criteria, and verification.
3. Give the complete initial packet to the normal `implementer`. Require RED
   -> GREEN -> REFACTOR evidence, changed files, command results, and
   unresolved concerns. Preserve unrelated worktree changes.
4. Treat partial work as progress and request review only after the full
   milestone submission is evidenced.
5. Send each complete submission to the read-only `reviewer` for inspection
   against the whole milestone, specification, compatibility, safety,
   portability, and test-quality contract.
6. Record findings with stable IDs and return the entire unresolved set in one
   remediation packet. Apply the exact remediation and escalation limits from
   [milestone supervision](../ai/milestone-supervision.md).
7. After reviewer approval, verify as the main agent, update documentation and
   lifecycle evidence, check the milestone, append one execution-log row,
   establish the next ledger checkpoint, and continue immediately.

Do not stop for ordinary test failures, reviewer findings, remediation, or an
approved milestone. Do not use destructive cleanup. A final response is
permitted only by the durable-ledger gate in
[milestone supervision](../ai/milestone-supervision.md).

## Fixed implementation decisions

### Version policy

- Keep strict version-specific readers for local and portable versions two,
  three, and four. Keep rejecting version one.
- Establish explicit current-writer constants whose value is four. Preserve
  separately named version-two and version-three schema identifiers for
  decoding, fixtures, and explicit legacy encoding.
- Do not make generic marshal helpers silently upgrade their inputs. Every
  production service that owns a new/replacement generation sets the current
  write version before encoding.
- Version four remains valid with empty hook maps and no companion role.
- Do not change unrelated document versions.

### Artifact ownership

- Init authors both the local config and portable manifest as version four.
- Clone and release materialization author a version-four local config. Their
  tracked portable manifest is authenticated Git-owned input and remains
  byte-identical, including when it is version two or three.
- Update publishes a version-four local config. Its candidate portable
  manifest remains the exact plan-bound Git generation.
- Preserve the existing deterministic init project-ID algorithm. A writer
  constant rename or output-format change must not change IDs for identical
  repository identity facts.

### Upgrade timing

- Upgrade version two or three only when the owning operation already replaces
  that exact file for its normal product behavior.
- Preserve byte-identical no-op contracts. In particular, hook install/share
  with no effective change remains non-writing and non-migrating.
- A failed, rejected, dry-run, planning, or read-only operation never migrates
  either file.
- Preserve all fields not intentionally changed by the owning operation,
  including local hooks during update and hook-management migrations.

### Safety and scope

- Preserve all current locks, generation checks, modes, atomic publication,
  rollback, recovery, cleanup, exact-byte checks, and credential redaction.
- Portable/shared hook declarations gain no execution authority from a version
  change.
- No migration command, background migration, or load-time write is added.
- No dependency, network protocol, Git history operation, or public command
  result version is added by this plan.

## Stable contracts to establish early

| Contract | Owner and consumers | Invariant and enforcement |
|---|---|---|
| Supported-read set | `internal/config`; all loaders and services | Local and portable v2/v3/v4 decode through their strict schemas; v1 and unknown versions fail. Focused table/fuzz tests enforce version and unknown-field boundaries. |
| Current-write selection | `internal/config` constants plus service write boundaries | Current output is v4; legacy schema constants are never mistaken for the production writer default. Compile-time usages and writer matrix tests enforce the distinction. |
| Explicit encoder version | `MarshalProject`, `MarshalPortableManifest`; services and tests | Marshaling honors a valid caller-supplied version and never performs hidden migration. Service tests prove that production writers select v4 before calling it. |
| Git-owned portable bytes | clone, release materialization, and update | Consumed/tracked manifests retain exact source or candidate bytes and hashes; only newly authored local configs move to v4. Existing exact-byte transaction tests remain authoritative. |
| Upgrade-on-write | config mutations, hook management, update, repository baseline mutation | An actual replacement emits v4 and preserves unrelated semantic fields; no-op/read/failure generations remain exact. Cross-version mutation matrices enforce it. |
| Stable init identity | init identity derivation and registry consumers | Identical repository identity facts produce the same project ID before and after the writer-policy change. Golden regression evidence prevents accidental namespace drift. |

## Architecture and dependency boundaries

```text
strict v2/v3/v4 bytes
          |
          v
version-specific decoder -----> in-memory config/manifest
                                      |
                         read-only ---+--- no write
                                      |
                       owned mutation/new generation
                                      |
                                      v
                         select current version (v4)
                                      |
                                      v
                         explicit canonical encoder
                                      |
                                      v
                    existing atomic transaction boundary
```

- `internal/config` owns schema identifiers, strict decoding, validation, and
  explicit-version canonical encoding. It performs no I/O during load/marshal.
- Each service owns the decision that a write is legitimate and applies the
  current write version at that boundary.
- Clone, release, and update Git adapters remain responsible for exact tracked
  manifest authority. Version policy must not move that responsibility into a
  generic serializer.
- Registry, workspace state, locks, and recovery records remain downstream
  transaction participants but do not adopt configuration version four.
- Tests use temporary files/repositories and injected services. They do not
  read or mutate user configuration, credentials, SSH agents, or home data.

## Global definition of done

Every approved milestone has complete RED -> GREEN -> REFACTOR evidence,
independent reviewer approval with no unresolved material findings, and tests
for success, strict rejection, no-mutation, rollback, and relevant cross-
version behavior.

For each milestone, run its focused tests and the applicable repository gates:

```text
go test ./internal/config
go test ./internal/service
go test ./internal/cli
go test ./...
go test -race ./...
go vet ./...
gofmt -l .
make build
make test-release-layout
git diff --check
```

Use repository-native faster verification only where current plan-authoring
guidance permits it, and record the exact command/evidence in the durable
ledger. Before completion, the full normal/race, vet, format, build, release-
layout, and relevant Linux/macOS/Windows gates must agree with the policy.

The source specification, this plan, status overview, user documentation,
code comments, fixtures, and delivered behavior must use the same supported-
read/current-write vocabulary.

## Risk and rollout boundaries

- The largest compatibility risk is accidentally replacing strict legacy
  decoding with a permissive v4 decode. Version-specific negative fixtures
  remain mandatory.
- The largest integrity risk is canonicalizing a tracked manifest that clone,
  release, or update promises to preserve exactly. Test both decoded semantics
  and raw bytes/hashes.
- Updating the init serialization can accidentally alter deterministic project
  IDs. Freeze existing identity vectors before changing writer constants.
- Upgrade-on-write changes canonical YAML bytes and can expose missing field-
  preservation in mutation code. Test complete values across v2/v3/v4 inputs,
  not only the version field.
- A no-op must not become a write solely for migration. Retain byte, mode, and
  where supported timestamp evidence for existing no-op contracts.
- This is an intentional forward-only writer rollout: files written as v4 are
  not readable by older binaries that only support v2/v3. No dual-write or
  downgrade mechanism is required.

## Milestones

### [ ] M00 — Separate legacy read schemas from the current v4 writer contract

Specification coverage: [§§3–4](../spec/canonical-v4-configuration-writes.md#3-version-vocabulary), [§7](../spec/canonical-v4-configuration-writes.md#7-data-preservation-and-safety), and acceptance criteria [1–2, 9–10](../spec/canonical-v4-configuration-writes.md#9-acceptance-criteria).

Scope:

- Add unambiguous constants/names for local and portable v2, v3, v4, and the
  current write version without changing accepted wire values.
- Update config comments and production call sites that currently treat the v2
  constant as both schema identity and writer default.
- Preserve strict v2/v3 wire structs and full v4 decoding. Preserve explicit
  legacy marshal capability and validate hook-free/companion-free v4 values.
- Freeze deterministic init identity vectors before refactoring version names;
  retain the existing identity namespace and inputs.
- Inventory every production local/portable marshal and write call. Classify
  each as explicit legacy support, new generation, owned replacement, or
  exact-input verification and record the matrix in tests or focused code
  comments.
- Do not change service output versions in this milestone beyond what is needed
  to introduce the contract safely.

Test-first slices:

1. Table-test local and portable v1/v2/v3/v4/unknown inputs, including each
   schema's forbidden known fields and unknown fields; prove only v2/v3/v4
   valid inputs load.
2. Add hook-free and companion-free v4 round trips plus explicit v2/v3 marshal
   round trips; prove encoders do not mutate caller versions.
3. Add golden init-identity vectors before refactoring and prove the constant
   split leaves every project ID unchanged.
4. Add a writer-usage guard or focused test matrix that fails when a production
   creation/replacement path uses the legacy default accidentally.

Verification:

- `go test ./internal/config`
- Focused init identity and writer-policy tests in `./internal/service`.
- `go test ./...`
- `go test -race ./internal/config ./internal/service`
- `go vet ./...`, `gofmt -l .`, and `git diff --check`.

Exit criteria: version terminology is unambiguous, strict read compatibility
and explicit legacy encoding are locked by tests, v4 needs no optional feature,
and deterministic project identity is unchanged.

### [ ] M01 — Emit v4 from every new project materialization

Specification coverage: [§5](../spec/canonical-v4-configuration-writes.md#5-new-generation-policy), [§7](../spec/canonical-v4-configuration-writes.md#7-data-preservation-and-safety), and acceptance criteria [3–5, 8–9](../spec/canonical-v4-configuration-writes.md#9-acceptance-criteria).

Scope:

- Make init plan, dry-run, execution, and CLI rendering use v4 for both the
  local config and newly authored portable manifest.
- Make clone always construct a v4 local config for v2/v3/v4 source manifests,
  with and without hooks/companions.
- Make release materialization always construct a v4 local config for each
  supported manifest version and release-lock combination.
- Remove lowest-representable-version selection from these paths while
  retaining feature validation.
- Preserve byte-identical tracked manifest verification in clone and release,
  along with modes, selected commits, hashes, cleanup, rollback, registry, and
  workspace-state publication.
- Update fixtures whose only assumption was that new hook-free projects use
  v2. Keep explicit legacy fixtures for reader compatibility.

Test-first slices:

1. Change init expectations to v4 locally and portably; cover plan/dry-run and
   execution, deterministic bytes, no optional fields, and stable project ID.
2. Matrix clone over v2/v3/v4 manifests and optional hooks/companions; require
   local v4 and raw tracked-manifest equality before and after success.
3. Matrix release materialization over supported portable versions; require
   local v4, exact manifest/lock authentication, and unchanged release inputs.
4. Inject rejection/failure at existing publication boundaries and prove no
   partial migration, exact cleanup/rollback, and unchanged modes/registry/
   workspace state.

Verification:

- Focused init, clone plan/execution, and release-materialization tests in
  `./internal/service` and matching CLI tests.
- `go test ./internal/config ./internal/service ./internal/cli`
- `go test -race ./internal/service ./internal/cli`
- Global definition-of-done commands.

Exit criteria: every successful new init, clone, and release materialization
authors its in-scope configuration generations as v4, while Git-owned portable
inputs and all transaction guarantees remain exact.

### [ ] M02 — Upgrade owned rewrites, preserve no-ops, and close documentation

Specification coverage: [§6](../spec/canonical-v4-configuration-writes.md#6-upgrade-on-write-policy), [§§7–8](../spec/canonical-v4-configuration-writes.md#7-data-preservation-and-safety), and acceptance criteria [6–10](../spec/canonical-v4-configuration-writes.md#9-acceptance-criteria).

Scope:

- Make update publication always encode its replacement local config as v4
  across supported current/candidate version combinations. Preserve local
  hooks and all unrelated semantic fields.
- Preserve update's exact candidate-manifest bytes, plan digest, Git checkout,
  journal, reconciliation, state/registry publication, rollback, and recovery.
- Upgrade project-scoped config set/unset replacements from v2/v3 to v4.
- Upgrade changed hook install/share generations from v2/v3 to v4. Preserve
  unchanged/rejected hook operations byte-for-byte and retain hook consent and
  portability checks.
- Audit all remaining local/portable replacement paths, including repository
  baseline mutation, and require v4 wherever a replacement is actually
  authored. Do not weaken existing v4 prerequisites for companion operations.
- Add explicit read-only/no-migration coverage for resolution, config get/list,
  status, doctor, planning, dry-run, and hook inventory.
- Update README and relevant format/command documentation to say current
  writers emit v4, legacy v2/v3 remain readable, and v1 remains rejected.
  Update lifecycle/traceability evidence required by repository policy.

Test-first slices:

1. Matrix update publication across current local v2/v3/v4 and candidate
   portable v2/v3/v4; assert local v4, exact candidate bytes, semantic hook/
   topology preservation, and unchanged rollback generations on failure.
2. Matrix project config set/unset and changed hook install/share over legacy
   inputs; assert v4 canonical replacements with complete semantic preservation.
3. Exercise semantic no-op, conflict, cancellation, stale-generation, dry-run,
   and read-only paths; assert exact bytes, modes, timestamps where stable,
   registry/state files, Git state, and zero hook execution.
4. Complete a production write-call audit and add a regression that detects a
   remaining lowest-version writer. Run documentation examples and portable
   cross-platform tests.

Verification:

- Focused update-publication, config-service, hooks-management,
  repository-branch, status/doctor, and CLI tests.
- `go test ./internal/config ./internal/service ./internal/cli`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `gofmt -l .`
- `make build`
- `make test-release-layout`
- `make check`
- `git diff --check`
- Matching Linux, macOS, and native Windows CI evidence for the final tree when
  publication of that tree is separately authorized.

Exit criteria: every successful production replacement emits v4, update
publication and all actual legacy mutations upgrade safely, no-op/read/failure
paths remain non-migrating, documentation agrees, and all acceptance criteria
have independently reviewed verification evidence.

## Execution log

Append one concise row only after a milestone is independently approved and
verified. Detailed active, resume, remediation, and finding evidence belongs
only in the durable run ledger.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
