# Future enhancement — stabilize the agent-based milestone loop

## Purpose

Create a small, orchestrator-owned command-line tool that makes the milestone
supervision process executable, restart-safe, and resistant to duplicate,
stale, or incomplete agent messages.

The authoritative behavioural requirements are
[`milestone-supervision.md`](milestone-supervision.md) and
[`run-ledger-layout.md`](run-ledger-layout.md). This document is a design brief
for a later implementation plan; it does not change those process rules.

The tool must preserve the ownership model:

- The **orchestrator/main agent** is the only actor allowed to mutate the
  durable run ledger or advance a milestone.
- An **implementer** supplies progress or a complete submission with evidence.
- A **reviewer** supplies a decision and finding set after inspecting the
  current shared filesystem.
- An **escalation reviewer** supplies a bounded adjudication decision only.

Implementers must not run the mutating transition command. Reviewers may run
the read-only validation command before returning a decision.

## Problem this solves

Running the process wholly as agent conversation is vulnerable to operational
flakiness even when each agent follows its prompt: duplicated delivery of a
message, stale review against a changed shared worktree, interruption between
an agent response and ledger update, incomplete submissions accidentally sent
to review, and remediation counters incremented at the wrong boundary.

The enhancement should turn these conventions into enforced transition guards.
It is a workflow controller and validator, not an autonomous code-writing or
code-review agent.

## Proposed command surface

Use a repository-local command, tentatively named `milestone-flow`. The exact
language, package location, and build integration are implementation-plan
decisions.

```text
# Read-only; usable by the orchestrator, reviewer, and CI.
milestone-flow check --plan <plan-path> --ledger <ledger-path>

# Orchestrator only; validates an event, updates the ledger atomically, and
# emits the exact next permitted dispatch/action as structured output.
milestone-flow apply --plan <plan-path> --ledger <ledger-path> \
  --event <event.json>

# Orchestrator only; reconciles an interrupted run before its next dispatch.
milestone-flow resume --plan <plan-path> --ledger <ledger-path>

# Read-only; gate required immediately before a user-facing final response.
milestone-flow final-check --plan <plan-path> --ledger <ledger-path>
```

`apply` must never send agent messages itself in its first version. It should
emit a machine-readable next-action/packet descriptor; the orchestrator then
performs the actual dispatch and records that dispatch through a subsequent
event. Keeping external messaging outside the state transition makes retries
safe and keeps the tool suitable for local tests and CI.

## Authoritative state

The tracked Markdown ledger remains the human-readable durable record required
by the process. The tool may maintain a colocated machine-readable sidecar
(for example, `<ledger>.state.json`) to avoid lossy Markdown parsing. If a
sidecar is used, define one canonical serialization and make every mutation
update the sidecar and Markdown action log as one recoverable transaction.

State must include at least:

```text
run_state: active | blocked | complete
plan_identity: path plus content digest
milestone_id and milestone title
phase: setup | implementing-initial | reviewing-initial |
       implementing-remediation | reviewing-remediation |
       verifying-approval | adjudicating | blocked | complete
scope_checklist: every planned scope item and its evidence status
finding_set: stable IDs, state, severity, location, evidence, and provenance
remediation_attempts: integer 0..3
implementation_tier: normal | escalation
active_packet: ID, recipient role, purpose, required finding IDs, dispatch state
submission: ID, kind (initial|remediation), packet ID, evidence references
worktree_digest: digest at review dispatch and/or review start
processed_event_ids: immutable idempotency keys
resume_action: one exact imperative next action
final_response_permitted: yes | no
verification_and_adjudication_evidence
```

The state snapshot is a projection of the append-only event/action log. On
every read, the tool should validate that the snapshot agrees with the log;
where practical, derive sensitive values such as the remediation counter from
the log rather than trusting a mutable field alone.

## Event contracts

Use versioned JSON event envelopes. Each must contain a unique event ID,
timestamp, actor, event type, plan/ledger identity, and evidence references.
The tool rejects unknown schemas and duplicated event IDs without performing a
second transition.

