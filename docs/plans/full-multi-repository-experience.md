# Multi-repository composition loop and aggregate operations implementation plan

Status: initial
Source specification: [Full multi-repository experience capability specification](../spec/full-multi-repository-experience.md)
Implementation context: [Focused implementation context dump](full-multi-repository-experience-context.md)
Implemented prerequisites: [`wtree` specification](../spec/wtree.spec.md); [Portable manifest clone specification](../spec/portable-manifest-clone.md); [Portable manifest v2 base-repository format specification](../spec/portable-manifest-v2-base-repository-format.md); [Live-branch clone and upstream-aware human status specification](../spec/clone-live-branch-and-upstream-status.md); [Logical project root and repository forest specification](../spec/logical-project-root-base-repository.md); [Automatic nested mount ignore protection specification](../spec/automatic-nested-mount-ignore-protection.md)
Source of truth: [`internal/config`](../../internal/config); [`internal/domain`](../../internal/domain); [`internal/git`](../../internal/git); [`internal/plan`](../../internal/plan); [`internal/service`](../../internal/service); [`internal/store`](../../internal/store); [`internal/transaction`](../../internal/transaction); [`internal/cli`](../../internal/cli); [`cmd/wtree`](../../cmd/wtree); [CI workflow](../../.github/workflows/ci.yml); [Makefile](../../Makefile)
Delivery style: test-first, one independently reviewed milestone at a time; no dependency addition, repository relocation, deletion, branch creation, merge, rebase, shell insertion, hook execution, push, tag, commit, publication, release, or persisted workspace/manifest schema migration

