# `wtree` – Specification

## 1. Purpose

`wtree` is a command-line tool for managing Git worktrees across projects that consist of multiple nested Git repositories.

The main use case is software development with AI coding agents, where new isolated checkouts need to be created and removed quickly.

A project may contain:

- one root Git repository
- zero or more nested independent Git repositories
- arbitrarily nested repositories
- repositories whose checkout directory names differ between workspaces
- already existing manually created worktrees that need to be imported

`wtree` treats such a project as one logical workspace.

A single command should be able to create, inspect, import, remove, or delete a complete workspace across all repositories.

Example:

```text
Original project:

~/code/product/
├── .git/
├── frontend/
└── backend/
    ├── .git/
    └── shared/
        └── .git/
```

A new workspace:

```text
~/.local/share/wtree/worktrees/<project-id>/feature-login/
├── .git
├── frontend/
└── api/
    ├── .git
    └── common/
        └── .git
```

Here:

```text
backend        → checked out as api
backend/shared → checked out as api/common
```

These are still the same repositories.

Repository identity must therefore never depend on checkout directory names.

---

# 2. Core Principles

## 2.1 Repository identity is independent of path

The following concepts must always remain separate:

```text
Repository Identity
Branch
Workspace
Checkout Path / Mount
```

A repository must never be identified by:

```text
backend/
```

because the same repository may appear in another workspace as:

```text
api/
```

Instead, `wtree` must identify repositories using their Git identity.

The primary Git identity should be derived from:

```bash
git rev-parse --path-format=absolute --git-common-dir
```

All worktrees belonging to the same Git repository share the same common Git directory.

Paths must be normalized before comparison.

---

# 3. Terminology

## Project

A logical collection of related Git repositories.

Example:

```text
product
├── root
├── backend
└── shared
```

---

## Repository

A Git repository belonging to a project.

Each repository has a stable logical ID.

Example:

```text
root
backend
shared
```

A repository has:

```text
id
commonGitDir
defaultBranch
parentRepository
defaultMount
sourceCheckout
```

---

## Workspace

A synchronized development environment consisting of one checkout per repository.

Example:

```text
feature/login
```

A workspace usually uses the same logical branch name across all repositories.

---

## Checkout

The concrete worktree of one repository inside one workspace.

A checkout contains at least:

```text
repositoryId
branch
mount
resolvedPath
```

---

## Mount

The directory where a repository appears relative to its parent repository checkout.

Example:

```text
backend:
  mount: api

shared:
  parent: backend
  mount: common
```

produces:

```text
workspace/
└── api/
    └── common/
```

Mounts should therefore preferably be stored relative to the parent repository rather than as complete workspace-relative paths.

---

# 4. Repository Hierarchy

Repositories form a hierarchy.

Example:

```text
root
└── backend
    └── shared
```

Configuration representation:

```yaml
repositories:
  root:
    parent: null
    mount: .

  backend:
    parent: root
    mount: backend

  shared:
    parent: backend
    mount: shared
```

The effective workspace path is calculated recursively.

Conceptually:

```text
effectivePath(root) = workspaceRoot

effectivePath(repo) =
    effectivePath(parent(repo))
    + checkout.mount
```

Example:

```text
backend.mount = api
shared.mount = common
```

results in:

```text
workspace/api/common
```

Changing a parent mount automatically relocates all descendants.

---

# 5. Branch Model

A workspace normally represents one logical branch across multiple repositories.

Example:

```text
Workspace: feature/login

Repository    Branch
-------------------------------
root          feature/login
backend       feature/login
shared        feature/login
```

Repositories may have different default branches:

```text
root     → main
backend  → main
shared   → develop
```

When creating:

```bash
wtree create feature/login
```

the branches could therefore be created from:

```text
root:
main → feature/login

backend:
main → feature/login

shared:
develop → feature/login
```

However, the preferred default for AI-oriented workflows should be:

```bash
wtree create feature/login --from HEAD
```

or equivalent default semantics where each repository branches from its own current HEAD.

This allows the current aggregate project state to be reproduced.

---

# 6. Workspace Storage

Worktrees must not be stored next to normal source repositories by default.

A path such as:

```text
~/code/.worktrees
```

is undesirable because IDEs, search tools, file watchers, LSPs and AI tooling may automatically discover duplicate copies.

The default should therefore use an OS-specific user data directory.

On Linux/XDG:

```text
~/.local/share/wtree/
```

Example:

```text
~/.local/share/wtree/
├── projects/
├── state/
└── worktrees/
```

Workspace layout:

```text
~/.local/share/wtree/worktrees/
└── <project-id>/
    ├── feature-login/
    ├── fix-auth/
    └── agent-task-123/
```

The equivalent OS-native data location should be used on macOS and Windows.

---

# 7. Stable Project Identity

Project directory names are not globally unique.

Example:

```text
~/code/customer-a/backend
~/code/customer-b/backend
```

Therefore every initialized project receives a stable project ID.

Example:

```yaml
project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: product
```

The project ID must remain stable even when the source project is moved.

The human-readable name is separate from the ID.

Workspace storage should use the stable ID internally.

Example:

```text
~/.local/share/wtree/worktrees/3f97ab90.../feature-login
```

UI output may display:

```text
Project: product
```

rather than the UUID.

---

# 8. Project Configuration

The project should contain a configuration file such as:

```text
.wtree.yml
```

Example:

```yaml
version: 1

project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: product

repositories:
  root:
    source: .
    parent: null
    mount: .
    default_branch: main

  backend:
    source: backend
    parent: root
    mount: backend
    default_branch: main

  shared:
    source: backend/shared
    parent: backend
    mount: shared
    default_branch: develop
```