```text
run_started
packet_dispatched
implementer_progress
implementer_complete_submission
reviewer_decision
orchestrator_verification_result
adjudication_requested
adjudication_decision
milestone_documented_and_checked
interruption_reconciled
external_blocker_recorded
terminal_decision
```

Important payload requirements:

- A complete implementation submission identifies the active packet, covers
  every checklist item (and every unresolved finding for remediation), and
  provides required RED/GREEN and quality-gate evidence.
- A reviewer decision identifies the review packet and filesystem digest,
  explicitly says `approved` or `rejected`, and returns the complete finding
  set. Each material finding needs stable identity, severity, evidence, and
  location where practical.
- An adjudication request identifies only the allowed bounded question or
  finding IDs plus trigger, authoritative sources, claimed failure path, and
  requested decision.
- A dispatch event records the packet ID and recipient. The system should not
  consider a packet dispatched merely because it has been generated.

## State-machine pseudocode

```text
function handle(plan, ledger, command, event):
    acquire_exclusive_lock(ledger)
    state = load_state_and_validate(plan, ledger)
    reconcile_if_needed(state, plan, ledger)
    assert_global_invariants(state)

    if event.id in state.processed_event_ids:
        return success_with_original_result(event.id)  # idempotent retry

    require event.plan_digest == digest(plan)
    require event.schema_version is supported

    match command, event.type:

        case "apply", "run_started":
            require state does not yet exist
            state = initialize_first_unchecked_milestone(plan)
            append_checkpoint("ledger initialized")
            emit_next_action("dispatch initial normal implementer packet")

        case "apply", "packet_dispatched":
            require event.packet == state.active_packet
            require state.resume_action == dispatch_that_packet
            append_checkpoint("packet dispatched")
            state.resume_action = await_expected_actor_response(event.packet)

        case "apply", "implementer_progress":
            require state.phase is an implementing phase
            require event.packet_id == state.active_packet.id
            append_checkpoint("partial progress; not a complete submission")
            # No review dispatch, status completion, or counter change.

        case "apply", "implementer_complete_submission":
            require state.phase is an implementing phase
            require event.packet_id == state.active_packet.id
            require covers_every_required_scope_item(event, state)
            require has_red_green_and_quality_gate_evidence(event)
            record_complete_submission(event)
            state.phase = reviewing_phase_for(event.submission_kind)
            state.worktree_digest = digest_worktree()
            create_reviewer_packet(state, required_digest = state.worktree_digest)
            append_checkpoint("complete submission accepted; reviewer packet ready")
            emit_next_action("dispatch normal reviewer packet")

        case "apply", "reviewer_decision":
            require state.phase is a reviewing phase
            require event.packet_id == state.active_packet.id
            require event.reviewed_worktree_digest == digest_worktree()
                # A stale review is rejected; it creates neither findings nor attempts.
            require decision_covers_full_milestone_scope(event)

            if event.decision == approved:
                require event.findings has no unresolved material finding
                append_checkpoint("reviewer approved")
                state.phase = verifying_approval
                emit_next_action("run orchestrator verification commands")

            else:  # rejected
                findings = assign_or_preserve_stable_finding_ids(event.findings)
                require findings are complete, material, and actionable
                record_full_finding_set(findings)

                if latest_submission_is_initial(state):
                    # Initial rejection is explicitly uncounted.
                    state.phase = implementing_remediation
                    state.implementation_tier = normal
                    create_complete_remediation_packet(findings)
                    append_checkpoint("initial rejection; attempts remain 0")
                    emit_next_action("dispatch normal remediation packet")

                else:
                    require latest_submission_covered_all_prior_unresolved_findings
                    state.remediation_attempts += 1

                    if state.remediation_attempts == 3:
                        state.run_state = blocked
                        state.phase = blocked
                        state.final_response_permitted = yes
                        state.resume_action = "await user reset, scope change, or external resolution"
                        append_checkpoint("third rejected complete remediation; blocked")
                        emit_terminal_result()
                    else:
                        state.phase = implementing_remediation
                        state.implementation_tier = escalation
                            if state.remediation_attempts == 2 else normal
                        create_complete_remediation_packet(findings)
                        append_checkpoint("complete remediation rejected; counter incremented")
                        emit_next_action("dispatch selected remediation packet")

        case "apply", "orchestrator_verification_result":
            require state.phase == verifying_approval
            record_exact_commands_and_results(event)
            require event.result == passed
            document_contract_changes_and_check_milestone(plan)
            append_execution_log_entry()

            if next_unchecked_milestone_exists(plan):
                state = initialize_next_unchecked_milestone(plan)
                append_checkpoint("next milestone state created")
                emit_next_action("dispatch initial normal implementer packet")
            else:
                state.run_state = complete
                state.phase = complete
                state.final_response_permitted = yes
                state.resume_action = "report completed run"
                append_checkpoint("all milestones approved; terminal completion")

        case "apply", "adjudication_requested":
            require state is active
            require request_is_bounded_and_allowed(event)
            remember_phase_to_restore_after_adjudication()
            state.phase = adjudicating
            create_escalation_reviewer_packet(event.exact_question_or_finding_ids)
            append_checkpoint("bounded adjudication packet ready")
            emit_next_action("dispatch escalation reviewer packet")

        case "apply", "adjudication_decision":
            require state.phase == adjudicating
            require every_result is CONFIRMED_MATERIAL_FINDING | INVALID_FINDING | UNRESOLVED
            update_only_adjudicated_findings(event)
            # Never modify remediation_attempts in this branch.
            restore_phase_before_adjudication()
            append_checkpoint("bounded adjudication recorded")
            emit_exact_prior_resume_action()

        case "resume", "interruption_reconciled":
            inspect plan, ledger, referenced evidence, and worktree
            record facts separately from unrecorded worktree changes
            never infer approval, completed submission, resolution, or attempts
            append_checkpoint("interruption reconciled")
            emit_exact_resume_action()

        case "final-check", none:
            require state.final_response_permitted == yes
            require terminal_state_has_valid_evidence(state)
            require no active packet, unchecked milestone, or nonterminal resume action
            return allow_final_response

        otherwise:
            reject("event is not permitted in the current state")

    atomically_persist_snapshot_and_append_only_markdown_checkpoint(state)
    remember_result_by_event_id(event.id)
    release_lock(ledger)
```

