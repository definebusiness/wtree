# Lifecycle-hook tutorial

Lifecycle hooks are opt-in setup commands. Local `post-create` hooks are
trusted machine-local configuration, portable `post-clone` hooks require
per-clone authority, and shared `post-create` hooks remain inert until a user
installs them locally.

## Hands-on configuration

Start with a project initialized or cloned by `wtree`. Edit its generated
`.wtree.yml`, change the top-level version from `2` to `3`, keep every existing
field, and add a top-level `hooks` fragment like this:

```yaml
version: 3
# Keep the existing project, logical_root, repositories, worktrees, discovery,
# and manifest fields here.
hooks:
  post-create:
    - id: download-backend-modules
      repository: backend
      command: ["go", "mod", "download"]
      timeout: 5m
```

The `repository` field is optional and defaults to the configured base
repository. Commands are non-empty direct argument arrays, not shell strings;
use an explicit shell executable and arguments only when shell behavior is
needed. Hook IDs must be unique within an event, and an omitted timeout defaults
to one minute.

Validate and inspect the local declaration, then create a workspace:

```sh
wtree hooks list
wtree create feature/hook-demo
```

To intentionally bypass trusted local setup for one create, use:

```sh
wtree create feature/without-setup --no-hooks
```

To offer the local `post-create` event to other users, copy it into the tracked
portable manifest and commit that manifest yourself:

```sh
wtree hooks share post-create
git add project.wtree.yml
git commit -m 'Share post-create workspace setup'
```

On another clone, inspect the shared declaration before explicitly copying only
missing events into that clone's ignored local configuration:

```sh
wtree hooks list
wtree hooks install --missing
wtree hooks list
```

If an authorized hook fails, the workspace remains published. Inspect it, fix
the external cause, and resume only the matching incomplete run:

```sh
wtree status feature/hook-demo
wtree doctor feature/hook-demo
wtree hooks retry feature/hook-demo
```

Portable `hooks.post-clone` declarations use the same hook fields. Change the
portable manifest to version 3, preserve its existing project and repository
fields, and add a fragment such as:

```yaml
version: 3
# Keep the existing project and repositories fields here.
hooks:
  post-clone:
    - id: verify-cloned-project
      command: ["go", "test", "./..."]
      timeout: 10m
```

This declaration runs only when the individual clone command includes
`--run-hooks`:

```sh
wtree clone ./project.wtree.yml ./product --run-hooks
```

## Executable acceptance path

The offline acceptance path uses temporary local Git repositories, a tracked
`sh` fixture on Unix or tracked native `.exe` test-binary helper on Windows for
lifecycle ordering, and generated Go helpers for direct-process guarantees.
`.cmd` fixtures remain supplemental PATHEXT/availability coverage and are never
directly launched. It does not contact a network endpoint or read global Git
configuration. Run it from the repository root:

```sh
make tutorial-test
```

`make tutorial-test` preserves the legacy all-command fixture and then runs
one stateful cross-platform Go acceptance flow,
`TestLifecycleHookTutorialAcceptance`. It carries one temporary local-Git
fixture through authoring, sharing, clone observation, installation, failed
create, status, doctor, retry, bypass, and both clone-consent outcomes. It
asserts files, branches, HEADs, workspace state, durable records, and
human/JSON output at their public boundaries. The following matrix names the
additional focused evidence run by the same target.

| Stage | Executable evidence |
|---|---|
| Decode hook-free v2 and every v3 source/event combination | `TestLifecycleHookPublicContractMatrix` |
| Author and inspect a local declaration; share and install it | `TestHooksCommandsRenderVersionedResultsAndKeepJSONSeparateOnErrors` |
| Observe portable declarations in clone dry-run; verify unauthorized skip and explicitly authorized clone | `TestCloneV3PortableHooksDryRunAndUnauthorizedSkipPublicContracts` |
| Create, retain a published workspace after a hook failure, and verify its durable record | `TestCreateHookRunnerPersistsFirstSuccessAndStopsAtLaterFailure` |
| Intentionally bypass a valid local hook with `--no-hooks` | `TestCreateNoHooksValidatesAndCommitsWithoutHookAuthority` |
| Inspect and resume an incomplete run without rerunning the durable prefix | `TestHookRunnerResumesFailedAndFinalizingRecords`; `TestHookRetryUsesSingleInventoryCandidateAndRendersBoundedResult` |
| Update v2/v3 manifest content without executing shared declarations or changing local consent | `TestUpdatePublicationPreservesLocalV3HookConsentWithoutExecutingSharedContent` |
| Classify output, writer failure, timeout, cancellation, child cleanup, and direct-process behavior | `TestHookProcessClassifiesOutputTimeoutCancellationAndNonZero` |
| Reject URL credentials and preserve secret/output privacy in environments, forced stream boundaries, and durable records | `TestPortableHookCommandSyntaxIsElementAwareAndCrossPlatform`; `TestHookEnvironmentPortableAllowlistExcludesSecrets`; `TestHookProcessForcedBoundaryNeverLeaksCredentialContinuation`; `TestHookProcessForcedBoundaryRedactsNewlineTerminatedContinuations`; `TestHookRunRecordRoundTripAndPrivacy` |
| Serialize simultaneous retry/initial execution | `TestHookRunnerSerializesConcurrentSameEvent` |

