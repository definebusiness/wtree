# Immutable release lock manifests implementation plan

Status: implemented
Source specification: [Immutable release lock manifests specification](../spec/release-lock-manifests.md)
Source idea: [Immutable release lock manifests](../ideas/release-lock-manifests.md)
Implementation context: [Immutable release lock manifests implementation context](release-lock-manifests-context.md)
Related contracts: [Portable manifest clone specification](../spec/portable-manifest-clone.md); [Logical project root and repository forest specification](../spec/logical-project-root-base-repository.md); [Local and shared workspace lifecycle hooks specification](../spec/local-workspace-lifecycle-hooks.md)
Source of truth: [`internal/config/portable_manifest.go`](../../internal/config/portable_manifest.go); [`internal/config/hooks.go`](../../internal/config/hooks.go); [`internal/git/adapter.go`](../../internal/git/adapter.go); [`internal/service/clone_plan.go`](../../internal/service/clone_plan.go); [`internal/service/clone_execute.go`](../../internal/service/clone_execute.go); [`internal/service/hook_plan.go`](../../internal/service/hook_plan.go); [`internal/service/hook_runner.go`](../../internal/service/hook_runner.go); [`internal/cli/root.go`](../../internal/cli/root.go); [`tutorial/README.md`](../../tutorial/README.md)
Delivery style: test-first, one independently reviewed milestone at a time; no separate release verification command, post-materialize hook, credential management, lock-aware update, built-in Git publication, or release orchestration

## Execution contract for Codex

When asked to run this plan, follow
[`docs/ai/milestone-supervision.md`](../ai/milestone-supervision.md) and maintain
the durable ledger at `docs/ai/runs/release-lock-manifests.md`. Continue through
all milestones without waiting at approved handoffs unless the supervision
process records a genuine blocker.

For each milestone:

1. Reconcile the specification, plan, implementation context, ledger,
   source-of-truth files, and current worktree.
2. Record the milestone's complete scope, test-first slices, exit criteria, and
   verification commands in the ledger.
3. Dispatch the complete implementation packet to the required implementer.
4. Require focused RED, GREEN, and REFACTOR evidence before review.
5. Submit the complete milestone to the required read-only reviewer, remediate
   all material findings under the defined escalation limit, and rerun the
   milestone verification as the main agent.
6. Only after approval and verification, update affected documentation and
   lifecycle evidence, mark the milestone complete, and continue immediately.

## Product boundary

This delivery adds two commands:

```text
wtree release lock <release-name> [workspace]
wtree release materialize <lock-file>
```

The feature freezes and reconstructs source. It does not attempt to become a
release server. CI remains responsible for building, testing, packaging,
signing, attestation, publication, deployment, promotion, and notification.

Fixed decisions:

- `project.wtree.lock.yml` is a small deterministic revision overlay containing
  the release name, project ID, exact portable-manifest digest, and exact
  non-base commits.
- The caller-provided base checkout anchors the base revision; the lock never
  contains a base revision.
- Generation observes the current clean local workspace without fetching or
  claiming an atomic cross-repository snapshot.
- A clean tracked lock from the previous release is replaced normally.
  `--force` is needed only to overwrite an untracked or locally modified lock.
- Local-v3 `post-release` runs after lock publication but before the caller's
  base commit and tag. It is local-only, sequential, trusted, and must be
  idempotent.
- Materialization starts from a clean CI-provided base checkout, fetches every
  advertised branch and tag ref, and creates exact detached non-base checkouts.
- Authentication is delegated to standard noninteractive Git mechanisms. The
  minimum supported paths are SSH agent, askpass, and configured credential
  helpers. `wtree` stores no credentials and offers no credential flags.
- Materialization reuses established clone staging, verification, publication,
  rollback, recovery, path, ignore, and registry machinery.
- Successful materialization is the verification boundary. There is no
  separate `release verify` command.
- CI runs subsequent commands explicitly, including through `wtree exec` when
  useful. There is no `post-materialize` hook.
- Human and JSON output reuse existing conventions and remain deterministic,
  bounded, and secret-safe.
- Full documentation and an executable offline release tutorial are required.

## Architecture

```text
registered development workspace
        |
        +-- release lock --> canonical tracked lock
        |                       |
        |                       +--> local post-release hooks
        |                            (example: child tags)
        |
CI checkout of base commit/tag
        + exact manifest + lock
        + Git-owned noninteractive authentication
        |
        +-- release materialize
                +--> reuse clone staging/acquisition
                +--> exact detached child commits
                +--> local state and registry
                +--> explicit later CI commands
```

Ownership:

- `internal/config` owns the strict lock schema and local-v3 event extension.
- `internal/git` owns exact advertised-ref acquisition and a purpose-specific
  authentication environment that permits normal Git authentication without
  persisting or rendering secrets.
- `internal/service` owns lock generation, hook coordination, and
  materialization by composing existing clone safety primitives.
- `internal/cli` owns the two commands and existing-style human/JSON output.
- `tutorial` owns the complete release and CI journey.

## Definition of done

- Focused tests precede implementation and cover the behavior named by each
  milestone without duplicating already proven clone safety matrices.
