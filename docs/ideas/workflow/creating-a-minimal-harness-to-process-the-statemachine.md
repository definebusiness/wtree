# Archon-Based Harness for Deterministic Milestone Orchestration

Status: specified
Specification: [Archon deterministic milestone harness specification](../../spec/archon-milestone-harness.md)

## Decision

Use [Archon](https://github.com/coleam00/Archon) as the portable workflow
runtime instead of building a new general-purpose harness executable.

The deliverable should be a packaged Archon workflow containing reusable
commands and deterministic scripts. Archon should provide workflow execution,
agent-provider integration, isolation, persistence, resumption, and streaming
output. The packaged workflow should provide the milestone state machine,
transition validation, remediation policy, completion predicate, progress
messages, and commit policy.

The central invariant remains unchanged:

> The model decides how to perform one bounded action. Persisted facts and
> deterministic code select that action and decide whether the run may advance
> or end.

Adopting Archon must not weaken this invariant by replacing it with a prompt or
an AI-emitted completion signal.

## Why Archon

Archon already supplies much of the repository-independent infrastructure that
the original harness would otherwise have to implement:

* YAML workflows with dependency ordering, conditions, and loops;
* Codex, Claude, and other agent-provider integrations;
* model and provider selection per workflow node;
* structured node output;
* deterministic Bash and script nodes;
* persistent workflow runs, events, artifacts, and resumption;
* isolated Git worktrees by default;
* foreground terminal streaming and other user interfaces; and
* packaged and global workflows that can be installed once and used from many
  repositories.

Using Archon changes the scope from building an orchestration platform to
building one strict workflow package on top of an existing platform.

## Archon compatibility gate

Archon v0.9.0 cannot satisfy this idea as released. It requires every loop to
declare an `until` string emitted by the model. When both `until` and
`until_bash` are configured, either one completes the loop. A model can
therefore end a v0.9.0 loop even while the deterministic check remains false.

The package must require the first released Archon version containing the
deterministic-only loop change introduced by
[Archon commit `d6c102b4`](https://github.com/coleam00/Archon/commit/d6c102b417238803ec8582d4e49b932fdc732621),
or an equivalent later implementation. That change permits `until_bash` to be
the only completion channel, with no `until` value for the model to emit.

Do not depend on a moving development branch. Pin a released Archon version and
run a compatibility suite before accepting an upgrade.

The decisive compatibility test is:

```text
Given an incomplete persisted state,
when any agent emits COMPLETE, DONE, APPROVED, or a final-looking response,
then Archon continues the workflow and does not mark the run complete.
```

## Portable workflow package

The workflow should be distributed as a self-contained Archon package with a
shape similar to:

```text
deterministic-milestones/
├── deterministic-milestones.yaml
├── commands/
│   ├── implement.md
│   ├── review.md
│   ├── remediate.md
│   └── escalate-remediation.md
└── scripts/
    ├── initialize-state.ts
    ├── select-action.ts
    ├── apply-result.ts
    ├── check-completion.ts
    ├── print-progress.ts
    └── apply-commit-policy.ts
```

The package may be installed globally so that it is available in every
repository. A target repository should supply only repository-specific inputs
or configuration, such as its plan path, validation commands, documentation
rules, and model choices.

The workflow package must not import code from `wtree` or assume that the
target repository is written in Go. Repository-specific commands should be
configuration, not workflow-engine logic.

## Authoritative structured run state

The workflow should persist its authoritative state as versioned structured
data scoped to the current Archon run, for example:

```text
$ARTIFACTS_DIR/run-state.json
```

The state should not be a Markdown document and the workflow should not infer
its current phase by parsing terminal output or agent prose.

An initial state could contain:

```json
{
  "version": 1,
  "run_id": "<archon-workflow-id>",
  "run_state": "EXECUTING",
  "current_milestone": "M4",
  "phase": "IMPLEMENTING",
  "remaining_milestones": ["M4"],
  "remaining_acceptance_criteria": ["AC7"],
  "open_review_findings": ["RF3"],
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

Every state update should use atomic replacement or an equivalent
crash-consistent mechanism. The workflow should reject unsupported state
versions and preserve enough evidence to explain every transition.

Archon's own run database remains Archon's responsibility. The workflow should
use documented Archon inputs, artifacts, events, and resumption behavior rather
than query or modify Archon's database schema directly.

## Workflow control loop

The Archon workflow should use a `loop_group` whose only completion channel is
a deterministic `until_bash` check. One iteration should perform at most one
state-machine action:

```text
READ PERSISTED STATE
        │
        ▼
SELECT NEXT PERMITTED ACTION
        │
        ▼
PRINT HUMAN-READABLE PROGRESS
        │
        ▼
RUN ONE CONDITIONAL ACTION NODE
        │
        ├── implement
        ├── validate
        ├── review
        ├── remediate
        ├── escalate remediation
        ├── commit
        └── advance milestone
        │
        ▼
VALIDATE AND PERSIST THE RESULTING TRANSITION
        │
        ▼
DETERMINISTIC COMPLETION CHECK
        │
        ├── incomplete ──► next iteration
        ├── complete   ──► successful termination
        └── halt       ──► blocked, fatal, or cancelled path
```

The action selector and transition reducer must be deterministic scripts. AI
nodes provide implementation or review evidence; they do not write the state
or choose their successor directly.

## A final-looking model response is only node output

Archon should treat a normal assistant response as the result of the current
node, not as a request to terminate the workflow.

For example, an implementation agent may say:

> Everything looks good. The implementation is complete.

The transition script may still observe:

```text
M1 DONE
M2 DONE
M3 DONE
M4 PENDING
AC7 PENDING
RF3 OPEN

STORY_COMPLETE=false
```

It should then persist `required_action=CONTINUE` or a more specific next action.
Because the Archon loop has no model completion channel, the prose cannot end
the run.

## Validate transitions, not only current status

The transition reducer should implement an explicit state graph. For example:

```text
IMPLEMENTING
    ↓ complete implementation evidence
VALIDATING
    ↓ required validation passed
REVIEWING
    ├── approved ──► COMMITTING or DONE
    └── findings ──► REMEDIATING

REMEDIATING
    ↓ complete remediation evidence
REVIEWING
```

It should reject invalid transitions such as:

```text
IMPLEMENTING → DONE
```

when validation or review evidence is absent. A rejected transition should
leave the prior persisted state authoritative and produce a structured error
identifying the requested transition and missing evidence.

## Implementation, review, and remediation nodes

Implementation and remediation nodes may edit the isolated worktree. Review
nodes must start with fresh context and inspect the current filesystem rather
than rely on an implementer's summary.

Review output should use a validated structured schema containing at least:

```json
{
  "decision": "APPROVED",
  "findings": [],
  "validation_evidence": []
}
```

Findings should receive stable identities when first persisted. A remediation
node should receive the complete unresolved finding set rather than one finding
at a time.

The workflow should distinguish two counters:

* `remediation_cycles` counts how many times the milestone has entered
  remediation; and
* `rejected_complete_submissions` counts complete remediation submissions that
  a reviewer rejected.

The escalation implementer should be selected only after two rejected complete
submissions. A rejection of the escalation implementer's complete submission
sets the rejected-submission count to three and blocks the milestone. Partial
work, intermediate test failures, and incomplete submissions increment neither
counter.

## Reviewer isolation compatibility

A prompt telling a reviewer to remain read-only is insufficient.

Archon does not currently enforce `allowed_tools` or `denied_tools` for Codex
workflow nodes. Before using Codex as the reviewer, the workflow must provide an
independent read-only execution boundary, such as a provider capability,
read-only filesystem sandbox, or external reviewer adapter that Archon cannot
bypass.

Until that exists, the workflow may use a provider for which Archon enforces
read-only tool restrictions. Deterministic validation commands should run in
separate non-AI nodes so that a read-only reviewer does not need a writable
shell.

This is an adoption gate, not a prompt-writing problem.

## Human-readable terminal progress

Archon already streams foreground workflow output, but the package should add
deterministic progress nodes so that required progress does not depend on agent
narration.

The workflow should print immediately before it:

* starts implementing a new milestone;
* starts reviewing a milestone;
* starts remediating review findings;
* sends remediated work back for review;
* starts escalation remediation;
* commits or advances a milestone;
* approves or blocks a milestone; and
* completes or otherwise halts the run.

Every progress message should include the current milestone, phase,
remediation-cycle count, and rejected-complete-submission count, including when
the values are zero:

```text
[M4 | IMPLEMENTING | remediations=0 | rejected-submissions=0/3] Starting milestone implementation
[M4 | REVIEWING    | remediations=0 | rejected-submissions=0/3] Starting independent review
[M4 | REMEDIATING  | remediations=1 | rejected-submissions=0/3] Addressing findings R1, R2
[M4 | REVIEWING    | remediations=1 | rejected-submissions=0/3] Reviewing remediated work
[M4 | REMEDIATING  | remediations=2 | rejected-submissions=1/3] Addressing unresolved finding R2
[M4 | ESCALATING   | remediations=3 | rejected-submissions=2/3] Starting escalation remediation
[M4 | APPROVED     | remediations=3 | rejected-submissions=2/3] Milestone approved
[M5 | IMPLEMENTING | remediations=0 | rejected-submissions=0/3] Starting milestone implementation
```

The values must be read from the persisted state. Console output is
observational only and must not authorize, perform, or substitute for a state
transition.

Archon's foreground CLI may use standard output for human streaming. When a
machine-readable invocation is selected, structured command output and live
human progress must remain separable according to Archon's supported interface.

## Resume and idempotency

Archon persists workflow runs, but a failed `loop_group` can restart its current
iteration rather than resume at an individual body node. Every action must
therefore be safe to reconcile and retry.

Before performing an action, a deterministic node should compare the persisted
state with repository and artifact evidence. It should skip an already-applied
action, finish an interrupted publication when safe, or halt with a precise
external blocker when it cannot determine the correct result.

No agent message alone may establish that an implementation, review,
remediation, validation, or commit completed.

Archon's required `max_iterations` value should be a high operational safety
ceiling, not the remediation limit and not a substitute for the completion
predicate. Reaching it should produce a fatal diagnostic containing the current
state and remaining obligations.

## Caller-selected commit policy

The workflow should declare a `commit_policy` input and accept:

```text
none
    Do not stage or commit changes automatically.

milestone
    Create one commit after implementation, remediation, validation, and review
    have all succeeded for the milestone.

incremental
    Create smaller commits at validated implementation boundaries, with focused
    commits for later remediation where needed.
```

The package should expose a typed launcher that converts validated flags into
Archon's positional workflow input. For example:

```text
archon-milestones run \
  --repo /path/to/repository \
  --plan docs/plans/example.md \
  --branch archon/example \
  --commit-policy none
```

`none` should be the default. The input should be validated before any agent or
repository mutation and then persisted in the run state so resumption cannot
silently change it.

### Commit safety rules

Do not reuse an Archon workflow that instructs an AI node to stage, commit,
push, or open a pull request. Commit behavior in this package must be a
deterministic workflow responsibility.

In every mode, the workflow should:

* capture the repository and worktree baseline before implementation;
* use Archon's isolated worktree by default for mutating runs;
* distinguish run-owned changes from pre-existing or concurrent changes;
* stage only explicitly verified paths or hunks owned by the current milestone;
* refuse an automatic commit when ownership cannot be determined safely;
* never push, publish, rewrite history, squash existing commits, or perform
  destructive cleanup as part of the commit policy;
* record every created commit SHA and its milestone or slice in the structured
  run state; and
* verify that no milestone-owned residual changes remain when a committing
  policy finishes.

Running a mutating workflow without Archon's worktree isolation should require
an explicit override. Automatic commit modes should refuse that override unless
the ownership checks can still prove safety.

## Halt states and completion predicate

The domain state should distinguish:

```text
COMPLETE
BLOCKED_REQUIRES_HUMAN
FATAL_ERROR
CANCELLED
```

These domain states are independent of Archon's own run lifecycle states. The
structured state must preserve the exact domain outcome even when Archon maps it
to a broader completed, failed, paused, or cancelled status.

Successful completion should require:

```text
COMPLETE =
    all milestones DONE
AND all acceptance criteria SATISFIED
AND all blocking review findings RESOLVED
AND all mandatory validation PASSED
AND the selected commit policy SATISFIED
AND no required follow-up work exists
```

Only the deterministic completion script may establish this predicate. An
agent's structured or prose output is evidence for a transition, never the
completion decision itself.

`BLOCKED_REQUIRES_HUMAN` should be available only for the defined rejected
remediation limit or a concrete external blocker that deterministic recovery
cannot resolve safely. `FATAL_ERROR` covers invalid state, corrupted evidence,
an exhausted operational iteration ceiling, or an unrecoverable workflow
failure. `CANCELLED` records an explicit operator cancellation.

## Model-cost implications

Archon's per-node provider and model selection allows cheaper models to perform
bounded implementation work while stronger models handle review or escalation.
The state machine removes global liveness and termination decisions from those
models.

The expected responsibility split is:

```text
AI nodes:
    understand one bounded packet
    implement or review it
    return schema-valid evidence

Deterministic workflow nodes:
    track global obligations
    select the next permitted action
    enforce remediation limits
    validate transitions
    apply commit policy
    decide whether execution may end
```

This may make less expensive implementation models viable without allowing
their mistaken completion claims to stop the run.

## Portability boundaries

The package should support ordinary Git repositories on Linux, macOS, and
Windows where the pinned Archon version and selected agent provider are
supported.

Additional repository shapes require explicit handling:

* a single Git repository should use Archon's default isolated worktree;
* a non-Git folder or multi-repository root must not assume that Archon provides
  per-repository branch, commit, or rollback isolation;
* repository-specific validation commands must be declared rather than guessed;
* plan parsing must use a defined format or repository adapter instead of
  attempting to understand arbitrary Markdown heuristically; and
* provider-specific capabilities, especially read-only enforcement, must be
  validated before the first paid agent invocation.

## Acceptance conditions for a future specification

A specification derived from this idea should define and test at least:

1. the structured state schema and migration policy;
2. the legal transition table and event schemas;
3. the Archon workflow package and declared inputs;
4. the minimum compatible Archon version;
5. deterministic-only completion with adversarial model output;
6. implementation, review, remediation, and escalation routing;
7. read-only reviewer enforcement for every supported provider;
8. console progress and both remediation counters;
9. interruption reconciliation and idempotency;
10. commit-policy behavior and ownership checks;
11. domain-to-Archon halt-state mapping; and
12. portability and compatibility tests for every supported operating system
    and repository shape.

The implementation should not begin until the deterministic-only completion
and reviewer-isolation adoption gates have verified solutions.
