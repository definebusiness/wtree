# Archon deterministic milestone harness specification

Status: planned
Source idea: [Archon-based harness for deterministic milestone orchestration](../ideas/workflow/creating-a-minimal-harness-to-process-the-statemachine.md)
Implementation plan: [Archon deterministic milestone harness implementation plan](../plans/archon-milestone-harness.md)

## 1. Purpose

This specification defines a portable Archon workflow package that executes a
milestone implementation plan continuously through implementation, validation,
independent review, remediation, escalation, commit handling, and deterministic
completion.

Archon owns workflow scheduling, provider dispatch, worktree isolation,
streaming, artifacts, and run resumption. The package owns the domain state
machine. AI output is evidence submitted to that state machine; it cannot
select a transition or terminate the run.

## 2. Scope and product boundary

The deliverable consists of:

- a Bun/TypeScript launcher and installer;
- one Archon `loop_group` workflow;
- reusable implementation, review, remediation, and escalation commands;
- deterministic plan-adapter, state, transition, progress, recovery, and Git
  commit scripts;
- schemas and compatibility metadata;
- automated conformance, unit, integration, and portability tests; and
- installation and operating documentation.

This is not a new general-purpose workflow engine. It must use documented
Archon interfaces and must not query or modify Archon's database.

The package must not import `wtree` code, assume that a target repository uses
Go, or hard-code this repository's validation commands.

## 3. Distribution and invocation

### 3.1 Source package

The repository should contain one self-contained source package:

```text
archon-harness/
├── package.json
├── bun.lock
├── src/
│   ├── cli.ts
│   ├── install.ts
│   └── domain/
├── package/
│   └── .archon/
│       ├── workflows/deterministic-milestones.yaml
│       ├── commands/deterministic-milestones/
│       └── scripts/deterministic-milestones/
├── schemas/
└── test/
```

Names installed into a shared Archon scope must use the
`deterministic-milestones` prefix. Installation must refuse an overwrite
unless the existing file is byte-identical or the caller explicitly requests a
package upgrade from an identified earlier package version.

### 3.2 Installation scopes

The installer must support:

- repository scope: `<target>/.archon/{workflows,commands,scripts}`; and
- global scope: `$ARCHON_HOME/{workflows,commands,scripts}`, with Archon's
  documented default used when `ARCHON_HOME` is unset.

Installation must first compute and print an immutable file plan. It must
validate containment, collisions, package version, and all source hashes before
writing. A failed installation must leave the destination either entirely
unchanged or recoverable through an automatically retained backup manifest.

Marketplace publication may be added later, but it is not required for the
first release.

### 3.3 Typed launcher

The public launcher is `archon-milestones`. A normal invocation is:

```text
archon-milestones run \
  --repo /path/to/repository \
  --plan docs/plans/change.md \
  --branch archon/change \
  --commit-policy none
```

The launcher must validate typed arguments and provider capabilities before
starting Archon. It passes one canonical JSON object as Archon's positional
workflow argument using a process argument array, never shell interpolation.

Required and optional inputs are:

| Input | Rule |
|---|---|
| `repo` | Required target root; resolve physically and require the selected repository mode. |
| `plan` | Required path contained by `repo`. |
| `branch` | Required for every mutating Git run. |
| `plan_adapter` | Defaults to `codex-milestone-markdown-v1`. |
| `commit_policy` | `none`, `milestone`, or `incremental`; defaults to `none`. |
| implementer, reviewer, and escalation profiles | All three effective profile IDs are required through flags or repository harness configuration; no implicit provider/model fallback is allowed. |
| `max_iterations` | Defaults to `20 + 12 × milestone_count + 2 × slice_count`; a caller may raise but not lower it. It is not a remediation limit. |

Unknown inputs, duplicate inputs, unsupported enum values, unsafe paths,
unavailable profiles, and an incompatible Archon version must fail before
worktree creation or any paid provider invocation.

## 4. Archon adoption gates

