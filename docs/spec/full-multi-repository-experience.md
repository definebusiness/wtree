# Full multi-repository experience capability specification

Status: initial
Source idea: none (created directly)
Source material: [Full-experience suggestions](../ideas/claude-opus-4.8-suggestions-for-full-experience.md); [Prioritized capability list](../ideas/claude-opus-4.8-prioritized-list.md)
Implementation plan: none

## 1. Purpose

This specification consolidates the capability gaps between `wtree`'s
independent-repository model and the complete day-to-day experience that users
expect from Git submodules and subtrees. It defines a coherent capability set,
its priority order, and the contracts shared by those capabilities.

The intended outcome is that a user can acquire, inspect, update, operate,
freeze, and selectively materialize a composed project without introducing
Git gitlinks, `.gitmodules`, merged histories, or path-derived repository
identity.

This is a capability specification, not an implementation plan. The priority
groups express product dependencies and delivery order. They are not plan
milestones and do not authorize implementation by themselves.

## 2. Relationship to existing contracts

The following documents remain authoritative for behavior they already
define:

- [`wtree.spec.md`](wtree.spec.md) defines project, repository, workspace,
  resolver, transaction, locking, recovery, output, and error behavior.
- [Portable manifest clone specification](portable-manifest-clone.md) defines
  portable manifest publication and complete project reconstruction.
- [Portable manifest v2 base-repository format specification](portable-manifest-v2-base-repository-format.md)
  defines the target portable manifest version and base-repository field.
- [Nested mount ignore management specification](nested-mount-ignore-management.md)
  defines immediate-parent committed-ignore safety.

The [release lock idea](../ideas/release-lock-manifests.md) and
[missing-branch idea](../ideas/allowing-missing-branches.md) remain focused
design inputs. Their lifecycle is not advanced by this consolidation. Release
lock schemas and mixed or partial workspace state schemas require dedicated
specifications before those portions of this document may be planned for
implementation.

When this document is more specific about a newly introduced command or its
integration with an existing command, this document governs that new surface.
It does not weaken an existing safety rule.

## 3. System-wide invariants

Every capability in this specification must preserve these invariants:

1. Every repository remains an ordinary independent Git repository. `wtree`
   must not create `.gitmodules`, gitlinks, or merged child history.
2. Repository IDs and recorded Git identity are authoritative. A command must
   not infer repository identity from a directory name, mount, or URL alone.
3. Portable data must not contain machine-local paths, credentials, tokens,
   worktree administration paths, or implicit machine state.
4. Every multi-repository mutation must complete preflight before mutation,
   use an immutable plan, execute in deterministic dependency order, and
   either commit its complete state transition or roll back owned changes.
5. Rollback may remove only artifacts proven to have been created by the
   current operation. Incomplete rollback must create recovery evidence and
   return the established rollback-incomplete error.
6. Default branch operations must not silently substitute another branch,
   create a missing branch, or replace a pinned revision with a moving tip.
7. Every new command must provide stable JSON output. Every new mutating
   command must provide `--dry-run`; JSON mode must not emit human progress on
   stderr.
8. Parent-child operations use deterministic topological order. Setup uses
   parent-first order and teardown uses child-first order unless the command
   explicitly documents a safe alternative.
9. Credentials must be obtained through existing Git mechanisms or explicit
   local configuration and must be redacted from plans, state, recovery data,
   and diagnostics.

## 4. Shared planning and reporting contract

Every aggregate command must resolve the project and workspace through the
central resolver and produce one per-repository result keyed by repository ID.
The result must include the resolved checkout path, effective mount, actual or
planned branch, and exact commit when those fields apply.

Planning failures identify the repository and failed check and occur before
mutation. Execution failures preserve successful and failed per-repository
results, report rollback status when applicable, and return non-zero.

`--dry-run` performs all safe local validation and remote observation needed
to make the plan useful. It does not create directories, refs, locks, state,
or recovery records. A remote-dependent dry-run may contact declared remotes.

Arrays in JSON are deterministic. An aggregate command returns non-zero when
any required repository fails, even when it continues collecting results from
other repositories.

## 5. P0: composition lifecycle

### 5.1 `wtree update`

`wtree update` is the canonical command for reconciling an existing checkout
with its portable manifest source. The name `sync` is reserved and is not an
alias in this specification.

The initial command surface is:

```text
wtree update
  [--project <directory-or-.wtree.yml>]
  [--from <manifest-source>]
  [--dry-run]
  [--json]
  [--verbose]
```

