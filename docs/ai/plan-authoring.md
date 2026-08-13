# Authoring implementation plans for orchestrated delivery

This guide describes how to prepare an implementation plan that can run
unattended through this repository's implementer → reviewer → remediation
process. Its audience is a coding agent (or person) asked to write a plan,
not the agent that executes one.

The desired result is an executable specification of work: it removes routine
decisions before implementation begins, divides work into independently
verifiable milestones, and gives the orchestrator enough information to keep
moving after each approval or review rejection. A plan is not a design memo,
issue list, or aspirational roadmap.

This document is guidance for source plans under `docs/plans/`. The execution
rules in [milestone-supervision.md](milestone-supervision.md) and the durable
state rules in [run-ledger-layout.md](run-ledger-layout.md) are normative.
Where this guide conflicts with either, the normative document wins.

## The authoring objective

Write the plan so that, after a user authorizes it, the main agent can perform
this loop without asking the user for ordinary implementation decisions:

```text
read plan and evidence
  → create/update durable run ledger
  → implement one complete milestone packet test-first
  → independently review the current filesystem
  → remediate the whole finding set when needed
  → verify and approve the milestone
  → record the execution result and immediately start the next milestone
```

The plan must make each arrow actionable. It should answer all of these before
execution starts:

- What repository facts and specifications are authoritative?
- What is in scope, explicitly out of scope, and deferred?
- Which decisions are fixed, and which narrow decisions are allowed to be made
  from repository evidence?
- What can be changed in each milestone, what behavior proves it works, and
  how does it interact with prior milestones?
- Which commands prove the milestone and the final product are sound?
- What external condition is genuinely allowed to stop the run?

If the answer to a question would materially alter product behavior, public
compatibility, security, data ownership, destructive behavior, or delivery
scope, decide it in the plan or explicitly reserve it for user direction. Do
not leave it as an implicit decision for an implementer.

## Required plan shape

Place the plan at `docs/plans/<descriptive-kebab-name>.md`. Use the following
top-level sections, in this order. Headings can have additional detail, but do
not omit a required section.

```markdown
# <product or change> implementation plan

Status: ready to execute
Source of truth: <links to specifications, issues, and authoritative code>
Delivery style: test-first, one reviewed milestone at a time

## Execution contract for Codex
## Fixed implementation decisions
## Stable contracts to establish early
## Architecture and dependency boundaries
## Global definition of done
## Milestones
## Execution log
```

For a small, tightly scoped change, combine “Stable contracts” and
“Architecture” only when neither contains material decisions. For a migration,
security-sensitive feature, API change, or multi-component project, keep both
sections separate and add a dedicated risk/rollout section before milestones.

### Header metadata

The title identifies the deliverable rather than the planning activity. The
status is `ready to execute` only after the authoring checklist in this guide
passes. Until then use `draft`.

`Source of truth` links every authoritative input by repository-relative path
and, where useful, a section or heading. Include relevant existing contracts,
API definitions, architecture decision records, user requirements, and test
fixtures. Do not state that an entire large document is authoritative when a
small named section is the actual authority.

`Delivery style` must state `test-first, one reviewed milestone at a time` for
this process. If the work needs a special constraint (for example, no network,
no publishing, feature flag rollout, or compatibility window), state it in the
header or fixed decisions.

## Execution contract for Codex

Start every executable plan with a direct execution contract. It turns a plan
into a durable handoff and prevents normal internal events from ending the
run. Adapt the paths and project-specific commands, but preserve these rules:

```markdown
## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes those decisions below.

For each unchecked milestone, in order:

1. Read this plan, the relevant source-of-truth sections, the durable run
   ledger at `docs/ai/runs/<plan-basename>.md`, and the current worktree.
   Create the ledger before the first dispatch. On resumption, reconcile the
   plan, ledger, evidence, and worktree, then append a reconciliation
   checkpoint before dispatching work.
2. Derive a complete checklist for this milestone from its scope, test-first
   slices, exit criteria, documentation requirements, and verification
   commands. Record it in the current ledger entry.
3. Give the complete initial packet to `implementer`. For remediation, use
   `implementer` when the ledger attempt count is 0 or 1, and
   `escalation-implementer` only when it is 2. Require RED → GREEN → REFACTOR
   evidence, files changed, verification results, and unresolved concerns.
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
```

Do not copy this blindly if it conflicts with repository instructions. Replace
the ledger path and append any repository-specific verification, approval, or
change-control rules. The execution contract must never relax the supervision
or ledger rules.

## Fixed implementation decisions

This section is the plan's ambiguity budget: ideally, it leaves none for
routine execution. State the chosen outcome and reason for decisions that
would otherwise recur across milestones.

Typical categories are:

| Category | Plan must decide |
|---|---|
| Product behavior | User-visible workflow, accepted inputs, defaults, error behavior, and explicit non-goals. |
| Public contracts | API/CLI/schema versions, compatibility policy, output formats, exit/error taxonomy, and migration strategy. |
| Architecture | Package/module boundaries, dependency direction, source-of-truth ownership, and allowed integration points. |
| Data and migration | Schemas, versions, atomicity, rollback/recovery behavior, data retention, and upgrade/downgrade rules. |
| Security and safety | Authorization boundaries, secret handling, validation, destructive-operation guards, and audit requirements. |
| Platform and operations | Supported environments, portability expectations, offline/network policy, feature flags, rollout, and observability. |
| Delivery constraints | Files that must not change, dependencies that may/may not be added, licensing, performance targets, and publish/commit authority. |

Good decisions are concrete: “JSON errors are one documented object on stdout;
human diagnostics go to stderr” is actionable. “Provide good JSON support” is
not. A plan may allow a later milestone to revise a fixed decision only when
authoritative repository evidence makes that necessary; require the milestone
to document the reason, compatibility impact, and test update.

## Stable contracts and architecture

Establish shared, high-leverage contracts before feature milestones consume
them. Examples include domain invariants, error types, configuration precedence,
serialization formats, protocol adapters, rendering ownership, concurrency
rules, and test-fixture interfaces.

For each contract, state:

1. Its owner (package, module, service, or document).
2. Its consumers and forbidden dependency directions.
3. The observable invariant or compatibility promise.
4. The tests or checks that enforce it.
5. Its migration/versioning rule, when data or APIs are involved.

Use a compact dependency diagram when there are three or more layers. It
prevents later milestones from inventing conflicting paths around a shared
abstraction.

```text
delivery/UI → application services → domain contracts
                         ↓
               adapters: storage / network / OS
```

The diagram must match the repository, not an aspirational architecture. Put
implementation details in milestones only after their owner and dependency
direction are clear.

## Global definition of done

Provide one testable definition of done that applies to every checked
milestone. This is not a substitute for milestone exit criteria; it is the
cross-cutting floor. Include only requirements that can be evidenced.

At minimum, address:

- test-first evidence for changed behavior, including meaningful failure or
  safety cases;
- focused tests plus exact repository-wide quality commands;
- formatting, static analysis, builds, and platform/CI expectations;
- compatibility and public-contract tests where relevant;
- hermeticity and fixture isolation for filesystem, environment, network, or
  time-sensitive tests;
- atomicity, rollback/recovery, locking, authorization, or validation
  requirements for mutations and sensitive operations;
- documentation, help, migration notes, or operational guidance introduced
  with public behavior; and
- independent reviewer approval with no unresolved material findings.

Name commands exactly, including relevant arguments. “Run tests” is not
sufficient. If a command cannot be run locally, say what proves it instead
(for example, a named CI job) and why. Do not promise a command that the
repository does not provide without including the milestone that adds it.

## Designing milestones

### Properties of a good milestone

Each milestone is a complete vertical slice with a narrow enough risk surface
to implement and review in one cycle. It must have:

- a stable ID (`M00`, `M01`, …) and imperative, outcome-focused title;
- an explicit checked/unchecked Markdown checkbox in the heading;
- links to the exact source-of-truth sections it covers;
- a complete scope list, including production code, contracts, docs, and test
  support needed for that slice;
- numbered test-first slices that name success and failure/safety behavior;
- unambiguous exit criteria; and
- all milestone-specific verification commands, if they differ from the
  global definition of done.

A milestone should leave the codebase in a coherent state. It can introduce an
internal capability without a user command, but it cannot require a later
milestone to make its own claimed exit criteria true. Avoid a “foundation”
milestone that only creates empty directories or speculative abstractions;
establish an executable contract and tests in the same slice.

### Order milestones by dependency and risk

Usually order work as follows, adapting to the project:

1. Build/test quality gates and minimal executable wiring.
2. Pure domain rules, schemas, and path/value validation.
3. Adapters and hermetic fixtures for external systems.
4. Persistence, versioning, locks, and migrations.
5. Resolution/discovery and reusable service foundations.
6. Rendering/API/error contracts.
7. Planning/preflight or dry-run capabilities before mutations.
8. Mutating workflows, ordered from reversible to destructive.
9. Diagnostics, repairs, documentation/UX completion, packaging, and final
   independent acceptance review.

This order is a heuristic, not a requirement. The important rule is that a
milestone may depend only on approved earlier milestones and explicitly named
existing capabilities. Put shared high-risk invariants before the first
consumer, and prove a transaction engine with fake effects before connecting
real destructive effects.

### Split milestones when necessary

Split a milestone if any of the following is true:

- its review would require understanding unrelated subsystems;
- it contains both a new reusable abstraction and multiple independent
  consumers;
- a failure could cause data loss, security exposure, or public breakage but
  cannot be exhaustively simulated in its proposed tests;
- it combines a semantic contract change, a storage migration, and broad UI
  changes without an intermediate compatibility boundary;
- its scope cannot be expressed as one complete checklist and one finding set;
  or
- a reviewer could reasonably approve some work while rejecting the rest.

Do not split merely by directory, file count, or agent convenience. Conversely,
do not create tiny milestones that only rename code or add placeholders unless
they independently reduce risk and have a verifiable outcome.

### Milestone template

Use this shape for every milestone. Keep nouns, behavior, tests, and evidence
specific to the repository.

```markdown
### [ ] MNN — <outcome-focused title>

Specification coverage: <exact source links/sections, or “none; repository
contract: <path>”>

Scope:

- <observable behavior or invariant to implement>
- <named owner/module and integration boundary>
- <error, compatibility, persistence, security, or concurrency rule>
- <documentation/help/migration artifact required in this same milestone>
- <explicitly constrained non-goal, when needed to prevent scope drift>

Test-first slices:

1. <smallest success behavior and how it is observed>
2. <failure/invalid-input/safety behavior and expected result>
3. <boundary, integration, portability, or regression behavior>

Verification:

- `<exact focused command>`
- `<exact repository-wide command>`
- <named CI/build/manual evidence only when it cannot run locally>

Exit criteria: <all conditions that make this slice independently usable and
reviewable, including the relevant global definition of done requirements>.
```

“Test-first slices” are behavioral slices, not generic test categories. “Add
unit tests” is not a slice. “Reject a stale configuration before it mutates the
registry, then prove the registry byte-for-byte unchanged” is a slice.

For a codebase without an external specification, use existing public
contracts, issue acceptance criteria, and inspected implementation as sources
of truth. State explicitly when behavior is newly chosen by the plan.

## Verification design

Design verification while authoring each milestone, not after code exists.
Every scope bullet needs at least one evidence path: a focused test, contract
assertion, static check, black-box test, review check, or documented manual
validation. Prefer automated, deterministic evidence.

Use this mapping while reviewing the plan:

| Requirement kind | Preferred evidence |
|---|---|
| Pure rule or parser | Table-driven/unit tests plus boundary/fuzz tests if high risk. |
| Public CLI/API/output | Black-box/contract/golden or decoded structural assertions. |
| Persistent data | Versioned round trip, malformed/unknown version, crash/atomic-write, and migration tests. |
| Mutation | Preflight/no-mutation proof, injected failure points, rollback/recovery, and concurrent-use tests. |
| Filesystem/path | Hermetic temporary fixtures; spaces, Unicode, separators, containment, and symlink cases where relevant. |
| External tool/service | Adapter fakes plus bounded real integration fixtures; no dependence on user credentials or global machine state. |
| Cross-platform behavior | Named CI build/test matrix and platform-specific unit tests for divergent semantics. |
| Security/authorization | Explicit deny cases, boundary tests, audit/redaction assertions, and adversarial input tests. |

Make the expected outcome clear. A command name alone is weak evidence unless
the plan says what property it verifies. Do not use reviewers to compensate for
missing tests: reviewer inspection validates the complete milestone, but tests
must demonstrate intended behavior.

## Scope boundaries and change control

Write boundaries before implementation begins.

- List features, commands, integrations, migrations, publications, or
  destructive actions that are explicitly outside scope.
- State whether dependencies may be added and the acceptance criteria for one.
- State commit, release, deploy, data-backfill, credentials, and production
  access authority separately; code-change authorization does not imply them.
- Define the safe default for incomplete information. For example, reject,
  dry-run, preserve existing data, or require an explicit flag.
- Explain how a reviewer finding beyond plan scope is handled: determine
  whether the source plan already requires it; if not, record it and obtain a
  user decision before materially expanding scope. It must not consume a
  remediation attempt.

An external blocker is not “a test failed” or “the implementation needs more
work.” It is an unavailable credential/service/tool that cannot be safely
obtained, an irreconcilable authoritative conflict, or an unauthorized
destructive/product decision. Name likely blockers and their safe continuation
conditions when foreseeable.

## Execution log

End the plan with an append-only execution log. It is a concise summary of
approved milestones, not a live state machine and not the durable ledger.

```markdown
## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
```

The executing main agent adds a row only after approving a milestone. Include
the date, milestone ID, exact verification performed, reviewer result, and a
commit SHA only when commits are part of the authorized run. Detailed packets,
findings, attempts, checkpoints, and resume instructions belong exclusively
in `docs/ai/runs/<plan-basename>.md`.

