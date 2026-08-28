# Implementation context — Windows portability and CI hardening

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Windows portability and CI hardening implementation plan](windows-portability-and-ci-hardening.md)
Source specification: [Windows portability and CI hardening specification](../spec/windows-portability-and-ci-hardening.md)
Captured: 2026-08-27
Current-main investigation head: `87ec560`
Source-branch completion head: `89ce325`

## 1. Purpose and authority

This document preserves the evidence behind the selective-port plan. It is a
handoff for the implementer and reviewer: branch ancestry, source failure
history, current-main gaps, affected ownership boundaries, and approaches that
must not be repeated.

The source specification remains the behavior contract and the parent plan
remains the execution contract. If this context conflicts with either, the
specification and plan win. The source branch and its completed run ledger are
historical evidence; neither is the source of truth for current `main`.

## 2. Question investigated and conclusion

The investigation asked whether `main` already implements what
`codex/automatic-nested-mount-ignore-protection-ci` intended to deliver.

The answer has two parts:

1. The product feature is already present. Commit `8ad7f3c` on `main`
   automatically updates and verifies immediate-parent `.gitignore` files for
   nested mounts during init and create. The corresponding specification and
   implementation plan are marked `implemented`, and focused current-main
   automatic-ignore tests pass.
2. The complete source-branch portability result is not present. The branch
   continued after its feature commit with CI, Windows atomic-publication,
   filesystem-identity, file-mode, native-fixture, and configured-path fixes.
   Current `main` retains the pre-remediation workflow and several of the
   production/test patterns that failed in hosted Windows CI.

Consequently the branch is neither a feature branch to merge nor wholly
redundant. Its remaining value is a set of behavior-level corrections that
must be revalidated and adapted to current `main`.

## 3. Branch topology and chronology

The common ancestor is:

```text
d16bc80 feat: enforce base repository validation in portable manifest v2 and expand test coverage
```

The relevant history then diverges:

```text
main
  8ad7f3c feat: automate nested mount ignores and add tutorial e2e
  ... later clone, logical-root, forest, and multi-repository work ...
  87ec560 feat: complete multi-repository workspace workflows

codex/automatic-nested-mount-ignore-protection-ci
  097a235 feat: protect nested mounts automatically
  c5f6b51 ci: make formatting check portable
  d8dffe1 ci: preserve LF checkout on Windows
  5119606 ci: allow slow cross-platform test suites
  284a6c9 ci: bound slow Windows test packages
  d853ae5 ci: partition slow Windows service tests
  c86e45b ci: expose Windows partition failures
  4f42a82 fix: preserve Windows atomic publication
  fda4874 fix: preserve Windows file invariants
  92d1cc1 test: use native Windows path fixtures
  5726c4e test: compare Windows paths by identity
  ed1365c test: preserve configured Windows paths
  89ce325 docs: complete automatic ignore protection plan
```

Commit `8ad7f3c` is not a merge of the source branch. It is a later aggregate
feature commit containing the automatic-ignore implementation plus other
tutorial and documentation work, but it does not contain the complete
post-`097a235` Windows/CI remediation series. `git cherry` therefore does not
identify patch-equivalent commits, and a raw `main...branch` diff exaggerates
the desired port by mixing the portability delta with all subsequent `main`
development.

## 4. Evidence that the feature itself is already on main

Current `main` contains these feature boundaries:

- `internal/service/init.go` builds complete direct-child ignore requirements,
  applies the reusable ignore plan before publication, and verifies every
  effective in-checkout `.gitignore` source.
- `internal/service/create.go` inserts parent-first ignore-owner inspection and
  `ensure_ignore:<parent>` transaction steps, applies planned rules, verifies
  them through Git, and records rollback evidence.
- `internal/service/ignore_plan.go` owns deterministic planning, snapshots,
  compare-and-replace application, conflicts, and partial-progress reporting.
- `internal/git/ignore_working_tree.go` preserves Git's winning-rule evidence
  instead of reducing `git check-ignore` to an unqualified boolean.
- `internal/cli/automatic_ignore_e2e_test.go` exercises public init/create,
  dry-run, error, rollback, retry, and no-gitlink behavior.

The investigation ran this focused current-main command:

```sh
go test ./internal/git ./internal/fsutil ./internal/service ./internal/cli \
  -run 'Ignore|Gitignore|Atomic' -count=1 -v
```

