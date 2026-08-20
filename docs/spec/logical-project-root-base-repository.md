# Logical project root and repository forest specification

Status: initial
Source idea: [Logical project roots with a designated base repository](../ideas/logical-project-root-base-repository.md)
Implementation plan: none
Implemented prerequisite: [Portable manifest v2 base-repository format specification](portable-manifest-v2-base-repository-format.md) and its [implementation plan](../plans/portable-manifest-v2-base-repository-format.md)

## 1. Purpose and scope

This specification makes a plain directory a first-class project and workspace
root. A project may contain multiple top-level Git repositories and arbitrary
nested repositories. Exactly one top-level repository is the **base
repository** that owns the tracked portable manifest and machine-local project
configuration.

This specification is a follow-on to the implemented portable-manifest v2
work. It does not re-specify or re-authorize that completed scope. In
particular, portable manifest version 2, the required
`project.base_repository` field, canonical field ordering, sorted repository
and initial-commit data, rejection of portable manifest v1, and exact tracked
manifest byte verification are already established.

When implemented, this specification replaces only the single-root topology
restrictions and corresponding deferrals in sections 4, 5, and 7 of the
[portable manifest v2 specification](portable-manifest-v2-base-repository-format.md).
All other portable-manifest, clone, identity, transaction, security, and Git
contracts in that document, the
[portable manifest clone specification](portable-manifest-clone.md), and
[`wtree` specification](wtree.spec.md) continue to apply.

## 2. Goals and non-goals

### 2.1 Goals

- Permit a non-Git logical project root containing one or more top-level Git
  repositories.
- Preserve the existing root-Git layout as the one-top-level-repository case
  mounted at `.`.
- Support sibling top-level repositories and nested repositories together.
- Keep base-repository authority independent of Git parentage.
- Apply one topology model consistently to init, clone, discovery, resolution,
  workspaces, import, inspection, removal, recovery, and registry operations.
- Preserve deterministic planning, identity-bound mutation, rollback, and
  machine-readable output across a repository forest.

### 2.2 Non-goals

- Git submodules, gitlinks, subtree merges, or shared repository history.
- Inferring the base from its directory name, remote, or discovery order.
- Storing project metadata in the plain logical-root directory.
- Supporting more than one base repository.
- Automatically converting an existing local configuration to the new local
  schema.
- Adding manifest source schemes, adopting an existing clone destination, or
  changing clone URL and credential rules.
- Redesigning partial-workspace policy, branch policy, or mount overrides
  beyond making their existing behavior forest-aware.

## 3. Terminology and authority

### 3.1 Logical project root

The logical project root is the filesystem directory that contains a project
checkout. It is the coordinate system for top-level repository mounts and the
destination passed to `wtree clone`. It may be an ordinary directory or the
worktree root of a top-level repository mounted at `.`.

The logical project root is not repository identity and need not contain
`.git`, `.wtree.yml`, or `project.wtree.yml`.

### 3.2 Top-level and nested repositories

A top-level repository has an empty `parent`. Its `mount` is relative to the
logical project root.

A nested repository names its immediate Git parent. Its `mount` is relative
to that parent's checkout. Parent links describe Git containment only.

Together the repositories form a non-empty acyclic forest. Each top-level
repository is one root of that forest.

### 3.3 Base repository

`project.base_repository` names exactly one top-level repository. The base
repository owns:

- tracked `project.wtree.yml` at its checkout root;
- ignored machine-local `.wtree.yml` at its checkout root; and
- the base checkout's committed `/.wtree.yml` ignore rule.

Base ownership grants metadata authority only. It does not make a sibling a
child of the base, change any mount, or make the base the logical project root.

## 4. Portable v2 forest contract

The portable schema and every per-repository field remain those already
defined for version 2. This specification changes the interpretation and
validation of `parent` and top-level `mount`; it adds no portable fields.

Example:

```yaml
version: 2
project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme
  base_repository: api
repositories:
  api:
    clone:
      remote: origin
      url: https://example.test/acme/api.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - 0123456789abcdef0123456789abcdef01234567
    parent: ""
    mount: api
    default_branch: main
  shared:
    clone:
      remote: origin
      url: https://example.test/acme/shared.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - 123456789abcdef0123456789abcdef012345678
    parent: api
    mount: shared
    default_branch: main
  web:
    clone:
      remote: origin
      url: https://example.test/acme/web.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - 23456789abcdef0123456789abcdef0123456789
    parent: ""
    mount: web
    default_branch: main
```

This resolves to:

```text
<logical-root>/
├── api/                 # base repository
│   └── shared/          # child of api
└── web/                 # top-level sibling of api
```

### 4.1 Required invariants

A portable v2 repository forest is valid only when all of these conditions
hold:

1. There is at least one repository and at least one top-level repository.
2. Every non-empty parent names an existing repository, and parent links are
   acyclic.
3. `project.base_repository` names an existing top-level repository.
4. A top-level mount is `.` or a clean, non-absolute, logical-root-relative
   path. A nested mount remains a clean, non-absolute path relative to its
   immediate parent and cannot be `.`.
5. Top-level mounts are unique. No two resolved repository paths are equal.
6. Resolved paths remain inside the logical root and must not enter `.git` or
   any other forbidden Git administration path.
7. Two resolved paths may overlap only when the corresponding repositories
   have the same declared ancestor/descendant relationship. Path containment
   never creates an implicit parent relationship.
8. If a top-level repository uses `mount: .`, it is the only top-level
   repository. All other repositories must be its declared descendants. This
   preserves the traditional root-Git layout without treating nested sibling
   paths as independent forest roots.
9. Filesystem resolution must reject symlink traversal or canonical aliases
   that escape the owning logical root or parent checkout.

The effective path is:

```text
effectivePath(top-level) = logicalRoot + top-level.mount
effectivePath(nested) = effectivePath(parent) + nested.mount
```

Validation and deterministic ordering operate on repository IDs and declared
parent links, never on map iteration or discovery order.

The general parent-first order is increasing forest depth and then lexical
repository ID. Operations that must reach metadata authority first put the
base repository before the other depth-zero repositories, which remain
lexical; all deeper levels remain increasing-depth then lexical. Child-first
order is decreasing depth and then reverse lexical repository ID.

### 4.2 Ignore ownership

Only a declared nested mount has a Git parent that must ignore it. The
immediate parent repository owns that committed ignore rule, using the
contracts in the
[automatic nested mount ignore protection specification](automatic-nested-mount-ignore-protection.md).

A top-level sibling has no Git parent and receives no ignore rule in the plain
logical root or in the base repository. The base repository owns only its
`/.wtree.yml` local-metadata rule plus rules for its declared direct children.

## 5. Local configuration v2

Logical-root support replaces the machine-local `.wtree.yml` schema with
version 2. Version 1 local configuration is rejected with a direct diagnostic
that reinitialization is required; it is not translated or rewritten while
being read.

The base checkout contains the only project `.wtree.yml`. Its relevant shape
is:

```yaml
version: 2
project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme
  base_repository: api
logical_root: ..
repositories:
  api:
    source: api
    parent: ""
    mount: api
    default_branch: main
  shared:
    source: api/shared
    parent: api
    mount: shared
    default_branch: main
  web:
    source: web
    parent: ""
    mount: web
    default_branch: main
manifest:
  path: project.wtree.yml
  source: /srv/manifests/acme/project.wtree.yml
```

`project.base_repository` must agree with the portable manifest.
`logical_root` is a required clean relative path resolved from the directory
containing `.wtree.yml`. It is `.` when the base repository is mounted at `.`;
otherwise it is the exact relative path back to the logical root. The resolved
logical root joined with the base repository's effective source path must
canonicalize to the directory containing `.wtree.yml`. Absolute values,
symlink aliases, and values that do not invert the base checkout's placement
are invalid.

Every `repositories[*].source` is logical-root-relative. Nested `mount` values
remain immediate-parent-relative. `manifest.path` remains exactly
`project.wtree.yml`, but it is base-checkout-relative. A local
`manifest.source` retains the existing absolute local-path or normalized
HTTP(S) rules.

The project ID remains derived from canonical portable repository facts with
constant placeholder project ID and name values. The identity input includes
the selected base and every repository's identity, clone/upstream facts,
parent, and mount. It excludes the logical-root path, common Git directories,
display name, local configuration, and other machine-local state. Therefore
two definitions with the same base repository but different sibling sets or
topology receive different deterministic project IDs.

## 6. Initialization and discovery

### 6.1 `wtree init`

The command surface adds:

```text
wtree init [logical-root] [--base-repository <repository-id>]
```

The positional path, or the current directory when omitted, is the logical
project root. `init` does not guess an unmarked plain ancestor above that path.
It discovers candidate Git worktrees below the logical root using the existing
bounded traversal and ignore controls, identifies their canonical roots and
common Git directories, and derives parent links by actual path containment.

Repositories with no discovered Git ancestor inside the logical root are
top-level. A repository nested inside another discovered repository is a
child of its nearest discovered Git ancestor regardless of which repository
is selected as base.

When discovery finds one top-level repository, omission of
`--base-repository` selects that unambiguous repository. When it finds more
than one, the flag is required and must name a discovered top-level repository.
There is no interactive prompt or discovery-order fallback. Failure lists the
candidate IDs and mounts and performs no mutation.

