# Troubleshooting

Start with the read-only commands that match the problem:

```sh
wtree project list
wtree status <workspace>
wtree doctor <workspace>
```

Add `--json` when structured output is easier to inspect. For commands that
change state, use `--dry-run` first when it is supported.

## A lifecycle hook left setup incomplete

`wtree create` and an explicitly authorized `wtree clone --run-hooks` publish
the valid workspace before lifecycle hooks run. A timeout, cancellation,
launch failure, or non-zero hook exit therefore leaves the workspace intact;
it does not undo branches, worktrees, registry state, or manifests. Start with
read-only inspection:

```sh
wtree status <workspace>
wtree doctor <workspace>
```

Correct the external cause, then run the exact bounded retry command, normally:

```sh
wtree hooks retry <workspace>
```

Retry requires the recorded configuration or manifest bytes, hook plan, and
workspace facts to still match. It resumes at the recorded position, never
starts a fresh run, and never reruns a durably completed hook. Do not delete or
edit the private hook-run record to bypass that check. Hooks must be idempotent
because an interruption after child side effects but before durable recording
can require a hook to run again.

Portable `post-clone` hooks run only with `--run-hooks`; without it, clone
intentionally skips them and creates no retry record. `shared_hooks` are never
executed directly. Use `wtree hooks list` to inspect sources and `wtree hooks
install` to copy an explicitly shared `post-create` definition into trusted
local configuration. `--no-hooks` is the intentional local `post-create`
bypass. Review literal arguments before sharing; hooks are direct programs,
not sandboxed shell snippets, and portable hooks receive only a sanitized
environment. Durable records and execution-result/error JSON omit arguments,
paths, output, and environment values; list and plan/dry-run inspection output
intentionally shows configured/resolved executables and literal arguments.

## Update stopped with an active journal

An interrupted `wtree update` deliberately leaves its journal and private
backups in place rather than guessing which files to remove. Read-only
commands such as `status` and `doctor` remain safe to run; mutating resolver
operations refuse to proceed while that journal is present. Do not delete the
journal or edit the registry/state files manually. Record the exact error and
use the documented recovery path once it is available for the operation.

## Duplicate project after reinitializing

This usually happens when a project was initialized, its local `.wtree.yml`
was deleted, and `wtree init` was run again in the same checkout. The original
project is still in the global registry, so `init` refuses to create a second
registration for the same configuration path or Git repository identity. The
error names the conflicting project ID or IDs.

Inspect the registry from any directory:

```sh
wtree project list
```

Find the old project ID mentioned by the error. If its `PRUNABLE` value is
`true`, preview and then remove the stale registration:

```sh
wtree project prune <old-project-id> --dry-run
wtree project prune <old-project-id>
```

Then return to the project checkout and initialize it again:

```sh
cd /path/to/project
wtree init
```

`project prune` removes only the selected global registry entry. It does not
delete repositories, Git worktrees, project configuration, workspace state,
recovery data, or lock files.

Do not prune an entry when `PRUNABLE` is `false`. If the registration is valid
but you intentionally want to replace it, preview and use the explicit
registry-only operation instead:

```sh
wtree project unregister <project-id> --dry-run
wtree project unregister <project-id>
```

Unregistering also retains all project and Git data. If its `.wtree.yml`
remains, a later mutating command from that project can register it again.

## Project cannot be determined or is ambiguous

Commands normally infer the project from `.wtree.yml`, persisted workspace
state, or Git identity. From an unrelated directory, or when more than one
registration matches, select the intended project explicitly:

```sh
wtree project list
wtree status --project /path/to/project
```

`--project` accepts the base checkout, its `.wtree.yml`, or a registered
logical/workspace root whose persisted state validates the complete forest.
For a plain logical root, `.wtree.yml` is inside the designated base
repository rather than at the logical root. You can also run inspection
commands from any verified top-level or nested checkout.
Use the duplicate-project procedure above if `project list` reports a stale
registration; do not edit the registry file by hand.

## Local configuration version 1 is rejected

Local project configuration now requires schema version 2 with an explicit
base repository, logical-root relationship, repository topology, and portable
manifest metadata. A version 1 `.wtree.yml` is rejected with a message that
reinitialization is required. It is not converted or rewritten in place.

Preserve any local changes, then initialize the intended logical root again.
When it contains more than one top-level repository, name the top-level base
repository explicitly:

```sh
cd /path/to/logical-root
wtree init --base-repository api --dry-run
wtree init --base-repository api
```

Review the base-owned `.wtree.yml` and `project.wtree.yml` plus every Git
parent's `.gitignore` change. `init` does not stage, commit, or push them.
Global configuration, registry, workspace state and recovery records retain
their existing version-1 formats. Portable manifests remain version 2 when
hook-free; any portable hook declaration is explicitly version 3.