`source` points to a known checkout used for Git repository administration and discovery.

`source` is not repository identity.

Repository identity must still be verified through Git.

---

# 9. Global Configuration

Global config should exist in an OS-specific config location.

Linux example:

```text
~/.config/wtree/config.yml
```

Example:

```yaml
worktrees:
  root: ~/.local/share/wtree/worktrees
```

Configuration precedence:

```text
1. command-line override
2. project-specific configuration
3. global user configuration
4. OS-specific built-in default
```

Commands:

```bash
wtree config get worktrees.root
wtree config set worktrees.root ~/worktrees
```

Project-specific override:

```bash
wtree config set --project worktrees.root /mnt/fastdisk/wtree
```

---

# 10. Runtime State

Git remains the authority for actual Git worktree state.

`wtree` maintains additional logical state.

Conceptually:

```text
Git
→ actual repository/worktree state

wtree state
→ expected project/workspace structure
```

The state must never blindly override Git reality.

A workspace state may look like:

```json
{
  "id": "feature-login",
  "name": "feature/login",
  "path": "/home/user/.local/share/wtree/worktrees/PROJECT/feature-login",
  "repositories": {
    "root": {
      "branch": "feature/login",
      "mount": ".",
      "resolvedPath": "/home/user/.../feature-login"
    },
    "backend": {
      "branch": "feature/login",
      "mount": "api",
      "resolvedPath": "/home/user/.../feature-login/api"
    },
    "shared": {
      "branch": "feature/login",
      "mount": "common",
      "resolvedPath": "/home/user/.../feature-login/api/common"
    }
  }
}
```

The resolved path may be persisted as a convenience but must be recomputable from the hierarchy.

---

# 11. Command Invocation Context

`wtree` should work from any directory inside a known project or workspace.

Examples:

```bash
cd ~/code/product
wtree status
```

must work.

So must:

```bash
cd ~/code/product/backend/src
wtree status
```

and:

```bash
cd ~/.local/share/wtree/worktrees/.../feature-login/api/src
wtree status
```

---

# 12. Project Discovery

Two complementary discovery mechanisms should be used.

## 12.1 Local configuration discovery

Starting from the current working directory:

```text
cwd
↑
parent
↑
parent
```

until `.wtree.yml` is found.

This is useful inside the original source checkout.

---

## 12.2 Git identity discovery

Inside generated or imported worktrees:

```bash
git rev-parse --show-toplevel
git rev-parse --path-format=absolute --git-common-dir
```

The common Git directory is matched against a global project registry.

Conceptually:

```text
cwd
 ↓
Git checkout
 ↓
commonGitDir
 ↓
known repository
 ↓
project
 ↓
workspace
```

---

# 13. Explicit Project Selection

Automatic discovery is convenience, not identity.

All relevant commands should allow:

```bash
wtree <command> --project /path/to/project ...
```

Short form:

```bash
wtree <command> -p /path/to/project ...
```

This is useful for scripts and AI agents.

---

# 14. `init`

Usage:

```bash
wtree init
```

or:

```bash
wtree init /path/to/project
```

Optional:

```bash
wtree init --worktree-root /some/path
```

`init` should:

1. identify the root repository
2. recursively discover nested Git repositories
3. determine repository hierarchy
4. determine Git common directories
5. assign stable repository IDs
6. determine source checkout paths
7. determine default mounts
8. determine default branches where possible
9. create project ID
10. create `.wtree.yml`
11. register the project globally

Example discovery:

```text
product/
├── .git
├── backend/
│   ├── .git
│   └── shared/
│       └── .git
└── frontend/
```

becomes:

```text
root
└── backend
    └── shared
```

`frontend` is not a Git repository and therefore does not become a separate repository entry.

---

# 15. Nested Repository Discovery

Discovery must avoid expensive or dangerous blind recursion through arbitrary dependency directories.

It should support exclusions.

Example:

```yaml
discovery:
  ignore:
    - node_modules/**
    - vendor/**
    - .venv/**
    - target/**
    - build/**
```

The implementation should also avoid traversing into already detected nested Git repositories in a way that confuses parent/child identification.

Repository relationships should be derived from filesystem nesting of detected repository roots.

---

# 16. `create`

Usage:

```bash
wtree create <branch>
```

Example:

```bash
wtree create feature/login
```

Creates:

- a workspace
- a worktree for every configured repository
- synchronized branches

Default workspace location:

```text
<worktree-root>/<project-id>/<sanitized-workspace-name>
```

The logical workspace name should remain:

```text
feature/login
```

even if the filesystem directory becomes:

```text
feature-login
```

or another safe encoding.

The mapping must be deterministic and collision-safe.

---

# 17. `create --from`

Examples:

```bash
wtree create feature/login --from HEAD
wtree create feature/login --from main
wtree create feature/login --from develop
```

If `HEAD` is used:

```text
each repository uses its own current HEAD
```

If an explicit ref is used:

```text
each repository attempts to resolve that ref independently
```

If one repository cannot resolve the requested ref, preflight fails before changes are made.

---

# 18. Mount Overrides

Example:

```bash
wtree create feature/login --mount backend=api
```

If:

```text
root
└── backend
    └── shared
```

and defaults are:

```text
backend → backend
shared  → shared
```

then:

```bash
--mount backend=api
```

results in:

```text
workspace/
└── api/
    └── shared/
```

The child's relative mount remains unchanged.

