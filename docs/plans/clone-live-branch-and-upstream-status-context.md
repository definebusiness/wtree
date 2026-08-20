# Implementation context — live-branch clone and upstream-aware human status

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Live-branch clone and upstream-aware human status implementation plan](clone-live-branch-and-upstream-status.md)
Source specification: [Live-branch clone and upstream-aware human status specification](../spec/clone-live-branch-and-upstream-status.md)
Captured: 2026-08-20

## 1. Purpose

This document preserves implementation-relevant evidence and decisions from
the investigation and product discussion that are intentionally not repeated
in the normative specification or milestone plan. The specification remains
the behavior contract, and the plan remains the execution contract. If this
context conflicts with either, the specification and plan win.

## 2. Explicit product conclusions from the discussion

The user established these conclusions before requesting the plan:

- Ordinary clone is expected to obtain the current tip of the branch selected
  by the portable manifest. It should not freeze a commit merely because that
  commit was observed during preflight.
- Independent repositories cannot merge coordinated changes into all `main`
  branches atomically. Exact preflight capture can therefore freeze a mixed
  repository state just as easily as a compatible one; it does not prove
  cross-repository compatibility.
- Temporary incompatibility among moving default branches is accepted as an
  ordinary repository-coordination problem. Users may fetch and fast-forward
  again after maintainers finish publishing coordinated changes.
- Known-compatible, reproducible commit sets belong to an explicit release
  lock or lockfile workflow, not ordinary branch clone.
- A common unrestricted pull operation was rejected as too dangerous because
  it can merge, rebase, conflict, or leave a partially updated project.
  Future aggregate branch movement should be fast-forward-only, with status
  identifying repositories that require manual work.
- `wtree status` must not fetch implicitly. A future explicit `wtree fetch`
  refreshes remote-tracking refs; status then reports the locally known
  upstream relationship.
- Manifest reconciliation through future `wtree update` is broader than this
  plan. Repositories removed from a manifest should be retained as unmanaged
  rather than deleted automatically.

The last three points explain adjacent product direction but do not authorize
fetch, update, sync, pull, lock, or manifest-reconciliation implementation in
this plan.

## 3. Exact clone failure mechanism

The current adapter sequence is:

1. `Clone` runs `git clone --no-checkout --origin <remote> ...` in
   `internal/git/portable.go`.
2. The executor fetches the preflight commit by object ID.
3. `CheckoutTrackingBranch` fetches the selected remote branch into its
   remote-tracking ref.
4. It runs `git branch -f <local-branch> <preflight-commit>`.
5. It configures `branch.<local>.remote` and `branch.<local>.merge`.
6. It checks out the local branch with hooks suppressed.

The faulty assumption is that `git clone --no-checkout` selects no branch.
It avoids populating worktree files, but when remote `HEAD` points to `main`,
Git still creates local `main` and attaches the clone's `HEAD` to it.

The later force command then fails even when the requested commit is already
the value of local `main`. Git rejects the operation because the branch is in
use, not because its object ID would change.

A minimal investigation produced these facts:

```text
remote HEAD:    refs/heads/main
clone HEAD:     refs/heads/main
working files:  0
branch exit:    128
branch error:   fatal: cannot force update the branch 'main' used by worktree at '<temporary clone>'
```

Equivalent reproducer:

```sh
git --git-dir=origin.git symbolic-ref HEAD refs/heads/main
git clone --no-checkout --origin origin -- origin.git clone
commit=$(git --git-dir=origin.git rev-parse refs/heads/main)
git -C clone branch -f main "$commit"
```

This reproducer is diagnostic only. Implementation tests must create isolated
temporary repositories and must not depend on global Git configuration.

## 4. Why the existing tutorial and tests miss the defect

The tutorial initially creates ordinary bare origins with symbolic `HEAD` at
`refs/heads/main`. Near the end of `tutorial/setup-fixture.sh`, it then creates
`fixture/clone-bootstrap` at the same commit and rewrites every bare origin's
symbolic `HEAD` to that branch.

That changes the failing sequence into:

```text
branch attached by clone: fixture/clone-bootstrap
branch force-created by wtree: main
```

Because the names differ, `git branch -f main ...` succeeds. The tutorial and
its expected final branch lists also treat `fixture/clone-bootstrap` as an
intended local branch, so the workaround became part of the passing E2E rather
than exposing the normal case.

The focused adapter tests also avoid the collision:

- `pushedRepository` commonly pushes local `main` to a differently named
  remote branch such as `published`;
- bare test remotes do not consistently set symbolic `HEAD` to the selected
  branch; and
- clone execution fixtures commonly use different local and remote branch
  names such as `local-main` and `published-main`.

The regression must therefore assert the complete equality case explicitly:

```text
remote symbolic HEAD = refs/heads/main
manifest remote ref  = refs/heads/main
manifest local branch = main
```

It must also assert the inverse case: a distinct remote `HEAD` does not leave
an additional local branch.

## 5. Clone implementation map

The primary current boundaries are:

