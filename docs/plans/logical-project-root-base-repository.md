# Logical project root and repository forest implementation plan

Status: implemented
Source specification: [Logical project root and repository forest specification](../spec/logical-project-root-base-repository.md)
Implementation context: [Focused implementation context dump](logical-project-root-base-repository-context.md)
Related idea: [Logical project roots with a designated base repository](../ideas/logical-project-root-base-repository.md)
Implemented prerequisites: [Portable manifest v2 base-repository format specification](../spec/portable-manifest-v2-base-repository-format.md); [Live-branch clone and upstream-aware human status specification](../spec/clone-live-branch-and-upstream-status.md); [Automatic nested mount ignore protection specification](../spec/automatic-nested-mount-ignore-protection.md)
Source of truth: [`internal/config`](../../internal/config); [`internal/domain/project.go`](../../internal/domain/project.go); [`internal/pathutil/mount.go`](../../internal/pathutil/mount.go); [`internal/discovery`](../../internal/discovery); [`internal/service`](../../internal/service); [`internal/plan`](../../internal/plan); [`internal/store`](../../internal/store); [`internal/cli`](../../internal/cli); [`cmd/wtree`](../../cmd/wtree)
Delivery style: test-first, one independently reviewed milestone at a time; strict local-configuration v2 cutover; no automatic migration, dependency addition, commit, push, publication, or release

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan and its source specification fix them.

For each unchecked milestone, in order:

1. Read this plan, the source specification, the focused context dump, the
   relevant source-of-truth files, the current worktree, and the durable ledger
   at `docs/ai/runs/logical-project-root-base-repository.md`. Create that
   ledger only after execution is authorized and before the first
   implementation dispatch. On resumption, reconcile the plan, ledger,
   evidence, and filesystem before dispatching more work.
2. Record a complete current-milestone checklist in the ledger. It must cover
   every scope item, test-first slice, documentation item, exit criterion, and
   verification command in that milestone.
3. Give the complete initial packet to the normal `implementer`. Require RED
   → GREEN → REFACTOR evidence, changed files, command results, compatibility
   evidence, and unresolved concerns. Use the normal `implementer` for
   remediations starting with zero or one rejected complete submission and the
   `escalation-implementer` only when the ledger already records two.
4. Treat partial work as progress only. Request review only after every
   checklist item has complete evidence.
5. Send every complete submission to the read-only `reviewer`, which inspects
   the current shared filesystem against the full milestone, specification,
   safety, portability, compatibility, and test-quality requirements.
6. Record all material findings with stable IDs and return the entire
   unresolved set in one remediation packet. Apply the exact three-rejected-
   complete-remediation limit from
   [`milestone-supervision.md`](../ai/milestone-supervision.md). Do not use an
   escalation reviewer as a routine second review.
7. After reviewer approval, run the milestone verification as the main agent,
   update affected contracts and documentation, check the milestone, append a
   concise execution-log row, replace the ledger snapshot with the next
   unchecked milestone, and dispatch that milestone immediately.

Follow the mandatory ledger shape, checkpoints, transitions, and final-response
gate in [`run-ledger-layout.md`](../ai/run-ledger-layout.md). Do not stop for
ordinary test failures, reviewer findings, partial submissions, temporary
compatibility gates, or approved milestones. Preserve unrelated worktree
changes; do not use destructive cleanup commands; commit only when separately
authorized. A final response during execution is permitted only by the durable
ledger gate.

## Fixed implementation decisions

### One forest and one metadata authority

- A project is a non-empty, acyclic forest of declared repositories. Parent
  links express Git containment; filesystem containment is valid only when it
  agrees with those links.
- Exactly one top-level repository is `baseRepository`. It owns the tracked
  `project.wtree.yml`, ignored local `.wtree.yml`, and its own committed
  `/.wtree.yml` ignore rule. Base authority never creates parentage.
- A top-level mount is logical-root-relative and may be `.` or a clean
  multi-component path. `.` is valid only when it is the sole top-level root.
  A child mount remains immediate-parent-relative and cannot be `.`.
- Ordinary path components between an owner root and a checkout are grouping
  directories. They are neither repositories nor metadata/ignore owners. If a
  grouping path is a declared repository, every repository below it must name
  the corresponding parent.
- General parent-first order is increasing depth then lexical repository ID.
  Metadata-authority operations place the base before other depth-zero IDs and
  leave deeper ordering unchanged. Child-first order is decreasing depth then
  reverse lexical ID. One shared implementation owns these orders.
- Effective-path validation happens before mutation and covers clean mounts,
  logical containment, declared ancestry, equality/overlap, reserved Git
  administration paths, canonical aliases, and symlink traversal.

### Local configuration and resolution

