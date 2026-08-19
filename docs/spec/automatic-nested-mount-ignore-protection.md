# Automatic nested mount ignore protection specification

Status: planned
Source idea: [Automatically protect nested repository mounts](../ideas/automatic-nested-mount-ignore-protection.md)
Previous design: [Nested mount ignore management specification](nested-mount-ignore-management.md) and [implementation plan](../plans/nested-mount-ignore-management.md), both superseded by the source idea
Implementation plan: [Automatic nested mount ignore protection implementation plan](../plans/automatic-nested-mount-ignore-protection.md)
Applies to: `wtree init`, `wtree create`, nested repository mount
validation, Git ignore inspection, `.gitignore` updates, dry-run output, and
failure recovery

## 1. Purpose and scope

Every non-root repository is mounted inside its immediate parent repository.
Unless the parent ignores that directory, an ordinary `git add .` can stage
the nested repository as an embedded-repository gitlink with mode `160000`.

`wtree` must make this protection an invariant of initialization and workspace
creation. The user does not run a preparatory command or opt in with a flag:

- `wtree init` protects every discovered source mount before publishing the
  project configuration or state; and
- `wtree create` protects every effective mount in the newly created parent
  worktree before adding the corresponding child worktree.

This specification replaces the manual `wtree add-ignore` and `--add-ignore`
design. It is authoritative where it conflicts with the committed-base mount
ignore requirements in [`wtree.spec.md`](wtree.spec.md) or with the superseded
design.

This scope does not change clone, checkout, import, update, sync, remove, or
delete behavior.

## 2. Terms and ownership

For each non-root repository:

- the **child** is the repository placed at a nested mount;
- the **parent** is the child's configured immediate parent repository;
- the **effective mount** is the normalized parent-relative mount used by the
  same workspace path-resolution code that places the child, after applying a
  `create --mount` override when present;
- the **owning ignore file** is `.gitignore` at the root of the immediate
  parent checkout; and
- an **effective `.gitignore` rule** is the winning Git ignore rule reported
  for the effective mount whose source is a `.gitignore` within that parent
  checkout.

Given this hierarchy:

```text
root
└── backend
    └── shared
```

the owning files and default generated rules are:

```text
root/.gitignore:     /backend/
backend/.gitignore:  /shared/
```

With `--mount backend=api --mount shared=common`, a newly created workspace
uses:

```text
<workspace>/.gitignore:      /api/
<workspace>/api/.gitignore:  /common/
```

The root repository has no parent mount and never receives a nested-mount rule
for `.`.

## 3. Protection invariant

Before a non-root child checkout exists at its effective mount, Git inspection
in the immediate parent checkout must report that mount ignored by an
effective `.gitignore` rule.

Git is the source of truth. `wtree` must use `git check-ignore -v --no-index`
or an equivalent Git operation against the intended directory path, including
a directory indication, and must inspect the reported rule source. A result
counts only when the source is a `.gitignore` located within the immediate
parent checkout and capable of governing the mount.

The following do not satisfy the invariant:

- `.git/info/exclude`;
- `core.excludesFile` or another global exclude source;
- a rule from outside the immediate parent checkout;
- a generated rule that is overridden by an effective negation or another
  more specific rule; or
- a textual rule that `wtree` assumes is effective without asking Git.

An existing effective rule may be in the owning root `.gitignore` or in
another `.gitignore` whose scope covers a multi-component mount. When Git
already reports such a rule, `wtree` must not append its generated rule.

## 4. Generated rule contract

When protection is missing, `wtree` derives exactly one literal, anchored
directory rule from the same normalized parent-relative mount used for
workspace placement:

```gitignore
/<mount>/
```

The generator must:

1. convert platform separators through the central mount normalizer;
2. retain `/` only as the path-component separator;
3. escape Git-ignore metacharacters and whitespace that would otherwise
   change the literal path meaning; and
4. reject the mount if it contains a line break, NUL, an invalid encoding, or
   any other value that cannot be represented by one unambiguous Git-ignore
   pattern.

The resulting line must match the normalized mount literally. Pattern
generation must not clean, reinterpret, or independently reconstruct a mount
after workspace path validation.

Before appending, `wtree` must also detect whether the exact generated line is
already present in the owning file. If the line is present but Git still says
the mount is visible, `wtree` must fail verification and identify an ignore
rule conflict; it must not append a duplicate line. This makes retry safe even
when a deeper `.gitignore` or a negation prevents the generated root rule from
becoming effective.

## 5. File inspection and update rules

Every owning path is inspected with non-following filesystem operations.
`wtree` must refuse an existing symlink, directory, device, socket, or any
other non-regular `.gitignore`. It must never follow a replacement target
outside the parent checkout.

For each regular or missing owning file, `wtree` must:

- preserve every existing byte and line in its original order;
- append only the missing generated rules;
- preserve the permissions of an existing file;
- create a missing file with ordinary non-executable permissions subject to
  the process umask;
- add a separator newline only when a non-empty file lacks one;
- use CRLF for appended lines when the existing file uses CRLF exclusively,
  and LF otherwise, without normalizing existing line endings; and
- coalesce every rule for direct children of the same parent into one file
  replacement.

Files and rules are planned deterministically in parent-first order, then by
parent repository ID and child repository ID.

Each changed file is replaced atomically with a temporary file in the same
directory. The implementation must write the complete new content, preserve
or establish the required mode, sync and close the temporary file, atomically
rename it over the target, and sync the containing directory where the
platform supports those operations. A failure must leave the target as a
complete old or complete new file, never truncated or partially appended.

Planning records the target's existence, type, content, and mode. Immediately
before replacement, `wtree` must reject a concurrent change rather than
overwrite it. A successful source-checkout replacement is retained if a later
source file or publication step fails. Retry starts from fresh Git and
filesystem inspection and adds only rules still absent.

## 6. `wtree init`

### 6.1 Preflight

Initialization discovers and validates the complete repository graph before
the first write. For every child it derives the normalized mount and generated
rule, resolves the immediate source parent and owning file, inspects the file
type, and asks Git whether the mount is already effectively ignored.

Preflight must collect all required file changes before mutation. A mount that
cannot be represented safely, an unsafe target type, an escaping target, or a
Git inspection failure prevents every write.

The existing `/.wtree.yml` protection remains an initialization
responsibility. When the root `.gitignore` also owns nested-mount rules, all
missing lines are coalesced into one atomic replacement.

### 6.2 Publication order

After preflight, initialization performs these phases in order:

1. revalidate and replace each changed source-parent `.gitignore`;
2. verify every nested source mount with Git, including mounts that required
   no change;
3. publish `.wtree.yml` and `project.wtree.yml` and update registry and default
   workspace state through the existing initialization publication; and
4. report every `.gitignore` changed by this invocation.

No wtree-owned configuration, registry entry, lock-owned durable state, or
workspace state may be published before every source mount passes
verification.

Successful source `.gitignore` replacements are monotonic. If a later source
replacement, verification, or wtree publication step fails, initialization
does not restore already changed source files. Wtree-owned publication still
uses its existing rollback and incomplete-cleanup rules. The diagnostic must
list changed files that were retained, targets still requiring work, and the
fact that retry will not duplicate completed rules.

### 6.3 Dry run and result

`wtree init --dry-run` performs discovery, mount validation, Git ignore
inspection, file-type checks, and change planning. It reports each owning file
and exact rule that would be appended, but creates no temporary file, lock,
configuration, manifest, registry entry, or workspace state.

The existing init JSON `ignoreUpdates` array remains the machine-readable
representation. It contains only files that would change and exact generated
rules, uses deterministic ordering, and is `[]` when no ignore update is
needed.

Normal human success output lists every changed `.gitignore` and instructs the
user to review and commit the changes. When no file changed, output states
that every nested mount was already protected. `wtree` never stages or commits
the files.

## 7. `wtree create`

### 7.1 Planning

Create planning validates every effective mount and generated rule, including
configured mounts and repeated `--mount` overrides. It determines the owning
parent worktree path and the rule that execution must ensure. It does not
require the selected base commit or source checkout to contain that rule.

The public workspace plan remains version 1. Automatic ignore protection is
an execution invariant derived from each repository's existing `parentId`,
`mount`, and `path`; it does not introduce an `ignoreUpdates` field, a new
public action, inverse file-restoration metadata, or a plan version increase.

Human `create --dry-run` output includes a deterministic section that lists
each parent `.gitignore` and rule that execution will ensure. Because the
parent worktrees do not yet exist, this is an ensure list rather than a claim
that every listed file will change. The version-one JSON plan continues to
express the same requirements through the non-root repository entries. Dry
run creates no temporary file, lock, branch, worktree, recovery record, or
workspace state.

### 7.2 Parent-first execution

Execution remains outside-in. For each parent repository, `wtree` must:

1. create the planned branch and parent worktree;
2. inspect effective ignore behavior in that new parent worktree for every
   direct child's effective mount;
3. atomically append all missing generated rules to the parent worktree's
   root `.gitignore` in one replacement;
4. ask Git again and require every direct child mount to be effectively
   ignored by a `.gitignore`; and only then
5. add the direct child worktrees.

The process repeats when a newly added child is itself the parent of deeper
repositories. No child `.git` file or directory may exist at a mount before
that mount passes step 4. Source checkouts are never modified by create.

After successful execution, each changed parent worktree is intentionally
dirty only through the reported `.gitignore` update and any unrelated state
permitted by existing create contracts. Human output lists all changed files
and instructs the user to review and commit them in their owning repositories.

### 7.3 Failure and cleanup