This is the first implementation plan derived from the broad capability
specification. It delivers the implementation-ready P0 and P1 composition
loop: `update`, expanded drift diagnosis, `exec`, `fetch`, drift-aware
`status`, and push readiness. The source specification explicitly permits
multiple plans and forbids planning several later capabilities before focused
schemas and compatibility decisions exist. The deferred scope is listed under
[Scope and change control](#scope-and-change-control); this plan does not by
itself satisfy the complete source specification.

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes the decisions within its delivery slice.

For each unchecked milestone, in order:

1. Read this plan, the source specification, the focused context dump, the
   relevant source-of-truth files, the current worktree, and the durable ledger
   at `docs/ai/runs/full-multi-repository-experience.md`. Create the ledger
   only after execution is authorized and before the first implementation
   dispatch. On resumption, reconcile the plan, ledger, referenced evidence,
   and filesystem, then append a reconciliation checkpoint before dispatch.
2. Record a complete current-milestone checklist in the ledger. Include every
   scope item, test-first slice, documentation item, exit criterion, and
   verification command from that milestone.
3. Give the complete initial packet to the normal `implementer`. Require RED
   -> GREEN -> REFACTOR evidence, changed files, command results, compatibility
   evidence, and unresolved concerns. Use the normal `implementer` for
   remediations starting with zero or one rejected complete submission and the
   `escalation-implementer` only when the ledger already records two.
4. Treat partial work as progress only. Request review only after every
   checklist item has complete evidence.
5. Send every complete submission to the read-only `reviewer`, which inspects
   the current shared filesystem against the full milestone, specification,
   safety, portability, compatibility, and test-quality requirements.
6. Record all material findings with stable IDs and return the complete
   unresolved set in one remediation packet. Apply the exact three-rejected-
   complete-remediation limit from
   [`milestone-supervision.md`](../ai/milestone-supervision.md). Use an
   `escalation-reviewer` only for the bounded adjudication cases allowed there.
7. After reviewer approval, run the milestone verification as the main agent,
   update affected contracts and documentation, check the milestone, append a
   concise execution-log row, replace the ledger snapshot with the next
   unchecked milestone, and dispatch that milestone immediately.

Follow the mandatory ledger shape, checkpoints, transitions, and
final-response gate in
[`run-ledger-layout.md`](../ai/run-ledger-layout.md). Do not stop for ordinary
test failures, reviewer findings, partial submissions, rollback remediation,
or approved milestones. Preserve unrelated worktree changes; do not use
destructive cleanup commands; commit only when separately authorized. A final
response during execution is permitted only by the durable-ledger gate.

The only anticipated external blockers are an irreconcilable conflict between
the candidate manifest and an existing repository identity, a platform that
cannot provide the required safe local mutation/rollback primitive, or a
request to expand into the explicitly deferred compatibility-gated scope.
Record exact evidence and safe continuation conditions rather than weakening a
contract.

## Fixed implementation decisions

### Delivery slice and compatibility posture

- Deliver specification sections 3-6, limited to P0 and P1 behavior. Preserve
  all implemented acquisition, topology, ignore, resolver, transaction,
  recovery, error, and output contracts.
- Keep portable manifest version 2, local project configuration version 2,
  workspace/registry/recovery version 1, workspace plan version 1, and clone
  plan/result version 2. This plan adds no fields to an existing strict stored
  or portable schema.
- Backward compatibility with binaries from older `wtree` releases is not a
  delivery requirement. Keeping the current schema versions is a scope and
  risk decision for this plan, not an older-client promise. Every successful
  mutation must nevertheless leave all retained repositories and persisted
  workspaces resolvable, valid, inspectable, and safely operable by the
  resulting application.
- New command JSON uses a command-owned `version: 1`, `operation`, overall
  `status`, deterministic `repositories`, and an optional bounded `failure`.
  Repository arrays use `domain.Project.ParentFirst()` order; user selections
  may filter that order but never reorder it.
- The common v1 wire names are fixed: envelopes use `version`, `operation`,
  `status`, `dryRun` when applicable, `projectId`, `workspace`,
  `repositories`, and optional `failure`; repository facts use `id`, optional
  `parentId`, `mount`, `path`, optional `branch`, optional `head`, optional
  `observedCommit`, `status`, and optional `failure`; failures use `code` and
  `message`. Update adds `classification`, `plannedBranch`, `actualHead`,
  `action`, and `rollbackStatus`; exec adds envelope `command` and repository
  `stdout`, `stderr`, `stdoutTruncated`, `stderrTruncated`, and `exitCode`;
  fetch adds `remote`, `remoteRef`, `previousRemoteCommit`, and
  `actualRemoteCommit`; push adds `findings`, whose entries use `code` and
  `message`. Omit inapplicable optional fields rather than emitting invented
  empty facts.
- Allowed envelope statuses are `planned`, `completed`, or `failed` for
  update/exec/fetch and `ready`, `blocked`, or `failed` for push readiness.
  `failed` means an operational command failure; `blocked` means successful
  push-readiness observation with one or more readiness findings.
- A failed aggregate result is rendered exactly once. Add one CLI/root error
  boundary that carries the constructed result and exit category so JSON mode
  renders the result envelope instead of appending a generic JSON error
  document. Human mode renders the result then returns the same stable exit
  category.
- Cause-specific application categories always win. A rollback-incomplete
  result is always `rollback_incomplete`; otherwise, after collecting allowed
  partial results, the first failed repository in deterministic execution
  order supplies its existing validation, Git, dirty-workspace, conflict, or
  internal category. A launched `exec` child that exits non-zero and a
  successfully observed but blocked push-readiness result use `conflict`/exit
  code 8. A fetch transport or Git-command failure remains `git`/exit code 6.
  Per-repository exec/fetch statuses are `planned`, `completed`, `failed`, or
  `canceled`.
- Existing JSON fields are never removed or retyped. Additive status and
  doctor fields use `omitempty` where absence preserves the old semantic.
- Existing status findings continue to return exit code 0 when observation
  succeeds. Aggregate `update`, `exec`, `fetch`, and push-readiness return
  non-zero when any required repository fails.
- No third-party dependency may be added. No commit, push, publication,
  release, or deployment is authorized by execution of this plan.

### Shared aggregate behavior

- Resolve project/workspace context through `service.Resolver`. Observational
  commands use `ResolveReadOnly`; mutating `update` resolves read-only for
  planning and reconciles/publishes only inside its locked commit phase.
- Every per-repository result includes repository ID, resolved checkout path,
  effective mount, actual or planned branch, and actual HEAD or the exact
  preflight observation relevant to the command.
- Planning errors occur before mutation and identify the repository and check.
  Independent execution failures are collected when the command has no
  rollback promise (`exec` and `fetch`). Transactional `update` stops at its
  first execution failure and reports completed, failed, rolled-back, and
  unreverted effects.
- Dry-run performs all safe local validation and declared remote observation,
  but creates no directories, refs, locks, state, journals, recovery records,
  or temporary repositories. JSON mode emits no human progress on stderr.
- All Git invocations use argument arrays and the existing sanitized,
  non-interactive, locale-neutral adapter environment. Values derived from a
  manifest, config, branch, path, URL, or user command argument are never
  interpolated into a shell string.
- Credentials and query-like secret shapes are redacted with the established
  bounded diagnostic helpers before plans, results, journals, recovery data,
  or errors are persisted or rendered.
- Direct-process stdout and stderr use the M00 bounded pre-truncation redaction
  contract independently. Capture tracks the total byte count and retains the
  first 64 KiB plus rolling last 64 KiB as inspection windows. When total
  output is at most 128 KiB, reconstruct the exact original byte sequence by
  known offsets (never content matching), redact that complete string once,
  and only then select its first and last 32 KiB. When output is larger, a
  bounded stateful scanner observes every byte while it streams, records
  credential/query redaction spans that intersect either retained window, and
  applies them without treating the genuinely omitted middle as adjacency.
  Only the redacted first and last 32 KiB are rendered, separated by
  `\n[wtree: output truncated]\n`. Tests must prove overlapping-window
  reconstruction, read-boundary and final-cut credentials, real gaps, repeated
  content, numeric-password and username-only userinfo, sensitive queries,
  ordinary URLs and `host:8443/path@text`, bounded memory, and independent
  stdout/stderr behavior.

### `wtree update`

- Implement exactly `wtree update [--project <directory-or-.wtree.yml>]
  [--from <manifest-source>] [--dry-run] [--json] [--verbose]`. Do not add a
  `sync` alias.
- Update operates only on the source/default workspace represented by the
  project configuration and default workspace state. It never updates a
  generated named workspace implicitly.
- Preflight reads and validates every persisted workspace generation against
  the current project before considering the candidate. Any already-invalid
  state fails with zero mutation. A candidate that adds or removes a repository
  fails with zero mutation while any non-default workspace state exists,
  including an imported partial workspace; pure fast-forwards may proceed
  because they do not reinterpret those states.
- Source precedence is `--from`, then local configuration `manifest.source`.
  Normalize and validate the selected source with `ManifestSourceLoader`.
  Persist a replacement source only after complete success.
- The candidate manifest must retain the project ID and base-repository ID.
  An existing repository ID must retain its recorded Git identity, parent,
  mount, default branch, clone remote/URL, and upstream remote/merge contract.
  A mount change is `mount-change-blocked`; any other existing-repository
  contract change is `structurally-inconsistent`. Only the selected remote
  ref's commit tip may move. New IDs must pass the complete clone verification
  set.
- Classification is a pure, immutable plan step. Each current/candidate
  repository is exactly one of `unchanged`, `fast-forwardable`, `added`,
  `removed-retained`, `dirty`, `divergent`, `missing`,
  `mount-change-blocked`, or `structurally-inconsistent`.
- Existing mount changes are reported as `mount-change-blocked` and fail the
  whole preflight with zero mutation. This applies even when no linked
  worktrees are detected; relocation remains compatibility-gated by
  specification section 10.2.
- Dirty, divergent, detached, missing, identity-mismatched, occupied-target,
  invalid-ignore, invalid-tracked-manifest, or structurally inconsistent
  required repositories fail the complete preflight. Update never resets,
  cleans, stashes, merges, rebases, creates a branch, or substitutes a ref.
- Existing default branches may move only by fast-forward to the
  execution-time tip fetched from the manifest-selected remote ref. The
  preflight advertised commit is diagnostic evidence, not the checkout target.
  A deleted, ambiguous, non-commit, non-descendant, or unverifiable
  replacement fails and triggers rollback of owned effects.
- Added repositories are assembled parent-first in a private staging area,
  verified with clone's selected-ref, actual-HEAD, upstream, identity-root,
  clean-worktree, no-submodule, tracked-manifest, and immediate-parent-ignore
  contracts, then published only to absent verified mounts.
- Removed repositories are never deleted or moved. They are excluded from the
  new project/workspace model and recorded as retained unmanaged checkouts in
  a strict project-local reconciliation record at
  `<data-dir>/projects/<project-id>/reconciliation.json` with version 1,
  manifest digest, repository ID, canonical path, recorded common Git
  directory, and the digest of the manifest generation that removed it. This
  new file is owned by the update service; it is not a workspace-state
  extension and contains no URL or credentials.
- Before the first existing-checkout or public metadata mutation, write a
  strict version-1 operation journal below
  `<data-dir>/projects/<project-id>/update/<operation-id>/`. The journal owns
  byte-for-byte backups of local config, default workspace state, registry,
  reconciliation state, and the tracked manifest when those files may change,
  plus old/new repository HEADs and owned staging/publication identities.
  Files are private, atomically written, and redacted.
- Hold the project lock from execution revalidation through repository effects
  and publication. Execute repository effects parent-first; roll them back in
  exact reverse order. A branch may be restored only when its ref, HEAD,
  worktree cleanliness, and identity still match the operation's owned new
  generation. Never overwrite a concurrent generation.
- Publish matching candidate configuration, default workspace state, registry
  facts, and reconciliation state using compare-and-swap checks and byte-exact
  backups. Never write or stage `project.wtree.yml` independently: its bytes
  change only with the verified base-repository fast-forward and must equal the
  candidate bytes at the actual base HEAD. The journal is the visibility
  boundary during the multi-file commit: resolver-backed mutators refuse the
  project while it exists; status and doctor report it.
- Remove the journal only after full postcondition verification. A completely
  rolled-back failure removes its owned journal and returns the original error
  marked as cleanly rolled back. Incomplete rollback retains the journal and
  backups, writes the established recovery summary, and returns
  `rollback_incomplete`. Recovery is diagnostic in this plan; no automatic
  repair command is added.

### Drift-aware `doctor` and `status`

- One service-owned drift snapshot compares the current portable manifest,
  local configuration, default or selected workspace state, registry identity,
  retained-unmanaged record, operation/recovery records, and actual checkout
  facts. `update`, `doctor`, and `status` consume this snapshot rather than
  independently inventing repository-set classifications.
- `doctor` adds stable findings for manifest-declared missing repositories,
  state/disk repositories absent from the manifest, identity/URL/upstream/
  branch/mount disagreements, missing committed immediate-parent ignore
  coverage, and unresolved update journal/recovery records.
- New finding codes are `manifest-repository-missing`,
  `manifest-repository-unmanaged`, `manifest-configuration-mismatch`,
  `repository-url-mismatch`, `repository-upstream-mismatch`,
  `parent-ignore-missing`, `retained-unmanaged-repository`,
  `update-in-progress`, and `update-recovery-record`. Reuse existing identity,
  branch, mount, missing, and recovery codes when their meaning is already
  exact; do not create aliases for them.
- New doctor findings are observational and not fixable. Existing allowlisted
  `doctor --fix` repairs remain unchanged; it does not clone, move, delete,
  fast-forward, edit ignore rules, execute recovery, or infer manifest intent.
- Status remains network-free and non-mutating. It reports locally available
  manifest-vs-state and manifest-vs-disk drift, expected/actual identity,
  mount, branch, and commit facts while preserving all existing fields and the
  existing synchronized-workspace human table.
- Add only these workspace JSON fields: `manifestDigest`, `manifestDrift`, and
  `repositoryDrift`. Each `repositoryDrift` entry has `id`, `classification`,
  optional `path`, and optional `expectedPath`. Add only `expectedHead` and
  `identityMismatch` to existing repository status entries. All are omitted
  when unavailable or false so existing synchronized JSON remains additive.
- When the common Git directory or top-level identity check fails, preserve
  the current `unknownRepository: true`, `status: "unknown-repository"`, human
  `STATUS`, and `UPSTREAM: n/a` behavior. Also set the additive
  `identityMismatch: true` and corresponding drift entry; never replace or
  reinterpret the existing fields.
- Status drift classifications are exactly `manifest-missing`, `state-only`,
  `disk-only`, `retained-unmanaged`, `identity-mismatch`, or `path-mismatch`.
- The existing `partial` and `missingRepositoryIds` fields retain their current
  meaning. Do not introduce workspace kind, omission, lock, provenance, or
  transport fields before the focused workspace-state specification.

### `wtree exec`

- Implement the exact command surface in specification section 6.1. The
  tokens after `--` are mandatory and are passed directly to
  `exec.CommandContext`; `wtree` never inserts a shell.
- Select present repositories from persisted workspace state. Existing partial
  workspaces run only their present repositories and report the existing
  explicitly missing set; no new omission semantics are inferred.
- Default order is parent-first and `--reverse` is child-first. Run one process
  at a time so ordering is observable and deterministic. A non-zero process
  result does not prevent later repositories from running; context
  cancellation marks unstarted repositories canceled and starts no more
  processes.
- Add the documented `WTREE_*` variables from persisted state corroborated by
  runtime Git facts. Override inherited variables with the verified values;
  do not expose internal data paths, URLs, or credentials.
- Capture bounded stdout and stderr plus exit status per repository. Human mode
  streams repository-labeled captured results after each process. JSON emits
  only the deterministic result envelope on stdout and no progress on stderr.
  Apply the shared bounded pre-truncation redaction contract, then retain the
  first 32 KiB and last 32 KiB of each redacted stream; when the middle is
  omitted, join them with the exact marker `\n[wtree: output truncated]\n` and
  set the corresponding JSON truncation boolean.
- Dry-run verifies paths, identities, branches, and HEADs and lists the exact
  argument array and redacted environment facts without starting a process.
  Arbitrary command side effects are explicitly outside rollback guarantees.

### `wtree fetch`

- Add `wtree fetch [--project <path>] [--workspace <name>] [--dry-run]
  [--json] [--verbose]`. Fetch every present repository in deterministic
  parent-first order using its configured upstream remote and explicit ref.
- Normal execution updates remote-tracking refs but never changes local branch
  refs, HEADs, index entries, or worktree files. Continue after a repository
  failure and return a failed aggregate result after all possible observations.
- Dry-run uses remote advertisement inspection and does not invoke `git fetch`
  or mutate refs. Record observed remote commits separately from actual local
  HEADs. Never fall back to remote `HEAD` or another branch.
- Fetch has no rollback promise because remote-tracking ref updates are safe
  Git metadata observations. Documentation must say this plainly.

### Push readiness

- Implement the exact non-publishing `wtree push [--project <path>]
  [--workspace <name>] [--json]` surface from specification section 6.3. Do
  not add `--dry-run`: the command is always an observational readiness check.
- Inspect every present repository and fail readiness for dirty state,
  detached HEAD, missing/malformed upstream, local commits not at the
  advertised upstream, a locally behind or divergent branch, identity-root
  mismatch, or a project-metadata commit not proven reachable from the exact
  advertised commit.
- Remote proof uses the configured upstream URL/ref and immutable object IDs.
  It must not create or update local refs, invoke `git push`, create tags, or
  write any project state. A transport/authentication failure is an
  operational failure, not a readiness finding.
- Existing partial workspaces are not ready because the absent repositories
  cannot be proven remotely available. The result identifies them without
  reclassifying them as intentional omissions.
- Readiness finding codes are exactly `dirty`, `detached`, `missing-upstream`,
  `ahead`, `behind`, `diverged`, `unpublished-head`, `identity-mismatch`,
  `metadata-commit-unavailable`, `missing-repository`, and
  `partial-workspace`.

## Stable contracts to establish early

| Contract | Owner and consumers | Invariant and enforcement |
|---|---|---|
| Aggregate result v1 | `internal/service`; rendered by `internal/cli` | Command-owned envelopes share deterministic repository identity/path/branch/commit/failure facts without making one union schema. Structural JSON tests, redaction tests, and output-writer failures enforce it. |
| Drift snapshot | `internal/service`; consumed by update, status, and doctor | One immutable observation owns repository-set and structural classification; no consumer mutates or silently re-observes it. Table tests permute input and cover stale/unknown/missing/retained cases. |
| Update plan v1 | `internal/service`; consumed only by update executor and CLI | Candidate bytes/digest, source, current generations, observed remote tips, classification, actions, and rollback ownership are complete, validated, immutable, and credential-free. Plan round-trip/tamper tests and zero-mutation dry-run tests enforce it. |
| Update journal/reconciliation v1 | `internal/service` with atomic primitives from `internal/store`/`internal/fsutil`; observed by resolver and doctor | Journal checkpoints make incomplete multi-file/repository mutation visible and resumable; reconciliation distinguishes retained removals from unknown drift. Strict decode, mode, CAS, crash-boundary, and byte-exact restoration tests enforce it. |
| Git aggregate primitives | `internal/git`; consumed by service only | Explicit remote/ref fetch, fast-forward, owned rollback, runtime identity, and direct-command facts use argument arrays and the hardened environment. Hermetic bare-remote and fake-binary tests cover ref movement, deletion, credentials, cancellation, and no-hook/no-shell behavior. |
| Direct execution | `internal/service`; process adapter beneath service; rendered by CLI | One bounded direct process per selected repository in declared order, with exact verified `WTREE_*` facts and no shell insertion. Fake-executable tests enforce argv/environment fidelity and continuation behavior. |
| Status compatibility | Existing status service/CLI | Observation findings do not change success exit semantics; old fields and synchronized human rendering remain stable, while additive drift facts are deterministic. Golden and decoded compatibility tests enforce it. |

## Architecture and dependency boundaries

```text
internal/cli commands and rendering
          |
          v
internal/service command coordinators
   |          |              |
   |          |              +--> immutable aggregate results
   |          +--> shared drift snapshot/classifier
   +--> update planner --> update executor/journal
          |                    |
          v                    v
 internal/config/domain   internal/store/fsutil/lock/transaction
          |
          v
 internal/git + direct-process adapter
```

- `internal/config` owns strict local and portable decoding and pure URL/ref
  validation. It must not learn CLI flags, Git subprocesses, workspace state,
  update journals, or result rendering.
- `internal/domain` owns project topology/order and current workspace
  invariants. This plan does not widen its persisted-model meaning.
- `internal/git` owns Git command construction, parsing, sanitized environment,
  bounded diagnostics, and typed fact/mutation results. It must not know
  manifest schemas, update classifications, CLI rendering, or data-dir paths.
- `internal/service` owns source precedence, drift classification, immutable
  plans, command coordination, journals, rollback ownership, per-repository
  results, and stable application error categories.
- `internal/store`, `internal/fsutil`, `internal/lock`, and
  `internal/transaction` provide strict decoding, atomic/CAS publication,
  locking, and reversible step primitives. Reuse them without weakening their
  existing byte ownership rules; update-specific storage stays service-owned.
- `internal/cli` owns Cobra surfaces, exact help, human rendering, and JSON
  envelope emission. It must not call Git to enrich a service result or
  reconstruct checkout paths from mounts.
- `cmd/wtree` continues to map stable service errors to existing exit codes.
  New findings do not create a new error category or exit code.

## Global definition of done

- Every changed behavior has recorded RED -> GREEN -> REFACTOR evidence. Tests
  prove success plus invalid-input, no-mutation, cancellation, redaction,
  deterministic ordering, and failure/rollback behavior at the touched boundary.
- Filesystem/Git tests are hermetic: temporary repositories, bare remotes,
  isolated config/data directories, injected clocks/processes/filesystems, no
  real credentials, no developer Git configuration, and no network service.
- Update failure injection covers every owned repository and publication
  boundary, byte-exact restoration, concurrent-generation preservation,
  complete rollback cleanup, incomplete rollback journal/recovery visibility,
  cancellation, and paths containing spaces and Unicode.
- Public JSON has decoded structural assertions and deterministic/golden
  coverage. JSON mode writes no human stderr. Writer failures are returned and
  never trigger a second JSON document.
- Existing init/clone/create/checkout/import/remove/delete/list/path/repo/
  doctor/status/config/project behavior and all strict schema/version tests
  remain green. No existing fixture is weakened to accommodate a new command.
- Every successful update reloads every persisted workspace through the normal
  resolver/list validation path, corroborates every retained repository by
  recorded Git identity, and leaves existing inspection and teardown safety
  checks usable. Repository additions/removals with any non-default state are
  proven zero-mutation refusals rather than successful incompatible changes.
- Public behavior ships with command help, root help, README/how-to/tutorial or
  troubleshooting updates in the same milestone. Historical run ledgers and
  completed execution logs remain untouched.
- After formatting only changed Go files, every milestone runs:

  ```text
  make fmt-check
  go vet ./...
  go test -timeout=15m ./... -count=1
  go test -race -timeout=15m ./... -count=1
  go build ./cmd/wtree
  ```

- The final milestone additionally runs:

  ```text
  make tutorial-test
  make release-test
  ```

  and requires the repository's Ubuntu, macOS, and Windows CI matrix to pass
  formatting, vet, tests, race tests, build, and release-layout checks.
- A normal independent reviewer approves every milestone with no unresolved
  material finding. The final acceptance review maps every requirement in this
  plan and source-specification sections 3-6 to an automated or named CI
  evidence path.

## Scope and change control

In scope:

- source/default-workspace manifest reconciliation without existing checkout
  relocation or deletion;
- shared structural drift observation and expanded doctor/status reporting;
- direct aggregate execution, explicit aggregate fetch, and non-publishing push
  readiness; and
- the documentation, recovery visibility, compatibility tests, and CI evidence
  required by those behaviors.

Deferred to focused specifications and later plans:

- release locks and locked materialization;
- ordinary checkout remote-branch materialization, mixed/fallback/new-branch
  policies, generalized workspace kinds, provenance, and intentional omission;
- workspace-state v2 or any migration of strict persisted data;
- submodule/subtree adoption and any live conversion;
- lifecycle hooks, hook trust/retry, and LFS orchestration;
- selective materialization, shallow/partial clone, transport-mode state, URL
  profiles, relative URL semantics, or manifest/config version changes;
- existing-repository relocation, cleanup of removed repositories, automatic
  repair/recovery, coordinated publishing, Git push, tag creation/signing, and
  release automation.

A reviewer finding already required by this plan is added to the current
milestone checklist. A finding requiring deferred behavior, a schema/version
change, a new dependency, or an unauthorized destructive/publication action is
recorded for user direction and does not consume a remediation attempt.

## Risk and rollout

- Update is the highest-risk surface. Its planner/classifier, Git primitives,
  journal storage, and rollback engine are approved before the public command
  may execute a mutation.
- There is no feature flag and no automatic migration. New command surfaces
  are additive; existing commands keep their current defaults.
- Update refuses cases not proven safe, especially relocation, dirt,
  divergence, detached branches, identity changes, occupied mounts, stale
  generations, partial/generated workspaces, and unresolved recovery.
- Fetch and exec explicitly lack rollback for their documented effects. Their
  per-repository continuation and aggregate failure make partial completion
  visible rather than implying transactionality.
- Rollout evidence is the cross-platform CI matrix plus hermetic end-to-end
  fixtures. Publishing binaries remains separately authorized.

## Milestones

### [x] M00 — Establish aggregate and Git/process contracts

Specification coverage: [system-wide invariants](../spec/full-multi-repository-experience.md#3-system-wide-invariants); [shared planning and reporting](../spec/full-multi-repository-experience.md#4-shared-planning-and-reporting-contract)

Scope:

- Add command-neutral service facts for ordered repository identity, path,
  mount, branch, HEAD/observation, status, bounded output, and stable failure.
- Establish the bounded pre-truncation redaction primitive used by later
  aggregate execution: first/last 64 KiB inspection windows, total-offset
  overlap reconstruction and one complete-string redaction at or below
  128 KiB, and bounded full-stream redaction evidence for larger output before
  final first/last 32 KiB selection. A real omitted gap is never concatenated
  or parsed as adjacent bytes.
- Add only the Git/process adapter primitives required by later milestones:
  explicit configured-ref fetch observation/execution, fast-forward with an
  ownership-safe inverse, and direct argument-array execution with bounded
  streams.
- Preserve the current `git.Git` fake seam and sanitized environment; update
  all fakes intentionally rather than bypassing the interface.
- Add no public command and no speculative storage abstraction.

Test-first slices:

1. Encode deterministic parent-first aggregate facts and reject duplicates,
   missing required fields, secret-shaped diagnostics, and invalid status/failure
   combinations.
2. Observe and fetch one explicit configured ref without consulting remote
   `HEAD`; prove branch movement/deletion, cancellation, and no local branch or
   worktree mutation during observation.
3. Fast-forward only a clean attached branch and restore it only while the
   owned new generation still matches; reject non-descendants and concurrent
   movement without data loss.
4. Invoke a fake executable with exact argv/environment/cwd, bounded streams,
   exit status, cancellation, and no implicit shell interpretation. Prove
   pre-truncation credential/query scanning across process-read and final-cut
   boundaries; exact offset-based reconstruction for every overlap size up to
   128 KiB; non-overlapping larger streams without artificial adjacency;
   numeric-password and username-only userinfo; ordinary URL, port/path/at-sign,
   punctuation, and repeated-content preservation; exact first/last 32 KiB
   output; independently bounded stdout/stderr; and bounded scanner memory.

Verification:

- `go test ./internal/git ./internal/service -run 'Test(Aggregate|FetchConfigured|FastForward|DirectProcess)' -count=1`
- Run the global milestone commands.

Exit criteria: Later planners can use tested, non-public aggregate, Git, and
process contracts without choosing command behavior or weakening existing Git
fakes, redaction, cancellation, or rollback ownership.

### [x] M01 — Build one immutable drift snapshot and update classifier

Specification coverage: [shared planning and reporting](../spec/full-multi-repository-experience.md#4-shared-planning-and-reporting-contract); [`wtree update`](../spec/full-multi-repository-experience.md#51-wtree-update); [extended doctor](../spec/full-multi-repository-experience.md#53-extended-doctor-coverage)

Scope:

- Load and correlate current tracked portable manifest bytes, candidate
  manifest bytes, local config, default workspace state, registry generation,
  retained-unmanaged state, unresolved operation/recovery records, actual
  checkout identity/path/branch/HEAD/cleanliness, ignore facts, and advertised
  selected refs.
- Inventory every persisted workspace state before mutation, validate each
  against the current project, and record whether any non-default complete or
  imported-partial generation exists.
- Produce one immutable parent-first drift snapshot and the exact update
  classifications fixed above, including manifest/state/disk set differences.
- Reject project/base changes; any existing repository identity, parent, mount,
  default-branch, clone, or upstream contract change; repository-set changes
  while non-default state exists; generated/partial workspace targets; stale
  generations; invalid manifests; and unresolved recovery before any mutation.
- Keep classification pure after injected observations and include complete
  repository/check provenance for deterministic JSON.

Test-first slices:

1. Classify unchanged, fast-forwardable, added, and removed-retained
   repositories across nested forests and input permutations.
2. Classify and reject dirt, divergence, detached/missing/unknown identity,
   blocked mount changes, occupied additions, ignore failure, tracked-manifest
   mismatch, and inconsistent state/config/registry generations.
3. Preserve pure fast-forward eligibility with valid named workspaces, but
   reject additions/removals when any named complete or imported-partial state
   exists; prove every rejected state generation remains byte-identical.
4. Move or delete a selected remote ref at the observation seam and prove the
   snapshot records evidence without turning it into an exact execution target.
5. Prove all failures identify repository/check, redact sources, and leave
   files, refs, locks, state, and recovery paths byte-for-byte absent/unchanged.

Verification:

- `go test ./internal/service -run 'Test(DriftSnapshot|UpdateClassif)' -count=1`
- `go test ./internal/config ./internal/domain ./internal/git -count=1`
- Run the global milestone commands.

Exit criteria: A single tested snapshot completely determines whether update
may proceed and supplies status/doctor consumers without hidden re-observation
or material implementation choices.

### [x] M02 — Publish a validated update plan and dry-run contract

Specification coverage: [`wtree update`](../spec/full-multi-repository-experience.md#51-wtree-update); [required verification](../spec/full-multi-repository-experience.md#12-required-verification)

Scope:

- Add strict `UpdatePlanVersion = 1` and a planner containing normalized source,
  candidate digest/bytes, captured public generations, per-repository
  classifications, observed commits, planned actions, order, verification,
  publication set, and rollback ownership.
- Validate plan completeness and internal consistency; keep candidate bytes
  privately copied and excluded from rendered JSON.
- Add CLI parsing and human/JSON dry-run rendering for the exact `update`
  surface. `--from` precedence and credential-free normalization are fixed here.
- Document update refusal categories and the no-relocation/no-deletion policy
  in command help and installed how-to text.

Test-first slices:

1. Produce byte-stable parent-first plans for unchanged, fast-forward, added,
   and removed-retained combinations and reject tampered versions, digests,
   order, facts, actions, or secret-bearing fields.
2. Prove stored-source and `--from` precedence, local/HTTP source validation,
   aggregate remote failure collection, and bounded redacted diagnostics.
3. Exercise human and decoded JSON dry-run output, writer failure, verbose/JSON
   separation, cancellation, and exact zero-mutation assertions over filesystem,
   refs, locks, journal, state, registry, and recovery paths.

Verification:

- `go test ./internal/service ./internal/cli -run 'Test(UpdatePlan|ExecuteUpdateDryRun|UpdateHelp)' -count=1`
- Run the global milestone commands.

Exit criteria: `wtree update --dry-run` is a complete, stable, useful public
preflight that can be reviewed independently and cannot mutate or expose
credentials.

### [x] M03 — Execute repository updates with journaled rollback

Specification coverage: [`wtree update`](../spec/full-multi-repository-experience.md#51-wtree-update); [system-wide mutation invariants](../spec/full-multi-repository-experience.md#3-system-wide-invariants)

Scope:

- Implement strict private update journal/backups, project locking,
  execution-time revalidation, parent-first effects, reverse rollback, and
  operation progress.
- Fast-forward existing repositories to execution-time selected-ref tips only
  after clean/identity/ref revalidation. Never merge, rebase, reset unowned
  work, run hooks, or create branches.
- Stage added repositories privately and apply the full clone verification set
  before publishing absent mounts with exact ownership receipts.
- Retain removed repositories untouched and prepare their reconciliation facts;
  do not yet commit public metadata in this milestone.
- On every injected failure/cancellation, either restore all owned effects and
  remove the journal or retain precise private recovery evidence without
  overwriting concurrent changes.

Test-first slices:

1. Fast-forward one and nested existing repositories after the selected branch
   moves between plan and execution; record actual HEADs and deterministic order.
2. Stage and publish nested additions while enforcing identity, upstream,
   tracked base manifest, clean state, submodule refusal, grouping ownership,
   and immediate-parent ignore rules.
3. Inject failure before/after every Git, staging, rename, and verification
   boundary and prove reverse rollback, byte/ref ownership, concurrent-change
   preservation, and clean-versus-incomplete recovery classification.
4. Prove cancellation, paths with spaces/Unicode, symlink/case aliases,
   credential redaction, no hooks, no remote-HEAD fallback, and no deletion or
   movement of removed/existing repositories.

Verification:

- `go test ./internal/git ./internal/service -run 'TestUpdateExecut' -count=1`
- `go test ./internal/service -run 'TestUpdate.*(Rollback|Recovery|Cancel|Concurrent|Added)' -count=1`
- Run the global milestone commands.

Exit criteria: Repository effects are safe and exhaustively failure-injected
behind an internal executor, with no public metadata generation committed and
no unowned cleanup path.

### [x] M04 — Commit update metadata and expose the complete command

Specification coverage: [`wtree update`](../spec/full-multi-repository-experience.md#51-wtree-update); [portable acquisition baseline](../spec/full-multi-repository-experience.md#52-portable-acquisition-baseline)

Scope:

- Atomically/CAS-publish matching local config, default-workspace state,
  registry, and reconciliation generations under the journal visibility
  boundary; verify the tracked manifest already supplied by the actual base
  HEAD, then verify the complete project and remove the journal.
- Persist replacement `manifest.source` only on success; keep exact prior bytes
  on any clean rollback. Record retained-unmanaged checkouts without URL or
  credentials.
- Wire non-dry-run CLI progress, human result, version-1 JSON, stable exit/error
  mapping, and output-writer failure behavior.
- Make resolver-backed mutators refuse active update journals; make read-only
  resolution preserve enough context for status/doctor visibility.
- Add end-to-end update of a nested forest and regression coverage for current
  init/clone with manifest v2 topology.
- After publication, load and validate every persisted workspace through the
  normal resolver/list paths and verify every retained repository by recorded
  Git identity before the journal may be removed.

Test-first slices:

1. Complete unchanged, fast-forward, added, removed-retained, and replacement-
   source updates and verify config/state/registry/manifest/reconciliation agree
   with actual HEADs and identities.
2. Inject failure at every publication/CAS/postcondition/journal-removal
   boundary; prove byte-exact rollback or durable incomplete recovery and
   refusal to overwrite concurrent generations.
3. Invoke through the CLI in human, verbose, JSON, and cancellation modes;
   prove deterministic per-repository results, no JSON stderr, correct exit
   codes, and no false success after partial or incomplete rollback.
4. Re-run acquisition and forest regressions to prove update introduced no
   alternate project model, schema migration, exact preflight pinning, or
   relocation behavior.
5. With named workspaces present, complete a pure fast-forward and prove list,
   lookup, status, checkout/remove/delete safety checks, and repository lookup
   retain their existing behavior; separately prove repository-set changes are
   rejected before mutation.

Verification:

- `go test ./internal/service ./internal/cli ./cmd/wtree -run 'Test.*Update' -count=1`
- `go test ./internal/service -run 'TestUpdate.*(Publication|Generation|Journal|Recovery)' -count=1`
- Run the global milestone commands.

Exit criteria: The complete public update workflow is transactional,
recoverable, schema-compatible, documented, and end-to-end usable for the safe
classification set fixed by this plan.

### [x] M05 — Extend doctor with shared drift and recovery visibility

Specification coverage: [extended doctor coverage](../spec/full-multi-repository-experience.md#53-extended-doctor-coverage)

Scope:

- Consume the shared drift snapshot and add stable findings for all P0
  manifest/state/disk, identity, URL/upstream, branch/mount, committed-ignore,
  retained-unmanaged, journal, and recovery cases.
- Preserve deterministic topology order, severity, repository IDs, and
  additive JSON/human compatibility.
- Keep every new finding non-fixable and preserve the existing doctor repair
  allowlist and lock/CAS safety unchanged.
- Document how operators distinguish retained removal, drift, active/incomplete
  update, and actionable recovery.

Test-first slices:

1. Diagnose each new finding independently and in combined nested-forest
   permutations with stable codes/order and no false classification of current
   imported partial workspaces.
2. Prove `doctor` and `doctor --fix --dry-run` create no files/locks/refs and
   `doctor --fix` never applies a new disallowed repair.
3. Preserve existing repair behavior, output fields, writer failures,
   cancellation, and current recovery visibility while adding update journals.

Verification:

- `go test ./internal/service ./internal/cli -run 'TestDoctor' -count=1`
- Run the global milestone commands.

Exit criteria: Doctor explains every P0 drift/recovery state without inferring
destructive or network mutation, and existing safe repairs remain unchanged.

### [x] M06 — Deliver direct deterministic cross-repository execution

Specification coverage: [`wtree exec`](../spec/full-multi-repository-experience.md#61-wtree-exec)

Scope:

- Add the exact `exec` command surface, resolver/service coordinator, direct
  process adapter, deterministic ordering, verified environment, bounded
  output, aggregate results, and dry-run.
- Support present repositories in existing complete and imported-partial
  workspaces without inventing omission/workspace-kind semantics.
- Continue after ordinary child exit failures; stop starting processes on
  cancellation; never promise rollback for invoked-program effects.
- Add root/command help and practical examples showing explicit shell use only
  when the user supplies a shell executable.

Test-first slices:

1. Execute an argv-recording fake across a nested forest parent-first and
   child-first, verifying cwd, exact tokens, stable `WTREE_*` values, and no
   inherited override.
2. Continue after non-zero exits, bound/truncate stdout/stderr deterministically,
   aggregate failure, and classify cancellation/unstarted repositories.
3. Prove dry-run validation without process start, partial-workspace behavior,
   missing/identity/branch/HEAD refusal, JSON silence, writer failure, and
   literal metacharacters without shell expansion.

Verification:

- `go test ./internal/service ./internal/cli ./cmd/wtree -run 'Test.*Exec' -count=1`
- Run the global milestone commands.

Exit criteria: Users can safely and predictably invoke one explicit executable
across all present repositories with complete machine-readable evidence and no
implicit shell or rollback claim.

### [x] M07 — Deliver explicit aggregate fetch

Specification coverage: [`wtree fetch` and drift-aware status](../spec/full-multi-repository-experience.md#62-wtree-fetch-and-drift-aware-status)

Scope:

- Add the fixed fetch command surface, configured-ref planner, parent-first
  coordinator, per-repository continuation/result, dry-run advertisement, and
  verbose/human/JSON rendering.
- Revalidate identity/path/upstream before each fetch and use only the declared
  remote/ref under the hardened Git environment.
- Prove local branch refs, HEAD, index, worktree files, config, workspace state,
  registry, journals, and recovery remain unchanged; only remote-tracking refs
  may change during execution.
- Document that fetch is explicit, non-transactional Git metadata mutation and
  that status remains network-free.

Test-first slices:

1. Fetch changed refs across a nested forest in order while preserving local
   branches/worktrees and making later status ahead/behind facts observable.
2. Continue after missing ref, transport, authentication-like, and malformed
   upstream failures with bounded redacted aggregate results.
3. Prove dry-run remote observation changes no refs, cancellation starts no
   later fetches, partial workspaces cover present repositories only, and JSON
   output is deterministic and silent on stderr.

Verification:

- `go test ./internal/git ./internal/service ./internal/cli -run 'Test.*Fetch' -count=1`
- Run the global milestone commands.

Exit criteria: Fetch refreshes only declared remote-tracking facts, exposes all
partial outcomes, and cannot be mistaken for branch movement or a transaction.

### [x] M08 — Add compatible manifest and disk drift to status

Specification coverage: [`wtree fetch` and drift-aware status](../spec/full-multi-repository-experience.md#62-wtree-fetch-and-drift-aware-status); [status compatibility](../spec/full-multi-repository-experience.md#104-status-exit-codes-and-output-compatibility)

Scope:

- Consume the local-only portion of the shared drift snapshot and add
  manifest/state/disk repository-set and expected/actual identity, mount,
  branch, and commit facts.
- Preserve existing `WorkspaceStatus`/`RepositoryStatus` JSON fields and
  meanings, status exit semantics, upstream facts, and the default human table
  for an existing synchronized workspace.
- Preserve `unknownRepository`, `status: "unknown-repository"`, and human
  `UPSTREAM: n/a` on identity failures while adding `identityMismatch` and the
  drift entry.
- Render additive summaries only when new drift exists; preserve current
  partial/missing behavior without new workspace kinds.
- Update status help/how-to/tutorial text to describe last-fetched upstream and
  local manifest drift precisely.

Test-first slices:

1. Report declared-but-absent, state/disk-but-not-manifest, retained-unmanaged,
   identity, mount, branch, and HEAD drift deterministically without network
   calls or mutation.
2. Decode old and new JSON shapes and compare synchronized human golden output
   byte-for-byte; compare identity-mismatch human output and all pre-existing
   JSON fields byte-for-byte; prove findings still return success when
   observation succeeds.
3. Preserve partial, stale, detached, unknown, missing, ahead/behind, writer
   failure, cancellation, nested-child dirt filtering, and no-fetch regressions.

Verification:

- `go test ./internal/service ./internal/cli -run 'Test(Status|ExecuteStatus|RenderWorkspaceStatus)' -count=1`
- Run the global milestone commands.

Exit criteria: Status supplies the P1 local drift view without a schema break,
implicit fetch, changed success semantics, or changed synchronized human table.

### [x] M09 — Deliver non-publishing push readiness

Specification coverage: [push readiness](../spec/full-multi-repository-experience.md#63-push-readiness)

Scope:

- Add the exact `push` readiness command, remote/local observation service,
  deterministic repository findings, human/JSON rendering, and stable aggregate
  success/failure behavior.
- Check cleanliness, attachment, configured upstream, ahead/behind/divergence,
  exact advertised tip, repository identity roots, metadata commit reachability,
  and complete-workspace presence without updating refs or state.
- Separate operational transport/authentication failures from readiness
  findings and redact all remote diagnostics.
- Document explicitly that the command never pushes or creates refs/tags and
  that publication remains a manual/future workflow.

Test-first slices:

1. Approve a complete clean nested workspace whose exact local tips are
   advertised and whose identity roots/metadata commits are reachable.
2. Produce stable findings for dirty, ahead, behind, diverged, detached,
   missing-upstream, missing/partial checkout, unpublished tip, and identity/
   metadata mismatch cases.
3. Prove no `git push`, tag/ref creation, fetch, state write, lock, or recovery
   path occurs; cover bounded remote failure, cancellation, JSON silence,
   deterministic order, and writer errors.

Verification:

- `go test ./internal/git ./internal/service ./internal/cli ./cmd/wtree -run 'Test.*Push' -count=1`
- Run the global milestone commands.

Exit criteria: `wtree push` answers only whether the complete workspace is
already safely available upstream, with no remote mutation or false readiness.

### [ ] M10 — Close P0/P1 traceability and cross-platform acceptance

Specification coverage: [delivery order](../spec/full-multi-repository-experience.md#11-delivery-order-and-dependency-gates); [required verification](../spec/full-multi-repository-experience.md#12-required-verification); [explicit non-goals](../spec/full-multi-repository-experience.md#13-explicit-non-goals)

Scope:

- Add a requirement-to-test traceability table for every delivered P0/P1
  contract and record later-plan dependencies for every deferred P2-P4/P3 item.
- Run a hermetic end-to-end nested-forest flow: init/clone, remote changes,
  update dry-run/update, doctor/status, exec, fetch/status refresh, push
  readiness, added repository, and retained removal.
- Audit CLI help, README, installed how-to, tutorial, troubleshooting, schema
  versions, error/exit mapping, redaction, recovery visibility, release layout,
  and repository document lifecycle metadata.
- Obtain final independent review and cross-platform CI evidence without
  committing, publishing, releasing, or editing historical run ledgers.

Test-first slices:

1. Add the end-to-end fixture and first prove it fails at the missing integrated
   behavior, then make the complete supported composition loop pass.
2. Exercise aggregate failure, update rollback/incomplete recovery, dirty/
   divergent/missing/retained states, JSON determinism, no implicit shell/
   hook/push/tag/branch creation/deletion, and credential redaction together.
3. Map every in-scope specification statement to focused/integration/CI
   evidence and every deferred statement to its explicit compatibility gate.

Verification:

- Run all global and final milestone commands.
- Verify a GitHub Actions run passes Ubuntu, macOS, and Windows jobs from
  [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml).
- Manually audit the traceability map, all milestone checkboxes, execution log,
  durable ledger terminal state, and lifecycle/status-overview consistency.

Exit criteria: All M00-M10 behavior is independently approved and verified on
the supported CI matrix; documentation and traceability are complete; the
plan may become `implemented`, while the source specification remains
`planned` because its explicitly deferred capabilities still require later
specifications and plans.

## Execution log

Append entries during execution; do not rewrite earlier evidence. Detailed
packets, findings, attempts, checkpoints, and resume instructions belong only
in `docs/ai/runs/full-multi-repository-experience.md`.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-24 | M00 | Focused aggregate/Git/process tests, `make fmt-check`, `go vet ./...`, full non-race tests (service 760.680s), full race tests (service 644.472s), and `go build ./cmd/wtree` passed; bounded pre-truncation redaction uses exact overlap reconstruction and bounded full-stream evidence | Normal reviewer approved R1-R5 and the revised R3 contract after final escalation remediation; no material findings | Not authorized |
| 2026-08-24 | M01 | Focused drift/classifier normal and race tests, config/domain/Git, `make fmt-check`, `go vet ./...`, both diff checks, full non-race tests (service 731.445s), full race tests (service 792.566s), and `go build ./cmd/wtree` passed; immutable collection, exact authority/race handling, upstream evidence, retained-history correlation, normalized diagnostics, and current/candidate membership classification are covered | Normal reviewer approved R1-R4 and the user-reset bounded R3 successive-update remediation with no material findings | Not authorized |
| 2026-08-24 | M02 | Focused update-plan/collector/CLI/Git normal and race tests, `make fmt-check`, `go vet ./...`, both diff checks, full non-race tests (service 586.938s), full race tests (service 707.081s), and `go build ./cmd/wtree` passed; private immutable plan data, structural source authority/redaction, deterministic aggregate failures, cancellation, decoded dry-run output, writer failures, and exhaustive zero mutation are covered | Normal reviewer approved R1-R3 and the production CLI stored/override local/HTTP source matrix with no material findings | Not authorized |
| 2026-08-25 | M03 | Focused executor/recovery/Git normal and race tests, `make fmt-check`, `go vet ./...`, both diff checks, one post-approval full non-race test (service 676.069s), one full race test (service 721.297s), and `go build ./cmd/wtree` passed; strict journals/backups, execution-time configured-ref updates, verified additions, retained facts, exact rollback/recovery ownership, terminal cleanup, and strict receipt decoding are covered | The user-reset lean review approved the bounded strict-receipt correction with no material findings; full gates then passed once on the approved tree | Not authorized |
| 2026-08-26 | M04 | Focused update/publication and doctor-authority normal/race tests, `make fmt-check`, `go vet ./...`, both diff checks, full non-race tests (service 856.213s), and `go build ./cmd/wtree` passed. Two exact full-race attempts reached the 15-minute service timeout only in pre-existing M07 status/forest Git-test contention after every earlier package passed; focused cumulative M04 race coverage passed and no race diagnostic appeared. Transactional publication, resolver-backed mutation authority, deterministic result/output contracts, and the final under-lock `doctor --fix` journal refusal are covered | Independent normal reviewer approved the user-reset bounded doctor-authority correction and cumulative M04 scope with no material findings | Not authorized |
| 2026-08-26 | M05 | Focused doctor/shared-drift and committed-ignore normal/race tests, `make fmt-check`, `go vet ./...`, both diff checks, full non-race tests (service 769.550s), full race tests (service 811.303s), and `go build ./cmd/wtree` passed. Doctor projects the immutable shared drift snapshot, diagnoses tracked manifest/state/disk/identity/upstream/committed-ignore/retained/journal/recovery evidence, preserves safe repairs and exact cancellation, and makes committed ignore observation without temporary filesystem state | Independent normal reviewer approved the final escalation remediation for R4/R7 and all resolved/original M05 scope with no material findings | Not authorized |
| 2026-08-26 | M06 | Focused Exec normal/race tests, `make fmt-check`, `go vet ./...`, both diff checks, and `go build ./cmd/wtree` passed. The single post-reset full normal/race runs reached the 15-minute service timeout only in pre-existing M07 forest/lock real-Git tests after every earlier package passed; no Exec assertion or race diagnostic appeared. Direct argv/no-shell execution, all-repository preflight, exact v1 evidence, deterministic order/environment, bounded redacted output, continuation/cancellation, partial workspaces, human streaming/writer stop, and exact observation cancellation precedence are covered | Independent normal reviewer approved the user-reset bounded R6 cancellation-precedence fix and all resolved/original M06 scope with no material findings | Not authorized |
| 2026-08-27 | M07 | Focused Fetch normal/race tests, `make fmt-check`, `go vet ./...`, both diff checks, and `go build ./cmd/wtree` passed. Main and implementer full normal/race commands reached only the 15-minute service timeout in pre-existing forest/init/doctor real-Git harness tests after every earlier package passed; no Fetch assertion or race diagnostic appeared. Exact configured-ref mutation, ParentFirst continuation/streaming, dry-run, cancellation, partial workspace, deterministic output, actual authority inventory, and network-free status compatibility are covered | Independent reviewer approved the final escalation one-pass ParentFirst remediation and all resolved/original M07 scope with no material findings | Not authorized |
| 2026-08-27 | M08 | Focused status normal/race tests, `make fmt-check`, `go vet ./...`, both diff checks, full non-race tests (service 875.333s), full race tests (service 875.553s, no race report), and `go build ./cmd/wtree` passed. Shared local-only drift projection, additive compatible JSON/human output, authority/operation evidence, strict selected/default workspace isolation, no-fetch/zero-mutation behavior, and legacy status semantics are covered | Independent reviewer approved the final escalation selected/default identity-authority correction and all resolved/original M08 scope with no material findings | Not authorized |
| 2026-08-27 | M09 | Focused Push normal/race tests, `make fmt-check`, `go vet ./...`, both diff checks, and `go build ./cmd/wtree` passed. The exact post-review full normal/race commands reached only the 15-minute service timeout in unrelated doctor/planner and established M07 parallel real-Git harness rows after every other package passed; no Push assertion or race diagnostic appeared. Immutable configured-upstream authority, no-hook fact collection, exact finding/output contracts, complete three-repository behavior, real resolver-backed registry/default/named/reconciliation/journal-backup/recovery coexistence, recursive zero mutation, cancellation/writer semantics, and root one-document redaction are covered | Independent reviewer approved the user-reset resolver-backed R5 authority-coexistence remediation and all resolved/original M09 scope with no material findings | Not authorized |