## Specification traceability

The acceptance criteria in [the lifecycle-hook specification](../docs/spec/local-workspace-lifecycle-hooks.md#15-acceptance-criteria) map directly to executable evidence:

| Criteria | Evidence |
|---|---|
| 1–4: topology, dry-run, core failure, publication, durable retry | `TestLifecycleHookTutorialAcceptance`; `TestCreateHookRunnerPersistsFirstSuccessAndStopsAtLaterFailure` |
| 5: timeout, cancellation, launch/output/writer errors | `TestHookProcessClassifiesOutputTimeoutCancellationAndNonZero` and the forced-boundary tests in the tutorial target |
| 6–8: share/install/list atomic contracts | `TestHooksCommandsRenderVersionedResultsAndKeepJSONSeparateOnErrors`; focused hook-management gate |
| 9–10: portable authorization and v2/v3 compatibility | `TestCloneV3PortableHooksDryRunAndUnauthorizedSkipPublicContracts`; `TestLifecycleHookPublicContractMatrix` |
| 11: read-only setup diagnostics | `TestLifecycleHookTutorialAcceptance` status/doctor stage |
| 12: Linux/macOS/Windows process, locking, and atomic-write evidence | focused local gate plus the matching hosted CI matrix for the exact delivered tree |

The focused suite additionally maps every interruption/concurrency boundary:
preflight and publication in create/clone lifecycle tests; between-hooks and
record finalization in runner tests; share/install CAS in management tests;
update rollback in update-publication tests; and retry locking in runner and
inventory tests. Native Windows tests cover suspended Job ownership and
process-tree termination; Unix tests cover process-group cleanup. Atomic
record and configuration tests exercise pre-replacement failure, durable
replacement, and finalization removal. These platform-specific tests are
compiled and executed by the three-job hosted matrix.

The repository-wide focused hook gate adds the adversarial and platform
matrices that are intentionally more exhaustive than this tutorial:

```sh
go test ./internal/... ./cmd/... -run 'Test(Hook|LifecycleHook|Hooks|VersionTwo)' -count=1
```

Those tests prove that ambient and credential-derived values do not enter
durable records or JSON; literal command arguments are limited to documented
list/plan surfaces; output never becomes record or structured-result data; and
process, atomic-write, locking, cancellation, and retry boundaries remain
portable across Linux, macOS, and Windows. The hosted CI matrix reruns the
complete normal/race, vet, build, and release layout gates on those three
platforms when the exact delivered tree is available to Actions.

## Operating contract

Use local `.wtree.yml` version 3 `hooks.post-create` only for trusted,
idempotent setup. `wtree create --no-hooks` is the intentional bypass. Portable
`project.wtree.yml` version 3 `hooks.post-clone` requires `wtree clone
--run-hooks` for that invocation; `shared_hooks.post-create` is inert until
`wtree hooks install` copies it into local configuration. Hook commands are
direct argument arrays, not shell fragments. Portable hooks use a sanitized
environment. A separator-bearing portable executable must be source-relative,
tracked, and contained; a bare command uses sanitized `PATH` and Windows
`PATHEXT` when applicable. Durable records and execution-result/error JSON
never persist command output, executable paths, literal arguments, or
environment values. List and plan/dry-run inspection output intentionally
exposes configured/resolved executables and literal arguments. The integrated
tutorial uses a tracked `sh` fixture on Unix and a tracked native `.exe`
test-binary helper on Windows for lifecycle ordering; `.cmd` fixtures remain availability/PATHEXT-only.
Generated-Go-helper process tests cover direct
launch, cancellation, child cleanup, writer failure, and redaction.

After a hook failure, inspect `wtree status <workspace>` and `wtree doctor
<workspace>`, correct the cause, then run `wtree hooks retry <workspace>`.
Retry validates the exact configuration/manifest bytes, plan, and workspace
facts. It never starts a fresh run or reruns a durably completed hook.
