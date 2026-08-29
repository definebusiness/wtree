# Documentation status overview

Last reviewed: 2026-08-29

This document summarizes lifecycle metadata for the idea, specification, and
implementation-plan documents under `docs/`. Operational documentation,
agent-process documentation, tutorials, troubleshooting guides, and run
ledgers do not use this lifecycle.

## Summary

| Status | Count |
|---|---:|
| `initial` | 8 |
| `specified` | 4 |
| `planned` | 4 |
| `implemented` | 15 |
| `superseded` | 2 |
| `abandoned` | 0 |
| **Total** | **33** |

## Ideas

| Document | Status | Resulting specification |
|---|---|---|
| [Actionable recovery from an incomplete rollback](ideas/actionable-incomplete-rollback-recovery.md) | `initial` | None |
| [Allow workspaces when a branch is missing](ideas/allowing-missing-branches.md) | `initial` | None |
| [Automatically protect nested repository mounts](ideas/automatic-nested-mount-ignore-protection.md) | `specified` | [Automatic nested mount ignore protection specification](spec/automatic-nested-mount-ignore-protection.md); supersedes the prior [specification](spec/nested-mount-ignore-management.md) and [implementation plan](plans/nested-mount-ignore-management.md) |
| [Clone and synchronize a multi-repository project](ideas/cloning-a-multi-repository-project.md) | `specified` | [Portable manifest clone specification](spec/portable-manifest-clone.md) |
| [Final reviewer](ideas/workflow/final-reviewer.md) | `initial` | None |
| [Immutable release lock manifests](ideas/release-lock-manifests.md) | `initial` | None |
| [Logical project roots with a designated base repository](ideas/logical-project-root-base-repository.md) | `specified` | [Portable manifest v2 base-repository format specification](spec/portable-manifest-v2-base-repository-format.md); [logical project root and repository forest specification](spec/logical-project-root-base-repository.md) |
| [Machine-local and shared workspace lifecycle hooks](ideas/local-workspace-lifecycle-hooks.md) | `initial` | None; complements the planned specification's portable `post-clone` contract |
| [Archon-based harness for deterministic milestone orchestration](ideas/workflow/creating-a-minimal-harness-to-process-the-statemachine.md) | `specified` | [Archon deterministic milestone harness specification](spec/archon-milestone-harness.md) |

## Specifications

