# Cost Optimization Checklist for a Long-Running AI Orchestrator

## Purpose

This document defines what must be verified and enforced in the current orchestration setup to keep the **main orchestrator as inexpensive as possible without sacrificing autonomous completion reliability**.

The assumed architecture is:

* A capable, relatively expensive model acts as the **main orchestrator / parent agent**.
* Cheaper models perform most implementation work as **workers / sub-agents**.
* The full run state is persisted externally in a state file.
* The state file tracks:

    * Milestones
    * Acceptance criteria
    * Review findings
    * Validation status
    * Remaining work
* The orchestrator is expected to continue until the **entire story is implemented and verified**, not merely until the current milestone or worker task is complete.
* Premature termination is considered a critical orchestration failure.

The primary cost objective is therefore:

> **Minimize expensive parent-model tokens and parent-model invocations per successfully completed story.**

The relevant unit of optimization is not cost per turn. It is:

> **Cost per autonomously completed story.**

---

# 1. Keep the Parent's Responsibility Narrow

The expensive parent should act primarily as:

* Orchestrator
* State evaluator
* Task decomposer
* Worker dispatcher
* Review coordinator
* Completion gatekeeper

It should avoid doing implementation work itself unless there is a clear reason.

## Verify

* [ ] The parent does not routinely write application code that could be delegated.
* [ ] The parent does not perform exploratory repository work that a cheaper worker could perform.
* [ ] The parent does not manually execute long debugging loops.
* [ ] The parent does not repeatedly inspect large parts of the repository without a concrete orchestration reason.
* [ ] The parent does not duplicate validation work already performed by workers.
* [ ] Expensive reasoning is reserved for decisions that materially affect the global run.

The parent should primarily answer questions such as:

* What remains unfinished?
* What should happen next?
* Which worker should handle it?
* Is the worker result sufficient?
* Does the state file need updating?
* Is a review finding actually resolved?
* Are all completion conditions satisfied?
* May the run terminate?

---

# 2. Minimize Parent Invocations

A major cost driver is the number of times the expensive orchestrator is invoked.

A worker should therefore receive a sufficiently complete task to perform several related operations before returning control.

## Avoid

```text
Parent
→ Worker edits one file
→ Parent

Parent
→ Worker runs tests
→ Parent

Parent
→ Worker fixes one test
→ Parent

Parent
→ Worker reruns tests
→ Parent

Parent
→ Worker reports completion
→ Parent
```

## Prefer

```text
Parent
→ Worker receives complete milestone objective
→ Worker investigates
→ Worker implements
→ Worker debugs
→ Worker runs required validation
→ Worker fixes failures
→ Worker returns final milestone result
→ Parent evaluates result
```

## Verify

* [ ] Workers are allowed to perform multi-step tasks autonomously.
* [ ] Workers are explicitly instructed to resolve ordinary implementation problems themselves.
* [ ] Workers do not return to the parent after every intermediate action.
* [ ] Worker tasks include their own validation requirements.
* [ ] Worker tasks include clear completion criteria.
* [ ] The parent is invoked primarily at meaningful state transitions.
* [ ] There are no unnecessary "status check" parent calls.
* [ ] Tool or worker events do not automatically wake the parent unless a decision is required.

---

# 3. Use the Persistent State File as the Source of Truth

The external run-state file is one of the most important cost controls in the architecture.

The parent should not depend on remembering the entire run transcript.

## The state file should contain

* Story identifier
* Story goal
* Milestones
* Status of every milestone
* Acceptance criteria
* Status of every acceptance criterion
* Review findings
* Status of every review finding
* Important architectural decisions
* Important blockers
* Validation evidence
* Current active task
* Remaining work
* Final completion status

Example:

```markdown
# Story State

## Story
ABC-123

## Milestones

- [x] M1 — Database schema
- [x] M2 — Service implementation
- [ ] M3 — API integration
- [ ] M4 — Final regression validation

## Acceptance Criteria

- [x] AC1
- [x] AC2
- [ ] AC3

## Review Findings

- [x] RF1
- [ ] RF2

## Current Work

M3 is in progress.

## Completion

STORY_COMPLETE=false
```

## Verify

