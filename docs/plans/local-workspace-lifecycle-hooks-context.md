# Implementation context — local and shared workspace lifecycle hooks

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Local and shared workspace lifecycle hooks implementation plan](local-workspace-lifecycle-hooks.md)
Source specification: [Local and shared workspace lifecycle hooks specification](../spec/local-workspace-lifecycle-hooks.md)
Captured: 2026-08-29
Captured repository head: `e907be5d562b` on `feat/lifecycle-hooks`

## 1. Purpose and precedence

This dump preserves the current-code evidence and integration hazards that an
implementer or reviewer would otherwise need to rediscover. It is not a second
specification. The source specification owns product behavior and the parent
plan owns scope, sequencing, verification, and supervision. Both take
precedence if this context becomes stale or conflicts with them.

The capture was made from a working tree that already contained the new idea,
specification, plan, related-specification link, and status-overview changes.
The commit named above is therefore a baseline locator, not a claim that the
captured documentation or later implementation is committed. Execution must
inspect the current shared filesystem and preserve unrelated changes.

## 2. Current baseline at a glance

| Boundary | Current implementation | Hook delivery consequence |
|---|---|---|
| Local project configuration | Strict YAML version 2 in `internal/config/config.go`; one accepted version; no hooks | Add a separate accepted v3 without changing v2 meaning or blindly changing the default emitted version. |
| Portable manifest | Strict canonical YAML version 2 in `internal/config/portable_manifest.go`; no extension blocks | Extend explicit canonical wire structs for v3 while retaining exact v2 bytes and validation. |
| Domain project/workspace | `internal/domain`; validated forest topology, base repository, source paths, resolved checkout paths | Keep hooks separate from domain and workspace state; correlate declarations to these authoritative objects in service planning. |
| Workspace plan/state | Plan v1 and store workspace v1 | Do not add hook consent or run progress to either format. Use an additive command result and a separate private run record. |
| Create transaction | `WorkspaceTransaction.Execute` owns project lock, revalidation, effects, result validation, state commit, and rollback | Hook execution must begin only after `Execute` returns and its deferred project unlock has run. Hook generation still needs locked revalidation before core mutation. |
| Clone transaction | `CloneExecutor.Execute` stages privately, validates, then holds registry and project locks through destination/state/registry publication | Validate portable executables in staging before rename; execute only after `Execute` returns and both deferred locks release. |
| Update | Immutable baseline plus journaled repository effects and CAS publication of manifest/local config/state/registry/reconciliation | Preserve local v3 hooks through every decode, clone, comparison, marshal, rollback, and recovery path; never install or run shared hooks. |
| Direct processes | `RunDirectProcess` uses direct argv, bounded redacted capture, Unix process groups, and Windows `taskkill` | Reuse termination and pipe-safety behavior, but do not reuse its environment/output policy unchanged. Hooks require two environment policies and live contextual stderr. |
| Locks | `internal/lock.Manager` provides registry/project locks plus a general path-based `Acquire` | Add a validated project/workspace/event lock path; do not hold the project lock while user code runs. |
| Persistent private state | `internal/store` strict JSON v1 types and atomic 0600 writes | Add an independently versioned hook-run record. Do not raise the shared workspace/registry/recovery `store.Version`. |
| Status/doctor | Additive drift projection plus read-only update/recovery inventory | Add conditional setup inventory without changing clean output, status exit semantics, or doctor repair authority. |

No `hooks` command group, hook schema, hook runner, hook-run store, event lock,
or working-tree “is this path tracked?” Git method exists at capture time.

## 3. Configuration and wire-format evidence

### 3.1 Local configuration

`internal/config/config.go` currently defines:

- `ProjectConfigVersion = 2` and `GlobalConfigVersion = 1`;
- `ProjectConfig` with project, logical root, repositories, worktrees,
  discovery, and manifest metadata;
- `LoadProject`, which strictly decodes, gives a special v1 rejection, accepts
  only `ProjectConfigVersion`, runs `requireLocalV2Fields`, and validates; and
- `ProjectConfig.Validate`, which also accepts only the single constant.

`internal/config/portable_manifest.go` contains `MarshalProject`, another exact
single-version guard. `internal/config/files.go` repeats that guard in
`WriteProjectFile` and then calls the common 0600 atomic YAML writer.

