# AI-assisted delivery process

This directory defines the repository's process for planning and delivering
work with AI agents. The goal is a continuous, evidence-based delivery loop:
plan the work, implement one complete milestone at a time, independently
review it, verify it, record the result, and immediately continue with the next
unchecked milestone.

## Experimental status

This is an experimental, almost agent-only delivery loop: people set direction,
approve plans, and make material product decisions, while agents carry out most
of the delivery cycle. It differs from the AI loops Define Business uses in its
normal products. Those loops are fully autonomous until AI cannot handle a
case alone, with human oversight available as needed. Their process is
controlled by programmed state machines that call agents; agents do not control
the process themselves with scripts intended to compensate for their
shortcomings, as they do in this repository's test loops.

## Current process

The process has four responsibilities:

| Responsibility | Owner |
|---|---|
| Define requirements, approve a plan, and make material product decisions | User |
| Create and run the plan, maintain the ledger, verify work, and advance milestones | Main agent / orchestrator |
| Implement a complete milestone test-first and provide evidence | Implementer |
| Inspect the shared worktree independently and approve or reject the milestone | Reviewer |

For a plan run, the orchestrator creates and maintains the durable ledger at
`docs/ai/runs/<plan-basename>.md`. It is the single source of live run state;
the plan's execution log only records approved milestones. A run continues
without pausing at milestone boundaries. Failed tests, incomplete submissions,
and review findings are handled within the loop. A final response is allowed
only after all milestones are approved or a valid terminal blocker is recorded.

Read these documents in this order when using or changing the process:

1. [orchestrated-delivery-howto.md](orchestrated-delivery-howto.md) — user and
   operator guide.
2. [plan-authoring.md](plan-authoring.md) — requirements for an executable,
   testable milestone plan.
3. [milestone-supervision.md](milestone-supervision.md) — mandatory execution,
   review, remediation, and stop rules.
4. [run-ledger-layout.md](run-ledger-layout.md) — required durable-ledger
   format, state invariants, checkpoints, and final-response audit.

## Approve and run a plan

From the repository root, give the main agent this prompt after reviewing and
approving a ready-to-execute plan. Replace `<plan-name>` with the plan filename
without `.md`:

```text
I approve docs/plans/<plan-name>.md.

Run it continuously from the first unchecked milestone using the repository's
milestone supervision process. Create and maintain the durable run ledger at
docs/ai/runs/<plan-name>.md.

Use implementer, reviewer, and remediation agents exactly as specified by the
plan and AGENTS.md. Preserve unrelated worktree changes. Do not commit,
publish, deploy, install, or modify real user data unless I separately
authorize it.

Continue unattended until every milestone is approved or a valid terminal
blocker is recorded.

Do not send a final response while the run ledger is active. Use commentary only.
Continue from the ledger’s Resume from action after every milestone and agent.
```

If a run is interrupted, resume it with:

```text
Resume docs/plans/<plan-name>.md.

Reconcile the plan, durable ledger, current worktree, and recorded evidence.
Append the reconciliation checkpoint, then perform the ledger's exact
"Resume from" action. Continue unattended until complete or validly blocked.

Do not send a final response while the run ledger is active. Use commentary only.
Continue from the ledger’s Resume from action after every milestone and agent.
```

## Required stabilization: a state-machine tool

The current workflow is defined in Markdown and agent coordination, but it
**must be enhanced with an orchestrator-owned state-machine command** to make
the process less flaky. The implementation brief is
[future-enhancement-and-stabilization-of-agent-based-loop.md](future-enhancement-and-stabilization-of-agent-based-loop.md).

The proposed repository-local `milestone-flow` command will enforce the
existing process rather than replace it. In particular, it must:

- validate legal transitions before the orchestrator mutates a ledger or
  advances a milestone;
- process versioned, idempotent events so duplicate or stale agent messages
  cannot cause a second transition;
- bind review decisions to the worktree that was reviewed;
- reject incomplete submissions from entering review;
- derive and enforce remediation-attempt and escalation rules;
- atomically keep the human-readable ledger and any machine-readable state in
  sync; and
- reconcile interrupted runs and gate final responses.

Until that tool exists, the documents above remain authoritative and the main
agent must apply their guards manually. The state-machine implementation must
preserve their ownership model: only the orchestrator may advance workflow
state or write the durable ledger; implementers and reviewers provide evidence
and decisions, not state transitions.
