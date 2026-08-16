# Agent instructions

## Repository document language

All repository documentation must be written in English, regardless of the
language used to communicate with Codex. This includes README files,
specifications, implementation plans, idea documents, tutorials, runbooks,
and other explanatory prose maintained in the repository. Preserve verbatim
third-party quotations only when their original wording is materially
required.

## Idea, specification, and plan lifecycle

Documents under `docs/ideas/`, `docs/spec/`, and `docs/plans/` must declare a
`Status:` near the top of the document. Agents must maintain the status and
the relationships described below automatically as part of creating or
implementing these documents; the user does not need to request the metadata
or reciprocal-link updates separately.

Use these lifecycle states:

- Idea: `initial` -> `specified`
- Specification: `initial` -> `planned` -> `implemented`
- Plan: `initial` -> `implemented`
- Any non-terminal state may become `superseded` or `abandoned`, but only on
  explicit user request. Never infer either terminal state from inactivity,
  replacement work, implementation changes, or conversation context.

Apply these creation and transition rules:

1. A newly created idea has `Status: initial`.
2. An idea is optional. A specification may be created directly without an
   idea; in that case it has `Status: initial` and records
   `Source idea: none (created directly)`.
3. When a specification is created from an idea, the new specification has
   `Status: initial`, the idea changes to `Status: specified`, and both
   documents receive relative Markdown links to each other.
4. A plan must be based on a specification. If a plan is requested and no
   specification exists, create the necessary specification first. When an
   applicable idea exists, derive the specification from it and apply rule 3;
   otherwise create the specification directly and apply rule 2.
5. When a plan is created from a specification, the new plan has
   `Status: initial`, the specification changes to `Status: planned`, and both
   documents receive relative Markdown links to each other. A specification
   may link to multiple implementation plans.
6. A plan changes to `Status: implemented` only after its entire scope is
   implemented, reviewed, and verified. Starting work or completing only some
   milestones does not change the plan status.
7. A specification changes to `Status: implemented` only when the delivered
   and verified implementation satisfies its full scope and all plans required
   for that scope are implemented.

Document lifecycle status is separate from execution state. Active, paused,
blocked, remediation, and completion checkpoints for an authorized plan run
belong in its durable ledger, not in the plan's `Status:` value.

When the user explicitly requests `superseded`, add a `Superseded by:` link to
the replacement and a reciprocal `Supersedes:` link in the replacement. When
the user explicitly requests `abandoned`, record a concise `Abandonment
reason:`. Treat both states as terminal: make no further substantive changes
to that document except correcting lifecycle metadata or links. Resumed work
requires a new document that links to the terminal document.

These rules apply immediately to every newly created lifecycle document. They
do not require a bulk migration of existing documents, but an existing idea,
specification, or plan must be brought into compliance when an agent edits it
or creates a new lifecycle relationship involving it.

For work driven by an implementation plan with milestones, follow
[`docs/ai/milestone-supervision.md`](docs/ai/milestone-supervision.md).
It defines the required implementation, review, remediation, verification, and
stop conditions. Its rules apply before an agent reports a milestone complete
or advances to the next milestone.

## Continuous milestone execution

Once the user authorizes a milestone plan, execute it as one uninterrupted
process. An approved milestone is a handoff point, not a stopping point: log
it, create the next milestone ledger, dispatch the next implementer packet,
and continue without waiting for a user message or permission.

For every authorized plan run, maintain its tracked durable run ledger at
`docs/ai/runs/<plan-basename>.md`, as specified by the supervision process.
The ledger is the resume record for active, interrupted, blocked, and completed
runs; the plan's execution log remains the concise history of completed
milestones.

The ledger must conform to
[`docs/ai/run-ledger-layout.md`](docs/ai/run-ledger-layout.md). Its document
shape, current-state invariants, checkpoint rules, transition procedure, and
pre-final-response audit are mandatory.

Do not send a final response merely to announce that a milestone has started,
is implementing, is reviewing, or was approved. Commentary updates are fine,
but the agent must remain active and continue the next required action.

Before any final response during an authorized plan run, apply the
final-response gate in `docs/ai/milestone-supervision.md`. A final response is
prohibited unless the durable ledger explicitly permits it.

The only normal stop condition is a milestone blocked after three rejected
complete remediation submissions, as defined by the supervision process. The
third remediation opportunity uses the Escalation Implementer after two such
rejections; it neither resets nor extends that limit. A genuine external
blocker that cannot be safely resolved within the authorized scope may also
require a stop; report the concrete blocker and all evidence. Do not stop for
ordinary test failures, review findings, escalation, milestone transitions, or
incomplete implementation slices.
