# Implementation context — immutable release lock manifests

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Immutable release lock manifests implementation plan](release-lock-manifests.md)
Source specification: [Immutable release lock manifests specification](../spec/release-lock-manifests.md)
Captured: 2026-09-04
Captured repository head: `99098d3ee594`

## 1. Purpose and precedence

This document preserves the product reasoning, current-code seams, and rejected
scope that an implementer or reviewer would otherwise need to rediscover. It is
not a second specification or plan. The source specification owns required
behavior, and the parent plan owns scope, sequencing, verification, and
supervision. They take precedence if this context becomes stale or conflicts
with them.

The captured commit is only a baseline locator. The release documents and other
working-tree changes may not be committed at that revision. Every plan run must
inspect the current filesystem and preserve unrelated work.

## 2. Product intent

The feature is deliberately small in concept:

1. `wtree release lock` records the current non-base repository commits.
2. A trusted local `post-release` hook may perform caller-owned automation such
   as creating matching tags in the non-base repositories.
3. The caller reviews and commits the lock in the base repository, then tags
   that new base commit.
4. CI checks out the base commit or tag and runs `wtree release materialize`.
5. Materialization reconstructs every non-base repository at its locked commit.
6. CI runs build, test, package, sign, attest, publish, deploy, and notification
   steps explicitly, using ordinary commands or `wtree exec`.

The lock establishes reproducible source composition. It is not a release
server, package manager, deployment system, or provenance framework.

## 3. Settled release model

### Two-layer identity

The base repository owns both tracked inputs:

```text
project.wtree.yml       repository identities, sources, topology, and mounts
project.wtree.lock.yml  release name and exact non-base commits
```

CI's checked-out base commit anchors the base revision. Recording that revision
inside a file committed by the same revision would be circular. The lock binds
the exact portable-manifest bytes by SHA-256 so repository IDs cannot silently
acquire a different meaning.

The release name is passive metadata. `wtree` preserves it and supplies it to
the local hook but does not treat it as a version, Git ref, filename, or remote
publication instruction.

### Caller-owned observation

Lock generation reads the local repositories sequentially. The user accepts
that a repository could change during observation and owns the decision that
the resulting combination was tested and is suitable for release. Do not add
cross-repository locking, remote freshness checks, or final all-repository
revalidation to manufacture an atomicity guarantee that Git cannot provide.

### Everyday lock replacement

The stable lock filename is meant to be committed repeatedly. A clean tracked
lock from the preceding release is therefore replaced normally. Protection is
needed only when the existing lock is untracked or differs from its base-HEAD
version; `--force` explicitly discards that local candidate.

## 4. Current repository baseline and reuse seams

| Area | Current implementation | Release delivery consequence |
|---|---|---|
| Portable configuration | `internal/config/portable_manifest.go` owns strict deterministic v2 parsing, validation, repository maps, base repository, URLs, identities, topology, and mounts. | Keep the lock separate. Reuse portable validation and exact source bytes; do not add revisions or credentials to the manifest. |
| Local hooks | `internal/config/hooks.go` owns local/portable/shared event validation and canonical ordered declarations. Local v3 currently accepts `post-create`; portable accepts `post-clone`. | Add `post-release` only to local v3. Preserve all existing event/source meanings and byte compatibility. |
| Hook plans | `internal/service/hook_plan.go` currently accepts only the established create/local and clone/portable combinations and binds plans to source/workspace generations. | Extend the combination explicitly for release/local rather than bypassing plan validation. Add the release name only to the authoritative runtime environment. |
| Hook execution | `internal/service/hook_runner.go` and process helpers own direct argv, bounded output, cancellation, timeouts, process-tree cleanup, and durable setup-hook behavior. | Reuse process execution but do not add a durable retry record for `post-release`. Rerunning an identical lock reruns the complete idempotent event. |
| Clone planning/execution | `internal/service/clone_plan.go`, `clone_execute.go`, `clone_staging.go`, and related safety helpers own private staging, forest ordering, validation, publication, rollback, and recovery. | Materialization should compose these mechanisms with exact revision selection instead of implementing a parallel clone transaction. |
| Git adapter | `internal/git/adapter.go` owns argument-array Git invocation, environment shaping, bounded errors, redaction, and hook/submodule defenses. | Add narrowly typed advertised-ref fetch and detached-checkout operations plus a purpose-specific authenticated network environment. |
| Workspace state and registry | `internal/store` and clone publication record a complete workspace after repositories are verified and published. | Record the adopted base and exact detached children using existing versions unless an actual missing fact proves a schema change necessary. |
| Aggregate execution | `internal/cli/exec.go` and `internal/service/exec.go` preflight every repository and run one direct command in deterministic order with repository context. | A materialized workspace must immediately support `wtree exec`; this is the explicit mechanism for cross-repository CI preparation or tests. |
| CLI conventions | `internal/cli/root.go`, command renderers, and service errors provide versioned JSON, deterministic human output, selectors, exit categories, and redaction. | Follow existing patterns; do not create an oversized release-specific output framework. |

## 5. Authentication gap and boundary

The current adapter's general hardened environment is intentionally too narrow
for ordinary private-repository CI. It retains only basic executable, system,
and temporary-directory variables and overrides user/system Git configuration,
terminal prompting, and askpass. In particular, it does not retain
`SSH_AUTH_SOCK`, `GIT_ASKPASS`, helper-specific secret environment, or normal
credential-helper configuration.

Materialization must introduce a purpose-specific network-operation boundary
that delegates authentication to Git. The minimum useful contract is:

- preserve SSH-agent access through `SSH_AUTH_SOCK`;
- permit a caller-selected `GIT_ASKPASS` helper and the inherited environment
  that helper needs;
- permit caller-configured Git credential helpers;
- force noninteractive behavior with `GIT_TERMINAL_PROMPT=0`; and
- retain explicit argument arrays, URL validation/redaction, disabled checkout
  hooks, and disabled recursive submodules.

Authentication material is process input, never plan or project data. Do not
copy an inherited environment, helper response, token, password, private key,
or credential-bearing URL into JSON, human output, errors, recovery records,
workspace state, manifests, or locks.

Tests need no hosted service. A fake Git executable, askpass helper, or SSH
wrapper can assert that expected channels arrive and that credential canaries
never escape. Platform-specific tests should cover environment and executable
resolution on Windows without inventing a provider integration.

Do not implement token storage, refresh, provider login, SSH key creation,
known-host enrollment, or credential CLI flags. Those would turn a small Git
delegation boundary into a credential product.

## 6. Lock-generation integration notes

- Read and retain the exact portable-manifest bytes before constructing the
  lock digest.
- Use the resolved complete workspace and its persisted repository identities
  and paths; do not infer a second repository graph from disk.
- The base lock path is the only permitted dirty path. Distinguish whether its
  bytes equal base `HEAD`, are absent at `HEAD`, or contain a local candidate
  before deciding whether ordinary replacement or `--force` applies.
- Build the complete canonical bytes before mutation and reuse established
  atomic-file helpers and target-identity protections.
- Byte-identical success must avoid a rewrite but still run the hook, providing
  the simple retry mechanism after a prior hook failure.
- Release-hook executable preflight occurs before lock publication. Actual
  trusted code runs only after publication and after releasing the project
  mutation lock.
- A hook failure never turns a valid lock back into a failed core mutation and
  never rolls back earlier tags or other external effects.

## 7. Materialization integration notes

The caller has already checked out the desired base branch, tag, or commit. The
tracked portable manifest and lock at that commit are the complete input.
Materialization must not clone, move, detach, rewrite, or otherwise manage the
base repository.

For every non-base repository:

1. initialize and configure it in private staging using the portable source;
2. authenticate through Git's caller-provided noninteractive mechanisms;
3. fetch explicit advertised head and tag refspecs;
4. require the locked object to exist and be a commit;
5. check it out detached without hooks or recursive submodules; and
6. apply the existing identity, mount, ignore, cleanliness, and publication
   validation.

Fetching advertised refs rather than requesting the object ID directly makes
remote publication meaningful. A release author normally makes each child
commit advertised by pushing its matching release tag before publishing the
base release tag. If the object remains unavailable, failure is correct.

Stage and validate all children before publishing any final child mount. Reuse
existing ownership receipts, rollback, recovery, registry locking, and state
publication. Add release-specific behavior only where revision selection,
adopted-base handling, or authentication actually differs.

Successful materialization already proves the exact initial workspace. A
separate `release verify` command would immediately repeat the same work and is
deferred until a concrete cached/existing-workspace use case exists.

## 8. Hook decisions

`post-release` is deliberately local and trusted. The tutorial declares the
same `tag-wtree-release` executable once for each non-base repository. Each
invocation receives the exact release name, repository ID, repository path, and
observed HEAD and runs with that repository as its working directory.

The example program creates an annotated tag at the supplied HEAD, succeeds if
the existing tag peels to that commit, and rejects a collision. It does not tag
the base or push. The user then explicitly runs:

```sh
git add project.wtree.lock.yml
git commit -m "chore: lock release v1.4.0"
git tag -a v1.4.0 -m "Release v1.4.0"
```

The name `post-release` means post lock-generation in this contract; it runs
before the base release commit and tag. User documentation must say so plainly.

There is no `post-materialize` hook. In CI, the next pipeline command is clearer
and equally capable. `wtree exec` supplies deterministic repository iteration
when the same command belongs in every repository. If a future one-command
setup requirement emerges, evaluate reuse of the existing portable
`post-clone --run-hooks` trust model rather than automatically adding another
event.

## 9. Scope traps to avoid

- Do not restore a standalone verification command merely because verification
  is useful internally; materialization success already contains that proof.
- Do not add provider-specific authentication or secret persistence.
- Do not make `wtree` create the base commit/tag, push refs, sign artifacts, or
  coordinate publication.
- Do not require `--force` for the ordinary replacement of a clean tracked
  prior-release lock.
- Do not add an atomic cross-repository observation claim.
- Do not execute portable or shared hooks during materialization.
- Do not duplicate clone safety, transaction, or output abstractions.
- Do not broaden the delivery into existing-workspace conversion, lock-aware
  update, status/doctor integration, submodules, Git LFS policy, artifact
  provenance, or deployment.

## 10. Required end-to-end evidence

The executable tutorial is part of the feature, not optional polish. It must
show the ordinary user journey with a base and at least two non-base
repositories:

1. create and inspect a lock;
2. create idempotent child tags through local `post-release` declarations;
3. review, add, and commit the lock and tag the resulting base commit;
4. publish children before the base tag;
5. check out the base release as CI would;
6. authenticate through a fake noninteractive Git channel and materialize exact
   detached children;
7. run explicit subsequent CI work, including one `wtree exec` example; and
8. prove tag collisions, unavailable objects, authentication failures, and
   secret redaction without external network or real credentials.

The documentation must describe this as reproducible source acquisition. It
must not imply atomic tagging, credential management, release verification as a
separate command, implicit build actions, or a post-materialization hook.