The command uses the stored `manifest.source` unless `--from` is supplied. A
replacement source is persisted only after the complete update succeeds.

Planning must compare the current portable project definition, the candidate
manifest, persisted workspace state, registry identity, and actual checkouts.
It classifies each repository as unchanged, fast-forwardable, newly added,
moved, removed from the manifest, locally dirty, divergent, missing, or
structurally inconsistent.

The default update may:

- fetch and fast-forward configured default branches to the exact commits
  captured by the plan;
- clone newly declared repositories into staging and publish them at their
  verified mounts;
- relocate a clean unchanged-identity checkout when the manifest changes its
  mount and the move is safe on the same filesystem; and
- atomically publish matching configuration, workspace state, and registry
  data after repository verification.

The default update must not:

- delete a repository removed from the manifest;
- overwrite local changes, untracked files, divergent branches, or an
  occupied target mount;
- change a repository's identity merely because a URL or mount matches; or
- update generated non-default workspaces implicitly.

A removed repository is reported as retained and unmanaged. Cleanup requires
a separately specified explicit destructive operation. A move that cannot be
performed and rolled back safely fails preflight.

Update reuses clone's exact-commit, upstream, initial-commit, ignore, tracked
manifest, URL, and submodule verification. Newly added repositories are
staged before publication. Existing-checkout changes use an operation journal,
byte-for-byte metadata backups, and reverse-order rollback.

### 5.2 Portable acquisition baseline

`wtree init` and `wtree clone` are the acquisition baseline for every later
capability. This document adds no alternate project model and does not weaken
their published contracts. P0 cannot be considered complete unless init and
clone work with the then-current portable manifest version and topology.

### 5.3 Extended `doctor` coverage

`wtree doctor` must distinguish intentional state from drift and add checks
for:

- repositories declared by the manifest but absent on disk;
- repositories present in workspace state or at known mounts but absent from
  the manifest;
- repository identity, URL, upstream, branch, and mount disagreement;
- missing immediate-parent committed `.gitignore` coverage;
- unresolved update or hook recovery/incomplete-operation records; and
- configuration for locks, hooks, selections, transport modes, and URL
  profiles as those features become available.

`doctor` is observational by default. `doctor --fix` may repair only state
already classified as safe by its governing specification. It must not clone,
delete, move, fast-forward, or execute hooks as an inferred repair.

## 6. P1: daily aggregate operations

### 6.1 `wtree exec`

The cross-repository execution command is named `wtree exec`; `foreach` is not
introduced as a second spelling.

```text
wtree exec
  [--project <directory-or-.wtree.yml>]
  [--workspace <name>]
  [--reverse]
  [--dry-run]
  [--json]
  -- <executable> [argument...]
```

The executable is invoked directly with an argument array in each present
repository. `wtree` must not insert a shell. Users who require shell syntax
must explicitly invoke a shell as the executable.

The default order is parent-first and `--reverse` is child-first. The command
runs all selected present repositories, captures each exit status, and returns
non-zero if any invocation fails. It exports stable `WTREE_PROJECT_ID`,
`WTREE_WORKSPACE`, `WTREE_REPOSITORY_ID`, `WTREE_MOUNT`, `WTREE_PATH`,
`WTREE_BRANCH`, and `WTREE_COMMIT` values derived from persisted state and
verified runtime facts.

`exec` is an explicitly user-authorized arbitrary command runner. The
transaction and rollback guarantee does not extend to side effects of the
invoked program. Dry-run lists invocations and environment facts without
starting the executable.

### 6.2 `wtree fetch` and drift-aware `status`

`wtree fetch` invokes Git fetch for every present repository without changing
checked-out branches or worktree files. Fetch changes remote-tracking refs and
Git metadata, so it is not described as read-only even though it does not
advance local branches.

Fetch uses configured remotes, deterministic repository order, the hardened
non-interactive Git environment, and per-repository results. A failure in one
repository does not roll back already fetched remote-tracking refs, but the
overall command fails and reports every observed result. Dry-run uses remote
advertisement inspection without updating refs.

`wtree status` must add:

- per-repository upstream, ahead, behind, and divergence information;
- manifest-vs-state and manifest-vs-disk repository-set drift;
- expected-vs-actual identity, mount, branch, and commit facts;
- workspace kind once mixed, partial, or locked workspaces exist; and
- intentionally omitted repositories separately from missing repositories.

Status remains observational and must not fetch implicitly.

### 6.3 Push readiness

The first coordinated push surface is a readiness check, not a publisher:

```text
wtree push [--project <directory-or-.wtree.yml>]
  [--workspace <name>] [--json]
```