### 4.1 Deterministic-only loop completion

Archon v0.9.0 is unsupported because its loop completion is the logical OR of
the model's `until` signal and `until_bash`. The package must pin the first
stable Archon release that accepts a `loop_group` with `until_bash` and no
model `until` signal.

The pin consists of an exact semantic version, release artifact hashes for
supported platforms, and the result of the package conformance suite. An
Archon upgrade is a reviewed package change and must rerun that suite.

The installed workflow must contain no model-controlled completion token.
Given incomplete persisted state, output such as `COMPLETE`, `DONE`,
`APPROVED`, or final-looking prose from any body node must not end the loop.

### 4.2 Enforced read-only review

The reviewer must run with fresh context and an independently enforced
read-only tool/filesystem boundary. A prompt is not an enforcement boundary.

The package's capability table must identify the reviewer mechanisms verified
against the pinned Archon release. A provider/profile absent from that table
must be rejected before the first provider invocation. Codex review is
unsupported while Archon does not enforce its workflow tool restrictions and
no separate read-only sandbox or adapter is configured.

Implementation and escalation profiles may use Codex because those roles are
allowed to mutate the isolated worktree.

The workflow must execute validation in deterministic non-AI nodes. The
reviewer may read their evidence but must not need a writable shell.

## 5. Plan input contract

The built-in `codex-milestone-markdown-v1` adapter accepts a defined Markdown
dialect rather than arbitrary prose. It requires:

- one lifecycle `Status:` line;
- milestone headings of the form
  `### [ ] MNN — <title>` or `### [x] MNN — <title>`;
- within every unchecked milestone, non-empty `Scope:`,
  `Test-first slices:`, `Verification:`, and `Exit criteria:` fields;
- unique milestone IDs in document order; and
- no checked milestone after an unchecked milestone unless the adapter is
  explicitly running in resume/reconciliation mode.

The adapter produces a canonical versioned plan object containing ordered
milestones, scope items, numbered slices, verification commands, exit
criteria, documentation obligations, and source links.

An alternate adapter may be selected by name. It must emit the same canonical
schema and pass the same validator. Adapter output, not a heuristic parse of
agent prose, is the only plan input accepted by the state machine.

## 6. Authoritative run state

Each Archon run owns a versioned state file under its artifact directory:

```text
$ARTIFACTS_DIR/run-state.json
```

The minimum state is:

```json
{
  "version": 1,
  "run_id": "<archon-run-id>",
  "sequence": 0,
  "outcome": "EXECUTING",
  "current_milestone": "M00",
  "phase": "IMPLEMENTING",
  "remaining_milestones": ["M00"],
  "remaining_scope_items": ["M00-S1"],
  "remaining_acceptance_criteria": ["M00-E1"],
  "open_findings": [],
  "remediation_cycles": 0,
  "rejected_complete_submissions": 0,
  "implementation_tier": "normal",
  "commit_policy": "none",
  "commit_state": "NOT_REQUIRED",
  "created_commits": [],
  "required_action": "IMPLEMENT",
  "story_complete": false
}
```

The complete schema must also retain:

- the canonical plan hash and package/Archon versions;
- target repository and worktree identities;
- effective provider profiles and capability evidence;
- baseline Git status and ownership evidence;
- completed slice and validation evidence;
- stable review finding IDs and their state;
- action idempotency keys; and
- the last transition and diagnostic.

State updates must validate the old state, requested event, legal transition,
and resulting state before publication. Publication uses a same-directory
temporary, flush, atomic replacement, and retained prior generation. Recovery
selects only a schema-valid generation whose sequence and hash chain agree.
Terminal output and Archon's database are not state inputs.

## 7. Deterministic state machine

The workflow may request only the action stored in `required_action`.
One loop iteration performs at most one domain action:

