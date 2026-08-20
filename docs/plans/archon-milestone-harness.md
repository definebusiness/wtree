# Archon deterministic milestone harness implementation plan

Status: initial
Source specification: [Archon deterministic milestone harness specification](../spec/archon-milestone-harness.md)
Implementation context: [Archon harness context](archon-harness-context.md)
Source idea: [Archon-based harness for deterministic milestone orchestration](../ideas/workflow/creating-a-minimal-harness-to-process-the-statemachine.md)
External contracts: [Archon DAG workflows](https://github.com/coleam00/Archon/blob/dev/packages/docs-web/src/content/docs/book/dag-workflows.md); [Archon CLI reference](https://github.com/coleam00/Archon/blob/dev/packages/docs-web/src/content/docs/reference/cli.md); [deterministic-only loop change](https://github.com/coleam00/Archon/commit/d6c102b417238803ec8582d4e49b932fdc732621)
Delivery style: test-first, one independently reviewed milestone at a time; no unreleased Archon dependency, marketplace publication, push, pull request, deployment, release, history rewrite, or multi-repository transaction

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes them below.

For each unchecked milestone, in order:

1. Read this plan, the source specification, the concise implementation
   context, the relevant current files, the durable ledger at
   `docs/ai/runs/archon-milestone-harness.md`, and the current worktree.
   Create the ledger before the first implementation dispatch. On resumption,
   reconcile the plan, ledger, referenced evidence, and filesystem before
   dispatching more work.
2. Record a complete active-milestone checklist in the ledger. Include every
   scope item, test-first slice, documentation requirement, exit criterion,
   and verification command.
3. Give the complete initial packet to the normal `implementer`. Require RED
   → GREEN → REFACTOR evidence, changed files, command results, and unresolved
   concerns. Use the normal `implementer` for remediations starting with zero
   or one rejected complete submission and the `escalation-implementer` only
   when the ledger already records two.
4. Treat partial work as progress. Request review only after every checklist
   item has complete evidence.
5. Send every complete submission to the read-only `reviewer`, which inspects
   the shared filesystem against the complete milestone, specification,
   safety, portability, compatibility, and test-quality requirements.
6. Record all material findings with stable IDs and return the entire
   unresolved set in one remediation packet. Apply the exact
   three-rejected-complete-remediation limit from
   [milestone supervision](../ai/milestone-supervision.md). Do not use an
   escalation reviewer as a routine second review.
7. After reviewer approval, run the milestone verification as the main agent,
   update affected contracts and documentation, check the milestone, append
   its execution-log row, create the next milestone ledger snapshot, and
   dispatch the next initial packet immediately.

Do not stop for ordinary test failures, reviewer findings, partial submissions,
or approved milestones. Preserve unrelated changes and do not use destructive
cleanup. Commit only when separately authorized for the repository work that
executes this plan; the product's tested commit-policy behavior does not grant
commit authority to the plan run itself. A final response is permitted only by
the durable-ledger gate in
[milestone supervision](../ai/milestone-supervision.md).

M00 has two allowed external blockers: no stable Archon release contains the
required deterministic-only loop behavior, or no reviewer profile can prove an
enforced read-only boundary. Record exact release/provider evidence and stop
before M01 rather than weakening either gate.

## Fixed implementation decisions

### Package and toolchain

- Add one isolated Bun/TypeScript package at `archon-harness/`. Do not add
  JavaScript dependencies to the Go module or move existing Go code into the
  package.
- Source installable assets below
  `archon-harness/package/.archon/`. Prefix every installed workflow,
  command, and script name with `deterministic-milestones`.
- Pin Bun in `archon-harness/package.json` and CI. Commit `bun.lock` and use
  frozen installs. Use `mdast-util-from-markdown` as the only direct production
  third-party library; pin its exact version and record all transitive licenses.
  All other production code uses Bun/Node standard APIs. Any additional direct
  dependency requires an explicit plan change.
- M00 selects and records the first stable Archon release containing
  deterministic-only `until_bash`, including artifact hashes. Development
  branches and floating `latest` downloads are forbidden.
- Add Archon/Bun jobs to the existing Linux, macOS, and Windows CI matrix
  without changing or skipping the current Go jobs.

### Invocation and isolation

- Expose `archon-milestones install|uninstall|run|doctor`. `run` validates
  input and sends canonical JSON as Archon's positional argument with an
  argument-array process spawn.
- Require effective implementer, reviewer, and escalation profile IDs from
  explicit CLI flags or a repository harness configuration file. There is no
  implicit provider/model fallback.
- Require Archon worktree isolation and an explicit branch for Git mutations.
  Reject `--no-worktree` and equivalent configuration.
- Resolve repository, plan, installation, artifact, and worktree paths
  physically. Reject traversal, symlink escape, control characters, and
  containment ambiguity before worktree creation or provider spend.
- Default `commit_policy` to `none`. Persist all effective input before
  the first AI action and reject a resume whose effective input differs.
- Compute the default operational ceiling as
  `20 + 12 × milestone_count + 2 × slice_count`. A caller may raise it but
  may not lower it below that value. It never controls remediation policy.

### State and workflow authority

- The strict `codex-milestone-markdown-v1` adapter owns conversion from the
  documented plan dialect to canonical plan JSON. The reducer consumes only
  validated canonical JSON and schema-valid events.
- Persist domain state at `$ARTIFACTS_DIR/run-state.json` with sequence,
  content hash, prior-generation hash, and retained prior generation.
- Only deterministic scripts parse plans, choose actions, validate events,
  change state, increment counters, create commits, and decide completion.
  Commands return evidence and never edit domain state.
- The Archon workflow uses one `loop_group`, no `until` signal, one
  deterministic `until_bash`, and at most one domain action per iteration.
- Schema-invalid, stale, or illegal events leave state byte-identical and
  produce a bounded diagnostic. A final-looking model response is ordinary
  node output.

### Review and remediation

- Review always uses fresh context and a capability-table-approved read-only
  profile. Unsandboxed Codex review is rejected; Codex remains allowed for
  implementation and escalation.
- Validation runs in deterministic script nodes. Reviewers inspect validation
  evidence and the filesystem without a writable shell.
- Persist the full reviewer finding set with stable IDs and send all unresolved
  findings in every remediation packet.
- Increment `remediation_cycles` on entry to remediation or escalation.
  Increment `rejected_complete_submissions` only after rejection of a
  complete remediation submission. Escalate after two; block after the
  escalation submission is rejected and the count becomes three.
- Partial submissions, intermediate failures, invalid output, and initial
  implementation rejection never increment the rejected-submission counter.

### Console and machine output

- A deterministic renderer writes one human progress line to standard error
  before every significant action or terminal outcome. It always includes
  phase, milestone or `RUN`, `remediations=N`, and
  `rejected-submissions=N/3`.
- Standard output contains one final schema-valid JSON result from the
  launcher. Capture Archon's child streams and forward only bounded, redacted
  live output to standard error; never give the child direct access to the
  launcher's standard output. Tests must prove that progress, Archon streaming,
  provider prose, and secrets cannot corrupt the final JSON.
- Bound diagnostic text and provider evidence by bytes and redact configured
  secret values before persistence or rendering.

### Commit and publication safety

- `none` never stages or commits. `milestone` commits after complete
  milestone approval. `incremental` works one declared slice at a time and
  commits only validated run-owned changes; remediation creates later focused
  commits without rewriting earlier history.
- Git operations use argument arrays and explicit pathspecs. Capture baseline,
  index, worktree, ref, and object evidence before mutation; refuse ambiguous
  ownership.
- Never push, publish, merge, rebase, squash, reset, clean, delete branches,
  open pull requests, or invoke an AI command that does so.
- Installer writes use a complete preflight plan, byte/hash comparison,
  collision refusal, temporary files, and a backup manifest. Uninstall removes
  only files still matching the installed manifest.

## Stable contracts to establish early

| Contract | Owner and consumers | Invariant and enforcement |
|---|---|---|
| Archon compatibility | `src/compatibility`; launcher, installer, CI | Only the exact pinned stable release and verified reviewer mechanisms pass preflight. Black-box conformance rejects model-looking completion and unsupported reviewer profiles before spend. |
| Canonical plan | `src/domain/plan`; initializer and packet builder | A strict Markdown adapter emits versioned ordered obligations or a no-mutation error. Parser fixtures cover malformed headings, missing fields, duplicates, ordering, paths, and Unicode. |
| Domain state and reducer | `src/domain/state` and `transition`; every workflow script | Valid old state plus one valid event yields exactly one legal next state. Table tests cover all edges, counters, stale events, and completion. |
| Persistence and recovery | `src/domain/store`; state scripts and resume | Publication is crash-consistent, sequence/hash-linked, and recoverable without trusting terminal or AI output. Failure injection covers each write boundary on supported platforms. |
| Progress protocol | `src/domain/progress`; terminal users and launcher | Every significant action prints both counters from persisted state to stderr; stdout remains final JSON only. Golden and stream-separation tests enforce it. |
| Review evidence | command schemas plus reducer; reviewer and remediation packet builders | Fresh read-only review returns one validated decision and complete stable finding set. Capability and adversarial schema tests enforce it. |
| Commit ownership | `src/git`; commit action and completion check | Only run-owned paths/hunks are staged; created SHAs are recorded; no prohibited Git operation occurs. Hermetic repositories and command-spy tests enforce it. |
| Install manifest | `src/install`; repository/global installer | Install, upgrade, and uninstall are contained, collision-safe, hash-checked, and reversible. Temporary-home fixtures enforce it. |

## Architecture and dependency boundaries

```text
archon-milestones CLI
        │
        ├── compatibility + input + install preflight
        │
        └── Archon process adapter
                  │
                  ▼
        deterministic-milestones.yaml
                  │
        ┌─────────┴──────────┐
        ▼                    ▼
AI command nodes      deterministic script nodes
evidence only         plan → state → action → transition
        │                    │
        └─────────┬──────────┘
                  ▼
        artifact state + evidence
                  │
                  ├── stderr progress
                  ├── deterministic Git adapter
                  └── final stdout result
```

- `src/domain` is pure except for injected clock/ID/hash interfaces and does
  not import Archon, Git, process, or filesystem adapters.
- `src/archon`, `src/fs`, and `src/git` adapt external effects to domain
  inputs. They may depend on domain contracts; domain code must not depend on
  them.
- Installed scripts are thin entry points into shared bundled modules. They do
  not duplicate transition or validation rules.
- AI command Markdown knows packet schemas and role rules but cannot import,
  call, or bypass persistence and Git adapters.
- Tests use temporary roots, repositories, Archon homes, artifact directories,
  and injected processes. They never read user Archon credentials,
  configuration, Git configuration, or home directories.

## Global definition of done

Every approved milestone has complete RED → GREEN → REFACTOR evidence,
independent reviewer approval with no unresolved material findings, and
hermetic tests for success plus relevant rejection, no-mutation, interruption,
secret, and portability paths.

After M00 establishes the scripts, run the applicable focused commands and:

```text
(cd archon-harness && bun install --frozen-lockfile)
(cd archon-harness && bun run format:check)
(cd archon-harness && bun run lint)
(cd archon-harness && bun run typecheck)
(cd archon-harness && bun test)
(cd archon-harness && bun run build)
(cd archon-harness && bun run test:archon)
make check
git diff --check
```

`test:archon` uses the exact pinned Archon binary and release hashes. Tests
requiring a real paid provider are excluded from the default command and run
only in the named credentialed acceptance job; all other tests are local and
network-free after frozen dependency/tool setup.

The GitHub Actions Linux, macOS, and Windows jobs must pass. State replacement,
installation, argument passing, console stream separation, and Git ownership
need runtime evidence on each platform, not cross-compilation alone.

Current specification, plan, context, package README, CLI help, schemas,
compatibility metadata, lifecycle overview, and delivered behavior must agree.
Files under `docs/ai/runs/` other than this plan's authorized ledger remain
untouched.

## Risk and rollout boundaries

- Archon compatibility is the primary external risk. M00 fails closed if the
  required stable release or read-only reviewer mechanism is unavailable.
- Filesystem atomic replacement and Git ownership differ by platform. Establish
  them with failure injection and native CI before connecting provider nodes.
- AI schema compliance is untrusted input. Preserve raw output only as bounded,
  redacted evidence and reject it before transition processing when invalid.
- Global installation can collide with user assets. Preflight and manifests
  precede all writes; upgrade and uninstall never remove unknown bytes.
- There is no production deployment or marketplace rollout in this plan.
  Installation tests target temporary roots. Real global installation,
  credentials, commits, publication, and releases require separate authority.
- A reviewer finding outside the specification is recorded for user scope
  direction and does not consume a remediation attempt.

## Milestones

### [ ] M00 — Prove and pin the Archon adoption boundary

Specification coverage: [§§3–4](../spec/archon-milestone-harness.md#3-distribution-and-invocation), [§14](../spec/archon-milestone-harness.md#14-portability-and-safety), and [§15.1–3](../spec/archon-milestone-harness.md#15-required-acceptance-evidence).

Scope:

- Create `archon-harness/` with frozen Bun metadata, test/type/lint/format/build
  scripts, pure compatibility types, and temporary-fixture helpers.
- Add exact Archon compatibility metadata: stable version, per-platform release
  hashes, deterministic-loop feature, supported reviewer mechanisms, and
  explicit unsupported versions/profiles.
- Add a minimal black-box Archon conformance workflow whose body emits
  completion-looking output while persisted state remains incomplete, then
  becomes complete only through `until_bash`.
- Add launcher preflight that checks exact version/build identity, workflow
  syntax, required runtime, worktree support, and reviewer capability before
  worktree creation or provider invocation.
- Extend CI with pinned Bun and Archon setup on Linux, macOS, and Windows while
  preserving all Go jobs.
- Do not build the product state machine in this milestone. Stop as an external
  blocker if no stable release or enforced reviewer mechanism satisfies the
  specification.

Test-first slices:

1. Make a v0.9.0 fixture and a completion-looking body output fail the
   compatibility contract; then accept only a stable release whose loop stays
   active until deterministic state is complete.
2. Make unsandboxed Codex and an unknown reviewer profile fail before a process
   spy records worktree or provider activity; accept only a capability-table
   profile with enforcement evidence.
3. Reject wrong version, wrong hash, missing Bun, missing Archon, invalid
   workflow syntax, and forbidden direct-checkout configuration with bounded
   diagnostics and no repository changes.
4. Run the minimal conformance workflow and package quality scripts on all
   supported CI platforms.

Verification:

- `(cd archon-harness && bun test test/compatibility test/preflight)`
- `(cd archon-harness && bun run test:archon -- conformance)`
- `(cd archon-harness && bun run format:check && bun run lint && bun run typecheck && bun run build)`
- Existing `make check` and `git diff --check`.

Exit criteria: an exact stable Archon release and at least one enforced
read-only reviewer mechanism are pinned and proven, adversarial completion
output cannot terminate the conformance loop, and unsupported environments
fail before mutation or provider spend.

### [ ] M01 — Establish the canonical plan and transition system

Specification coverage: [§5](../spec/archon-milestone-harness.md#5-plan-input-contract), [§6](../spec/archon-milestone-harness.md#6-authoritative-run-state), [§7](../spec/archon-milestone-harness.md#7-deterministic-state-machine), [§8](../spec/archon-milestone-harness.md#8-review-remediation-and-escalation), and [§10](../spec/archon-milestone-harness.md#10-validation-and-completion).

Scope:

- Implement the strict `codex-milestone-markdown-v1` adapter and canonical
  versioned plan schema, including hashes, obligations, commands, and source
  links.
- Implement version-one state, event, implementation-result, review-result,
  finding, validation, and final-result schemas with strict unknown-field and
  version rejection.
- Implement the pure action selector, legal transition table, stable finding
  identity, remediation counters, implementation-tier selection, milestone
  advancement, operational ceiling calculation, and completion predicate.
- Generate bounded packets for implement, validate, review, remediate,
  escalate, commit, and advance actions from canonical state.
- Do not add filesystem persistence, real commands, provider execution, or Git
  mutation in this milestone.

Test-first slices:

1. Parse a minimal valid plan and reject duplicate/out-of-order IDs, malformed
   checkboxes, missing required blocks, empty commands, unsupported status,
   invalid UTF-8/control characters, and checked-after-unchecked ambiguity.
2. Table-test every permitted state/event edge and require byte-identical old
   state for illegal, stale-sequence, wrong-milestone, incomplete, and
   schema-invalid events.
3. Prove initial review rejection enters remediation with
   `remediations=1, rejected=0`; complete remediation rejections advance to
   `1`, `2`, then escalation and blocked `3`; prove all non-counting events
   leave the rejected counter unchanged.
4. Prove final-looking prose cannot affect action or completion and that
   completion remains false for each individually missing obligation, failed
   validation, unresolved finding, commit requirement, or required action.

Verification:

- `(cd archon-harness && bun test test/plan test/schema test/transition test/completion)`
- `(cd archon-harness && bun run typecheck && bun run lint)`
- Global definition-of-done commands.

Exit criteria: pure deterministic code can turn the defined Markdown dialect
into canonical obligations and select every next action, counter, and terminal
outcome without Archon, provider, filesystem, or Git authority.

### [ ] M02 — Make state, resume, and progress crash-consistent

Specification coverage: [§6](../spec/archon-milestone-harness.md#6-authoritative-run-state), [§9](../spec/archon-milestone-harness.md#9-human-readable-terminal-output), [§11](../spec/archon-milestone-harness.md#11-resume-and-idempotency), and [§13](../spec/archon-milestone-harness.md#13-outcomes-and-process-exit).

Scope:

- Implement artifact-directory initialization, state generation validation,
  same-directory temporary publication, flush/replace, prior-generation
  retention, hash chaining, and recovery.
- Implement idempotency keys and reconciliation records for every action type,
  with injected clock, ID, filesystem, process, and interruption seams.
- Implement deterministic stderr progress rendering and final stdout JSON,
  including byte bounds and secret redaction.
- Add thin named Archon script entry points for initialize, select action,
  apply result, check completion, print progress, and reconcile.
- Do not dispatch AI nodes or create Git commits in this milestone.

Test-first slices:

1. Interrupt before temp creation, after write, after flush, before replacement,
   and after replacement; recover the latest valid sequence without accepting a
   partial, corrupt, unlinked, or future-version state.
2. Replay every action idempotency key and prove no duplicate transition,
   counter, finding, evidence, or terminal outcome; reject changed plan,
   profile, repository, worktree, or commit-policy identity on resume.
3. Golden-test every action and outcome line with both counters at zero and
   non-zero values, including escalation and block; prove values come from
   state and secrets/prose are redacted and bounded.
4. Prove concurrent stdout/stderr capture leaves exactly one schema-valid final
   object on stdout and human progress on stderr on Linux, macOS, and Windows.

Verification:

- `(cd archon-harness && bun test test/store test/recovery test/progress test/streams)`
- `(cd archon-harness && bun run test:platform)`
- Global definition-of-done commands.

Exit criteria: persisted state is the recoverable authority across every
injected interruption, retries are idempotent, and users always see accurate
phase and remediation counts without corrupting machine output.

### [ ] M03 — Connect bounded AI actions to the Archon loop

Specification coverage: [§4](../spec/archon-milestone-harness.md#4-archon-adoption-gates), [§7](../spec/archon-milestone-harness.md#7-deterministic-state-machine), [§8](../spec/archon-milestone-harness.md#8-review-remediation-and-escalation), [§10](../spec/archon-milestone-harness.md#10-validation-and-completion), and [§15.11](../spec/archon-milestone-harness.md#15-required-acceptance-evidence).

Scope:

- Author the production `deterministic-milestones.yaml` with one
  deterministic-only `loop_group`, conditional one-action routing, required
  worktree isolation, high operational ceiling, and no model completion signal.
- Author reusable implementation, review, remediation, and escalation command
  Markdown with fresh-context review, complete packet input, schema-valid
  evidence output, bounded prose, and no transition/commit authority.
- Connect deterministic validation commands, schema validation, evidence
  persistence, reducer application, progress-before-action, and
  completion-after-transition.
- Enforce reviewer capability at both launcher preflight and node dispatch.
  Preserve the complete unresolved finding set and select escalation only from
  the persisted counter.
- Add test workflow variants that substitute deterministic fixtures for paid
  providers and one credentialed acceptance path for the supported reviewer.

Test-first slices:

1. Drive implement → validate → review → approve → advance with fixture nodes;
   assert exact node order, one action per iteration, fresh review context, and
   deterministic completion.
2. Make agents emit `COMPLETE`, invalid JSON, wrong schema versions, partial
   submissions, stale findings, and contradictory decisions; require retry or
   fatal evidence according to schema without unauthorized advancement.
3. Drive initial findings, two normal rejected complete remediations,
   escalation, approval, and third-rejection block; assert complete finding
   packets, profile selection, counters, console lines, and absence of a fourth
   remediation.
4. Attempt reviewer filesystem mutation in the supported credentialed
   read-only profile and require enforcement; prove unsupported Codex review is
   rejected before invocation.

Verification:

- `(cd archon-harness && bun test test/workflow test/commands test/review)`
- `(cd archon-harness && bun run test:archon -- workflow)`
- Named credentialed reviewer-isolation acceptance job.
- Global definition-of-done commands.

Exit criteria: the pinned Archon workflow continuously routes bounded AI
evidence through deterministic transitions, independently reviews current
files, enforces the remediation limit, and cannot be ended by model output.

### [ ] M04 — Enforce deterministic commit policies

Specification coverage: [§12](../spec/archon-milestone-harness.md#12-commit-policies), [§14](../spec/archon-milestone-harness.md#14-portability-and-safety), and [§15.8](../spec/archon-milestone-harness.md#15-required-acceptance-evidence).

Scope:

- Implement injected Git argument-array operations, baseline and worktree
  identity capture, path/hunk ownership classification, explicit staging,
  commit creation, SHA verification, residual-run-change checks, and
  reconciliation after interruption.
- Implement `none`, post-approval `milestone`, and validated-slice
  `incremental` action routing and state evidence.
- Refuse dirty or ambiguous ownership, index interference, ref movement,
  symlink/path escape, hooks, commit failure, and resume mismatch without
  staging unrelated bytes.
- Add command-spy deny tests for push, publish, merge, rebase, squash, reset,
  clean, branch deletion, broad staging, and shell interpolation.
- Connect commit satisfaction to milestone advancement and final completion.

Test-first slices:

1. Prove `none` never invokes staging/commit and still completes when all
   non-commit obligations pass.
2. In temporary repositories, prove `milestone` creates exactly one
   post-approval commit containing only run-owned changes and records its SHA;
   make ambiguity or commit failure keep the milestone incomplete.
3. Prove `incremental` commits declared slices only after their validation,
   adds focused remediation commits, never rewrites prior commits, and requires
   no residual run-owned changes at completion.
4. Inject interruption at index update, commit object creation, ref update,
   state publication, and resume; reconcile exactly once without duplicate
   commit or loss of unrelated changes on every supported platform.

Verification:

- `(cd archon-harness && bun test test/git test/commit-policy test/commit-recovery)`
- `(cd archon-harness && bun run test:platform -- git)`
- Global definition-of-done commands.

Exit criteria: every commit policy is deterministic, idempotent, ownership-safe,
and unable to push or rewrite history, and commit obligations participate in
the completion predicate.

### [ ] M05 — Package, install, and prove portable operation

Specification coverage: [§3](../spec/archon-milestone-harness.md#3-distribution-and-invocation), [§13](../spec/archon-milestone-harness.md#13-outcomes-and-process-exit), [§14](../spec/archon-milestone-harness.md#14-portability-and-safety), [§15](../spec/archon-milestone-harness.md#15-required-acceptance-evidence), and [§16](../spec/archon-milestone-harness.md#16-explicit-non-goals).

Scope:

- Complete `install`, `uninstall`, `doctor`, and `run` with repository
  and global scopes, immutable install plans, collision-safe upgrades, backup
  manifests, hash-owned uninstall, exact Archon spawn, signal forwarding, and
  domain exit mapping.
- Add black-box fixtures for two unrelated target repositories with different
  validation commands and implementation profiles. Prove the installed global
  package works without importing `wtree` or assuming Go.
- Add full workflow acceptance for success, validation retry, remediation,
  escalation block, cancellation, fatal ceiling, resume, all commit policies,
  malformed plans, secret redaction, and installation recovery.
- Document prerequisites, pinned compatibility, profile configuration, plan
  dialect, installation, invocation, progress format, outcomes, resume,
  commit safety, limitations, troubleshooting, and upgrade procedure.
- Finish the cross-platform CI matrix and audit package/distribution contents,
  licenses, generated artifacts, and repository lifecycle metadata.
- Do not install globally on the developer machine, publish a package or
  marketplace entry, use real user credentials outside the named acceptance
  job, push, open a pull request, deploy, or release.

Test-first slices:

1. Install into temporary repository and global roots, detect every collision,
   upgrade only a known prior manifest, recover injected publication failure,
   and uninstall only unchanged owned files.
2. Launch through paths with spaces and Unicode, pass canonical JSON without
   shell expansion, forward cancellation, map all domain outcomes, and preserve
   stdout/stderr separation.
3. Run the package against non-Go and Go fixture repositories with different
   plan adapters/commands, resume after each action boundary, and prove no
   target-specific import or configuration assumption.
4. Run the full conformance and acceptance matrix on Linux, macOS, and Windows;
   audit documentation and installed bytes against the specification and
   explicit non-goals.

Verification:

- `(cd archon-harness && bun test)`
- `(cd archon-harness && bun run test:integration)`
- `(cd archon-harness && bun run test:archon)`
- `(cd archon-harness && bun run test:platform)`
- `(cd archon-harness && bun run format:check && bun run lint && bun run typecheck && bun run build)`
- `make check`
- `git diff --check`
- Exact Linux, macOS, and Windows CI run plus named credentialed reviewer
  acceptance evidence.

Exit criteria: the versioned package installs safely in repository or global
scope, runs unchanged across unrelated repositories, prints continuous
human-readable progress with both remediation counters, resumes safely, and
satisfies every specification acceptance condition and non-goal.

## Execution log

Append one concise row only after a milestone is independently approved and
verified. Detailed active, resume, remediation, and finding evidence belongs
only in the durable run ledger.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
