# `wtree` incremental implementation plan

Status: ready to execute  
Source of truth: [`docs/spec/wtree.spec.md`](../spec/wtree.spec.md)  
Delivery style: test-first, one reviewed milestone at a time

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is checked or a genuine external blocker is reached. Do not ask for routine design decisions; this plan fixes those decisions below.

For each unchecked milestone, in order:

1. Read this plan, the relevant specification sections, the durable run ledger
   at `docs/ai/runs/wtree-implementation-plan.md`, and the current repository
   state. Create the ledger before the first dispatch if it does not exist;
   reconcile and append a checkpoint before resuming an interrupted run.
2. Create or update a concise working plan for only that milestone.
3. Delegate initial implementation to an agent named `implementer`. For a
   remediation packet, select `implementer` while the milestone ledger's
   `Remediation attempts` is `0` or `1`; select `escalation-implementer` only
   when it is `2`. Its brief must require strict test-first development:
   - add the smallest relevant failing test;
   - run it and capture the expected failure (RED);
   - add the minimum production code to pass (GREEN);
   - refactor while keeping all tests green;
   - run the milestone verification commands and report files changed, RED/GREEN evidence, and unresolved concerns.
4. Inspect the implementer's work and verification evidence. Do not accept production code that lacks a test that failed for the intended reason first. Existing tests that already cover the behavior are acceptable only for pure refactors.
5. Delegate review to a separate agent named `reviewer`. The reviewer must not implement the feature. It must inspect the diff and specification, run focused tests plus the full available suite, and report findings by severity with file/line references. It must check behavior, safety, portability, test quality, and scope.
6. If review finds anything above a stylistic suggestion, send the complete
   unresolved finding set back in one test-first remediation packet, then have
   `reviewer` re-review. The normal reviewer remains authoritative for every
   implementation tier. Use `escalation-reviewer` only for an explicitly
   bounded qualifying adjudication under the milestone supervision process;
   never as an automatic second pass. Repeat only within the three rejected
   complete-remediation limit.
7. Run the milestone's verification commands yourself. Update documentation/contracts affected by the milestone. Check the milestone only when its exit criteria and the global definition of done are satisfied.
8. Append the durable ledger checkpoint after every material action and record
   a short entry in the execution log at the end of this file only when a
   milestone is approved: date, milestone, tests run, review result, and commit
   SHA if commits are part of the run.
9. Continue immediately with the next unchecked milestone. A normal test failure, review finding, or implementation mistake is not a reason to stop. Stop only for missing credentials/services, unavailable required tooling that cannot be installed safely, an irreconcilable specification conflict, or a destructive choice not authorized by this plan.

Agents share the same worktree. Run only one editing agent at a time. The reviewer is read-only. Preserve unrelated user changes and never use destructive Git cleanup commands. Commit only if the user separately requests commits.

Suggested invocation:

```text
Run docs/plans/wtree-implementation-plan.md from the first unchecked milestone.
Use an implementer agent for test-first implementation and a separate reviewer
agent for review. Remediate review findings and continue unattended until done.
```

## Fixed implementation decisions

These choices remove ambiguity for unattended execution. A later reviewed milestone may change one only when repository evidence makes it necessary, and must record the reason.