It compares each present checkout with its configured upstream and reports
unpushed commits, behind or diverged branches, missing upstreams, detached
checkouts, dirty state, and commits referenced by project metadata that are
not proven available from the configured remote. It succeeds only when the
complete workspace is remotely available and has no blocking finding.

This command must not invoke `git push`, create refs, or publish tags. A future
multi-repository publishing command requires its own specification with
explicit remote mutation, partial-publication, retry, and recovery semantics.

## 7. P2: reproducibility and heterogeneous workspaces

### 7.1 Release lock integration

Release locks must remain separate from the moving `project.wtree.yml`.
The stable default filename is `project.wtree.lock.yml`; alternative archival
locations may be added later without changing the default.

A focused release-lock specification must, at minimum:

- use a strict, deterministic, object-format-neutral schema;
- bind project and repository IDs, topology, effective mounts, clone sources,
  repository identity, and every non-base repository's full object ID;
- bind the exact portable manifest content or its cryptographic digest;
- use an annotated base-repository tag as the base commit anchor so the lock
  does not contain a circular self-reference;
- keep lock generation local and non-publishing;
- provide a separate remote-availability verification step required by the
  documented pre-publication workflow; and
- define locked materialization for clone and update without substituting
  branch tips or creating ordinary development branches.

`wtree status`, `doctor`, and JSON must identify a locked workspace and report
lock-vs-checkout mismatches. Lock generation, tag creation, tag signing,
tagging child repositories, pushing, and materialization are separate actions;
none may occur as an implicit side effect of another.

### 7.2 Mixed and partial workspace integration

Strict all-repository branch synchronization remains the default. Ordinary
`checkout` must first materialize an unambiguous configured remote-tracking
branch when no local branch exists. Ambiguous remote candidates fail before
mutation.

Genuinely missing branches may be handled only through explicit policies
defined by a focused workspace specification. Per-repository fallback mapping
is the first mixed-workspace policy. Creating missing branches and omitting
repositories are distinct operations and must not share a vague
`--allow-missing` contract.

Workspace state and every inspection command must represent
`synchronized`, `mixed`, `partial`, and `locked` kinds explicitly. Each
checkout records branch provenance as existing local, materialized tracking,
fallback, newly created, locked detached, or omitted. Removal and branch
cleanup must respect that provenance and must never delete a shared fallback
or pre-existing branch.

## 8. P3: adoption and setup

### 8.1 Migration analysis

Migration starts with dry-run-first analysis:

```text
wtree adopt --from-submodules
wtree adopt --from-subtree --prefix <path>
```

Submodule analysis reads `.gitmodules`, gitlinks, checked-out commits, URLs,
branches when discoverable, and mount relationships. It produces a proposed
repository mapping, portable manifest, and immediate-parent ignore changes.
It must not silently remove gitlinks, rewrite history, or claim that the
project is converted while submodule metadata remains committed.

Subtree analysis requires a prefix and accepts discovered upstream metadata
only when unambiguous. When history does not prove the upstream, split point,
or repository identity, the command requests explicit values rather than
guessing.

The initial adoption commands are analyzers and artifact generators. Applying
their result to a live repository, including removal of submodule metadata or
extraction of subtree history, requires a separately specified explicit
mutation workflow.

### 8.2 Lifecycle hooks

Portable configuration may eventually declare project-level and per-repository
`post-clone`, `post-checkout`, and `post-update` hooks. Hooks must be argument
arrays, not implicit shell strings. Their working directory, order, sanitized
environment, timeout behavior, and output are part of the portable contract.

Merely obtaining a remote manifest is not consent to execute its code. A
clone, checkout, or update runs declared hooks only with an explicit
`--run-hooks` authorization. Dry-run always lists hooks and never executes
them.

Hooks run after the core `wtree` transaction commits. Their arbitrary side
effects are outside core rollback guarantees. A failure must report that the
project operation succeeded but setup is incomplete, return non-zero, record
the failed hook and repository, and make a safe explicit retry possible. Hooks
must never run during rollback.

## 9. P4: scale and transport flexibility

### 9.1 Selective materialization

Clone and update may accept explicit repository-ID selections only after the
partial-workspace state contract exists. A valid selection is ancestor-closed:
including a nested repository includes every configured parent needed to own
its mount. This avoids fabricating directories inside an omitted parent
checkout.

Omitted repositories are persisted as intentional omissions and are visible
in status, list, repository lookup, doctor, exec, fetch, push readiness, and
JSON. Commands must not treat them as drift or silently materialize them.

