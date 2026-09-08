# Workspace name shorthand specification

Status: planned
Source idea: [Workspace name shorthand](../ideas/search-for-branch-if-no-exact-match.md)
Implementation plan: [Workspace name shorthand implementation plan](../plans/workspace-name-shorthand.md)

## 1. Purpose and scope

Make short workspace names useful in everyday navigation and restoration.
`wtree path harden` selects `feat/harden-loops` when it is the only eligible
workspace whose name contains `harden`.

Only `path`, `status`, and `checkout` gain default substring selection and a
command-local Boolean `--exact` flag. Matching is case-sensitive and literal.
There is no exact-name priority, ranking, chooser, terminal detection, or
prompt. Multiple matches always fail and report the choices.

This specification extends the selection behavior in the base
[`wtree` specification](wtree.spec.md). It does not broaden destructive command
selection, Git branch discovery, checkout branch creation, remote checkout,
missing-branch policies, or partial-workspace restoration. Project resolution,
worktree ownership, transaction, rollback, recovery, and normal command
validation remain authoritative.

## 2. Inputs and selection

### 2.1 Command surface

| Command | Default selection | `--exact` selection |
|---|---|---|
| `path <workspace>` | Eligible workspace name substring | Eligible full workspace name or persisted ID |
| `status [workspace]` | Eligible workspace name substring when supplied | Eligible full workspace name or persisted ID when supplied |
| `checkout <workspace-or-branch>` | Registered workspace name substring | Full workspace name or persisted ID; otherwise exact existing local branch |

`--exact=false` uses default matching. Do not add `--match`, an alias, a
configuration default, or an environment override. Do not add `--json` to
`path`. Commands outside this table do not gain `--exact` or substring
selection; in particular, `remove`, `delete`, `doctor`, hooks, repository
commands, and release commands retain their existing argument semantics.

An omitted `status` selector continues to inspect the current workspace;
`status --exact` is also current-workspace inspection. This implicit lookup
does not enumerate substring candidates. `path` and `checkout` still require
one argument. An explicitly empty string is `invalid_arguments` (exit 2),
including with `--exact`. Do not trim, case-fold, Unicode-normalize, expand
wildcards, interpret regex syntax, or correct spelling in a supplied selector.

### 2.2 Candidate source and matching

Resolve the project through existing read-only project resolution, including
`--project` and `--data-dir`. Enumerate the project's authoritative validated
workspace state using the existing inventory boundary. Include a registered
`default` workspace normally; do not manufacture state or discover Git branches
to enlarge this inventory. An imported workspace is matched by its logical
name regardless of the branches recorded in its checkouts.

In default mode, compare the query with the full `workspace.Name` using
case-sensitive substring containment. Full names receive no priority. IDs,
branch names, directory names, and paths are not additional match fields.

In exact mode, equality with a full name or persisted ID identifies a
candidate, once per workspace. Preserve conflict detection when one selector
is the name of one workspace and the ID of another. Exact selection does not
override the eligibility rules in section 3.

After command-specific eligibility filtering:

1. Zero candidates: fail with `workspace_not_found` (exit 4), subject only to
   the explicit exact-branch checkout rule in section 4.
2. One candidate: use that workspace's full name, identity, root, and retained
   checkout data through normal command validation. Do not pass the substring
   downstream as a branch name or workspace identity.
3. Multiple candidates: fail with `conflict` (exit 8), listing all eligible
   candidates in ascending case-sensitive name order, then ID order for ties.
   Do not pick an identical name, shortest name, or first candidate.

For `feat/foo` and `feat/foo-tests`, querying `feat/foo` in default mode is
ambiguous. `--exact feat/foo` selects the former if exact identity is unique.
Presence of an unusable but eligible candidate must not cause the resolver to
choose a different candidate that happens to pass checkout validation.

## 3. Removed workspace eligibility

### 3.1 Definition without a schema migration

Workspace state currently has no removal marker. For this feature, a retained
workspace is classified as removed only when both facts are established:

- Every recorded checkout path is definitely absent on disk.
- None of those paths remains registered as a worktree of its configured Git
  repository, including stale or prunable registrations.

Observe recorded checkouts, not intentionally omitted repository IDs in a
partial workspace. A state with no recorded checkouts is not proof of removal;
surface a validation failure. A logical root or grouping directory left behind
after removal does not make the workspace present.

Use local filesystem observations and existing `ListWorktrees` Git operations.
Only definite not-exist results establish absence. Permission, I/O, or Git
observation errors fail selection through the existing error taxonomy; never
discard an uncertain candidate to turn ambiguity into uniqueness. An existing
node, including a dangling symlink or wrong node type, is evidence of damage,
not proof of removal. Inspect existing ancestors sufficiently to avoid treating
an unresolved symlink as a definitely missing checkout. Compare Git paths using
the repository's platform-aware path rules, including Windows case handling
and existing-ancestor canonicalization for absent leaves.

This is an observation of absence, not a record of how it happened. Manual
removal followed by Git pruning can be indistinguishable from `wtree remove`.
No removal timestamp, migration, Git prune/repair, cleanup, network access, or
new persistent inventory is introduced. Cache Git worktree lists per repository
within one selection operation only.

### 3.2 Command policies

| Observed state | `path` / explicit `status` | `checkout` |
|---|---|---|
| Present workspace | Include | Include |
| Some recorded checkouts missing | Include; normal command checks apply | Include; normal checkout checks apply |
| All recorded checkouts absent, with a remaining Git registration | Include as damaged | Include; normal checkout checks apply |
| All recorded checkouts absent, no remaining registrations | Exclude as removed | Include retained state |
| Observation cannot establish eligibility | Fail observation | No absence classification needed; normal preflight applies |