## Workspace checkout or metadata has drifted

Use `doctor` to compare persisted workspace state with the filesystem and Git:

```sh
wtree doctor <workspace>
wtree doctor <workspace> --json
```

`doctor` reports the logical workspace root, designated base repository, and
every declared repository in deterministic parent-first order. Conditions
include missing checkouts, changed branches or HEADs, mount mismatches,
duplicate or unknown repositories, and stale Git worktree registrations. It
is read-only unless `--fix` is supplied.

Only repairs explicitly listed by `doctor` are eligible for automatic repair.
Preview them before applying:

```sh
wtree doctor <workspace> --fix --dry-run
wtree doctor <workspace> --fix
```

Currently, safe repairs are limited to verified checkout mount/path metadata
and Git registrations for worktrees whose directories are missing. Branch or
HEAD mismatches, unknown or duplicate checkouts, stale workspace state, and
recovery records require manual investigation.

## Push readiness is blocked

`wtree push` only reports whether a complete workspace is already ready for
manual publication. It never invokes `git push`, fetches, or creates refs or
tags. Resolve the reported dirty checkout, missing upstream, divergence,
identity, metadata, or remote-availability condition, then publish each
repository manually. Coordinated publication is a future workflow.

`doctor` also separates four similarly named situations. A
`retained-unmanaged-repository` is a checkout deliberately retained after a
successful manifest removal; preserve it or reconcile it manually, but do not
treat it as an unknown checkout. A `manifest-repository-unmanaged` or another
manifest/identity/branch/mount finding is ordinary drift: compare the current
manifest, workspace state, and checkout before changing anything. An
`update-in-progress` finding means an update journal is still present, so
read-only diagnosis is safe but repair and other mutations refuse to run. An
`update-recovery-record` or existing `recovery-record` identifies actionable
incomplete rollback evidence; follow the recovery guidance below and retain
the record until every owned effect is accounted for. None of these findings
is an automatic `doctor --fix` repair.

## Remove or delete is blocked by local changes

`wtree remove` refuses to remove a dirty worktree. `wtree delete` also refuses
to delete an unmerged branch. Inspect the workspace and each reported
repository, then commit, stash, or otherwise preserve the work before retrying:

```sh
wtree status <workspace>
wtree remove <workspace> --dry-run
# or
wtree delete <workspace> --dry-run
```

If discarding exactly the reported changes is intentional, preview the narrow
force overrides before applying them:

```sh
wtree remove <workspace> --force --dry-run
wtree delete <workspace> --force --dry-run
```

Then repeat the chosen command without `--dry-run`. For `remove`, `--force`
only permits dirty worktree removal. For `delete`, it also permits deletion of
reported unmerged branches.

## An operation reports an incomplete rollback

An error categorized as `rollback_incomplete` means an operation failed and
one or more completed effects could not be undone. The error identifies the
recovery record. When the affected workspace can still be resolved, `doctor`
reports it as `recovery-record`:

```sh
wtree doctor <workspace>
wtree doctor <workspace> --json
```

The original command fails immediately, so the leftover is not silent. The
human error names the failed and unreverted steps; JSON preserves the same
stable recovery evidence. Mutating operations are blocked while the recovery
record remains, but read-only inspection stays available.

Do not delete the recovery record just to unblock commands. Use the reported
repository IDs and resolved paths to inspect every retained checkout before
changing anything:

```sh
wtree status <workspace> --json
wtree doctor <workspace> --json
git -C /reported/checkout status --short --branch
git -C /reported/checkout worktree list --porcelain
```

Then choose a safety-first repair based on what the record reports:

- If the retained checkout contains user work, commit it, move it to a safe
  location, or otherwise preserve it before attempting cleanup.
- If it is the exact operation-created checkout and its Git identity, branch,
  HEAD, and registration still match, remove it with the corresponding Git
  repository's `git worktree remove` command.
- Preserve any replacement, concurrent checkout, unrelated file, grouping
  directory, or identity that cannot be proven to belong to the failed
  operation. Reconcile the workspace state or registry only after the
  filesystem and Git registrations agree.
- Retry `wtree doctor <workspace>` after each repair. Remove recovery metadata
  only after all retained effects have been accounted for and the workspace is
  consistent. Registry pruning and unregistering remain blocked while
  unresolved recovery metadata exists.

During `create`, an automatic `.gitignore` update in a newly created worktree
is safe for rollback. Any other tracked, staged, or untracked change is
preserved instead, so the recovery record can be used to inspect and recover
that worktree without discarding user data.

Today `doctor` reports the evidence but does not generate a complete command
script, and there is no `wtree recover` command. The staged proposal for those
improvements is documented in the
[actionable recovery idea](ideas/actionable-incomplete-rollback-recovery.md).