## Mandatory invariants and guards

The future implementation must enforce these rules rather than merely report
them:

1. Only the orchestrator can run a mutating command. Prefer an invocation
   capability/token supplied by the orchestration runtime; at minimum document
   the command as orchestrator-only and restrict write permissions where the
   environment permits it.
2. Lock per ledger/run. A second concurrent invocation must wait or fail
   safely, never interleave action-log rows.
3. Exactly-once event processing through persistent event IDs and deterministic
   replay results.
4. Every state-changing command creates a pre-dispatch or post-event checkpoint
   with time, actor, action, evidence, resulting state, and one exact next
   action.
5. The state snapshot, Markdown headings, plan checkboxes, and action log must
   agree. Reject contradictions instead of guessing how to repair them.
6. A review is valid only for the recorded current filesystem identity. A
   changed worktree requires another review; it does not count as a rejection.
7. Partial work, intermediate test failures, stale review, and initial review
   rejection never increment remediation attempts.
8. Only rejection of a complete remediation submission increments attempts.
   The third such rejection blocks the run and no fourth remediation packet is
   possible.
9. Escalation implementation is selected only when the already-recorded count
   is `2`; escalation adjudication is explicit, bounded, and never affects the
   counter or introduces unrelated findings.
10. An approved milestone must run orchestrator verification and complete the
    documented next-milestone transition before a final response is possible.
11. `final_response_permitted: yes` is legal only for complete runs or valid
    blocked/external-blocked terminal states with recorded evidence and safe
    continuation conditions.

## Worktree identity decision

The implementation plan should choose a digest definition that detects changes
relevant to review without being overly environment-sensitive. Candidate input
is a deterministic combination of repository status and content hashes for
tracked and relevant untracked task files, excluding known transient output.

The tool must record the chosen algorithm and its inputs. It should report a
clear stale-review diagnostic showing expected and observed identities. It must
not rely only on `git diff` if the process needs untracked files reviewed.

## Persistence and recovery design

