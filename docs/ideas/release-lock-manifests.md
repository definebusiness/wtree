# Idea: immutable release lock manifests

Status: initial

## Summary

`wtree` should support freezing one tested multi-repository workspace into an
immutable release lock manifest. The repositories would retain their separate
Git histories and normal development workflows, while the outer repository
would record the exact child commits that together form a release.

The outer repository's release tag would identify its own commit. A committed
lock manifest at that tagged commit would identify every nested repository's
exact commit. Together, those two layers would provide one reproducible and
auditable release identity without converting the project to a monorepo or
introducing Git submodules.

This follows the same broad model as revision-locked manifests used by
multi-repository tools such as Android's `repo`: repository topology and
fetch information are described separately from the exact revisions selected
for a reproducible checkout.

## Why a separate lock manifest is needed

The portable `project.wtree.yml` should continue to describe the moving
project definition:

- repository identities and hierarchy;
- clone remotes and URLs;
- default branches;
- default mounts; and
- other information needed for normal clone, update, and synchronization
  workflows.

A release lock has a different lifecycle. It is an immutable snapshot of one
verified combination of commits. Mixing exact release commits into the normal
project manifest would make it unclear whether `sync` should follow branches
or remain permanently pinned.

The proposed files therefore have distinct responsibilities:

```text
project.wtree.yml       portable, moving project definition
project.wtree.lock.yml  immutable revisions for one release snapshot
```

The lock filename is tentative. A versioned name such as
`releases/v1.4.0.wtree.lock.yml` could be useful when the outer repository
retains multiple release locks on one long-lived branch. When a lock exists
only at its corresponding outer release tag, a stable root filename is
sufficient because Git history already versions it.

## The root commit is anchored by the outer tag

The lock manifest should not contain the final commit ID of the outer
repository in which that same file is committed. Doing so creates a circular
dependency: committing the file changes the commit ID that the file would
need to contain.

Instead:

1. the outer repository's annotated release tag identifies the root commit;
2. the lock manifest stored in that root commit identifies all nested
   repository commits; and
3. the combination is the complete release snapshot.

Conceptually:

```text
outer tag v1.4.0
  └── outer commit
      └── project.wtree.lock.yml
          ├── backend commit
          ├── frontend commit
          └── shared commit
```

The outer tag is therefore the single entry point that CI, release tooling,
and users should retain.

## Possible lock format

One possible schema is:

```yaml
version: 1

project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221

release:
  name: v1.4.0

repositories:
  backend:
    revision: 4f3c27f6b2b09a795552ad3c9c42f520d018b52e
    remote: origin
    url: https://github.com/acme/backend.git
    mount: backend

  frontend:
    revision: 8b7133d374a09fd0d2f92ae92094386bd42c56aa
    remote: origin
    url: https://github.com/acme/frontend.git
    mount: frontend

  shared:
    revision: 1930ad76a20bdbb59bc017d54161b3219449b27c
    remote: upstream
    url: https://github.com/acme/shared.git
    parent: backend
    mount: shared
```

The exact schema requires a later specification. At minimum, each entry must
bind a stable repository identity to:

- a full Git object ID rather than an abbreviated hash;
- the fetch source needed by an independent CI environment;
- its immediate parent and effective release mount; and
- enough identity information to prevent a commit from an unrelated
  repository from being accepted merely because an object ID string matches.

The lock must not contain machine-local checkout paths, credentials, access
tokens, worktree administration paths, or mutable branch names as the source
of the locked revision.

## Proposed release workflow

A typical release would begin after the coordinated release branches have
completed hardening:

```sh
wtree status release/v1.4.0
wtree release lock v1.4.0
git add project.wtree.lock.yml
git commit -m "chore: lock release v1.4.0"
git tag -a v1.4.0 -m "Release v1.4.0"
git push origin release/v1.4.0 v1.4.0
```

The command names are illustrative, not yet a public contract.

Before generating the lock, `wtree` should verify every repository in the
selected workspace:

1. The checkout has the configured Git identity.
2. The checkout is present, attached to the expected release branch, and not
   partial.
3. Tracked, staged, and untracked changes are absent unless a future explicit
   policy defines otherwise.
4. The current commit is known exactly.
5. The commit is reachable from the intended remote or has otherwise been
   proven available to release CI.
6. The effective parent and mount match the workspace being frozen.
7. No unresolved recovery record or structural doctor finding remains.
8. Existing lock output would either be identical or require explicit
   replacement intent.

The lock should be generated deterministically. Repeating the command against
the same workspace and project definition should produce byte-identical
output.

## Child repository tags

Tagging every nested repository with the same release name is useful but not
required for reproducibility. The full object IDs in the lock manifest are the
authoritative revision bindings.

Optional child tags provide:

- a human-readable release marker inside each repository;
- easier repository-local auditing and support work;
- a natural place for signed or annotated release metadata; and
- protection against garbage collection when hosting policies do not retain
  otherwise unreachable commits indefinitely.

