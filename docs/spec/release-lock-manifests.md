# Immutable release lock manifests specification

Status: implemented
Source idea: [Immutable release lock manifests](../ideas/release-lock-manifests.md)
Implementation plan: [Immutable release lock manifests implementation plan](../plans/release-lock-manifests.md)
Related specifications: [Portable manifest clone specification](portable-manifest-clone.md); [Logical project root and repository forest specification](logical-project-root-base-repository.md); [Local and shared workspace lifecycle hooks specification](local-workspace-lifecycle-hooks.md); [Full multi-repository experience capability specification](full-multi-repository-experience.md)

## 1. Purpose and scope

`wtree` must be able to record the current commits of a multi-repository
workspace and reconstruct the same non-base repositories around a base checkout
provided by CI:

```text
wtree release lock <release-name> [workspace]
wtree release materialize <lock-file>
```

The portable `project.wtree.yml` remains the authority for repository identity,
clone sources, topology, and mounts. The release lock is only a revision
overlay: a release name, an exact portable-manifest digest, and the commits of
all non-base repositories.

This feature establishes reproducible release source. Build, test, package,
sign, attest, publish, deploy, promote, and notify steps remain ordinary CI
commands. A successful materialization already verifies the resulting workspace;
the first version has no separate `release verify` command and no
`post-materialize` hook.

## 2. Release identity and ownership

The portable manifest's `project.base_repository` identifies the base
repository. The base repository owns and versions `project.wtree.yml` and
`project.wtree.lock.yml`.

The base revision is deliberately absent from the lock. CI selects the base
commit or tag containing the matching manifest and lock. A complete release is:

```text
base repository commit
  + exact project.wtree.yml bytes at that commit
  + exact project.wtree.lock.yml bytes at that commit
  + every non-base commit named by the lock
```

Lock generation observes local repositories sequentially without fetching. It
does not claim an atomic cross-repository snapshot. The caller owns coordination
and decides whether the observed combination is suitable for release.

`wtree` does not interpret the release name as a Git ref or semantic version. It
does not commit, sign, tag, push, publish, or deploy, except that a trusted local
`post-release` hook may perform caller-defined effects.

## 3. Release lock format

The canonical version-one document is:

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
```

All shown fields are required. `repositories` contains exactly the portable
manifest's non-base repository IDs and may be empty for a base-only project.
The lock contains no URLs, topology, mounts, branches, hooks, credentials,
paths, timestamps, tag names, or base revision.

Validation requires:

- `version` equals integer `1`;
- `project.id` equals the portable project ID;
- `project.manifest_sha256` is the lowercase SHA-256 digest of the exact
  `project.wtree.yml` bytes;
- `release.name` is nonblank and contains no control characters; and
- every `revision` is a full lowercase 40- or 64-character hexadecimal object
  ID that Git subsequently proves is a commit in the applicable repository.

Decoding is strict: reject unknown or duplicate fields, aliases, merge keys,
multiple documents, malformed UTF-8, null required values, and trailing
document content. Canonical output is deterministic UTF-8 YAML with stable
field order, lexically sorted repository IDs, LF endings, and a final newline.

## 4. Lock generation

### 4.1 Command

```text
wtree release lock <release-name> [workspace]
  [--force] [--dry-run] [--json] [--no-hooks] [--data-dir <path>]
```

The optional workspace uses normal workspace resolution and defaults to the
workspace containing the invocation path. Output is always
`project.wtree.lock.yml` at the base repository root.

### 4.2 Preconditions and observation

Before writing, the command must:

1. resolve one complete registered workspace;
2. strictly load the exact portable manifest bytes;
3. require configuration, project identity, topology, paths, and persisted
   workspace membership to agree;
4. require every repository checkout to exist with its recorded Git identity;
5. require every checkout to be clean, except for the base lock path; and
6. read the full current `HEAD` of every non-base repository.

Attached and detached checkouts are accepted. Branch, upstream, ahead/behind,
and remote availability do not affect generation. Generation performs no
fetch. The caller is responsible for ensuring the selected commits are
published before publishing the base release.

### 4.3 Writing and replacement

- An absent lock is atomically created.
- A byte-identical lock succeeds without rewriting it.
- A differing lock that is tracked at base `HEAD` and whose working bytes still
  equal that tracked version is atomically replaced as the normal next-release
  operation.
- A differing untracked lock or a lock with uncommitted working changes fails
  unless `--force` is supplied.
- `--force` replaces only that lock after all other preconditions pass.
- Symlinks, directories, special files, or target-identity changes fail safely.
- Failure before replacement leaves the prior bytes intact.

The command never stages or commits the resulting file. Dry-run performs the
same local validation, reports the candidate lock and disposition, and writes
nothing or runs no hook.

A successful real invocation runs `post-release` even when the lock is already
byte-identical, providing a simple retry path for an idempotent hook.

## 5. Local `post-release` hook

### 5.1 Authority and timing

Local configuration version three accepts `hooks.post-release`. Version two,
portable hooks, and `shared_hooks` reject the event. The hook is trusted local
configuration and is never distributed through the portable manifest.

Despite its concise name, `post-release` runs after successful lock creation or
acceptance and before the caller commits the lock or creates the base release
tag. Documentation must state this timing explicitly.

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
```

