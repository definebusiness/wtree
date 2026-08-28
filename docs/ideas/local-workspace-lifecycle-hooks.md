# Idea: machine-local and shared workspace lifecycle hooks

Status: initial

## Problem

A repository can require machine-local files that Git intentionally does not
track. Examples include `.env`, IDE configuration, certificates, and a Spring
application's `application.properties`. A normal Git worktree does not contain
these ignored files, so a newly created `wtree` workspace may have the correct
repositories and branches but still be unusable until the developer recreates
or adapts local configuration by hand.

This is especially awkward for nested repositories. A setup script needs to
know both the registered source checkout for one repository and that
repository's resolved path in the new workspace. Reconstructing either path
from directory names or mounts would duplicate `wtree` topology rules and fail
for logical project roots, sibling repositories, nested repositories, or
workspace-specific mount overrides.

## Proposed direction

Allow the ignored, machine-local `.wtree.yml` to declare trusted lifecycle
hooks. The first useful event is `post-create`: it runs only after the complete
workspace transaction has validated every checkout and committed authoritative
workspace state. At that point every included repository is on the planned
branch at the planned revision and every resolved checkout path is final.

Hooks should be repository-scoped by default. `wtree` supplies the exact source
and target paths for the selected repository, making the common setup script a
simple copy or transformation rather than a topology-discovery program.

An illustrative local configuration is:

```yaml
version: 3

# Existing project, logical_root, repositories, worktrees, discovery, and
# manifest fields are omitted from this example.

hooks:
  post-create:
    - id: configure-spring-application
      repository: backend
      command: [".wtree-hooks/configure-spring-worktree"]
      timeout: 60s
```

The hook executable may itself be ignored. For a repository-scoped hook, a
relative executable path containing a path separator is resolved against that
repository's registered source checkout, while the process working directory
is the corresponding checkout in the new workspace. An absolute executable is
also reasonable in this machine-local file. A bare executable name is resolved
through `PATH`.

Commands are argument arrays and are executed directly. `wtree` does not
perform shell parsing, variable interpolation, or command substitution. A user
who needs shell behavior can opt into it explicitly, for example with
`["sh", "-c", "..."]`, and accepts the platform and quoting implications.

The tracked portable manifest has two distinct hook roles:

- its directly executable hook surface contains only `post-clone`, because no
  local `.wtree.yml` exists yet when clone setup must run; and
- `shared_hooks` contains inert templates for supported local events. Shared
  hooks never execute from the portable manifest. A developer must explicitly
  install them into `.wtree.yml`, after which they behave exactly like other
  trusted local hooks.

| Source | Supported events | Execution policy |
|---|---|---|
| Portable `hooks` | `post-clone` | Only during clone with `--run-hooks` |
| Portable `shared_hooks` | `post-create`; later `post-checkout` and `post-update` | Never directly; requires explicit installation |
| Local `.wtree.yml` `hooks` | `post-create`; later `post-checkout` and `post-update` | Automatic unless `--no-hooks` is supplied |

For example, a portable manifest may distribute the local setup above without
authorizing it to execute:

```yaml
shared_hooks:
  post-create:
    - id: configure-spring-application
      repository: backend
      command: [".wtree-hooks/configure-spring-worktree"]
      timeout: 60s
```

Shared definitions must be portable: they cannot contain absolute paths,
credentials, ambient secret values, or other machine-specific data. Relative
executables with path separators must refer to tracked project content that
will exist in the selected repository's registered source checkout after
installation. An ignored local-only executable is valid locally but cannot be
shared.

## Sharing, installation, and inspection

The focused command surface is:

```text
wtree hooks list
wtree hooks share <event> [--force]
wtree hooks install [--force | --missing]
wtree hooks retry <workspace>
```

`wtree hooks share <event>` copies the complete ordered local definition for
that event into the portable manifest's `shared_hooks`. If no shared definition
exists, it is added. If the existing definition is canonically identical,
`wtree` reports that overriding would have no effect and leaves the manifest
unchanged. If it differs, the command fails without mutation unless `--force`
is supplied; `--force` replaces only the selected shared event. The command
validates portability before changing the tracked manifest and never stages or
commits it.

`post-clone` is not a local event and therefore cannot be shared. Attempting
`wtree hooks share post-clone` fails with an unsupported-event diagnostic and
does not change either configuration.

`wtree hooks install` compares every shared definition with `.wtree.yml` before
writing anything. By default, any differing local definition is a conflict and
the complete install fails without mutation. Identical definitions are
reported as unchanged. `--force` replaces local definitions for every event
present in `shared_hooks` while preserving unrelated local-only events.
`--missing` adds only events absent locally and reports existing identical or
differing events without changing them. `--force` and `--missing` are mutually
exclusive. Canonically identical definitions are a reported no-op in every
mode. Every successful installation replaces `.wtree.yml` atomically.

`wtree hooks list` is observational and groups definitions by source:

- directly executable portable `post-clone` hooks;
- inert portable shared hooks; and
- active local hooks.