A write or verification failure prevents every affected direct child from
being added. Atomic replacement guarantees that each existing `.gitignore`
has complete old or new content.

Ignore edits in newly created worktrees remain owned by the existing create
transaction. Normal rollback may remove a transaction-created worktree and
thereby remove its ignore edit. It may force-remove that worktree only when
the dirty state is exactly the automatic `.gitignore` change. If unrelated
content appeared, cleanup must preserve the worktree and record exact recovery
evidence rather than discard possible user work.

Failure output distinguishes:

- source files, if any, retained by a separate operation;
- changed files still present in preserved create worktrees;
- files removed with cleanly rolled-back worktrees; and
- parent mounts that remain unverified and whose children were not added.

Rerunning after complete rollback starts from a fresh plan. Recovery of an
incomplete rollback follows the existing create recovery contract and must not
append a generated rule already present in a preserved worktree.

## 8. Error and compatibility rules

- An unrepresentable or escaping mount and an unsafe `.gitignore` file type use
  the existing `validation` classification.
- A target changed after preflight, a changed locked plan, or lock contention
  uses `conflict`.
- Failure to run or interpret Git ignore inspection uses `git`.
- File creation, sync, close, or atomic replacement failures use the existing
  internal I/O classification. Retained source updates are partial progress,
  not rollback failures.
- Failure to clean wtree-owned initialization artifacts or transaction-created
  worktrees uses the existing incomplete-cleanup or rollback-incomplete
  contract.
- Human errors go to stderr. JSON mode continues to emit one error envelope on
  stdout and no human diagnostic on stderr.
- No configuration, manifest, registry, workspace-state, recovery, or public
  workspace-plan schema version changes because of this feature.
- Existing repository and mount ordering remains stable.

The `wtree add-ignore` command, `init --add-ignore`, and `create --add-ignore`
are not part of the supported command surface. Help, examples, tutorials, and
diagnostics must not advertise them.

## 9. Safety and portability

- All source and workspace paths pass through the central mount and
  symlink-aware containment utilities before filesystem access.
- No generated ignore target may escape its immediate parent checkout.
- Git commands remain hermetic with respect to user aliases, prompts, optional
  locks, locale, and global ignore configuration; global configuration may
  influence `check-ignore` output but can never qualify as protection.
- Behavior must work on Linux, macOS, and Windows, including spaces, Unicode,
  missing files, read-only files, mixed existing line endings, and symlinks
  where supported.
- `wtree` never edits `.git/info/exclude`, global Git configuration,
  `.gitmodules`, or a child repository to protect its own mount.
- `wtree` never stages, commits, pushes, or removes stale ignore rules.

## 10. Explicit non-goals

- A standalone ignore-management command or opt-in flag.
- Reconstructing committed-base ignore files for create preflight.
- A dedicated ignore-update action or schema in the public workspace plan.
- Editing a source checkout during create.
- Editing a historical commit, detached base, or branch that is not the newly
  created workspace branch.
- Automatically resolving conflicting negations or rewriting user patterns.
- Removing, sorting, consolidating, or otherwise cleaning existing rules.
- Protecting mounts during checkout, clone, import, update, sync, remove, or
  delete in this change.
- Treating submodules as independent nested repositories.

## 11. Acceptance scenarios

The implementation must prove at least these behaviors:

1. Init of a three-level hierarchy writes `/backend/` to the root parent and
   `/shared/` to the backend parent, then verifies both with Git before
   publishing configuration or state.
2. An effective rule in any qualifying `.gitignore` suppresses a generated
   duplicate, while local and global excludes do not.
3. Missing owning files are created safely; regular files retain content,
   order, newline bytes, and permissions; symlinks and non-regular files are
   rejected before mutation.
4. Spaces, Unicode, and supported Git-ignore metacharacters produce one
   literal rule, while line-breaking or otherwise unrepresentable mounts fail
   preflight.
5. A later source write or init publication failure leaves each changed source
   file complete, reports retained and remaining work, publishes no unsafe
   project state, and retries without duplicate rules.
6. Create with default and overridden mounts updates each newly created parent
   worktree before adding any direct child and never changes a source checkout.
7. An overriding ignore rule that keeps a mount visible causes verification to
   fail before child creation; a retry does not append the same generated line
   again.
8. A create failure cleanly rolls back transaction-owned branches and
   worktrees, or preserves unexpected dirty content with exact recovery
   evidence.
9. Successful human output identifies every changed `.gitignore`, and no
   operation stages or commits it.
10. Repeating successful init or create does not append rules for mounts that
    Git already reports as effectively ignored by `.gitignore` files.
11. Init and create dry runs report the required protections while leaving
    files, locks, branches, worktrees, recovery records, registry data, and
    workspace state unchanged.
12. After success, `git add .` in each parent cannot stage a managed child
    repository as a `160000` gitlink.