Existing ordered, repository-scoped, direct-process, working-directory,
timeout, cancellation, output-bounding, and executable-resolution behavior
applies. `--no-hooks` skips executable preflight and execution. Dry-run and core
failure run no hook.

### 5.2 Environment and failure

Each hook runs in its selected repository. The inherited environment is
retained after `wtree` removes and authoritatively replaces reserved `WTREE_`
values, including:

| Variable | Value |
|---|---|
| `WTREE_HOOK` | `post-release` |
| `WTREE_OPERATION` | `release-lock` |
| `WTREE_RELEASE_NAME` | Exact decoded release name |
| `WTREE_HEAD` | Full observed HEAD of the selected repository |

Hooks run sequentially after the valid lock has been published and the project
mutation lock released. The first failure stops later hooks and fails the
command, but the lock and earlier hook effects remain. There is no durable hook
retry record in the first version; rerunning the identical lock command starts
the event again. Release hooks must therefore be idempotent.

### 5.3 Tagging example contract

The tutorial supplies one `tag-wtree-release` program through inherited `PATH`
and declares it separately for each non-base repository. It:

- validates `WTREE_RELEASE_NAME` as a Git tag name;
- creates an annotated local tag at `WTREE_HEAD`;
- accepts an existing tag only when its peeled commit equals `WTREE_HEAD`;
- rejects a collision without moving the tag; and
- never tags the base repository or pushes.

After successful child tagging, the caller reviews and commits the lock and
tags the resulting base commit:

```sh
git add project.wtree.lock.yml
git commit -m "chore: lock release v1.4.0"
git tag -a v1.4.0 -m "Release v1.4.0"
```

Child commits and tags must be pushed before the base release tag so every
published base release can be reconstructed.

## 6. CI materialization

### 6.1 Starting state

```text
wtree release materialize <lock-file>
  [--dry-run] [--json] [--verbose] [--data-dir <path>]
```

The lock path is the regular `project.wtree.lock.yml` at the current base
checkout root, beside `project.wtree.yml`. Both files must be tracked at the
current base `HEAD`, and their working bytes must equal the tracked bytes.

The base checkout may be attached or detached. It must be clean, match the
portable base identity and mount, and contain no submodule configuration. The
project must not already be registered at the derived logical root; local
configuration, workspace state, recovery records, and non-base destinations
must be absent. The first version does not update, repair, or convert an
existing workspace.

Before network or filesystem mutation, materialization validates the strict
manifest and lock, exact manifest digest, project ID, repository set, topology,
mounts, identities, committed parent ignore rules, destinations, and registry
conflicts.

### 6.2 Authentication delegated to Git

`wtree` does not parse, store, refresh, or transmit credentials itself.
Materialization delegates remote authentication to Git's standard
noninteractive transport mechanisms and the caller's Git configuration and
process environment.

At minimum, the implementation must support:

- SSH-agent authentication, including the caller's `SSH_AUTH_SOCK`;
- HTTPS authentication through `GIT_ASKPASS`, including the helper's required
  inherited secret environment; and
- caller-configured Git credential helpers where Git would normally use them
  noninteractively.

`GIT_TERMINAL_PROMPT=0` remains authoritative: missing credentials fail instead
of prompting. Portable manifests and locks remain credential-free. `wtree`
accepts no credential CLI flags and persists no authentication environment,
helper output, tokens, passwords, private keys, or credential-bearing URLs.
Command results and Git errors remain bounded and redact credential-bearing URL
userinfo. Authentication tests use fake helpers, agents, or Git wrappers and
local remotes; they require no real secret or hosted service.

This delegation must not re-enable repository Git hooks, implicit shell
execution, submodule recursion, or branch selection. Provider-specific login,
credential storage, SSH key management, host-key enrollment, and token refresh
are explicitly outside `wtree`.

### 6.3 Exact repository acquisition

For each non-base repository, materialization reuses the established clone
staging, publication, rollback, and recovery machinery while changing revision
selection:

1. initialize a private staged repository;
2. configure the manifest's remote name and clone URL;
3. fetch all advertised branch and tag refs using explicit refspecs;
4. require the locked object ID to resolve exactly to a commit;
5. check out that commit detached, with hooks and recursive submodules disabled;
6. verify exact `HEAD`, detached state, identity roots, remote configuration,
   cleanliness, and absence of submodules; and
7. publish verified repositories parent-first, followed by local configuration,
   default workspace state, and registry state.