For each definition it shows the event, ordered hook IDs, repository scope,
command, execution policy, and whether a matching definition is missing,
identical, or conflicting in another source. JSON exposes the same grouping
and comparison without changing files.

## Hook environment

`post-create` should inherit the invoking process environment, with `wtree`
overwriting a reserved set of variables with authoritative values:

| Variable | Meaning |
|---|---|
| `WTREE_HOOK` | Hook event, initially `post-create` |
| `WTREE_OPERATION` | Core operation that produced the event, initially `create` |
| `WTREE_PROJECT_ID` | Stable project ID |
| `WTREE_PROJECT_NAME` | Human-readable project name |
| `WTREE_BASE_REPOSITORY_ID` | Repository that owns local project metadata |
| `WTREE_WORKSPACE_ID` | Stable storage-safe workspace ID |
| `WTREE_WORKSPACE_NAME` | User-facing workspace or branch name |
| `WTREE_SOURCE_LOGICAL_ROOT` | Absolute logical root of the registered source project |
| `WTREE_TARGET_LOGICAL_ROOT` | Absolute logical root of the new workspace |
| `WTREE_REPOSITORY_ID` | Repository selected by the hook declaration |
| `WTREE_SOURCE_REPOSITORY` | Absolute registered source checkout for that repository |
| `WTREE_TARGET_REPOSITORY` | Absolute resolved checkout for that repository in the new workspace |
| `WTREE_BRANCH` | Planned checked-out branch for that repository |
| `WTREE_HEAD` | Full object ID validated for that checkout |

`WTREE_SOURCE_REPOSITORY` is the unambiguous equivalent of the “default
worktree” path: it is the source checkout recorded by the local project
configuration, not a path guessed from the target mount. Scripts should use
these values rather than invoking `wtree` recursively to rediscover the active
workspace.

For example, the configured executable could contain:

```sh
#!/bin/sh
set -eu

cp "$WTREE_SOURCE_REPOSITORY/.env" "$WTREE_TARGET_REPOSITORY/.env"
cp "$WTREE_SOURCE_REPOSITORY/src/main/resources/application.properties" \
  "$WTREE_TARGET_REPOSITORY/src/main/resources/application.properties"
```

## Execution and ordering

The create lifecycle should be:

1. Load and strictly validate the local configuration and hook declarations.
2. Build the complete workspace and hook plan. A missing repository, invalid
   command, duplicate hook ID, invalid timeout, or unavailable source-relative
   executable fails preflight before workspace mutation.
3. Revalidate and execute the existing core create transaction.
4. Validate all repository identities, branches, revisions, and mounts.
5. Commit authoritative workspace state and release the project mutation lock.
6. Execute `post-create` hooks sequentially in declaration order.
7. Report either complete success or a valid workspace whose local setup is
   incomplete.

Hooks run outside the core transaction and after its lock is released. This
avoids holding the project lock while arbitrary user code runs and gives the
script the fully published workspace requested by the use case. Hook side
effects are not rollback-safe, so a hook must never be inserted among Git
worktree creation steps or run during rollback.

`--dry-run` lists hook ID, event, repository, working directory, resolved
executable, arguments, and timeout after validating them, but never executes a
hook. Machine-readable planning should expose the same facts without including
ambient environment values, which may contain secrets.

## Failure, retry, and observability

A failed or timed-out `post-create` hook does not undo a successfully committed
workspace. The command returns non-zero and clearly distinguishes these facts:

- core workspace creation succeeded;
- local setup is incomplete;
- the failed hook ID and repository;
- the exit status or timeout, without dumping secret environment values; and
- the command for an explicit retry.

`wtree` should write a durable hook-run record outside the repository, alongside
its other project state. The record contains the operation, workspace ID, hook
configuration fingerprint, successful hook IDs, the failed or next hook ID,
and timestamps. `status` and `doctor` can then report incomplete setup without
weakening the authoritative workspace-state format.

An explicit command such as `wtree hooks retry <workspace>` resumes at the first
unfinished hook and does not rerun already successful hooks. If `.wtree.yml`
changed since the record was written, retry fails safely and asks the user to
start a fresh explicit hook run. Hook authors should still make scripts
idempotent because a process can be interrupted after producing side effects
but before `wtree` durably records success.

A user may intentionally bypass local hooks with an explicit `--no-hooks`
option. The success output should say that hooks were skipped. A deliberate
skip is not a failed hook run and does not create an incomplete-setup record.

## Trust boundary and configuration versioning

Writing a command in the ignored local `.wtree.yml` is explicit machine-local
consent, so valid local hooks run by default unless the user passes
`--no-hooks`. Installing an inert shared definition is the same explicit local
consent; merely cloning or updating the portable manifest is not.

The only directly executable portable event is `post-clone`. Obtaining a
manifest from a repository or URL is not consent to execute it, so clone runs
that event only with explicit `--run-hooks` authorization. `--run-hooks` never
executes `shared_hooks`, and `--no-hooks` controls active local hooks rather
than portable declarations.

