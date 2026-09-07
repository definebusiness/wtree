# Companion repositories traceability

Status: implemented
Source idea: none (traceability companion created directly)
Source specification: [Companion repositories specification](companion-repositories.md)
Implementation plan: [Companion repositories implementation plan](../plans/companion-repositories.md)

This matrix records acceptance evidence for the companion-repository delivery.
Independent normal review approved the complete M05 tree after R1-R7 and
hosted H1-H6 remediation with no unresolved material finding. Exact
implementation SHA `f7254b6` passed complete Ubuntu, macOS, and native Windows
matrices in PR run `34076324241` and push run `34076322381`, so the source plan,
specification, and this traceability companion are implemented.

| §12 acceptance criterion | Production owner | Focused evidence | Public/tutorial evidence | Safety and platform evidence |
|---|---|---|---|---|
| 1. Strict v4 role and v3-hook compatibility | `internal/config`, `internal/domain`, clone/update publication | config version/canonicalization and companion config tests | [Companion tutorial](../../tutorial/COMPANIONS.md) documents explicit v4 adoption | v2/v3 strict rejection and canonical-byte regressions run normally and under race; no implicit ordinary-file upgrade |
| 2. Baseline selection for clone and create | `internal/plan`, `internal/service` clone/create planners | companion plan/create/clone tests, including `--from`, conflict, order, and rollback | runner clones v4 and proves ordinary `--from` versus companion `default_branch` | hermetic local bare origins only; transaction recovery is covered by full normal/race gates |
| 3. Valid local advancement and lifecycle | status, doctor, fetch, push-readiness, remove, checkout, delete services | companion status/lifecycle/exec/fetch/push/delete tests | runner commits locally, observes `headAdvanced`, selects the companion, removes, restores, and deletes it | no remote branch deletion; baseline retained; space/Unicode runner fixture plus existing nested-mount coverage exercise paths |
| 4. Atomic future baseline | `internal/service/repository_branch.go`, config atomic publication | `repository_branch` service and CLI tests | runner proves v1 dry-run/JSON then a real baseline change without moving the active checkout | no fetch, switch, merge, reset, staging, commit, push, or branch creation; split-publication/recovery tests are focused |
| 5. Exact exec scopes | exec service and CLI | CLI/service selection tests | runner proves default, ordinary-only, exact companion, and mutually-exclusive selector failure | empty/incompatible selector preflight precedes launch; ordinary JSON omits additive fields |
| 6. Best-effort companion update | companion-update service/CLI and configured-ref adapter | `companion_update` service and CLI contract tests, Git aggregate tests | runner proves dry-run envelope, baseline-before-clean-present-workspace ordered entry, exact previous/resulting heads, Git branch advancement, and persisted expected-head update | focused cases cover invalid state, stale authority, cancellation, rollback/recovery, no duplicate processing, no merge/force/push/global rollback, and redaction |
| 7. End-to-end docs, compatibility, hooks, update/recovery, locking | existing update, hooks, release-lock, rendering, documentation owners | public CLI JSON/human/error tests; release and hook suites; ordinary compatibility tests | [companion](../../tutorial/COMPANIONS.md), [all-command](../../tutorial/ALL-COMMANDS.md), [release](../../tutorial/RELEASES.md), README, and INSTALL; runner binds dry-run and real lock backend revision to the companion Git HEAD | local tutorial/release/normal/race/vet/format/build/whitespace gates passed; exact SHA `f7254b6` passed complete Ubuntu, macOS, and native Windows PR and push matrices |

## Result and error contract audit

Aggregate result envelopes remain v1. Workspace-plan, exec, fetch, and push
repository rows add `companion: true` only for companions; plan rows also add
`baseline`. Status remains unversioned and adds companion facts only when
applicable. `repo-branch` and `companion-update` use their documented v1
envelopes. Focused CLI structural tests assert exact field sets, one JSON
document on errors, and existing typed exit categories.

All fixtures use disposable local bare repositories, isolated data directories,
direct Git arguments, and no credential or global-config dependency. Fixture
setup may commit and push only to those local bare origins; no external/live
publication occurs. The dedicated
tutorial path includes spaces and Unicode. Existing cross-platform path/ref
and nested-forest tests remain the canonical focused Windows/topology evidence;
hosted verification must use the exact delivered tree and is not replaced by
this local matrix.