* [ ] The state file is authoritative.
* [ ] The parent reads the current state instead of reconstructing it from conversation history.
* [ ] Completed work is represented compactly.
* [ ] Old worker transcripts are not needed to determine current status.
* [ ] Important decisions are promoted into the persistent state.
* [ ] Review findings are explicitly tracked until resolved.
* [ ] Acceptance criteria are explicitly tracked until validated.
* [ ] The state file remains small enough to read cheaply.
* [ ] Historical implementation detail is removed or summarized once no longer operationally relevant.

---

# 4. Prevent Context Growth

The parent should not continuously accumulate the complete history of the run.

A long-running agent is cheap only if its effective context remains controlled.

## Do not repeatedly provide

* Full worker conversations
* Full test logs
* Full build logs
* Complete diffs
* Entire repository files that have already been summarized
* Previously resolved debugging discussions
* Old review discussions
* Long implementation transcripts
* Repeated copies of unchanged instructions

## Prefer compact evidence

Instead of:

```text
12,000 lines of test output
```

store:

```text
Validation:
- unit tests: passed
- integration tests: passed
- type check: passed
- lint: passed
- relevant command: npm test -- foo
```

Instead of retaining a complete implementation transcript:

```text
M2 completed.
Files changed:
- src/foo.ts
- src/bar.ts

Validation:
- unit tests passed
- integration tests passed

Decision:
Used transaction boundary at service layer because ...
```

## Verify

* [ ] Worker output is summarized before becoming persistent context.
* [ ] Large command outputs are filtered.
* [ ] Test logs are represented as conclusions plus relevant failures.
* [ ] Large diffs are not repeatedly sent to the parent.
* [ ] Old context is compacted when no longer required.
* [ ] The parent sees only information needed for its current decision.
* [ ] Repository content is fetched on demand rather than permanently carried.

---

# 5. Preserve Prompt Caching

If the underlying API or runtime supports prompt caching, the prompt structure should maximize reusable prefixes.

Stable information should appear before volatile information.

## Prefer this ordering

```text
Stable system instructions
Stable AGENTS.md instructions
Stable orchestration rules
Story definition
Persistent state
Current worker result
Current event
```

rather than constantly rearranging the same content.

## Verify

* [ ] Stable instructions remain byte-for-byte or structurally stable whenever possible.
* [ ] Frequently changing content appears near the end of the prompt.
* [ ] The parent prompt is not unnecessarily regenerated in a different format every turn.
* [ ] Large stable instruction blocks are not dynamically rewritten.
* [ ] Timestamps, random IDs, or volatile metadata do not invalidate otherwise cacheable prefixes unless required.
* [ ] The same orchestration instructions are not duplicated in several changing prompt sections.

---

# 6. Keep AGENTS.md Focused

The orchestrator instructions must be strong enough to prevent premature termination, but unnecessary verbosity also costs tokens on every relevant invocation.

AGENTS.md should contain durable invariants, not large amounts of transient run information.

## It should clearly specify

* The story, not the current milestone, is the unit of completion.
* Every milestone must be complete.
* Every acceptance criterion must be satisfied.
* Every blocking review finding must be resolved.
* Required validation must have passed.
* A worker returning successfully does not imply story completion.
* Completion must be determined from the persistent state.
* If unfinished work remains, the orchestrator must continue.
* Intermediate progress is not a reason to terminate.
* Human intervention should not be required merely to tell the orchestrator to continue.

## Verify

* [ ] Completion rules are explicit.
* [ ] Completion rules are concise.
* [ ] There are no contradictory termination instructions.
* [ ] The same rule is not repeated excessively.
* [ ] Temporary story-specific information is stored in the state file rather than AGENTS.md.
* [ ] The parent can determine whether it may stop through a simple completion predicate.

---

# 7. Define a Hard Completion Predicate

Do not rely on a vague concept of "looks finished."

The orchestrator should evaluate a deterministic completion condition.

Example:

```text
STORY_COMPLETE =
    all milestones == done
AND all acceptance criteria == satisfied
AND all blocking review findings == resolved
AND required validation == passed
AND no known required work remains
```

## Verify

* [ ] Every required completion dimension is machine-readable or clearly structured.
* [ ] The completion predicate is evaluated before final termination.
* [ ] A completed worker task cannot accidentally satisfy the global completion condition.
* [ ] "Current milestone complete" and "story complete" are represented as different states.
* [ ] Review completion and implementation completion are represented separately if necessary.
* [ ] The state file explicitly contains `STORY_COMPLETE=false` until the predicate is actually satisfied.

---

# 8. Enforce Termination Outside the Model Where Possible

The cheapest premature-stop failure is the one that never reaches the user.