- Tests use temporary repositories, local bare remotes, and fake authentication
  helpers or wrappers; they require no network service or real credential.
- Lock parsing is strict and serialization deterministic.
- Authentication remains noninteractive and is absent from portable data,
  command results, errors, logs, and persisted state.
- Existing Git hooks and recursive submodules never run implicitly.
- Existing init, clone, lifecycle-hook, workspace, and aggregate-command tests
  remain green.
- README, help/how-to, troubleshooting, specifications, traceability, status
  overview, and the executable tutorial agree with delivered behavior.
- Run the repository's applicable required gates:

  ```text
  ./scripts/docs-check.sh
  make check-local
  go test ./... -count=1
  go test -race ./... -count=1
  go vet ./...
  make fmt-check
  make build
  make tutorial-test
  make release-test
  git diff --check
  ```

- Supported Linux, macOS, and native Windows CI gates pass, and independent
  review has no unresolved material finding.

## Risks

- **Private repository authentication:** the current adapter removes standard
  authentication environment. Introduce a narrow network-operation boundary
  that delegates to Git while retaining noninteractive behavior and preventing
  secret output or persistence. Do not build a credential manager.
- **Unavailable commits:** materialization fetches advertised heads and tags,
  then requires the exact object. Documentation must require publishing child
  commits and tags before the base release tag.
- **Partial publication:** reuse existing staging, ownership, rollback, and
  recovery mechanisms; validate every child before publishing the first mount.
- **Hook side effects:** hooks cannot be rolled back. The example accepts an
  existing tag only at the expected commit and fails on collisions.
- **Detached workspaces:** release workspaces are build inputs. Existing
  branch-oriented commands retain their explicit success or rejection rules.

## Milestones

### [x] M00 — Generate release locks and run local post-release hooks