The current local configuration and portable manifest are strict version-two
schemas. Adding local `hooks`, portable `post-clone`, or `shared_hooks` must
therefore use explicitly versioned successor schemas; version two must not
silently acquire a second meaning. A later specification should choose the
migration behavior and whether a new binary continues reading hook-free
version-two files.

Hook output may contain application secrets. Human output can be streamed with
the hook ID as context, but durable records and JSON results should retain only
bounded diagnostics and never persist the ambient environment. `wtree` does
not claim to sandbox trusted local executables or undo their filesystem,
network, or process side effects.

## Relationship to existing lifecycle-hook work

The [full multi-repository experience specification](../spec/full-multi-repository-experience.md#82-lifecycle-hooks)
reserves only portable `post-clone`, with explicit `--run-hooks`
authorization. This idea supplies the complementary local and sharing model:
locally authored or explicitly installed hooks, authoritative source-to-target
path pairs, and each operation's existing publication boundary. The three
sources can share execution, retry, timeout, validation, and diagnostic
machinery while retaining separate consent and execution policies.

## Event model and staged expansion

The schema should allow additional event names without pretending that every
operation has the same safe boundary. Useful follow-on events are:

- `post-checkout`, after an existing branch workspace is fully published;
- `post-update`, after all selected repositories and project metadata are
  reconciled and committed.

Local and shared `post-clone` are deliberately unsupported. `.wtree.yml` and
its installed hooks become available only after clone has completed, which is
too late for that clone's event. The explicitly authorized portable
`post-clone` hook is the single setup path for acquisition.

Commands such as update that republish machine-local configuration must
preserve local hook definitions exactly unless the user explicitly runs a hook
installation command. Merely receiving changed `shared_hooks` never installs,
updates, or removes a local hook.

Pre-operation hooks such as `pre-create` may also be useful, but they solve a
different problem from creating ignored files in a completed workspace. If
added, they should run after read-only planning and before acquiring the core
mutation lock. Their own arbitrary side effects remain outside rollback, and
the core plan must be revalidated afterward. No pre-hook should observe a
partially created workspace or run inside rollback.

Removal hooks need an especially explicit design. A `pre-remove` hook could
block cleanup or preserve data, while `post-remove` can no longer use the
target checkout as its working directory. They should not be inferred from the
create contract.

## Goals

- Make a newly created workspace locally usable without manually copying
  ignored, location-dependent files.
- Give scripts authoritative source and target paths for any repository in a
  logical-root repository forest.
- Preserve the existing atomic core workspace transaction and rollback
  guarantees.
- Provide deterministic ordering, bounded execution, dry-run visibility,
  durable incomplete-setup diagnostics, and safe retry.
- Allow portable but inert sharing of local hook definitions with atomic,
  conflict-safe installation and clear source inspection.
- Keep trusted local execution separate from untrusted portable project
  metadata.

## Non-goals

- Copying every ignored file automatically or inferring which ignored files a
  project needs.
- Embedding secrets or machine paths in the portable manifest.
- Treating arbitrary hook side effects as transactionally reversible.
- Providing a cross-platform shell language or interpolating command strings.
- Running shared hooks directly from a portable manifest.
- Running portable `post-clone` without explicit `--run-hooks` authorization.
- Supporting local or shared `post-clone` after the acquisition event has
  already passed.
- Defining pre-operation or removal-hook semantics as part of the first
  `post-create` delivery.

## Acceptance examples

- A repository-scoped `post-create` hook for a nested Spring repository sees
  the exact registered source checkout and exact new checkout, runs only after
  the new checkout is on the validated branch, and creates ignored application
  configuration in the new checkout.
- A project with a plain logical root, sibling repositories, nested mounts,
  and mount overrides supplies correct absolute paths without scripts
  reconstructing topology.
- Invalid declarations and dry-run cause no workspace or hook side effects.
- A core create failure runs no hook. A hook failure preserves the committed
  workspace, returns non-zero, creates a durable incomplete-setup record, and
  can be retried without rerunning durably successful hooks.
- Commands execute without an implicit shell, use the target repository as
  their working directory, honor cancellation and timeout, and never expose
  ambient environment values in plans or durable records.
- Sharing rejects machine-specific definitions, requires `--force` for a
  differing existing shared event, and treats an identical event as a reported
  no-op.
- Default installation is atomic and conflict-free; `--force` replaces only
  shared event names and `--missing` adds only absent local event names.
- Hook listing groups portable `post-clone`, inert shared, and active local
  definitions and reports identical, missing, and conflicting definitions.
- A shared hook never runs from the portable manifest. Once explicitly
  installed, its local copy follows normal automatic execution and
  `--no-hooks` behavior.

## Questions for a specification

- Should a hook-free local version-two configuration remain readable, or
  should the project require explicit migration to local version three?
- Should portable `post-clone` and `shared_hooks` use one new manifest version
  or separately versioned companion data?
- Should an explicit fresh-run command rerun all hooks, only one named hook, or
  both forms?
- Should inherited standard output and standard error be streamed only, or
  should a bounded tail also be retained for `doctor` diagnostics?
- Does `--no-hooks` need a project policy that can forbid bypassing essential
  local setup, or is explicit user choice always sufficient?