Blob-filtered partial clone and shallow history are transport policies, not
repository identity policies. Each checkout records its transport mode.
Partial clone must retain normal commit identity verification. Shallow history
may relax initial-commit verification only through a separately specified,
explicitly selected mode; status and doctor must report that identity
verification is incomplete rather than presenting the checkout as fully
verified.

LFS orchestration is deferred until hooks or a dedicated transport contract
can provide explicit execution, failure, and retry semantics.

### 9.2 URL resolution profiles

Portable manifests remain credential-free and reviewable. Named URL profiles
are machine-local mappings that transform an accepted canonical repository URL
to an alternate transport base for clone, fetch, update, lock verification,
and adoption.

Profile resolution must be deterministic, selected explicitly, and shown in
dry-run with credentials redacted. The resolved URL passes the same scheme,
userinfo, control-character, path, and credential validation as a manifest
URL. Persisted portable configuration retains the canonical URL; local state
may record the selected profile name but not embedded credentials.

Relative repository URLs may be resolved only against a well-defined portable
manifest source URL or an explicit profile base. Filesystem-relative behavior
must not depend on the caller's current directory.

## 10. Open questions and compatibility conflicts

The following questions must be resolved before the affected capability is
included in an implementation plan. Until then, existing command behavior,
persisted-state compatibility, and output contracts take precedence over the
new behavior proposed by this specification.

### 10.1 Checkout and remote branch materialization

Existing `wtree checkout` restores retained state or checks out a local branch
that already exists in every repository. It never creates branches, and a
missing local branch causes transactional preflight failure. Section 7.2 would
instead require ordinary checkout to create a local tracking branch when an
unambiguous remote branch exists. That is a direct behavior change for users
and automation that rely on missing-branch failure.

Open questions:

- Should remote materialization require an explicit flag or a separately
  named command so ordinary checkout remains local-branch-only?
- If it remains part of ordinary checkout, how will callers explicitly retain
  the old fail-on-missing behavior?
- How will the plan distinguish an existing local branch from a tracking
  branch that it will create, and how will rollback remove only a branch
  created by the failed operation?

No implementation may make ordinary checkout create a branch until these
questions are resolved in a focused workspace specification.

### 10.2 Existing-repository relocation and Git identity

Section 5.1 permits update to relocate an existing clean checkout after a
manifest mount change while also saying that generated non-default workspaces
are not updated implicitly. The current repository identity and registry model
uses the absolute common Git directory. Moving a source repository can change
that identity and can invalidate the administrative pointers of every linked
Git worktree that shares it.

Open questions:

- Should the first update implementation report existing-repository mount
  changes without applying them?
- If relocation is supported, must it be a separately authorized migration
  rather than ordinary update behavior?
- Must relocation be rejected whenever linked worktrees exist, or must the
  transaction repair and verify every affected worktree, state record,
  configuration path, and registry identity?
- What stable identity proves that a repository before and after relocation is
  the same repository when its common Git directory changes?

Until those questions are resolved, update may place newly added repositories
at their declared mounts but must not relocate an existing repository.

### 10.3 Workspace-state schema compatibility

Current workspace state is a strict version-one contract. It already represents
partial workspaces with `partial` and `missingRepositoryIds` and rejects unknown
fields and unsupported versions. Sections 7.1, 7.2, and 9.1 require workspace
kind, branch provenance, locked revisions, omissions, and transport modes that
cannot all be represented by that schema.

Open questions:

- Does the expanded model require workspace state version two?
- How are all existing version-one synchronized and partial states migrated
  without losing mounts, paths, branches, HEADs, or omission information?
- Must new binaries continue to read version-one state, and what diagnostic
  should an older binary give for version-two state?
- Are `partial` and `missingRepositoryIds` retained for compatibility, derived
  from the new kind, or replaced only at a version boundary?

The new fields must not be written into version-one state, and an implementing
plan must include migration and backward-compatibility tests.

### 10.4 Status exit codes and output compatibility

Current status behavior reports dirtiness and structural drift as successful
observations. Its default human table and JSON field names are established
interfaces. The shared aggregate rule in section 4 could instead be read to
require a non-zero exit whenever a repository has a finding, while sections
6.2 and 7.2 require additional workspace and repository information.

Open questions:

- Does status continue to return zero whenever observation succeeds, regardless
  of reported drift, and reserve non-zero for argument, resolution, I/O, or Git
  observation failures?
- Are new JSON fields explicitly additive, or does the output require its own
  schema version?
- How can workspace kind and manifest drift be exposed without changing the
  existing default human table for synchronized workspaces?