Multiple overrides:

```bash
wtree create feature/login \
  --mount backend=api \
  --mount shared=common
```

results in:

```text
workspace/
└── api/
    └── common/
```

Mount overrides are workspace-specific.

Every override that changes a nested repository's configured mount must be
ignored by a committed `.gitignore` rule in its parent repository at the
parent's selected base commit. Uncommitted rules, global excludes, and
repository-local excludes do not satisfy this requirement because they do not
protect the new parent worktree. Preflight must reject a missing rule before
creating any branch, worktree, or state.

---

# 19. Create Order

Nested worktrees must be created from outside to inside.

Example:

```text
root
backend
shared
```

Creation order:

```text
1. root
2. backend
3. shared
```

This ensures parent directories exist before nested checkouts are placed inside them.

---

# 20. Workspace Creation Algorithm

Conceptually:

```text
resolve project
 ↓
build workspace plan
 ↓
preflight entire plan
 ↓
create root worktree
 ↓
create nested worktrees parent-first
 ↓
validate resulting structure
 ↓
persist workspace state
```

No mutation should happen before preflight has validated the entire operation as far as possible.

---

# 21. Transactional Operations

Multi-repository operations must behave transactionally.

Example:

```text
repo 1 ✓
repo 2 ✓
repo 3 ✓
repo 4 ✗
```

The command should not leave a partially created workspace unless rollback itself fails.

Expected behavior:

```text
Creating workspace feature/login

[1/5] root       ✓
[2/5] backend    ✓
[3/5] shared     ✓
[4/5] tooling    ✗

Rolling back...

shared           ✓
backend          ✓
root             ✓

Workspace creation failed.
```

Architecture:

```text
Plan
 ↓
Validate
 ↓
Execute
 ↓
Commit state
```

On failure:

```text
Execute
 ↓
Failure
 ↓
Rollback completed actions in reverse order
```

Rollback failures must be reported explicitly.

---

# 22. Preflight Validation

Before mutations, validate as much as possible.

For `create`:

```text
✓ project configuration valid
✓ repository identities reachable
✓ repository hierarchy valid
✓ branch names valid
✓ bases resolve
✓ target paths do not conflict
✓ mounts do not overlap illegally
✓ requested branches do not conflict
✓ Git worktree constraints allow checkout
✓ target workspace is not already registered
```

For destructive operations:

```text
✓ worktrees known
✓ branches known
✓ working trees clean where required
✓ branches not checked out elsewhere
✓ no unexpected nested repositories
```

---

# 23. `--dry-run`

All mutating commands should support:

```bash
--dry-run
```

Example:

```bash
wtree create feature/login --dry-run
```

Output:

```text
Workspace: feature/login

Repository   Base       Branch          Mount
------------------------------------------------
root         HEAD       feature/login   .
backend      HEAD       feature/login   backend
shared       HEAD       feature/login   backend/shared

Target:
/home/me/.local/share/wtree/worktrees/PROJECT/feature-login

No changes made.
```

`--dry-run` must execute planning and validation but no mutation.

---

# 24. `checkout`

Usage:

```bash
wtree checkout <branch>
```

Purpose:

Create a workspace for branches that already exist.

Difference:

```text
create
→ create new branches + worktrees

checkout
→ use existing branches + create worktrees
```

If required branches do not exist, the operation fails unless an explicitly designed fallback option exists.

Do not silently create missing branches.

---

# 25. `list`

Usage:

```bash
wtree list
```

Example:

```text
WORKSPACE       PATH                                    STATUS
feature/login   ~/.local/share/wtree/.../feature-login  clean
fix/auth        ~/.local/share/wtree/.../fix-auth       modified
agent/task-42   ~/.local/share/wtree/.../agent-task-42  clean
```

Machine-readable:

```bash
wtree list --json
```

---

# 26. `status`

Usage:

```bash
wtree status
wtree status feature/login
```

If no workspace is specified, infer it from current location when possible.

Example:

```text
Workspace: feature/login

REPOSITORY   BRANCH          MOUNT        STATUS
root         feature/login   .            clean
backend      feature/login   api          modified
shared       feature/login   api/common   clean
```

Optional detailed output may contain:

```text
ahead
behind
modified files
untracked files
detached HEAD
missing checkout
unexpected branch
```

JSON:

```bash
wtree status --json
```

Example:

```json
{
  "workspace": "feature/login",
  "repositories": [
    {
      "id": "root",
      "branch": "feature/login",
      "path": "/...",
      "clean": true
    },
    {
      "id": "backend",
      "branch": "feature/login",
      "path": "/.../api",
      "clean": false
    }
  ]
}
```

---

# 27. `path`

Usage:

```bash
wtree path feature/login
```

Output should be only the absolute path by default:

```text
/home/me/.local/share/wtree/worktrees/.../feature-login
```

This enables:

```bash
cd "$(wtree path feature/login)"
```

No decorative text should be printed to stdout for this command unless requested.

Diagnostics may go to stderr.

---

# 28. Repository Path Lookup

Useful for agents:

```bash
wtree repo path backend
```

When invoked inside `feature/login`:

```text
/home/me/.local/share/wtree/.../feature-login/api
```

This means callers never need to know whether the repository currently lives under:

```text
backend
api
server
services/api
```

Repository ID is the stable abstraction.

Suggested structured variant:

```bash
wtree repo get backend --json
```

Example:

```json
{
  "id": "backend",
  "workspace": "feature/login",
  "branch": "feature/login",
  "mount": "api",
  "path": "/.../feature-login/api"
}
```

