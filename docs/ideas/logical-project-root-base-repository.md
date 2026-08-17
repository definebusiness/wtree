# Idea: logical project roots with a designated base repository

Status: specified
Specification: [Portable manifest v2 base-repository format](../spec/portable-manifest-v2-base-repository-format.md)

## Summary

`wtree` should support a project whose checkout root is a plain directory,
rather than requiring that directory itself to be a Git repository. Such a
logical project root can contain several sibling repositories, one of which is
the designated **base repository**. The base repository is the project's Git
and metadata authority; it does not have to be the filesystem root or the
parent of every other repository.

This makes the following layout a first-class project:

```text
acme/
├── api/                 # designated base repository
│   └── shared/          # nested repository
├── web/                 # sibling repository
└── tools/               # sibling repository
```

The design retains the existing nested-repository model. A project can mix
top-level sibling repositories and arbitrary-depth nested repositories. The
relationship that makes a repository the base is deliberately separate from
the relationship that makes one repository a Git parent of another.

Backward compatibility with existing portable manifests is not required. The
tool is early enough that a deliberate, single manifest-format replacement is
preferable to carrying a compatibility mode or ambiguous migration behavior.

## Goals

- Permit a non-Git logical project root containing one or more top-level Git
  repositories.
- Require exactly one repository to be the base repository.
- Keep repository identity independent of checkout paths.
- Preserve nested repository support at every depth beneath any repository.
- Give one repository a well-defined place to track the portable manifest and
  associated project metadata.
- Make `init`, `clone`, discovery, import, resolution, workspace operations,
  and recovery use the same topology rules.
- Replace the manifest schema outright instead of accepting older manifests.

## Non-goals

- Supporting both the previous one-root-repository manifest and the new
  schema at runtime.
- Making sibling repositories into Git submodules or linking their histories.
- Selecting the base repository implicitly from a directory name, Git remote,
  or discovery order.
- Requiring every top-level sibling to be a descendant of the base repository.

## Terminology

### Logical project root

The filesystem directory that contains the project checkout. It may be a Git
repository or an ordinary directory. It is the coordinate system for
top-level repository mounts and for the destination passed to `wtree clone`.

### Top-level repository

A project repository whose `parent` is empty. Its `mount` is relative to the
logical project root. A project has one or more top-level repositories.

### Nested repository

A project repository whose `parent` names another project repository. Its
`mount` is relative to that parent's checkout, as today.

### Base repository

The one repository selected by `project.base_repository`. It owns the tracked
portable manifest and the ignored local configuration. It may be a
top-level repository at a non-`.` mount, or it may be the repository mounted
at `.` when the logical project root is itself a Git repository.

Being the base repository grants metadata authority only. It does not make
all sibling repositories children of the base repository.

## Proposed portable manifest shape

The next manifest format should express the logical-root and base-repository
model explicitly. The exact version number is a specification decision; this
idea uses `version: 2` to make the intentional format break visible.

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
      initial_commits: ["..."]
    parent: ""
    mount: api
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
      initial_commits: ["..."]
    parent: ""
    mount: web
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
      initial_commits: ["..."]
    parent: api
    mount: shared
    default_branch: main
```

The ordinary nested-root case remains naturally expressible without a special
compatibility format:

```yaml
version: 2
project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme
  base_repository: root
repositories:
  root:
    parent: ""
    mount: .
    # clone, upstream, identity, and default_branch omitted here