Do not simply change `ProjectConfigVersion` from 2 to 3. Many current call
sites and fixtures construct local configuration with that constant, and init
and clone must continue to emit hook-free v2. A low-churn implementation can
retain the existing v2/default constant, introduce an explicit hook-capable v3
constant, dispatch validation by `value.Version`, and share the required base
field validation without changing established v2 diagnostics.

The required-field inspector is named `requireLocalV2Fields` but most of its
checks are also v3 base-schema requirements. Refactor it carefully: v2 must
still reject `hooks` through strict decode, v3 must require the same base
fields, and tests may assert exact diagnostic wording.

### 3.2 Portable manifest

`internal/config/portable_manifest.go` currently defines
`PortableManifestVersion = 2`. `LoadPortableManifest` strict-decodes one Go
struct and `PortableManifest.Validate` accepts only that constant.
`MarshalPortableManifest` canonicalizes and then marshals a dedicated
`portableManifestYAML` representation; repository keys and initial commits
are sorted for byte stability.

As with local config, do not repurpose the v2 constant if v2 remains the
default output of init. Add explicit version dispatch and make the canonical
wire representation version-aware. A v2 marshal must not gain empty
`hooks: {}` or `shared_hooks: {}` fields, reordered keys, changed quoting, or a
different error. V3 retains repository canonicalization while preserving hook
array order.

### 3.3 Equality and copying hazards

Hook maps contain ordered slices and nested command slices. Every canonical,
snapshot, result, and retry constructor must deep-copy both levels. In
particular:

- `DriftSnapshotInput` copying in `internal/service/drift_snapshot.go`
  currently copies repositories and discovery ignores only;
- update execution-baseline cloning copies captured raw local bytes but parsed
  config helpers may acquire new slice/map aliases;
- canonicalization helpers currently assume only repository maps need copies;
  and
- CLI/service results must not expose mutable references subsequently used for
  fingerprints or persistence.

Canonical equality is semantic and default-aware. Stale-write and retry
checks are exact-byte comparisons. Do not substitute one for the other.

## 4. Project, workspace, and source authority

The resolver in `internal/service/resolve.go` turns `.wtree.yml`, registry,
workspace state, and Git identity into `domain.Project` and
`domain.Workspace`. The domain project retains:

- `ConfigPath`, the authoritative ignored local file;
- `LogicalRoot` and `BaseRepository`;
- every repository's registered `SourcePath` and canonical common Git
  directory; and
- forest ordering through `ParentFirst`.

Workspace state in `internal/store/store.go` retains workspace ID/name/root and
per-repository branch, mount, resolved path, HEAD, and detached state.
`RequireWorkspace` and resolver paths are the correct sources for retry and
status. A hook must not reconstruct paths from names or mounts.

The resolver currently returns parsed domain values, not the exact local
configuration bytes required for hook source fingerprints. Hook planning and
management therefore need a captured source-generation object containing at
least path, exact bytes, decoded config, and SHA-256. It must be correlated to
`Project.ConfigPath` and revalidated at the appropriate lock boundaries.

The tracked portable manifest path is derived from the base repository source
plus `local.Manifest.Path`. Management commands operate on the working tracked
file because they intentionally edit it without staging. Status/update use
committed or captured manifest authority for their existing purposes. Do not
silently reuse a committed-manifest reader when share/install needs current
working bytes, or a working-file reader when retry is bound to exact published
portable bytes.

## 5. Create integration seam

The public create flow lives in `internal/cli/plan.go`:

1. Resolve runtime paths and project read-only.
2. Resolve worktree root and mount overrides.
3. Call `WorkspacePlanner.Plan` for dry-run.
4. For a real create, reconcile the project.
5. Call `WorkspaceCreator.CreateWithResult`.
6. Render the plan or existing create/rollback diagnostics.

`internal/service/create.go` plans again and calls
`WorkspaceTransaction.Execute`. The transaction acquires the project mutation
lock, reruns the planner under that lock, runs reversible effects, validates
the complete result, writes authoritative workspace state, and returns. Its
deferred unlock runs as `Execute` returns. `CreateWithResult` returns only
after that point.