If a commit is unavailable after fetching advertised heads and tags,
materialization fails. It never fetches an unadvertised object ID directly,
selects a substitute branch tip, or falls back to remote `HEAD`.

All children are staged and verified before the first final mount is published.
Failure removes only paths and state proven to have been created by this
invocation. Uncertain data is retained with actionable recovery information.
The existing base checkout is never moved, rewritten, or removed.

Dry-run validates local inputs, topology, destinations, and conflicts and
reports planned repositories and revisions. It performs no remote operation and
does not claim that commits or credentials are currently available.

### 6.4 Successful result and subsequent CI work

Success means every non-base repository is clean and detached at its exact
locked commit, the base remains at the caller-selected commit, and the complete
workspace is registered for normal read-only commands and `wtree exec`.

Materialization executes no lifecycle hook. CI runs preparation, build, test,
packaging, signing, publication, deployment, and notification commands as
explicit later steps. This provides clearer pipeline logs and avoids another
trusted-code boundary. A future concrete setup requirement may reuse the
existing explicitly authorized portable `post-clone` model; the first version
does not add `post-materialize`.

## 7. CLI and output

`release` is a new root group with `lock` and `materialize` children. Help,
errors, selectors, and flags follow existing CLI conventions.

Human output is deterministic and concise. JSON output follows existing
versioned command-result conventions and includes operation, status, project
ID, release name, lock path, manifest digest, dry-run where applicable, and
parent-first repository results with expected and observed revisions. Lock
results distinguish created, unchanged, and replaced; materialization results
distinguish the caller-provided base from created children.

JSON mode emits one document and no human prose. Secrets, authentication
environment, helper output, credential-bearing URLs, hook output, and unbounded
Git diagnostics must not enter JSON, persisted state, or errors. Hook failure
must clearly state that lock generation succeeded while the external action
failed.

## 8. Documentation and tutorial

Delivery is incomplete without:

- README release concepts, command summary, trust boundaries, authentication,
  and a CI example;
- root and command help plus `--how-to` guidance;
- troubleshooting for unavailable commits, authentication failure, manifest
  mismatch, dirty base, occupied destinations, tag collision, hook failure, and
  incomplete cleanup;
- current specification, traceability, and lifecycle updates; and
- `tutorial/RELEASES.md`, linked from the existing tutorial index and command
  guide.

The offline automated tutorial must demonstrate:

1. a base and at least two non-base repositories in a sibling or nested layout;
2. one local `post-release` declaration per non-base repository using the same
   idempotent tag program from `PATH`;
3. dry-run, lock creation, identical rerun, hook bypass, replacement of the
   previously committed lock, and collision failure;
4. reviewing, adding, and committing the lock, then tagging the new base commit;
5. publishing child commits and tags before the base tag;
6. a clean CI checkout of that base release followed by materialization;
7. exact detached child commits and an unchanged base commit;
8. explicit subsequent CI commands, including an example using `wtree exec`;
9. fake noninteractive Git authentication propagation and secret-safe output;
   and
10. no `post-materialize` hook or implicit build/release action.

## 9. Acceptance criteria

1. Lock output is strict, deterministic, bound to exact manifest bytes, and
   records exactly the current non-base commits without fetching.
2. Ordinary next-release generation replaces the clean tracked prior lock;
   overwriting an untracked or locally modified lock requires `--force`.
3. Local `post-release` hooks receive the exact release name and repository
   commit, run only after lock success, and are safely rerunnable by contract.
4. The documented hook tags only non-base repositories, accepts matching tags,
   rejects collisions, and leaves base commit/tag and all pushes explicit.
5. Materialization adopts but never alters the CI-provided base checkout and
   creates every non-base repository detached at the exact locked commit.
6. Missing advertised commits, invalid input, unavailable credentials, and
   publication failures produce clear failures without a false complete state.
7. SSH-agent, askpass, and configured credential-helper authentication reach
   Git noninteractively without secrets entering portable data, results, logs,
   errors, or persisted state.
8. Existing clone safety, staging, rollback, recovery, registry, path, ignore,
   and cross-platform behavior is reused rather than duplicated.
9. Materialization success itself proves the exact resulting workspace; no
   separate verification command or post-materialization hook is required.
10. Full documentation and the offline tutorial agree with the implementation,
    and repository normal, race, vet, formatting, build, tutorial, and release
    gates pass on supported platforms.

## 10. Non-goals

- A complete release-management, artifact, deployment, or provenance system.
- A separate `release verify` command in the first version.
- A `post-materialize` hook or automatic execution of portable hooks.
- Credential storage, provider login, token refresh, SSH key management, or
  credentials in manifests, locks, arguments, results, or state.
- Atomic cross-repository observation, tagging, pushing, or rollback.
- Built-in commit, tag, signature, or push operations.
- Direct fetching of unadvertised object IDs.
- Submodules or conversion/update of existing workspaces.
- Lock-aware development updates or general status/doctor integration.