```

## Required invariants

The future specification should require all of the following:

1. `project.base_repository` names an existing repository.
2. At least one repository has an empty `parent`; more than one is allowed.
3. Repository parent links form an acyclic forest, not necessarily a single
   rooted tree.
4. A top-level repository mount is a clean, unique path relative to the
   logical project root. `.` is permitted only when that top-level repository
   is actually checked out at the logical root.
5. A nested repository mount is a clean, unique path relative to its immediate
   Git parent and cannot escape it.
6. No resolved repository path may overlap another except through a declared
   ancestor/descendant relationship.
7. The base repository must be top-level. This keeps the location and Git
   ownership of the manifest unambiguous.
8. `project.wtree.yml` is tracked in the base repository. `.wtree.yml` is
   machine-local and ignored by that same repository.
9. Clone verifies that the manifest bytes tracked by the base repository at
   the selected commit are exactly the bytes used for planning.
10. Ignore rules apply only to declared nested mounts in their immediate Git
    parent. Top-level sibling mounts cannot be represented by a `.gitignore`
    rule in a non-Git logical root.

## Required implementation changes

### Manifest and configuration model

- Replace the current single-root validation with forest validation and the
  explicit base-repository field.
- Replace the current requirement that the only parentless repository has
  `mount: .`.
- Define one canonical, deterministic manifest rendering order that includes
  `project.base_repository`.
- Update local configuration so its manifest path resolves relative to the
  base repository while project-relative paths resolve relative to the logical
  project root.
- Update project and registry identity rules to distinguish the logical root,
  base-repository identity, and per-repository Git identities.

### Discovery and initialization

- Discover a project from either an explicit logical-root directory or a
  checkout inside one of its repositories. Discovery must no longer stop at
  the nearest enclosing Git root.
- Identify all candidate repositories under the logical root, then derive
  their containment forest by path containment and Git identity.
- Require an explicit base selection when discovery finds more than one
  top-level repository. A flag such as `wtree init --base-repository api` is a
  possible interface; the specification should settle the exact command.
- Treat the existing root-Git layout as the case where the root checkout is
  selected as the base repository, not as a separate model.
- Write `project.wtree.yml`, `.wtree.yml`, and any necessary `.gitignore`
  update in the base repository. Do not create a metadata file in the plain
  logical-root directory.

### Clone and transaction safety

- Make `wtree clone` create the logical destination first, then clone all
  top-level repositories beneath it and descendants parent-first.
- Locate and verify the tracked portable manifest through the base checkout,
  rather than assuming it is at destination root.
- Extend planning, ownership inventories, publication, cleanup, and rollback
  to cover a forest of top-level clone roots. Every mutation must remain
  identity-bound and confined to paths created by that invocation.
- Register a default workspace only after every top-level and nested checkout,
  and the base-owned local configuration, has passed verification.

### Workspace, import, and resolution behavior

- Build workspaces by creating a directory for the logical workspace root,
  materializing every top-level repository there, and then materializing each
  repository's descendants.
- Resolve an explicit project path from the base repository's local
  configuration and map it back to the logical root without relying on a Git
  repository at `.`.
- Update import validation to accept paths in any top-level repository and to
  prove membership using the declared forest, not a single configured root.
- Update status, remove, prune, recovery, and registry operations to enumerate
  all top-level repositories before processing descendants.

### Documentation and tests

- Revise the core specification, portable manifest specification, README,
  tutorial, CLI help, examples, and traceability material to use the new
  model consistently.
- Add integration coverage for: a plain logical root with sibling repositories;
  a mix of siblings and deep nesting; base selection; base-manifest byte
  identity; workspace creation; import; clone rollback; and recovery after
  partial publication.
- Add negative tests for missing or non-top-level base repositories, multiple
  or overlapping top-level mounts, cycles, an untracked base manifest, and a
  manifest whose selected base checkout does not contain the planned bytes.

## Intentional breaking changes

This is an intentional manifest-contract break, not a migration feature.

- Existing `version: 1` portable manifests and local configuration files are
  rejected with a direct diagnostic that this build requires the logical-root
  manifest format.
- The `repositories` graph changes from exactly one root to a forest, and the
  required `project.base_repository` field replaces the implicit root as the
  metadata authority.
- `project.wtree.yml` and `.wtree.yml` move from the assumed checkout root to
  the base repository when the logical root is not itself a Git repository.
- Existing `wtree init`, `clone`, `import`, and explicit-project path behavior
  that assume a Git repository at the project directory must be revised.
- Scripts and CI that read manifests directly must generate or consume the new
  schema. There is no automatic conversion and no dual-parser period.

The user-facing release notes should give a concise rebuild path: recreate the
project metadata with the new `wtree init` flow, select the base repository,
review and commit the new manifest in that repository, then distribute that
manifest. Since the tool has no compatibility commitment yet, this avoids the
risk of silently translating incomplete topology information.

## Compatibility and risk assessment

The new design should not weaken the existing capabilities after they are
implemented against the revised schema:

- A traditional root Git repository remains supported as a logical root with
  its root checkout selected as base.
- Arbitrarily nested independent repositories remain supported.
- Repository identity, upstream verification, branch behavior, and worktree
  isolation remain per repository.
- The absence of backward manifest compatibility is deliberate, not an
  accidental regression.

The main engineering risk is treating the base relationship as a parent
relationship. Keeping those two axes separate prevents a sibling such as
`web` from being incorrectly mounted under `api`, and prevents base metadata
rules from changing Git containment or ignore behavior.

## Open questions for a specification

- Should `wtree init` require `--base-repository` whenever there is more than
  one top-level repository, or should an interactive prompt be allowed?
- Should the portable manifest always live at `project.wtree.yml` in the base
  repository, or permit a controlled base-relative path?
- Which stable facts should determine a logical project's ID now that its
  filesystem root might not be a Git repository?
- How should a registry distinguish two logical projects that select the same
  base repository but use different sibling sets?
- Should `clone` accept a manifest path inside a base repository checkout as
  its primary source, or retain URL and local-file sources exactly as today?
- Which existing commands should be redesigned in the first implementation
  plan, and which should reject the new format until their forest support is
  complete?

## Relationship to existing work

This idea broadens the project topology described by the implemented portable
manifest clone work. It should be specified and planned as a new change rather
than retrofitting compatibility into that completed manifest format.