If the harness permits it, final termination should be validated programmatically.

Example:

```text
model requests final response
        ↓
harness reads state
        ↓
STORY_COMPLETE?
   │
   ├── no → reject termination and continue
   │
   └── yes → allow final response
```

## Verify

* [ ] A final assistant response is not automatically considered valid completion.
* [ ] The harness checks the persistent story state before accepting termination.
* [ ] If unfinished obligations remain, the harness automatically re-enters the loop.
* [ ] The user is not required to manually say "continue."
* [ ] A model mistake cannot silently convert an incomplete story into a completed run.

This also permits cheaper models to be tested safely because premature termination becomes recoverable rather than fatal.

---

# 9. Delegate Work at the Largest Safe Granularity

Worker assignments should be large enough to reduce parent round-trips but small enough to remain independently verifiable.

Poor granularity:

```text
Rename this function.
```

Better granularity:

```text
Implement milestone M3 completely.

Requirements:
- integrate endpoint X
- update validation
- update tests
- fix regressions caused by the change
- run required checks

Return only when:
- implementation is complete, or
- a genuine architectural/blocking decision requires parent input
```

## Verify

* [ ] Worker tasks correspond roughly to complete milestones or meaningful sub-milestones.
* [ ] Workers receive all information required to finish the assignment.
* [ ] Workers can inspect the repository themselves.
* [ ] Workers can run tests themselves.
* [ ] Workers can fix ordinary failures themselves.
* [ ] Workers return only for blockers that actually require orchestration.

---

# 10. Prevent Worker Escalation Spam

Cheap workers can indirectly make the expensive parent costly if they escalate too frequently.

## Workers should not escalate

* Minor test failures
* Straightforward compiler errors
* Formatting issues
* Obvious missing imports
* Routine merge conflicts inside their own work
* Normal debugging
* Small implementation decisions already covered by repository conventions

## Workers should escalate

* Conflicting acceptance criteria
* Architectural decisions outside delegated authority
* Missing credentials or inaccessible systems
* Irreversible/destructive operations requiring approval
* Contradictory repository requirements
* Genuine product decisions
* A blocker that cannot be resolved from available information

## Verify

* [ ] Worker prompts define escalation criteria.
* [ ] Workers are explicitly expected to self-debug.
* [ ] Workers distinguish "difficulty" from "blocker."
* [ ] Parent calls caused by trivial issues are measured.

---

# 11. Return Structured Worker Results

Worker results should be compact and easy for the parent to evaluate.

Recommended format:

```markdown
## Result

Status: COMPLETE

## Changes

- Implemented ...
- Updated ...
- Removed ...

## Files

- path/a
- path/b

## Validation

- unit tests: PASS
- integration tests: PASS
- lint: PASS
- type check: PASS

## Acceptance Criteria Covered

- AC2
- AC3

## Remaining Issues

None.

## Parent Decision Required

No.
```

## Verify

* [ ] Worker output has a standard schema.
* [ ] The parent does not need expensive reasoning merely to understand worker prose.
* [ ] Validation evidence is explicit.
* [ ] Remaining blockers are explicit.
* [ ] A worker cannot ambiguously report partial work as completion.
* [ ] Full logs are omitted unless required for diagnosis.

---

# 12. Use Cheap Models for Mechanical Tasks

The strongest model should not perform work that can reliably be handled by a cheaper model.

Candidates for cheaper workers include:

* Repository search
* File discovery
* Simple refactors
* Test execution
* Formatting
* Documentation updates
* Boilerplate implementation
* Straightforward test creation
* Log analysis
* Static code inspection
* Mechanical review checks
* Gathering evidence for acceptance criteria

The expensive parent should handle:

* Global planning
* Cross-milestone reasoning
* Conflicting findings
* Architecture-sensitive delegation
* Deciding whether evidence is sufficient
* Determining whether the story is actually complete

---

# 13. Avoid Parent-Level High Reasoning Unless Needed

If the parent model supports configurable reasoning effort, the highest setting should be justified by measured reliability.

The relevant tradeoff is:

```text
lower reasoning cost
vs.
premature termination / wrong orchestration decisions
```

A cheaper reasoning setting is not cheaper if it causes:

* More retries
* More parent turns
* Wrong delegation
* Repeated work
* Missed review findings
* Human interventions
* Premature termination

## Verify

