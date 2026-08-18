# Portable manifest v2 base-repository format implementation plan

Status: implemented
Source specification: [Portable manifest v2 base-repository format specification](../spec/portable-manifest-v2-base-repository-format.md)
Related idea: [Logical project roots with a designated base repository](../ideas/logical-project-root-base-repository.md)
Source of truth: [portable manifest v2 base-repository format specification §§1–7](../spec/portable-manifest-v2-base-repository-format.md); [portable manifest clone specification §§2–5, 9–10](../spec/portable-manifest-clone.md); [`internal/config/portable_manifest.go`](../../internal/config/portable_manifest.go); [`internal/service/init.go`](../../internal/service/init.go); [`internal/service/clone_plan.go`](../../internal/service/clone_plan.go); [`internal/service/clone_execute.go`](../../internal/service/clone_execute.go); [`internal/cli/root.go`](../../internal/cli/root.go)
Delivery style: test-first, one reviewed milestone at a time; breaking portable-manifest format cutover only; no compatibility mode, dependency additions, commits, pushes, publication, or release

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes those decisions below.

For each unchecked milestone, in order:

1. Read this plan, the relevant source-of-truth sections, the durable run
   ledger at `docs/ai/runs/portable-manifest-v2-base-repository-format.md`,
   and the current worktree. Create the ledger before the first dispatch. On
   resumption, reconcile the plan, ledger, evidence, and worktree, then append
   a reconciliation checkpoint before dispatching work.
2. Derive and record in that ledger a complete checklist covering every scope
   item, test-first slice, documentation item, exit criterion, and command.
3. Give the complete initial packet to the normal `implementer`. For
   remediation, use `implementer` when the recorded attempt count is `0` or
   `1`, and `escalation-implementer` only when it is `2`. Require RED → GREEN
   → REFACTOR evidence, changed files, command results, and unresolved
   concerns.
4. Treat partial work as progress only. Do not request review or change the
   remediation count until every checklist item has complete evidence.
5. Send each complete submission to the read-only `reviewer`, which inspects
   the shared filesystem and all applicable source-of-truth, safety,
   portability, scope, and test-quality requirements.
6. Record every material reviewer finding with a stable ID and return the
   complete unresolved set in one remediation packet. Apply the exact
   three-rejected-complete-remediation limit in
   [`milestone-supervision.md`](../ai/milestone-supervision.md); do not use an
   escalation reviewer as a routine second review.
7. After reviewer approval, run the milestone commands as the main agent,
   update the relevant documentation and plan checkbox, append the concise
   execution-log row, then create the next milestone ledger snapshot and
   dispatch its initial packet immediately.

Do not stop for ordinary test failures, review findings, partial submissions,
or approved milestones. Preserve unrelated user changes; do not use destructive
cleanup commands; commit only when separately authorized. A final response is
permitted only by the ledger gate in `milestone-supervision.md`.

## Fixed implementation decisions

- `project.wtree.yml` moves directly and exclusively to schema `version: 2`.
  All v1 and other unsupported versions fail before clone planning or any
  mutation with a diagnostic that says the logical-root manifest format is
  required. There is no translation, dual-schema decoding, opt-in legacy mode,
  or migration command.
- `PortableManifestVersion` becomes `2`. `PortableProject` gains the required
  YAML/JSON field `base_repository`, serialized between `name` and the
  repository map. The field uses the existing portable-ID grammar and must
  name a repository entry.
- The only accepted v2 topology remains the existing one-root tree: exactly
  one parentless repository, its `mount` is `.`, and
  `project.base_repository` is exactly that repository. Every other repository
  has an existing immediate parent and a parent-relative mount. Keep all
  existing path-overlap, cycle, clone/upstream, identity, and mount validation.
- Canonical output remains byte deterministic: repository IDs sort lexically,
  initial-commit arrays sort lexically, and the new base field has one fixed
  placement. Canonicalization may sort input commit arrays before validation;
  it must not silently repair any other malformed schema field.
- The local `.wtree.yml` schema remains version 1 and its
  `manifest.path: project.wtree.yml` location is unchanged. Registry,
  workspace, recovery, domain, and clone-plan schema versions stay unchanged.
- `wtree init` continues to discover the current root repository ID and writes
  it to `project.base_repository`. It continues to publish both metadata files
  in that checkout, retain `/.wtree.yml` ignore management, and never ignores
  the portable manifest. The deterministic project-ID calculation includes the
  canonical v2 manifest facts, including the discovered base ID.
- `wtree clone` continues to use the sole root as its selected checkout,
  validates the v2 manifest before destination, registry, remote, staging, or
  other mutation planning, and preserves byte-identical verification of the
  root checkout's tracked `project.wtree.yml`.
