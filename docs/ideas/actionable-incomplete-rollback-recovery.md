# Idea: actionable recovery from an incomplete rollback

Status: initial

## Summary

When a mutating `wtree` operation fails and cannot completely undo its work,
the safest behavior is to preserve anything whose ownership is uncertain. That
avoids deleting a user's checkout, but it can leave branches, worktrees,
grouping directories, Git registrations, or metadata behind. `wtree` records
the incomplete rollback and blocks another mutation, yet today the user must
translate that record into manual recovery steps.

Recovery support could be delivered in two increments:

1. Extend `wtree doctor` so it explains the incomplete rollback and prints the
   exact commands needed to inspect and safely recover it.
2. Add a dedicated `wtree recover` command that performs only recovery actions
   whose ownership can be proven and preserves everything else for explicit
   human review.

The first increment makes the existing safety-first behavior usable without
committing to an automated recovery engine. The second increment turns the
same evidence and safety rules into a repeatable, transactional workflow.

## Problem

An incomplete rollback is deliberately treated as a serious, visible error:

- the command fails instead of reporting success;
- the error identifies the rollback as incomplete and points to durable
  recovery metadata;
- the recovery record names the failed step, completed steps, unreverted
  steps, and rollback failures;
- later mutations are blocked while the record remains unresolved; and
- project inventory and `doctor` report that manual action is required.

This prevents silent data loss, but the record is an implementation-oriented
diagnostic rather than a user-oriented procedure. A user still has to answer:

- Which directories and Git registrations belong to the failed operation?
- Which paths contain work that must be preserved?
- Which branch can be removed, and which branch existed beforehand?
- In what order should a repository forest be repaired?
- When is it safe to remove the recovery record and retry?

The difficult case is not a normal interrupted command. Ordinary failures
usually roll back cleanly. The difficult case occurs when the filesystem, Git,
a hook or wrapper, another process, or the user changes an affected path while
the transaction is running or rolling back. At that point, deleting the
current occupant merely because it is at the expected path can destroy data
that the transaction did not create.

The governing rule should remain: uncertainty produces a visible leftover,
not deletion.

## Goals

- Make every incomplete rollback immediately actionable.
- Explain what remains, why `wtree` preserved it, and what blocks the next
  mutation.
- Generate commands from verified facts instead of generic cleanup advice.
- Distinguish read-only inspection, proven-safe cleanup, and actions that need
  human confirmation.
- Preserve human-output and JSON-output parity.
- Support root-Git projects, plain logical roots, multiple top-level
  repositories, grouped mounts, and nested repositories.
- Make recovery repeatable: interruption during recovery must leave durable,
  accurate evidence that can be inspected and resumed.

## Non-goals

- Treating a clean working tree as proof that `wtree` owns it.
- Automatically deleting an unknown or substituted checkout.
- Guessing ownership from a path, branch name, or commit alone.
- Bypassing the project lock, recovery-generation checks, or normal Git
  worktree safety.
- Making `doctor --fix` a general destructive repair command.
- Hiding an unresolved recovery record merely so another mutation can run.

## Increment 1: `doctor` prints a recovery procedure

`wtree doctor` should remain read-only unless the user separately selects an
existing allowlisted repair mode. For an incomplete rollback, its first job is
to turn durable evidence into a concrete recovery procedure.

### Recovery discovery

Doctor should be able to diagnose recovery metadata even when the failed
operation never published workspace state. It therefore cannot depend only on
resolving an existing workspace. It should accept a project/workspace selector
or recovery-record path and correlate the record with validated project
configuration, registry data, workspace state when present, and current Git
worktree registrations.

Malformed, unsupported, ambiguous, or mismatched records should still be
reported. They should not produce destructive commands.

### Human output

For each unresolved step, doctor should show:

1. What the failed operation attempted.
2. What was successfully reverted.
3. What remains or cannot be proven.
4. Read-only commands that inspect the relevant path, repository identity,
   branch, commit, working-tree status, and Git registration.