* [ ] Reasoning level is selected through evaluation, not intuition.
* [ ] Lower-cost settings have been tested against real stories.
* [ ] Premature termination rate is measured.
* [ ] Parent token usage per successful story is measured.
* [ ] Human intervention rate is measured.
* [ ] A more expensive model is retained if it materially improves completion reliability.

---

# 14. Optimize for Cost per Successful Story

Do not optimize individual turns in isolation.

Track:

```text
parent_cost
+ worker_cost
+ retries
+ failed runs
+ repeated work
+ human recovery effort
--------------------------------
successfully completed stories
```

A model that costs 40% less per token but requires repeated human intervention may be economically worse.

## Primary metrics

* [ ] Total cost per completed story
* [ ] Parent cost per completed story
* [ ] Worker cost per completed story
* [ ] Parent calls per completed story
* [ ] Parent input tokens per completed story
* [ ] Parent cached input tokens per completed story
* [ ] Parent reasoning/output tokens per completed story
* [ ] Worker calls per completed story
* [ ] Retry count
* [ ] Failed worker assignments
* [ ] Premature parent terminations
* [ ] Human interventions
* [ ] Stories completed fully autonomously

---

# 15. Measure Premature Termination as a First-Class Failure

For a long-running orchestrator, per-turn reliability compounds.

Even a low probability of erroneous termination can become unacceptable over many orchestration decisions.

Therefore track:

```text
Autonomous Completion Rate =
stories completed without human "continue" intervention
/
all attempted stories
```

## Verify

* [ ] Premature stop events are explicitly logged.
* [ ] The exact state at termination is captured.
* [ ] Remaining milestones are recorded.
* [ ] Remaining acceptance criteria are recorded.
* [ ] The model and reasoning setting are recorded.
* [ ] The number of parent turns before failure is recorded.
* [ ] Comparisons between orchestrator models use full-story completion rate, not isolated benchmark prompts.

---

# 16. Control Review Loops

Review can create expensive ping-pong between parent and workers.

Prefer:

```text
implementation worker
        ↓
review worker
        ↓
structured findings
        ↓
parent decides what is blocking
        ↓
fix worker receives all blocking findings together
        ↓
review again only where necessary
```

Avoid:

```text
review finds one issue
→ parent
→ worker
→ parent
→ review
→ parent
→ worker
...
```

## Verify

* [ ] Review findings are batched.
* [ ] Multiple related findings are fixed together.
* [ ] Non-blocking findings do not create unnecessary parent cycles.
* [ ] Resolved findings are marked in the persistent state.
* [ ] Review workers provide concise, structured findings.
* [ ] Re-review is scoped to changed or affected areas where possible.

---

# 17. Avoid Revalidating Everything After Every Milestone

Validation should match the risk and scope of the change.

During intermediate milestones:

* Run relevant tests.
* Run affected integration checks.
* Run local static validation.

At final completion:

* Run the full required acceptance suite.
* Run global validation required by the repository/story.

## Verify

* [ ] Full regression suites are not executed after every trivial change unless required.
* [ ] Milestone-specific validation is defined.
* [ ] Final story validation is clearly separated from intermediate validation.
* [ ] Workers run their own validation whenever possible.
* [ ] Parent reasoning is not spent analyzing successful repetitive test output.

---

# 18. Do Not Send Raw Repository State to the Parent

The parent should request targeted information rather than consume broad repository dumps.

## Prefer

```text
Need:
- interface definition
- affected callers
- relevant tests
```

over:

```text
Send the entire src/ tree.
```

## Verify

* [ ] Repository exploration is delegated.
* [ ] Search results are summarized.
* [ ] Only relevant source excerpts reach the parent.
* [ ] Large generated files are excluded.
* [ ] Lockfiles, build artifacts, binaries, and irrelevant logs do not enter model context.
* [ ] Diffs are scoped.

---

# 19. Prevent Duplicate Work

Repeated implementation is expensive even when workers are cheap because it causes additional orchestration turns.

## Verify

* [ ] The state file records what has already been implemented.
* [ ] Workers read the latest state before starting.
* [ ] Workers inspect existing repository changes before reimplementing functionality.
* [ ] Parent assignments specify what is already done.
* [ ] Review findings reference concrete existing work.
* [ ] Failed workers leave enough state for replacement workers to continue rather than restart.

---

# 20. Make Runs Recoverable

An interrupted run should resume from external state without replaying the entire history.

## Verify

