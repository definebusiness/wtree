# Implementation context — logical project roots and repository forests

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Logical project root and repository forest implementation plan](logical-project-root-base-repository.md)
Source specification: [Logical project root and repository forest specification](../spec/logical-project-root-base-repository.md)
Captured: 2026-08-20

## 1. Purpose and precedence

This dump preserves only the product decisions and current-code evidence needed
to implement the parent plan. It is not a second specification and does not
repeat the complete normative contract. The specification owns behavior, the
plan owns execution, and both take precedence over this context if they differ.

The implemented [portable-manifest v2](../spec/portable-manifest-v2-base-repository-format.md)
and [live-branch clone](../spec/clone-live-branch-and-upstream-status.md)
contracts remain prerequisites. Their historical plans and run ledgers are not
part of this implementation surface and must not be rewritten.

## 2. Decisions established before planning

- The base repository is one designated top-level repository. It owns
  `project.wtree.yml` and `.wtree.yml`, but it is not an implicit parent of
  sibling repositories and does not redefine the logical root.
- A plain logical root can group top-level repositories beneath ordinary
  directories, for example `services/serviceA` and `services/serviceB`.
- The traditional root-repository layout can use the same grouping shape for
  declared children. In that case `services/` is an ordinary directory inside
  the parent worktree; the parent repository, not the grouping directory,
  owns the child ignore rules.
- If a grouping path is itself a declared repository, descendants below it
  must name it as their Git parent. Filesystem containment never invents
  parentage.
- The idea document's former open-question section was removed. Normative
  implementation requirements belong only in the specification and plan.
- Machine-readable topology is added only to commands that actually report
  project topology. Those JSON objects receive `logicalRoot` and
  `baseRepository`; scalar `path` and `repo path` output and focused `repo get`
  output stay unchanged.
- Clone preflight commits remain observations. Execution checks out and
  records the selected branch tips fetched during execution; this feature does
  not introduce a cross-repository atomic snapshot guarantee.

## 3. Existing schema and version boundaries

| Boundary | Current implementation | Required direction |
|---|---|---|
| Portable manifest | `internal/config/portable_manifest.go`, version 2, explicit `base_repository`, single root mounted at `.` | Keep version 2 and all established fields; broaden only forest validation and mount interpretation. |
| Local project config | `internal/config/config.go`, version 1, no logical root or base, optional manifest metadata | Cut over strictly to local version 2 with required base, logical root, and manifest metadata; reject local v1 after delivery. |
| Global config | Shares `config.Version == 1` with local config | Remain version 1; split the constants and validation paths before changing the local version. |
| Domain project | `internal/domain/project.go`, version 1, exactly one root | Represent and validate a forest plus declared base and resolved logical-root context. |
| Workspace state | `internal/store/store.go`, version 1 | Remain version 1; its root path and per-repository resolved paths already represent a forest. |
| Workspace plan | `internal/plan/plan.go`, version 1 | Remain version 1; add the specified topology context without reinterpreting existing fields. |
| Registry and recovery | `internal/store/store.go`, version 1 | Remain version 1; derive logical root from base config and corroborate it with state rather than adding a registry field. |
| Clone plan/result/completed JSON | Version 2 | Remain version 2; add topology context and forest paths without reviving exact-commit semantics. |

The shared `config.Version` constant is an immediate migration hazard: raising
it directly would also reject every global configuration. Local and global
versions need distinct constants, focused diagnostics, and compatibility
tests.

## 4. Current topology assumptions that must be removed

### Model, mount, and ordering

- `domain.Project.Validate` requires exactly one parentless repository.
- `domain.Project.ParentFirst` starts from a single `root()` and recursively
  traverses lexically sorted children. It therefore drops sibling trees and
  does not implement the required depth-then-ID ordering.
- `ChildFirst` merely reverses that traversal. The required forest contract is
  decreasing depth and reverse lexical ID.
- `pathutil.NormalizeMount(..., root=true)` accepts only `.`. Callers use the
  boolean to conflate top-level status with the historical root mount.
- `Project.EffectivePaths` is the principal runtime path resolver and already
  rejects undeclared overlaps. It should remain the single effective-path
  authority after it is extended for logical-root-relative top-level mounts,
  parent-relative child mounts, grouping components, reserved paths, and
  canonical/symlink checks.

Ordering must be centralized and reused by configuration validation, planning,
execution, inspection, and teardown. General parent-first order is increasing
depth then lexical repository ID. Metadata-authority operations move the base
ahead of other depth-zero repositories only. Child-first order is decreasing
depth then reverse lexical repository ID.

### Discovery, initialization, and resolution

- `internal/discovery/discovery.go` first replaces the supplied path with its
  containing repository root, so a plain directory cannot be the discovery
  boundary and sibling top-level repositories are invisible.
- `internal/service/init.go` treats the first parentless repository as both the
  project root and metadata owner. Its request has no base selector, its
  local/portable files are rooted there, and base ignore handling is tied to
  the historical repository ID.
- Candidate project-ID hashing already uses canonical manifest facts. The new
  forest membership, parentage, mounts, and selected base must flow through
  those facts while the logical-root path remains excluded. Existing retained
  artifact collision allocation must stay intact.
- `internal/service/resolve.go` finds `.wtree.yml` by ancestor traversal only
  for paths beneath the base checkout, otherwise relying on registry and Git
  identity. It currently resolves repository sources relative to the config
  directory. Local v2 requires resolving `logical_root` from the base config,
  then resolving every source from that logical root.