After complete preflight, init writes `project.wtree.yml` and `.wtree.yml` in
the base checkout, updates only required nested-parent ignore files and the
base `/.wtree.yml` rule, publishes default workspace state rooted at the
logical root, and registers every repository identity. It writes nothing in
the plain logical-root directory and never stages, commits, or pushes.

All existing published-upstream, clone URL override, manifest source,
transaction, concurrency, dry-run, JSON, and retained-ignore-progress rules
remain in force. Dry-run identifies the logical root, base repository,
top-level repositories, resolved paths, metadata targets, and ignore owners.

### 6.2 Runtime discovery and explicit selection

Resolution uses the following evidence in order without treating convenience
as identity:

1. an explicit `.wtree.yml` path or a path inside the base checkout may find
   the base-owned local configuration by ancestor traversal;
2. a path inside any registered repository is matched by canonical common Git
   directory to the registry, then to its project and workspace;
3. a registered logical-root or workspace-root path is matched directly to
   persisted state; and
4. an explicit logical-root path is accepted only when registry/state or a
   base configuration reached by the preceding rules proves the relationship.

The resolver must not recursively scan an arbitrary directory for a plausible
`.wtree.yml`, select an unrelated registered project, or stop merely because
it encounters the nearest Git root. If evidence identifies multiple projects,
resolution remains ambiguous and requires a more specific `--project` value.

From the base configuration, the resolver canonicalizes `logical_root`, then
resolves all project-relative sources from that directory. From a sibling or
nested checkout, it uses registered Git identity and persisted workspace
paths. Thus commands work inside every declared repository even though the
logical root contains no metadata file.

## 7. Forest-aware clone

The existing `wtree clone <manifest-source> [destination]` command and source
types remain unchanged. The destination denotes the logical project root and
must satisfy the existing non-existent-destination and canonical-parent rules.

Planning must validate the complete forest, resolve every remote branch and
exact commit, compute every effective path, reject path conflicts before
mutation, and produce a deterministic parent-first plan. Among otherwise
unordered top-level repositories, the base repository is first and remaining
IDs are lexical; descendants use the same stable topological tie-break.

Execution creates a unique staging logical root in the destination's existing
parent. It clones top-level repositories at their logical-root-relative mounts
and descendants at their immediate-parent-relative mounts. Before a nested
clone, the selected parent commit must contain an effective committed ignore
rule. No such check applies between top-level siblings.

The base checkout's selected commit must track `project.wtree.yml` with bytes
identical to the bytes used for planning. The base checkout must also contain
an effective committed `/.wtree.yml` ignore rule. These checks occur in the
base checkout, not at the logical root. After every checkout passes identity,
upstream, exact-commit, cleanliness, submodule, path, and topology verification,
clone writes local configuration v2 into the staged base checkout.

Publication atomically renames the complete staged logical root when the
existing same-filesystem contract permits it, then publishes default workspace
state and the registry entry while holding the established locks. State uses
the logical root as the workspace path and records every actual checkout path.
The registry points to the base-owned `.wtree.yml` and maps every canonical
common Git directory to its repository ID.

The ownership inventory covers the staging logical root, every repository
checkout, the final logical root created by publication, local configuration,
workspace state, registry generation, and recovery evidence. Cleanup and
rollback may remove only invocation-created, identity-revalidated artifacts.
A failure in any tree rolls back the complete forest or reports the existing
rollback-incomplete contract.

## 8. Forest-aware workspaces

Workspace roots use the same topology as the logical project root. `create`
and `checkout` first create or validate the logical workspace directory, then
materialize every top-level repository and each descendant in deterministic
parent-first order. A root-Git project mounted at `.` retains its existing
layout.

Mount overrides are interpreted relative to the same owner as their defaults:
top-level overrides are logical-workspace-root-relative and nested overrides
are immediate-parent-relative. The complete effective-path set is validated
for containment, equality, overlap, reserved Git paths, canonical aliases, and
symlink traversal before mutation.

Automatic ignore protection applies only to nested repositories and is written
in their immediate parent worktrees. A plain logical workspace root never
receives a generated `.gitignore`.

Workspace state records the logical workspace root in `path` and one checkout
entry per repository with its actual mount and resolved path. The existing
version 1 state shape already expresses those facts and remains version 1.
Forest support changes validation and traversal, not the persisted meaning of
`path`, `mount`, or `resolvedPath` and not the state wire shape.

Removal and deletion operate child-first across every tree, with reverse
lexical repository ID as the tie-break for otherwise unordered nodes. No
top-level checkout or workspace directory is removed until all applicable
descendants pass preflight and are removed. Branch, dirtiness, force,
transaction, and recovery rules remain unchanged.

## 9. Forest-aware import and resolution

Import treats its target as a logical workspace root, whether or not that path
is a Git worktree. It discovers Git worktrees below that root with the bounded
discovery rules, resolves each repository exclusively through common Git
directory identity, and derives actual containment.