Consequences:

- A lifecycle coordinator can safely invoke the hook runner after
  `CreateWithResult` returns; putting hook steps into `createSteps` or
  `WorkspaceTransactionRequest.Steps` would violate rollback and lock rules.
- Hook preflight cannot live only in CLI. The exact local config/hook
  generation must participate in the real service flow and be rechecked under
  the existing project lock before the first core effect. The transaction
  currently revalidates only the workspace plan, so it needs a narrow injected
  generation-revalidation seam or an equivalent service-owned extension.
- After core return and before writing the run record, revalidate the source
  and new workspace state again. A mismatch at that point is setup-incomplete,
  not a reason to undo the committed workspace.
- Existing `CreateResult` contains the public plan and automatic-ignore
  evidence. Keep hooks outside `plan.WorkspacePlan`; an additive lifecycle
  result can embed/project existing plan fields with `hooks,omitempty` so
  hook-free JSON does not change.
- `--no-hooks` must bypass executable availability and execution but still
  pass strict config decode. Dry-run must perform full hook availability
  preflight without calling reconcile, acquiring a run lock, or writing a
  record.

Create tests already have extensive effect ordering, clean/incomplete rollback,
replacement attack, forest, ignore, cancellation, and observing-lock seams in
`create_test.go`, `create_forest_internal_test.go`,
`default_root_rollback_internal_test.go`, `transaction_test.go`, and
`internal/cli/automatic_ignore_e2e_test.go`. Extend those seams instead of
building a parallel create fixture framework.

## 6. Clone integration seam

Clone planning and CLI ownership are split across:

- `internal/service/clone_plan.go` for immutable plan and remote/preflight
  facts;
- `internal/service/clone_execute.go` for private acquisition, validation,
  publication, state, registry, and recovery;
- `internal/service/clone_result.go` for public planning/completion envelopes;
  and
- `internal/cli/clone.go` for flags, progress, and rendering.

`CloneExecutor.Execute` writes `.wtree.yml` inside private staging, inventories
the staged tree, then acquires registry and project locks. It revalidates
publication generations, renames staging to the destination, verifies final
identities, writes default workspace state and registry, and only then returns.
Both lock handles are deferred inside `Execute`, so post-clone execution must
be owned by a wrapper/coordinator after that method returns.

Portable executable availability cannot be fully proven by the initial
dry-run because repository content is absent. The executor needs a
pre-publication validation seam after all selected repositories and their
HEADs exist in private staging but before destination rename. That seam may
inspect and hash executable facts; it must never execute a hook. A validation
failure follows normal private-staging cleanup and publishes nothing.

Clone currently constructs a hook-free local v2 configuration from the
portable manifest. Preserve that behavior for portable v3: portable hooks and
shared hooks stay in the tracked manifest, are not copied into `.wtree.yml`,
and do not force a local version upgrade.

Clone plan/result JSON already has command-owned versioned envelopes. Add
portable executable hooks and inert shared definitions with `omitempty`
compatibility rather than changing the meaning of repository verification or
workspace state. `--run-hooks` must be included in CLI argument validation and
must select only portable `hooks.post-clone`.

## 7. Direct process boundary: reuse and required changes

`RunDirectProcess` in `internal/service/aggregate.go` is valuable but not a
drop-in hook runner. It already provides:

- exact argv launch with `exec.Command`, no shell;
- clean absolute working-directory validation;
- independent stdout/stderr pipes whose ownership avoids `os/exec` wait/EOF
  races;
- bounded head/tail retention and streaming redaction of URL userinfo and
  sensitive query values;
- Unix process-group termination and Windows `taskkill /T /F` with fallback;
- cancellation cleanup bounds; and
- clear separation of start errors, context errors, and non-zero exit codes.

It also currently:

- applies `sanitizedDirectProcessEnvironment`, which discards ordinary
  inherited variables, forces empty home/XDG, disables Git configuration, and
  supports only the existing exec-command `WTREE_` keys;
- forces `LC_ALL=C` and `LANG=C`;
- buffers/redacts child output and returns it instead of streaming contextual
  output to the command's stderr; and
- has no per-invocation timeout field because callers supply the context.

