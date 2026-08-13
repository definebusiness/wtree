# How to deliver work through the orchestrated milestone process

This guide shows how to turn a user story or specification into a plan, start
its implementation loop, and recover cleanly when something goes wrong. It is
written for developers who want a predictable result without needing to manage
every agent step themselves.

The process has three roles:

| Role | Responsibility |
|---|---|
| You | Provide the desired outcome, authoritative requirements, and decisions only you can make; authorize the plan and any material scope change. |
| Main agent | Owns the plan run, durable ledger, delegation, verification, and continuous progress. |
| Implementer and reviewer | The implementer changes code test-first; the separate read-only reviewer independently examines the current worktree and evidence. |

The main agent does not finish just because one milestone finishes. Once you
authorize a plan, it continues through all milestones until the complete plan
is approved or there is a genuine terminal blocker.

For the exact rules behind this guide, see
[plan-authoring.md](plan-authoring.md),
[milestone-supervision.md](milestone-supervision.md), and
[run-ledger-layout.md](run-ledger-layout.md).

## The short version

1. Give the agent a clear story or specification and point it to the codebase.
2. Ask it to create a plan only—do not start implementation yet.
3. Read the plan for business decisions, scope, safety, and acceptance criteria.
4. Explicitly authorize the plan run.
5. Let the main agent keep going; use progress updates or the durable ledger
   to follow it.
6. If a material decision or true external dependency blocks work, answer the
   specific question. Ordinary failed tests and review findings are handled
   inside the loop.

## Before you begin

Prepare the information an agent cannot safely infer:

- The outcome in user terms: who needs what and why.
- Acceptance criteria: observable examples of success and failure.
- Sources of truth: specification, ticket, designs, API contracts, security or
  compliance requirements, and relevant repository paths.
- Boundaries: what must not change, supported platforms, backwards
  compatibility, performance constraints, and whether dependencies are allowed.
- Authority: whether commits, releases, deployments, data migration, external
  services, or production access are allowed. Asking for code does not grant
  those actions automatically.

It is fine for some details to be unknown. Mark them as decisions you want the
agent to propose before implementation. Do not hide a material choice in a
vague phrase such as “make it intuitive” or “handle errors nicely.”

### A useful user-story format

```text
As a <person or system>, I want <capability>, so that <outcome>.

Success looks like:
- <observable behavior>
- <observable behavior>

It must not:
- <unsafe or out-of-scope behavior>

Sources of truth:
- <path, URL, ticket, API document, or design>

Constraints and decisions:
- <compatibility, platform, security, timeline, dependency, or authority rule>
```

Example:

```text
As a support engineer, I want to export a filtered audit report as CSV so that
I can investigate incidents outside the application.

Success looks like:
- A date range and actor filter produce a downloadable CSV with documented columns.
- A caller without audit-read permission receives the existing authorization response.
- An empty result produces a valid CSV header and no rows.

It must not:
- Expose secret values or bypass tenant boundaries.
- Change existing JSON export behavior.

Sources of truth:
- docs/spec/audit.md, sections “Permissions” and “Audit event schema”
- internal/audit and api/openapi.yaml

Constraints and decisions:
- Support the currently supported browsers; do not add third-party analytics.
- Do not deploy or publish; code and tests only.
```

## Step 1: Ask for a plan, not code

Start with planning when the request touches more than a tiny isolated change,
has safety/compatibility implications, or needs more than one testable slice.
The plan author investigates the current code and requirements, resolves normal
engineering decisions, and produces a reviewed sequence of milestones.

Use this prompt, replacing the bracketed parts:

```text
Create an implementation plan for this request; do not implement it yet.

<paste the user story, acceptance criteria, and constraints>

Read AGENTS.md, docs/ai/plan-authoring.md,
docs/ai/milestone-supervision.md, docs/ai/run-ledger-layout.md, the listed
sources of truth, and relevant code/tests. Create
docs/plans/<feature-name>-implementation-plan.md.

Make it executable by the orchestrated milestone process: fix routine design
decisions, identify scope and non-goals, provide exact verification commands,
and split it into independently testable and reviewable milestones. Include
test-first slices, safety/failure cases, documentation changes, and a final
acceptance step when appropriate. Do not create a run ledger and do not begin
implementation. Ask me only about material choices that cannot be resolved
from the repository or requirements.
```

For a well-developed specification, use a shorter prompt:

```text
Turn docs/spec/<name>.md into a ready-to-execute orchestrated implementation
plan at docs/plans/<name>-implementation-plan.md. Follow
docs/ai/plan-authoring.md exactly. Inspect existing code and tests to make the
plan fit this repository. Do not implement or start a ledger.
```

