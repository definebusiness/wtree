# Idea: immutable release lock manifests

Status: specified
Specification: [Immutable release lock manifests specification](../spec/release-lock-manifests.md)

## Summary

`wtree` should freeze the current commits of one local multi-repository
workspace into a small, deterministic release lock. The lock is a revision
overlay for the portable `project.wtree.yml`, not a second project manifest.

The base repository owns and versions both files:

```text
project.wtree.yml       moving repository topology and clone information
project.wtree.lock.yml  immutable release name and child revisions
```

The release identity has two layers:

1. the base repository commit selected by the CI checkout or an outer release
   tag; and
2. the exact non-base repository commits recorded in the lock at that base
   commit.

The base revision is deliberately absent from the lock. Recording it in a file
committed by that same revision would create a circular dependency. The release
name in the lock is passive metadata: `wtree` does not interpret it as a branch
or tag and supplies it only to the local `post-release` hook.

## Lock format

A minimal format is:

```yaml
version: 1

project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  manifest_sha256: 7f2e0c4c5f1c9bf1c1d53e95b71db65dc21b2f84d451af140e027f976f4f0f3b

release:
  name: v1.4.0

repositories:
  backend:
    revision: 4f3c27f6b2b09a795552ad3c9c42f520d018b52e
  frontend:
    revision: 8b7133d374a09fd0d2f92ae92094386bd42c56aa
  shared:
    revision: 1930ad76a20bdbb59bc017d54161b3219449b27c
```

The repository map contains every non-base repository and no base-repository
entry. Each revision is a full hexadecimal Git object ID rather than an
abbreviation. The linked specification accepts full 40- or 64-character
lowercase hexadecimal IDs and adds repository-local commit verification, so it
does not assume that every repository uses SHA-1.

The lock does not duplicate repository URLs, remotes, identities, parents,
mounts, default branches, hooks, or machine-local paths. Those values remain
authoritative in the exact portable manifest generation bound by
`manifest_sha256`. This avoids disagreement rules between two project models.

The stable default filename is `project.wtree.lock.yml`. Alternative archival
filenames are outside the first version because the base repository's Git
history already versions the file.

## Creating a lock

The illustrative command is:

```sh
wtree release lock v1.4.0 [workspace] [--force] [--dry-run] [--json] [--no-hooks]
```

The release name is required, written verbatim after validation as non-empty
control-free text, and passed to `post-release`. It has no other semantics in
`wtree`; in particular, `wtree` does not require it to be a valid Git ref.

Lock generation uses the caller-owned local workspace. It:

1. loads the selected complete workspace and its portable manifest;
2. requires every configured repository to be present, clean, and of the
   configured identity;
3. reads the current full `HEAD` of every non-base repository;
4. writes the project ID, portable-manifest digest, release name, and observed
   revisions in deterministic order; and
5. atomically creates `project.wtree.lock.yml` in the base repository.

This is an observation, not an atomic cross-repository snapshot. The caller is
responsible for not changing repositories while the command runs and for
choosing a combination that has actually been tested. `wtree` does not require
a particular branch, inspect upstream state, contact remotes, or revalidate all
repositories immediately before writing the lock.

Generating byte-identical content when the target already contains those exact
bytes succeeds without rewriting it. A clean tracked lock from the previous
release is replaced normally. An untracked lock or one with uncommitted changes
is protected unless the caller supplies `--force`.

`--dry-run` performs the same local validation and renders the proposed release
name and repository-to-revision mapping without writing the lock or running a
hook.

## Local `post-release` hook

The hook-capable local `.wtree.yml` configuration should accept a
repository-scoped `post-release` event using the existing ordered hook shape:

```yaml
version: 3

hooks:
  post-release:
    - id: tag-backend
      repository: backend
      command: [tag-wtree-release]
      timeout: 5m
    - id: tag-frontend
      repository: frontend
      command: [tag-wtree-release]
      timeout: 5m
    - id: tag-shared
      repository: shared
      command: [tag-wtree-release]
      timeout: 5m
```

`post-release` is local and trusted because release scripts may need the
caller's credentials, signing configuration, and other ambient environment.
It is not read from or embedded in the portable manifest or release lock.

The event runs only after the lock file has been written successfully. It runs
outside the core lock-generation operation and receives the normal
authoritative local hook environment, with these release-specific values:

| Variable | Value |
|---|---|
| `WTREE_HOOK` | `post-release` |
| `WTREE_OPERATION` | `release-lock` |
| `WTREE_RELEASE_NAME` | Exact `release.name` from the generated lock |
| `WTREE_HEAD` | Validated full object ID of the hook's selected repository |