- Language: Go, organized as a normal Go module with `cmd/wtree` as the executable entry point and internal packages beneath `internal/`.
- Supported baseline: the Go version declared in `go.mod`; CI tests Linux, macOS, and Windows on the currently supported stable Go release.
- CLI: Cobra. YAML: `gopkg.in/yaml.v3`. UUIDs: `github.com/google/uuid`. Locking: a maintained cross-platform advisory file-lock package selected during bootstrap and wrapped behind an internal interface.
- Persistence: human-edited project/global configuration in versioned YAML; registry, workspace state, and recovery records in versioned JSON. All durable writes use same-directory temporary files, sync/close, atomic replacement, and restrictive permissions where supported.
- OS locations: `os.UserConfigDir()` for global configuration/registry and `os.UserCacheDir()` only for disposable cache; workspace/state data uses an explicit `WTREE_DATA_HOME` test/advanced override, otherwise `os.UserConfigDir()`'s sibling data convention implemented per OS (XDG data home on Linux, Application Support on macOS, LocalAppData on Windows). `worktrees.root` remains configurable.
- Git is invoked as a subprocess through one adapter. Commands use `git -C <path>` and parsed porcelain output; business rules never execute Git directly.
- Repository identity is the normalized, canonical absolute result of `git rev-parse --path-format=absolute --git-common-dir`. Persist it, but never infer identity from a checkout directory name.
- Workspace storage names use a readable slug plus a truncated SHA-256 digest of the logical name. Logical names remain unchanged in state. The complete name-to-directory mapping is persisted and collision checked.
- Imports reject incomplete workspaces by default; `--allow-partial` records missing repositories explicitly. Ambiguous inferred names require `--name`.
- Unsupported submodules are detected and rejected with an actionable error in the MVP.
- Mutation locks are exclusive per project ID. Registry updates also take a registry lock. Read commands do not take an exclusive project lock but tolerate an atomic state replacement.
- `--force` is modeled as explicit per-command allowances, never as bypass-all-validation.
- Optional `new` and `shell-init` commands are outside the MVP. The complete required command set in specification section 53 is in scope.

## Stable contracts to establish early

### Exit codes

| Code | Meaning |
|---:|---|
| 0 | success |
| 1 | unexpected/internal failure |
| 2 | invalid command or arguments |
| 3 | project not found or ambiguous |
| 4 | workspace not found |
| 5 | configuration or preflight validation failed |
| 6 | Git operation failed |
| 7 | dirty worktree safety refusal |
| 8 | branch/path/state conflict |
| 9 | rollback incomplete; recovery required |

### Output rules

- `path` and `repo path` print exactly one absolute path plus newline to stdout; diagnostics go to stderr.
- JSON mode emits one documented JSON value to stdout for success or failure and no human progress text there.
- Human progress/output is renderer-owned; domain and application packages never print.
- JSON structures are contract-tested with golden files or decoded structural assertions. Additive fields are allowed; existing field meaning is not silently changed.

### Core package boundaries

```text
cmd/wtree                  executable wiring
internal/cli               Cobra commands, help/how-to, exit mapping
internal/domain            Project/Repository/Workspace/Checkout and invariants
internal/git               Git adapter and porcelain parsers
internal/config            project/global config and precedence
internal/store             registry, workspace state, atomic files, migrations
internal/discovery         project/context/nested-repository discovery
internal/pathutil          normalization, containment, mounts, storage names
internal/lock              project and registry locking
internal/plan              immutable operation plans and preflight results
internal/transaction       execute/rollback/recovery machinery
internal/service           use cases; no CLI or formatting concerns
internal/render            human and JSON output
internal/testutil          hermetic Git repositories and filesystem fixtures
```

Dependencies point inward: CLI and render call services; services use interfaces for Git, stores, locks, and filesystem effects; domain has no infrastructure dependencies.

## Global definition of done

Every checked milestone must meet all of these conditions:

- New behavior was developed RED → GREEN → REFACTOR, and tests cover success plus meaningful failure/safety cases.
- Focused tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, and formatting pass. If a platform-specific test cannot run locally, CI coverage and build checks provide the platform gate.
- Errors preserve context and map to the stable exit taxonomy; stdout/stderr and JSON rules are tested where relevant.
- Filesystem/Git tests are hermetic: temporary directories, isolated HOME/XDG/AppData environment, no network, no dependence on the developer's Git configuration.
- Paths containing spaces, Unicode, and platform separators are covered where relevant. Tests do not assume a default Git branch name.
- Mutations preflight before changing state, use the appropriate lock, commit state atomically only after validation, and have rollback/recovery tests.
- Public behavior is reflected in built-in help/how-to and repository docs in the same milestone that introduces it.
- The separate reviewer reports no unresolved correctness, safety, portability, or maintainability findings.

## Milestones

### [x] M00 — Bootstrap the module and quality gates

Scope:

- Create `go.mod`, `cmd/wtree`, the internal package skeleton only where immediately used, and a deterministic version variable.
- Add a minimal root command that supports `--version`, `-h`, and `--help`; unknown commands/invalid arguments return exit code 2.
- Build hermetic test helpers for command execution with captured stdout/stderr and isolated environment variables.
- Add CI for formatting, vet, unit/integration tests, race tests where supported, and build/test on Linux, macOS, and Windows.
- Add `Makefile` targets (or an equally small cross-platform documented command set) for `test`, `test-race`, `vet`, `build`, and `check`.