| Document | Status | Predecessor | Implementation plan |
|---|---|---|---|
| [`wtree` specification](spec/wtree.spec.md) | `implemented` | Created directly | [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md); [project registry implementation plan](plans/project-registry-management.md) |
| [`wtree` specification traceability](spec/wtree.traceability.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md) |
| [Nested mount ignore management specification](spec/nested-mount-ignore-management.md) | `superseded` | Created directly; superseded by the [automatic protection story](ideas/automatic-nested-mount-ignore-protection.md) | [Nested mount ignore management implementation plan](plans/nested-mount-ignore-management.md), also superseded |
| [Automatic nested mount ignore protection specification](spec/automatic-nested-mount-ignore-protection.md) | `implemented` | [Automatically protect nested repository mounts](ideas/automatic-nested-mount-ignore-protection.md) | [Automatic nested mount ignore protection implementation plan](plans/automatic-nested-mount-ignore-protection.md) |
| [Archon deterministic milestone harness specification](spec/archon-milestone-harness.md) | `planned` | [Archon-based harness for deterministic milestone orchestration](ideas/workflow/creating-a-minimal-harness-to-process-the-statemachine.md) | [Archon deterministic milestone harness implementation plan](plans/archon-milestone-harness.md) |
| [Full multi-repository experience capability specification](spec/full-multi-repository-experience.md) | `planned` | Created directly from preserved source material | [Multi-repository composition loop and aggregate operations implementation plan](plans/full-multi-repository-experience.md); first split delivery covers P0/P1, while deferred P3 limits directly executable portable hooks to explicitly authorized `post-clone` and delegates local/shared hooks to the focused idea |
| [Full multi-repository experience P0/P1 traceability](spec/full-multi-repository-experience.traceability.md) | `planned` | Created directly as a companion to the [full multi-repository experience capability specification](spec/full-multi-repository-experience.md) | [Multi-repository composition loop and aggregate operations implementation plan](plans/full-multi-repository-experience.md); maps delivered M00–M09 P0/P1 contracts to focused, integrated, documentation, and CI evidence while retaining exact gates for P2/P3/P4 |
| [Live-branch clone and upstream-aware human status specification](spec/clone-live-branch-and-upstream-status.md) | `implemented` | Created directly | [Live-branch clone and upstream-aware human status implementation plan](plans/clone-live-branch-and-upstream-status.md) |
| [Logical project root and repository forest specification](spec/logical-project-root-base-repository.md) | `implemented` | [Logical project roots with a designated base repository](ideas/logical-project-root-base-repository.md) | [Logical project root and repository forest implementation plan](plans/logical-project-root-base-repository.md) |
| [Portable manifest clone specification](spec/portable-manifest-clone.md) | `implemented` | [Clone and synchronize idea](ideas/cloning-a-multi-repository-project.md) | [Portable manifest clone implementation plan](plans/portable-manifest-clone.md); current format defined by the [portable manifest v2 specification](spec/portable-manifest-v2-base-repository-format.md) |
| [Portable manifest v2 base-repository format specification](spec/portable-manifest-v2-base-repository-format.md) | `implemented` | [Logical project roots with a designated base repository](ideas/logical-project-root-base-repository.md) | [Portable manifest v2 base-repository format implementation plan](plans/portable-manifest-v2-base-repository-format.md) |
| [Windows portability and CI hardening specification](spec/windows-portability-and-ci-hardening.md) | `planned` | Created directly; follows the implemented automatic nested mount ignore work | [Windows portability and CI hardening implementation plan](plans/windows-portability-and-ci-hardening.md); [Windows portability simplification and CI remediation implementation plan](plans/windows-portability-simplification-and-ci-remediation.md) |

The logical-project-root specification and its implementation plan are now
implemented. All milestones M00–M08 are independently approved and verified,
covering plain logical roots, local project configuration v2, grouping and
worktree ownership, forest-aware commands, additive topology context, public
documentation, and release gates. The source idea remains `specified`, its
lifecycle state after producing the specification.

## Implementation plans