- Keep portable manifests at version 2 and widen only their forest topology.
  Preserve the established base field, canonical encoding, initial-commit
  ordering, strict parsing, live selected-branch execution, and tracked-byte
  verification.
- Split the currently shared configuration version constant. Global config
  remains version 1. Local project config moves directly to strict version 2
  with required `project.base_repository`, `logical_root`, `repositories`,
  `worktrees`, and `manifest`; `discovery` remains optional.
- Reject local version 1 with a direct reinitialization diagnostic. Do not
  translate, rewrite, dual-parse, or silently repair it.
- Resolve the local `logical_root` from the base config directory, verify that
  it inverts the base checkout's effective source path, and resolve every
  repository source from the logical root. The persisted value remains a
  clean relative path; runtime code uses canonical absolute paths only after
  validation.
- Resolve commands using explicit base-config ancestry, registered canonical
  common-Git-directory identity, and persisted logical/workspace root and
  checkout paths. Never recursively scan an arbitrary directory for metadata
  or infer a sibling beneath the base checkout.
- Keep registry, workspace state, workspace plan, and recovery at version 1.
  The registry continues to store the base configuration path and repository
  identities; derive and corroborate logical roots from validated config and
  state rather than adding a registry field.

### Initialization, clone, and workspace mutation

- `wtree init [logical-root] [--base-repository ID]` treats the explicit path,
  or current directory when omitted, as the discovery boundary. Discovery is
  deterministic, confined to that root, honors existing ignores, and has no
  newly invented depth or entry cap.
- One discovered top-level repository is the implicit base. Multiple
  top-level repositories require an explicit top-level base ID; missing,
  unknown, nested, and ambiguous choices fail before mutation and list useful
  candidates.
- Init and clone write project metadata only in the base checkout. Their
  default workspace state uses the logical root and all actual checkout paths.
  Init identity hashing includes the selected base and complete canonical
  forest facts but excludes the logical-root path; retained-artifact collision
  suffix behavior remains unchanged.
- Clone plans from one immutable validated forest and observed remote facts,
  but execution fetches each selected branch and records its actual HEAD. The
  destination and staging root are logical roots. Grouping directories are
  created only inside private staging after full preflight.
- Only the base requires byte-identical tracked manifest and committed
  `/.wtree.yml` ignore verification. Only declared children require committed
  ignore verification in their immediate actual parent. Top-level siblings do
  not require or receive cross-tree ignore rules.
- Workspace create and checkout use the same effective paths, order, and
  grouping ownership. Mount overrides remain relative to the same owner as
  defaults. A plain workspace root gets no generated `.gitignore` or metadata.
- Every mutating service extends existing ownership, compare-and-swap,
  cancellation, rollback, and recovery mechanisms to all trees and created
  grouping directories. It may remove only invocation-created artifacts whose
  identities and ownership still match.

### Import, inspection, teardown, and output

- Import observes its target as a logical workspace root and matches
  repositories exclusively by registered common Git directory. It accepts a
  starting path inside any known checkout only when topology and persisted
  evidence yield one root; otherwise an explicit target is required.
- Status, doctor, inventory, prune, removal, deletion, and recovery use the
  central forest order and persisted path mapping. Teardown completes all
  applicable descendants before top-level checkouts and the logical workspace
  directory.
- Registration conflicts compare validated canonical logical roots and all
  top-level checkout paths as well as base config paths and repository
  identities. Unregistration remains state cleanup, not repository deletion.
- Add camel-case `logicalRoot` and `baseRepository` to topology-bearing JSON
  from init; clone planning/completion; create, checkout, import, remove, and
  delete plans/results; status; doctor; and healthy project-list entries.
  Preserve existing fields and versions and cover additive compatibility.
- Stale inventory entries omit topology facts that cannot be established.
  Failures before validated topology exists omit them. Init, clone, and
  healthy inventory report the project logical root; workspace operations,
  status, and doctor report that result's logical workspace root.
- Do not add topology fields to scalar `path` or `repo path`, or to focused
  `repo get` output. Portable objects keep `base_repository`.

### Incremental safety during the plan run

- Every approved milestone must leave all repository tests green. If a shared
  model begins accepting a forest before a mutating consumer is forest-safe,
  that consumer must have a focused, named preflight capability guard that
  fails before mutation and is covered by a no-mutation test.
- Record every temporary guard in the durable ledger when introduced. Remove
  it in the milestone that makes its consumer forest-safe, and prove in M08
  that no forest-support guard remains.
- Do not preserve partial-feature compatibility in the delivered result.
  After M08, portable v2 forests are supported everywhere in scope and local
  configuration v1 is rejected everywhere.

## Architecture and dependency boundaries

