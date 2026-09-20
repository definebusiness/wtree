# Bug report: stale default HEADs and orphaned removal recovery have no actionable repair

Status: initial

Reported: 2026-09-20  
Severity: misleading health diagnostics and manual metadata repair required  
Related incident: [Stale worktree checkout conflict](bug-stale-worktree-checkout-conflict.md)  
Related proposal: [Actionable incomplete rollback recovery](actionable-incomplete-rollback-recovery.md)

## Summary

`wtree status` combined ordinary advances of the default checkout's branches
with a leftover failed-removal record into five drift rows. It labeled the
removal record `update-recovery-record`, even though the record explicitly
said `operation: remove`. `doctor --fix --dry-run` offered no repairs, and
doctor could not inspect the affected workspace by name because its workspace
state was already absent.

This report groups the second user-visible incident into two independently
testable parts: default HEAD reconciliation and orphaned recovery diagnosis.
The wrong operation label is a confirmed defect. Strict HEAD comparison is
existing behavior; the intended replacement policy must preserve immutable
workspace/release semantics and explicit repair authorization.

## Environment and project

The [related report](bug-stale-worktree-checkout-conflict.md#environment-and-provenance)
records executable/source provenance, OS, Git version, and topology. The
installed executable was `wtree 0.5.0`; inspected source was
`a0707a4c5e772f5b2c1655093538bcc1d48fc53b`, not a proven exact binary match.

Project ID: `dd84fc7e-9e74-5e51-91d9-ce7cb57bee6c`. Let `DATA` denote
`/Users/marcel/Library/Application Support/wtree` in the paths below.

## Captured symptoms

```text
REPOSITORY  ORIGIN     CHECK                   STATUS
root        checkout   head                    mismatch
platform    checkout   head                    mismatch
            authority  default-state           inconsistent
            authority  unresolved-operation    inconsistent
            operation  update-recovery-record  incomplete-operation
```

The repository branches and identities matched the persisted state:

| Repository | Branch | Persisted HEAD | Actual HEAD |
|---|---|---|---|
| root | dev | `0b2bd2455dacd032739b5d2b559a7f21e554e6ce` | `cefec3eb355c622c2a2aea0244f44c566e839003` |
| platform | dev | `3be4c3a418cfbf3db7a17141ef45d4e943ebd524` | `e137dd0294f4d4576ee153a9ab960b48b76da1f0` |
| ai | main | `279baa2702f9e17904804d3143630eae55001d94` | Same |

During repair, `git merge-base --is-ancestor <persisted-head> <actual-head>`
succeeded for each repository: the changed tips were descendants, not branch
switches. Initial status also reported untracked content in root/platform;
later status reported them clean. The metadata repair did not modify working
files, and the intervening cause of this cleanliness change was not captured.

Persisted default state was at `DATA/state/<project-id>/default.json`, version
1, with `id` and `name` both `default`, `path` pointing at the source checkout,
and repository entries containing `branch`, `mount`, `resolvedPath`, and
`head`. The only workspace state file for this project was `default.json`.

Recovery file:
`DATA/projects/<project-id>/recovery/feature-model-update-aa9e756679860f855044b28efea662f3.json`.
Its complete content was:

```json
{
  "version": 1,
  "projectId": "dd84fc7e-9e74-5e51-91d9-ce7cb57bee6c",
  "workspaceId": "feature-model-update-aa9e756679860f855044b28efea662f3",
  "operation": "remove",
  "failedStep": "remove_worktree:platform",
  "completedSteps": null,
  "unrevertedSteps": ["remove_worktree:platform"],
  "rollbackFailures": [{
    "step": "remove_worktree:platform",
    "error": "conflict: refuse to replace changed worktree path for repository \"platform\""
  }]
}
```

Additional observations:

- `wtree -p /Users/marcel/Projects/specs-crm doctor --fix --dry-run --json`
  returned findings but no proposed repairs.
- `doctor feature/model-update --json` returned exit 4, `workspace_not_found`.
- Root/platform had no linked worktrees. By repair time, neither did `ai`.
- The old workspace directory under `/Volumes/My Shared Files/Projects/tools/
  wtree-tutorial/global-worktrees/<project-id>/<workspace-id>` did not exist.
- Doctor also reported `parent-ignore-missing` for `ai`. This is separate
  from the five displayed rows; its resolution was not verified and must not
  be attributed to this metadata repair.

## Deterministic reproductions

Use isolated data directories and real disposable Git fixtures. No shared
volume or original commits are required.

### A. Default branch advances

1. Initialize a project with root and nested platform on `dev`, plus optional
   companion `ai` on `main`. Persist the default workspace at commits A/B/C.
2. Commit ordinary changes in root and platform, producing descendants A2/B2,
   without changing branches, mounts, repository identities, or configuration.
   Keep the working trees clean for the minimal test.
3. Run status JSON and human output, then doctor with fix dry run.
4. Assert current behavior: root/platform HEAD mismatches, project-level
   default-state inconsistency, and no repair for those differences.
5. Repeat with dirty/untracked content to ensure any reconciliation changes
   only metadata and never resets, stashes, stages, or commits user files.

### B. Orphaned removal recovery

1. Start with a valid default workspace whose saved HEADs match live Git.
2. Seed the recovery JSON above, substituting fixture IDs. Leave the referenced
   feature workspace state absent, its target path absent, and all feature
   worktree registrations absent. Preserve feature branch refs if present.
3. Run status, project inventory, default doctor, fix dry run, and doctor for
   the missing feature workspace.
4. Observe `unresolved-operation`, the misleading `update-recovery-record`,
   absence of a repair proposal, and workspace-not-found for targeted doctor.
5. Combine with fixture A to reproduce the user's complete five-row table.

This seeded fixture reproduces the confirmed diagnostic/recovery failure,
not the unknown original removal failure. Separately use the remover's
failure-injection fixtures to replace a worktree path between execution and
rollback and verify that ownership refusal preserves the replacement and
records the failed step. Do not weaken that refusal to make the test pass.

## Code-level findings and fix entry points

- [`internal/service/drift_snapshot.go`](../../internal/service/drift_snapshot.go):
  `correlateDefaultDriftState` treats any saved/live branch or HEAD difference
  as a default-state failure. Snapshot collection emits an unresolved-operation
  failure for each durable operation record.
- [`internal/service/status.go`](../../internal/service/status.go):
  `applyLocalStatusDrift` labels every operation outside an `/update/` path
  `update-recovery-record`, ignoring its `Operation` value.
- [`internal/service/doctor_drift.go`](../../internal/service/doctor_drift.go):
  `doctorOperationFindings` repeats the same path-based classification, with
  only generic manual-action guidance.
- [`internal/service/doctor.go`](../../internal/service/doctor.go): `Fix`
  returns without mutation when no repairs are planned. Existing companion
  descendant handling provides a useful comparison, not blanket permission
  to relax HEAD checks for every workspace.
- [`internal/service/remove.go`](../../internal/service/remove.go):
  `restoreRemovedWorktree` produces the recorded ownership-refusal message.
  This is a rollback failure; the record does not establish the original
  execution error or prove that a real path replacement occurred.

## Expected behavior and regression acceptance

1. Human and JSON diagnostics identify the actual operation (`remove`),
   workspace ID, record path, failed step, and rollback failure. Project-level
   and operation-level rows explain their relationship rather than implying
   separate unexplained failures. Define any JSON compatibility migration.
2. Recovery inspection works from the durable record when workspace state is
   missing. A missing state file alone never authorizes deleting evidence.
3. Provide an explicit, previewable reconciliation path for verified default
   branch advances, or clearly document an alternative policy. Read-only
   status must not rewrite state. Preserve strict checks for branch switches,
   detached HEAD, divergent history, changed identity/mount, release pins, and
   non-default workspace contracts.
4. Any recovery archival requires fresh evidence that no affected checkout,
   registration, unresolved destructive action, or uncertain ownership remains.
   An unavailable volume, replacement directory, locked registration, malformed
   record, or missing authority produces concrete inspection guidance instead.
5. Repair uses repository-native project locking and compare-and-swap state
   publication, preserves an audit/backup, and revalidates immediately before
   mutation. Cover concurrent changes, failure between repair steps, retry,
   and idempotence. No repair may discard branches, commits, or working files.
6. Fix dry run describes each proposed change and writes nothing. After a
   successful authorized fixture repair, the five rows disappear and checkout
   dry run succeeds. Independent findings remain visible.

Existing test anchors:

- `internal/service/status_drift_internal_test.go`, especially
  `TestApplyLocalStatusDriftProjectsProjectAuthoritiesAndOperationsOnce`,
  currently expects a `remove` record to be called `update-recovery-record`.
- `internal/service/doctor_drift_test.go` for operation classification and
  retained recovery evidence.
- `internal/service/doctor_test.go`:
  `TestDoctorFixPreservesStateWithRecoveryRecord` and
  `TestDoctorReportsBranchAndHeadDriftWithoutFix` encode existing safety rules.
- `internal/service/status_test.go`:
  `TestCompanionAdvancedHeadIsInformationalAcrossStatusDoctorFetchRemoveAndCheckout`
  and `TestStatusWithDataDirTrackedManifestFallbackStillReportsRecovery`.
- Add isolated end-to-end CLI coverage for both output formats and recovery
  inspection without workspace state; keep genuine incomplete rollback tests.

## Incident workaround and verification

With write approval, a one-off script backed up default state and the recovery
record to `DATA/repair-backups/20260920-010950`. It checked branch attachment,
ancestor relationships, absence of feature registrations and workspace state,
and absence of the old target directory; then refreshed saved HEADs and removed
the active recovery file while keeping its backup. It did not alter Git refs
or working files. This manual workaround is evidence, not a production repair
algorithm or permission to clear arbitrary recovery records.

Afterward, `wtree status --json` showed matching saved/live HEADs with no drift
array, and `wtree checkout feature/model-update --dry-run --json` successfully
planned all three repositories. Actual checkout was not executed. The product
issues remain open; no source fix or regression tests were implemented during
the incident. The original failed removal command, execution error, and event
that removed feature workspace state remain unknown.
