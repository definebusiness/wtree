# Nested mount ignore management specification

Status: superseded
Superseded by: [Automatic nested mount ignore protection story](../ideas/automatic-nested-mount-ignore-protection.md)
Source idea: none (created directly)
Implementation plan: [Nested mount ignore management implementation plan](../plans/nested-mount-ignore-management.md)
Applies to: `wtree add-ignore`, `wtree init`, `wtree create`, workspace
planning, Git ignore inspection, human output, JSON output, and documentation

## 1. Purpose

Every nested repository is an independent Git checkout placed inside a parent
repository checkout. The parent must ignore that mount directory. Otherwise
the parent can appear dirty and an accidental `git add .` can stage the child
as an embedded-repository gitlink.

`wtree` must therefore:

1. verify every nested mount before initializing a project or creating a new
   workspace;
2. fail before mutation when a required committed ignore rule is missing;
3. give an actionable `wtree add-ignore` hint;
4. provide `wtree add-ignore` to add missing rules safely; and
5. support `--add-ignore` on `init` and `create` for an explicit one-command
   workflow without making Git commits for the user.

This document is authoritative when it is more specific than the general
mount-override text in [`wtree.spec.md`](wtree.spec.md).

## 2. Terms and ownership

For each non-root repository:

- the **child** is the repository being mounted;
- the **parent** is its configured immediate parent repository;
- the **effective mount** is the child mount after applying any
  workspace-specific `--mount` override; and
- the **owning ignore file** is `.gitignore` in the root of the immediate
  parent repository checkout.

Rules are always parent-relative. Given this hierarchy:

```text
root
└── backend
    └── shared
```

the default rules belong in different repositories:

```text
root/.gitignore:     /backend/
backend/.gitignore:  /shared/
```

If `backend=api` and `shared=common`, the rules are:

```text
root/.gitignore:     /api/
backend/.gitignore:  /common/
```

The root repository must never be treated as a nested mount and must never
receive a mount-ignore rule for `.`.

## 3. Ignore safety invariant

Before `wtree create` mutates anything, every effective nested mount must be
ignored by a committed `.gitignore` rule in the immediate parent's selected
base commit.

- `HEAD` is the selected base when `--from` is omitted.
- With `--from <ref>`, the ref is resolved independently in every repository,
  and each child is checked against its parent's resolved base commit.
- A rule only in the current working tree does not satisfy this preflight.
- `.git/info/exclude`, `core.excludesFile`, and other global excludes do not
  satisfy it because they are not carried into a new checkout.
- An effective committed rule may be in the parent's root `.gitignore` or a
  committed `.gitignore` in an ancestor directory of a multi-component mount.
- Git's own ignore semantics are authoritative. `wtree` must not implement a
  partial glob matcher for validation.

The same committed-rule invariant applies during plain `wtree init`, using
each parent repository's current `HEAD`. This ensures the portable project is
not initialized with unsafe default mounts.

## 4. Generated rules

When `wtree` adds a rule, it writes one anchored directory rule to the owning
parent repository's root `.gitignore`:

```gitignore
/<parent-relative-mount>/
```

The rule generator must escape Git-ignore metacharacters so the mount is
matched literally while retaining `/` as the path separator. It must support
spaces, Unicode, `#`, `!`, brackets, and other mount characters already
accepted by the central mount validator.

Before adding a rule, `wtree` evaluates the existing working-tree
`.gitignore` files using Git semantics. If they already ignore the mount, it
does not add a duplicate. Repository-local and global excludes still do not
count.

When changing a `.gitignore`, `wtree` must:

- refuse a symlink or non-regular file;
- preserve all existing bytes and ordering;
- add a line break first only when the existing non-empty file lacks one;
- preserve the existing newline style when it is unambiguous and otherwise
  use LF;
- append generated rules in deterministic parent-first, repository-ID order;
- coalesce all additions for the same parent file into one write;
- preserve the permissions of an existing file; and
- create a missing file with ordinary non-executable file permissions subject
  to the process umask.

`wtree` never stages or commits `.gitignore`.

## 5. `wtree add-ignore`

### 5.1 Command surface

```text
wtree add-ignore
  [--project <directory-or-.wtree.yml>]
  [--mount <repository-id>=<mount>]...
  [--dry-run]
  [--json]
  [--data-dir <path>]
```

The command takes no positional arguments. It does not accept `--from`,
`--force`, or `--verbose`.

Repeated `--mount` uses the same parsing, validation, hierarchy relocation,
unknown-ID rejection, duplicate-ID rejection, containment checks, and
collision checks as workspace planning. An override changes the rule planned
for that repository; unspecified repositories retain their configured or
discovered mount.

### 5.2 Project discovery

For an initialized project, `add-ignore` resolves the project normally and
uses its configured repository graph and source checkouts. It edits the
owning `.gitignore` in those source checkouts, not in an arbitrary generated
workspace.