Apply these rules to both default and exact selection. Fully removed
workspaces are inspectable using the unchanged exact `doctor` workflow and
restorable with `checkout`; `--exact` does not make their paths usable.

For `path`, after unique selection, require the stored logical root to be an
accessible directory; reject a missing root or a root that is itself a symlink
or non-directory with `validation` (exit 5). A platform's canonical ancestor
alias is not itself a root symlink. Do not fall through to a different workspace
on validation failure. This remains a scalar path lookup, not full repository
health validation; use `status` or `doctor` to diagnose damaged checkouts.

Observe eligibility only for name/ID candidates after the existing inventory
has validated state. Existing inventory validation errors remain failures;
do not skip corrupt state files or introduce a tolerant inventory rewrite.

## 4. Checkout resolution and execution

Default checkout searches registered workspace names, including removed ones.
When no workspace matches, fail with guidance to use
`wtree checkout --exact <full-branch-name>` for an existing local branch that
has no workspace. Do not fall back to local or remote branch discovery.

With `--exact`, first use existing exact workspace name/ID lookup. An exact
workspace conflict or invalid state fails; only an actual absence of exact
workspace state permits the existing exact local-branch checkout path. That
path still requires the branch in every participating repository and never
creates or fetches branches. Convert a selected persisted ID to its workspace's
full name before branch checks.

Select once and retain the selected identity and state snapshot through plan
and execution. Normal retained mounts, explicit `--path`, `--worktree-root`,
`--mount`, partial/detached checks, companion behavior, hook behavior, and
branch validation apply without reinterpretation of the query. Successful
human and JSON results report the resolved full workspace name.

No-match, ambiguity, or invalid selection must fail before registry
reconciliation, worktree/branch effects, hook execution, or state publication.
Dry runs perform the same selection and validation without mutation.

If selected state changes or disappears before execution, fail with `conflict`
instead of repeating substring selection, choosing another workspace, or
falling back to branch checkout. Carry a state-generation precondition into
registry reconciliation and the existing locked transaction revalidation.
For exact branch-only checkout, retain an absence precondition so a newly
appearing workspace cannot silently change the target. Preserve established
lock order and rollback/recovery guarantees; a later failure can retain the
existing documented reconciliation effects but must not switch targets.

## 5. Output and error contract

No command in this feature reads input for selection. TTYs, pipes, shell
interpolation, `--json`, `--dry-run`, and verbose output do not change matching.

Human ambiguity diagnostics contain the original query, ordered full candidate
names, their IDs where needed to distinguish names, and guidance to narrow the
query or use `--exact`. Quote or escape names as data; do not emit terminal
control sequences or invent shell commands from unescaped names.

The executable prints human errors to stderr and leaves stdout empty. A
successful `path` prints only the selected root and a newline, with no
resolution notice. The CLI library continues returning errors for the process
boundary to render; do not print a duplicate diagnostic inside the command.

For supported JSON commands, use the existing single error envelope on stdout,
with no human stderr diagnostic. Extend its `error` object with an optional
`selection` member for lookup no-match and ambiguity failures:

```json
{
  "success": false,
  "error": {
    "code": "conflict",
    "message": "workspace selector \"feat/foo\" is ambiguous; narrow the query or use --exact",
    "selection": {
      "query": "feat/foo",
      "mode": "substring",
      "candidates": [
        {"id": "foo-id", "name": "feat/foo"},
        {"id": "foo-tests-id", "name": "feat/foo-tests"}
      ]
    }
  }
}
```

`mode` is `substring` or `exact`; `candidates` is an ordered array, empty
(`[]`, not `null`) on lookup no-match. Non-selection errors omit `selection`.
Preserve existing error codes, exits, rollback/setup members, success shapes,
and schema versions. The new optional error detail is additive. Branch-only
checkout failures after successful exact resolution keep their existing errors.

## 6. Delivery and acceptance

Use existing dependencies and Git adapters. Keep legacy exact helpers exact;
wire the new selector only into the three named commands. Update help, the
embedded how-to, README, relevant tutorials and their executable fixtures when
each public behavior is introduced. In particular, branch-only checkout
examples must explicitly use `--exact`. Do not normalize historical run ledgers.

| ID | Required acceptance evidence |
|---|---|
| AC01 | Prefix, middle, and suffix matches; case-sensitive literal matching; exact-name overlap is ambiguous; partial IDs and branch/path aliases do not match. |
| AC02 | `--exact` selects full names or IDs and preserves cross-identity conflicts; `--exact=false`, empty selectors, omitted `status`, and required arguments obey section 2. |
| AC03 | Removed states are excluded for `path`/`status` in both modes and included for checkout; partial/damaged/stale-registration and observation-error fixtures prove no false removal. |
| AC04 | Path remains scalar; human and JSON no-match/ambiguity outputs, sorted candidates, exit codes, and noninteractive streams obey section 5. |
| AC05 | Unique shorthand restores retained mounts under the canonical name; dry run agrees; exact branch-only checkout works while default unmatched branch input fails. |
| AC06 | Ambiguity/no-match has no mutation; selected-state replacement/removal and branch-only absence races cannot retarget execution; existing preflight/rollback checks still pass. |
| AC07 | Destructive and all other excluded commands retain exact semantics and reject the new flag; changing the shared lookup helper cannot silently widen their targets. |
| AC08 | Help, examples, executable tutorials, focused/full tests, race checks, release checks, and Ubuntu/macOS/Windows acceptance agree with the delivered contract. |

The linked plan is required for the full scope. This specification becomes
`implemented` only after that plan is fully implemented, independently reviewed,
and verified.
