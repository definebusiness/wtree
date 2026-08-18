# Portable manifest v2 base-repository format specification

Status: implemented
Source idea: [Logical project roots with a designated base repository](../ideas/logical-project-root-base-repository.md)
Implementation plan: [Portable manifest v2 base-repository format implementation plan](../plans/portable-manifest-v2-base-repository-format.md)

## 1. Purpose and scope

This specification pulls forward the portable-manifest format portion of the
logical-project-root idea. It replaces the portable manifest contract with
version 2 and makes the metadata authority explicit, without adding support
for sibling repositories or a non-Git logical project root.

It is a deliberately narrow, breaking format change. The existing one-root,
nested-repository topology remains the only supported topology. The full
logical-root and sibling-repository design remains deferred to the source
idea.

The safety, identity, transaction, and repository rules in
[the portable manifest clone specification](portable-manifest-clone.md) and
[`wtree` specification](wtree.spec.md) continue to apply unless this
specification replaces them.

## 2. Compatibility boundary

Portable manifests are version 2. Implementations must reject version 1 and
every other unsupported version with a direct diagnostic that the logical-root
manifest format is required. They must not translate version 1 data, accept
both schemas, or provide a compatibility mode.

The local `.wtree.yml` schema remains version 1. Its existing manifest
metadata remains valid: `manifest.path` is `project.wtree.yml` and is relative
to the base repository. In this scoped implementation the base repository is
also the project checkout root, so no local-configuration path relocation is
required.

## 3. Version 2 portable manifest schema

The strict schema is:

```yaml
version: 2
project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme-shop
  base_repository: root
repositories:
  root:
    clone:
      remote: origin
      url: https://github.com/acme/acme-shop.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    identity:
      initial_commits:
        - 0123456789abcdef0123456789abcdef01234567
    parent: ""
    mount: .
    default_branch: main
```

`project.base_repository` is required, uses the existing portable repository
ID grammar, and names an entry in `repositories`. Serialization must render it
as `base_repository` and remain deterministic: repository keys are lexical and
initial-commit arrays are sorted.

All existing repository clone, upstream, identity, ID, and mount validation
rules continue to apply.

## 4. Scoped topology invariants

Until sibling support is separately specified and implemented, a valid version
2 manifest must satisfy all of these additional constraints:

1. The repository graph has exactly one parentless repository.
2. That repository has `mount: .`.
3. `project.base_repository` names that sole parentless repository.
4. Every other repository is a descendant of that repository and its mount is
   relative to its immediate parent.

Thus `base_repository` is explicit in the persisted contract, but has no new
path or Git-parent semantics in this scope. It identifies the same checkout
that already owns `project.wtree.yml`, `.wtree.yml`, and the root `.gitignore`.

Manifests with multiple parentless repositories, a base repository that is not
the sole root, a top-level mount other than `.`, or a base ID that does not
exist are invalid. Supporting any of them requires a later specification for
the logical-root forest model.

## 5. Authoring and clone requirements

`wtree init` must write `version: 2` and set `project.base_repository` to the
discovered root repository ID. It continues to write both metadata files in
that root checkout and to manage the root `.gitignore` entry for `.wtree.yml`.

`wtree clone` must validate the v2 manifest before planning mutations. Its
existing root checkout remains the selected base checkout; it continues to
verify that this checkout tracks byte-identical `project.wtree.yml` content.
No clone destination, discovery, workspace, import, resolver, registry, or
recovery behavior is broadened by this specification.

## 6. Required verification

Tests must cover:

- canonical v2 decode and byte-stable rendering with `base_repository`;
- rejection of version 1, missing base ID, unknown base ID, and a base ID that
  is not the sole root repository;
- `init` emitting `base_repository: root` and version 2; and
- clone planning and execution accepting the scoped v2 layout while preserving
  tracked-manifest byte verification.

The existing nested-repository tests remain required, proving that the format
cutover does not weaken the one-root parent-relative mount model.

## 7. Explicit deferrals

This specification does not authorize:

- a plain logical project directory;
- more than one top-level repository;
- top-level mounts other than `.`;
- base-relative metadata outside the logical checkout root; or
- forest-aware discovery, clone transactions, workspaces, imports, resolution,
  registry operations, recovery, or ignore management.

Those capabilities require a later specification and implementation plan based
on the source idea.