For an uninitialized project, `add-ignore` discovers the independent nested
repository tree from the intended outer Git root and uses the same stable IDs,
parent relationships, mount validation, submodule rejection, and discovery
ignore options as `init`. The user must run it at the intended outer root or
select that root with `--project`; an uninitialized invocation from inside a
child repository must not guess an unseen outer project.

This uninitialized mode makes the hinted workflow valid:

```sh
wtree add-ignore
git -C backend add .gitignore
git -C backend commit -m 'ignore shared repository mount'
git add .gitignore
git commit -m 'ignore nested repository mounts'
wtree init
```

For a custom future mount:

```sh
wtree add-ignore --mount backend=api
git add .gitignore
git commit -m 'ignore api mount'
wtree create feature/login --mount backend=api
```

If `create --from <ref>` selects a base other than the current source branch,
the user is responsible for making the generated rule part of that selected
base. The failure message must name the selected parent base so this is clear.

### 5.3 Planning and output

`add-ignore --dry-run` performs full discovery, validation, ignore matching,
and file safety checks without creating, replacing, or touching any file or
lock. Human dry-run output lists each owning repository, `.gitignore` path,
and rule that would be appended, then ends with `No changes made.`

Normal human output distinguishes changed files from already-safe mounts. If
files changed, it ends with a direct instruction to review and commit every
listed `.gitignore` before creating workspaces from those branches. If no
change is needed, it reports that all effective nested mounts are already
ignored and does not imply that a commit is needed.

JSON output uses this stable shape, with deterministic arrays and no `null`
collections:

```json
{
  "operation": "add-ignore",
  "rootPath": "/code/product",
  "dryRun": false,
  "files": [
    {
      "repositoryId": "root",
      "path": "/code/product/.gitignore",
      "addedRules": ["/backend/"],
      "alreadyIgnoredRepositoryIds": ["frontend"]
    }
  ]
}
```

Only fields owned by this contract may be added without updating this
specification and its contract tests.

## 6. `wtree init`

### 6.1 Plain init

After discovery and before writing `.wtree.yml`, `project.wtree.yml`, registry
data, workspace state, locks, or `.gitignore`, plain `wtree init` checks every
discovered nested mount against the immediate parent's current `HEAD`.

If any mount lacks a committed `.gitignore` rule, init fails with the existing
validation error/exit-code contract and performs no mutation. The diagnostic
lists every missing parent/child/mount tuple in deterministic order and says:

```text
Run `wtree add-ignore`, commit the changed .gitignore files, and retry;
or rerun `wtree init --add-ignore` to initialize and add the rules now.
```

The existing local configuration rule remains an init responsibility: a
successful init ensures that the root `.gitignore` effectively contains
`/.wtree.yml`. `project.wtree.yml` remains visible to Git.

### 6.2 `init --add-ignore`

`wtree init --add-ignore` authorizes adding all missing discovered nested-mount
rules during the initialization transaction. It also ensures `/.wtree.yml`.
It does not require the newly added rules to have existed in `HEAD`, and it
does not commit them.

All deterministic discovery, mount, identity, registry, path, file-type, and
permission checks occur before the first write. The operation snapshots every
affected `.gitignore`; if a later init publication step fails, rollback
restores the exact previous bytes and permissions or removes a file created by
this invocation. A rollback failure uses the existing incomplete-cleanup
diagnostic conventions and identifies every file whose restoration failed.

`init --add-ignore --dry-run` reports both the normal init plan and proposed
ignore additions but performs zero mutation. JSON adds an `ignoreUpdates`
array with the same per-file semantics as `add-ignore`; an empty array is
encoded as `[]`.

Successful human output lists changed `.gitignore` files and tells the user to
review and commit them together with the portable manifest. It must not claim
that `wtree` committed the files.

## 7. `wtree create`

### 7.1 Plain create

Plain `wtree create` validates all effective nested mounts, including mounts
that equal configured defaults and mounts changed by repeated `--mount`.
Validation is part of the immutable plan and locked revalidation. A missing
rule fails before any branch, worktree, directory, state, recovery file, or
lock-owned mutation is created.

The human and JSON error uses the existing validation taxonomy, identifies the
child repository, parent repository, effective mount, and resolved parent base
commit, and includes an actionable hint:

- for configured mounts: `wtree add-ignore`;
- for overrides: the required `wtree add-ignore --mount <id>=<mount>` form;
- in both cases: commit the changed rules on the selected base and retry, or
  use `wtree create ... --add-ignore`.

No full original command line is reconstructed from untrusted values.

### 7.2 `create --add-ignore`

`wtree create ... --add-ignore` authorizes a one-command workspace creation
when selected base commits lack required ignore rules. It never changes a
source checkout and never commits.

Execution remains parent-first:

1. create the parent branch and worktree;
2. atomically update that new parent worktree's root `.gitignore` with all
   missing rules for its direct children;
