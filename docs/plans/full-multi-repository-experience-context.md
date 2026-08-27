# Implementation context — multi-repository composition loop and aggregate operations

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Multi-repository composition loop and aggregate operations implementation plan](full-multi-repository-experience.md)
Source specification: [Full multi-repository experience capability specification](../spec/full-multi-repository-experience.md)
Captured: 2026-08-22; updated: 2026-08-24

## 1. Purpose and precedence

This dump preserves implementation-relevant repository evidence, compatibility
constraints, seams, and risk analysis for the parent plan. It is not a second
specification. The source specification owns product behavior, the plan owns
the fixed delivery decisions and execution process, and both take precedence
over this context if they differ.

The plan is deliberately the first split delivery from a larger capability
specification. This dump therefore covers the P0/P1 implementation surface in
depth and records why the remaining capability groups cannot safely leak into
the run.

## 2. Current delivered baseline

The repository already implements the prerequisites on which this plan must
build:

- independent repository forests with stable repository IDs and a designated
  top-level base repository;
- logical project roots, grouped top-level repositories, immediate-parent
  nesting, centralized parent-first/metadata-first/child-first order, and
  effective-path validation;
- strict local project configuration v2 and strict portable manifest v2;
- portable init/clone with observed preflight commits, execution-time selected
  branch tips, private staging, actual-HEAD verification, registry/state
  publication, rollback/recovery, and credential redaction;
- source and generated workspaces, including the existing v1 imported-partial
  representation;
- create/checkout/remove/delete transactions, automatic nested-mount ignore
  protection, resolver-based project/workspace discovery, project registry
  management, status, and doctor; and
- stable application errors, human/JSON rendering, Cobra help, release scripts,
  and Ubuntu/macOS/Windows CI.

The current root command registers `project`, `init`, `clone`, `config`,
`create`, `checkout`, `remove`, `delete`, `import`, `doctor`, `list`, `status`,
`path`, and `repo`. There is no `update`, `sync`, `exec`, `fetch`, or `push`
command today.

## 3. Current schema and public compatibility boundaries

| Boundary | Current owner and version | Constraint for this plan |
|---|---|---|
| Portable manifest | `internal/config/portable_manifest.go`, strict version 2 | Keep byte/canonical semantics and all fields. Do not add locks, hooks, selections, transport modes, profiles, relative URLs, or a second meaning for v2. |
| Local project config | `internal/config/config.go`, strict version 2 | Existing required `project`, `logical_root`, `repositories`, `worktrees`, and `manifest.path/source` are sufficient for P0/P1. Update may replace a complete generation but may not widen the schema. |
| Domain project/workspace | `internal/domain`, current domain version 1 | Forest ordering/path rules are reusable. Keep current complete/`Partial` plus `MissingRepositoryIDs` meaning; do not add workspace kinds or provenance. |
| Workspace state | `internal/store/store.go`, strict JSON version 1 | Existing checkouts record branch, mount, resolved path, HEAD, and detached. Unknown fields are rejected, so update-specific retained/journal data must not be smuggled into v1. |
| Registry/recovery | `internal/store/store.go`, strict JSON version 1 | Preserve registry identity/config-path meaning and established recovery summary. Update-specific detailed backups require a separate private contract. |
| Workspace plan | `internal/plan/plan.go`, strict version 1 | Owns only create/checkout. Do not overload it with update/fetch/exec actions. |
| Clone plan/result and completed CLI JSON | `internal/service/clone_*`, version 2 | Reuse patterns and facts, not wire types or version numbers. Update gets a separate version-1 contract. |
| Status JSON/human | `internal/service/status.go`, `internal/cli/status.go` | Existing fields and the `REPOSITORY BRANCH MOUNT STATUS UPSTREAM` table are established. Findings return success when observation succeeds and status never fetches. |
| Doctor JSON/human | `internal/service/doctor.go`, `internal/cli/doctor.go` | Findings have code/severity/repository/message/fixable. New drift codes can be additive; the mutation allowlist must not broaden implicitly. |
| Errors/exits | `internal/service/error.go`, CLI/cmd mapping | Reuse existing categories and exit codes 0-9. No new finding-specific exit category is necessary. |