It passed locally, including real-Git ignore qualification, atomic failure
matrices, forest init/create rollback, public automatic-ignore output,
retry/no-duplicate behavior, and no-manual-command coverage. This evidence is
not Windows runtime evidence and must not be used as a substitute for M03.

The implemented feature documents are:

- `docs/spec/automatic-nested-mount-ignore-protection.md`;
- `docs/plans/automatic-nested-mount-ignore-protection.md`; and
- the immutable historical run ledger at
  `docs/ai/runs/automatic-nested-mount-ignore-protection.md`.

The new run must not edit that completed ledger.

## 5. Historical hosted-CI failure sequence

The source branch's completed ledger records a useful causal sequence. Stable
labels C1–C11 below are historical labels, not findings in the new run.

| Label | Hosted evidence and correction |
|---|---|
| C1 | The formatting pipeline was not portable or failure-safe. It was replaced with a `pipefail`-protected, NUL-delimited tracked-file pipeline that reports all unformatted files and propagates producer/formatter failures. |
| C2 | Hosted Windows had `core.autocrlf=true`, converting all 149 tracked Go files to CRLF before formatting. A Windows-only pre-checkout configuration disabled conversion. |
| C3/C4 | Windows exceeded Go's inherited 10-minute test-binary timeout and later exceeded a 30-minute suite timeout. Finite larger bounds were introduced from observed runtime rather than skipping tests. |
| C5 | The slow Windows service package was divided into eight deterministic sequential shards; all non-service packages still ran once in each normal/race mode. |
| C6 | Initial partition failures were difficult to diagnose. The helper accumulated failures and emitted bounded GitHub annotations for assertions, panics, timeouts, races, compilation, and inventory failures. |
| C7 | Windows atomic publication attempted an unsupported directory flush after successful rename. The correction retained Unix directory sync and used Windows open/stat/type/close validation, while preserving genuine failures. The same hosted pass exposed clone staging containment and parent-identity assumptions. |
| C8 | Windows file identity, rename metadata, permission reporting, and rollback behavior differed from Unix assumptions. The corrections added identity priming, narrow rename reconciliation, platform-private staging predicates, observable requested-permission checks, and affected rollback coverage. |
| C9 | Several tests used non-native path fixtures or injected failures at the wrong Windows boundary. Tests were adjusted without weakening production contracts. |
| C10 | Lexical path equality missed Windows aliases and emitted existing path spelling differed from expected spelling. Target selection and output assertions used filesystem identity while retaining exact output structure. |
| C11 | Tests conflated verbatim configured values with derived filesystem children. The correction preserved configured spelling and used native joining only for derived paths. |

The final historical run was GitHub Actions run `32289428176` at exact source
head `ed1365c`. Its jobs passed:

| Platform | Job |
|---|---:|
| Ubuntu | `96186554807` |
| macOS | `96186554625` |
| Windows | `96186554841` |

That run proves the corrections worked together on the old branch tree. It
does not prove they still apply cleanly or completely to current `main`, whose
test inventory and production architecture have changed.

## 6. Candidate commit map

### `c5f6b51` — portable formatting check

Changed `.github/workflows/ci.yml` to enumerate tracked Go files with
`git ls-files -z`, run `gofmt` per file, preserve filenames containing spaces,
and propagate pipeline errors.

Current-main observation: the workflow still uses `go list` followed by a
shell glob per package directory. The old pattern is present unchanged and the
candidate behavior is still missing.

### `d8dffe1` — LF checkout on Windows

Configured `core.autocrlf=false` on Windows before `actions/checkout`.

Current-main observation: no pre-checkout Windows configuration exists. The
historical failure affected every Go source, so this remains a direct CI gap.

### `5119606` and `284a6c9` — explicit time bounds

Introduced finite normal/race timeouts and adjusted them after observed hosted
Windows runtime exceeded the first bound.

Current-main observation: the workflow invokes `go test ./...` and
`go test -race ./...` without explicit timeouts. The current suite is larger
than the historical suite, so the old numeric values are evidence, not
automatically correct current values.

### `d853ae5` and `c86e45b` — Windows partition runner

Added `scripts/ci-test.sh`. It enumerates non-service packages, inventories
top-level `Test`, `Example`, and `Fuzz` names in `internal/service`, assigns
them round-robin to eight anchored shards, runs normal/race modes, accumulates
failures, and emits escaped annotations with bounded causal excerpts.