### What a good plan contains

You do not need to inspect every technical detail. Check that the plan has:

- a clear outcome and links to the exact authoritative inputs;
- a continuous execution contract, fixed decisions, global definition of done,
  milestone checkboxes, and an execution-log table;
- milestones with scope, test-first slices, exact verification, and exit
  criteria—not just a list of files to modify;
- early foundations for shared contracts, persistence, external adapters, and
  safety checks before dependent or destructive features;
- explicit non-goals and authority limits; and
- tests for invalid, unsafe, rollback, compatibility, and concurrent behavior
  wherever those risks apply.

The plan is ready when an implementer could take the first unchecked milestone
without inventing product behavior and a reviewer could independently decide
whether it is complete.

## Step 2: Review and authorize the plan

Read the plan at a product level. The key questions are:

| Ask | You are checking for |
|---|---|
| Does it deliver the right user outcome? | The requirements were understood. |
| Are the defaults, permissions, errors, and compatibility promises right? | Decisions that are costly to change later. |
| Is anything important missing or out of scope? | Scope and safety boundaries. |
| Are migration, deletion, network, or release actions correctly limited? | Authority and irreversible effects. |
| Is the milestone order sensible? | Risky foundations come before dependent work. |

Request plan changes before authorization when the answer changes what users
receive, what data is changed, what interfaces remain compatible, or what the
team is committing to deliver. Do not require changes merely because a plan
uses an unfamiliar internal implementation detail; ask for an explanation
instead.

When ready, authorize execution explicitly:

```text
I approve docs/plans/<feature-name>-implementation-plan.md. Run it from the
first unchecked milestone through completion using the orchestrated process.
Keep unrelated worktree changes intact. Do not commit, deploy, publish, or
access production systems without asking me first.
```

If commits are wanted, say so separately and state the preferred convention:

```text
I also authorize commits after each approved milestone using conventional
commit messages. Do not push or open a pull request.
```

## Step 3: What happens after authorization

The main agent creates one durable run ledger at
`docs/ai/runs/<plan-basename>.md`. This is the live resume record. It contains
the active milestone checklist, current findings, remediation count, evidence,
and exactly what must happen next.

For each milestone, the loop is:

```text
complete checklist
  → implementer: RED → GREEN → REFACTOR
  → reviewer: independent decision
  → main agent: verify, document, approve
  → immediately begin the next unchecked milestone
```

The source plan's execution log is different: it receives only a short row
after a milestone is approved. It is useful history, but not the place to
resume an interrupted run.

Normal process events require no action from you:

- A test fails during development.
- The implementer reports partial progress.
- The reviewer finds a defect or asks for a correction.
- A milestone is approved and the next one begins.
- The agent needs to re-run safe checks or reconcile an interruption.

The main agent should send brief progress updates, but it must remain active;
an update is not a request for permission to continue.

## Step 4: Follow progress without disrupting it

Ask simple status questions when useful:

```text
What milestone is active, what has been independently verified, and what is
the exact next action in the durable ledger?
```

```text
Summarize progress in plain language. Include completed milestones, current
risks, verification evidence, and whether you need any decision from me.
Continue the authorized run after answering unless a real blocker needs me.
```

If you inspect the ledger, look first at `## Run state`:

- `State: active` means the plan is still running. `Resume from` says the next
  permitted action.
- `State: complete` means every milestone was approved.
- `State: blocked` means the ledger must contain the exact blocker, evidence,
  unresolved findings, and conditions for safe continuation.

`Final response permitted: no` is normal while any milestone remains. It
becomes `yes` only at a recorded complete or valid blocked terminal state.

## Step 5: Troubleshooting

### The agent appears to have stopped mid-run

An interruption is recoverable. Do not ask it to restart from scratch or
delete local work. Use:

```text
Resume the authorized plan run. Read the source plan, durable ledger, evidence
referenced by its last action, and current worktree. Reconcile any difference
between the ledger and filesystem in a new ledger checkpoint, then perform the
ledger's exact Resume from action. Do not infer approvals, finding resolutions,
or remediation attempts from unrecorded changes.
```

This preserves prior evidence and avoids accidentally counting an incomplete
attempt as a failed remediation.

### A test or quality check fails

This is normally internal work, not a blocker. Ask for an evidence-based
diagnosis if you want visibility:

```text
Diagnose the failing verification. Keep the plan run active: record the exact
command, failure, affected milestone, and next safe action in the ledger, then
continue with the required implementation or remediation work. Escalate to me
only if the failure is a genuine external blocker or requires a material scope
decision.
```

Common fixes include completing missing code, correcting a test expectation,
repairing hermetic fixtures, or installing an allowed missing tool. The agent
must not weaken a test merely to make it pass.