---

# 29. `remove`

Usage:

```bash
wtree remove feature/login
```

Meaning:

```text
remove worktrees
keep branches
```

This should allow later recreation through:

```bash
wtree checkout feature/login
```

Removal order is reverse hierarchy:

```text
shared
backend
root
```

Nested repositories must always be removed before their parents.

---

# 30. `delete`

Usage:

```bash
wtree delete feature/login
```

Meaning:

```text
remove workspace worktrees
delete corresponding branches
```

Conceptual order:

```text
1. preflight all repositories
2. verify destructive operation is safe
3. remove nested worktrees deepest-first
4. remove root worktree
5. delete branches
6. remove workspace state
```

Branch deletion should be conservative by default.

Unmerged branches should not be forcibly removed unless `--force` is explicitly supplied.

---

# 31. `--force`

`--force` must never become a generic "ignore all safety checks" switch.

Each operation should explicitly define what additional unsafe actions `--force` permits.

Examples:

```text
allow removal of modified worktrees
allow deletion of unmerged branches
allow repair of conflicting state
```

It should not bypass unrelated integrity checks.

---

# 32. Import Existing Workspaces

Import is a first-class feature.

Usage from a known project:

```bash
cd ~/code/product
wtree import ~/experiments/login-test
```

Or from the existing workspace itself:

```bash
cd ~/experiments/login-test
wtree import
```

---

# 33. Import Example

Known project:

```text
~/code/product/
├── .git
└── backend/
    └── .git
```

Existing external workspace:

```text
~/experiments/login-test/
├── .git
└── api/
    └── .git
```

Import discovers:

```text
login-test root
→ commonGitDir matches repository "root"

login-test/api
→ commonGitDir matches repository "backend"
```

Result:

```text
Workspace: login-test

REPOSITORY   MOUNT   BRANCH
root         .       feature/login
backend      api     feature/login
```

The fact that `backend` was checked out under `api` must be persisted.

All later operations must use `api`.

---

# 34. Import Must Use Git Identity

Import must never infer repository identity from directory names.

Wrong:

```text
directory is "api"
→ therefore repository id is "api"
```

Correct:

```text
api/
 ↓
git common dir
 ↓
known repository identity
 ↓
backend
```

---

# 35. Import Discovery

When importing:

1. identify target workspace root
2. identify its root Git checkout
3. resolve root repository identity
4. resolve project from registry
5. scan nested Git repositories
6. resolve every nested repository by common Git directory
7. derive parent-child placement
8. determine actual mounts
9. determine branches
10. validate completeness
11. persist workspace state

Unknown Git repositories should be reported.

The implementation should not silently attach unknown repositories to the project.

Potential future behavior may offer explicit adoption, but not implicitly.

---

# 36. Partial Imports

If a known project contains:

```text
root
backend
shared
```

but the imported workspace contains only:

```text
root
backend
```

the import should detect the missing `shared`.

Default behavior should be explicit.

Possible output:

```text
Workspace contains only 2 of 3 configured repositories.

✓ root
✓ backend
✗ shared missing
```

The implementation plan should define whether this is:

- allowed as an incomplete workspace
- rejected by default
- allowed via an explicit flag

The recommended default is to reject incomplete imports unless an explicit option such as:

```bash
--allow-partial
```

is used.

Partial state must then be represented explicitly rather than treated as healthy.

---

# 37. Workspace-Specific Mount Persistence

Once a workspace has been created or imported, all operations must use the workspace's persisted mount mapping.

Never recalculate a repository path from its source path.

Forbidden logic:

```text
workspaceRoot + repository.source
```

Correct logic:

```text
workspace.checkout(repositoryId).resolvedPath
```

or:

```text
resolve effective mount through workspace checkout hierarchy
```

---

# 38. Repository Resolver

All Git operations should go through a central resolver.

Conceptual API:

```text
workspace.resolveRepository("backend")
```

returns:

```text
/home/me/.../feature-login/api
```

Every command must use this resolver.

No command-specific code should reconstruct repository paths independently.

---

# 39. `doctor`

Usage:

```bash
wtree doctor
wtree doctor feature/login
```

Purpose:

Compare:

```text
expected logical state
vs
actual Git/filesystem state
```

Possible checks:

```text
repository source checkout reachable
common Git directory still matches
workspace root exists
checkout exists
branch matches expected branch
mount matches configured mount
Git recognizes worktree
nested hierarchy valid
no duplicate repository checkout
no unknown nested repository
state entry not stale
```

Example:

```text
Workspace: feature/login

✓ root
  branch: feature/login

⚠ backend
  configured mount: backend
  actual mount: api
  repository identity matches

✓ shared
```

---

# 40. `doctor --fix`

Usage:

```bash
wtree doctor --fix
```

Safe repairs may include:

```text
update stale mount metadata
remove stale workspace state
run git worktree repair/prune when appropriate
repair resolvable paths
```

Potentially destructive repairs should not happen automatically.

They require an explicit command or `--force`.

---

# 41. Git Worktree Pruning and Repair

The implementation should account for manually removed worktree directories.

Relevant Git operations include:

```bash
git worktree list --porcelain
git worktree prune
git worktree repair
```

`doctor` should use these carefully where appropriate.

Git remains the source of truth for registered worktrees.

---

# 42. Existing Branch Checked Out Elsewhere

Git does not normally allow the same local branch to be checked out simultaneously in multiple worktrees.

`wtree` must detect this during preflight.

Example:

```text
feature/login is already checked out for backend
```