* [ ] The state file contains enough information to resume the story.
* [ ] Current milestone status is persisted.
* [ ] Important decisions are persisted.
* [ ] Validation results are persisted.
* [ ] Review findings are persisted.
* [ ] Worker ownership is not required to understand completed work.
* [ ] A fresh parent instance can continue from the current state.
* [ ] Recovery does not require replaying previous parent/worker conversations.

This reduces both token cost and operational fragility.

---

# 21. Separate Operational State from Historical Notes

The parent primarily needs **current obligations**, not a diary.

Recommended split:

```text
story-state.md
    Current authoritative operational state

decisions.md
    Durable architectural/product decisions

run-log.md
    Optional historical/debugging information
```

Only the first two should normally enter parent context.

## Verify

* [ ] Historical logs are not automatically included in every orchestration call.
* [ ] The operational state stays concise.
* [ ] Architectural decisions are separately retrievable.
* [ ] Debug logs remain available without polluting the normal prompt.

---

# 22. Use Explicit State Transitions

A predictable state machine reduces expensive reasoning.

Example:

```text
PENDING
  ↓
IMPLEMENTING
  ↓
VALIDATING
  ↓
REVIEW
  ↓
FIXING
  ↓
DONE
```

Story level:

```text
PLANNING
  ↓
EXECUTING
  ↓
FINAL_REVIEW
  ↓
FINAL_VALIDATION
  ↓
COMPLETE
```

## Verify

* [ ] Every milestone has an explicit state.
* [ ] The parent can determine the next valid action cheaply.
* [ ] Workers cannot skip required states.
* [ ] `DONE` means validated completion, not merely "implementation attempted."
* [ ] Story `COMPLETE` is distinct from milestone `DONE`.

---

# 23. Avoid Expensive Parent Self-Reflection on Every Turn

Repeated prompts such as:

```text
Reconsider the entire story.
Review all decisions.
Think deeply about everything that could still be wrong.
```

can massively increase reasoning tokens.

Use deep global review only at meaningful checkpoints.

## Good checkpoints

* Initial decomposition
* Architectural blocker
* Completion of a major milestone group
* Pre-final review
* Final completion decision

## Verify

* [ ] Global re-planning is not performed after every worker result.
* [ ] The parent reads structured state instead of rediscovering progress.
* [ ] Deep reasoning is triggered by exceptions or checkpoints.
* [ ] Routine transitions remain routine.

---

# 24. Detect Loops and Thrashing

Repeated parent/worker cycles can silently dominate costs.

Track recurring patterns such as:

```text
same test failure
same review finding
same worker assignment
same file being rewritten
same blocker being escalated
```

## Verify

* [ ] Repeated identical failures are detected.
* [ ] The parent changes strategy after a defined number of failed attempts.
* [ ] Workers receive previous failure evidence.
* [ ] Replacement workers do not blindly repeat the same approach.
* [ ] A retry budget exists.
* [ ] Persistent blockers are escalated appropriately.

---

# 25. Batch Parent Decisions

Where safe, the parent should make several related orchestration decisions in one invocation.

Example:

Instead of:

```text
Which worker handles M4?
```

then later:

```text
What validation should M4 run?
```

then:

```text
Should review happen afterward?
```

the parent can produce:

```text
M4 assignment:
- worker type
- implementation objective
- constraints
- required validation
- expected result schema
- review requirement
```

## Verify

* [ ] Worker assignment and validation expectations are created together.
* [ ] Predictable follow-up decisions are made in the same parent turn.
* [ ] The parent is not invoked merely to answer questions that were foreseeable one turn earlier.

---

# 26. Keep Worker Outputs Bounded

Cheap workers can still generate large outputs that become expensive when consumed by the expensive parent.

## Worker output limits should favor

* Conclusions
* Changed files
* Validation evidence
* Blocking issues
* Relevant decisions
* Remaining work

## They should avoid

* Narrating every tool call
* Full terminal transcripts
* Full source-file dumps
* Repeating the original task
* Large speculative explanations
* Long summaries of successful routine work

## Verify

* [ ] Worker output format is concise.
* [ ] Raw logs are stored separately.
* [ ] The parent receives references or summaries where possible.
* [ ] Maximum useful response size is defined.

---

# 27. Keep the Parent Out of the Hot Implementation Loop

A healthy architecture should look approximately like:

```text
                    ┌──────────────────┐
                    │ Persistent State │
                    └────────┬─────────┘
                             │
                             ▼
                    ┌──────────────────┐
                    │ Expensive Parent │
                    │  Orchestrator    │
                    └────────┬─────────┘
                             │
            ┌────────────────┼────────────────┐
            ▼                ▼                ▼
       Cheap Worker     Cheap Worker     Cheap Worker
       implementation   testing          review
            │                │                │
            └────────────────┼────────────────┘
                             ▼
                       Compact Results
                             │
                             ▼
                    ┌──────────────────┐
                    │ Expensive Parent │
                    │ Next decision    │
                    └──────────────────┘
```

The expensive parent should not sit between every tool call performed by the workers.

---

# 28. Model Selection Must Be Evaluated End-to-End

For the parent, compare candidate configurations on real stories.

For example:

```text
Configuration A:
Sol high

Configuration B:
Terra xhigh
```

Keep constant:

* Same stories
* Same repository
* Same worker models
* Same worker prompts
* Same state format
* Same acceptance criteria
* Same review process
* Same completion predicate

Measure differences in:

* Total story cost
* Parent cost
* Parent calls
* Parent tokens
* Reasoning tokens
* Run duration
* Premature stops
* Wrong delegations
* Repeated work
* Human interventions
* Final correctness

## Decision criterion

Do not select the cheaper model merely because its token price is lower.

Select the configuration with the best combination of:

```text
low cost
+
high autonomous completion rate
+
high final correctness
```

---

# 29. Run Cost Telemetry Must Be Persisted

Every run should produce enough telemetry to identify where money is being spent.

Recommended record:

```json
{
  "story": "ABC-123",
  "parent": {
    "model": "...",
    "reasoning_effort": "...",
    "calls": 24,
    "input_tokens": 0,
    "cached_input_tokens": 0,
    "output_tokens": 0,
    "reasoning_tokens": 0,
    "cost": 0
  },
  "workers": {
    "calls": 38,
    "cost": 0
  },
  "retries": 2,
  "premature_stops": 0,
  "human_interventions": 0,
  "story_completed": true,
  "validation_passed": true,
  "total_cost": 0
}
```

## Verify

* [ ] Parent and worker costs are tracked separately.
* [ ] Cached and uncached input are distinguishable.
* [ ] Reasoning/output consumption is visible.
* [ ] Parent calls are counted.
* [ ] Worker calls are counted.
* [ ] Retry costs are visible.
* [ ] Failed runs are included in aggregate cost calculations.
* [ ] Human interventions are tracked.

---

# 30. Establish Cost Regression Tests

Changes to prompts, orchestration logic, state format, or model configuration should be evaluated against a representative story suite.

## Track over time

```text
median cost / completed story
p90 cost / completed story
median parent calls / story
median parent reasoning tokens / story
autonomous completion rate
premature termination rate
retry rate
```

## Verify

* [ ] A representative evaluation set exists.
* [ ] Prompt changes are benchmarked.
* [ ] Model changes are benchmarked.
* [ ] State-format changes are benchmarked.
* [ ] Orchestration changes are benchmarked.
* [ ] Cost regressions are treated similarly to performance regressions.

---

# 31. Investigate Expensive Outliers

Average cost can hide pathological runs.

For every unusually expensive story, determine whether the cause was:

* Excessive parent calls
* Excessive reasoning
* Growing context
* Cache misses
* Worker failures
* Review loops
* Test/debugging loops
* Duplicate work
* Poor task decomposition
* Premature termination followed by restart
* Repeated repository exploration
* Oversized worker output
* Unnecessary full validations

## Verify

* [ ] p90/p95 story cost is monitored.
* [ ] Outlier runs are inspectable.
* [ ] Every parent invocation can be tied to a reason.
* [ ] Repeated patterns can be identified automatically.

---

# 32. Optimize Parent Wake-Up Policy

The parent should wake only when its judgment is actually needed.

Good wake-up events:

* Worker completed assignment
* Worker encountered genuine blocker
* Milestone validation failed after worker self-recovery
* Review produced blocking findings
* Milestone became complete
* Story reached final completion evaluation

Poor wake-up events:

* Worker opened a file
* Worker edited a file
* Worker started tests
* One test temporarily failed
* Worker installed a dependency
* Worker made routine implementation decisions

## Verify

* [ ] Wake-up conditions are explicit.
* [ ] Worker progress events do not automatically invoke the parent.
* [ ] Parent calls correspond to orchestration decisions.

---

# 33. Prefer Evidence Over Re-Reasoning

