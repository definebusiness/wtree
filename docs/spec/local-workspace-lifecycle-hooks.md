# Local and shared workspace lifecycle hooks specification

Status: planned
Source idea: [Machine-local and shared workspace lifecycle hooks](../ideas/local-workspace-lifecycle-hooks.md)
Implementation plan: [Local and shared workspace lifecycle hooks implementation plan](../plans/local-workspace-lifecycle-hooks.md)
Related capability specification: [Full multi-repository experience §8.2](full-multi-repository-experience.md#82-lifecycle-hooks)

## 1. Purpose

This specification defines trusted machine-local workspace hooks, inert
portable hook sharing, and explicitly authorized portable clone hooks. The
initial delivery makes a newly created repository forest usable with local
ignored files without asking a hook to rediscover `wtree` topology.

The core workspace or clone transaction remains authoritative. Hooks execute
only after that transaction has published and released its project mutation
lock. Hook failure therefore means that the Git workspace exists and is valid
but local setup is incomplete; it never rolls the workspace back.

## 2. Scope

The initial delivery includes:

- local `post-create` hooks in `.wtree.yml`;
- portable, explicitly authorized `post-clone` hooks in
  `project.wtree.yml`;
- portable `shared_hooks.post-create` definitions that are inert until a user
  installs them locally;
- `wtree hooks list`, `share`, `install`, and `retry`;
- complete hook planning in create and clone dry-run output;
- sequential direct process execution with authoritative environment values,
  cancellation, and timeouts;
- durable incomplete-run records and safe resume;
- additive status and doctor diagnostics; and
- version-three local and portable schemas while retaining hook-free
  version-two read compatibility.

`post-checkout` and `post-update` are reserved event names but are not accepted
in either schema in this delivery. Pre-operation, removal, fresh-run, and
single-hook run commands require later specifications.

## 3. Trust and source model

There are three distinct definition sources:

| Source | Accepted event | Consent and execution policy |
|---|---|---|
| Portable `hooks` | `post-clone` | Executed only by clone with `--run-hooks` |
| Portable `shared_hooks` | `post-create` | Never executed from the manifest; copied only by explicit install |
| Local `.wtree.yml` `hooks` | `post-create` | Executed by create unless `--no-hooks` is supplied |

Reading, cloning, fetching, or updating a portable manifest is not consent to
execute `shared_hooks`. `--run-hooks` authorizes only portable `post-clone` for
that clone invocation. Installing a shared definition into the ignored local
configuration is machine-local consent, after which the installed copy is an
ordinary local hook.

Local hooks are trusted programs. `wtree` validates and bounds their launch,
but does not sandbox them, restrict their filesystem or network access, or
undo their side effects. Documentation must tell hook authors to make hooks
idempotent.

## 4. Versioned configuration contracts

### 4.1 Local configuration

Local project configuration version two keeps its existing strict schema and
meaning. A new binary continues to read and write hook-free version-two files.
Unknown fields, including `hooks`, remain invalid in version two.

Local version three has the same required project, logical-root, repository,
worktree, discovery, and manifest fields as version two and adds an optional
top-level `hooks` mapping. Only version three may contain local hooks. A
version-three file without hooks is valid, although normal init and clone
continue to emit version two when no local-only version-three data is needed.

`wtree hooks install` upgrades a version-two local file to version three only
when it actually installs at least one event. A no-op install does not rewrite
or upgrade the file. Ordinary update preserves a version-three local hook
mapping semantically and never installs, changes, or removes hooks in response
to portable `shared_hooks` changes.

### 4.2 Portable manifest

Portable manifest version two likewise keeps its exact existing schema,
canonical encoding, and meaning. Version three retains every version-two
field and adds optional top-level `hooks` and `shared_hooks` mappings. A
version-two document containing either field is invalid.

`wtree hooks share` upgrades the tracked manifest from version two to version
three only when it adds the first shared event. A no-op or rejected share does
not rewrite it. Existing init behavior may continue to produce version two.
Clone and update accept both versions; version-two regression fixtures must
remain byte-stable.

Local and portable version numbers are independent. Loading a version-three
portable manifest does not itself upgrade `.wtree.yml` and does not install
shared hooks.

### 4.3 Hook declaration

Each event value is a non-empty ordered sequence:

```yaml
hooks:
  post-create:
    - id: configure-spring-application
      repository: backend
      command: [".wtree-hooks/configure-spring-worktree"]
      timeout: 60s
```

Each hook has these fields:

| Field | Rule |
|---|---|
| `id` | Required portable ID, unique within its event |
| `repository` | Optional repository ID; omission selects the configured base repository |
| `command` | Required non-empty sequence; element zero is the executable and remaining elements are literal arguments |
| `timeout` | Optional positive Go duration; default `60s`, maximum `24h` |

Unknown hook fields, unknown event names, empty event lists, duplicate IDs,
unknown repositories, an empty executable, NUL or newline in any command
element, and invalid or excessive timeouts are validation errors. Hook order is
declaration order. Repository defaulting and timeout defaulting happen before
comparison, planning, fingerprinting, or rendering.

Commands are direct argument arrays. `wtree` performs no shell parsing,
variable interpolation, globbing, or command substitution. Shell behavior is
available only when the declaration explicitly invokes a shell.

### 4.4 Canonical equality

Canonical event equality compares the event name and ordered hooks after
repository and timeout defaults are applied. For each hook it compares the
ID, effective repository ID, exact command elements, and timeout nanoseconds.
YAML comments, mapping order, quoting, and whether a default was explicit do
not affect equality. Hook order does affect equality.

Canonical equality is used only for list/share/install comparison and hook
plan fingerprints. Exact captured file-byte digests are separately used to
reject stale writes and retries.

## 5. Command and path resolution

For every effective repository-scoped hook:

- a bare executable name is resolved with `PATH` from the event's effective
  environment policy;
- an absolute executable path is used as written for a local hook and is
  forbidden in portable `hooks` or `shared_hooks`;
- a relative executable containing either platform path separator is cleaned,
  resolved beneath the selected repository checkout, and rejected if it
  escapes that checkout; and
- the process working directory is the selected target repository checkout.

For local `post-create`, source-relative executables resolve against the
repository's registered source checkout. The target working directory may not
exist during initial planning, but its authoritative final path is already in
the immutable workspace plan. Preflight requires the source executable to be
available before core workspace mutation.

For portable `post-clone`, the executable resolves inside the privately staged
selected repository. Clone validates it before public publication; execution
still occurs only after publication. A clone dry-run made before acquisition
reports availability as `deferred` while fully validating syntax and the
planned path.

When sharing a source-relative executable, the resolved file must be tracked
by the selected source repository. Portable command elements may not contain
absolute or home-relative filesystem paths, file URLs, URL user information,
or control characters. The schema has no environment field and never copies
ambient environment values. Because arbitrary literal credentials cannot be
identified reliably, the command documentation must require review of literal
arguments before sharing.

Preflight checks regular-file and platform executability rules through the
same process adapter used for launch. Symlink and containment checks use the
repository's existing physical-path safety conventions. Planning and
validation never execute the program.

## 6. Hook environment

Local `post-create` hooks inherit the invoking process environment. Portable
`post-clone` hooks use a sanitized environment because their definitions came
from portable project data, even after explicit execution authorization. The
portable allowlist is `PATH`; locale variables `LANG`, `LC_ALL`, and `LC_*`;
temporary-directory variables `TMPDIR`, `TMP`, and `TEMP`; and the Windows
process-launch variables `PATHEXT`, `SYSTEMROOT`, `WINDIR`, and `COMSPEC`.
Matching is case-insensitive on Windows. `HOME`, credential variables, Git
configuration variables, and all other ambient entries are absent from the
portable environment.

Immediately before either event launches, `wtree` replaces every variable in
its reserved `WTREE_` set with authoritative values and removes duplicate
inherited entries. Users cannot override these values through the parent
environment.

| Variable | Value |
|---|---|
| `WTREE_HOOK` | `post-create` or `post-clone` |
| `WTREE_OPERATION` | `create` or `clone` |
| `WTREE_PROJECT_ID` | Stable project ID |
| `WTREE_PROJECT_NAME` | Human-readable project name |
| `WTREE_BASE_REPOSITORY_ID` | Configured base repository ID |
| `WTREE_WORKSPACE_ID` | Storage-safe workspace ID |
| `WTREE_WORKSPACE_NAME` | User-facing workspace name |
| `WTREE_SOURCE_LOGICAL_ROOT` | Absolute registered source logical root |
| `WTREE_TARGET_LOGICAL_ROOT` | Absolute resulting workspace logical root |
| `WTREE_REPOSITORY_ID` | Effective hook repository ID |
| `WTREE_SOURCE_REPOSITORY` | Absolute registered source checkout |
| `WTREE_TARGET_REPOSITORY` | Absolute resulting checkout |
| `WTREE_BRANCH` | Validated checkout branch |
| `WTREE_HEAD` | Validated full object ID |

For `post-create`, source values come from the registered project and target
values from the newly published workspace. For `post-clone`, the published
default workspace becomes the registered source, so the source and target
logical roots and repository paths are identical. The workspace ID and name
are both `default`.

Dry-run, list, JSON, logs, errors, and durable records never include the
ambient or sanitized environment. Empty or inherited secret values are not
persisted.

## 7. Planning and execution boundaries

### 7.1 Local `post-create`

Create follows this order:

1. Strictly load the selected local configuration and declarations.
2. Build the complete core workspace plan and hook plan.
3. Resolve and validate every hook executable, target working directory,
   argument, timeout, repository, branch, and planned HEAD.
4. Revalidate and execute the existing core transaction.
5. Validate all resulting repository identities, mounts, branches, and HEADs.
6. Publish authoritative workspace state and release the project mutation
   lock.
7. Acquire the dedicated workspace/event hook-run lock, create its durable run
   record, and execute hooks sequentially.
8. Remove the completed record and report success, or retain it and report a
   valid workspace with incomplete setup.

A core failure never creates a hook-run record or executes a hook. Hooks are
not transaction steps and never run during rollback.

`--no-hooks` still requires a valid local schema but skips executable
availability checks and execution. It reports an intentional skip and creates
no incomplete record. `--dry-run` performs full hook preflight, renders every
planned hook, and has no core, hook, lock, or record side effects.

### 7.2 Portable `post-clone`

Clone dry-run always lists portable `post-clone` declarations and never runs
them. A real clone without `--run-hooks` publishes the valid project, reports
that portable hooks were not authorized, and creates no incomplete record.

With `--run-hooks`, clone validates hook executables in private staging before
publication. After the existing clone transaction publishes and registers the
default workspace and releases its mutation lock, it uses the same durable
runner as create. Core clone failure or pre-publication hook preflight failure
runs no hook.

`--run-hooks` never runs `shared_hooks`. Clone does not accept `--no-hooks`.

### 7.3 Ordering, cancellation, and concurrency

Hooks run one at a time in declaration order. The first failure, timeout,
launch error, context cancellation, or record-publication error stops the
event; no later hook starts.

A dedicated lock for one project, workspace, and event prevents simultaneous
initial or retry execution of the same run. It is not the project mutation
lock and is held while user code runs. Immediately before each hook starts,
the runner revalidates the bound configuration/manifest and workspace-state
generations. Concurrent changes cause a safe incomplete result rather than
execution from mixed generations.

Timeout uses a child context for the individual hook. Cancellation follows the
repository's existing platform process-tree termination behavior. A timeout
is distinct from a normal non-zero exit in human and JSON diagnostics.

## 8. Dry-run and public results

Create and clone dry-run add an ordered `hooks` projection without removing or
retyping existing fields. Each entry contains:

- source (`local` or `portable`), event, hook ID, and effective repository;
- working directory;
- configured executable and resolved executable or deferred availability;
- literal arguments and timeout; and
- execution policy (`automatic`, `requires-run-hooks`, or `inert`).

No environment values are present. Shared hooks may be shown in clone planning
as inert metadata but are never included in an executable sequence.

Successful create and clone results report `hooksCompleted`, `hooksSkipped`,
and the ordered completed IDs only when hooks are applicable. Existing output
for hook-free version-two projects remains unchanged. A hook failure returns a
typed non-zero application error whose structured details identify the core
operation as completed, setup as incomplete, event, hook ID, repository,
failure kind, exit code when available, timeout, and retry command.

Hook stdout and stderr are streamed to command stderr with hook/event context;
stdout remains reserved for normal human results or one valid JSON document.
Neither stream is copied into JSON or durable state. Diagnostics produced by
`wtree` are bounded and redact manifest-source credentials using existing
redaction rules.

## 9. Durable run record and retry

### 9.1 Record contract

The private version-one record lives at:

```text
<data-dir>/projects/<project-id>/hooks/<workspace-id>/<event>.json
```

It contains only:

- version, project ID, workspace ID and name, operation, event, and source;
- SHA-256 of the exact local configuration or portable manifest bytes;
- canonical hook-plan SHA-256 and exact workspace-state SHA-256;
- ordered hook IDs, completed hook IDs, and next index;
- state (`running`, `failed`, or `finalizing`);
- bounded failure kind, failed ID, repository ID, exit code or timeout; and
- created and updated UTC timestamps.

It never contains command elements, executable paths, environment values,
stdout, stderr, or arbitrary child-process error text. Files and parent
directories use the repository's private state permissions and atomic,
durable replacement helpers.

The runner writes `running` before the first process starts and after every
durably recorded success. It writes `failed` for a known failure. After the
last success it writes `finalizing` before removing the record. If final
removal fails, retry performs only validation and cleanup; it does not rerun a
hook.

An interruption after a hook has side effects but before success is durably
recorded can cause that hook to run again. This is why idempotence is required.

### 9.2 `wtree hooks retry <workspace>`

Retry resolves the workspace through normal project selection and requires
exactly one valid incomplete record for the supported event. It reacquires the
dedicated lock, reloads the source bytes, rebuilds the plan, and requires all
three recorded digests and all ordered IDs to match. It also revalidates the
current checkout identity, branch, HEAD, and path facts.

On a match, retry resumes at `nextIndex`; it never reruns an ID recorded as
completed. A stale, malformed, unsupported, mismatched, or already locked
record causes no hook execution or record mutation and reports the safe next
action. There is no implicit fresh run if no record exists and no `--force`
bypass. A future fresh-run command requires a separate specification.

For local `post-create`, the source digest binds exact `.wtree.yml` bytes. For
portable `post-clone`, it binds exact tracked manifest bytes. Thus any source
change, including an unrelated reserialization, requires an explicit future
fresh run rather than guessing whether resume is safe.

## 10. Hook management commands

### 10.1 `wtree hooks list`

List is read-only and groups definitions as `portable`, `shared`, and `local`.
For each event it renders ordered IDs, effective repository, command, timeout,
execution policy, and pairwise state (`missing`, `identical`, or `conflicting`)
where another source supports comparison. Human and JSON output contain the
same facts. JSON uses a top-level schema version and deterministic ordering.

Portable `post-clone` has no local comparison. A malformed source makes list
fail rather than presenting partial definitions as trustworthy.

### 10.2 `wtree hooks share <event> [--force]`

The only accepted event is `post-create`. The command copies the complete
ordered effective local event into portable `shared_hooks` after portability
and tracked-executable validation.

- Missing shared event: add it.
- Canonically identical event: report unchanged and do not rewrite.
- Differing event: fail without mutation unless `--force` is present.
- `--force`: replace only the selected shared event.

`post-clone`, reserved future events, absent local definitions, unbound or
changing manifest generations, and non-portable declarations fail without
mutation. A successful change uses the project mutation lock, exact-byte
compare-and-swap, canonical manifest serialization, and atomic durable
replacement. It never stages or commits the manifest.

### 10.3 `wtree hooks install [--force | --missing]`

Install compares every portable shared event before writing anything.

- Default: any differing local event is a conflict and the whole command
  fails without mutation.
- `--force`: replace local definitions for all shared event names while
  preserving unrelated local-only events.
- `--missing`: add only absent local events and report existing identical or
  conflicting events without changing them.
- Identical definitions: always reported as unchanged.

`--force` and `--missing` are mutually exclusive. A manifest with no shared
events is a successful no-op. The command uses one immutable comparison plan,
the project mutation lock, exact-byte revalidation, and one atomic replacement
of `.wtree.yml`. It preserves all non-hook local configuration and never
changes the portable manifest.

All management mutation results have deterministic human and JSON forms that
report added, replaced, unchanged, skipped, and conflicting event names. They
never report execution or consent merely because a definition exists in the
portable source.

## 11. Status and doctor integration

`wtree status` adds setup information only when a hook-run record exists. Its
JSON projection identifies event, state, next hook ID, completed count, and
failure kind; it contains no command, path, environment, or process output.
The normal synchronized human table and exit status remain unchanged when no
record exists. An incomplete hook record is an observed setup finding, not a
Git workspace failure, so successful observation retains status's existing
zero-exit behavior.

`wtree doctor` reports stable non-fixable findings:

- `hook-setup-incomplete` for a valid resumable record;
- `hook-run-stale` when bound source or workspace generations changed; and
- `invalid-hook-run-record` for malformed, unsupported, or internally
  inconsistent state.

Doctor never executes, deletes, or rewrites hooks or records, including with
`--fix`. It may include the safe retry command only for a valid matching
record.

The hook-run format stays separate from workspace state version one and the
existing rollback recovery format. Readers must distinguish all three and
must not weaken authoritative workspace decoding.

## 12. Update preservation

Update never executes local hooks in this delivery. When it republishes local
configuration, it preserves the complete local hook mapping after decoded
canonicalization: event membership, order, IDs, effective or explicit
repository selections, commands, and timeouts remain semantically identical.
It does not copy, reconcile, remove, or upgrade hooks because
`shared_hooks` changed.

Update validates version-two and version-three candidate manifests under their
own schemas. Portable hook changes are manifest drift and publication content,
not execution consent. Any existing incomplete hook record remains visible;
its exact source digest will make retry stale after an authoritative source
change.

## 13. Safety and compatibility requirements

- Invalid declarations, management conflicts, stale generations, dry-run,
  and unauthorized portable hooks have zero hook and core mutation side
  effects except that a successful real core operation may intentionally
  publish while hooks are skipped.
- Hook failures never remove worktrees, branches, registry entries, manifests,
  or workspace state.
- Hook execution never holds the project mutation lock.
- Existing hook-free version-two init, clone, update, create, checkout,
  status, doctor, and JSON fixtures retain their behavior.
- Existing unknown-field and unsupported-version rejection stays strict.
- All paths, process behavior, locking, atomic writes, and tests support Linux,
  macOS, and Windows without assuming a POSIX shell.
- Tests use temporary data roots, repositories, environments, executables,
  and clocks and never use credentials, the network, user Git configuration,
  or user project configuration.

## 14. Non-goals

- Automatically copying all ignored files or inferring desired setup.
- A shell language, command-string interpolation, or hook dependency graph.
- Sandboxing, privilege reduction, network isolation, or rollback of hook side
  effects.
- Direct execution of `shared_hooks`.
- Local/shared `post-clone` or portable `post-create`.
- `post-checkout`, `post-update`, pre-operation, or removal hooks.
- A project policy that forbids `--no-hooks`; explicit local bypass remains
  the user's choice.
- Persisted output tails, ambient environments, literal command arguments, or
  executable paths in hook-run records.
- Fresh-run, named-hook run, record deletion, or force-retry commands.

## 15. Acceptance criteria

1. A nested repository `post-create` hook receives exact registered source and
   resolved target paths and runs after all checked-out branches and HEADs are
   published and validated.
2. Plain logical roots, sibling repositories, nested repositories, and mount
   overrides produce correct environment paths without topology discovery in
   the hook.
3. Invalid hooks and create dry-run execute nothing and mutate no core or hook
   state.
4. Core create failure executes no hook; hook failure retains the complete
   workspace, returns non-zero, records incomplete setup, and resumes without
   rerunning durably completed hooks.
5. Timeout, cancellation, launch failure, and non-zero exit are distinct,
   bounded, secret-safe results on all supported platforms.
6. Share rejects non-portable definitions and conflicts without mutation;
   force replaces only the named shared event.
7. Install is all-or-nothing by default, preserves unrelated local events,
   implements force and missing modes exactly, and upgrades version only when
   a write is necessary.
8. List groups all sources and reports canonical missing, identical, and
   conflicting states without changing files.
9. Portable `post-clone` runs only with `--run-hooks`; shared hooks never run,
   and skipped portable hooks create no incomplete record.
10. Version-two hook-free workflows and wire output remain compatible, while
    every hook-bearing document is explicitly version three.
11. Status and doctor diagnose valid, stale, and invalid hook-run records
    without exposing commands or changing authoritative workspace state.
12. Linux, macOS, and Windows CI pass the complete unit, race, vet, build,
    release-layout, and executable hook acceptance coverage.