Specification coverage: [§3–§5](../spec/release-lock-manifests.md#3-release-lock-format) and [§7](../spec/release-lock-manifests.md#7-cli-and-output).

Scope:

- Add strict release-lock types, decoding, validation, canonical encoding, and
  exact portable-manifest digest binding under `internal/config`.
- Implement `release lock` planning and execution from one complete clean
  workspace without remote observation.
- Create or normally replace a clean tracked prior lock; protect untracked or
  locally modified locks with `--force`; support identical reruns and dry-run.
- Extend local configuration version three with `post-release`, rejecting it
  from v2, portable, and shared sources.
- Reuse existing hook planning and direct-process execution with authoritative
  release name and repository HEAD environment.
- Add the `release` group and `lock` CLI with existing-style human/JSON output,
  `--no-hooks`, and clear core-success/hook-failure reporting.

Test-first slices:

1. Round-trip a golden multi-repository lock; reject malformed schema, wrong
   project/digest/repository sets, invalid commits, and blank/control-bearing
   names; prove stable lexical output.
2. Generate from attached and detached workspaces; reject incomplete, dirty,
   wrong-identity, and mismounted inputs without any fetch.
3. Cover absent, identical, clean-tracked replacement, protected untracked or
   locally modified replacement, `--force`, dry-run, and unsafe target types.
4. Prove local-v3 event acceptance and other-source rejection, exact reserved
   environment replacement, ordered execution, bypass, idempotent rerun, and
   retained lock/prior effects after hook failure.
5. Cover concise human and versioned JSON results, invalid arguments,
   cancellation, bounded output, and secret-safe failures.

Verification:

- `go test ./internal/config -run 'ReleaseLock|Hook' -count=1`
- `go test ./internal/service ./internal/cli -run 'ReleaseLock|PostRelease' -race -count=1`
- Applicable definition-of-done commands.

Exit criteria: a user can freeze a local workspace into the canonical lock and
run trusted, idempotent non-base tagging hooks without implicit Git publication.

### [x] M01 — Materialize exact children with Git-owned authentication

Specification coverage: [§6](../spec/release-lock-manifests.md#6-ci-materialization) and [§7](../spec/release-lock-manifests.md#7-cli-and-output).

Scope:

- Add narrow Git operations that fetch explicit advertised branch/tag refspecs,
  validate an exact object as a commit, and check it out detached.
- Define a purpose-specific network authentication environment that supports
  SSH agents, askpass helpers and their inherited secret environment, and
  configured credential helpers while keeping prompts disabled.
- Ensure authentication data is never copied into plans, results, diagnostics,
  durable state, recovery data, or portable files.
- Build materialization around a validated caller-provided base checkout and
  reuse clone staging, forest ordering, identity, mount, ignore, publication,
  rollback, recovery, registry, and state machinery.
- Fetch and validate every child in private staging before publishing any final
  child mount. Never run Git checkout hooks, recursive submodules, portable
  lifecycle hooks, or a post-materialize hook.
- Add `release materialize` human/JSON/dry-run/verbose behavior. Success reports
  the release name and exact expected/observed repository commits.

Test-first slices:

1. Materialize base-only, child, nested, and sibling fixtures from a clean base
   checkout and local bare remotes; assert exact detached commits, unchanged
   base, complete state, registry, and immediate `wtree exec` usability.
2. Cover branch-reachable, tag-only, and unavailable commits without direct
   object-ID fetch, branch-tip substitution, or remote-HEAD fallback.
3. Prove SSH-agent, askpass, helper-required secret environment, and configured
   credential-helper information reaches Git noninteractively; prove missing
   credentials fail without prompting.
4. Inject credential-shaped values into URLs, helper diagnostics, environment,
   and failures and prove they never enter results, logs, recovery, or state.
5. Cover principal manifest/digest/base/path/destination/registry conflicts and
   representative cancellation/publication failures, relying on existing clone
   tests for unchanged generic safety behavior.
6. Prove materialization runs no lifecycle hook and that explicit subsequent
   CI commands and `wtree exec` operate on the registered exact workspace.

Verification:

- `go test ./internal/git -run 'Release|Authentication|Advertised|Detached' -count=1`
- `go test ./internal/service ./internal/cli ./cmd/wtree -run 'ReleaseMaterialize|Authentication|ReleaseCommand' -race -count=1`
- Platform compile checks and applicable definition-of-done commands.

Exit criteria: a normal CI checkout can noninteractively authenticate to private
Git remotes, reconstruct all advertised locked children exactly, and proceed to
explicit pipeline commands without credential leakage or implicit code execution.

### [x] M02 — Complete the everyday release workflow and documentation

Specification coverage: [§8](../spec/release-lock-manifests.md#8-documentation-and-tutorial), [§9](../spec/release-lock-manifests.md#9-acceptance-criteria), and [§10](../spec/release-lock-manifests.md#10-non-goals).

Scope:

- Complete root/command help, `--how-to`, human/JSON compatibility, and
  process-level behavior for both commands.
- Add README guidance that presents the feature as reproducible source
  composition, not a release-management platform.
- Add troubleshooting for authentication, unavailable commits, manifest
  mismatch, dirty base, occupied destinations, tag collisions, hook failure,
  partial cleanup, and detached workspace restrictions.
- Add `tutorial/RELEASES.md` with a base and at least two non-base repositories,
  linked from the tutorial index and all-command guide.
- Supply one idempotent `tag-wtree-release` example and one local-v3 declaration
  per non-base repository.
- Demonstrate lock dry-run and creation, child tags, lock review/add/commit, base
  tag creation, child-first publication, clean CI checkout, authenticated exact
  materialization, and explicit later build/test steps including `wtree exec`.
- Update current specifications, traceability, and lifecycle overview without
  rewriting historical run evidence.

Test-first slices:

1. Run the complete offline tutorial through lock creation, child tagging, base
   lock commit/tag, fresh base checkout, materialization, and explicit CI work.
2. Prove matching tag reruns succeed, collisions fail without moving tags, and
   the normal next release replaces the clean tracked prior lock without
   `--force`.
3. Exercise fake SSH-agent/askpass authentication and prove credential canaries
   remain absent from all output and durable files.
4. Remove locked reachability, prove materialization fails without publishing a
   complete workspace, restore it, and prove a rerun succeeds.
5. Assert documentation never promises atomic release publication, credential
   management, a verification command, or a post-materialize hook.

Verification:

- `./scripts/docs-check.sh`
- `go test ./internal/cli ./cmd/wtree -run 'Release|Help|HowTo|Tutorial' -count=1`
- `make tutorial-test`
- Every definition-of-done command and matching supported-platform CI gates.

Exit criteria: a user can follow one concise documented workflow from tested
local commits through non-base tagging and a base lock commit/tag to an exact
private-repository CI workspace, then perform ordinary CI release steps.

## Execution log

- M00 approved and verified on 2026-09-04. Delivered strict canonical release
  locks, clean local workspace observation, protected atomic replacement,
  local-v3 `post-release` hooks, and the `release lock` CLI. Focused checks,
  documentation/build/format/vet gates, local integration, tutorial/release
  gates, the full test inventory, and the full race inventory pass; the
  durable review and remediation evidence is recorded in the
  [run ledger](../ai/runs/release-lock-manifests.md).
- M01 approved and verified on 2026-09-04. Delivered advertised-ref-only exact
  detached materialization, noninteractive Git-owned authentication, complete
  private staging before publication, authority-bound rollback and recovery,
  registered workspace reuse, and human/JSON `release materialize` behavior.
  Focused and supported-platform checks, documentation/local/build gates, full
  normal and race inventories, and tutorial/release integration gates pass;
  detailed review, remediation, and constrained-storage evidence remains in the
  [run ledger](../ai/runs/release-lock-manifests.md).
- M02 approved and verified on 2026-09-04. Delivered complete release help and
  how-to guidance, README and troubleshooting boundaries, and a hermetic
  two-child tutorial from idempotent local tagging and child-first publication
  through exact authenticated CI materialization and explicit `wtree exec`.
  Collision, unavailable-revision cleanup, credential-canary, exact-revision,
  lifecycle, platform-compile, full normal, and full race evidence passes; the
  detailed review and remediation history is recorded in the
  [run ledger](../ai/runs/release-lock-manifests.md).