### The reviewer rejects a milestone

This is the expected feedback loop. The main agent records the complete finding
set with stable IDs (such as `R1`) and sends all unresolved findings together
to the implementer for test-first correction. You normally do nothing.

Only rejected *complete remediation submissions* increase the remediation
counter. The initial implementation review does not. After two such rejections,
the final remediation is assigned to the escalation implementer. If that
complete submission is rejected too, the milestone becomes blocked; the agent
must report the unresolved findings and evidence rather than creating a fourth
attempt.

Use this only if you need the process checked:

```text
Confirm the reviewer findings are all recorded with stable IDs, the current
remediation packet covers every unresolved finding, and the remediation counter
reflects only rejected complete remediation submissions. Continue the plan.
```

### The agent asks a question

First distinguish a routine engineering detail from a material decision.
Routine choices should already be answered by the plan or repository evidence;
ask the agent to make and document the evidence-based choice. Material choices
need your direction: product behavior, API compatibility, permission model,
data deletion/migration, budget, legal/compliance policy, production access,
or a meaningful scope expansion.

For an evidence-based recommendation:

```text
Present the smallest viable options, your recommended option, affected
milestones, compatibility/safety impact, and the repository evidence. Do not
pause for a routine implementation detail; make the documented choice and
continue if it stays within the approved scope.
```

For a decision you make:

```text
Choose option <A/B>. This changes the plan scope as follows: <decision>. Update
the source plan and active ledger checkpoint, add the necessary tests and
documentation, then continue the authorized run.
```

### A required service, credential, or tool is unavailable

This can be a real external blocker only when it cannot be safely obtained or
substituted within authorized scope. Ask for a precise report:

```text
Record the external blocker in the durable ledger with the exact failed action,
evidence, affected scope, work already verified, and the minimum safe
continuation conditions. Do not mark the run complete. Tell me exactly what
authority, access, or decision is needed.
```

Once resolved, tell the agent to resume using the existing ledger. It must
append a reconciliation checkpoint; it must not erase the block history.

### The request or plan changes mid-run

Small clarifications that are already implied by the plan can be recorded and
implemented. For a material change, stop changing code until scope is explicit.
Use:

```text
Assess this requested change against the authorized plan. Identify affected
milestones, contracts, tests, risks, and any completed evidence. If it is a
material scope change, propose a plan amendment for my approval before
implementation; otherwise record the interpretation in the ledger and
continue.
```

Do not silently add newly discovered features to a remediation packet. A
reviewer finding outside the original scope needs the same change-control
decision and does not count against the remediation limit.

### You suspect unsafe or destructive behavior

Pause only the unsafe operation, not necessarily the whole process. Request
evidence before allowing any destructive action:

```text
Before performing <operation>, show the exact target, authorization boundary,
preflight checks, rollback/recovery path, and tests that prove it cannot affect
unrelated data. Do not execute it until the approved plan and my authority
cover it.
```

For production access, deployment, publishing, or irreversible migrations,
give separate explicit authority. A coding plan alone is not permission.

## Finishing the run

You should receive a final summary only when the durable ledger permits it. A
valid successful completion means every milestone is approved, required checks
are recorded, documentation/contracts are updated, and the execution log is
complete. A valid blocked completion means the ledger records either the
three-rejected-complete-remediation limit or a concrete external blocker and
safe continuation conditions.

Ask for this final handoff if needed:

```text
Give the final handoff from the durable ledger: completed outcome, milestones
and verification evidence, changed docs/contracts, any commits made, and any
remaining risks or follow-up. Confirm whether the run is complete or blocked.
```

## Quick reference: prompts by situation

| Situation | Prompt |
|---|---|
| Create a plan | “Create a ready-to-execute orchestrated implementation plan for <request>; inspect the sources and do not implement yet.” |
| Approve and run | “I approve `docs/plans/<name>.md`. Run it continuously from the first unchecked milestone; do not commit/deploy/publish without separate authority.” |
| Check status | “What is the active milestone, verified evidence, current risk, and exact ledger `Resume from` action? Continue afterward.” |
| Resume after interruption | “Reconcile plan, ledger, evidence, and worktree; append the reconciliation, then perform the exact `Resume from` action.” |
| Handle a material change | “Assess the change; propose and wait for approval of a plan amendment if it materially changes scope, contracts, safety, or authority.” |
| Investigate a blocker | “Record exact evidence and safe continuation conditions; tell me the smallest authorization or access needed.” |

The habit that makes this experience seamless is simple: decide the important
product and safety questions before authorizing the plan, then let the
orchestrated loop handle normal engineering uncertainty with tests, review,
and durable evidence.