Test-first slices:

1. CLI version/help and exit behavior.
2. Build metadata and command runner separation (`main` only maps errors to exit codes).
3. CI/build smoke tests.

Exit criteria: `go run ./cmd/wtree --version`, help, and invalid invocation behave predictably; all quality commands pass.

### [x] M01 — Domain model, graph validation, and path safety

Scope:

- Define versioned `Project`, `Repository`, `Workspace`, `Checkout`, detached-head, partial-workspace, and recovery concepts without persistence concerns.
- Validate exactly one root, known parents, unique IDs, acyclic hierarchy, branch/head consistency, and complete/explicitly-partial checkout membership.
- Implement stable topological parent-first and child-first ordering.
- Implement central effective-path resolution from parent-relative mounts.
- Reject absolute/empty (except root `.`), escaping, sibling-colliding, self-overlapping, and unsafe symlink-resolved mounts. Ensure descendants relocate when a parent mount changes.
- Implement deterministic, collision-safe workspace storage names.

Test-first slices: valid nested graph; missing parent/cycle/multiple roots; ordering; renamed parent and child mounts; `..`, absolute, collision and containment attacks; slash/collision names such as `feature/login` versus `feature-login`; spaces and Unicode.

Exit criteria: all hierarchy and path behavior can be exercised without Git or disk stores, and no other package constructs checkout paths independently.

### [x] M02 — Git adapter and hermetic Git fixtures

Scope:

- Define the `Git` interface and subprocess implementation for every operation in specification section 63.
- Set locale-neutral, non-interactive Git environment; retain command, repository, exit status, and bounded stderr in typed errors without leaking unrelated environment values.
- Parse `worktree list --porcelain`, status porcelain, symbolic branch/detached HEAD, refs, common Git dir, top level, and submodule metadata.
- Build test fixtures that create independent nested repositories, branches, commits, dirty states, detached HEADs, and manually created worktrees with controlled user identity.

Test-first slices: parser tables before subprocess code; common-dir equality across worktrees; branch checked out elsewhere; staged/modified/untracked status; detached HEAD; paths with spaces; missing/invalid ref; unsupported submodule detection.

Exit criteria: application code can obtain all actual Git facts solely through the tested adapter.

### [x] M03 — Versioned configuration, OS paths, and atomic stores

Scope:

- Implement `.wtree.yml` v1 and global YAML config schemas, strict decoding, defaults, path expansion, and precedence: CLI > project > global > OS default.
- Implement OS-specific config/data/worktree roots and test-only environment overrides without reading the real user environment.
- Implement versioned JSON registry, workspace state, and recovery record stores with atomic replace and corruption-safe reads.
- Reject unknown newer versions. Add an explicit migration framework and no-op v1 migration tests; never rewrite unknown data.
- Wrap cross-platform file locks; add registry and per-project lock acquisition, timeout, and contention errors.

Test-first slices: precedence matrix; Linux/macOS/Windows path functions; round trips; malformed/unknown fields and versions; interrupted write preserving old file; permissions where supported; concurrent writers/lock contention.

Exit criteria: storage failures cannot produce a half-written authoritative file, and all paths are injectable in tests.

### [x] M04 — Repository discovery and `init`

Specification coverage: sections 12–15, 71, 72, 76, 77, 79, 80, 85.

Scope:

- Discover the root Git repository and independent nested repositories with configurable ignore patterns and safe traversal that does not descend through dependency/build trees unnecessarily.
- Derive parent relationships from repository-root nesting, distinguish and reject submodules, assign stable logical IDs deterministically (`root`, then readable collision-safe IDs), capture source checkout/default mount/default branch/common Git dir.
- Implement `wtree init [path] [--worktree-root] [--dry-run] [--json]` with full preflight, atomic `.wtree.yml` creation, and global registration under locks.
- Treat the original source tree as the `default` workspace (persisted or deterministically projected) and ensure repeated init reports a conflict without overwriting.

Test-first slices: root-only, two nesting levels, sibling repos, ignored directories, `.git` file/worktree cases, ID collisions, submodule rejection, dry run, repeat init, rollback if registration fails, JSON/error contracts.