Trusted local hooks require full ordinary environment inheritance plus
reserved-key replacement. Portable hooks require the specification's distinct
allowlist, including locale wildcard handling and case-insensitive Windows
matching. Neither matches the existing exec policy exactly. Preserve exec
behavior and tests; extract or extend only shared process mechanics behind a
new injected request policy. Do not weaken `wtree exec` sanitization to make
hooks work.

Likewise, do not merely print the existing retained `Stdout`/`Stderr` after the
process exits: the hook contract requires live contextual stderr and no
persisted/result output. Add injected stream sinks while retaining pipe
draining, writer-error cancellation, redaction, and process-tree cleanup.
JSON mode still uses stderr for hook stream output and reserves stdout for one
JSON value.

Bare executable preflight must honor the effective event `PATH` without
mutating the parent process environment. `exec.LookPath` alone reads the
current process environment; provide an injected resolver that accepts the
effective path list. Portable syntax checks must recognize Unix and Windows
absolute/home/separator forms independent of the host performing `share`.

Platform availability differs: Unix requires a suitable regular executable;
Windows resolution depends on executable suffix/PATHEXT rules. Required tests
should use Go helper processes or generated native launchers and keep shell
fixtures supplemental.

## 8. Durable records, atomic files, and locking

`internal/store/store.go` uses one shared `Version = 1` for registry,
workspace, and rollback recovery records. Each decoder disallows unknown JSON
fields. Writes marshal indented JSON with a trailing newline and use 0600
atomic replacement through `internal/fsutil`.

Add a separately named `HookRunRecordVersion = 1`; do not change `store.Version`
or add hook fields to existing records. The store layer should own strict
encode/decode, internal consistency validation, and atomic byte publication.
Service owns plan/source/state fingerprint comparison and retry decisions.

`fsutil.WriteFileAtomicModeWithHook` creates a colocated temporary, chmods,
writes, syncs, closes, replaces, and syncs the directory. A failure after
replacement is represented by `fsutil.ReplacementCompleted`. Record and
management callers must distinguish “old generation retained” from “new
complete generation installed but durability postcondition failed”; tests
already expose every named step.

`lock.Manager.ProjectLock` validates the project ID and locks
`projects/<project>/project.lock`. The general `Manager.Acquire` can lock a
specific path but performs no ID/path validation. The new hook event lock
should derive only from already validated project/workspace/event identifiers,
create private parents, and use a deterministic sibling path that record
inventory ignores. Never pass raw CLI text into a lock path.

The run record state sequence is:

```text
absent
  → running(next=0)
  → running(next=N after each durable success)
  → failed(next=N) on a known failure
  → finalizing(next=len(hooks))
  → absent
```

An atomic-write failure before replacement retains the prior state. A
post-replacement error requires reading and validating the installed complete
generation before deciding the reported state. If removal after finalizing
fails, retry validates and removes only; it must not start a process.

The project lock is intentionally absent during this sequence. The event lock
prevents duplicate same-event execution, while exact source/state revalidation
before every launch detects concurrent authoritative changes.

## 9. Share/install and update publication

Share/install should use one immutable service plan containing captured file
paths, bytes/digests, decoded values, comparison states, and exact target
bytes. Mutation then follows the established pattern:

```text
capture and validate
  → acquire project mutation authority
  → recheck active update journal and exact generations
  → CAS at atomic before-rename boundary
  → validate installed complete generation
```

Use `acquireProjectMutationAuthority` rather than a config-only side lock so
management cannot race update publication. No-op and rejected operations must
not acquire a write, upgrade a version, alter timestamps, or leave temporary
files. Share writes only the tracked portable manifest. Install writes only
the ignored local config.

There is no current Git-interface method for “this working-tree path is
tracked.” `Git.TrackedFile` reads content at a commit and is used for committed
manifest authority. Sharing a working definition needs a focused Git fact,
normally a literal-path `git ls-files --error-unmatch -- <repo-relative-path>`
contract plus physical containment/file checks. Add it to fakes and real-Git
tests without broadening unrelated clone APIs.

Update's `updatePublicationTargets` decodes the exact baseline local config,
mutates repository/project/manifest fields, and marshals a new local file. If
`ProjectConfig` gains hooks, this code can preserve them naturally only if:

- load/validation accepts v3;
- repository-map edits do not overwrite the hook field;
- every snapshot/deep-copy path retains nested hook slices;
- `MarshalProject` emits the same hook semantics and correct version; and
- rollback/recovery continues restoring exact captured bytes.

Candidate portable hook/shared changes are carried in candidate manifest
bytes and tracked-manifest Git effects. They must not be projected into local
hooks. Existing update generation hashes already bind exact local and
candidate bytes; extend validation rather than introducing a second update
transaction.

## 10. CLI, JSON errors, status, and doctor

`internal/cli/root.go` registers all top-level commands and centralizes error
classification. Add one `newHooksCommand` child; keep management subcommand
flag rejection explicit and consistent with existing help tests.

The root execution boundary renders JSON errors after a command returns.
`render.JSONError` currently emits only `success`, `error.code`,
`error.message`, and optional clean-rollback detail. A hook command must not
write a partial JSON result and then return an error, because the root would
write a second JSON value. Extend the structured error boundary with typed
setup-incomplete details, or an equivalent single-envelope mechanism, while
preserving exact legacy envelopes when those details are absent.

Human hook output belongs on stderr. Existing create/clone success output is
on stdout; verbose core progress is already on stderr. Writer failures are
caller-visible errors and must cancel/stop later hooks without being
misclassified as a child exit.

`StatusService.StatusWithDataDir` currently projects checkout facts and
optional local manifest/update/recovery drift. Clean human output is one
repository table; the secondary “Local drift” table appears only when drift
exists. Add a separate conditional setup projection rather than marking Git
repositories dirty or changing observation exit behavior.

`DoctorService.Doctor` reads registry, sources, workspace paths, recovery, and
shared drift evidence. `doctor --fix` has a narrow allowlist for existing safe
repairs. Hook findings are non-fixable and should be appended through a shared
read-only hook-record inventory also used by status/retry. Do not make malformed
hook state prevent doctor from reporting all other independent findings.

Record inventory must reject unsafe names, symlinks, unknown files, malformed
JSON, wrong project/workspace/event binding, impossible indexes, and unsupported
versions without executing or rewriting anything. Normal absence must be
cheap and silent.

## 11. Suggested service-owned data flow

The exact file names may change during implementation, but dependency
direction should remain:

```text
captured config/manifest generation
        + domain project/workspace or core plan
        + injected Git/filesystem/process facts
                         │
                         ▼
                 immutable HookPlan
                         │
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
   CLI dry-run      durable runner   canonical digest
                                       + exact source/state digests
                                               │
                                               ▼
                                         retry validation
```

Useful service result distinctions are:

- core planned, hooks applicable;
- core completed, hooks completed;
- core completed, hooks intentionally skipped;
- core completed, setup incomplete;
- management changed/unchanged/skipped/conflicting; and
- retry resumed/finalized/rejected as missing, locked, stale, or invalid.

Do not encode these distinctions by parsing messages. Use typed values and
stable error/failure kinds, with all arbitrary child text excluded from the
durable and JSON contracts.

## 12. Test assets and minimum fixture matrix

Prefer existing helpers in `internal/testutil`, local bare remotes, injected
Git/process adapters, temporary data roots, and the repository's isolated Git
environment. Minimum topology fixtures are:

1. one base repository mounted at `.`;
2. a plain logical root with sibling top-level repositories and a non-dot base;
3. at least three nested repository levels;
4. workspace-specific mount overrides; and
5. spaces, Unicode, mixed separators, and Windows case/path variants.

Minimum declaration/source fixtures are:

1. local absolute executable;
2. local ignored source-relative executable;
3. portable tracked source-relative executable;
4. bare executable resolved through an injected PATH/PATHEXT;
5. omitted repository and timeout defaults;
6. multiple ordered hooks with a later failure; and
7. shared missing/identical/conflicting definitions under every install mode.

Every mutating path needs inventories before and after covering:

- local config and tracked manifest exact bytes and mode;
- registry, workspace state, update/recovery/hook records, and locks;
- branches, HEADs, worktree registrations, paths, and ignore updates;
- hook marker files/process counts; and
- temporary files and private staging directories.