## Plan authoring checklist

Mark the plan `ready to execute` only after every applicable item is true.

```text
[ ] The plan has a precise title, status, and links to all authoritative inputs.
[ ] Its execution contract names the plan-derived durable ledger and continuous loop.
[ ] Fixed decisions cover all material behavior, compatibility, safety, architecture, and delivery choices.
[ ] Stable contracts identify owner, consumers, invariants, and enforcement evidence.
[ ] Global definition of done contains exact executable checks or named CI evidence.
[ ] Every milestone has a unique checkbox ID, narrow outcome, complete scope, test-first slices, exit criteria, and verification.
[ ] Dependencies flow only from each milestone to approved earlier work or named existing code.
[ ] Each scope bullet maps to observable evidence, including negative/safety cases where relevant.
[ ] Mutation, persistence, security, and migration work includes preflight, failure, rollback/recovery, and compatibility evidence as applicable.
[ ] Public changes include documentation/help/contracts in the same or explicitly named later milestone, with no temporary undocumented gap where unsafe.
[ ] Out-of-scope work, authority boundaries, and genuine external blockers are explicit.
[ ] The final milestone provides acceptance/traceability evidence when the project is broad or high-risk.
[ ] The execution log skeleton is present and no run ledger has been pre-created by the plan author.
[ ] The plan does not ask the executor to make a material product or architectural choice absent new authoritative evidence.
```

## Common plan defects and repairs

| Defect | Why unattended execution stalls or degrades | Repair |
|---|---|---|
| Vague scope such as “implement authentication” | The implementer cannot form a complete packet; review scope is unknowable. | State flows, identities, storage, errors, compatibility, safety rules, and non-goals. |
| One giant “implement feature” milestone | Findings mix unrelated work and make remediation attempts meaningless. | Split at stable contracts, risk boundaries, and independently testable outcomes. |
| A milestone depends on a future abstraction | Implementation either invents incompatible interfaces or stops for a decision. | Move the contract earlier or defer the consumer. |
| Verification says “run tests” | The executor cannot prove the intended property or reproduce evidence. | Name focused and full commands plus expected coverage. |
| Tests only describe success | Safety and failure behavior is left to reviewer inference. | Add invalid-input, no-mutation, failure-injection, compatibility, and recovery slices as relevant. |
| Fixed decisions are buried in prose | Agents make different local choices across milestones. | Put them in a dedicated, concise decision section. |
| Docs and help are left to “cleanup” | Public behavior ships temporarily undocumented and may be inconsistent. | Include public artifacts in the behavior milestone or a clearly bounded UX milestone. |
| The plan says “ask if unsure” | Routine uncertainty interrupts the continuous run. | Resolve routine choices now; reserve only material authorized decisions. |
| The plan treats review rejection as a terminal event | The orchestrator stops instead of remediating. | Include the complete finding-set remediation and three-attempt semantics. |
| The execution log contains active state | Resume evidence becomes duplicated and contradictory. | Keep live state in the run ledger; log approved milestones only. |

## Prompt for a coding agent that must author a plan

Use this prompt after supplying the task, repository, and authoritative
materials:

```text
Create `docs/plans/<name>.md` as an executable implementation plan for the
repository's orchestrated milestone process. First read `AGENTS.md`,
`docs/ai/milestone-supervision.md`, `docs/ai/run-ledger-layout.md`, the
authoritative specification/requirements, and the relevant current code and
tests. Do not implement product code.

Use `docs/ai/plan-authoring.md` as the plan-authoring standard. Produce the
required plan layout: header metadata, execution contract, fixed decisions,
stable contracts, architecture/dependency boundaries, global definition of
done, checkbox milestones, and append-only execution log. Make every material
decision required for unattended execution explicit. For each milestone, give
precise source coverage, complete scope, test-first behavior slices, exact
verification, and independently reviewable exit criteria. Order milestones by
dependency and risk, and make mutation/destructive work depend on tested
planning/preflight and rollback/recovery foundations. State non-goals,
authority boundaries, and genuine external blockers.

The resulting plan must allow a main agent to initialize the required durable
ledger and execute continuously using implementer, reviewer, remediation, and
the three-rejected-complete-remediation limit without routine user questions.
Do not create the run ledger or begin execution. Before finishing, apply the
plan-authoring checklist and report any remaining material ambiguity as a
blocking question rather than silently choosing it.
```

## Final authoring test

Read the finished plan as if you are the main agent at the first unchecked
milestone. You should be able to construct a full checklist and implementation
packet without inventing behavior; instruct an independent reviewer exactly
what to inspect; identify the commands to run; and know the next permitted
action after approval or rejection. If any of those requires an ordinary user
question, the plan is not ready yet.