Strict decoding is important: existing tests reject unknown fields, unsupported
versions, multiple YAML/JSON documents, malformed values, and trailing JSON.
Any implementation that “just adds a field” to workspace or manifest v1/v2
would break the compatibility gates in specification section 10.

Backward compatibility with older `wtree` binaries is not required. Forward
repository compatibility is mandatory: after any successful mutation, the
resulting application must resolve and validate every retained workspace state,
must corroborate every retained repository by Git identity, and must preserve
the safety behavior of existing inspection and teardown commands. If that
cannot be proven before mutation, the operation must reject without changing
anything.

## 4. Existing ownership and reusable seams

### Resolution and project facts

- `service.Resolver.ResolveReadOnly` selects project/workspace without registry
  reconciliation and is the correct entry for status, doctor, exec, fetch, and
  push readiness.
- `Resolver.Resolve` may reconcile registry facts and therefore is inappropriate
  for dry-run or observational commands.
- `ResolveProject` exists for operations such as import that intentionally do
  not yet have workspace evidence.
- `Resolver.loadProject` validates local config, logical-root inversion,
  co-located portable manifest bytes, source paths, Git common-directory
  identity, and domain topology. Repository lookup must continue to use these
  identities rather than URL/path inference.
- `domain.Workspace.ResolveRepository` is the required checkout-path boundary;
  commands must not rebuild paths from names or mounts.

### Topology, paths, and state

- `domain.Project.ParentFirst`, `MetadataFirst`, and `ChildFirst` already own
  deterministic forest order. P0/P1 commands should filter those orders, not
  add new sorts.
- `domain.Project.EffectivePaths` owns mount/path calculation and overlap,
  containment, alias, and symlink safety.
- `service.WorkspaceStatePath`, `WorkspaceStateDirectory`, and
  `RecoveryRecordPath` centralize current public state locations.
- `store.Write*CAS`, `WriteRawCAS`, `WorkspaceBytes`, `RegistryBytes`, and the
  `fsutil.ReplacementCompleted` boundary already encode byte ownership and
  concurrent-generation protection. The update journal should reuse these
  primitives and their failure-injection style.
- `lock.Manager.ProjectLock` is the existing per-project mutation exclusion
  boundary.

### Clone and update-adjacent behavior

- `ManifestSourceLoader` normalizes local or HTTP(S) sources, enforces the
  one-MiB limit, follows the bounded redirect policy, and returns owned bytes.
  Its redaction helpers already cover credentials, query strings, diagnostic
  bounds, and source normalization.
- `ClonePlanner` separates immutable manifest bytes/digest, registry generation,
  destination facts, remote observations, ordered actions, and JSON-safe
  public data. Its private byte-copy and `Validate`/tamper tests are the closest
  plan pattern for update.
- `CloneExecutor` supplies the most relevant patterns for execution-time
  selected-ref fetch, actual-HEAD verification, parent-first staging,
  grouping-directory safety, identity/initial-root verification, tracked base
  manifest verification, committed parent ignore verification, CAS publication,
  owned-tree inventory, cancellation, cleanup, and incomplete recovery.
- `clone_safety.go`, `clone_registry_facts.go`,
  `publication_recovery.go`, and `registration_conflict.go` contain reusable
  capture/revalidation ideas. Avoid blindly exporting clone-private helpers if
  a smaller shared primitive can retain ownership clarity.
- Clone's `ObservedCommit` is diagnostic and execution fetches the live selected
  branch. Ordinary update must preserve this distinction. Exact commits belong
  only to later lock materialization.

### Current Git boundary

`internal/git.Git` is a deliberately broad injected interface. Relevant
implemented facts include:

- canonical common Git directory, top level, HEAD, current branch, worktree
  list, status/cleanliness, branch existence/merged/checked-out;
- ahead/behind, configured upstream, published-upstream verification, stable
  published repository facts, advertised selected-ref commit, initial commits,
  ancestry/identity containment, tracked-file bytes, and submodule detection;
- branch/worktree mutation plus clone, configured tracking-ref fetch, and
  execution-time tracking-branch checkout.

The adapter always uses argument arrays, a locale-neutral non-interactive
environment, bounded stderr, and credential redaction. New fast-forward,
fetch-only, and direct-process behavior must retain those properties. Adding a
method requires intentional updates to service fakes; creating a second raw
`exec.Command` Git path in a service would bypass important safety tests.