Exit criteria: a fixture matching the specification's product/backend/shared tree initializes into a valid config and registry with verified Git identities.

### [x] M05 — Project/context and workspace resolution

Specification coverage: sections 11–13, 37, 38, 76–80.

Scope:

- Resolve projects by explicit `-p/--project`, upward `.wtree.yml` search, or current checkout common-Git-dir lookup in the registry, in that precedence order.
- Detect ambiguity and stale/moved config paths; support project relocation by verified project ID and Git identities rather than name.
- Detect current workspace/repository from generated/imported checkout state or the default source workspace.
- Provide the single `Workspace.ResolveRepository(id)` path used by all later commands.

Test-first slices: invocation at root, nested ordinary directory, nested repo, generated checkout, explicit selection, no context, two-project ambiguity, moved source project, stale registry, renamed mounts.

Exit criteria: no command needs to infer repository identity or paths independently.

### [x] M06 — Renderers, errors, inspection foundation, and `config`

Scope:

- Implement typed application errors and the fixed exit-code mapping.
- Implement human and JSON renderers with strict stdout/stderr separation and stable error envelopes.
- Implement `config get/set/unset/list`, global and `--project` scopes, effective-value reporting, atomic writes, locking, JSON output, and unsupported option rejection.
- Establish reusable CLI option validation and golden/structural contract testing.

Test-first slices: every exit category; broken-pipe/renderer errors; config precedence through CLI; `get --json`; concurrent set; invalid keys/values/scopes; no decorative stdout for scalar output.

Exit criteria: future services return data/errors and never format or print them directly.

### [x] M07 — Immutable workspace plans and complete preflight

Specification coverage: sections 16–23, 42, 45, 64, 68–70, 84.

Scope:

- Define serializable/renderable create and checkout plans containing resolved bases, per-repository branches, mounts, paths, ordered steps, and expected inverse operations.
- Resolve `HEAD` independently per source checkout and explicit refs independently per repository.
- Preflight the full plan before mutation: project/config/identity, branch/ref validity, existing branches, checked-out branches, state/worktree conflicts, safe target/mount containment, sibling overlap, tracked parent content conflicts, permissions/parent feasibility, and workspace name collision.
- Support repeated `--mount repo=mount`, `--path`, `--dry-run`, and deterministic human/JSON plan output.

Test-first slices: parent-first order; default and renamed mounts; HEAD versus explicit refs; one missing ref prevents every mutation; checked-out branch; storage-name collision; tracked directory/file mount conflict; escape/symlink attacks; dry-run proves zero Git/store mutations.

Exit criteria: plans are pure values, independently testable, and both create/checkout can be fully validated and rendered without executing.

### [x] M08 — Transaction runner, rollback, and recovery

Specification coverage: sections 21, 65–67, 83.

Scope:

- Implement steps with explicit execute/rollback behavior, reverse rollback ordering, structured progress events, and injected failure points.
- Hold a per-project mutation lock for plan revalidation, execution, result validation, and atomic state commit.
- On incomplete rollback, atomically record recovery metadata and return exit 9 with actionable details. Successful rollback must leave no workspace state or recovery record.
- Make cancellation/interruption stop at a safe boundary and rollback completed reversible steps.

Test-first slices: success; failure at every step boundary; reverse rollback; rollback failure; state-commit failure; validation-after-execute failure; lock contention; cancellation; recovery record contents; no partial logical state.

Exit criteria: a fake effect adapter proves transactional invariants exhaustively before real create/delete operations use the runner.

### [x] M09 — `create`

Specification coverage: sections 16–23, 45–47, 51.

Scope:

- Implement `create <branch>` using the planner and transaction runner: create branches and add worktrees parent-first, validate resulting common Git dirs/branches/paths, then commit workspace state.
- Default `--from` to `HEAD`; implement `--from`, `--mount`, `--path`, `--dry-run`, `--json`, and `--verbose`. Reject unrelated `--force` use.
- Emit concise progress and rollback results without contaminating JSON stdout.

Test-first slices: root-only; three-level nested; renamed parent/child mounts; per-repository HEAD bases; explicit base; branch/path conflicts; failure for each repository with complete rollback; incomplete rollback/recovery; concurrent same/different workspace create; end-to-end JSON.