- Do not broaden discovery, workspace, import, resolver, project-registry,
  recovery, clone destination, or ignore-management semantics. In particular,
  sibling repositories, a plain logical root, non-`.` top-level mounts, and
  base-relative metadata relocation remain deferred.
- Existing tests and helpers that author manifests must be converted to valid
  v2 fixtures. Tests specifically exercising rejection of old/invalid input
  may retain literal v1 YAML. No repository-wide fixture rewrite outside the
  portable-manifest consumers is authorized unless compilation or an exact
  assertion requires it.

## Stable contracts to establish early

| Contract | Owner and consumers | Observable invariant and enforcement |
|---|---|---|
| Portable v2 model | `internal/config`; consumed by init, clone planning/execution, CLI rendering, and tests | A decoded manifest has a valid explicit base repository and the scoped one-root topology. `portable_manifest_test.go` proves decode, validation, canonical output, and v1 rejection. |
| Canonical portable bytes | `internal/config`; consumed by init identity calculation and clone tracked-file verification | Equal valid manifest values encode identically, including lexical repository/commit ordering and `base_repository`. Codec and init tests enforce it. |
| Init/clone boundary | `internal/service`; consumed by `internal/cli` | Init authors a valid v2 root manifest; clone accepts only it and verifies the exact tracked bytes after cloning the root. Service and CLI integration tests enforce it. |

`internal/config` must remain dependency-free from service, Git, filesystem,
and CLI packages. `internal/service` consumes the model but must not create a
second interpretation of base-repository topology; `internal/cli` only renders
service-owned results.

```text
CLI → service init / clone planner-executor → config portable-manifest contract
                 ↓                                  ↑
              Git and filesystem adapters ──────────┘
```

## Architecture and dependency boundaries

- Keep schema fields, strict YAML decoding, validation, canonical YAML-node
  ordering, and error text ownership in `internal/config/portable_manifest.go`.
- Keep root discovery and portable-manifest construction in `internal/service/init.go`;
  it supplies the discovered root ID rather than hard-coding `root`.
- Keep clone plan reconstruction and byte comparisons in the existing clone
  planner/executor path. Reconstructed `PortableManifest` values must include
  `base_repository` so plan self-validation compares like-for-like.
- Update only user-facing documentation that identifies the portable schema
  version or shows its YAML. Do not rewrite historical ideas or the already
  implemented v1 clone plan; add a clear v2 relationship where a current
  contract needs it.

## Global definition of done

Every approved milestone has complete RED → GREEN → REFACTOR evidence for its
changed behavior, focused hermetic tests for success and rejection/no-mutation
paths, independent reviewer approval with no unresolved material findings, and
the following main-agent checks (unless a milestone explicitly names a strict
subset before later integration exists):

- `gofmt -w` only on files changed by the authorized milestone, followed by
  `make fmt-check`.
- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`
- `make build`
- `make release-test`
- `git diff --check`

Tests must use temporary Git repositories, local remotes, temporary files, or
existing fakes; they must not require credentials or network access. No change
may weaken preflight validation, canonical byte checks, transaction behavior,
or redaction. The final milestone also updates current user-facing schema
examples and confirms all altered manifest fixtures are v2 except intentional
v1 rejection inputs.

## Milestones

### [x] M00 — Establish the strict portable-manifest v2 contract

Specification coverage: [§§2–4](../spec/portable-manifest-v2-base-repository-format.md#2-compatibility-boundary), [§§2–4](../spec/portable-manifest-v2-base-repository-format.md#3-version-2-portable-manifest-schema), and [§4](../spec/portable-manifest-v2-base-repository-format.md#4-scoped-topology-invariants).

Scope:

- Advance the portable-manifest version constant and model with the required
  `project.base_repository` field; retain local-config version 1 unchanged.
- Make strict decoding, `Validate`, and canonical serialization require the
  base ID, verify that it names the sole root, and retain all prior hierarchy
  and mount protections.
- Produce a direct, non-redacting diagnostic for all unsupported versions that
  identifies the logical-root manifest format requirement.
- Convert config-level canonical fixtures to v2 and preserve lexical output
  ordering and sorted initial commits.

Test-first slices:

1. Add a canonical v2 YAML fixture whose decode and repeated marshal operations
   preserve `base_repository` and exact bytes.
2. Add table-driven rejection tests for v1, missing base ID, invalid-ID base,
   unknown base ID, a non-root base, multiple roots, and a root mount other
   than `.`; assert validation occurs without I/O or mutation.
3. Prove nested valid repositories remain accepted only with immediate-parent
   relative mounts and that malformed prior v1 invariants remain rejected.

Verification:

- `go test ./internal/config -run 'PortableManifest|ValidatePortable' -count=1`
- `go test ./internal/config -race -count=1`
- Global definition-of-done commands.

Exit criteria: config is the sole owner of a strictly decoded, canonical v2
manifest contract; no v1 portable document can reach a consumer as valid.

### [x] M01 — Author v2 manifests from init without moving local metadata

Specification coverage: [§§2, 5–6](../spec/portable-manifest-v2-base-repository-format.md#2-compatibility-boundary) and [§5](../spec/portable-manifest-v2-base-repository-format.md#5-authoring-and-clone-requirements).

Scope:

- Populate the v2 base ID from init's discovered sole root before validation,
  deterministic ID calculation, dry-run rendering, and transactional writing.
- Update all init test helpers and expected YAML to v2; assert the local
  `.wtree.yml` still has version 1 and unchanged `manifest.path` semantics.
- Preserve existing init preflight, lock ordering, rollback, ignore ownership,
  and no-stage/no-commit behavior; this milestone must not change their scope.

Test-first slices:

1. Extend a successful root-plus-nested init test to assert `version: 2`,
   `base_repository: root` (or the discovered root ID), byte-stable re-marshal,
   and unchanged local manifest metadata.
2. Add a deterministic-ID regression that proves the v2 authored facts,
   including base ID, feed the same planned/project identity path and do not
   introduce machine-local state.
3. Exercise dry-run and a failed preflight/transactional fixture to prove no
   v1 output or partial metadata files are published.

Verification:

- `go test ./internal/service -run 'Init.*Manifest|Initializer|Deterministic' -count=1`
- `go test ./internal/cli -run 'Init' -count=1`
- Global definition-of-done commands.

Exit criteria: every manifest authored by init is valid v2 with an explicit
discovered base repository, while local configuration and existing init safety
semantics remain unchanged.

### [x] M02 — Consume and verify scoped v2 manifests through clone

Specification coverage: [§§2, 4–6](../spec/portable-manifest-v2-base-repository-format.md#2-compatibility-boundary), [§5](../spec/portable-manifest-v2-base-repository-format.md#5-authoring-and-clone-requirements), and [§6](../spec/portable-manifest-v2-base-repository-format.md#6-required-verification).

Scope:

- Update clone plan validation/reconstruction, service fixtures, execution
  fixtures, and CLI fixtures to construct and expect v2 manifests with the
  same root checkout and parent-first actions.
- Prove clone rejects v1 and malformed v2 manifests at decode/preflight before
  remote planning or destination mutation, while valid scoped v2 clone plans
  and executes unchanged.
- Preserve tracked-root `project.wtree.yml` byte identity checking after root
  checkout; a syntactically valid but byte-different served manifest must still
  fail and clean up as current behavior requires.
- Update current README/schema examples or portable-manifest user guidance to
  version 2 and `base_repository`; do not alter historical idea documents or
  the prior plan's execution history.

Test-first slices:

1. Convert normal clone planner/executor/CLI fixtures to v2 and prove valid
   root-plus-nested clone planning retains parent-first paths, read-only
   planning, and existing action ordering.
2. Feed literal v1, missing/unknown base, and non-root-base manifests through
   the service and CLI entry points; assert a logical-root-format diagnostic,
   no remote calls where test seams can observe them, and no destination or
   registry mutation.
3. Run a real local-Git clone using a tracked byte-identical v2 manifest, then
   a byte-different served copy; prove the former succeeds and the latter is
   rejected with existing cleanup/rollback protections intact.

Verification:

- `go test ./internal/service -run 'ClonePlan|CloneExecute|ClonePlanningResult' -count=1`
- `go test ./internal/cli -run 'Clone' -count=1`
- `go test ./internal/service ./internal/cli -race -run 'Clone' -count=1`
- Global definition-of-done commands.

Exit criteria: clone accepts only the scoped v2 contract, remains byte-safe
and transactional, current documentation shows the v2 schema, and all
portable-manifest consumers and fixtures are consistent.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-18 | M00 | `make fmt-check`; focused config tests/race; `go test ./... -count=1`; `go test -race ./... -count=1`; `go vet ./...`; `make build`; `make release-test`; `git diff --check` — pass | Approved after I01 remediated R1 persisted-base validation and R2 JSON wire name | Not committed (not authorized) |
| 2026-08-18 | M01 | Focused init service/CLI tests; `make fmt-check`; full tests/race; vet; build; release-test; diff-check — pass | Approved with no material findings | Not committed (not authorized) |
| 2026-08-18 | M02 | Focused clone service/CLI/race; `make fmt-check`; full tests/race; vet; build; release-test; diff-check — pass | Approved after I21 added real-Git valid-byte-mismatch cleanup coverage | Not committed (not authorized) |
