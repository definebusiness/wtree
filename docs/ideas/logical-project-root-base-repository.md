# Idea: logical project roots with a designated base repository

Status: specified
Specifications: [Portable manifest v2 base-repository format](../spec/portable-manifest-v2-base-repository-format.md); [Logical project root and repository forest](../spec/logical-project-root-base-repository.md)

## Summary

`wtree` should support a project whose checkout root is a plain directory,
rather than requiring that directory itself to be a Git repository. Such a
logical project root can contain several sibling repositories, one of which is
the designated **base repository**. The base repository is the project's
metadata authority; it does not have to be the filesystem root or the parent
of every other repository.

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

The portable-manifest v2 cutover and explicit base-repository field have
already been delivered. The remaining work broadens that same v2 format from
one root tree to a repository forest. Existing valid single-root v2 manifests
remain supported as the `mount: .` subset; portable v1 remains unsupported.

## Delivered prerequisite and remaining scope

The implemented portable-manifest v2 work already provides:

- the required `project.base_repository` field;
- canonical field ordering and deterministic repository rendering;
- rejection of portable manifest v1; and
- byte-identical tracked-manifest verification during clone.

This idea's remaining scope is a plain logical root, multiple top-level
repositories, base-owned local configuration v2, and forest-aware behavior
across discovery, initialization, clone, workspaces, import, resolution,
inspection, removal, registry operations, and recovery.

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
- Extend the implemented portable v2 topology without introducing portable v3
  or weakening the existing single-root v2 case.

## Non-goals

- Restoring portable manifest v1 compatibility or adding a dual portable
  parser.
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

### Grouping directory

A non-repository directory used by a multi-component mount. For example,
top-level repositories may use `services/serviceA` and `services/serviceB`
under a plain logical root, or a root repository may declare child repositories
at those mounts. The intermediate `services/` directory creates no implicit
repository or parent relationship.

### Base repository

The one repository selected by `project.base_repository`. It owns the tracked
portable manifest and the ignored local configuration. It may be a
top-level repository at a non-`.` mount, or it may be the repository mounted
at `.` when the logical project root is itself a Git repository.

Being the base repository grants metadata authority only. It does not make
all sibling repositories children of the base repository.

## Portable manifest forest shape

The implemented portable format already uses version 2 and expresses the base
repository explicitly. The forest work retains that version and broadens the
meaning of top-level `parent` and `mount` values:

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

The follow-on specification requires all of the following:

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
   its actual execution-time checkout are exactly the bytes used for planning.
10. Ignore rules apply only to declared nested mounts in their immediate Git
    parent. Top-level sibling mounts cannot be represented by a `.gitignore`
    rule in a non-Git logical root.
11. Multi-component mounts may create ordinary grouping directories, but a
    grouping path that is itself a repository must be represented by the
    corresponding declared parent relationship.

Detailed schema, command, transaction, compatibility, documentation, and test
requirements belong to the linked logical-root forest specification rather
than being duplicated in this idea.

## Compatibility and intentional breaking changes

The portable manifest contract does not change versions. Portable v1 was
already removed by the completed v2 cutover, and valid single-root v2
manifests remain valid. The intentional compatibility break in the remaining
scope is the machine-local project configuration.

- Existing local configuration version 1 is rejected after logical-root
  support is delivered; it is not translated or rewritten while being read.
- The portable `repositories` graph broadens from exactly one root to a forest,
  while the already-required `project.base_repository` field continues to
  identify metadata authority.
- `project.wtree.yml` and `.wtree.yml` move from the assumed checkout root to
  the base repository when the logical root is not itself a Git repository.
- Existing `wtree init`, `clone`, `import`, and explicit-project path behavior
  that assume a Git repository at the project directory must be revised.
- Scripts and CI that assume one parentless repository or `mount: .` must
  accept the broader v2 forest invariants. There is no portable v3 or dual
  portable parser.

The user-facing release notes should give a concise rebuild path: recreate the
project metadata with the new `wtree init` flow, select the base repository,
review and commit the new manifest in that repository, then distribute that
manifest. The explicit local-schema break avoids the risk of silently
translating incomplete topology information.

## Compatibility and risk assessment

The new design should not weaken the existing capabilities after they are
implemented against the revised schema:

- A traditional root Git repository remains supported as a logical root with
  its root checkout selected as base.
- Arbitrarily nested independent repositories remain supported.
- Repository identity, upstream verification, branch behavior, and worktree
  isolation remain per repository.
- Portable v1 remains unsupported, while existing valid single-root v2
  manifests remain supported.

The main engineering risk is treating the base relationship as a parent
relationship. Keeping those two axes separate prevents a sibling such as
`web` from being incorrectly mounted under `api`, and prevents base metadata
rules from changing Git containment or ignore behavior.

## Relationship to existing work

The implemented [portable manifest v2 specification](../spec/portable-manifest-v2-base-repository-format.md)
pulled forward only the breaking format cutover and explicit base field. The
remaining logical-root forest behavior is defined by the
[logical project root and repository forest specification](../spec/logical-project-root-base-repository.md)
and awaits a separate implementation plan.