5. A cleanup command only when all required ownership facts agree.
6. A final verification command and the condition for removing the recovery
   record.

Commands must be rendered with correct platform-specific quoting. A suggested
command must be copyable, must use resolved paths rather than ambiguous working
directory assumptions, and must state whether it is:

- `inspect`: read-only and safe to run;
- `verified cleanup`: supported by complete ownership evidence;
- `manual decision`: potentially destructive and not safe for automatic
  execution; or
- `blocked`: insufficient or contradictory evidence.

Examples of useful inspection commands include `git worktree list
--porcelain`, `git status --porcelain`, branch and `HEAD` inspection, and
filesystem inspection of retained paths. The exact cleanup commands depend on
the operation and evidence. Doctor must not print an unconditional `rm -rf`,
force-remove a worktree, or delete a branch when ownership is uncertain.

### JSON output

The JSON report should expose the same procedure as structured actions rather
than embedding commands only in prose. Each action should include a stable
code, classification, repository or step identifier, command arguments,
reason, prerequisites, and whether automatic execution would be allowed.

Existing finding fields and codes should remain compatible. Recovery guidance
should be additive or introduced through an explicitly versioned result if an
additive representation cannot preserve the public contract.

### Completion of manual recovery

Doctor should print the command that verifies the repaired state. The recovery
record is removed only after that verification establishes that every
unreverted effect is resolved and no contradictory replacement occupies an
owned path. Removing the record must be the final step, not the first step.

For legacy records that lack enough identity evidence, doctor should provide
inspection commands and explain the manual decision. It must not promote a
guess to a verified cleanup action.

## Increment 2: a `wtree recover` command

A later `wtree recover` command should execute the recovery procedure under the
same rules that doctor explains. Doctor remains the diagnostic surface;
`recover` becomes the explicit mutation surface.

The command should support a read-only plan, human and JSON output, progress
events, cancellation, and an explicit execution mode. The precise syntax is a
specification question, but the conceptual workflow is:

```text
wtree recover <workspace-or-record> --dry-run
wtree recover <workspace-or-record> --apply
```

Recovery should:

1. Resolve exactly one project and recovery record.
2. Acquire the project mutation lock.
3. Snapshot and validate the recovery-record generation.
4. Reconstruct the affected repository topology and operation-owned effects.
5. Revalidate filesystem and Git identity immediately before every mutation.
6. Execute verified actions in dependency-safe order.
7. Preserve uncertain replacements and report the remaining manual action.
8. Atomically update durable recovery progress after partial success.
9. Delete the recovery record only after complete post-recovery verification.

For a repository forest, cleanup order should respect the actual dependency:
children before parents, repository worktrees before their grouping
directories, and worktree cleanup before deletion of an operation-created
branch. A grouping directory may be removed only if the operation created that
exact directory object and it remains safely removable.

### Idempotence and interruption

Running the same recovery again must be safe. An already completed action
should be recognized from current evidence rather than treated as a new
failure. If recovery is canceled or fails halfway through, the record should
describe only the remaining work and retain the original failure history
needed for diagnosis.

If another process or the user changes a path after planning, generation and
identity checks must stop before mutation. Recovery should never overwrite a
new occupant to restore an older assumption.

### Outcome classes

The command should finish with one of three clear outcomes:

- `recovered`: every retained effect was safely resolved and the recovery
  record was removed;
- `partially recovered`: verified actions completed, but explicit manual work
  remains and the updated recovery record is retained; or
- `blocked`: no safe mutation was performed because evidence is missing,
  ambiguous, stale, or contradictory.

The command must never describe a preserved unknown checkout as a successful
cleanup.

## Required ownership evidence

Automated cleanup needs stronger evidence than the current version-1 recovery
record contains. Depending on the operation, a future version or additive
companion record may need:

- the planned logical root and repository topology;
- the repository ID and resolved path for every affected step;
- whether a branch or grouping directory pre-existed the operation;
- a filesystem identity receipt for each created path;
- expected source/common Git identity;
- expected per-worktree Git administrative identity;
- expected branch or detached state and commit;
- any retained or quarantined path created during rollback;
- the recovery-record generation and per-action completion state; and
- the exact reason an action could not be reverted.

The schema should record expected facts from the validated plan separately
from identities observed after mutation. Observing a checkout after a hook or
wrapper returns is not, by itself, proof that it is the checkout the operation
intended to create.

Old records must remain readable. If they do not contain enough evidence for
automatic cleanup, `recover` should fall back to a blocked/manual result while
doctor still supplies useful inspection guidance.

## Safety invariants

- Unknown ownership always means preserve and report.
- A clean checkout is not necessarily an owned checkout.
- Path equality is not repository identity.
- Repository identity alone is not per-worktree identity.
- Branch and commit agreement alone do not prove ownership.
- Cleanup requires the conjunction of the applicable plan, filesystem, Git,
  branch, commit, and generation facts.
- Identity must be checked after public hooks or progress callbacks and again
  at the final cleanup boundary.
- A substituted checkout, real directory, symlink, or concurrent replacement
  is never deleted as transaction-owned data.
- Recovery metadata remains until post-recovery verification succeeds.
- Recovery actions use the project lock and preserve unrelated worktree and
  registry state.

## User-visible behavior

The original failing command should continue to return the distinct
`rollback_incomplete` error and the recovery-record location. It should also
point directly to the next diagnostic command, for example:

```text
Run `wtree doctor ...` to inspect the retained effects and print recovery commands.
```

Doctor should make the blocking state obvious in both human and JSON output.
The user should not have to discover the recovery record indirectly through a
later command failure.

When `wtree recover` exists, doctor may additionally print the exact dry-run
invocation. It should still show why an action is verified or blocked rather
than reducing the diagnosis to “run recover.”

## Acceptance outcomes

### Increment 1

- Doctor finds incomplete rollback evidence with or without workspace state.
- Human output includes ordered, correctly quoted inspection and recovery
  commands.
- JSON exposes equivalent structured actions.
- No doctor invocation mutates recovery evidence or user data merely by
  generating guidance.
- Unsafe or under-specified cleanup is labeled as a manual decision or
  blocked, never verified.
- The original failure points to doctor as the immediate next action.

### Increment 2

- Recovery planning is read-only and deterministic.
- Applying recovery is locked, generation-checked, identity-checked, and
  resumable.
- Verified transaction-owned leftovers are removed in safe dependency order.
- Unknown or replaced paths and worktrees are preserved.
- Partial progress is written atomically and is safe to resume.
- Successful recovery removes the recovery record last and permits the
  original operation to be retried.
- Human, JSON, progress, cancellation, and exit-code behavior are specified
  and tested at every mutation and rollback boundary.

## Open questions

- Should `wtree recover` require `--apply`, or should mutation be the default
  with `--dry-run` available like other mutating commands?
- What is the most usable selector when workspace state was never published:
  workspace name, storage ID, recovery-record path, or an explicit project and
  workspace pair?
- Should recovery evidence evolve the existing record version or use an
  additive operation journal referenced by the record?
- Which legacy records can safely support automated actions, and which must
  remain guidance-only?
- Should doctor print native commands for each supported shell or render a
  shell-neutral argument list plus platform-specific human text?
- Should a future recovery command support one verified action at a time for
  advanced/manual workflows?
- Which hook and Git-wrapper behaviors are inside the supported threat model,
  and which must be documented as hostile external mutation?

## Related work

- [Logical project roots with a designated base repository](logical-project-root-base-repository.md)
- [Logical project root and repository forest specification](../spec/logical-project-root-base-repository.md)
- [Logical project root and repository forest implementation plan](../plans/logical-project-root-base-repository.md)
- [`wtree` specification](../spec/wtree.spec.md)