The plan must specify an atomic write protocol. A reasonable approach is:

1. Acquire a run-specific exclusive lock.
2. Validate the current Markdown and sidecar against the action log.
3. Write the next sidecar snapshot to a same-directory temporary file and
   fsync/rename it.
4. Append the fully rendered Markdown checkpoint, or write a fully rendered
   ledger replacement to a temporary file and fsync/rename it.
5. Store the event result keyed by event ID before releasing the lock.

If atomic cross-file commit is not available, include a transaction ID and
write-ahead intent record so `resume` can detect and complete or roll back a
partially persisted transition. Do not silently reconstruct decisions from
filesystem changes.

## Validation strategy required in the later plan

Build a transition-table/unit-test suite that covers every documented trace:

| Scenario | Required result |
|---|---|
| Initial complete submission approved | Verification, approval, next milestone initialization and dispatch; no final response. |
| Initial review rejected | Full finding set recorded; attempts remain `0`; normal remediation selected. |
| Remediation rejection at attempts `0` | Counter becomes `1`; normal remediation selected. |
| Remediation rejection at attempts `1` | Counter becomes `2`; escalation implementer selected. |
| Escalation remediation approved | Milestone approval path; no automatic escalation reviewer. |
| Escalation remediation rejected | Counter becomes `3`; valid blocked terminal state; no fourth dispatch. |
| Partial implementer update | Logged as progress only; no reviewer dispatch/counter change. |
| Stale reviewer response | Rejected as stale; no finding or counter change. |
| Duplicate callback/event | Same prior result returned; exactly one ledger transition. |
| Ordinary review | No escalation-reviewer packet is permitted. |
| Disputed `R1` | Only `R1` reaches bounded adjudication; counter unchanged. |
| Approved M06 with M07 remaining | M07-only snapshot/checklist and initial dispatch occur in the same run. |
| All milestones approved | Complete terminal checkpoint and final permission `yes`. |
| Interruption before/after each persistence step | `resume` reconciles explicitly with no inferred milestone decision. |
| Invalid ledger/snapshot mismatch | Fail closed with actionable recovery diagnostic. |

Also test schema validation, plan digest mismatch, lock contention, malformed
finding IDs, incomplete remediation coverage, final-check denial in all active
states, and external-blocker terminal evidence.

## Suggested implementation-planning questions

Resolve these before coding:

1. Where should the command live and how should it be packaged with this Go
   repository?
2. Is the sidecar a tracked file, ignored runtime state, or embedded in the
   Markdown ledger via a machine-readable block? Select one source of truth
   and a recovery protocol.
3. How is orchestrator-only mutation enforced in the target runtime?
4. What exact event transport will agent orchestration use: JSON files,
   stdin/stdout, or command arguments? JSON files/stdin are preferable for
   complete evidence payloads.
5. Which repository paths and untracked files belong in the worktree digest?
6. How will a required validation command group be represented and verified?
7. Is automatic repair ever allowed for a ledger mismatch, or must it always
   produce a manual reconciliation event? Default to fail closed.
8. Which human-readable output and JSON output fields are needed for agents,
   reviewers, CI, and a future UI?

## Non-goals for the first implementation

- Automatically selecting task scope, writing implementation packets, or
  judging code quality.
- Replacing the independent reviewer or escalation reviewer with heuristics.
- Dispatching agents or external messages directly.
- Rewriting historic evidence or changing the repository's plan format beyond
  the documented ledger and execution-log updates.
- Treating ordinary command/test failures as terminal conditions.

## Resume command for this conversation

Use the following prompt to continue from this design into an implementation
plan in a later conversation:

```text
Resume the future enhancement described in
docs/ai/future-enhancement-and-stabilization-of-agent-based-loop.md. Read that
document, docs/ai/milestone-supervision.md, docs/ai/run-ledger-layout.md, and
the repository instructions. Then create a concrete, milestone-based
implementation plan for the orchestrator-owned `milestone-flow` state-machine
script. Do not implement it yet. Include source locations, event schema and
persistence decisions, atomic recovery design, command UX, and the full
transition/integration test matrix. Follow the repository milestone-planning
process.
```