3. verify those child directories are ignored in the new parent worktree;
4. add the child worktrees; and
5. repeat for deeper parents before adding their children.

This ordering ensures the parent ignores a child before the child `.git` file
exists. The resulting `.gitignore` edits are intentionally uncommitted changes
on the new workspace branches. Human success output and JSON must list them
and instruct the user to review and commit each affected parent repository.

The immutable create plan exposes deterministic `ignoreUpdates` and explicit
`update_gitignore` steps with inverse restoration metadata. Because this adds
a new public plan action and validation rules, the workspace plan schema
version advances from 1 to 2. Persisted project, registry, workspace, and
recovery schema versions do not change.

`create --add-ignore --dry-run` shows the exact files and generated rules in
the planned workspace paths and performs zero mutation.

If ignore writing, verification, child worktree creation, result validation,
or state publication fails, rollback removes created child worktrees, restores
or removes `.gitignore` files, removes parent worktrees and branches, and
preserves the existing recovery semantics. A known-created worktree may be
force-removed during rollback solely to discard ignore edits made by this
transaction; unrelated pre-existing paths are never force-removed.

## 8. Standalone write transaction

`wtree add-ignore` preflights every target before writing any file. For an
initialized project it uses the existing project lock. In uninitialized mode
it does not create persistent wtree locks; instead it snapshots every target
and performs an optimistic unchanged-content/type check immediately before
each atomic replacement.

Writes use same-directory temporary files, file sync/close, atomic rename, and
directory sync where supported. On any failure, previously changed files are
restored in reverse order from exact snapshots. Concurrent modification is a
conflict, not an overwrite. A failed rollback reports all unrestored paths and
must never leave a truncated `.gitignore`.

## 9. Error and compatibility rules

- Invalid flags, malformed overrides, and positional arguments use
  `invalid_arguments`.
- Unsafe mounts, missing committed ignore rules, unsafe `.gitignore` file
  types, and uninitialized nested-context ambiguity use `validation`.
- Concurrent file changes and project-lock contention use `conflict`.
- Git inspection failures use `git`.
- Atomic write or rollback failures use the existing internal and
  rollback-incomplete classifications as applicable.
- Human diagnostics go to stderr through the existing CLI boundary. JSON mode
  emits exactly one error envelope on stdout and no human stderr.
- Existing command output remains compatible except for documented additive
  ignore-update fields and the workspace plan version change.
- The executable remains version `0.2.0`; no config or state migration is
  introduced by this feature.

## 10. Safety and portability

- All paths pass through the existing mount containment and symlink-aware
  path utilities before filesystem access.
- No generated ignore target may escape its owning parent checkout.
- Discovery continues to reject submodules and duplicate Git identities.
- Commands are hermetic with respect to user Git configuration and locale.
- Behavior must work on Linux, macOS, and Windows, including path separators,
  spaces, Unicode, missing files, read-only files, and symlinks where the
  platform supports them.
- No command publishes, pushes, stages, or commits.
- No command edits `.git/info/exclude`, global Git configuration,
  `.gitmodules`, or repository contents other than the specifically planned
  `.gitignore` files and existing wtree-owned initialization/workspace state.

## 11. Explicit non-goals

- Automatically committing or staging ignore changes.
- Editing an arbitrary historical ref or branch that is not checked out.
- Removing obsolete ignore rules when repositories or mounts change.
- Reformatting, sorting, or deduplicating unrelated `.gitignore` content.
- Treating submodules as nested independent repositories.
- Extending `--add-ignore` to `checkout`, `clone`, `sync`, `update`, `import`,
  `remove`, or `delete` in this change.
- Changing project/config/store schema versions or publishing a release.

## 12. Acceptance scenarios

The implementation must prove at least these behaviors:

1. Plain create succeeds when every default mount is ignored in the selected
   parent bases and fails without mutation when one default is missing.
2. A custom mount ignored only in the working tree, local exclude, global
   exclude, or a different branch is rejected.
3. `add-ignore` creates missing parent `.gitignore` files and appends literal,
   deterministic rules across a three-level graph without duplicating an
   already-effective rule.
4. `add-ignore --mount backend=api` adds `/api/` to the root parent while a
   descendant override adds its rule to the backend repository.
5. Plain init reports every missing mount and the `wtree add-ignore` hint with
   zero mutation.
6. `init --add-ignore` writes all rules plus `/.wtree.yml`, initializes the
   project, and restores every prior file if a later publication fails.
7. `create --add-ignore` edits each newly created parent worktree before its
   children are mounted, persists the workspace, and leaves only the reported
   `.gitignore` changes dirty.
8. Failure at every new create effect rolls back branches, worktrees, ignore
   files, and state or records exact incomplete recovery evidence.
9. All dry-run modes are byte-for-byte and metadata read-only, including no
   lock creation.
10. Human help, `--how-to`, README, tutorial fixtures, specification, and JSON
    contract tests describe the same workflows.