- Registry repository identities and workspace-state checkout paths are the
  evidence needed to resolve commands from sibling and nested repositories.
  Resolution must not add an arbitrary recursive metadata scan.

### Clone and workspaces

- Clone planning and execution currently map every parentless repository to
  the destination/staging root, require tracked-manifest verification on every
  parentless repository, and write `.wtree.yml` at the staging root.
- Forest clone must instead map each top-level mount below the staging logical
  root, create only preflighted grouping directories, verify and write metadata
  only in the base checkout, and preserve private staging plus atomic publish.
- Clone cleanup already maintains an ownership inventory. It must expand to
  every created grouping directory and checkout rather than introduce a
  separate cleanup mechanism.
- Workspace planning already computes repository paths and builds parent-owned
  ignore requirements, including multi-component child mounts. It still
  assumes a single `.` root via mount normalization and traversal.
- Grouping directories contain no generated metadata and no generated
  `.gitignore`. Creation and rollback must record only directories the current
  operation created and remove them only after identity/ownership validation.

### Import, inspection, and teardown

- Import currently derives one repository root and treats it as the workspace
  root. It must discover a declared forest within a logical root and resolve
  repositories only by canonical common-Git-directory identity.
- Status, doctor, project inventory, removal, and deletion all depend on the
  current single-root ordering or root derivation. They need the same shared
  topology and persisted workspace mappings rather than command-specific
  reconstruction.
- Registry conflict detection currently compares base config paths and Git
  identities. The new check also needs canonical logical roots and every
  top-level checkout path, derived from config and state without changing the
  registry wire shape.

## 5. Public output locations

The implementation must add `logicalRoot` and `baseRepository` to these
topology-bearing JSON results and their compatibility tests:

- init result and dry-run;
- clone plan, planning result, and completed clone envelope;
- create and checkout plans/results;
- import plan/result;
- remove and delete plans/results;
- status and doctor; and
- healthy project-list entries.

Stale project-list entries omit topology facts that cannot be validated and
retain their diagnostic. For init, clone, and healthy project-list entries,
`logicalRoot` names the project logical root. For workspace operations,
status, and doctor, it names the logical workspace root. `baseRepository`
always names the declared repository ID. Failures before topology validation
must not fabricate either value.

## 6. Test assets and likely edit surface

| Concern | Primary files and tests to inspect |
|---|---|
| Forest invariants, paths, order | `internal/domain/project.go`, `internal/pathutil/mount.go`, their unit/fuzz tests |
| Portable/local configuration | `internal/config/portable_manifest.go`, `internal/config/config.go`, `internal/config/files.go`, codec and validation tests |
| Discovery and init | `internal/discovery/discovery.go`, `internal/service/init.go`, `internal/cli/root.go`, focused and process tests |
| Runtime selection | `internal/service/resolve.go`, `internal/service/registration_conflict.go`, registry/state tests |
| Clone | `internal/service/clone_plan.go`, `clone_execute.go`, `clone_result.go`, `clone_registry_facts.go`, `internal/cli/clone.go` |
| Workspace operations | `internal/service/plan.go`, `create.go`, checkout/transaction/recovery code, `internal/plan/plan.go` |
| Import and observation | `internal/service/import.go` and discovery/identity tests |
| Status, doctor, inventory, teardown | `internal/service/status.go`, `doctor.go`, `project_inventory.go`, `remove.go`, `delete.go`, corresponding CLI renderers |
| Public behavior | `cmd/wtree` process tests, `internal/cli` tests, tutorial fixture/run script, README and how-to docs |

Tests should use temporary repositories, local bare remotes, isolated Git
configuration, and paths with spaces. The minimum representative fixtures are:

1. one repository at `.` with nested children;
2. a plain logical root with sibling top-level repositories under grouping
   directories and a non-`.` base;
3. a forest combining siblings with at least three nesting levels; and
4. a root repository at `.` whose children use multi-component grouping
   mounts.

Each mutating slice needs no-mutation preflight failures plus exact owned
rollback for failures after mutation. Forest tests must include symlink
aliases, undeclared containment, top-level overlap, cancellation, concurrent
publication, and failures in different trees.

## 7. Scope guards

- Do not introduce portable manifest v3, registry v2, workspace-state v2,
  workspace-plan v2, or recovery v2.
- Do not add automatic local-v1 conversion, a compatibility parser, a
  migration command, or a recursive arbitrary-root metadata search.
- Do not add submodule/gitlink support, partial-workspace redesign, branch
  policy changes, release locking, aggregate sync, commits, pushes, or
  publication.
- Do not treat base authority as Git parentage, create metadata in a plain
  logical root, or place ignore rules in grouping directories.
- Do not rewrite historical completed plans, execution logs, or run ledgers.

## 8. Useful implementation searches

```sh
rg -n "exactly one root|rootCount|\.root\(\)|ParentFirst|ChildFirst|ParentID == \"\"" internal
rg -n "NormalizeMount|ResolveMount|DefaultMount|resolvedPath|RootPath" internal
rg -n "config\.Version|CurrentVersion|PortableManifestVersion|ClonePlanVersion|CloneResultVersion" internal
rg -n "ConfigPath|CommonGitDir|repositoryRoot|findConfig|loadProject|sourceWorkspace" internal
rg -n "TrackedManifestExact|CommittedParentIgnore|manifest.path|\.wtree.yml" internal/service internal/config
rg -n "json:\"(rootPath|projectId|baseRepository|logicalRoot)" internal
```
