# Implementation context — Windows portability simplification and CI remediation

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Windows portability simplification and CI remediation implementation plan](windows-portability-simplification-and-ci-remediation.md)
Source specification: [Windows portability and CI hardening specification](../spec/windows-portability-and-ci-hardening.md)
Related prior plan: [Windows portability and CI hardening implementation plan](windows-portability-and-ci-hardening.md)
Related prior run ledger: [Windows portability and CI hardening run ledger](../ai/runs/windows-portability-and-ci-hardening.md)
Captured: 2026-08-28
Investigated tree: `826757565e72f7ec3620e0b500818395d0f0f480`
Hosted RED run: [GitHub Actions run 33168555356](https://github.com/definebusiness/wtree/actions/runs/33168555356)

## 1. Purpose and authority

This document preserves the evidence and reasoning needed to simplify the
Windows portability work without discarding its valid safety properties. It
records the exact hosted failures, separates causal failures from likely
cascades, identifies overengineered mechanisms, and defines the smaller design
the implementation plan should pursue.

The source specification remains the behavior contract. The parent plan is the
execution contract if the user later authorizes it. The prior plan and its run
ledger are historical evidence and must not be edited during this plan's run.
Where the earlier plan prescribed a mechanism more narrowly than the amended
specification, the amended specification and the new plan control this work.

No implementation, commit, push, workflow rerun, publication, dependency
installation, or real-user-data change was authorized while preparing this
context.

## 2. Executive conclusion

The Windows portability effort was necessary, but parts of its implementation
were overengineered or encoded the wrong portability boundary.

Keep the behavior that protects atomic publication, private staging,
replacement detection, native paths, portable permission assertions, exact
test coverage, and bounded CI. Simplify or remove mechanisms that:

- pass Git exclude rules through the POSIX-only `/dev/stdin` pathname;
- pre-create the exact directory that Git itself must create and retain a
  Windows handle to that directory across `git init`;
- rely on a path-only `os.SameFile` receipt after the original directory was
  unlinked and its inode may have been reused;
- inventory a Git object database while automatic maintenance may still
  create and remove transient lock files; or
- require a bespoke Windows sharding and annotation layer before measured
  native runtime proves that the standard monolithic test command is
  insufficient.

The target is less platform-specific code with stronger, narrower security
boundaries: a private same-volume container around an initially absent Git
destination, a retained handle only where an unlink-and-replacement window
actually exists, a securely owned temporary Git exclude file, quiescent Git
maintenance during staging, and the simplest CI runner that meets the measured
timeout and coverage requirements.

## 3. Exact hosted baseline

Run `33168555356` tested commit
`826757565e72f7ec3620e0b500818395d0f0f480`. Formatting and vet passed on all
three operating systems. The test step failed on each operating system, so
later build and release gates were skipped.

| Platform | Job | Environment | Result |
|---|---|---|---|
| Ubuntu | [job 98839761041](https://github.com/definebusiness/wtree/actions/runs/33168555356/job/98839761041) | Ubuntu 24.04.4, Git 2.55.0, Go 1.26.7 `linux/amd64` | Test failure |
| macOS | [job 98839761097](https://github.com/definebusiness/wtree/actions/runs/33168555356/job/98839761097) | macOS 26.5.2 `arm64`, Git 2.55.0, Go 1.26.7 `darwin/arm64` | Test failure |
| Windows | [job 98839760961](https://github.com/definebusiness/wtree/actions/runs/33168555356/job/98839760961) | GitHub-hosted Windows, Git for Windows, Go 1.26.7 | Test failure |

The investigated local machine used Apple Git 2.50.1 and Go 1.26.5 on
`darwin/arm64`. The Ubuntu failures therefore disprove the assumption that a
passing Unix-like local host establishes portable Git behavior.

## 4. Ubuntu failure analysis

### 4.1 Committed-ignore evaluation is not portable

These tests failed in `internal/git`:

- `TestAdapterChecksCommittedGitignoreAtRequestedRef`;
- `TestAdapterChecksNestedCommittedGitignoreWithWinningNegation`; and
- `TestAdapterCommittedIgnoreDoesNotUseTemporaryStorage`.

Downstream doctor failures were:

- `TestDoctorAcceptsNestedCommittedImmediateParentIgnore`; and
- `TestDoctorRequiresCommittedImmediateParentIgnore`.

`internal/git/ignore_committed.go` configures or supplies an exclude file as
`/dev/stdin`. Git 2.55 on Ubuntu returned false negatives. Git for Windows
reported that `/dev/stdin` cannot be used as an exclude file. The local Apple
Git version accepted the construction, which made the defect environment-
sensitive rather than Unix-versus-Windows in the simple sense.

The test that forbids temporary storage constrains an implementation detail
rather than a security outcome. Replace it with tests that require an exact,
private, securely cleaned temporary exclude file and prove that global, local,
and user exclude sources cannot leak into the committed-tree result.

### 4.2 Path-only directory receipts can accept a replacement

These `internal/service` tests failed on Linux:

- `TestWorkspaceCreatorForestReplacementBeforeAddWorktreeWritesRecovery`; and
- `TestWorkspaceGroupingRefusesDifferentRealDirectoryReceipt`.

The current grouping receipt retains `os.FileInfo` and later calls
`os.SameFile`. On Unix, that comparison is based on device and inode. After a
directory is removed, Linux may reuse the inode for a replacement, allowing a
path-only stale receipt to accept a different object.

The narrow remedy is justified hardening, not a broad retained-handle design:
hold an open descriptor or handle only across a substitution-sensitive window,
compare its current identity with the path at the validation boundary, and
close it deterministically. Tests need a deterministic identity seam or an
equivalent construction that cannot depend on opportunistic inode reuse.

## 5. macOS failure analysis

`TestUpdatePublicationCompletesRealAddedAndRetainedTransactions/added` failed
while capturing the fetched update staging inventory. An `lstat` of
`.git/objects/maintenance.lock` raced with that file's removal, after which
cleanup reported that private staging ownership changed.

Git 2.55 may start automatic or background maintenance during repository
operations. A whole-tree ownership snapshot must not require a transient Git
lock to remain present. The preferred first remedy is to run managed staging
Git commands with automatic maintenance and garbage collection disabled, then
capture inventory only after the command has completed and the tree is
quiescent. A broad class of ignored volatile paths is a fallback only if a
focused test proves that disabling maintenance is insufficient.

## 6. Windows failure groups

### 6.1 Git cannot create the pre-created staging root

Clone and staging tests, including
`TestRunCloneLocalAndHTTPThroughProcessBoundary`, failed because the hardened
staging code creates the exact destination first. Git for Windows then rejects
`git init -- <staging>` with `fatal: cannot mkdir ...: File exists`.

The smaller design is:

```text
destination parent
└── private same-volume container       created and protected by wtree
    └── Git destination child           initially absent; created by Git
```

Retain authority over the private container. Let Git create the child. After
Git returns, validate the child's type, identity, non-reparse status, parent
relationship, and inherited privacy before using or publishing it. Do not hold
a child handle across a Git operation when Windows sharing semantics can block
the operation.

### 6.2 Committed-ignore evaluation repeats the Ubuntu defect

The three `internal/git` committed-ignore tests fail with the explicit Git for
Windows `/dev/stdin` error. This is one cross-platform root cause, not a
separate Windows workaround.

### 6.3 Unix-shaped fixtures are rejected as Windows filesystem paths

Configuration and CLI project tests use pseudo-absolute paths that are not
absolute native Windows paths. Representative failures include strict v2
topology loading, absolute-source validation, worktree-root precedence, and
project list/prune/unregister scenarios.

Use `t.TempDir()` and `filepath` for values whose semantics are filesystem
paths. Keep literal path spelling only in tests of public parser or configured-
value preservation contracts.

### 6.4 Unix mode bits are not the Windows protection contract

`TestStoreRoundTripsVersionedStateAtomicallyWithPrivatePermissions` observed
mode `0666`. A test of exact Unix mode bits is not a valid Windows privacy
assertion. Use a platform-owned assertion of the observable protection or
writability contract; do not simply skip the test or accept arbitrary access.

### 6.5 Backup removal and downstream CLI failures require focused diagnosis

`TestExecuteContextPushResolverAuthorityAndReadOnlySnapshots` observed backup
paths that still existed after cleanup. Windows open-handle and removal
semantics are the leading hypothesis, but the implementation must first add a
focused reproducer and audit handle closure rather than weaken cleanup checks.

Status and update tests also failed, including default-identity-drift and
update dry-run JSON cases. These may cascade from native-path, staging, or
cleanup failures. Classify them only after the upstream causes are fixed, add
the smallest focused RED test for any independent defect, and preserve public
output contracts.

## 7. Keep, simplify, and remove

| Area | Decision | Reason |
|---|---|---|
| Atomic file publication and Windows directory-sync capability boundary | Keep | It preserves old-or-new publication while avoiding unsupported directory flush behavior. |
| Native path fixtures and identity-aware comparisons | Keep | Windows spelling and path semantics differ materially from Unix. |
| Observable Windows protection assertions | Keep | Privacy is required even when Unix mode bits are not meaningful. |
| LF checkout and NUL-safe formatting | Keep | These are small, portable, and directly prevent false CI results. |
| Private clone/update staging | Keep, narrow | The threat model is valid, but ownership belongs on a container around an absent Git child. |
| Retained filesystem handles | Narrow | Use only across real substitution windows; avoid a pervasive handle-based object model. |
| `/dev/stdin` committed-ignore input and no-temp purity test | Remove | It is not portable and tests a mechanism instead of isolation and cleanup. |
| Pre-created final Git staging root with retained Windows handle | Remove | It conflicts with Git for Windows and expands native code without improving the required boundary. |
| Whole-tree snapshot sensitivity to transient Git maintenance locks | Remove | Managed Git operations should be quiescent before stable inventory is captured. |
| Mandatory bespoke Windows sharding and failure taxonomy | Re-evaluate | Keep only the minimum proven necessary by native runtime and exact coverage measurements. |

## 8. Target architecture

### Portable committed-ignore evaluation

```text
committed .gitignore blobs
        ↓ exact composition
private temporary exclude file
        ↓ explicit Git arguments
Git ignore evaluation
        ↓ close and remove on every exit
boolean and winning-rule evidence
```

The file must be created with exclusive ownership, exact bytes, and
deterministic cleanup. Git must not consult repository, global, or user exclude
configuration for this operation.

### Substitution-resistant identity receipt

```text
open directory descriptor/handle
        ↓ operation with substitution risk
fstat retained object + stat/open current path
        ↓ compare object identity and type
close retained authority
```

This mechanism is local to boundaries that validate an object after its name
could have been replaced.

### Stable Git staging inventory

```text
managed Git command
  + maintenance.auto=false
  + gc.auto=0
        ↓ command completion
quiescent staging tree
        ↓ stable inventory and ownership validation
publish or cleanup
```

## 9. Approaches not to repeat

- Do not make a historical branch or its syscall structure the target design.
- Do not use POSIX device paths as portable filenames passed to Git.
- Do not require zero temporary storage when a private owned temporary file is
  the simpler isolation primitive.
- Do not pre-create a pathname that a third-party tool contractually creates.
- Do not treat `os.SameFile` on two path-derived snapshots as proof across an
  unlink-and-recreate window.
- Do not solve transient Git internals with an ever-growing list of ignored
  lock paths before disabling the source of background mutation.
- Do not skip Windows assertions for paths, privacy, rollback, or cleanup.
- Do not keep a complex sharding/annotation framework merely because it
  exists; preserve it only if measured native execution needs it.

## 10. Verification baseline and completion evidence

The failed run above is the immutable hosted RED baseline. A completed
remediation requires a later run for the exact reviewed tree in which Ubuntu,
macOS, and Windows all reach and pass tests, race tests, vet, formatting,
build, and release gates. A run that skips later stages after a test failure is
not sufficient.

Focused verification must include the named failures in this document, native
Windows clone execution, Linux replacement-receipt protection, Git 2.55
committed-ignore behavior, macOS update staging with maintenance disabled,
backup cleanup, and the public CLI status/update cases.

## 11. Scope boundaries

Preserve automatic nested mount ignores, live-branch clone semantics, logical
roots, repository forests, current schemas, transaction and recovery safety,
secret redaction, and non-interactive Git behavior. Do not change the root
`.gitignore`, add dependencies without separate authorization, weaken cleanup
or rollback checks, or rewrite broad test suites simply to obtain a green run.

Execution must preserve unrelated worktree changes. Commits, pushes, pull
requests, workflow reruns, releases, deployments, installations, and changes
to real user data remain separately authorized actions.