```text
portable v2 + local v2 ──→ validated project forest ──→ stable order + paths
          │                          │                         │
          │                          ├──→ resolver ← registry/state identities
          │                          │
          ├──→ init/discovery        ├──→ clone staging/publication
          └──→ CLI JSON              ├──→ workspace/import
                                     └──→ status/doctor/removal/recovery
```

- `internal/config` owns strict persisted syntax, canonical portable bytes,
  schema-version diagnostics, and config-relative logical-root validation. It
  must not depend on Git, service, CLI, or store packages.
- `internal/domain` owns the path-independent repository graph and stable
  forest order. `internal/pathutil` owns portable mount grammar and safe
  filesystem resolution. Neither package performs discovery or mutation.
- `internal/discovery` observes repositories below an explicit boundary and
  reports canonical path/common-Git-directory facts. It does not select the
  base, author configuration, or mutate ignores.
- `internal/service` owns evidence combination, topology resolution, planning,
  transaction ownership, mutation, rollback, recovery, and public service
  result facts. All services consume the shared forest order and effective
  path resolver rather than reconstructing topology.
- `internal/store` remains the owner of registry, workspace-state, and
  recovery version-one persistence and locking. It must not infer Git
  parentage from paths.
- `internal/cli` owns flags, human rendering, help, and completed-command JSON
  envelopes. It renders service facts and must not scan the filesystem or
  infer the base independently.

## Stable contracts to establish early

| Contract | Owner and consumers | Invariant and enforcement |
|---|---|---|
| Forest graph and ordering | `internal/domain`; all planners, executors, inspectors, teardown | Non-empty acyclic declared forest, one top-level base, exact depth/ID order, no map/discovery-order dependence. Table and permutation tests cover trees and forests. |
| Mount and effective paths | `internal/pathutil` plus domain composition; config/service consumers | Top-level logical-root-relative and child parent-relative paths, declared-containment overlap rules, reserved path and symlink/canonical escape rejection. Unit, fuzz, and filesystem tests enforce it. |
| Local v2 location inversion | `internal/config` and service loader/resolver | Base config's relative `logical_root` and base source resolve back to the config directory; all sources resolve from that root; global v1 is unaffected. Codec and resolver tests enforce it. |
| Base metadata authority | init and clone services | Exactly the declared top-level base owns/validates portable and local metadata; no sibling/grouping directory does. Dry-run, mutation, and failure tests enforce it. |
| Grouping-directory ownership | clone/workspace transactions and recovery | Only fully preflighted operation-created directories are recorded or removed; grouping paths contain no generated metadata/ignore. Failure-injection and rollback tests enforce it. |
| Resolution evidence | resolver consuming config, registry, Git identity, and workspace state | Commands resolve from every declared checkout/root without arbitrary recursive scanning or base-as-parent inference; ambiguity remains explicit. Integration tests exercise every entry point. |
| Additive topology JSON | service models and CLI envelopes | Only listed topology-bearing results add exact `logicalRoot`/`baseRepository`; versions and existing meanings remain stable. Golden/decoded compatibility tests enforce it. |

## Global definition of done

- Every changed behavior has recorded RED → GREEN → REFACTOR evidence with
  focused hermetic tests before implementation changes.
- Forest fixtures cover a plain root with grouped sibling top-level
  repositories and a non-`.` base; siblings plus at least three nesting
  levels; the root-Git `.` subset; and grouped children inside a root
  repository.
- Validation tests cover missing/unknown/nested base, missing parents, cycles,
  duplicate/equal/overlapping mounts, undeclared containment, `.` plus another
  top-level root, escapes, reserved Git paths, paths with spaces, grouping
  paths, symlink traversal, and canonical aliases.
- Mutating tests prove complete preflight or exact owned rollback for failure,
  cancellation, concurrent publication, output-writer failure where
  applicable, and failures in different top-level trees. They use temporary
  repositories, local remotes, and isolated Git configuration without network
  access or real user state.
- Existing live selected-branch behavior, observed-versus-actual commit
  semantics, manifest byte verification, initial-commit identity, nested
  ignore protection, credential redaction, submodule rejection, registry
  compare-and-swap, transaction, and recovery tests remain green.
- Compatibility tests prove portable manifest stays v2; global config,
  registry, workspace state, workspace plan, and recovery stay v1; clone
  plan/result/completion stay v2; local project config is strict v2; and every
  listed JSON addition preserves existing field meanings.
- Current README, root/command help, how-to, tutorial, topology/manifest and
  recovery documentation, specification traceability, lifecycle state, and
  execution evidence agree with the delivered behavior. Historical completed
  plans, logs, and ledgers remain unchanged.