If `wtree` eventually offers tag orchestration, it should be a separately
authorized operation rather than an implicit side effect of lock generation.
It would need complete preflight, remote/tag collision handling, rollback
limits, signing policy, and clear behavior when only some pushes succeed.
The first lock implementation should remain local and non-publishing.

## Reproducing a release

CI should start from the outer repository's release tag and then materialize
the child repositories from the committed lock:

```sh
git clone --branch v1.4.0 https://github.com/acme/product.git product
cd product
wtree clone --lock project.wtree.lock.yml .
wtree release verify project.wtree.lock.yml
```

These commands are illustrative. A future implementation might instead extend
`wtree sync` or introduce `wtree checkout --lock`.

Materialization should:

1. read and strictly validate the lock schema;
2. verify that it belongs to the same project definition;
3. fetch each repository from its recorded credential-free source;
4. check out the exact object ID in detached state or on a clearly named local
   release branch;
5. mount repositories parent-first using the locked hierarchy;
6. verify every resulting Git identity, object ID, mount, and clean status;
   and
7. fail transactionally rather than leave a release tree that appears
   complete but contains mixed revisions.

No branch tip may be substituted for a locked object ID. A branch can move
after release and therefore cannot establish reproducibility.

## Verification and provenance

A commit hash provides content identity within a Git object format, but the
release process also needs provenance. A robust design should combine:

- protected or signed outer release tags;
- protected child tags when optional per-repository tagging is used;
- credential-free, reviewable repository URLs in the portable project
  definition;
- confirmation that every locked object is available from its expected
  remote before the outer tag is published;
- CI verification that the checked-out object IDs exactly match the lock;
- an auditable association between the build artifact, outer tag, and lock
  file digest; and
- optional future attestations or software bill of materials without making
  them prerequisites for the basic lock format.

The outer tag and lock must be published only after all referenced child
commits are available. Otherwise the outer tag would describe a release that
cannot be reconstructed by another environment.

## Relationship to normal `wtree` behavior

Release locking should reuse existing `wtree` concepts rather than introduce
a second repository model:

- repository IDs remain independent of paths;
- nested repositories remain ordinary independent Git repositories;
- mounts remain ignored by their immediate parents;
- the project hierarchy remains parent-relative;
- normal development workspaces continue to follow branches;
- release materialization selects exact commits; and
- no `.gitmodules` file or gitlink is introduced.

This makes the lock manifest a metadata layer over the existing model. It does
not merge repository histories and does not change everyday workspaces into
submodules.

## Failure and safety expectations

Lock generation should be read-only except for writing its explicit output
file. It must fail before replacing an existing lock when:

- any repository is missing, dirty, detached unexpectedly, or at an
  unverified identity;
- a commit cannot be resolved or is not available from the intended remote;
- the workspace has inconsistent branches, mounts, or recovery state;
- the portable project definition and observed workspace disagree;
- an existing lock would be changed without explicit replacement intent; or
- output would contain credentials or machine-local paths.

A dry run should print the exact repository-to-object-ID mapping and proposed
output without touching the lock file, Git refs, index, worktrees, registry,
or workspace state.

Lock consumption must reject unknown schema versions, duplicate identities,
missing repositories, unsafe mounts, object-ID mismatches, and fetches that do
not make the requested object available. It must never silently fall back to
a default branch.

## Potential command surface

The eventual CLI might look like:

```text
wtree release lock <release-name> [<workspace>] [--output <path>] [--dry-run] [--json]
wtree release verify <lock-path> [--json]
wtree clone --lock <lock-path> [destination]
```

Alternative designs could use `wtree lock`, `wtree manifest lock`, or extend
existing clone/sync commands. Naming, replacement policy, detached-versus-local
branch behavior, and JSON contracts belong in a later specification and
implementation plan.

## Explicit non-goals

- Combining repository histories into a monorepo.
- Creating temporary or permanent Git submodules.
- Recording mutable branches instead of exact object IDs.
- Automatically committing, tagging, signing, or pushing during initial lock
  generation.
- Storing the outer repository's self-referential final commit in its own lock
  file.
- Replacing artifact signing, provenance attestations, or an SBOM system.
- Defining the final CLI or schema in this idea document.

## Open design questions

1. Should the lock be a stable root file versioned by the outer tag, or one
   named file per release under a `releases/` directory?
2. Should release mounts always come from the selected workspace, or should
   the portable project defaults remain authoritative?
3. Should a locked checkout be detached, use a generated local branch, or
   allow the caller to choose?
4. How should remote availability be proven without making normal local lock
   generation dependent on credentials or a network connection?
5. Should optional child tagging be a separate future command, and what
   partial-push recovery contract would it require?
6. Should the lock embed clone metadata or reference a digest of the exact
   `project.wtree.yml` from which it was derived?
7. How should SHA-1 and SHA-256 repositories express object IDs without
   coupling the schema to one Git object format?
8. Which command should materialize a lock: clone, checkout, sync, or a
   release-specific command?
