# `wtree` specification traceability

Status: implemented
Source specification: [`wtree` specification](wtree.spec.md)
Implementation plan: [`wtree` incremental implementation plan](../plans/wtree-implementation-plan.md)

This matrix maps every numbered section of [`wtree.spec.md`](wtree.spec.md) to
the owning implementation, behavioral evidence, and, where it is user-facing,
the installed help/how-to.  “Optional” is used only where the specification
itself labels the feature optional; no required MVP section is deferred.

| § | Requirement | Enforcement | Evidence / user surface |
|---:|---|---|---|
| 1 | Purpose | `internal/cli`, `internal/service` | CLI E2E; root help |
| 2 | identity/path separation | `internal/domain`, `internal/git`, resolver | domain/git/resolve tests |
| 3 | project/repository/workspace/checkout/mount terms | `internal/domain` | domain tests; root help concepts |
| 4 | repository hierarchy and effective paths | `domain.Project`, `pathutil` | project/mount/plan tests |
| 5 | synchronized branch model | planner/create/checkout services | plan/create/workspace tests |
| 6 | workspace storage precedence | `config.ResolveWorktreeRoot` | config/plan tests; `path` help |
| 7 | stable project identity | initializer, config, registry, project inventory | init/resolve/project-inventory tests; `project list` help |
| 8 | project configuration | `internal/config`, domain validation | config/init/resolve tests |
| 9 | global configuration | config service/files | config CLI/service tests |
| 10 | runtime state model | `store.WorkspaceState` | store/workspace tests |
| 11 | command invocation context | `service.Resolver` | resolve tests; root help |
| 12 | local/Git project discovery | resolver | resolve tests |
| 13 | explicit `--project` selection | root persistent option, resolver | root/resolve tests |
| 14 | `init` | initializer, registration-conflict policy, and init command | init/registration-conflict service/CLI tests; init help/how-to |
| 15 | nested repository discovery | initializer/Git adapter | init tests |
| 16 | `create` | planner, creator, create command | plan/create/CLI E2E; create help/how-to |
| 17 | `create --from` | planner request and Git adapter | plan/create tests; create help |
| 18 | mount overrides | planner/path utilities | plan/root tests; create help |
| 19 | parent-first creation | planner ordering | plan/create tests |
| 20 | creation algorithm | creator/transaction runner | create/transaction/E2E tests |
| 21 | transactional operations | `internal/transaction`, service transaction | transaction tests; recovery diagnostics |
| 22 | complete preflight | planner/remover/deleter | plan/remove/delete tests |
| 23 | dry-run has no mutation | CLI plan commands and read-only resolver | root/import/doctor/project-prune/project-unregister tests; command help |
| 24 | `checkout` | workspace creator/checkout command | workspace/CLI tests; checkout help |
| 25 | `list` | workspace service/list command | workspace CLI tests; list help |
| 26 | `status` | status service/Git adapter | status tests; status help |
| 27 | `path` | workspace lookup/path command | workspace CLI tests; path help |
| 28 | repository lookup | `Workspace.ResolveRepository`, repo command | workspace CLI structural tests; repo help |
| 29 | `remove` | remover/remove command | remove CLI/service tests; remove help/how-to |
| 30 | `delete` | deleter/delete command | delete CLI/service tests; delete help/how-to |
| 31 | constrained `--force` | remove/delete plans and CLI validation | remove/delete/root tests; command help |
| 32 | import existing workspaces | importer/import command | import service/CLI E2E; import help/how-to |
| 33 | import example behavior | importer mounts/branches | import tests; import how-to |
| 34 | import by Git identity | Git adapter/importer | import/resolve tests |
| 35 | import discovery | importer/Git adapter | import tests |
| 36 | partial imports | importer/state model | import tests |
| 37 | workspace-specific mounts | state/checkouts/planner | workspace/import/plan tests |
| 38 | central repository resolver | `Workspace.ResolveRepository` | workspace/CLI tests |
| 39 | `doctor` diagnostics | doctor service/command | doctor service/CLI tests; doctor help/how-to |
| 40 | narrow `doctor --fix` | doctor allowlist/transactional writes | doctor tests; doctor help |
| 41 | Git worktree prune/repair | doctor Git adapter boundary | doctor prune tests |
| 42 | branch checked elsewhere | Git adapter/planner preflight | plan/workspace tests |
| 43 | dirty worktrees | Git status/remover/deleter | remove/delete/status tests |
| 44 | detached HEAD | Git adapter/status/import | status/import tests |
| 45 | workspace naming | planner/domain validation | plan/domain tests |
| 46 | divergent workspace branches | imported checkout state/status | import/status tests |
| 47 | JSON output | `internal/render` and commands | render/CLI structural tests |
| 48 | exit taxonomy | `service.Error`, CLI classifier | errors/root/main tests; root help |
| 49 | stdout/stderr separation | render/CLI command boundaries | CLI output tests |
| 50 | shell integration | `path` and `repo path` scalar renderers | workspace tests; root/path/repo help |
| 51 | agent workflow | create/path/status/remove/delete | CLI E2E; global how-to |
| 52 | `new` alias | Optional surface intentionally absent | specification explicitly permits omission; `create --from HEAD` documented |
| 53 | MVP command set | root command construction | root/help command-matrix tests |
| 54 | common option validation | Cobra flags and CLI validators | help/root/config tests |
| 55 | complete root help | `rootHelp` | help tests |
| 56 | per-command help | detailed help renderer | help tests |
| 57 | `--how-to` | how-to renderer/terminal validation | help tests |
| 58 | global how-to topics | how-to renderer | help tests |
| 59 | command how-to guides | how-to renderer | help tests |
| 60 | help output stability | fixed templates and examples | help tests |
| 61 | domain model | `internal/domain` | domain tests/fuzz |
| 62 | internal architecture boundaries | domain/config/git/store/plan/transaction/service/cli packages | package tests and `go vet` |
| 63 | GitAdapter | `internal/git.Adapter` | adapter/parse/branch tests; fuzz |
| 64 | WorkspacePlanner | `service.WorkspacePlanner`, `internal/plan` | plan tests |
| 65 | TransactionRunner | `internal/transaction` | transaction tests |
| 66 | state commit ordering | service transaction/store/registry remover | transaction/project-registry-removal/store atomic tests for prune/unregister |
| 67 | concurrency | project locks and transaction/registry-removal/init locked revalidation | transaction/create/config/project-registry-removal/init tests for prune/unregister and duplicate registration |
| 68 | path safety | `internal/pathutil`, domain validators | pathutil/domain fuzz and tests |
| 69 | mount conflicts | planner validation | plan tests |
| 70 | root content versus nested mount | planner/preflight | plan tests |
| 71 | independent nested repositories | initializer/Git discovery | init/resolve tests |
| 72 | repository identity changes | resolver/doctor diagnostics | resolve/doctor tests |
| 73 | imported workspace names | importer validation | import tests |
| 74 | import branch mismatch | importer/status facts | import/status tests |
| 75 | status drift detection | status service | status tests |
| 76 | project registry | store/initializer/registration-conflict policy/resolver/project inventory/registry remover | init/registration-conflict/resolve/import/doctor/project-inventory/project-registry-removal tests; registry fuzz; `wtree project list`, `wtree project prune`, `wtree project unregister` |
| 77 | source checkout as workspace | initializer/default workspace | init/resolve tests |
| 78 | current workspace detection | resolver | resolve/workspace/status tests |
| 79 | no project context | resolver error mapping | resolve/root tests |
| 80 | ambiguous project detection | resolver/registration-conflict policy/project inventory/registry removal diagnostics | resolve/registration-conflict/project-inventory/project-registry-removal tests; `wtree project list`, guarded `wtree project prune`, intentional exact `wtree project unregister` |
| 81 | verbose logging | CLI transaction progress renderer | root/help tests |
| 82 | actionable error context | typed errors and transaction recovery rendering | errors/transaction/CLI tests |
| 83 | no partial logical state | transaction rollback/recovery/read-only inventory/atomic registry removal | transaction/remove/delete/project-inventory/project-registry-removal/store atomic prune/unregister no-mutation tests |
| 84 | repository ordering | domain graph/planner ordering | project/plan/remove/delete tests |
| 85 | configuration validation | config/domain loading | config/resolve tests |
| 86 | config/state/registry versioning | config/store migration boundaries | config/store tests and fuzz |
| 87 | config commands/scopes | config CLI/service | config CLI/service tests; config help |
| 88 | end-to-end workflow | CLI create/path/status/remove/delete | CLI E2E; global how-to |
| 89 | import workflow | CLI import and identity discovery | import CLI E2E; import how-to |
| 90 | required architectural invariants | rows below | focused invariant evidence below |
| 91 | primary product goal | create/path/remove/delete lifecycle | CLI E2E; root help/how-to |
| 92 | essential design statement | domain `Project → Repository → Workspace → Checkout` | domain/resolver tests; root help concepts |
| 93 | planning coverage | executed milestone plan and package boundaries | plan test suites, CI, this matrix |
| 94 | portable manifest authoring and clone extension | [portable manifest clone specification](portable-manifest-clone.md); `service.Initializer`, portable config codec, manifest loader, clone planner/executor, clone CLI | portable-config/init/manifest-source/clone-plan/clone-execute/clone CLI tests and process E2E; update/sync/release locking remain future work |