- Run `gofmt -w` only on Go files changed by an authorized milestone, followed
  by:

  ```text
  go test ./... -count=1
  go test -race ./... -count=1
  go vet ./...
  make fmt-check
  make build
  make release-test
  make check
  git diff --check
  ```

- Independent review has no unresolved material findings, every temporary
  forest capability guard is removed, and no file under `docs/ai/runs/` other
  than this plan's authorized ledger was changed.

## Risk and rollout

- Schema-cutover risk: local and global configuration currently share a
  version constant. Split their codecs and diagnostics first, keep global v1,
  migrate all valid local fixtures and writers together, and retain literal v1
  only in rejection tests.
- Path-safety risk: forests admit top-level multi-component mounts and ordinary
  grouping directories. Centralize path resolution, preflight the complete set
  before mutation, reject symlink/canonical aliases, and inventory every
  operation-created directory.
- Authority risk: existing code often equates root, first parentless
  repository, base, destination, and config directory. Name these facts
  separately in models and tests and prohibit discovery-order selection.
- Cross-command drift risk: many services traverse or reconstruct the current
  tree independently. Establish one order/path contract first and convert
  consumers milestone by milestone with explicit preflight guards.
- Rollback risk: a failure can occur after work in several independent trees.
  Preserve existing transaction and recovery contracts while extending
  identity evidence to every checkout and grouping directory; test failures in
  the first, middle, and last trees.
- Compatibility risk: topology fields touch several public JSON surfaces.
  Add them only at the enumerated results, keep current schema versions, decode
  old fields in tests, and verify omission when topology is unavailable.
- Moving-branch risk: independently selected branches can move at different
  times. Preserve observed preflight facts and actual execution HEADs without
  claiming a coordinated snapshot; release locking remains separate scope.
- The release is one completed feature cutover. Temporary capability guards
  are implementation scaffolding only and are not a supported compatibility
  mode. Installation, publication, commits, pushes, and release require
  separate authorization.

## Milestones

### [x] M00 — Establish forest graph, ordering, and path primitives