```text
INITIALIZING
  → IMPLEMENTING
  → VALIDATING
  → REVIEWING
      ├─ approved → COMMITTING or ADVANCING
      └─ findings → REMEDIATING
  → VALIDATING
  → REVIEWING
      ├─ approved → COMMITTING or ADVANCING
      ├─ rejected, attempts < 2 → REMEDIATING
      ├─ rejected, attempts = 2 → ESCALATING
      └─ escalation rejected → BLOCKED_REQUIRES_HUMAN
  → ADVANCING
      ├─ next milestone → IMPLEMENTING
      └─ all obligations satisfied → COMPLETE
```

The reducer accepts schema-valid events, including:

- implementation partial or complete submission;
- validation pass or failure with exact command evidence;
- review approval or a complete finding set;
- remediation partial or complete submission;
- commit success, refusal, or failure;
- milestone advancement;
- explicit cancellation; and
- fatal or external-blocker evidence.

An invalid or stale event changes no state and emits a structured diagnostic.
AI nodes never write `run-state.json`.

## 8. Review, remediation, and escalation

Every review begins with fresh provider context and inspects the current
filesystem, canonical milestone packet, validation evidence, and unresolved
finding set. Review output must validate against a schema containing a decision
and zero or more findings with severity, location where practical, failure
path, and evidence.

The reducer assigns stable finding IDs. Each remediation packet contains the
entire unresolved finding set.

The counters have distinct meanings:

- `remediation_cycles` increments whenever the state enters remediation or
  escalation for the current milestone; and
- `rejected_complete_submissions` increments only when the reviewer rejects
  a complete remediation submission.

The initial implementation rejection enters the first remediation cycle with
zero rejected remediation submissions. Remediations starting with zero or one
rejected complete submission use the normal implementation profile. After two
rejections, the next complete remediation uses the escalation profile. If its
complete submission is rejected, the counter becomes three and the milestone
blocks. No fourth remediation is permitted.

Partial work, intermediate command failures, invalid output, stale inspection,
and incomplete finding coverage increment neither counter.

## 9. Human-readable terminal output

The package must emit a deterministic progress line before every implementation,
validation, review, remediation, escalation, commit, advancement, approval,
block, cancellation, fatal halt, and successful completion action.

Every line includes the milestone or `RUN`, phase, remediation-cycle count,
and rejected-complete-submission count, including zeros:

```text
[M04 | IMPLEMENTING | remediations=0 | rejected-submissions=0/3] Starting milestone implementation
[M04 | REVIEWING    | remediations=0 | rejected-submissions=0/3] Starting independent review
[M04 | REMEDIATING  | remediations=1 | rejected-submissions=0/3] Addressing findings R1, R2
[M04 | ESCALATING   | remediations=3 | rejected-submissions=2/3] Starting escalation remediation
[M04 | BLOCKED      | remediations=3 | rejected-submissions=3/3] Remediation limit reached
```

The progress renderer reads a validated state snapshot and writes human output
to standard error. Standard output is reserved for the launcher's final
machine-readable result. Secrets, provider payloads, and unbounded model prose
must not appear in progress lines.

The launcher must capture Archon's child-process streams and forward bounded,
redacted live output to standard error. It must not let Archon or a provider
inherit the launcher's standard output directly.

## 10. Validation and completion

Validation commands come only from the canonical plan or an explicitly selected
repository adapter. They execute as argument arrays where possible and
otherwise through the target platform shell with the exact command displayed
and recorded. Timeouts, exit status, and bounded output hashes are evidence.

Successful completion is:

```text
all milestones done
AND all scope and exit obligations satisfied
AND all blocking findings resolved
AND all required validation passed
AND the selected commit policy satisfied
AND no required action remains
```

Only `check-completion.ts` may set `story_complete=true` and
`outcome=COMPLETE`. The Archon `until_bash` command succeeds only for that
validated state.

The operational `max_iterations` ceiling must exceed the calculated minimum
for the plan. Exhausting it is `FATAL_ERROR`, never successful completion.

## 11. Resume and idempotency

