# Documentation status overview

Last reviewed: 2026-08-17

This document summarizes lifecycle metadata for the idea, specification, and
implementation-plan documents under `docs/`. Operational documentation,
agent-process documentation, tutorials, troubleshooting guides, and run
ledgers do not use this lifecycle.

## Summary

| Status | Count |
|---|---:|
| `initial` | 4 |
| `specified` | 1 |
| `planned` | 1 |
| `implemented` | 6 |
| `superseded` | 0 |
| `abandoned` | 0 |
| **Total** | **12** |

## Ideas

| Document | Status | Resulting specification |
|---|---|---|
| [Allow workspaces when a branch is missing](ideas/allowing-missing-branches.md) | `initial` | None |
| [Clone and synchronize a multi-repository project](ideas/cloning-a-multi-repository-project.md) | `specified` | [Portable manifest clone specification](spec/portable-manifest-clone.md) |
| [Final reviewer](ideas/workflow/final-reviewer.md) | `initial` | None |
| [Immutable release lock manifests](ideas/release-lock-manifests.md) | `initial` | None |

## Specifications

| Document | Status | Predecessor | Implementation plan |
|---|---|---|---|
| [`wtree` specification](spec/wtree.spec.md) | `implemented` | Created directly | [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md); [project registry implementation plan](plans/project-registry-management.md) |
| [`wtree` specification traceability](spec/wtree.traceability.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md) |
| [Nested mount ignore management specification](spec/nested-mount-ignore-management.md) | `planned` | Created directly | [Nested mount ignore management implementation plan](plans/nested-mount-ignore-management.md) |
| [Portable manifest clone specification](spec/portable-manifest-clone.md) | `implemented` | [Clone and synchronize idea](ideas/cloning-a-multi-repository-project.md) | [Portable manifest clone implementation plan](plans/portable-manifest-clone.md) |

## Implementation plans

| Document | Status | Predecessor specification | Implementation evidence |
|---|---|---|---|
| [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | All milestones approved; [run ledger](ai/runs/wtree-implementation-plan.md) is complete; matching code and tests are present |
| [Project registry inspection and lifecycle implementation plan](plans/project-registry-management.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | All milestones approved; [run ledger](ai/runs/project-registry-management.md) is complete; project registry commands and tests are present |
| [Nested mount ignore management implementation plan](plans/nested-mount-ignore-management.md) | `initial` | [Nested mount ignore management specification](spec/nested-mount-ignore-management.md) | No milestones completed; `wtree add-ignore` and the associated flags are not implemented |
| [Portable manifest clone implementation plan](plans/portable-manifest-clone.md) | `implemented` | [Portable manifest clone specification](spec/portable-manifest-clone.md) | All milestones M00–M06 are approved; portable manifest clone is implemented and verified. |

## Maintenance

Update this overview in the same change whenever a lifecycle document is
created, receives a predecessor or successor, or changes status. Status
transitions and relationship rules are defined in [`AGENTS.md`](../AGENTS.md).