### Status and doctor

- `StatusService.Status` iterates `ParentFirst`, reconciles persisted checkouts
  with configured repositories and actual Git facts, filters managed child
  entries from parent dirt, and keeps cleanliness separate from structural
  status and upstream ahead/behind.
- Current summaries are `stale-state`, `missing`, `unknown-repository`,
  `mount-mismatch`, `detached`, `branch-mismatch`, `modified`, and `clean`.
- Status does not currently load the portable manifest as an independent
  compared generation or discover unmanaged repositories across the logical
  root. Those are the main P1 gaps.
- `DoctorService.Doctor` already checks registry/project source identity,
  discovered workspace checkouts, duplicates, unknown repositories, missing
  checkouts/worktree registrations, mount/branch/HEAD drift, and recovery.
- `DoctorService.Fix` is project-locked and currently repairs only verified
  path metadata and stale worktree registrations. New update/manifest findings
  must remain outside this allowlist.
- Doctor's filesystem walk is a useful starting point but the shared drift
  snapshot needs explicit containment, managed grouping, symlink, and current
  partial-workspace tests before multiple consumers rely on it.

## 5. Missing contracts the plan must add

### Shared drift snapshot

Today clone planning, status, doctor, resolver, and project inventory each
observe overlapping but differently scoped facts. Update cannot safely compose
their rendered results. It needs one service-owned snapshot that records:

- current tracked manifest bytes/digest and candidate bytes/digest;
- local config and manifest source/path;
- default workspace state and registry generation;
- declared/current/candidate repository sets and topology;
- actual checkout identities, canonical paths, branch/HEAD/cleanliness,
  upstream configuration, ignore evidence, and selected-ref observations;
- retained-unmanaged entries and update/recovery visibility; and
- the origin and time/generation of every observation needed for execution
  revalidation.

The snapshot must be immutable after construction. Update classifies it;
status and doctor consume local-only projections. A consumer must not silently
run Git again and combine a newer fact with the old snapshot.

### Update state and atomicity

An update spans mutations that the existing workspace transaction does not
cover: checked-out default branches, newly cloned repositories, local config,
tracked manifest, default state, registry, and retained-removal evidence. A
single filesystem rename cannot atomically commit all of them.

The parent plan therefore chooses a journaled transaction:

```text
preflight snapshot
  -> acquire project lock and revalidate
  -> write private operation journal + exact backups
  -> execute repository effects parent-first
  -> publish matching metadata generations with CAS
  -> verify complete postconditions
  -> remove journal/backups
```

On failure, reverse only effects whose owned new generations still match. A
clean reversal removes the owned journal. An incomplete reversal retains exact
evidence and produces the established rollback-incomplete summary. Resolver
mutators must refuse an active journal so a partially published generation
cannot be treated as ordinary authoritative state.

Removed repositories cannot remain in strict workspace state after the
candidate project no longer declares them. They also must not become
indistinguishable from unknown drift. A separate strict reconciliation record
is therefore necessary; it records only stable ID, canonical path/common Git
directory evidence, and the removing manifest digest, never URLs, timestamps,
or secrets.

### Aggregate results

Clone's result is clone-specific and workspace plans describe only
create/checkout. New commands need command-owned wire results that share a
small internal repository-fact vocabulary without forcing unrelated fields
into one public union. Tests should structurally decode exact version/status/
repository/failure combinations and verify order, redaction, and output-writer
behavior.

Aggregate exit classification follows the underlying failure, not merely the
fact that more than one repository was involved. Fetch transport/Git failures
remain `git`; validation and dirty-workspace failures retain those categories;
rollback-incomplete always wins. Only an `exec` child outcome and observed
push-readiness blockage use `conflict` when no more specific failure exists.

## 6. Command-specific implementation notes

### Update

- The stored source already exists as required local config
  `manifest.source`; `--from` can override it without a schema change.
- Local config also records `manifest.path`, presently `project.wtree.yml`.
  The loader verifies this file is co-located with the base config. Update must
  bind candidate bytes to the tracked file at the actual execution-time base
  HEAD, not merely accept bytes downloaded from another source. It must not
  write or stage that tracked file separately; normal base-branch fast-forward
  is its only successful update path.