The operation should fail clearly rather than allowing a later Git command to fail unexpectedly.

---

# 43. Dirty Worktrees

`remove` and `delete` must detect:

```text
modified files
staged files
untracked files where relevant
```

Default:

```text
refuse destructive removal
```

With explicit:

```bash
--force
```

the implementation may permit removal, but should report exactly which safety was overridden.

---

# 44. Detached HEAD

Imported workspaces may contain detached HEADs.

State must support:

```text
branch: null
head: <commit>
detached: true
```

Commands that require synchronized branch semantics should report this clearly.

Import should not invent a branch name.

---

# 45. Workspace Naming

Logical names may contain Git branch slashes:

```text
feature/login
agent/task-123
fix/auth/token
```

Filesystem representation must be safe.

Do not naïvely use branch names as nested directories unless that is intentional.

Possible strategy:

```text
feature/login → feature-login
```

But collisions must be avoided:

```text
feature/login
feature-login
```

would otherwise collide.

The implementation should use either:

- reversible escaping
- encoded names
- readable prefix plus stable hash
- state mapping from logical name to storage directory

Example:

```text
feature-login--91c42a
```

The exact scheme is an implementation decision but must be deterministic and collision-safe.

---

# 46. Workspace Branch Names May Diverge

The primary workflow synchronizes branch names.

However, the data model should not assume all checkouts always have identical branches.

It should store branch per checkout.

Example:

```json
{
  "root": {
    "branch": "feature/login"
  },
  "backend": {
    "branch": "feature/login-backend"
  }
}
```

The CLI may initially avoid exposing branch remapping, but the internal model should support it.

This is especially useful for imports.

---

# 47. `--json`

All important inspection commands should support machine-readable output.

At minimum:

```text
list
status
repo get
doctor
config get
```

Mutating commands should also support JSON summaries.

Example:

```bash
wtree create feature/login --json
```

Possible output:

```json
{
  "success": true,
  "project": "product",
  "workspace": "feature/login",
  "path": "/...",
  "repositories": [
    {
      "id": "root",
      "branch": "feature/login",
      "path": "/..."
    }
  ]
}
```

Error output should have a stable structure when `--json` is active.

---

# 48. Exit Codes

The CLI should use predictable non-zero exit codes.

The implementation plan should define a stable error taxonomy.

Possible categories:

```text
0  success
1  generic failure
2  invalid arguments
3  project not found
4  workspace not found
5  validation/preflight failure
6  Git operation failure
7  dirty workspace
8  conflict
9  rollback incomplete
```

Exact numbers may differ, but machine consumers need stable semantics.

---

# 49. stdout vs stderr

Commands intended for shell composition must keep stdout clean.

Example:

```bash
wtree path feature/login
```

stdout:

```text
/home/me/...
```

Warnings and diagnostics:

```text
stderr
```

Likewise:

```bash
wtree repo path backend
```

should produce only the resolved path on stdout.

---

# 50. Shell Integration

An external executable cannot change the parent shell's current directory.

Therefore:

```bash
wtree path feature/login
```

should be the primary primitive.

Usage:

```bash
cd "$(wtree path feature/login)"
```

Optional shell integration may be provided later:

```bash
eval "$(wtree shell-init zsh)"
```

which could define conveniences such as:

```bash
wt feature/login
```

This is not required for the initial implementation.

---

# 51. Agent-Oriented Workflow

A typical AI coding workflow:

```bash
wtree create agent/task-123
wtree path agent/task-123
```

Agent works inside returned path.

Inspect state:

```bash
wtree status agent/task-123 --json
```

Locate specific nested repository:

```bash
wtree repo path backend
```

After completion:

```bash
wtree remove agent/task-123
```

or:

```bash
wtree delete agent/task-123
```

The agent never needs to understand where nested repositories are physically mounted.

---

# 52. `new` Convenience Alias

Optional convenience command:

```bash
wtree new agent/task-123
```

Equivalent to:

```bash
wtree create agent/task-123 --from HEAD
```

This is useful for AI coding workflows.

It may be omitted from the MVP if minimizing CLI surface is preferred.

---

# 53. Complete MVP Command Set

Recommended initial command set:

```text
wtree init
wtree import
wtree create
wtree checkout
wtree list
wtree status
wtree path
wtree repo path
wtree repo get
wtree remove
wtree delete
wtree doctor
wtree config
```

Optional:

```text
wtree new
wtree shell-init
```

---

# 54. Common Global Options

Recommended:

```text
-h, --help
--how-to
-p, --project <path>
--json
--dry-run
--verbose
--force
--version
```

Not every option has meaning for every command.

Unsupported combinations should be rejected clearly rather than ignored silently.

---

# 55. Complete `-h` / `--help`

Running:

```bash
wtree -h
```

or:

```bash
wtree --help
```

must show the complete CLI command reference.

This must be more comprehensive than a typical minimal argument-parser help page.

Example structure:

```text
wtree — manage synchronized Git workspaces across nested repositories

USAGE
  wtree <command> [arguments] [options]

DESCRIPTION
  ...

GLOBAL OPTIONS
  ...

COMMANDS
  init
  import
  create
  checkout
  list
  status
  path
  repo
  remove
  delete
  doctor
  config

CONCEPTS
  project
  workspace
  repository
  mount
  repository identity

WORKTREE LOCATION
  ...

EXAMPLES
  ...

Run:
  wtree <command> --help

for detailed command help.
```

The global help should explain enough concepts that a new user can understand what `wtree` manages.

---

# 56. Per-Command Help

Example:

```bash
wtree create -h
```

