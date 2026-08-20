# Run ledger — Archon deterministic milestone harness implementation plan

## Run state

- Plan: `docs/plans/archon-milestone-harness.md`
- State: `blocked`
- Started: `2026-08-20T16:51:01+02:00`
- Last updated: `2026-08-20T16:54:00+02:00`
- Active milestone / phase: `M00 — Prove and pin the Archon adoption boundary / blocked`
- Resume from: `Re-query official Archon stable releases for a release containing deterministic-only commit d6c102b4 after the user resumes this blocked run.`
- Final response permitted: `yes`

## Current milestone ledger

- Scope checklist:
  - [ ] Create `archon-harness/` as an isolated Bun/TypeScript package with an exact Bun pin, frozen `bun.lock`, test/type/lint/format/build scripts, pure compatibility types, and temporary-fixture helpers.
  - [ ] Keep `mdast-util-from-markdown` as the only direct production dependency, pin it exactly, and record all transitive licenses; add no JavaScript dependency to the Go module.
  - [ ] Add exact Archon compatibility metadata for the first stable release containing deterministic-only `until_bash`: semantic version, build identity, supported-platform release artifact hashes, feature evidence, supported reviewer mechanisms, and explicit unsupported versions/profiles.
  - [ ] Add a minimal black-box Archon conformance workflow whose body emits completion-looking output while persisted state remains incomplete and whose loop completes only through deterministic `until_bash` state.
  - [ ] Add launcher preflight that rejects incompatible version/build/hash, missing Bun, missing Archon, invalid workflow syntax, unavailable reviewer capability, unsupported reviewer profiles, missing worktree support, and direct-checkout configuration before worktree creation or provider invocation.
  - [ ] Require explicit implementer, reviewer, and escalation profile IDs and reject unsandboxed Codex review or unknown reviewer profiles while accepting only capability-table profiles with enforcement evidence.
  - [ ] Extend existing CI with exact pinned Bun and Archon setup and M00 conformance/package checks on Linux, macOS, and Windows without changing, skipping, or weakening existing Go jobs.
  - [ ] Keep M00 bounded to compatibility, conformance, preflight, fixtures, package scaffolding, and CI; do not implement the product state machine.
  - [ ] RED/GREEN slice 1: make the v0.9.0 fixture and completion-looking body output fail compatibility, then accept only a stable release whose deterministic loop remains active until persisted state is complete.
  - [ ] RED/GREEN slice 2: make unsandboxed Codex and an unknown reviewer profile fail before a process spy records worktree/provider activity, then accept only an enforcement-proven capability-table reviewer profile.
  - [ ] RED/GREEN slice 3: reject wrong version, wrong hash, missing Bun, missing Archon, invalid workflow syntax, and forbidden direct-checkout configuration with bounded diagnostics and no repository changes.
  - [ ] RED/GREEN slice 4: exercise the minimal conformance workflow and package quality scripts in the supported Linux, macOS, and Windows CI jobs.
  - [ ] Document the package setup, exact pinned compatibility boundary, conformance mechanism, reviewer capability boundary, unsupported environments, and M00 verification without claiming later-milestone behavior.
  - [ ] Verify `(cd archon-harness && bun test test/compatibility test/preflight)`.
  - [ ] Verify `(cd archon-harness && bun run test:archon -- conformance)` against the exact pinned Archon binary and hashes without a paid provider.
  - [ ] Verify `(cd archon-harness && bun run format:check && bun run lint && bun run typecheck && bun run build)`.
  - [ ] Verify applicable global gates: frozen dependency state, full package tests/build/conformance, `make check`, and `git diff --check`; do not install tools or dependencies without separate user authority.
  - [ ] Establish exit evidence that an exact stable Archon release and at least one enforced read-only reviewer mechanism are pinned and proven, adversarial completion output cannot terminate the loop, and unsupported environments fail before mutation or provider spend; otherwise record one of M00's two allowed external blockers with exact release/provider evidence.
- Implementation tier: `normal`
- Remediation attempts: `0`
- Finding set:
  - `none; no M00 reviewer pass has occurred.`