## Logical-root repository-forest extension

The [logical project root and repository forest specification](logical-project-root-base-repository.md)
extends the implemented requirements above without renumbering the historical
core specification. Its authoritative implementation plan and durable run
ledger provide the detailed milestone evidence.

| Extension requirement | Enforcement | Evidence / user surface |
|---|---|---|
| Plain logical roots and sibling top-level repository trees | `domain.Project`, `Project.EffectivePaths`, resolver, discovery | domain/path/discovery/resolve tests; root help and README topology example |
| Exactly one top-level base metadata authority | local config v2, portable v2, init/clone loaders | config/init/clone tests; portable guidance |
| Logical-root-relative top-level mounts and immediate-parent-relative child mounts | `pathutil`, domain effective paths, planners/importer | path/domain/plan/import tests and fuzz |
| Stable forest ordering | `Project.ParentFirst` and `ChildFirst` | domain, clone/create/import/status/doctor/remove/delete tests |
| Forest-aware init, clone, create/checkout, import, inspection and teardown | `internal/service` and `internal/cli` command owners | focused forest integration and public CLI/process tests; tutorial |
| Additive topology JSON with scalar compatibility | service result objects and CLI renderers | structural JSON tests for init/clone/workspace/import/status/doctor/project/remove/delete; `path` and `repo path` scalar tests |
| Safety-first rollback and recovery across repository trees | transaction, receipt/CAS publication, recovery records | failure-injection, replacement, recovery, normal/race and tutorial evidence |
| Strict local v2 transition with unchanged surrounding schema versions | config/store/plan/result validators | local-v1 rejection and global/registry/workspace/recovery/plan compatibility tests |