Exit criteria: the primary `wtree create agent/task-123` workflow is safe and leaves one verified checkout per configured repository.

### [x] M10 — `checkout`, `list`, `path`, and repository lookup

Specification coverage: sections 24, 25, 27, 28, 37, 38, 47, 49.

Scope:

- Implement `checkout <workspace-or-branch>` using existing branches only; never create a missing branch. Reconstitute a removed workspace from retained state/mounts, or create a new checkout-state mapping when unambiguous.
- Implement `list [--json]`, `path <workspace>`, `repo path <id>`, and `repo get <id> [--json]` using central context/workspace resolution.
- Preserve workspace-specific renamed mounts through remove/checkout cycles.

Test-first slices: checkout existing branches; missing branch fails before mutation; branch checked out elsewhere; stored renamed mounts; detached/incompatible state refusal; list multiple/default/partial workspaces; exact one-line paths; invocation inside nested repository; unknown IDs/workspaces; JSON contracts.

Exit criteria: shell composition examples in the specification work byte-for-byte and never expose source-path assumptions.

### [x] M11 — `status`

Specification coverage: sections 26, 44, 46, 47, 75, 78.

Scope:

- Implement aggregate and per-repository status for an explicit workspace or inferred current one.
- Distinguish cleanliness from structural drift: modified, missing, branch mismatch, mount mismatch, detached, unknown repository, stale state, and partial workspace. Include head and optional ahead/behind where an upstream exists.
- Preserve divergent imported branches in the domain/output rather than assuming synchronization.

Test-first slices: clean/modified/staged/untracked; missing checkout; wrong branch; detached HEAD; divergent branches; renamed mount; no upstream; partial state; current-workspace inference; stable JSON.

Exit criteria: status reconciles expected state with actual Git/filesystem facts and remains read-only.

### [x] M12 — `remove`

Specification coverage: sections 22, 29, 31, 41–43, 67, 83.

Scope:

- Plan/preflight/execute worktree removal child-first while retaining branches and enough workspace/mount state for `checkout` restoration.
- Refuse staged, modified, and relevant untracked content by default. Define `remove --force` solely as permission to remove dirty worktrees and report each override.
- Detect missing/manual removals and stale Git registrations; do not silently prune or repair unrelated worktrees.
- Support `--dry-run`, `--json`, locking, rollback where possible, and explicit recovery when removal cannot be reversed safely.

Test-first slices: three-level order; clean success; every dirty category; force semantics; manual missing directory; Git remove failure mid-operation; state failure/recovery; renamed mounts; retained branches and successful subsequent checkout.

Exit criteria: remove never deletes branches and refuses unsafe work by default.

### [x] M13 — `delete`

Specification coverage: sections 22, 30, 31, 42, 43, 83.

Scope:

- Plan all worktree removals child-first, then branch deletions, then state deletion. Preflight every repository before any mutation.
- Refuse dirty worktrees and unmerged branches by default. Define `delete --force` precisely as allowing dirty worktree removal and forced deletion of unmerged target branches, without bypassing identity/path/integrity checks.
- Model the limited reversibility of branch deletion and persist recovery details if the transaction cannot restore the prior state.
- Support `--dry-run`, `--json`, and exact override reporting.

Test-first slices: fully merged success; unmerged refusal; dirty refusal; force for each/both; branch checked out elsewhere; failure at every remove/delete/state boundary; detached/imported checkout behavior; branch names diverging per repo; recovery instructions.

Exit criteria: destructive behavior is conservative, explicit, exhaustively failure-tested, and cannot delete a branch outside the workspace mapping.

### [x] M14 — `import`

Specification coverage: sections 32–36, 44, 46, 71, 73, 74.

Scope:

- Discover an external workspace from an argument or cwd, identify root and nested checkouts by common Git dir, map them to configured repository IDs, and derive actual parent-relative mounts.
- Determine branches/heads without rewriting them; preserve divergent branches and detached HEADs.
- Reject unknown repositories, duplicate identities, ambiguous project/name inference, and incomplete imports by default. Implement `--name` and `--allow-partial` with explicit missing-checkout state.
- Preflight all observations, then atomically persist state under lock. Support dry-run and JSON.

