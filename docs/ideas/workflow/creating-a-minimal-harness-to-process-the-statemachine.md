# Minimal Harness for Processing the Orchestration State Machine

Status: initial

Yes. **That would likely be one of the most effective ways to prevent the orchestrator from “falling asleep” prematurely**—provided the loop enforces it outside the model.

The key change is:

> The model no longer decides whether the run is finished.
> The state machine decides.

I would actually go one step further than “the orchestrator should call the script after every action”: **the harness should invoke the script automatically after every relevant action.** If the model itself has to remember to call it, you are only moving the same probabilistic failure somewhere else.

The control flow should look roughly like this:

```text
MODEL ACTION
    │
    ▼
tool / worker / edit / review / validation
    │
    ▼
STATE TRANSITION SCRIPT
    │
    ├── CONTINUE
    │      │
    │      └──► next orchestrator turn
    │
    ├── COMPLETE
    │      │
    │      └──► successful termination allowed
    │
    └── BLOCKED / ERROR
           │
           └──► recovery / escalation logic
```

Most importantly:

```text
Orchestrator emits "final answer"
        │
        ▼
Harness checks state machine
        │
        ├── COMPLETE=true
        │      └── accept final answer
        │
        └── COMPLETE=false
               └── reject termination
                   inject next turn
```

### `final` should no longer be a stop signal

Right now the implicit behavior is probably something like:

```text
assistant tool_call → continue
assistant normal_message → stop
```

That is fragile for long-running autonomous agents.

A better model is:

```text
assistant tool_call
    → execute
    → state transition
    → continue

assistant normal_message
    → state transition
    → if COMPLETE: accept
    → otherwise: message is NOT terminal
                 → continue
```

So Terra could suddenly say after M3:

> Everything looks good. The implementation is complete.

The harness checks:

```text
M1 DONE
M2 DONE
M3 DONE
M4 PENDING
AC7 PENDING
RF3 OPEN

STORY_COMPLETE=false
```

and simply feeds back something like:

```text
RUN_NOT_COMPLETE

Remaining obligations:
- M4
- AC7
- RF3

Continue execution.
```

The model is allowed to make the mistake. **The mistake simply no longer terminates the run.**

That is much more robust than repeatedly strengthening the prompt with more “DO NOT STOP” instructions.

## Give the state machine as much authority as possible

Ideally, the script should not merely print a status. It should calculate the actual run state from persisted facts.

For example:

```json
{
  "story_complete": false,
  "run_state": "EXECUTING",
  "current_milestone": "M4",
  "remaining_milestones": ["M4"],
  "remaining_acceptance_criteria": ["AC7"],
  "open_review_findings": ["RF3"],
  "required_action": "CONTINUE",
  "reason": "Story completion predicate is false"
}
```

The parent probably only needs a compact version:

```text
CONTINUE
Current: M4
Remaining: M4, AC7, RF3
```

There is no reason to spend expensive orchestrator tokens on a long explanation every time.

## Validate transitions, not just the current status

I would make the state machine prevent invalid transitions as well.

For example:

```text
IMPLEMENTING
    ↓ implementation result
VALIDATING
    ↓ validation evidence
REVIEWING
    ↓ no blocking findings
DONE
```

A milestone should not be able to jump directly from:

```text
IMPLEMENTING → DONE
```

if required validation is still missing.

The script could reject such a transition:

```json
{
  "transition": "REJECTED",
  "requested": "M4 -> DONE",
  "actual_state": "VALIDATING",
  "missing": [
    "integration_tests",
    "review"
  ],
  "required_action": "CONTINUE"
}
```

That removes even more state-management responsibility from the LLM.

## Let the caller choose the milestone commit policy

The harness should accept an explicit commit policy from the caller. The policy
controls only changes owned by the current milestone implementation and must be
persisted in the run state so that resumed execution uses the same behavior.

The supported modes should be:

```text
none
    Do not stage or commit changes automatically.

milestone
    After implementation, remediation, validation, and review have all
    succeeded, create one commit containing all changes owned by the milestone.

incremental
    Create smaller commits at coherent implementation boundaries. Each commit
    must represent a validated slice of the milestone; review remediation may
    produce additional focused commits.
```

For example, the harness could expose:

```text
harness run --commit-policy=none
harness run --commit-policy=milestone
harness run --commit-policy=incremental
```

`none` should be the default when the caller does not choose a policy. This
preserves the existing rule that commits require explicit authorization.