The existing project, workspace, repository, and path variables remain
available. A hook without an explicit `repository` uses the base repository,
as other local hooks do. The documented example places one script on `PATH`
and invokes it through one declaration for each non-base repository, giving
the same script that repository's exact `WTREE_HEAD` and working directory.

The hook is an explicitly configured user operation, not an implicit `wtree`
tagging feature. Its commits, tags, signatures, and pushes are outside rollback.
If it fails, the valid lock remains written and the command reports that the
post-release action failed. `--no-hooks` suppresses execution, and `--dry-run`
never runs it.

The hook example deliberately tags only non-base repositories. After it
succeeds, the user reviews and commits the new lock, then explicitly creates
the base tag at that new commit. Tagging the base before the lock commit would
point at the wrong base revision.

## CI materialization

A typical CI system already checks out the desired base branch, tag, or exact
commit. That checkout contains matching versions of `project.wtree.yml` and
`project.wtree.lock.yml`. CI therefore does not need `wtree clone` first:

```sh
git clone --branch v1.4.0 https://github.com/acme/product.git product
cd product
wtree release materialize project.wtree.lock.yml
```

The illustrative `release materialize` command adopts the existing base
checkout and creates the rest of the project. It:

1. strictly loads both files and verifies the project ID and exact portable
   manifest digest;
2. treats the existing base checkout and commit as authoritative;
3. derives repository identities, clone sources, topology, and mounts from
   `project.wtree.yml`;
4. creates every non-base repository parent-first at its configured mount;
5. fetches all advertised branch and tag refs from its configured remote;
6. fails if the locked commit is not available after that fetch;
7. checks out the exact locked commit in detached state;
8. verifies identity, mount, cleanliness, and exact `HEAD`; and
9. writes the local configuration, default workspace state, and registry entry
   needed by normal read-only `wtree` commands.

No branch tip may replace a missing locked commit. Materialization either finds
the exact object selected by the lock or reports that the release cannot be
reconstructed from the configured remote.

Authentication remains Git's responsibility. Materialization passes through
standard noninteractive SSH-agent, askpass, and configured credential-helper
authentication without putting credentials into either manifest, the lock,
command output, or persisted state. `wtree` does not accept or manage secrets.

The first version materializes only into an otherwise unmaterialized project
whose base repository was supplied by the caller or CI. Converting an existing
development workspace, reconciling topology changes, and lock-aware `update`
are separate future features.

Successful materialization already verifies the complete exact workspace. CI
then runs build, test, package, signing, publication, deployment, and
notification as explicit pipeline steps or through `wtree exec`. A separate
verification command and a `post-materialize` hook add no required capability
to the first version.

## Failure and cleanup

Lock generation changes only its output file before the optional hook runs.
Materialization validates all fetched locked revisions before publishing final
checkouts. On failure it removes repository paths and local state created by
that invocation and reports any path it cannot safely remove. It never removes
or rewrites the caller-provided base checkout.

Fetching remote refs is allowed to remain as an internal Git side effect. The
operation does not push, create remote refs, or silently fall back to a branch.

## Relationship to normal `wtree` behavior

Release locking reuses the existing project model:

- repository IDs remain independent of paths;
- the portable manifest remains the sole topology and fetch authority;
- nested and sibling repositories remain ordinary Git repositories;
- mounts remain parent-relative and protected by committed ignore rules;
- development workspaces continue to use branches; and
- materialized release children use detached exact commits.

No `.gitmodules`, gitlinks, merged histories, or second repository model are
introduced.

## Explicit non-goals for the first version

- Atomic observation of all repositories at one instant.
- Remote-availability checks during lock generation.
- Cloning or changing the caller-provided base repository.
- Lock-aware conversion or update of an existing development workspace.
- Automatic commits, tags, signing, or pushes performed by `wtree` itself.
- Rollback of effects produced by the user's `post-release` hook.
- Per-release archival filenames.
- Child tag orchestration as a built-in command.
- A separate release-verification command or `post-materialize` hook.
- Credential storage, provider login, token refresh, or SSH key management.
- General `status` or `doctor` integration.
- Artifact signing, provenance attestations, or SBOM generation.

## Specification handoff

The linked specification fixes the first schema to lowercase full 40- or
64-character hexadecimal object IDs, uses idempotent whole-event reruns rather
than durable setup-hook retry, and requires reuse of existing initialization,
clone, registration, and hook-process boundaries where their authority matches
this narrower release workflow.