## Required architectural invariants (§90)

| # | Invariant | Enforcing code | Focused evidence |
|---:|---|---|---|
| 1 | Git identity is not checkout path | `Repository.CommonGitDir`, resolver/importer | resolve/import tests |
| 2 | Workspace maps concrete checkouts | `domain.Workspace.Checkouts` | workspace tests |
| 3 | Checkout carries branch and mount | checkout validators/state | workspace/plan tests |
| 4 | Mounts are parent-relative | `pathutil.ResolveMount` | mount tests and fuzz |
| 5 | Effective paths are centralized | `Project.EffectivePaths` | project tests |
| 6 | Repository lookup is centralized | `Workspace.ResolveRepository` | workspace/CLI tests |
| 7 | Create is parent-first | planner/creator | plan/create tests |
| 8 | Remove is child-first | remover/deleter | remove/delete tests |
| 9 | Git is worktree authority | Git adapter/status/doctor | adapter/status/doctor tests |
| 10 | State is logical, versioned data | `internal/store` schemas | store tests/fuzz |
| 11 | Mutations preflight before effects | planners/transaction runner | plan/transaction tests |
| 12 | Failures attempt rollback | transaction/remover/deleter | transaction/remove/delete tests |
| 13 | State commits after Git effects | transaction state boundary | transaction tests |
| 14 | Manual worktrees are imported | importer | import service/CLI E2E |
| 15 | Import uses Git identity | importer discovery | import/resolve tests |
| 16 | Renamed mounts persist | imported checkout state | import/workspace tests |
| 17 | cwd is only a convenience | resolver precedence | resolve tests |
| 18 | explicit project selection works | root persistent flag/resolver | root/resolve tests |
| 19 | default storage stays external | configuration resolution | config/plan tests |
| 20 | human/JSON output are separate | render and command boundary | render/CLI tests |

## Release build contract

`scripts/release-build.sh` builds local artifacts only. With `VERSION=1.2.3`,
it writes `dist/wtree_1.2.3_{linux,darwin,windows}_amd64` (with `.exe` for
Windows) plus `dist/SHA256SUMS`. The version is injected through
`internal/cli.Version`; it copies the MIT `LICENSE` and `NOTICE` into the
release directory and includes them in the SHA-256 manifest. Nothing publishes,
tags, uploads, or signs a release.

The script stages binaries before replacing only recognized prior artifacts,
the manifest, and legal notices. Unrelated output-directory contents are
preserved. CLI rollback/recovery E2E uses a compiled platform-native,
test-only Git executable on `PATH`, including in the Windows CI job, without
altering production behavior.