- A new repository can reuse clone planning/execution verification but its
  destination is one absent effective mount inside an existing logical root,
  not a new whole-project destination. Staging and publication ownership must
  be narrowed accordingly.
- Existing checked-out branch fast-forward is not the same operation as clone's
  tracking checkout. It requires explicit ancestor proof, clean index/worktree,
  attached expected branch, upstream verification, hook suppression, and a
  safe inverse guarded by exact new ref/HEAD/identity facts.
- Mount changes are a hard preflight refusal. Moving a repository can change
  common-Git-directory identity and invalidate linked worktree administration;
  this plan must not experiment with repair.
- Generated workspaces remain untouched. Updating project topology while they
  exist may make their v1 state incompatible. Preflight must read and validate
  every current state, then reject any repository addition/removal while any
  non-default complete or imported-partial state exists. Existing repository
  parent, mount, default branch, clone remote/URL, and upstream contracts do not
  change in this plan. A pure source-checkout fast-forward may proceed because
  it does not reinterpret named state; postconditions must reload every state
  through the normal resolver/list path before committing success.

### Exec

- The Go process boundary should be injected for tests; Git's adapter is not a
  general arbitrary-command runner.
- The executable and arguments after `--` must remain separate tokens.
  Metacharacters, spaces, `$()`, redirects, and pipes are literal unless the
  user explicitly supplies a shell executable and its flags.
- Environment facts come from persisted workspace state corroborated with
  runtime identity/branch/HEAD immediately before each invocation.
- Bound captured output to prevent one repository from exhausting memory or
  producing unbounded JSON. The authorized M00 design retains the first and
  rolling last 64 KiB as inspection windows. At or below 128 KiB it rebuilds
  the exact original by total offsets, redacts the complete string once, and
  then selects the first and final 32 KiB. Above 128 KiB a bounded stateful
  scanner observes the full stream and records redaction evidence for retained
  bytes without concatenating the missing middle. Use the plan's exact
  middle-omission marker and expose truncation booleans.
- Overlap reconstruction is positional, never content-based: repeated output
  must not change which bytes belong to the original sequence. stdout and
  stderr have independent scanner/window state. Tests must distinguish an
  overlapping exact reconstruction from a real gap and cover credentials at
  process-read/final-cut boundaries plus ordinary URL and punctuation data.

### Fetch

- `git fetch` is not read-only: it changes remote-tracking refs and Git
  metadata. It is intentionally non-transactional and must report partial
  completion.
- Dry-run uses `AdvertisedCommit` against the configured URL and merge ref;
  it must not reuse a fetch command with a dry flag that still updates local
  metadata.
- Fetching explicit refspecs avoids accidental remote-HEAD behavior and makes
  different local/remote branch names testable.

### Status

- Existing upstream values are based on last-fetched local refs and must remain
  network-free. Fetch is the explicit refresh boundary.
- The compatibility gate requires the synchronized human table to stay
  byte-stable. New drift can be rendered as additional summary lines only when
  present.
- Current status returns a service error only when observation fails. Dirt and
  drift are successful facts; preserve that distinction.
- Current identity mismatch sets `unknownRepository`, renders
  `unknown-repository`, and makes upstream `n/a`. The new
  `identityMismatch`/drift facts are additive to those exact semantics, not a
  replacement classification.

### Push readiness

- The command name is intentionally `push`, but it must never call `git push`.
  Help and tests should make the non-publishing contract unmistakable.
- `PublishedUpstream` already compares local HEAD to the exact advertised
  configured upstream without fetching. Combined with local ancestry of the
  manifest identity roots, this can prove the current metadata commits are
  reachable from the exact published tip without updating refs.
- Transport/authentication failure means readiness could not be evaluated and
  is an operational error. A successful observation that finds local-only or
  dirty work is a deterministic readiness result with an overall failed status.

## 7. Test and fixture map

