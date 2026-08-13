# Agent instructions

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