Test-first slices: renamed `backend` → `api`; renamed descendants; invocation from workspace; directory/branch/explicit naming; divergent and detached branches; unknown/duplicate repo; missing repo reject/allow; ambiguity; import already registered; identity not directory-name assertions.

Exit criteria: every later command operates on the imported paths and per-checkout branches exactly as observed.

### [x] M15 — `doctor` diagnostics and safe repairs

Specification coverage: sections 39–41, 72, 75, 83.

Scope:

- Compare config/state/registry expectations with filesystem and Git: source reachability/identity, roots, branches/heads, mounts, worktree registration, hierarchy, duplicates, unknown repos, stale state, and recovery records.
- Implement human/JSON severity-coded findings and actionable remediation.
- Implement `--fix` only for classified safe actions: verified stale mount/path metadata, safely removable stale state, and narrowly scoped Git worktree prune/repair. Require `--force` or a dedicated future operation for destructive fixes.
- Plan and dry-run repairs, lock mutations, atomically update state, and retain audit details.

Test-first slices: one fixture per finding; multiple simultaneous findings; stale mount with matching identity; identity mismatch not auto-fixed; manual deletion/stale Git metadata; incomplete rollback record; dry-run; idempotent safe fix; failed fix rollback/recovery.

Exit criteria: doctor never changes state without `--fix`, and `--fix` cannot silently perform a destructive repair.

### [x] M16 — Complete help, how-to, and UX consistency

Specification coverage: sections 47–60, 81, 82.

Scope:

- Finish comprehensive global and per-command `-h/--help`, including concepts, supported options, safety rules, exit codes, examples, and worktree location.
- Implement shipped `--how-to` global guide with all 24 topics in section 58 and command-specific guides for create/import/remove/delete/doctor.
- Reject unsupported global/command option combinations. Finish `--verbose` diagnostic events with redaction rules.
- Audit every error for operation, project/workspace/repository context, rollback outcome, and next action.

Test-first slices: help golden tests; every advertised example parses in dry-run/hermetic fixtures; how-to topic assertions; option compatibility matrix; stdout/stderr/JSON audit; sensitive-environment redaction.

Exit criteria: the installed binary is self-documenting and every documented command/option matches implemented behavior.

### [x] M17 — Packaging, cross-platform hardening, and release readiness

Specification coverage: sections 6, 9, 45, 66–69, 86, 93.

Scope:

- Add reproducible versioned builds for Linux, macOS, and Windows, checksums, license/notices, installation instructions, and a minimal release workflow. Do not publish without explicit user authorization.
- Run end-to-end scenarios on all CI OSes, including path separators, executable naming, atomic replacement, locks, permission differences, line endings, spaces, and Unicode.
- Add black-box tests that initialize real nested repositories and exercise create → path/repo path → status → remove → checkout → delete, plus import and rollback/recovery flows.
- Fuzz high-risk pure parsers/resolvers: Git porcelain, YAML/JSON version decoding, mount containment, workspace names, and graph validation.
- Perform a final architecture audit for all 20 invariants in section 90 and document each invariant's enforcing code and tests.

Exit criteria: `go install ./cmd/wtree` produces a usable binary; CI is green on all target OSes; the full specification traceability audit has no MVP gaps.

### [x] M18 — Final independent acceptance review

Scope:

- Have the `reviewer` perform a clean-room pass from the specification, not merely the accumulated diffs.
- Run all static checks, unit/integration/race tests, packaging builds, help example tests, and end-to-end workflows.
- Verify every required command and global option, stable exit/JSON/stdout contracts, all safety/transaction invariants, cross-platform CI evidence, and documentation accuracy.
- Return every finding to `implementer` for test-first correction and repeat independent review until no blocking finding remains.
- Produce `docs/spec/wtree.traceability.md` mapping specification sections 1–93 to implementation packages, tests, help, or an explicitly justified post-MVP item. Required MVP behavior may not be deferred.