- Current packet and evidence: `M00-I0 halted before edits and is not a complete implementation submission. External blocker EB1: the latest stable Archon release is v0.9.0 (published 2026-08-17T12:48:02Z at https://github.com/coleam00/Archon/releases/tag/v0.9.0, tag commit 671e2ee77ea89c7de602f640fc2a222ed9f6ec14), while required deterministic-only loop commit d6c102b417238803ec8582d4e49b932fdc732621 was authored 2026-08-18T07:04:58Z and is absent from v0.9.0. The official compare has merge base 671e2ee7 and places d6c102b4 after the stable tag. The plan and specification explicitly reject v0.9.0 and forbid an unreleased dependency.`
- Verification / adjudication: `Main agent queried GitHub's official releases/latest, tag, commit, and compare APIs; latest stable=v0.9.0, draft=false, prerelease=false, deterministic commit date is later than the release, and v0.9.0...d6c102b4 reports status=ahead with the stable tag as merge base. Local verification: archon-harness absent; bun absent; archon absent; make check passed (go vet ./... and go test ./...); git diff --check passed. Safe continuation requires a stable Archon release whose ancestry contains d6c102b4 or a reviewed equivalent that passes the deterministic-only conformance contract, followed by explicit user resumption and ledger reconciliation. No reviewer adjudication occurred.`

## Action log

| Time | Actor | Action | Outcome / evidence | Resulting state and resume instruction |
|---|---|---|---|---|
| `2026-08-20T16:51:01+02:00` | main agent | Initialize the authorized run and M00 checklist before any agent dispatch. | Read the source plan, specification, context, supervision process, ledger layout, role configuration, CI, and worktree. `command -v bun` and `command -v archon` returned no path. Existing modified/untracked lifecycle documents were recorded and preserved. | `active`; M00 `implementing`; attempts `0`; dispatch the prepared M00 initial implementation packet to the normal implementer. |
| `2026-08-20T16:51:41+02:00` | main agent | Pre-dispatch checkpoint for normal implementer packet M00-I0. | The packet assigns `archon-harness/**` and the harness additions to `.github/workflows/ci.yml`, requires every M00 checklist item and RED/GREEN evidence, forbids ledger edits, commits, installs, publication, deployment, real credentials/data, paid providers, and unrelated changes, and requires exact authoritative evidence if either adoption gate is blocked. | `active`; M00 `implementing`; attempts `0`; collect the normal implementer's M00-I0 submission and checkpoint completeness. |
| `2026-08-20T16:53:30+02:00` | normal implementer | Report M00-I0 external-gate result. | Not a complete implementation submission; no attempt change. The implementer independently queried authoritative GitHub evidence and found latest stable v0.9.0 published before required commit d6c102b4. It halted before edits as required, confirmed bun and archon absent, and reported `make check` and `git diff --check` passing. | `active`; M00 `implementing`; attempts `0`; main agent must validate the release ancestry, filesystem, and available repository gates. |
| `2026-08-20T16:54:00+02:00` | main agent | Validate M00 adoption-gate evidence and local repository state. | Official GitHub API: latest stable v0.9.0, published 2026-08-17T12:48:02Z, tag commit 671e2ee7; deterministic-only commit d6c102b4 authored 2026-08-18T07:04:58Z; official compare uses 671e2ee7 as merge base and places d6c102b4 after it. Local commands confirmed `archon-harness/` absent, no bun/archon on PATH, `make check` passed, and `git diff --check` passed. | `active`; M00 `implementing`; attempts `0`; classify EB1 under M00's pre-authorized external-blocker rule. |
| `2026-08-20T16:54:00+02:00` | main agent | Record external blocker EB1 and safe-continuation conditions. | No stable Archon release contains deterministic-only loop completion; v0.9.0 is the latest stable and is explicitly unsupported. Pinning dev commit d6c102b4 would violate the plan. The second adoption gate and all implementation/runtime checks cannot safely be reached until the release gate passes. Safe continuation requires a stable release containing d6c102b4 or a reviewed equivalent, explicit user resumption, and recovery reconciliation against this ledger. | `blocked`; M00 `blocked`; attempts `0`; final response permitted `yes`; re-query official stable-release ancestry only after the user resumes the run. |
| `2026-08-20T16:54:00+02:00` | main agent | Record terminal run decision. | The authorized run stops at the plan's explicit M00 external blocker. No milestone is approved, no remediation submission was reviewed, no attempt was consumed, and no implementation/CI file was changed. The exact cause, evidence, and safe-resumption condition are recorded above. | `blocked`; terminal checkpoint recorded; final response permitted `yes`. |