must show full help for `create`.

Example:

```text
wtree create — create a synchronized workspace

USAGE
  wtree create <branch> [options]

DESCRIPTION
  Creates a new workspace containing worktrees for all configured
  repositories.

ARGUMENTS
  <branch>
      Branch/workspace name.

OPTIONS
  --from <ref>
  --path <path>
  --mount <repo>=<mount>
  --dry-run
  --json
  --force
  -h, --help

EXAMPLES
  wtree create feature/login
  wtree create feature/login --from main
  wtree create feature/login --mount backend=api
  wtree create feature/login --dry-run
```

Equivalent detailed help should exist for every command.

---

# 57. `--how-to`

`--how-to` is distinct from help.

```bash
wtree --how-to
```

must display a complete practical user guide.

Help answers:

```text
What commands and options exist?
```

How-to answers:

```text
How do I actually use the tool?
```

---

# 58. Global How-To Content

The built-in guide should cover at least:

```text
1. What wtree is
2. Initialize a project
3. What repository discovery does
4. Configure worktree storage
5. Create a workspace
6. Create from HEAD
7. Create from another branch/ref
8. Override nested repository mounts
9. Work inside a workspace
10. Resolve workspace paths
11. Resolve repository paths
12. Inspect status
13. Import an existing workspace
14. Import renamed nested checkouts
15. Remove a workspace
16. Restore an existing branch with checkout
17. Delete workspace and branches
18. Diagnose inconsistencies
19. Use --dry-run
20. Use --json
21. Use wtree from nested directories
22. Use --project explicitly
23. AI coding agent workflow
24. Important safety semantics
```

The guide is shipped with the installed version.

Therefore the guide should always describe that exact version's behavior.

---

# 59. Per-Command How-To

Commands may also support:

```bash
wtree create --how-to
wtree import --how-to
wtree doctor --how-to
```

This should provide examples and common workflows specific to that command.

Particularly important for:

```text
create
import
remove
delete
doctor
```

---

# 60. Help Output Stability

Human-readable help does not have to be API-stable at the whitespace level.

However:

- command names should be stable
- option names should be stable
- examples should be valid
- semantics must match the installed version

Machine integrations should rely on `--json`, not parsing help text.

---

# 61. Domain Model

Suggested logical model:

```text
Project
 ├── Repository[]
 └── Workspace[]
       └── Checkout[]
```

Conceptual types:

```typescript
type Project = {
  id: string
  name: string
  configPath: string
  repositories: Repository[]
}

type Repository = {
  id: string
  commonGitDir: string
  sourcePath: string
  parentId: string | null
  defaultMount: string
  defaultBranch?: string
}

type Workspace = {
  id: string
  name: string
  rootPath: string
  checkouts: Checkout[]
}

type Checkout = {
  repositoryId: string
  branch: string | null
  head: string
  detached: boolean
  mount: string
  resolvedPath: string
}
```

---

# 62. Suggested Internal Architecture

Recommended separation:

```text
CLI
 │
 ▼
Application / WorkspaceService
 │
 ├── ProjectDiscovery
 ├── RepositoryDiscovery
 ├── RepositoryIdentityResolver
 ├── WorkspaceResolver
 ├── WorkspacePlanner
 ├── GitAdapter
 ├── ConfigStore
 ├── StateStore
 ├── ProjectRegistry
 ├── TransactionRunner
 ├── Validator
 └── OutputRenderer
```

---

# 63. GitAdapter

The Git adapter should be deliberately simple.

Conceptual operations:

```text
getCommonGitDir(path)
getTopLevel(path)
getHead(path)
getCurrentBranch(path)
resolveRef(repo, ref)

listWorktrees(repo)
branchExists(repo, branch)
branchCheckedOut(repo, branch)

createBranch(repo, branch, base)
deleteBranch(repo, branch, force)

addWorktree(repo, path, branch)
removeWorktree(repo, path, force)

status(repo)
isClean(repo)

worktreePrune(repo)
worktreeRepair(repo)
```

Business logic should not live inside the Git adapter.

---

# 64. WorkspacePlanner

Planning should be separate from execution.

Example:

```text
CreateWorkspacePlan
├── CreateBranch(root)
├── AddWorktree(root)
├── CreateBranch(backend)
├── AddWorktree(backend)
├── CreateBranch(shared)
└── AddWorktree(shared)
```

A plan can then be:

```text
validated
rendered
serialized
dry-run
executed
rolled back
```

This greatly improves testability.

---

# 65. TransactionRunner

Each mutation step should optionally define an inverse operation.

Conceptually:

```text
Step:
  execute()
  rollback()
```

Example:

```text
CreateBranch
rollback → DeleteBranch

AddWorktree
rollback → RemoveWorktree
```

Rollback runs in reverse completion order.

Operations whose inverse is unsafe or impossible must be modeled explicitly.

---

# 66. State Commit Semantics

Workspace state should only become authoritative after successful execution.

Recommended flow:

```text
prepare temporary state
execute Git operations
validate result
atomically write workspace state
```

State writes should use atomic file replacement where possible.

A crash should not leave half-written JSON/YAML files.

---

# 67. Concurrency

AI agents may execute commands concurrently.

The implementation should therefore consider locking.

At minimum, serialize mutations:

```text
per project
```

Potentially:

```text
per project registry
per workspace
```

Locking should prevent:

```text
two simultaneous creates with same workspace
create racing with delete
state corruption
```

Read-only commands such as `status` may not need exclusive locking.

---

# 68. Path Safety

All paths must be normalized.

