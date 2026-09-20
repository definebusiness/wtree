# Bug report: stale worktree registration produces an unexplained checkout conflict

Status: initial

Reported: 2026-09-20  
Severity: blocks checkout until manual Git metadata cleanup  
Related incident: [Stale default state and orphaned removal recovery](bug-stale-default-state-and-removal-recovery.md)

## Summary

Checking out `feature/model-update` failed because the nested `ai` repository
retained a Git worktree registration for a missing directory. Git explicitly
identified the registration as prunable, but `wtree` discarded that evidence
and reported only that the branch was already checked out. The user could not
identify the conflicting path or distinguish an active checkout from stale
metadata from the error.

Rejecting a second checkout while Git still reserves the branch is correct.
The confirmed defect is the loss of diagnostic information and actionable
recovery guidance. Simply ignoring the registration is not a valid fix.

## Environment and provenance

- macOS, arm64; Git `2.50.1 (Apple Git-155)`.
- Installed executable: `/Users/marcel/go/bin/wtree`, reporting `wtree 0.5.0`.
- Its Go module build metadata reports
  `v0.0.0-20260907105227-d2ce8d048f55+dirty`; the exact dirty source is unknown.
- Source inspected for this report: commit
  `a0707a4c5e772f5b2c1655093538bcc1d48fc53b`. Do not assume it is byte-identical
  to the installed executable; reproduce against a fresh build as well.
- Project: `specs-crm`, ID `dd84fc7e-9e74-5e51-91d9-ce7cb57bee6c`.
- Repository forest: `root` at `/Users/marcel/Projects/specs-crm`, `platform`
  mounted at `platform`, and companion repository `ai` mounted at `tools/ai`.
- Local configuration version: 4. Root/platform default branch: `dev`;
  companion default branch: `main`.
- The exact original command and flags were not captured. The observed error
  comes from checkout planning.

## Captured evidence

```text
wtree: conflict: branch "feature/model-update" is already checked out for repository "ai"
```

`git -C /Users/marcel/Projects/specs-crm/tools/ai worktree list --porcelain`:

```text
worktree /Users/marcel/Projects/specs-crm/tools/ai
HEAD 279baa2702f9e17904804d3143630eae55001d94
branch refs/heads/main

worktree /Volumes/My Shared Files/Projects/tools/wtree-tutorial/global-worktrees/dd84fc7e-9e74-5e51-91d9-ce7cb57bee6c/feature-model-update-aa9e756679860f855044b28efea662f3/tools/ai
HEAD 279baa2702f9e17904804d3143630eae55001d94
branch refs/heads/feature/model-update
prunable gitdir file points to non-existent location
```

`git worktree prune --dry-run --verbose` in that repository returned:

```text
Removing worktrees/ai: gitdir file points to non-existent location
```

The source checkout was clean and on `main`. Root and platform had only their
source worktrees registered. The old path contains spaces and is on a shared
volume; a missing mount must not automatically be treated as deleted work.

## Deterministic reproduction

Use disposable repositories and isolated `wtree` data; never remove a real
user worktree to construct this test.

1. Extend the existing planner fixture to include a nested `ai` repository
   (also exercise `Companion: true`) and a sibling `platform` repository.
2. Create `feature/model-update` in every repository. Keep source checkouts on
   their normal branches.
3. In `ai`, run `git worktree add <temp>/old workspace/tools/ai
   feature/model-update`.
4. Delete only that disposable worktree directory using the test filesystem,
   leaving its Git registration intact. Do not use `git worktree remove`.
5. Assert that raw `git worktree list --porcelain` contains the branch, the
   missing path, and `prunable` before invoking the planner.
6. Plan checkout into a different, nonexistent target directory; reproduce
   through `wtree checkout feature/model-update --dry-run --json` with an
   equivalent initialized project fixture.
7. Observe the generic conflict with no path or stale-registration reason.
8. Explicitly prune the disposable stale registration and retry. Checkout
   planning should succeed if no independent problem exists.

## Expected behavior and fix boundaries

- Identify the repository, branch, registered path, and Git's prunable reason
  in human diagnostics and structured output.
- Distinguish a live checkout, missing/prunable registration, and locked or
  otherwise uncertain worktree. Explain that an unavailable volume may need
  reconnection rather than pruning.
- Offer a correctly quoted, repository-scoped inspection command and explicit
  cleanup guidance when supported by the evidence.
- Keep planning and dry runs read-only. Preserve branches, commits, files,
  unrelated registrations, and locked worktrees.
- Do not silently prune or bypass Git's branch reservation. If automatic repair
  is added later, it requires explicit repair semantics, fresh ownership
  checks, and protection against concurrent changes.

## Implementation entry points

- [`internal/git/parse.go`](../../internal/git/parse.go): `Worktree` has no
  prunable/locked fields; `ParseWorktreeList` silently drops both record types.
- [`internal/git/adapter.go`](../../internal/git/adapter.go): `ListWorktrees`
  and `BranchCheckedOut`; the latter returns only a boolean on a branch match.
- [`internal/service/plan.go`](../../internal/service/plan.go): checkout
  preflight constructs the generic conflict from that boolean.
- [`internal/service/plan_test.go`](../../internal/service/plan_test.go):
  extend `TestWorkspacePlannerCheckoutRejectsBranchCheckedOutElsewhere` with
  separate live and stale cases; retain rejection of live conflicts.
- Add parser/adapter coverage alongside existing tests in `internal/git` and
  CLI error rendering coverage in `internal/cli`.

## Regression acceptance criteria

1. Parse and retain `prunable` and `locked`, both with and without reasons;
   existing detached/bare records still parse correctly.
2. A live conflicting checkout is rejected and its location is reported.
3. A stale conflicting registration is rejected with its path, stale reason,
   and usable recovery guidance in human and JSON output.
4. A locked or unavailable-volume case never causes implicit cleanup. Spaces
   in paths survive parsing and command rendering.
5. Dry run leaves repository refs, Git registrations, workspace state, and
   recovery records byte-for-byte unchanged.
6. After explicit verified pruning, checkout succeeds; unrelated worktrees
   and branch tips remain unchanged.

## Incident resolution and remaining uncertainty

A later read showed that the stale `ai` registration had disappeared. The
cleanup command that removed it was not captured by the agent. After the
separate metadata repair described in the related report, checkout dry run
succeeded for all three repositories. Actual checkout was not executed.

The original cause of the abandoned registration is unknown. Its location
matches the workspace named in the removal recovery record, but that alone
does not prove the failed removal created it. This report is open; local
metadata cleanup did not fix the product defect.