- Which fields belong to status, and which checks remain exclusive to doctor?

Until resolved otherwise, status findings must not change the existing exit
semantics, existing JSON fields must not be removed or retyped, and the default
human rendering for existing synchronized workspaces must remain unchanged.

### 10.5 Portable manifest and local configuration evolution

The current portable manifest is a strict version-two schema. Existing binaries
reject unknown fields, unsupported versions, and relative clone URLs. Portable
hook declarations and new URL-resolution data or semantics therefore cannot be
added silently while claiming the same schema contract. Machine-local profile
selection may likewise require a versioned local or global configuration
change.

Open questions:

- Do portable hooks, relative URLs, or other portable additions require a
  manifest version three?
- Which URL-profile data is portable, local-project configuration, or global
  configuration, and which schema owns each field?
- Is compatibility based on a version transition, explicit feature
  negotiation, or separate companion files?
- How do older binaries fail clearly without partially interpreting a newer
  manifest or configuration?

An implementation must not give the same manifest or configuration version two
different schemas or meanings.

### 10.6 Existing partial-workspace behavior

Partial workspaces already exist through explicit import, are shown by list and
status, and are rejected by checkout and removal where a complete repository
mapping is required. The proposed generalized partial and mixed models must not
silently remove those existing capabilities or relax those refusals.

Open questions:

- Does the existing imported-partial representation become one case of the new
  `partial` workspace kind without changing its current JSON contract?
- Which commands become safe for partial workspaces, and which continue to
  reject them?
- What explicit option authorizes any behavior that currently fails because a
  workspace is partial, mixed, detached, or branch-divergent?
- How are intentionally omitted repositories distinguished from repositories
  that were expected at import time but are now missing?

Existing partial import, list, status, doctor, and safety refusals must remain
functional until a focused specification explicitly defines compatible
extensions.

### 10.7 Compatibility resolution gate

Before implementation begins for any section above, the applicable focused
specification and implementation plan must:

1. identify the existing CLI, JSON, persisted-state, manifest, registry, and
   Git-worktree contracts it touches;
2. state whether every change is additive, explicitly opt-in, migrated, or a
   deliberate versioned break;
3. include regression tests proving unaffected existing workflows continue to
   work; and
4. include migration, rollback, mixed-version, and older-client diagnostics
   wherever stored or portable formats change.

## 11. Delivery order and dependency gates

Delivery must follow this dependency order unless a later implementation plan
documents a stricter order:

| Priority | Capability group | Entry gate |
|---|---|---|
| P0 | update and doctor drift | current init, clone, manifest, ignore, transaction, and recovery contracts are implemented |
| P1 | exec, fetch/status, push readiness | central resolution and P0 drift classification are available |
| P2 | release locks and heterogeneous workspaces | focused lock and workspace-state specifications are planned |
| P3 | migration and hooks | P0 reconciliation exists; hook trust and retry contracts are specified |
| P4 | selection, transport modes, URL profiles | partial state and profile schemas are specified and doctor can diagnose them |

An implementation plan may split these capabilities across multiple plans.
It must not mark this specification implemented until every capability in
scope is delivered, reviewed, and verified, or until an explicit user action
supersedes or narrows this specification through the documented lifecycle.

## 12. Required verification

Each implementing plan must provide focused unit, integration, contract, and
process-boundary tests appropriate to its command surface. Across the complete
specification, evidence must cover:

- strict preflight with zero mutation on invalid aggregate operations;
- deterministic ordering and JSON for every new command;
- exact-commit planning and branch-movement resistance;
- rollback after failure at every owned mutation boundary;
- incomplete rollback recovery records and doctor visibility;
- dirty, divergent, detached, missing, omitted, mixed, and locked states;
- credential redaction and rejection of unsafe URL forms;
- nested repository ordering and immediate-parent ignore enforcement;
- no implicit shell execution, hooks, push, tag, branch creation, deletion,
  or fallback; and
- end-to-end acquisition, update, aggregate operation, release verification,
  and selective materialization for a nested multi-repository project.

## 13. Explicit non-goals

This specification does not authorize:

- changing the independent-repository architecture;
- silently adopting existing destinations during clone;
- automatic deletion of repositories removed from a manifest;
- implicit publishing of commits, branches, or tags;
- automatic execution of code from an untrusted manifest;
- history rewriting as part of migration analysis;
- guessing repository identity, subtree provenance, fallback branches, or URL
  profile selection; or
- weakening existing transaction, recovery, JSON, credential, or ignore
  guarantees for convenience.