| Document | Status | Predecessor specification | Implementation evidence |
|---|---|---|---|
| [`wtree` incremental implementation plan](plans/wtree-implementation-plan.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | All milestones approved; [run ledger](ai/runs/wtree-implementation-plan.md) is complete; matching code and tests are present |
| [Project registry inspection and lifecycle implementation plan](plans/project-registry-management.md) | `implemented` | [`wtree` specification](spec/wtree.spec.md) | All milestones approved; [run ledger](ai/runs/project-registry-management.md) is complete; project registry commands and tests are present |
| [Nested mount ignore management implementation plan](plans/nested-mount-ignore-management.md) | `superseded` | [Nested mount ignore management specification](spec/nested-mount-ignore-management.md) | Superseded by the [automatic protection story](ideas/automatic-nested-mount-ignore-protection.md); no milestones were authorized or completed |
| [Automatic nested mount ignore protection implementation plan](plans/automatic-nested-mount-ignore-protection.md) | `implemented` | [Automatic nested mount ignore protection specification](spec/automatic-nested-mount-ignore-protection.md) | All milestones M00–M05 are approved; exact Ubuntu/macOS/Windows run `32289428176` passed; the [durable run ledger](ai/runs/automatic-nested-mount-ignore-protection.md) is complete |
| [Archon deterministic milestone harness implementation plan](plans/archon-milestone-harness.md) | `initial` | [Archon deterministic milestone harness specification](spec/archon-milestone-harness.md) | Not started; milestones M00–M05 are unchecked |
| [Multi-repository composition loop and aggregate operations implementation plan](plans/full-multi-repository-experience.md) | `initial` | [Full multi-repository experience capability specification](spec/full-multi-repository-experience.md) | Authorized [durable run](ai/runs/full-multi-repository-experience.md) has M00–M09 approved and M10 locally implemented, independently approved, and verified with P0/P1 traceability, hermetic composition acceptance, and the extended executable all-command tutorial covering update, doctor/status, direct exec, configured fetch/status refresh, and non-publishing push readiness. M10 and the plan remain unchecked because the required matching Ubuntu/macOS/Windows Actions run cannot represent the uncommitted tree without separately authorized commit/push/PR activity; the workflow has no manual trigger. The [durable ledger](ai/runs/full-multi-repository-experience.md) records the exact external blocker and continuation condition. The broader source specification remains `planned` for deferred P2/P3/P4 capabilities. |
| [Logical project root and repository forest implementation plan](plans/logical-project-root-base-repository.md) | `implemented` | [Logical project root and repository forest specification](spec/logical-project-root-base-repository.md) | All milestones M00–M08 are independently approved and verified; the [durable run ledger](ai/runs/logical-project-root-base-repository.md) is complete and focused [implementation context](plans/logical-project-root-base-repository-context.md) is available |
| [Live-branch clone and upstream-aware human status implementation plan](plans/clone-live-branch-and-upstream-status.md) | `implemented` | [Live-branch clone and upstream-aware human status specification](spec/clone-live-branch-and-upstream-status.md) | All milestones M00–M02 approved; [durable run ledger](ai/runs/clone-live-branch-and-upstream-status.md) is complete; live selected-branch clone, v2 observed/actual output, and upstream-aware human status are implemented and verified |
| [Portable manifest clone implementation plan](plans/portable-manifest-clone.md) | `implemented` | [Portable manifest clone specification](spec/portable-manifest-clone.md) | All milestones M00–M06 are approved; portable manifest clone is implemented and verified, with the current format supplied by the implemented v2 specification and plan. |
| [Portable manifest v2 base-repository format implementation plan](plans/portable-manifest-v2-base-repository-format.md) | `implemented` | [Portable manifest v2 base-repository format specification](spec/portable-manifest-v2-base-repository-format.md) | All milestones M00–M02 approved; [durable run ledger](ai/runs/portable-manifest-v2-base-repository-format.md) is complete; strict v2 config, init authoring, and clone verification are implemented and verified. |
| [Windows portability and CI hardening implementation plan](plans/windows-portability-and-ci-hardening.md) | `implemented` | [Windows portability and CI hardening specification](spec/windows-portability-and-ci-hardening.md) | All milestones M00–M03 are independently approved and verified. Exact implementation run [`33229459008`](https://github.com/definebusiness/wtree/actions/runs/33229459008) passed every format, vet, normal, race, build, release-layout, reuse, and manifest gate on Ubuntu (7m24s), macOS (25m40s), and Windows (1h13m43s) at `b0f62fe`. Windows completed all eight exact-once normal and race service shards in 35m56s and 35m24s. The [durable run](ai/runs/windows-portability-and-ci-hardening.md) records the complete remediation and acceptance evidence with no unresolved finding. The source specification remains `planned` because the focused [simplification and CI remediation plan](plans/windows-portability-simplification-and-ci-remediation.md) remains an initial supporting plan, not a separately active run. |
| [Windows portability simplification and CI remediation implementation plan](plans/windows-portability-simplification-and-ci-remediation.md) | `initial` | [Windows portability and CI hardening specification](spec/windows-portability-and-ci-hardening.md) | Not started; M00–M05 are unchecked. The plan is based on the exact-tree failed run [`33168555356`](https://github.com/definebusiness/wtree/actions/runs/33168555356) and the companion [hosted failure and simplification context](plans/windows-portability-simplification-and-ci-remediation-context.md). It removes the non-portable `/dev/stdin` input and pre-created Git destination design, narrows retained-handle use, stabilizes Git inventory, repairs native contracts, and makes CI complexity evidence-driven. |

## Maintenance

Update this overview in the same change whenever a lifecycle document is
created, receives a predecessor or successor, or changes status. Status
transitions and relationship rules are defined in [`AGENTS.md`](../AGENTS.md).
