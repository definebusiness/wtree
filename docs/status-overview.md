# Documentation status overview

Last reviewed: 2026-08-20

This document summarizes lifecycle metadata for the idea, specification, and
implementation-plan documents under `docs/`. Operational documentation,
agent-process documentation, tutorials, troubleshooting guides, and run
ledgers do not use this lifecycle.

## Summary

| Status | Count |
|---|---:|
| `initial` | 5 |
| `specified` | 3 |
| `planned` | 0 |
| `implemented` | 10 |
| `superseded` | 2 |
| `abandoned` | 0 |
| **Total** | **20** |

## Ideas

| Document | Status | Resulting specification |
|---|---|---|
| [Allow workspaces when a branch is missing](ideas/allowing-missing-branches.md) | `initial` | None |
| [Automatically protect nested repository mounts](ideas/automatic-nested-mount-ignore-protection.md) | `specified` | [Automatic nested mount ignore protection specification](spec/automatic-nested-mount-ignore-protection.md); supersedes the prior [specification](spec/nested-mount-ignore-management.md) and [implementation plan](plans/nested-mount-ignore-management.md) |
| [Clone and synchronize a multi-repository project](ideas/cloning-a-multi-repository-project.md) | `specified` | [Portable manifest clone specification](spec/portable-manifest-clone.md) |
| [Final reviewer](ideas/workflow/final-reviewer.md) | `initial` | None |
| [Immutable release lock manifests](ideas/release-lock-manifests.md) | `initial` | None |
| [Logical project roots with a designated base repository](ideas/logical-project-root-base-repository.md) | `specified` | [Portable manifest v2 base-repository format specification](spec/portable-manifest-v2-base-repository-format.md) |
| [Minimal harness for processing the orchestration state machine](ideas/workflow/creating-a-minimal-harness-to-process-the-statemachine.md) | `initial` | None |

## Specifications

| Document | Status | Predecessor | Implementation plan |
|---|---|---|---|
| [`wtree` specification](spec/wtree.spec.md) | `implemented` | Created directly | [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md); [project registry implementation plan](plans/project-registry-management.md) |
| [`wtree` specification traceability](spec/wtree.traceability.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md) |
| [Nested mount ignore management specification](spec/nested-mount-ignore-management.md) | `superseded` | Created directly; superseded by the [automatic protection story](ideas/automatic-nested-mount-ignore-protection.md) | [Nested mount ignore management implementation plan](plans/nested-mount-ignore-management.md), also superseded |
| [Automatic nested mount ignore protection specification](spec/automatic-nested-mount-ignore-protection.md) | `implemented` | [Automatically protect nested repository mounts](ideas/automatic-nested-mount-ignore-protection.md) | [Automatic nested mount ignore protection implementation plan](plans/automatic-nested-mount-ignore-protection.md) |
| [Full multi-repository experience capability specification](spec/full-multi-repository-experience.md) | `initial` | Created directly from preserved source material | None |
| [Portable manifest clone specification](spec/portable-manifest-clone.md) | `implemented` | [Clone and synchronize idea](ideas/cloning-a-multi-repository-project.md) | [Portable manifest clone implementation plan](plans/portable-manifest-clone.md); current format defined by the [portable manifest v2 specification](spec/portable-manifest-v2-base-repository-format.md) |
| [Portable manifest v2 base-repository format specification](spec/portable-manifest-v2-base-repository-format.md) | `implemented` | [Logical project roots with a designated base repository](ideas/logical-project-root-base-repository.md) | [Portable manifest v2 base-repository format implementation plan](plans/portable-manifest-v2-base-repository-format.md) |

## Implementation plans

| Document | Status | Predecessor specification | Implementation evidence |
|---|---|---|---|
| [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | All milestones approved; [run ledger](ai/runs/wtree-implementation-plan.md) is complete; matching code and tests are present |
| [Project registry inspection and lifecycle implementation plan](plans/project-registry-management.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | All milestones approved; [run ledger](ai/runs/project-registry-management.md) is complete; project registry commands and tests are present |
| [Nested mount ignore management implementation plan](plans/nested-mount-ignore-management.md) | `superseded` | [Nested mount ignore management specification](spec/nested-mount-ignore-management.md) | Superseded by the [automatic protection story](ideas/automatic-nested-mount-ignore-protection.md); no milestones were authorized or completed |
| [Automatic nested mount ignore protection implementation plan](plans/automatic-nested-mount-ignore-protection.md) | `implemented` | [Automatic nested mount ignore protection specification](spec/automatic-nested-mount-ignore-protection.md) | All milestones M00–M05 are approved; exact Ubuntu/macOS/Windows run `32289428176` passed; the [durable run ledger](ai/runs/automatic-nested-mount-ignore-protection.md) is complete |
| [Portable manifest clone implementation plan](plans/portable-manifest-clone.md) | `implemented` | [Portable manifest clone specification](spec/portable-manifest-clone.md) | All milestones M00–M06 are approved; portable manifest clone is implemented and verified, with the current format supplied by the implemented v2 specification and plan. |
| [Portable manifest v2 base-repository format implementation plan](plans/portable-manifest-v2-base-repository-format.md) | `implemented` | [Portable manifest v2 base-repository format specification](spec/portable-manifest-v2-base-repository-format.md) | All milestones M00–M02 approved; [durable run ledger](ai/runs/portable-manifest-v2-base-repository-format.md) is complete; strict v2 config, init authoring, and clone verification are implemented and verified. |

## Maintenance

Update this overview in the same change whenever a lifecycle document is
created, receives a predecessor or successor, or changes status. Status
transitions and relationship rules are defined in [`AGENTS.md`](../AGENTS.md).