| Concern | Existing evidence to extend |
|---|---|
| Git commands, environment, errors | `internal/git/adapter_test.go`, `operations_test.go`, `portable_test.go`, `portable_internal_test.go`, `error_test.go` |
| Manifest strictness and URL safety | `internal/config/portable_manifest_test.go`, fuzz tests, path tests |
| Forest order/effective paths | `internal/domain/project_test.go`, `paths_test.go`, `internal/pathutil/*_test.go` |
| Clone observation/execution/rollback | `internal/service/clone_plan*_test.go`, `clone_execute_test.go`, `clone_safety_test.go`, `clone_registry_facts_test.go`, publication/recovery tests |
| Transaction failure injection | `internal/service/transaction_test.go`, `internal/transaction/*_test.go`, store atomic/publication tests |
| Resolver and state compatibility | `internal/service/resolve_test.go`, `load_project_internal_test.go`, `internal/store/store_test.go`, registry decode tests |
| Status compatibility | `internal/service/status*_test.go`, `internal/cli/status*_test.go` |
| Doctor findings/fix allowlist | `internal/service/doctor*_test.go`, `internal/cli/doctor_test.go` |
| CLI parsing/output/exit behavior | `internal/cli/root_test.go`, help/errors tests, `cmd/wtree/main_test.go`, `internal/render/*_test.go` |
| Full user flow | `internal/cli/e2e_test.go`, `automatic_ignore_e2e_test.go`, `tutorial/run-all-commands.sh` |
| Cross-platform/release | `.github/workflows/ci.yml`, `scripts/release-build_test.sh` |

New tests should follow existing package boundaries: internal seams may use
package-internal tests, public CLI behavior should invoke `cli.Execute`, and
process-boundary exit/JSON behavior belongs in `cmd/wtree` where applicable.

## 8. Verification environment and timing

- `go.mod` declares Go 1.26.5. CI installs Go 1.26.x and runs on Ubuntu,
  macOS, and Windows.
- The Makefile's default test timeout is 15 minutes. Service/CLI integration
  tests create many real temporary Git repositories and are materially slower
  than pure package tests; focused milestone regexes should precede full gates.
- CI checks formatting, vet, tests, race tests, binary build, cross-platform
  release layout, and safe reuse of a release directory.
- The full local verification command initiated during this context capture was
  manually interrupted after the command, CLI, config, discovery, domain,
  fsutil, Git, lock, pathutil, plan, and render packages passed; the service
  package had not completed, so this is not a clean full-suite baseline claim.
- At capture time the worktree contained unrelated staged/deleted entries
  `internal/service/forest_unsupported.go` and
  `internal/service/forest_unsupported_test.go`. They belong to the user and
  must not be restored, deleted from the index, or otherwise rewritten by a
  future plan run unless separately authorized.

## 9. Deferred compatibility gates

The parent plan must not absorb these decisions:

- Checkout remote materialization changes current fail-on-missing behavior and
  needs a focused workspace specification with explicit rollback provenance.
- Locked, mixed, partial, and omitted workspace kinds need a versioned stored
  model and transactional migration-or-rejection plus post-change validation;
  older-client readability is not required.
- Release locks need a strict object-format-neutral schema, base-tag anchor,
  digest binding, remote-availability workflow, and exact detached
  materialization contract.
- Hooks and relative/profile URLs cannot be added to strict portable manifest
  v2. Trust, retry, schema ownership, and forward repository usability require
  focused specifications; older-client readability is not required.
- Existing-checkout relocation can change common Git identity and invalidate
  linked worktrees. It needs separately authorized migration semantics.
- Adoption, selective materialization, shallow/partial transport, LFS, cleanup,
  coordinated publishing, and automatic recovery each introduce behavior not
  required to deliver the safe P0/P1 slice.

If implementation uncovers evidence that one of these is required for an
in-scope milestone, the correct action is to record an external scope blocker
and obtain a plan/specification change. It is not safe to choose a schema or
public behavior inside remediation.

## 10. First-dispatch checklist

Before dispatching M00, the main agent should:

1. Re-read the parent plan, specification sections 3-6 and 10-13, this context,
   repository instructions, milestone supervision, and run-ledger layout.
2. Inspect the current worktree and preserve unrelated user changes.
3. Create only `docs/ai/runs/full-multi-repository-experience.md` with the
   mandatory active M00 snapshot and complete plan-derived checklist.
4. Confirm the normal implementer/reviewer profiles are available and that the
   reviewer is read-only.
5. Run focused current package tests needed to distinguish a pre-existing
   failure from M00 changes, without claiming a full baseline until the whole
   required command completes.