Exit criteria: all earlier milestones remain checked, traceability is complete, the reviewer approves release readiness, and the repository-wide quality gate is green.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-12 | M00 | `go run ./cmd/wtree --version`, help and invalid-invocation checks; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check` | Approved after test-first remediation for terminal-flag arguments, Windows race CI, and artifact-clean builds | n/a (workspace has no Git metadata) |
| 2026-08-12 | M01 | `go test ./internal/domain ./internal/pathutil`; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check` | Approved after test-first remediation for symlink-resolved containment and centrally derived checkout paths | n/a (workspace has no Git metadata) |
| 2026-08-12 | M02 | `go test ./internal/git ./internal/testutil`; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check` | Approved after test-first remediation for complete adapter coverage, environment isolation, canonical Git identities, and typed operation failures | n/a (workspace has no Git metadata) |
| 2026-08-12 | M03 | `go test ./internal/store -run TestPreReplacementFailuresPreserveOldState -count=1`; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check` | Approved after test-first remediation for atomic-write fault injection, strict versioned decoding, OS path precedence, durable schemas, and named locks | n/a (workspace has no Git metadata) |
| 2026-08-12 | M04 | `go test ./internal/discovery ./internal/service ./internal/cli ./internal/git ./cmd/wtree -count=1`; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; `git diff --check` | Approved after test-first remediation for atomic init rollback after post-publication failures, duplicate Git identities, real submodule detection, dry-run preflight, OS environment paths, and JSON/error contracts | n/a (workspace has no Git metadata) |
| 2026-08-12 | M05 | `go test ./internal/service -run '^TestResolver' -count=1`; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; gofmt and diff checks | Approved; resolver precedence, identity-verified registry use, generated/default workspace resolution, and central checkout lookup independently reviewed | n/a (workspace has no Git metadata) |
| 2026-08-12 | M06 | focused config regression/CLI tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; gofmt and diff checks | Approved after remediation for preflight-before-project-config-write and hermetic OS config-path test isolation | n/a (workspace has no Git metadata) |
| 2026-08-12 | M07 | focused plan/preflight/CLI tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; gofmt and diff checks | Approved after remediation for Git-authoritative branch validation and preserving unavailable-repository Git failures | n/a (workspace has no Git metadata) |
| 2026-08-12 | M08 | focused transaction/service tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation for shared state paths, prior state/recovery preservation, mandatory validation boundaries, and actionable rollback recovery | n/a (workspace has no Git metadata) |
| 2026-08-12 | M09 | focused create/rollback/concurrency/CLI-render tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation for signal cancellation, rollback-result rendering, concurrent creation, and three-level renamed-mount execution coverage | n/a (workspace has no Git metadata) |
| 2026-08-12 | M10 | focused checkout/lookup safety tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation for mount overlays, repository JSON workspace contract, and unsafe-state checkout refusal coverage | n/a (workspace has no Git metadata) |
| 2026-08-12 | M11 | focused status/Git parser/read-only tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation for lock-free status facts, NUL path parsing, Unicode child mounts, and rename filtering | n/a (workspace has no Git metadata) |
| 2026-08-12 | M12 | focused remove recovery test; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation for non-reversible forced dirty removal recovery | n/a (workspace has no Git metadata) |
| 2026-08-12 | M13 | focused delete force-scope tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation for exact dirty/unmerged force allowances and clean rollback retention | n/a (workspace has no Git metadata) |
| 2026-08-12 | M14 | focused import name/dry-run registry tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation for ambiguous topology naming and read-only dry-run resolution | n/a (workspace has no Git metadata) |
| 2026-08-12 | M15 | focused doctor tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation to preserve retained/recovery state and keep diagnosis/dry-run registry-read-only | n/a (workspace has no Git metadata) |
| 2026-08-12 | M16 | focused how-to/option matrix tests; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; diff check | Approved after remediation for validated how-to forms and meaningful doctor dry-run compatibility | n/a (workspace has no Git metadata) |
| 2026-08-12 | M17 | release reuse, all E2Es, fuzz smokes, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make check`, `go install ./cmd/wtree`, diff check | Approved after remediation for convergent release artifacts and portable organic recovery E2E | n/a (workspace has no Git metadata) |
| 2026-08-12 | M18 | clean-room §§1–93 traceability/help/contract acceptance; all E2Es and fuzz smokes; release reuse/determinism/checksums; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make check`; `go install ./cmd/wtree`; gofmt and diff checks | Approved after remediation for read-only resolution, unified JSON exits, init/config selector semantics, accurate generated help, and complete traceability | n/a (workspace has no Git metadata) |