Use the current direct-process helper tests for descendant processes, inherited
writers, redaction-window boundaries, cancellation, and Windows termination.
Add hook-specific stream/environment tests without weakening exec tests.

## 13. Milestone-to-edit-surface map

| Milestone | Primary existing surfaces to inspect or extend |
|---|---|
| M00 schemas | `internal/config/config.go`, `portable_manifest.go`, `files.go`, config/portable tests and fuzzers |
| M01 management | new service/CLI hook files; `internal/cli/root.go`; `internal/git/adapter.go`; `internal/lock`; `internal/fsutil`; config writers/tests |
| M02 runner | `internal/service/aggregate.go`, `process_unix.go`, `process_windows.go`; `internal/store/store.go`; lock/process/store tests |
| M03 create | `internal/cli/plan.go`; `internal/service/plan.go`, `create.go`, `transaction.go`; create/forest/rollback/CLI tests |
| M04 retry/diagnostics | `internal/service/resolve.go`, `workspace.go`, `status.go`, `doctor.go`, drift fallback/inventory; status/doctor/CLI tests |
| M05 clone/update | `clone_plan.go`, `clone_execute.go`, `clone_result.go`, `internal/cli/clone.go`; `drift_snapshot.go`, `update_*`; clone/update tests |
| M06 acceptance/docs | `internal/cli/root.go`, `howto.go`, help tests, `README.md`, `docs/INSTALL.md`, `docs/TROUBLESHOOTING.md`, tutorial and release tests |

New hook files should remain grouped by responsibility (schema, planning,
management, runner, record inventory) rather than accumulating all behavior in
one large service file. Exact decomposition remains subordinate to the plan's
architecture boundaries.

## 14. Approaches to avoid

- Do not bump the single v2 constants and repair breakage by making every new
  init/clone emit v3.
- Do not add optional hook fields to v2 structs and rely on `omitempty`; strict
  version meaning, not serialized emptiness, is the compatibility boundary.
- Do not put hook definitions in `domain.Project`, hook progress in workspace
  state, or hook execution inside transaction/rollback step lists.
- Do not hold registry/project mutation locks across arbitrary child code.
- Do not run portable executable validation only after public clone
  publication when private staged content was available earlier.
- Do not weaken `RunDirectProcess` sanitization or make `wtree exec` inherit the
  full environment.
- Do not retain child output “for diagnostics”; the specification deliberately
  keeps it out of records and JSON.
- Do not compare raw YAML for install/share identity or canonical hook objects
  for stale retry/CAS. Each comparison has a different purpose.
- Do not use a shell to implement direct commands, tracked-file checks, path
  lookup, or Windows process control.
- Do not let `doctor --fix`, status, list, or dry-run create directories,
  acquire mutation locks, delete records, or execute hooks.
- Do not stage or commit the manifest after share, and do not treat
  `--run-hooks` as authorization for `shared_hooks`.

## 15. Useful implementation searches

```sh
rg -n "ProjectConfigVersion|requireLocalV2Fields|MarshalProject|WriteProjectFile" internal
rg -n "PortableManifestVersion|portableManifestYAML|MarshalPortableManifest" internal
rg -n "WorkspaceTransaction|CreateWithResult|createSteps|ValidateResult|Revalidate" internal/service internal/cli
rg -n "publication-lock|destination-rename|state-write|registry-write|CloneExecutionResult" internal/service internal/cli
rg -n "RunDirectProcess|sanitizedDirectProcessEnvironment|terminateDirectProcess|boundedProcessStream" internal/service
rg -n "ProjectLock|RegistryLock|Manager.*Acquire|WriteRawCAS|ReplacementCompleted" internal
rg -n "LocalConfigBytes|cloneDrift|MarshalProject|updatePublicationTargets" internal/service
rg -n "StatusWithDataDir|applyStatusFallbackDrift|DoctorFinding|doctorFallbackFindings" internal/service internal/cli
rg -n "JSONError|ErrorDetails|classifyError|ExitCode" internal/render internal/cli internal/service
rg -n "TrackedFile|ls-files|EvalSymlinks|CanonicalPotentialPath" internal/git internal/service internal/pathutil
```