Because Archon may restart a failed `loop_group` iteration, every mutating
action must have a persisted idempotency key and reconcile before repeating.

On resume, the workflow validates package, Archon, plan, repository, worktree,
provider, and commit-policy identities against the state. It then compares
state with filesystem, validation, review, and Git evidence. It may:

- skip an action already proven complete;
- finish publication of an already completed result;
- rerun a read-only or safely idempotent action; or
- halt with an exact external blocker when ownership cannot be established.

It must not infer completion, approval, finding resolution, or commit success
from an agent message alone.

## 12. Commit policies

`none` is the default and creates no commits.

`milestone` creates one commit only after implementation, validation, and
review approval for the milestone.

`incremental` processes one declared test-first slice at a time and may create
one commit after that slice's required validation succeeds. Later review
remediation is committed in focused additional commits after its validation.
History is never rewritten to hide earlier slices.

For every policy, the deterministic Git adapter must:

- capture the isolated worktree baseline;
- attribute changed paths or hunks to the current action;
- stage only proven run-owned changes;
- refuse when ownership is ambiguous;
- record every created commit SHA and associated milestone/slice;
- leave user and unrelated changes untouched; and
- never push, publish, merge, rebase, squash, reset, clean, or delete branches.

Completion under a committing policy requires no residual run-owned changes.
A commit refusal or failure keeps the milestone incomplete.

## 13. Outcomes and process exit

The domain outcomes are:

| Outcome | Launcher exit |
|---|---:|
| `COMPLETE` | 0 |
| `BLOCKED_REQUIRES_HUMAN` | 2 |
| `FATAL_ERROR` | 3 |
| `CANCELLED` | 130 |

The final standard-output object contains the outcome, run ID, state path,
current/last milestone, both remediation counters, created commit SHAs, and a
bounded diagnostic. Domain outcome remains authoritative even if Archon uses a
broader completed, failed, paused, or cancelled run status.

## 14. Portability and safety

The package must support the platforms for which the pinned Archon release,
Bun runtime, and chosen provider are supported. Linux, macOS, and Windows CI
must cover pure logic, filesystem publication, installation, argument passing,
path containment, console streams, and Git fixtures.

Single Git repositories use Archon's required worktree isolation. Folder
projects may run only with `commit_policy=none` and an explicit adapter that
declares its mutation and recovery boundary. Multi-repository roots require a
separate adapter and are not part of the first release.

The launcher and scripts must reject traversal, symlink escapes, malformed
UTF-8, control characters, unknown schema versions, secret-bearing output, and
unsafe Git ownership before mutation or provider spend.

## 15. Required acceptance evidence

The delivered package must prove:

1. the pinned Archon release accepts deterministic-only `loop_group`
   completion and ignores adversarial completion-looking body output;
2. v0.9.0 and every unsupported release fail preflight;
3. unsupported reviewer profiles, including unsandboxed Codex review, fail
   before provider invocation;
4. the strict plan adapter rejects malformed or ambiguous plans;
5. every legal and illegal transition, counter boundary, completion predicate,
   and terminal outcome is table-tested;
6. state publication and resume survive injected interruption at every
   mutation boundary;
7. every significant terminal line contains both counters;
8. `none`, `milestone`, and `incremental` policies satisfy ownership and
   no-push/no-rewrite rules;
9. repository and global installation are collision-safe and reversible;
10. Linux, macOS, and Windows tests pass; and
11. one bounded end-to-end run with a supported read-only reviewer proves
    implementation → validation → review → remediation → approval → completion.

## 16. Explicit non-goals

The first release does not:

- modify Archon itself or depend on an unreleased Archon branch;
- support model-controlled completion;
- support Codex as reviewer without an independently verified read-only
  boundary;
- publish to the Archon marketplace;
- push, open pull requests, deploy, release, or rewrite Git history;
- infer arbitrary Markdown plan formats;
- orchestrate multi-repository transactions; or
- replace target-repository instructions, authorization rules, or validation
  commands.
