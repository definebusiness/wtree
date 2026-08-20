# Implementation context — Archon harness

Status: initial
Document type: implementation context, not an implementation plan
Parent plan: [Archon deterministic milestone harness implementation plan](archon-milestone-harness.md)
Source specification: [Archon deterministic milestone harness specification](../spec/archon-milestone-harness.md)
Captured: 2026-08-20

## Purpose

This document retains only external compatibility facts and repository
starting conditions needed to execute the parent plan. The specification is
the behavior contract and the plan is the execution contract; both override
this context if they differ.

## Archon facts that affect implementation

- Archon discovers repository workflows, commands, and named scripts below
  `.archon/workflows/`, `.archon/commands/`, and `.archon/scripts/`.
  Current Archon also supports the corresponding global locations below
  `ARCHON_HOME` and one subdirectory beneath each discovery root.
- Archon script nodes run TypeScript/JavaScript with Bun or Python with uv.
  Named scripts write captured node output to standard output.
- The documented CLI passes the workflow's free-form input as the positional
  argument exposed as `$ARGUMENTS`; the investigated CLI did not document the
  `--input key=value` form previously shown in the idea.
- A workflow can require worktree isolation. Direct-checkout execution must
  not be used for this mutating harness.
- A failed `loop_group` may restart its current body iteration rather than
  resume after an individual body node. Domain actions therefore need their
  own persisted idempotency and reconciliation.

Primary references:

- [Archon DAG workflow documentation](https://github.com/coleam00/Archon/blob/dev/packages/docs-web/src/content/docs/book/dag-workflows.md)
- [Archon CLI reference](https://github.com/coleam00/Archon/blob/dev/packages/docs-web/src/content/docs/reference/cli.md)
- [Archon changelog](https://github.com/coleam00/Archon/blob/dev/CHANGELOG.md)

## Adoption-gate evidence

The investigated Archon v0.9.0 loop contract requires a model `until` signal.
When `until_bash` is also present, either condition completes the loop. That
release cannot make persisted state the sole completion authority.

[Archon commit `d6c102b4`](https://github.com/coleam00/Archon/commit/d6c102b417238803ec8582d4e49b932fdc732621)
changes loop validation so `until_bash` can be used without a model signal.
The plan must pin the first stable release that contains this behavior and
passes the local conformance suite; it must not pin the development branch.

Archon's workflow-level `allowed_tools` and `denied_tools` restrictions were
not enforced for Codex in the investigated implementation. Codex therefore
cannot be selected as the independent reviewer unless a separately tested
read-only boundary is added. This limitation does not prevent Codex from being
an implementer.

## Repository starting conditions

- The repository currently contains Go tooling only. It has no
  `archon-harness/`, `package.json`, Bun lockfile, Archon workflow, or
  TypeScript test setup.
- Neither `bun` nor `archon` was available on `PATH` when this context was
  captured.
- Existing CI in `.github/workflows/ci.yml` runs Linux, macOS, and Windows
  with Go setup only. Harness jobs must add pinned Bun and Archon setup without
  weakening the existing Go matrix.
- Files under `docs/ai/runs/` are immutable outside their authorized plan
  run. The parent plan's ledger must not be created until plan execution is
  explicitly authorized.

## Useful first-pass commands

```sh
archon version
archon workflow list --cwd /path/to/fixture
rg -n "until_bash|loop_group|allowed_tools|denied_tools" archon-harness
bun test --cwd archon-harness
git diff --check
```

The first two commands are expected to fail until the pinned tools are
installed. They are diagnostics, not permission to download or execute an
unreleased Archon build.