The imported set must match the declared forest:

- every top-level checkout is relative to the imported logical root;
- every nested checkout is inside its declared immediate parent;
- path containment that contradicts the declared parent is invalid;
- unknown repositories are reported and never adopted implicitly; and
- missing repositories follow the existing explicit partial-import policy.

Import may begin from a path inside any known top-level or nested repository.
When no target root was supplied, it proceeds only if registered identity,
declared topology, and discovered checkout paths yield exactly one logical
workspace root. Otherwise it requires an explicit target rather than guessing.

The central repository resolver uses persisted workspace mappings for all
later commands. It never reconstructs a sibling path relative to the base
checkout and never interprets base authority as parentage.

## 10. Registry, inspection, and recovery

The registry continues to bind a project to its base configuration path and
all canonical repository identities. The logical root is derived from the
base configuration and corroborated by the default workspace state; it is not
duplicated in the registry. Project registration and conflict checks must
additionally compare canonical logical roots and every top-level checkout path
so aliases cannot register the same checkout or logical root twice.

Registry, workspace state, workspace plan, clone plan, and recovery records
remain at their existing schema versions because their current root path,
repository map or list, resolved path, and ordered-step fields can represent a
forest. This specification does not authorize adding fields or silently
reinterpreting an existing field. A later need for an incompatible wire change
requires a separate versioned specification.

`status`, `path`, `repo path`, `doctor`, project inspection, prune, and
recovery must enumerate every top-level repository before descendants using
the stable ordering rules. They must report the logical root, base repository,
each repository's declared parent, effective mount, resolved checkout path,
and identity or drift where relevant.

Recovery records and rollback reports must identify actions by repository ID,
resolved path, and operation-owned identity. Recovery may never assume one
root worktree, remove a logical-root directory merely because one top-level
repository is gone, or reconstruct a sibling beneath the base.

Project unregistration remains registry/state cleanup, not permission to
delete repositories or the logical root. Filesystem deletion continues to
require the command-specific ownership and destructive-operation contracts.

## 11. Compatibility and transition

Portable manifests remain version 2. Existing valid single-root v2 manifests
remain valid as the `mount: .` one-tree subset. No portable v3 schema or dual
portable parser is introduced by this specification.

Local configuration changes to version 2 as defined in section 5. Existing
version 1 `.wtree.yml` files are rejected after this feature is delivered.
The supported rebuild path is:

1. run the new `wtree init` against the intended logical root;
2. select the base explicitly when multiple top-level repositories exist;
3. review the generated local and portable configuration plus ignore changes;
4. commit the base-owned `project.wtree.yml` and applicable `.gitignore`
   changes; and
5. distribute the committed portable manifest through the existing source
   mechanisms.

There is no automatic conversion, compatibility mode, staging, commit, push,
or inferred deletion in that workflow.

## 12. Required verification

Implementation must include focused unit, integration, race, and public CLI
coverage for at least:

1. a plain logical root with two sibling top-level repositories and a
   non-`.` base;
2. siblings combined with at least three levels of nesting;
3. the traditional root-Git layout remaining valid;
4. explicit base selection, implicit single-candidate selection, and rejection
   of missing, unknown, nested, or ambiguous bases without mutation;
5. local configuration v2 root/base/path resolution and rejection of v1;
6. missing parents, cycles, duplicate/equal/overlapping mounts, `.` combined
   with another top-level root, escapes, reserved Git paths, symlink aliases,
   and containment that lacks the declared ancestry;
7. init writing metadata only in the base and ignore rules only in their
   owning Git repositories;
8. clone planning and execution across multiple roots, including base-first
   deterministic order and exact planned commits;
9. rejection and full cleanup when the base checkout lacks the tracked
   manifest, contains different manifest bytes, or does not ignore
   `/.wtree.yml`;
10. workspace create, checkout, status, path resolution, remove, and delete
    across the forest;
11. import from the logical root and from inside each top-level tree, including
    identity mismatch, topology mismatch, and ambiguous-root rejection;
12. registry collision, concurrent publication, cancellation, rollback, and
    recovery after partial work in different top-level trees; and
13. deterministic project identity differing when sibling membership,
    parentage, mount, or selected base differs.

All mutating tests must prove complete preflight or exact owned rollback. Tests
must use temporary repositories and local remotes and must not require network
access or credentials.

## 13. Documentation requirements

The implementation must update current README examples, CLI help, how-to and
tutorial material, core topology descriptions, portable-manifest guidance,
traceability, JSON examples, and recovery documentation so they consistently
distinguish:

- the logical project or workspace root;
- top-level and nested repositories;
- Git parentage;
- base metadata authority; and
- base-relative metadata paths versus logical-root-relative project paths.

Historical execution logs remain historical and must not be rewritten to
claim they implemented this forest scope.