The caller's choice is authoritative. The orchestrator and workers must not
silently change it during a run. If a different policy is needed for a later
milestone, the caller may override it explicitly at that milestone boundary,
and the harness must record the new value before implementation begins.

### Commit safety rules

Automatic committing must remain a deterministic harness responsibility, not
an optional instruction that the model has to remember. In every mode, the
harness should:

* capture the repository and worktree baseline before milestone implementation;
* distinguish milestone-owned changes from pre-existing or concurrent user
  changes;
* never stage or commit changes that are not owned by the milestone;
* refuse an automatic commit when ownership cannot be determined safely;
* never push, publish, rewrite history, squash existing commits, or use
  destructive cleanup as part of this policy;
* record every created commit SHA and its milestone or slice in persistent run
  state; and
* include the effective policy and commit results in the milestone completion
  evidence.

In `milestone` mode, `DONE` is not valid until the single milestone commit has
been created successfully. The commit must include implementation changes and
all review remediation for that milestone. A commit failure leaves the
milestone incomplete and enters normal recovery or escalation handling.

In `incremental` mode, the implementation packet should define the intended
slice boundaries where they are known. A worker may propose an additional
boundary, but the harness creates a commit only after the slice's required
validation succeeds. The final milestone transition must verify that every
milestone-owned change is either included in a recorded slice or remediation
commit, with no residual milestone-owned changes left uncommitted.

In `none` mode, the harness leaves all milestone changes uncommitted and reports
the changed paths at completion. An uncommitted milestone is still allowed to
reach `DONE` when its implementation, validation, and review requirements are
satisfied.

The state machine can represent the decision and its evidence compactly:

```json
{
  "milestone": "M4",
  "commit_policy": "milestone",
  "commit_state": "PENDING",
  "created_commits": [],
  "required_action": "COMMIT_MILESTONE"
}
```

After a successful automatic commit:

```json
{
  "milestone": "M4",
  "commit_policy": "milestone",
  "commit_state": "COMPLETE",
  "created_commits": ["<full-commit-sha>"],
  "required_action": "ADVANCE"
}
```

This keeps commit behavior caller-controlled while allowing the harness to
enforce it consistently across implementation, review, remediation, resume,
and completion.

## Do not rely on the model to invoke the script

I would **not** make this the primary mechanism:

```text
AGENTS.md:
"After every action you MUST run update-state.sh."
```

You can still include that instruction, but the actual guarantee should live in the harness:

```text
after_action() {
    execute_action
    run_state_machine
}
```

Otherwise the same orchestrator that forgets to continue can also forget to call `update-state.sh`.

**Deterministic behavior belongs in the deterministic part of the system.**

## This could make cheaper parent models viable again

This is especially interesting for your Terra experiment.

Right now the parent has to do all of this:

```text
1. Understand the story.
2. Plan the work.
3. Delegate intelligently.
4. Evaluate worker results.
5. Track global obligations.
6. Reliably decide whether execution must continue.
7. Terminate exactly once, at the correct point.
```

With a state-machine-controlled loop, you remove a critical part:

```text
6 + 7 → deterministic harness
```

Then Terra xhigh no longer has to guarantee the liveness of the whole run. It mainly has to produce the **next sensible action**.

That could significantly reduce the capability gap between Terra and Sol for this particular job.

It might even make Terra high usable again—not because it suddenly becomes smarter, but because its most damaging failure:

> “I think we're done.”

no longer has any authority.

## Do not make `COMPLETE` the only possible halt state

I would make it the only **successful termination state**, but not the only possible halt.

You still want exceptional states such as:

```text
COMPLETE
BLOCKED_REQUIRES_HUMAN
FATAL_ERROR
CANCELLED
```

Otherwise a genuinely unsolvable situation could become:

```text
CONTINUE
→ fail
→ CONTINUE
→ fail
→ CONTINUE
→ ...
```

and burn money forever.

So preferably:

```text
SUCCESS TERMINATION:
    only STATE == COMPLETE

NON-SUCCESS HALT:
    BLOCKED_REQUIRES_HUMAN
    FATAL_ERROR
    CANCELLED
```

`BLOCKED` should also be defined strictly enough that the model cannot use it as a new convenient way to stop working.

### Recommended completion predicate

```text
COMPLETE =
    all milestones DONE
AND all acceptance criteria SATISFIED
AND all blocking review findings RESOLVED
AND all mandatory validation PASSED
AND no required follow-up work exists
```

And **only the script/state machine should be allowed to set `COMPLETE=true`.**

That gives you a much stronger autonomous architecture:

> **The model decides what to do next. The state machine decides whether the work is allowed to end.**