Current-main observation: the script is absent and Windows runs the same
monolithic commands as Unix. The service suite has since gained forest and
multi-repository tests, so shard balance, discovery rules, and timeout values
must be measured again. The old script also lacks a focused self-test file;
the new plan requires deterministic helper tests before trusting it as a
coverage gate.

### `4f42a82` — Windows atomic publication and staging ownership

Introduced platform-specific `syncDirectory` behavior:

- Unix opens and syncs the directory;
- Windows opens, stats, validates directory type, and closes it without the
  unsupported `FlushFileBuffers` operation; and
- common tests inject open/stat/type/close failures.

It also added platform-specific private staging predicates and parent
filesystem-identity checks to the then-current clone executor.

Current-main observation:

- `internal/fsutil/atomic.go` still opens the containing directory and calls
  `Sync` after replacement;
- `internal/fsutil/sync_windows.go` only classifies
  `ERROR_INVALID_FUNCTION`/unsupported errors and has no separate directory
  capability boundary;
- `sync_directory_open.go` and its tests are absent; and
- clone staging still checks exact Unix permission bits and textual parent
  path equality.

The fsutil gap is close to the historical patch. The clone patch is not:
current clone execution now uses live selected branches, actual checked-out
commits, logical roots, and forest-aware path assembly. It requires a fresh
adaptation and review.

### `fda4874` — Windows file invariants

Added or refined:

- stable file-identity priming before a path-owning rename;
- platform-specific private staging modes;
- narrow Windows reconciliation of root metadata changed by rename;
- observable writable/read-only matching instead of exact synthesized Unix
  owner/group bit equality;
- parent-identity validation before cleanup; and
- Windows clone/create/rollback tests.

Current-main observation: `clone_safety.go` still requires exact mode equality
and exact pre/post-rename root metadata, inventory capture does not explicitly
prime identity, and create rollback does not prime the owned worktree identity
before quarantine. These are candidate gaps, but newer forest rollback paths
also use identity checks and must be audited so the port is neither partial nor
duplicative.

### `92d1cc1`, `5726c4e`, and `ed1365c` — test semantics

These commits corrected tests rather than broadly changing product behavior:

- use native absolute temp paths for filesystem tests;
- identify a target object with `os.SameFile` instead of cleaned string
  equality when aliases are possible;
- compare existing emitted paths by identity while preserving exact line
  shape, ordering, rules, and messages;
- preserve supplied `DataDir`/plan path spelling; and
- use `filepath.Join` for OS-derived children such as worktree roots and
  `.gitignore` filenames.

Current-main observation: `internal/config/paths_test.go` still contains
literal `C:/...` and `D:/...` fixtures, and several current tests use cleaned
path strings to identify injected targets. Some such comparisons are correct
for pure lexical contracts; M01 must distinguish those from safety assertions
about real objects rather than replace them indiscriminately.

## 7. Current-main implementation map

| Boundary | Current owner | Porting concern |
|---|---|---|
| Atomic compare/replace | `internal/fsutil/atomic.go` and platform sync files | Separate regular-file flushing from directory durability capability without hiding post-rename failures |
| Clone staging/publication | `internal/service/clone_execute.go` | Parent identity, private mode, cleanup target, current live-branch and forest semantics |
| Clone snapshots/inventory | `internal/service/clone_safety.go` | Windows identity priming, mode observability, rename-generated metadata, concurrent mutation rejection |
| Create rollback | `internal/service/create.go` plus forest-specific files | Prime and compare owned identities without weakening quarantine or unrelated-dirt preservation |
| Configured and derived paths | `internal/config/paths.go` and consumers | Preserve supplied spelling; derive children with native separators; avoid pseudo-Windows filesystem fixtures |
| Public path output tests | `internal/cli` tests | Keep exact output grammar but compare existing aliases by identity where appropriate |
| Matrix orchestration | `.github/workflows/ci.yml` | Pre-checkout bytes, portable formatting, finite normal/race execution, unchanged build/release gates |
| Windows partitioning | candidate `scripts/ci-test.sh` | Exact-once inventory, failure propagation, annotation quality, testability, current-suite balance |

Important newer-main surfaces absent from the source branch include logical
project roots, multi-root repository forests, live-branch clone output v2,
aggregate operations, and expanded tutorial coverage. Review must explicitly
protect these surfaces even when a historical patch did not know about them.

## 8. Root `.gitignore` finding

The repository root `.gitignore` differs between current `main` and the source
branch:

```diff
 /bin/
 /dist/
 *.swp
 /.idea/
+.DS_Store
```

The branch does not modify `.gitignore` as part of automatic nested mount
protection; it merely predates or omits the `.DS_Store` entry present on
`main`. Product behavior writes the relevant immediate-parent `.gitignore`
files in user projects at runtime. Removing `.DS_Store` from this repository's
root file is therefore unrelated and explicitly excluded from the port.

## 9. Why a wholesale merge or cherry-pick is unsafe

A `main...branch` comparison contains dozens of files and thousands of lines
that are not the desired delta. It combines:

- two different deliveries of the automatic-ignore feature;
- later portability corrections on the source branch;
- later live-clone, logical-root, forest, and multi-repository changes on
  `main`;
- completed historical ledgers that must remain immutable; and
- unrelated tutorial, workflow-idea, and documentation differences.

In particular, replaying `4f42a82` or `fda4874` directly would apply patches
against an older clone executor and older tests. Even a clean textual
cherry-pick would not prove that current cleanup, recovery, and forest
boundaries are covered. The required unit of porting is a current behavior
plus a current-main regression test, not a historical commit.

## 10. Rejected shortcuts

- **Merge the branch and resolve conflicts.** This makes old and new feature
  deliveries compete and risks reverting newer behavior.
- **Cherry-pick all follow-ups.** Several commits mix fsutil, clone, tests, CI,
  and the immutable historical ledger. Their context is obsolete in places.
- **Copy only the Windows sync function.** Hosted failures also exposed parent
  identity, permission, rename metadata, path fixtures, and CI diagnostics.
- **Copy the partition script without tests.** A runner that silently omits a
  test is worse than a slow runner; exact-once behavior needs its own evidence.
- **Treat cross-compilation as Windows verification.** It proves build tags and
  symbols, not runtime directory handles, file IDs, rename timestamps, or path
  aliases.
- **Accept every Windows sync error.** That would hide real open, stat, close,
  permission, handle, and I/O failures after publication.
- **Relax all file mode and path comparisons.** Only platform-unobservable
  Unix bits and alias spelling may be treated specially; writability,
  identity, ownership, and exact configured-value contracts remain enforced.
- **Use the historical green run as final evidence.** It verifies the old tree,
  not the exact reviewed current-main result.
- **Remove `.DS_Store` from `.gitignore`.** It is unrelated to runtime nested
  mount protection.

## 11. Suggested first-pass investigation commands

The implementer should refresh evidence rather than assume this capture is
still current:

```sh
git status --short --branch
git merge-base main codex/automatic-nested-mount-ignore-protection-ci
git log --oneline --reverse d16bc80..codex/automatic-nested-mount-ignore-protection-ci
git show --stat 4f42a82 fda4874 92d1cc1 5726c4e ed1365c
git diff 097a235..89ce325 -- .github/workflows/ci.yml scripts/ci-test.sh internal/fsutil internal/service internal/config internal/cli

rg -n "Sync\(|syncDirectory|WriteFileAtomic|Atomic" internal/fsutil
rg -n "SameFile|filepath\.Clean|Mode\(\)\.Perm|MkdirTemp|removeOwnedTree|translateCloneRootAfterRename" internal/service internal/config internal/cli
rg -n "C:/|D:/|assertSameExistingPath|WorktreeRoot" internal --glob '*_test.go'
sed -n '1,120p' .github/workflows/ci.yml
```

For each candidate commit, the gap matrix should record:

```text
commit / behavior / historical failure / current owner / current test /
current-main reproduction / decision / planned milestone / regression risk
```

## 12. Verification handoff notes

- The local focused automatic-ignore and atomic suite passed during this
  investigation, but no current-main Windows runtime was executed.
- Historical source-branch run `32289428176` supplies a known-good behavioral
  reference and useful runtime scale, not acceptance for the new plan.
- Current-main full service and CLI suites are materially larger than the old
  branch suites. Re-measure shard balance and timeout headroom.
- A Windows compile-only check should write test binaries to an isolated
  temporary directory so the repository is not left with `*.test.exe`
  artifacts.
- The final hosted run must target the exact reviewed implementation tree.
  Starting it may require separately authorized commit/push/PR activity; the
  durable ledger must record that authorization boundary rather than assuming
  it.
- Do not create the new durable ledger until the user authorizes execution.
  Once created, only the authorized run may edit it.