The parent should not rediscover facts that workers can supply directly.

Instead of asking the parent:

```text
Do you think the tests probably pass?
```

provide:

```text
Command:
npm test

Exit status:
0

Relevant suites:
37 passed
0 failed
```

Then the parent merely records the evidence.

## Verify

* [ ] Workers return concrete validation evidence.
* [ ] Workers return file paths and relevant identifiers.
* [ ] Review workers cite findings precisely.
* [ ] Parent reasoning is used for judgment, not fact reconstruction.

---

# 34. Keep Final Verification Separate and Explicit

Before termination, the parent should perform one dedicated global completion evaluation.

Example:

```text
1. Read current story state.
2. Confirm every milestone is DONE.
3. Confirm every acceptance criterion is satisfied.
4. Confirm every blocking review finding is resolved.
5. Confirm required final validation passed.
6. Confirm no known required work remains.
7. Set STORY_COMPLETE=true.
8. Only then produce the final response.
```

## Verify

* [ ] Final completion is a distinct phase.
* [ ] The parent does not infer completion merely because the latest worker succeeded.
* [ ] Final validation evidence is available.
* [ ] The persistent state is updated before termination.
* [ ] `STORY_COMPLETE=true` is written only after all gates pass.

---

# 35. Define the Optimization Priority Correctly

For this setup, optimization should occur in approximately this order:

1. **Prevent premature termination.**
2. **Prevent failed or repeated work.**
3. **Reduce expensive parent invocations.**
4. **Reduce parent reasoning/output tokens.**
5. **Keep parent context compact.**
6. **Maximize cache reuse.**
7. **Delegate more work to cheaper workers.**
8. **Reduce worker cost where it does not reduce reliability.**
9. **Evaluate cheaper parent models only after the above controls are measurable.**

A slightly more expensive parent that completes stories autonomously can be cheaper overall than a cheaper parent that:

* Stops early
* Needs human prompting
* Makes poor delegations
* Repeats work
* Requires retries
* Loses track of acceptance criteria

---

# Minimum Required Invariants

The following rules should never be violated:

```text
1. The story is the unit of completion.

2. A milestone completing does not mean the story is complete.

3. A worker completing does not mean the story is complete.

4. The persistent state is the source of truth.

5. The parent must continue while any required milestone,
   acceptance criterion, validation, or blocking review finding
   remains unfinished.

6. Workers should complete substantial autonomous units of work
   before returning to the parent.

7. The expensive parent should be invoked only when its judgment
   is required.

8. Parent context must remain compact and operational.

9. Raw history must not accumulate indefinitely in parent context.

10. Termination should be mechanically rejected whenever the
    completion predicate is false, if the harness allows this.

11. Cost must be evaluated per successfully and autonomously
    completed story.

12. Premature termination and human intervention are cost failures,
    not merely UX issues.
```

# Recommended Dashboard

At minimum, monitor these numbers for every orchestrator configuration:

| Metric                                   | Goal     |
| ---------------------------------------- | -------- |
| Autonomous story completion rate         | Maximize |
| Premature stops per story                | Zero     |
| Human interventions per story            | Zero     |
| Parent calls per story                   | Minimize |
| Parent reasoning/output tokens per story | Minimize |
| Parent input tokens per story            | Minimize |
| Cached-input ratio                       | Maximize |
| Worker retries per story                 | Minimize |
| Review/fix cycles per story              | Minimize |
| Total cost per completed story           | Minimize |
| Final validation success rate            | Maximize |

# Practical Target Architecture

```text
                         STORY
                           │
                           ▼
                  ┌─────────────────┐
                  │ Persistent State│
                  └────────┬────────┘
                           │
                           ▼
                  ┌─────────────────┐
                  │ Capable Parent  │
                  │  Orchestrator   │
                  └────────┬────────┘
                           │
                    Large assignment
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
         Cheap Impl     Cheap Test   Cheap Review
           Worker         Worker        Worker
              │            │            │
              └────────────┼────────────┘
                           │
                    Structured results
                           │
                           ▼
                  ┌─────────────────┐
                  │ Update State    │
                  └────────┬────────┘
                           │
                           ▼
                    Completion gate
                       │         │
                     false      true
                       │         │
                    continue    finish
```

The ideal parent is therefore not the model doing the most work.

It is the model that makes the **fewest necessary high-quality decisions required to move a story from initial state to verified completion without human intervention**.