The tool should reject dangerous mount values such as:

```text
../../outside
```

unless explicitly designed to support external mounts.

Recommended rule:

```text
repository mounts must stay within the workspace root
```

Nested repository paths must not escape their parent checkout.

Symlinks require careful handling.

Path comparisons should use canonicalized paths where appropriate.

---

# 69. Mount Conflicts

Preflight must detect invalid combinations such as:

```text
backend → api
shared  → ..
```

or two sibling repositories both mapped to:

```text
services
```

Also detect a repository mount overlapping a normal tracked file or directory in a way that prevents the worktree from being created.

---

# 70. Root Repository Content vs Nested Worktree Mount

A root repository may track a directory where a nested independent repository normally lives.

Before replacing/populating that directory with a nested worktree, `wtree` must understand how the current project is structured.

Potential conflicts must be preflighted.

The implementation should not blindly delete files from the outer worktree to make room for a nested repository.

Any required replacement semantics must be explicit and safe.

---

# 71. Independent Nested Repositories

The primary model described here concerns independent nested Git repositories.

They are not assumed to be Git submodules.

Submodules have different semantics.

Submodule support may be a future extension and should not accidentally be conflated with independent repositories.

During discovery, the tool should be able to distinguish:

```text
independent nested Git repository
Git submodule checkout
```

If submodules are unsupported initially, report them explicitly.

---

# 72. Repository Identity Changes

If a repository is recloned or its Git metadata path changes, the common Git directory may change even though the remote repository conceptually remains the same.

For the MVP, identity may be tied to `commonGitDir`.

`doctor` should detect when a configured source checkout no longer matches the stored identity.

A future version may support stronger identity using additional information such as:

```text
remote URL
repository UUID
initial commit identity
explicit project metadata
```

But path-independent worktree identity must remain the immediate requirement.

---

# 73. Imported Workspace Names

When running:

```bash
cd ~/experiments/login-test
wtree import
```

the default workspace name may be derived from:

- branch name, if all repositories use the same branch
- directory name
- explicit `--name`

Recommended:

```bash
wtree import --name feature/login
```

should always allow overriding inference.

Import must not silently pick an ambiguous name if the branch topology is inconsistent.

---

# 74. Import Branch Mismatch

Example imported workspace:

```text
root     feature/login
backend  login-api
shared   detached HEAD
```

This must be preserved accurately.

Output should indicate:

```text
Workspace branches are not synchronized.
```

Do not rewrite branches during import.

---

# 75. `status` Drift Detection

`status` should distinguish normal Git dirtiness from workspace drift.

Example:

```text
backend:
  expected branch: feature/login
  actual branch:   hotfix/test
```

This is not merely "modified"; it is structural drift.

Possible statuses:

```text
clean
modified
missing
branch-mismatch
mount-mismatch
detached
unknown-repository
stale-state
```

---

# 76. Project Registry

A global registry allows `wtree` to recognize worktrees outside the original project directory.

Conceptually:

```json
{
  "projects": {
    "3f97...": {
      "name": "product",
      "configPath": "/home/me/code/product/.wtree.yml",
      "repositories": {
        "/home/me/code/product/.git": "root",
        "/home/me/code/product/backend/.git": "backend"
      }
    }
  }
}
```

The actual on-disk format is an implementation detail.

It should support atomic updates and project relocation.

---

# 77. Original Checkout Is Also a Workspace

The original source checkout should conceptually be treated as a workspace too, even if stored slightly differently.

Example:

```text
Workspace: default
Path: ~/code/product
```

This makes commands such as:

```bash
wtree status
wtree repo path backend
```

behave consistently in both original and generated workspaces.

Whether the original workspace is explicitly persisted or dynamically resolved is an implementation decision.

---

# 78. Current Workspace Detection

Given a current path:

```text
~/.local/share/wtree/.../feature-login/api/src
```

`wtree` should detect:

```text
Project: product
Workspace: feature/login
Repository: backend
Mount: api
```

This requires matching the current Git checkout and/or filesystem location against workspace state.

---

# 79. Command Behavior Without Project Context

If no project can be discovered:

```bash
wtree status
```

should fail clearly:

```text
No wtree project could be determined from the current directory.

Use:
  wtree init
or:
  wtree status --project <path>
```

Do not silently pick an arbitrary registered project.

---

# 80. Ambiguous Project Detection

If repository identity matches multiple project definitions, do not guess.

Require:

```bash
--project
```

and explain the ambiguity.

---

# 81. Logging and Verbose Mode

Normal output should remain concise.

With:

```bash
--verbose
```

show additional details such as:

```text
resolved project
configuration paths
repository identity
Git commands being invoked
resolved mounts
state paths
rollback actions
```

Avoid leaking sensitive environment values unnecessarily.

---

# 82. Error Messages

Errors should state:

```text
what failed
which repository
which workspace
what Git operation failed
whether rollback succeeded
what the user can do next
```

Example:

```text
Failed to create workspace "feature/login".

Repository:
  backend

Reason:
  branch "feature/login" is already checked out at:
  /home/me/other-checkout

No changes were made.
```

---

# 83. Safety Invariant

A central invariant:

> No mutating multi-repository operation should intentionally leave the project in a partially updated logical workspace state.

If rollback cannot fully restore the prior state:

```text
exit non-zero
record recovery metadata
show explicit recovery instructions
```

`doctor` should be able to inspect such situations.

---

# 84. Repository Ordering

The project graph must be acyclic.

Creation:

```text
topological order parent → child
```

Removal:

```text
reverse topological order child → parent
```