| Area | Current responsibility | Relevant risk during change |
|---|---|---|
| `internal/git/portable.go` | Remote advertisement, clone, exact-object fetch, tracking-branch construction | Hostile Git configuration and templates, hook execution, ref grammar, remote `HEAD` leakage, cancellation |
| `internal/service/clone_plan.go` | Remote observation, parent-first repository plan, exact-commit actions, destination and registry facts | Public plan v1 semantics, deterministic JSON, aggregate remote failures, tamper validation |
| `internal/service/clone_result.go` | Stable dry-run result v1 and defensive copies | Version transition, field meaning, failed-result provenance, secret redaction |
| `internal/service/clone_execute.go` | Staging, exact fetch/checkout, verification, publication, state/registry writes, cleanup and recovery | Replacing every planned-commit verification with actual-HEAD evidence without weakening rollback ownership |
| `internal/cli/clone.go` | Human dry-run/success, completed JSON, help, progress and error rendering | Existing exact-commit claims and version-one completed envelope |
| `tutorial/setup-fixture.sh` and tutorial expectations | Public E2E fixture and documented branch behavior | Removing the workaround exposes stale expected branch lists and prose |

Important executor dependencies on the planned commit are not confined to the
checkout command. The current value feeds parent ignore inspection,
repository verification, final identity checks, cleanup ownership, action
rendering, and workspace state expectations. Implementation must inventory all
uses of `AdvertisedCommit`, `ExactCommit`, and `ParentCommit`; changing only
`CheckoutTrackingBranch` would leave contradictory verification behavior.

The base repository has an additional race-sensitive rule: its checked-out
`project.wtree.yml` must be byte-identical to the manifest source loaded at the
start. A live branch advance that changes other files is acceptable. A live
branch advance that changes the tracked manifest causes safe clone failure;
the implementation must not silently switch to that newer manifest during the
same operation.

## 6. Output-contract context

Current output models encode the old guarantee rather than merely displaying
an internal field:

- `ClonePlanVersion` is `1`.
- `CloneResultVersion` is `1`.
- repository plans/results expose `advertisedCommit`.
- actions expose `exactCommit` and `parentCommit`.
- human dry-run labels the value `exact commit`.
- clone command help says every branch is resolved to an exact commit.
- completed clone JSON embeds the pre-execution plan but does not expose an
  explicit actual commit per repository.

The version-two output decision in the parent plan was not an independently
requested feature. It is a compatibility consequence of removing the old
execution guarantee: silently keeping version one while changing
`exactCommit` from an execution promise into an observation would make
automation misinterpret output. The plan therefore chooses an explicit
version boundary and observed-versus-actual terminology.

No persisted plan is being migrated. Portable manifest v2, local config v1,
workspace state v1, registry, and recovery formats are separate contracts and
remain unchanged.

## 7. Status implementation evidence

`StatusService.repositoryStatus` currently does all of the following for an
attached checkout:

1. reads branch and `HEAD`;
2. reads working-tree dirtiness;
3. calls `AheadBehind`;
4. stores `Ahead`, `Behind`, and `Upstream`; and
5. calls `summarizedStatus`.

`AheadBehind` is entirely local. It verifies `@{upstream}` and runs the
equivalent of:

```sh
git rev-list --left-right --count HEAD...@{upstream}
```

It does not fetch or contact the configured remote. The values describe the
last locally fetched remote-tracking ref.

The information loss occurs only in presentation:

- `summarizedStatus` considers structural drift and working-tree cleanliness
  but not `Ahead`, `Behind`, or `Upstream`;
- human rendering prints only `REPOSITORY`, `BRANCH`, `MOUNT`, and `STATUS`;
  and
- JSON already contains the upstream facts, although zero-valued fields use
  `omitempty` and may be absent from encoded output.

Consequently a repository may encode facts equivalent to:

```json
{
  "clean": true,
  "behind": 1,
  "upstream": true,
  "status": "clean"
}
```

while human output shows only `clean`. The chosen fix adds a separate human
`UPSTREAM` column instead of changing the established meaning of the service
and JSON `status` field.

## 8. Rejected or deferred alternatives

- **Keep exact preflight pinning and only repair `main`/`main`.** A Git command
  sequence could avoid the checked-out-branch error while preserving the old
  pin, but that would retain the product behavior explicitly rejected in the
  discussion.
- **Skip `branch -f` only when the branch already points to the planned
  commit.** This fixes only one symptom and still treats preflight observation
  as an ordinary-clone lock.
- **Let remote `HEAD` choose the checkout.** This contradicts portable
  manifest branch authority and fails when the selected branch intentionally
  differs from remote `HEAD`.
- **Make status fetch automatically.** Fetch mutates remote-tracking refs,
  requires network and credentials, can be slow or fail, and would make an
  observational command unexpectedly stateful.
- **Change JSON `status` to `behind`, `ahead`, or `diverged`.** That conflates
  working-tree/structural state with upstream relationship and breaks an
  already useful machine-readable distinction.
- **Implement aggregate fetch or update in the same plan.** Those capabilities
  have broader result, partial-failure, manifest, transaction, and recovery
  contracts in the full-experience specification and remain separately
  planned work.

## 9. Useful investigation commands for the implementer

These commands locate the cross-cutting old semantics before editing:

```sh
rg -n "AdvertisedCommit|advertisedCommit|ExactCommit|exactCommit|ParentCommit|parentCommit" internal tutorial README.md docs/spec
rg -n "fixture/clone-bootstrap|symbolic-ref HEAD" tutorial internal --glob '*.sh' --glob '*_test.go'
rg -n "AheadBehind|summarizedStatus|renderWorkspaceStatus" internal
```

The implementer should treat files under `docs/ai/runs/` as immutable except
for the one ledger created by an explicitly authorized run of the parent plan.
