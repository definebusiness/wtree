# Durable run-ledger layout

Use this document for every authorized milestone-plan run. It defines the
required content and update rules for files in `docs/ai/runs/`.

## File identity and ownership

- Create exactly one tracked ledger at
  `docs/ai/runs/<plan-basename>.md`, where `<plan-basename>` is the plan
  filename without its extension.
- The ledger is the authoritative resume record for that plan in the current
  worktree. The plan execution log is only a concise history of approved
  milestones; it cannot replace the ledger.
- Only the main agent writes the ledger. Implementers and reviewers must not
  edit it, including to report progress, findings, approvals, or test results.
- The ledger is append-only in its action log. Current-state sections may be
  updated only to reflect the newest logged checkpoint; never silently rewrite
  or delete prior evidence.

## Required document shape

Every ledger must use these headings in this order:

```markdown
# Run ledger — <plan title>

## Run state

- Plan: `<repository-relative plan path>`
- State: `active` | `blocked` | `complete`
- Started: `<ISO-8601 timestamp or documented historical value>`
- Last updated: `<ISO-8601 timestamp>`
- Active milestone / phase: `<ID — title> / implementing|reviewing|approved|blocked`
- Resume from: `<one exact next permitted action>`
- Final response permitted: `yes` | `no`

## Current milestone ledger

- Scope checklist:
  - [ ] `<one explicit plan scope item, test slice, exit criterion, documentation item, or verification command>`
- Implementation tier: `normal` | `escalation`
- Remediation attempts: `0` | `1` | `2` | `3`
- Finding set:
  - `<ID> — unresolved|accepted|disputed|resolved|invalidated, severity, location: evidence>`
- Current packet and evidence: `<packet identity, submission state, and evidence references>`
- Verification / adjudication: `<main-agent commands/results and bounded adjudication decisions>`

## Action log

| Time | Actor | Action | Outcome / evidence | Resulting state and resume instruction |
|---|---|---|---|---|
```

Do not add a second current-milestone section. When a milestone is approved,
replace the *current-state* fields with the next unchecked milestone before
dispatching it; retain the completed milestone's evidence in the action log.

## Current-state invariants

The fields above are a single snapshot and must agree with each other:

- `State: active` requires `Final response permitted: no`.
- An `implementing` or `reviewing` milestone requires `State: active` and
  `Final response permitted: no`.
- `State: complete` requires every plan milestone approved, an action-log
  terminal checkpoint, and `Final response permitted: yes`.
- `State: blocked` requires the exact unresolved finding set or external
  blocker, evidence, remediation count, safe-continuation conditions, an
  action-log terminal checkpoint, and `Final response permitted: yes`.
- `Resume from` is always one imperative action. It cannot say “ready”,
  “awaiting work”, “continue”, or describe more than the next permitted step.
- The scope checklist must describe the active milestone only. It must not
  retain prior milestone items or prior findings after the active milestone
  changes.
- The finding set must describe the active milestone only. It begins as
  `none; no <ID> reviewer pass has occurred.` and changes only through a
  reviewer/adjudication checkpoint.
- `Implementation tier` is `normal` for initial work and remediations starting
  with attempts `0` or `1`; it is `escalation` only for a remediation starting
  at attempts `2`.

## Mandatory checkpoints

Append an action-log row before dispatching any implementer, reviewer, or
adjudicator packet. Append another row after each of these events:

- interruption/recovery reconciliation;
- implementer progress report or complete submission;
- reviewer decision and complete finding set;
- complete-remediation rejection and counter change;
- escalation-review decision;
- each main-agent verification command group;
- milestone approval, source-plan checkbox/execution-log update, and next
  milestone transition;
- external-blocker or three-rejection block decision;
- final terminal decision.

Each row must record the time, actor, action, evidence or exact command/result
reference, resulting state, and the exact next action. A partial progress row
must explicitly say that it is not a complete submission and does not affect
the attempt counter.

## Finding and remediation rules

- Give every material reviewer finding a stable identifier (`R1`, `R2`, …).
  Preserve that identity through remediation and adjudication.
- Record the entire unresolved finding set in one checkpoint and send all of
  it in one remediation packet.
- The initial implementation rejection is not a remediation attempt. Increment
  the counter only when the reviewer rejects a *complete remediation
  submission*.
- When a finding is resolved, retain it in the current set as `resolved` until
  the milestone is approved; do not silently remove it.
- Do not use escalation review except for the bounded cases in
  `milestone-supervision.md`; it never changes the attempt counter.

## Transition procedure

After a reviewer approves a milestone, the main agent must, in order:

1. Run and record its own required verification.
2. Update documentation/contracts and mark the plan milestone checked.
3. Append the approval checkpoint and concise plan execution-log entry.
4. Replace the current-state snapshot with the next unchecked milestone's
   complete checklist, `normal` tier, `0` attempts, and empty finding set.
5. Append the next-milestone-ledger checkpoint.
6. Append the pre-dispatch checkpoint and dispatch the initial packet.

Steps 4–6 occur in the same uninterrupted plan run. They are mandatory before
any user-facing completion-style summary.

## Pre-final-response audit

Immediately before a final response, read the ledger and verify:

```text
[ ] `Final response permitted: yes`.
[ ] Run state is `complete` with every milestone approved, or `blocked` with
    a documented three-rejection or concrete external-blocker terminal state.
[ ] The action log has the terminal checkpoint and exact supporting evidence.
[ ] There is no active milestone, unchecked checklist item, pending packet, or
    `Resume from` action other than the recorded terminal state.
```

If any item is false, a final response is prohibited. Perform the exact
`Resume from` action instead. “Milestone approved”, “next milestone ready”,
“checklist not yet created”, “packet not yet dispatched”, and “waiting for an
agent” are all non-terminal states.

## Resume audit

Before resuming an interrupted run, read the plan, ledger, referenced evidence,
and worktree. Append a reconciliation checkpoint that distinguishes recorded
facts from unrecorded filesystem changes. Never infer approval, finding
resolution, complete submission, or attempt count from unrecorded work.

## Manual layout verification

Before changing this layout, manually check these cases:

| Case | Required ledger result |
|---|---|
| M06 approved; M07 unchecked | Active snapshot becomes M07, checklist is M07-only, packet dispatch is logged, final response remains `no`. |
| Reviewer rejects initial work | Full finding set recorded; attempts stay `0`; normal remediation packet logged. |
| Reviewer rejects complete remediation at attempts 1 | Attempts become `2`; next implementation tier is `escalation`; final response remains `no`. |
| Reviewer rejects escalation complete remediation | Attempts become `3`; blocked evidence/conditions recorded; final response becomes `yes`. |
| All milestones approved | State becomes `complete`; terminal action is logged; final response becomes `yes`. |