Specification coverage: [§§3–4](../spec/logical-project-root-base-repository.md#3-terminology-and-authority) and [§12 items 2–6](../spec/logical-project-root-base-repository.md#12-required-verification).

Scope:

- Extend the portable v2 topology validator and domain model from one tree to
  the specified forest with one declared top-level base.
- Replace root-boolean mount semantics with explicit top-level versus child
  contracts and establish complete effective-path validation.
- Implement the exact general parent-first, base-first, and child-first orders
  once and route existing safe consumers through them.
- Preserve canonical portable bytes and all existing clone/upstream/identity
  field validation.
- Add named preflight guards to any not-yet-safe mutating consumer newly
  reachable with a forest; record them for later removal.

Test-first slices:

1. Add permutation-driven graph tests for multiple roots, selected base,
   missing parent, nested/unknown base, cycles, and the three exact orders.
2. Add table/fuzz tests for `.` root compatibility, grouped top-level and child
   mounts, path equality/overlap, undeclared containment, escapes, reserved
   administration paths, spaces, symlink aliases, and canonical aliases.
3. Add portable v2 decode/validate/canonical round-trip tests for a forest and
   prove repository map order does not affect bytes or traversal.
4. Add no-mutation tests for every temporary service guard introduced by this
   milestone.

Verification:

- `go test ./internal/domain ./internal/pathutil ./internal/config -count=1`
- `go test ./internal/domain ./internal/pathutil ./internal/config -race -count=1`
- `go test ./internal/service -run 'ForestUnsupported|Portable|Mount|Order' -count=1`
- Global definition-of-done commands.

Exit criteria: one tested model defines valid forests, effective paths, and
all stable orders; existing one-root projects remain valid; unsafe consumers
reject forests before mutation rather than misinterpreting them.

### [x] M01 — Cut local configuration to v2 and establish topology resolution

Specification coverage: [§5](../spec/logical-project-root-base-repository.md#5-local-configuration-v2), [§6.2](../spec/logical-project-root-base-repository.md#62-runtime-discovery-and-explicit-selection), and [§11](../spec/logical-project-root-base-repository.md#11-compatibility-and-transition).

Scope:

- Split local and global config versions; implement strict required local v2
  fields, relative logical-root validation, base/source inversion, and exact
  v1 rejection diagnostics while keeping global v1 unchanged.
- Thread validated base ID and canonical logical-root facts through the loaded
  runtime project without persisting redundant registry data.
- Resolve base-config ancestry, sibling/nested registered Git identities,
  project logical roots, and logical workspace roots using config,
  registry, and state evidence with deterministic ambiguity handling.
- Update existing one-root init and clone writers plus test fixtures to emit
  valid local v2 immediately, without yet broadening their discovery/execution
  capability.
- Preserve registry, workspace-state, plan, recovery, and clone wire versions.

Test-first slices:

1. Add codec tests for the complete v2 shape, missing/unknown fields, invalid
   relative roots/sources, inversion mismatch, symlink alias, exact v1
   rejection, and unchanged global v1 acceptance.
2. Add resolver tests from the base, every sibling/nested identity, logical
   project root, logical workspace root, explicit config, ambiguous evidence,
   stale state, and arbitrary unregistered directories.
3. Convert valid local fixtures and assert root-Git init/clone output remains
   functional with `logical_root: .`, declared base, and required manifest.
4. Add compatibility tests for all persisted version boundaries and prove no
   registry schema field was added.

Verification:

- `go test ./internal/config -count=1`
- `go test ./internal/service -run 'Resolve|LoadProject|LocalConfig|Registry|SourceWorkspace' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Config|Init|Clone|Resolve' -count=1`
- Global definition-of-done commands.

Exit criteria: every supported local config is strict v2, global and other
persisted v1 schemas remain readable, and topology can be resolved from every
authorized evidence source without recursive guessing.

### [x] M02 — Make discovery and init author complete forests

Specification coverage: [§6.1](../spec/logical-project-root-base-repository.md#61-wtree-init), [§4.2](../spec/logical-project-root-base-repository.md#42-ignore-ownership), and [§12 items 1–7 and 13](../spec/logical-project-root-base-repository.md#12-required-verification).

Scope:

- Discover all canonical Git worktrees below the explicit logical-root
  boundary, derive nearest declared containment, and retain deterministic IDs,
  ignores, cancellation, and no-unbounded-ancestor guessing.
- Add `--base-repository`, enforce implicit versus explicit selection, and
  include candidate IDs/mounts in non-mutating ambiguity errors.
- Plan and write portable v2 plus local v2 only in the base checkout, update
  only the base metadata ignore and immediate nested-parent ignores, and
  publish default state/registry facts for the whole forest.
- Keep deterministic project identity relocation-independent and sensitive to
  membership, parentage, mount, clone/upstream facts, identity, and selected
  base; preserve retained-artifact collision suffix allocation.
- Add `logicalRoot` and `baseRepository` to init dry-run/result JSON while
  retaining existing fields and human behavior.

Test-first slices:

1. Discover the four canonical fixtures, ignored directories, linked
   worktrees, symlink escapes, paths with spaces, and different traversal/map
   orders with identical results.
2. Prove one top-level base is implicit; multiple roots require a valid
   top-level selection; missing, unknown, nested, and ambiguous choices leave
   configs, manifests, ignores, state, and registry byte-identical.
3. Inject failures after each base metadata, parent-ignore, state, and registry
   step across different trees and prove exact rollback/recovery evidence.
4. Decode init JSON for topology additions and old fields; test identity
   stability under relocation and changes to siblings, topology, mount, and
   base plus retained-artifact allocation.

Verification:

- `go test ./internal/discovery -count=1`
- `go test ./internal/service -run 'Init|Discover|ProjectID|Ignore|Registration' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Init' -count=1`
- Global definition-of-done commands.

Exit criteria: init safely authors and registers the full forest from a plain
or root-Git logical root, stores metadata only in the selected base, and
reports deterministic topology.

### [x] M03 — Plan forest clone and version-compatible topology output

Specification coverage: [§7 paragraphs 1–3](../spec/logical-project-root-base-repository.md#7-forest-aware-clone), [§10 output contract](../spec/logical-project-root-base-repository.md#10-registry-inspection-and-recovery), and [§12 items 8 and 14](../spec/logical-project-root-base-repository.md#12-required-verification).

Scope:

- Plan every top-level and nested checkout from the destination logical root,
  preflight the complete resolved path set and grouping directories, and order
  base first at depth zero followed by stable forest order.
- Observe every selected remote branch without changing execution-time live
  branch semantics or claiming a coordinated snapshot.
- Require tracked-manifest and local-metadata-ignore verification only for the
  base; require committed parent ignore only for declared children.
- Extend clone plan, planning result, actions, human dry-run, and JSON with
  logical-root/base facts while keeping all version-two schemas and existing
  field meanings.
- Keep forest execution behind its named preflight guard until M04; dry-run
  must fully represent the forest and execution must still fail before staging.

Test-first slices:

1. Plan grouped sibling roots with a non-`.` base, a mixed deep forest, and
   root-Git compatibility; assert exact paths, verification owners, actions,
   and base-first/depth order.
2. Reject every topology/path/symlink/destination/registry conflict before
   remote or filesystem mutation and aggregate remote failures as currently
   required.
3. Advance selected branches after planning and prove plan values remain
   observations; preserve manifest bytes, credential redaction, defensive
   copies, cancellation, and plan tamper rejection.
4. Decode dry-run/planning JSON and prove additive topology fields, version 2,
   deterministic ordering, and unchanged existing meanings.

Verification:

- `go test ./internal/service -run 'ClonePlan|CloneResult|CloneRegistry|Forest' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Clone.*Dry|Clone.*JSON|Clone.*Plan' -count=1`
- `go test ./internal/service -run 'ClonePlan' -race -count=1`
- Global definition-of-done commands.

Exit criteria: clone dry-run is a complete deterministic forest plan with
truthful v2 topology and live-branch observations, while non-dry forest clone
cannot yet mutate.

### [x] M04 — Execute and publish forest clone transactionally

Specification coverage: [§7 paragraphs 4–7](../spec/logical-project-root-base-repository.md#7-forest-aware-clone) and [§12 items 8–9 and 12](../spec/logical-project-root-base-repository.md#12-required-verification).

Scope:

- Assemble every top-level and nested checkout plus required grouping
  directories inside one private staging logical root, then atomically publish
  under the established same-filesystem contract.
- Fetch/check out selected live branch tips, capture actual HEADs, verify
  identity/upstream/cleanliness/no-submodules, check child ignores in actual
  parents, and check exact manifest/base ignore only in the base.
- Write local v2 only in the staged base, then publish all checkout paths and
  actual commits to workspace state and all identities/base config to the
  registry using existing locks and compare-and-swap behavior.
- Expand ownership inventory, rollback, and recovery to all created grouping
  directories, checkouts, published root, state, and registry facts.
- Add topology to completed clone output, remove the forest execution guard,
  and preserve observed-versus-actual terminology.

Test-first slices:

1. Clone all four fixtures and assert layout, base metadata location, grouping
   contents, one selected local branch, tracking, actual commits, state, and
   registry resolution from every tree.
2. Move branches between plan and execute in multiple trees; require actual
   fetched HEADs and reject deleted/replaced refs without observed-commit
   fallback.
3. Reject missing/different base manifest, missing base ignore, missing child
   parent ignore, identity/upstream mismatch, dirty/submodule checkout, and
   hostile path/hook/configuration conditions with no publication.
4. Inject cancellation and failures before/after each tree, grouping creation,
   rename, state write, and registry write; verify cleanup ownership, rollback
   incomplete reporting, recovery, secret redaction, and concurrent publish.
5. Decode completed JSON and verify logical/base additions, observed plan
   commits, deterministic actual HEADs, old fields, and writer failures.

Verification:

- `go test ./internal/git -run 'Clone|Portable|CheckoutTrackingBranch' -count=1`
- `go test ./internal/service -run 'CloneExecute|CloneRecovery|CloneRollback' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Clone' -count=1`
- `go test ./internal/git ./internal/service -race -count=1`
- Global definition-of-done commands.

Exit criteria: forest clone performs one safe staged transaction, publishes a
resolvable logical root with base-owned metadata, and truthfully reports every
actual checkout.

### [x] M05 — Create and checkout complete forest workspaces

Specification coverage: [§8 paragraphs 1–5](../spec/logical-project-root-base-repository.md#8-forest-aware-workspaces), [§4.2](../spec/logical-project-root-base-repository.md#42-ignore-ownership), and [§12 items 10 and 12](../spec/logical-project-root-base-repository.md#12-required-verification).

Scope:

- Extend create/checkout planning and execution to every top-level tree and
  descendant using shared effective paths, stable order, and per-owner mount
  overrides.
- Preflight and transactionally create ordinary grouping directories; reject
  existing incompatible entries, aliases, escapes, and undeclared overlaps.
- Generate ignore protection only in each declared child's immediate parent;
  never generate metadata or `.gitignore` in a plain logical/grouping root.
- Store logical workspace root plus every actual checkout mount/path in
  unchanged workspace-state v1 and preserve branch/worktree rollback and
  recovery behavior.
- Add topology to version-one workspace plans and create/checkout results and
  remove their forest capability guards.

Test-first slices:

1. Plan and execute create/checkout for all canonical fixtures, custom
   top-level/child mount overrides, paths with spaces, and map-order
   permutations; assert exact paths, steps, and state.
2. Prove grouped children inside a root repository update only the parent
   ignore while grouped top-level siblings create no ignore file.
3. Reject conflicts, symlinks, dirty/branch/worktree violations, occupied
   grouping paths, and invalid overrides before mutation.
4. Inject failures and cancellation across directory creation, worktree/branch
   operations, ignore updates, state publication, rollback, and recovery in
   different trees; prove operation-owned cleanup only.
5. Decode plan/result JSON for additive topology and unchanged version/fields.

Verification:

- `go test ./internal/plan ./internal/domain -count=1`
- `go test ./internal/service -run 'WorkspacePlan|Create|Checkout|Ignore|Transaction|Recovery' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Create|Checkout' -count=1`
- `go test ./internal/service -run 'Create|Checkout' -race -count=1`
- Global definition-of-done commands.

Exit criteria: create and checkout materialize the complete declared forest at
one logical workspace root with correct ignore and grouping ownership, v1
state/plan compatibility, and exact rollback.

### [x] M06 — Import forests and resolve every supported command context

Specification coverage: [§9](../spec/logical-project-root-base-repository.md#9-forest-aware-import), [§6.2](../spec/logical-project-root-base-repository.md#62-runtime-discovery-and-explicit-selection), and [§12 item 11](../spec/logical-project-root-base-repository.md#12-required-verification).

Scope:

- Import from an explicit logical workspace root or a path inside any known
  tree using confined deterministic discovery, common-Git-directory identity,
  persisted state/registry evidence, and declared parentage.
- Require the observed set to match the declared forest subject to the
  existing explicit partial-import policy; reject unknown identity,
  contradictory containment, missing required trees, and ambiguous inferred
  roots without adoption.
- Publish version-one workspace state for the logical root and every observed
  checkout while preserving locks, concurrency, no-mutation dry-run, rollback,
  and recovery contracts.
- Add topology to import plans/results and ensure subsequent project
  selection, `path`, `repo path`, and `repo get` work from every tree without
  broadening their output shapes.
- Remove remaining resolver/import forest guards.

Test-first slices:

1. Import each canonical fixture from the root, base, every top-level sibling,
   and nested checkout; assert one inferred root where provable and explicit
   ambiguity otherwise.
2. Exercise exact full and existing partial policy with missing, unknown,
   duplicate-identity, relocated, mis-parented, symlinked, and overlapping
   checkouts.
3. Prove registry/state conflicts, concurrent publication, cancellation, and
   injected persistence failures leave no partial adopted workspace.
4. Decode import JSON topology additions and assert scalar/focused commands
   retain exact old output while resolving to correct forest paths.

Verification:

- `go test ./internal/discovery -count=1`
- `go test ./internal/service -run 'Import|Resolve|WorkspaceSelection|RepositoryPath' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Import|Path|Repo' -count=1`
- `go test ./internal/service -run 'Import|Resolve' -race -count=1`
- Global definition-of-done commands.

Exit criteria: import binds only identity-verified declared forests to one
logical workspace root, and every supported selection/path command resolves
from every tree without output drift or recursive guessing.

### [x] M07 — Make inspection, registry conflict, teardown, and recovery forest-aware

Specification coverage: [§8 paragraph 6](../spec/logical-project-root-base-repository.md#8-forest-aware-workspaces), [§10](../spec/logical-project-root-base-repository.md#10-registry-inspection-and-recovery), and [§12 items 10, 12, and 14](../spec/logical-project-root-base-repository.md#12-required-verification).

Scope:

- Enumerate status, doctor, healthy project inventory, prune diagnostics, and
  recovery evidence in stable forest order using declared parent, effective
  mount, resolved path, identity/drift, logical root, and base where relevant.
- Add canonical logical-root and all top-level checkout-path collision checks
  while retaining the registry v1 wire shape and stale-entry behavior.
- Plan and execute remove/delete child-first across every tree, delaying
  top-level and logical-root removal until descendants pass preflight and
  complete; preserve force, dirtiness, branch, transaction, and recovery
  rules.
- Never let unregister/prune imply filesystem deletion or let recovery infer
  sibling ownership from the base.
- Add topology fields to status, doctor, healthy inventory, remove, and delete
  plans/results; omit unavailable facts for stale/failure results; remove all
  guards owned by these consumers.

Test-first slices:

1. Assert exact status/doctor/inventory ordering and facts for healthy,
   missing, dirty, detached, stale, unknown, relocated, and multi-tree drift;
   prove status remains network-free.
2. Attempt conflicting registration through logical-root aliases, top-level
   path aliases, base config aliases, and common Git identities; prove
   deterministic diagnostics and byte-identical state on failure.
3. Remove/delete deep mixed forests and assert decreasing-depth/reverse-ID
   order, root retention until safe, grouping cleanup ownership, and no
   unrelated directory removal.
4. Inject preflight failures, cancellation, concurrent state changes, and
   failures after removals in different trees; prove exact rollback/recovery
   evidence and safe resume behavior.
5. Decode every affected JSON surface for additive fields, stale omission,
   unchanged versions/fields, deterministic order, and failure before known
   topology.

Verification:

- `go test ./internal/service -run 'Status|Doctor|Inventory|RegistrationConflict|Remove|Delete|Recovery|Prune' -count=1`
- `go test ./internal/cli ./cmd/wtree -run 'Status|Doctor|Project|Remove|Delete|Recovery' -count=1`
- `go test ./internal/service -run 'Remove|Delete|Recovery|Registration' -race -count=1`
- Global definition-of-done commands.

Exit criteria: inspection, collision detection, teardown, and recovery cover
the entire forest without schema drift, unsafe inferred ownership, or
premature logical-root deletion.

### [x] M08 — Close public documentation, compatibility, and release gates

Specification coverage: [§§1–13](../spec/logical-project-root-base-repository.md), especially [§12](../spec/logical-project-root-base-repository.md#12-required-verification) and [§13](../spec/logical-project-root-base-repository.md#13-documentation-requirements).

Scope:

- Remove every temporary forest capability guard and audit all root/tree/base,
  parent-first/child-first, config-path/source-path, and topology-output call
  sites for obsolete single-root assumptions.
- Add public process and tutorial scenarios for grouped sibling roots with a
  non-`.` base, mixed nesting, and root-Git grouped children, including init,
  clone, create/checkout, import, status/path, remove/delete, and project
  inspection flows.
- Update README, root/command help, how-to, tutorial, core topology and
  portable-manifest guidance, JSON examples, recovery docs, specifications,
  traceability, lifecycle status, plan evidence, and status overview.
- Prove local v1 rejection/reinitialization guidance and all unchanged schema
  versions/output meanings at public boundaries.
- Perform the final portability, race, release, documentation-link, and dirty-
  worktree audit without rewriting historical execution evidence.

Test-first slices:

1. Add end-to-end CLI/process tests that exercise the complete forest
   lifecycle from each supported selection context and initially fail at any
   remaining guard or single-root assumption.
2. Add a source audit/test that fails if named temporary forest guards remain
   or if topology-bearing JSON surfaces omit required fields.
3. Run the tutorial against local remotes and assert exact layouts, metadata
   and ignore ownership, actual commits, stable order, cleanup, and scalar
   output compatibility.
4. Audit documentation links, examples, help snapshots, schema-version
   assertions, stale/failure omission, Windows-safe paths, and historical-file
   immutability.

Verification:

- `go test ./internal/... ./cmd/wtree/... -count=1`
- `go test ./internal/... ./cmd/wtree/... -race -count=1`
- `sh tutorial/run-all-commands.sh`
- `! rg -n 'ForestUnsupported|forest support is not available|exactly one root repository' internal --glob '*.go'`
- Global definition-of-done commands.

Exit criteria: every command in the specification supports the same logical-
root forest contract, all public and persisted compatibility boundaries are
verified, current documentation is consistent, no temporary guard remains,
and the complete plan is independently approved.

## Execution log

Append one concise row only after a milestone is independently approved and
verified. Detailed active/resume/remediation evidence belongs exclusively in
the durable run ledger.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-20 | M00 | Focused domain/path/config/service tests, full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after R1–R5 remediation and independent re-review | Not created |
| 2026-08-21 | M01 | Focused config/service/CLI tests, full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after R1–R5 remediation and third-submission escalation re-review | Not created |
| 2026-08-21 | M02 | Focused discovery/service/CLI and in-flight cancellation tests, full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after M02-R1 cancellation remediation and independent re-review | Not created |
| 2026-08-21 | M03 | Focused clone planning/result/CLI and planning-race tests, full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after M03-R1/R2 guard and result-envelope remediation and independent re-review | Not created |
| 2026-08-21 | M04 | Focused Git/service/CLI clone execution and recovery tests, grouping-substitution regressions, combined and full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after M04-R1 grouping-containment remediation and independent re-review | Not created |
| 2026-08-21 | M05 | Focused plan/domain/service/CLI, transaction and safety-first worktree-identity tests, full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after R1–R4 remediation and user-authorized safety-first revised-scope review | Not created |
| 2026-08-22 | M06 | Focused discovery/service/CLI, shared-ignore and state-publication ownership tests, full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after M06-R1/R2 discovery-policy and safety-first receipt/CAS recovery remediation and independent re-review | Not created |
| 2026-08-22 | M07 | Focused inspection/collision/teardown/recovery and CLI tests, marker-preservation and publication-recovery regressions, full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after M07-R1–R4 ownership, Doctor topology, publication recovery, and preserved-content remediation with independent re-review | Not created |
| 2026-08-22 | M08 | Public forest lifecycle/tutorial, source guard, schema/output, portability/link/security audits, root-Git default-ancestor rollback tests, full normal/race suites, vet, format, build, release, `make check` tutorial, and diff audit passed | Approved after M08-R1 root-Git ancestor-ownership remediation and independent re-review | Not created |