The graph validator should reject cycles in configuration.

---

# 85. Configuration Validation

On load:

```text
version supported
project id valid
repository IDs unique
root exists
exactly one logical root
parents exist
no cycles
mount values safe
source paths valid where required
repository identities match
```

Do not defer obvious configuration errors until halfway through a mutation.

---

# 86. Config Versioning

Config contains:

```yaml
version: 1
```

State and global registry should also be versioned.

Future migrations should be explicit.

Unknown newer versions should not be silently rewritten.

---

# 87. Possible Config Commands

Suggested:

```bash
wtree config get <key>
wtree config set <key> <value>
wtree config unset <key>
wtree config list
```

Scopes:

```text
global
project
```

Example:

```bash
wtree config get worktrees.root
wtree config set worktrees.root ~/wtree-data
wtree config set --project worktrees.root /mnt/ssd/wtree
```

---

# 88. Example End-to-End Workflow

Original:

```text
~/code/product/
├── .git
├── frontend/
└── backend/
    ├── .git
    └── shared/
        └── .git
```

Initialize:

```bash
cd ~/code/product
wtree init
```

Create:

```bash
wtree create feature/login
```

Result:

```text
~/.local/share/wtree/worktrees/PROJECT/feature-login/
├── .git
├── frontend/
└── backend/
    ├── .git
    └── shared/
        └── .git
```

Create with renamed mounts:

```bash
wtree create feature/new-api \
  --mount backend=api \
  --mount shared=common
```

Result:

```text
feature-new-api/
└── api/
    └── common/
```

Find workspace:

```bash
wtree path feature/new-api
```

Find backend:

```bash
cd "$(wtree path feature/new-api)"
wtree repo path backend
```

returns:

```text
.../feature-new-api/api
```

Inspect:

```bash
wtree status --json
```

Remove worktrees only:

```bash
wtree remove feature/new-api
```

Restore:

```bash
wtree checkout feature/new-api
```

Delete completely:

```bash
wtree delete feature/new-api
```

---

# 89. Example Import Workflow

Known source project:

```text
~/code/product/
└── backend/
```

Existing manually created worktree:

```text
~/experiments/login/
└── api/
```

Run:

```bash
cd ~/experiments/login
wtree import
```

Discovery:

```text
.
→ root repository

api/
→ Git identity == configured backend repository
```

Persist:

```text
root:
  mount: .

backend:
  mount: api
```

Subsequently:

```bash
wtree repo path backend
```

returns:

```text
~/experiments/login/api
```

and:

```bash
wtree remove <workspace>
```

must remove the actual `api` worktree, not assume `backend`.

---

# 90. Required Architectural Invariants

The implementation must preserve these invariants:

1. Repository identity must never be based solely on checkout path.

2. A workspace explicitly maps repositories to concrete checkouts.

3. Every checkout has its own branch and mount.

4. Mounts are preferably relative to repository parents.

5. All effective paths are resolved centrally.

6. No command independently reconstructs nested repository paths.

7. Creation proceeds parent-first.

8. Removal proceeds child-first.

9. Git is authoritative for actual worktree state.

10. `wtree` state describes logical project/workspace state.

11. Multi-repository mutations are planned and preflighted before execution.

12. Failed mutations attempt rollback.

13. Workspace state is committed only after successful Git operations.

14. Existing manually created worktrees can be imported.

15. Import identifies repositories using Git identity, not directory names.

16. Renamed nested mounts must be preserved permanently per workspace.

17. `cwd` is convenience, not identity.

18. Commands must also support explicit project selection.

19. Worktrees live outside ordinary source directories by default.

20. Human output and machine-readable output are separate concerns.

---

# 91. Primary Product Goal

The tool should make this workflow trivial:

```bash
wtree create agent/task-123
cd "$(wtree path agent/task-123)"
```

An AI coding agent can then work in an isolated synchronized project checkout containing every nested repository.

When finished:

```bash
wtree remove agent/task-123
```

or:

```bash
wtree delete agent/task-123
```

The agent should not need to know:

- how many Git repositories exist
- where nested repositories are mounted
- whether a nested repository is called `backend`, `api`, or `server` in this workspace
- where worktrees are physically stored
- how individual Git worktree commands need to be ordered
- how rollback across repositories works

`wtree` owns this abstraction.

---

# 92. Essential Design Statement

The central conceptual model of `wtree` is:

```text
Project
  ↓
Repository Identity
  ↓
Workspace
  ↓
Checkout
  ├── branch
  └── mount
```

A repository is identified by Git identity.

A workspace determines where and how that repository is checked out.

This distinction is fundamental to the entire implementation.

---

# 93. Implementation Planning Guidance

An implementation plan derived from this specification should explicitly cover:

- CLI framework and command structure
- domain model
- Git abstraction
- project discovery
- nested repository discovery
- repository identity resolution
- configuration model
- global project registry
- workspace state model
- OS-specific storage locations
- mount resolution
- branch synchronization
- workspace planning
- preflight validation
- transaction and rollback model
- create algorithm
- checkout algorithm
- import algorithm
- remove algorithm
- delete algorithm
- doctor/repair behavior
- JSON output contracts
- error taxonomy and exit codes
- locking/concurrency
- filesystem/path safety
- configuration migrations
- tests for nested and renamed repositories
- tests for failure and rollback
- built-in help
- built-in how-to documentation
- packaging and distribution
- cross-platform behavior

The implementation plan should preferably be split into independently testable milestones, beginning with the domain model and Git abstraction before implementing destructive workspace operations.
