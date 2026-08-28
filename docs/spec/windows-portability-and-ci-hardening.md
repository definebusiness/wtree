# Windows portability and CI hardening specification

Status: planned
Source idea: none (created directly)
Implementation plan: [Windows portability and CI hardening implementation plan](../plans/windows-portability-and-ci-hardening.md)
Related specification: [Automatic nested mount ignore protection specification](automatic-nested-mount-ignore-protection.md)
Source branch: `codex/automatic-nested-mount-ignore-protection-ci`, follow-up commits after `097a235` through `89ce325`

## 1. Purpose and scope

The automatic nested mount ignore behavior is already implemented on `main`.
The source branch continued after its feature commit with Windows filesystem
corrections, native-path test corrections, and CI reliability work that did
not land with the feature. This specification defines the behavior that must
be ported onto the current `main` architecture.

The source commits are evidence and implementation candidates, not patches to
apply mechanically. `main` has since changed clone execution, logical-root and
repository-forest behavior, tests, tutorials, and documentation. Every ported
change must therefore be justified by a reproducible current-main failure or
an explicit missing portability contract, adapted to current ownership
boundaries, and reviewed for regressions against newer behavior.

## 2. Baseline and provenance

The following source-branch commits define the candidate delta:

| Commits | Candidate behavior |
|---|---|
| `c5f6b51`, `d8dffe1` | Portable formatting checks and LF-preserving Windows checkout |
| `5119606`, `284a6c9`, `d853ae5`, `c86e45b` | Bounded test timeouts, Windows test partitioning, and visible CI failure evidence |
| `4f42a82`, `fda4874` | Windows atomic publication, file-mode, directory identity, clone cleanup, and rollback invariants |
| `92d1cc1`, `5726c4e`, `ed1365c` | Native Windows fixtures, filesystem-identity comparisons, and configured-path preservation |

Before changing production code, implementation must classify each candidate
as one of:

- required and still missing on `main`;
- already satisfied by newer `main` code or tests;
- obsolete because its source code path no longer exists; or
- unrelated and excluded.

The classification and supporting file/test evidence belong in the new plan's
durable run ledger. An obsolete source patch must not be recreated merely to
make the branch and `main` textually similar.

## 3. Windows atomic publication

Atomic file replacement must retain the existing old-or-new publication
contract on Unix and Windows:

- the temporary regular file is written, flushed, and closed before rename;
- replacement never exposes partial target bytes;
- a successful replacement preserves the requested observable file
  invariants supported by the platform;
- stale target generations, type changes, symlink substitutions, and unsafe
  parent changes remain conflicts rather than overwrite opportunities;
- pre-replacement failures preserve the original target;
- post-replacement failures report that publication may already have occurred;
  and
- owned temporary files are removed without deleting an unowned target.

Unix must continue to sync the containing directory after replacement where
the filesystem supports it. Windows must not turn a successful replacement
into a failure solely because `FlushFileBuffers` cannot be used on a directory
handle. The Windows capability boundary must still open, validate, and close
the expected directory, propagate genuine open/stat/close failures, and retain
the injectable pre-boundary failure seam used by atomic failure tests.

Tests must cover existing and newly created files, permission requests,
injected failures at every publication boundary, temporary cleanup, unsupported
sync behavior, and real Windows directory-handle behavior.

## 4. Filesystem identity, modes, and path preservation

Safety decisions must compare filesystem objects rather than depend on
textual path equality when Windows can represent the same object with drive
letter casing, separators, or other native normalization differences.

The implementation must preserve these contracts wherever they remain
applicable in current `main`:

- capture or prime stable `os.FileInfo` identity before an owned path is
  renamed or quarantined when Windows resolves identity lazily;
- prove that clone staging and cleanup targets retain the expected parent
  directory identity, are absolute clean paths, are not symlinks, and remain
  within owned boundaries;
- reconcile only metadata changes that are demonstrably caused by the Windows
  rename operation, while continuing to reject concurrent mutations;
- validate requested file protection through portable observable properties
  when Windows does not report Unix permission bits exactly;
- preserve user-configured paths as configured unless an existing public
  contract explicitly requires canonicalization; and
- use native temporary Windows paths in platform tests instead of Unix-hosted
  pseudo-paths such as literal `C:/...` fixtures.

Path assertions for emitted existing files should use cleaned native paths or
filesystem identity as appropriate. String equality remains required for
public values whose contract is specifically to preserve configured spelling.

These rules must be adapted to the current clone, logical-root, forest,
workspace, rollback, and recovery implementations. They must not restore an
older clone algorithm or remove newer safety checks.

## 5. Portable and diagnostic CI

The GitHub Actions matrix remains Ubuntu, macOS, and Windows. It must provide
equivalent quality evidence on all three platforms without hiding or silently
skipping test failures.

The workflow and any supporting script must satisfy all of the following:

- disable Windows checkout line-ending conversion before repository checkout
  so tracked Go sources are checked exactly as committed;
- format-check every tracked `*.go` file with NUL-safe filename handling and
  propagate `gofmt`, pipeline, and enumeration failures;
- apply explicit, bounded timeouts to normal and race suites;
- partition the slow Windows service suite into deterministic, disjoint
  shards while running every discovered top-level test, example, and fuzz
  target exactly once per mode;
- run all non-service packages once per normal/race mode;
- preserve the exit status of both the test command and log transport;
- continue running remaining Windows shards after an individual shard fails,
  then return failure for the job;
- emit useful GitHub annotations for test failures, panics, timeouts, data
  races, compilation errors, inventory failures, and transport failures;
- fail closed on empty, duplicate, or incompletely assigned test inventory;
  and
- retain build, release-layout, release-reuse, manifest, vet, and formatting
  gates.

The partition helper must be testable outside GitHub Actions. Its inventory,
assignment, exit-code, escaping, and annotation behavior require focused
shell-level tests or an equally deterministic repository-native harness.

## 6. Compatibility and safety boundaries

This work must preserve:

- automatic nested mount ignore behavior and output;
- current clone live-branch semantics and versioned public output;
- current logical-root, repository-forest, workspace, registry, recovery, and
  transaction contracts;
- existing configuration and persisted schema versions;
- secret redaction and non-interactive Git behavior; and
- unrelated user and worktree changes.

The port does not authorize:

- merging or replaying the source branch wholesale;
- reverting newer `main` behavior to match the older branch tree;
- modifying the repository root `.gitignore`, including removing its
  `.DS_Store` entry;
- changing public schemas, adding dependencies, or broad test rewrites solely
  to reduce runtime;
- weakening identity, rollback, durability, or cleanup checks to make Windows
  tests pass; or
- committing, pushing, opening a pull request, publishing, or releasing.

## 7. Required verification

Verification must include:

- focused atomic, directory-sync, identity, mode, clone safety, rollback,
  configuration-path, and automatic-ignore tests;
- deterministic tests of the CI partition and failure-reporting helper;
- full normal and race suites, vet, formatting, build, release, tutorial, and
  diff checks on the implementation platform;
- Windows compilation for platform-specific files before remote CI; and
- one matching GitHub Actions run in which Ubuntu, macOS, and Windows all pass
  the normal, race, build, release-layout, and repository quality gates.

If a matching remote run cannot be started without separately authorized
commit/push/PR activity, the implementation may be locally complete but the
plan and this specification must remain non-implemented until that evidence is
available or the user explicitly changes the verification scope.
