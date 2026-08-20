# Live-branch clone and upstream-aware human status specification

Status: implemented
Source idea: none (created directly)
Implementation plan: [Live-branch clone and upstream-aware human status implementation plan](../plans/clone-live-branch-and-upstream-status.md)
Related specifications: [Portable manifest clone specification](portable-manifest-clone.md); [`wtree` specification](wtree.spec.md); [Full multi-repository experience capability specification](full-multi-repository-experience.md)

## 1. Purpose and scope

This specification corrects two current user-facing behaviors:

1. ordinary portable clone fails when a remote's symbolic `HEAD` and the
   manifest-selected local branch are both `main`; and
2. human `wtree status` output can say `clean` while its already-computed Git
   facts say that the branch is ahead, behind, or divergent from its upstream.

The changes are deliberately narrow. They do not implement aggregate fetch,
manifest reconciliation through `wtree update`, release locks, automatic
merge or rebase, or repository deletion.

## 2. Relationship to existing contracts

For ordinary portable clone, this specification replaces the exact-commit
execution requirement in the implemented
[portable manifest clone specification](portable-manifest-clone.md). Remote
preflight remains mandatory, but an observed remote commit is no longer an
immutable checkout target.

This specification also resolves the narrow human-rendering part of the
drift-aware status work described in sections 6.2 and 10.4 of the
[full multi-repository experience specification](full-multi-repository-experience.md).
It does not implement that specification's `wtree fetch`, manifest drift,
workspace-kind, omission, or update capabilities.

All existing destination safety, credential handling, repository identity,
tracked-manifest, nested-ignore, transaction, rollback, registry, and
workspace-state rules remain in force unless this document explicitly changes
them.

## 3. Ordinary clone branch semantics

### 3.1 Manifest authority

For each repository, the portable manifest remains authoritative for:

- the clone URL and local remote name;
- the remote branch named by `upstream.merge`;
- the resulting local branch named by `default_branch`; and
- the local branch's upstream remote and merge reference.

The remote's symbolic `HEAD` is transport metadata only. It must not select a
branch, create an additional local branch, or override the manifest.

After a successful clone, each new repository has exactly one local branch:
the manifest-selected `default_branch`. That branch tracks the manifest's
configured remote and merge reference.

### 3.2 Remote preflight is observational

Planning still contacts every declared remote before filesystem mutation. It
validates that every selected remote branch exists and records the commit
advertised at that time for diagnostic and dry-run output.

The observed commit is not a promise that execution will check out that object.
It must not be passed to a force-reset or used as the immutable target of an
ordinary clone.

Execution fetches the manifest-selected remote branch and checks out the tip
obtained by that execution-time fetch. If the branch advances after planning
but before that fetch, the clone uses the newer fetched tip. If it advances
after the fetch, the resulting checkout may immediately be behind until the
next fetch; ordinary clone does not loop until a moving branch becomes stable.

This behavior intentionally provides no cross-repository atomic snapshot and
no claim that independently moving branches are mutually compatible.
Compatible release snapshots belong to a separately specified lock or release
manifest.

### 3.3 Checkout and verification

The implementation must establish the selected branch without force-updating
a branch that Git considers checked out. It must work for all of these cases:

- remote `HEAD` and selected local branch are both `main`;
- remote `HEAD` names a different branch;
- the local branch name differs from the remote branch name; and
- the selected branch advances after planning but before execution fetch.

Repository verification uses the commit actually checked out during
execution. In particular:

- the actual `HEAD` is recorded in default workspace state;
- identity roots must be reachable from the actual `HEAD`;
- parent ignore checks use the actual checked-out parent commit;
- the base repository's tracked `project.wtree.yml` must remain byte-identical
  to the loaded manifest; and
- clean-worktree, submodule, upstream, mount, and Git-identity checks continue
  unchanged.

If the selected remote branch disappears, cannot be fetched, fails
verification, or no longer contains the byte-identical tracked manifest, clone
fails through the existing cleanup and rollback contract. It must not fall
back to remote `HEAD`, another branch, or a stale preflight commit.

### 3.4 Planning and output terminology

Dry-run remains useful for validating sources, branch availability,
destinations, registry conflicts, ordering, and verification requirements. It
must describe remote commits as **observed commits**, never as exact checkout
targets.

Because the current version-one clone plan and result schemas describe exact
commit execution, the implementation must introduce version two for those
public JSON documents rather than silently reinterpret version-one fields.
Version two uses `observedCommit` for the preflight observation and does not
emit `exactCommit` or `parentCommit` as execution promises. Completed clone
JSON must expose the actual checked-out commit for every repository.

The persisted portable manifest, local configuration, workspace state,
registry, and recovery schema versions do not change.

## 4. Human status upstream reporting

### 4.1 No implicit fetch

`wtree status` remains observational and must not contact a remote or update
remote-tracking refs. Ahead and behind values are relative to the locally
available upstream ref and may be stale until a future `wtree fetch` or an
ordinary Git fetch updates it.

### 4.2 Human table

The existing `STATUS` column retains its current working-tree and structural
meaning, including `clean`, `modified`, `missing`, `detached`, and the existing
drift states. Human output adds an `UPSTREAM` column so `clean` is never the
only visible fact when upstream drift is known.

For a valid attached checkout, render exactly one of:

| Git facts | Human `UPSTREAM` value |
|---|---|
| no configured upstream | `none` |
| ahead 0, behind 0 | `up-to-date` |
| ahead N, behind 0 | `ahead N` |
| ahead 0, behind N | `behind N` |
| ahead N, behind M | `diverged (ahead N, behind M)` |

Render `n/a` when no meaningful upstream comparison can be made, including a
missing, stale-state, unknown-repository, or detached checkout.

The column is always present so the table shape is deterministic. Repository
ordering, branch selection, mount rendering, output-channel behavior, and
failure taxonomy remain unchanged.

### 4.3 JSON compatibility

The current status JSON already exposes `ahead`, `behind`, and `upstream`.
Those fields and the existing `status` field retain their meanings. This scope
does not add a JSON-only summary string or change `status: "clean"` to mean
`up-to-date`.

## 5. Required verification

Tests must prove:

- clone succeeds when remote `HEAD`, remote branch, and local branch are all
  `main`;
- a distinct remote `HEAD` creates no extra local branch and does not affect
  the selected checkout;
- different local and remote branch names retain correct tracking;
- a branch advance between planning and execution is checked out at the
  execution-time fetched tip, without a force-update of the preflight commit;
- remote deletion and verification failures retain existing no-publication,
  cleanup, rollback, and secret-redaction behavior;
- clone dry-run and completed JSON use the version-two observed-versus-actual
  contract and human output no longer claims an exact planned commit;
- status human output covers no-upstream, up-to-date, ahead, behind, diverged,
  detached, missing, and structural-drift cases;
- status JSON remains compatible and status performs no network or fetch
  operation; and
- the tutorial end-to-end clone works with ordinary `main` remote heads and no
  `fixture/clone-bootstrap` workaround.

## 6. Explicit non-goals

This specification does not authorize:

- `wtree fetch`, `wtree update`, `wtree sync`, or a common pull command;
- automatic merge, rebase, conflict resolution, or branch publication;
- automatic deletion of a repository removed from a portable manifest;
- a compatibility guarantee across independently moving repository branches;
- locked or release clone semantics;
- weakening tracked-manifest, identity, ignore, credential, rollback, or
  destination protections; or
- implicit network access from `wtree status`.
