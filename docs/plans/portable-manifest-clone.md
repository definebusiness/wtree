# Portable manifest clone implementation plan

Status: implemented
Source specification: [Portable manifest clone specification](../spec/portable-manifest-clone.md)
Related idea: [Clone and synchronize a multi-repository project](../ideas/cloning-a-multi-repository-project.md)
Source of truth: [`docs/spec/portable-manifest-clone.md`](../spec/portable-manifest-clone.md); [`docs/spec/nested-mount-ignore-management.md`](../spec/nested-mount-ignore-management.md) for immediate-parent ignore ownership and committed-rule semantics; [`docs/spec/wtree.spec.md` §§7, 12–15, 21–23, 47–49, 66–72, 76–85](../spec/wtree.spec.md); [`internal/config/config.go`](../../internal/config/config.go); [`internal/discovery/discovery.go`](../../internal/discovery/discovery.go); [`internal/git/adapter.go`](../../internal/git/adapter.go); [`internal/service/init.go`](../../internal/service/init.go); [`internal/service/resolve.go`](../../internal/service/resolve.go); [`internal/store/store.go`](../../internal/store/store.go); [`internal/lock/lock.go`](../../internal/lock/lock.go); [`internal/cli/root.go`](../../internal/cli/root.go)
Delivery style: test-first, one reviewed milestone at a time; local and
HTTP(S) manifest sources; no update/sync implementation, dependency additions,
commits, pushes, publication, or release

## Execution contract for Codex

When asked to run this plan, continue unattended until every milestone is
checked or a genuine external blocker is reached. Do not ask for routine
design decisions; this plan fixes those decisions below.

For each unchecked milestone, in order:

1. Read this plan, the relevant source-of-truth sections, the durable run
   ledger at `docs/ai/runs/portable-manifest-clone.md`, and the current
   worktree. Create the ledger before the first dispatch. On resumption,
   reconcile the plan, ledger, evidence, and worktree, then append a
   reconciliation checkpoint before dispatching work.
2. Derive a complete checklist for this milestone from its scope, test-first
   slices, exit criteria, documentation requirements, and verification
   commands. Record it in the current ledger entry.
3. Give the complete initial packet to `implementer`. For remediation, use
   `implementer` when the ledger attempt count is `0` or `1`, and
   `escalation-implementer` only when it is `2`. Require RED → GREEN →
   REFACTOR evidence, files changed, verification results, and unresolved
   concerns.
4. Treat partial work as progress, not a submission. Do not request review or
   change the remediation counter until every checklist item is evidenced.
5. Send each complete submission to the read-only `reviewer`, which inspects
   the current shared filesystem, applicable sources of truth, scope, safety,
   portability, test quality, and required checks.
6. If review finds material issues, record the complete stable-ID finding set
   and return all unresolved findings in one test-first remediation packet.
   Apply the three rejected complete-remediation limit defined by
   `docs/ai/milestone-supervision.md`. Do not use `escalation-reviewer` as a
   routine second review.
7. On reviewer approval, run the milestone verification commands as the main
   agent, update affected documentation/contracts, check the milestone, and
   append its concise execution-log row.
8. Immediately create the next milestone ledger snapshot and dispatch its
   initial packet. Do not send a final response while work remains active.

Do not stop for a failing ordinary test, reviewer finding, partial submission,
or milestone approval. Stop only for a documented external blocker that cannot
be safely resolved within this authorized scope, or the three-rejected-complete
remediation terminal condition. Preserve unrelated user changes; do not use
destructive cleanup commands; commit only when separately authorized.

## Fixed implementation decisions

### Scope and delivery boundary

- This plan delivers the portable `project.wtree.yml` contract, generation by
  `wtree init`, and this exact root command:

  ```text
  wtree clone <manifest-source> [destination]
    [--worktree-root <path>]
    [--data-dir <path>]
    [--dry-run]
    [--json]
    [--verbose]
  ```

- `wtree update` and `wtree sync` remain future work. Remove or clearly mark
  existing README, tutorial, and built-in how-to text that falsely presents
  either command as available. Persisting `manifest.source` prepares for a
  future sync implementation but does not add one.
- Release lock manifests are outside this plan. `project.wtree.yml` follows
  movable default branches, not exact release object IDs.
- `clone` rejects root `--project`, `--force`, `--mount`, and `--from` with the
  existing invalid-arguments contract. Clone does not accept discovery-ignore
  flags because the manifest is the complete repository set.
- Keep the executable development version at `0.2.0`. This plan does not
  authorize release publication or a further version decision.

### Portable and local configuration contracts

- `.wtree.yml` remains ignored, machine-local configuration. Extend its
  version-one schema additively with:

  ```yaml
  manifest:
    path: project.wtree.yml
    source: /absolute/or/https/source
  ```

- Existing version-one local files without `manifest` remain valid. No
  automatic rewrite occurs merely because they are read. Unknown fields and
  newer versions remain rejected.
- `manifest.path` is the clean root-relative literal
  `project.wtree.yml` in v1. Alternate local filenames are deferred.
- `manifest.source` stores the source used to bootstrap or author the project:
  a cleaned absolute path for local input, or the exact credential-free
  normalized HTTP(S) URL whose fetch semantics are unchanged.
- `project.wtree.yml` is tracked portable configuration and contains no local
  checkout paths, worktree roots, common Git directories, credentials, or
  machine-specific state. Its strict version-one shape is:

  ```yaml
  version: 1
  project:
    id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
    name: acme-shop
  repositories:
    root:
      clone:
        remote: origin
        url: https://github.com/acme/acme-shop.git
      upstream:
        branch: main
        remote: origin
        merge: refs/heads/main
      identity:
        initial_commits:
          - 0123456789abcdef0123456789abcdef01234567
      parent: ""
      mount: .
      default_branch: main
  ```

- YAML uses the repository's strict one-document, known-field decoding.
  Repository maps render in lexical ID order; identity commit arrays are
  sorted; deterministic generation against unchanged repositories is
  byte-identical.
- A repository entry records exactly one clone remote and URL in v1. The
  upstream remote must equal the clone remote. Multiple remotes/profiles and
  relative repository URLs are deferred.
- `upstream.branch` is the local default branch name;
  `upstream.merge` is the full remote branch ref and must begin
  `refs/heads/`. A local default branch may track a differently named remote
  branch.
- `identity.initial_commits` contains every full root commit reachable from
  the selected default-branch `HEAD`, sorted and non-empty. Clone verification
  requires the cloned history to contain all recorded roots. This guards
  against plausible wrong repositories while intentionally not claiming to
  distinguish a legitimate fork that shares the same roots.
- Project/repository IDs, hierarchy, mounts, branch names, URLs, and initial
  commit identities receive central validation before any mutation.

### Init authoring behavior

- Extend `wtree init [path]` with repeatable
  `--clone-url <repository-id>=<url>` and optional
  `--manifest-source <path-or-http(s)-url>`.
- For each repository, init requires an attached current branch with a
  configured upstream. It obtains the local branch, upstream remote, full
  merge ref, and that remote's fetch URL from Git. It never assumes `origin`.
- Init resolves the advertised upstream ref and requires it to equal the local
  default-branch `HEAD`. An ahead, behind, diverged, deleted, or otherwise
  unpublished branch fails preflight; the generated moving-branch manifest
  must describe the exact published state that init inspected.
- `--clone-url` replaces only the recorded portable bootstrap URL for the
  named repository. The upstream remote/name/merge discovery still must be
  unambiguous and valid. Unknown or duplicate overrides fail before mutation.
- Reject repository URLs containing HTTP(S) userinfo, control characters,
  newlines, or credential-bearing forms. SSH/scp-style, absolute local-path,
  `file://`, and credential-free HTTP(S) Git URLs are accepted as opaque Git
  clone sources in v1. Local/`file://` URLs are primarily for local projects
  and hermetic tests; no URL rewriting occurs.
- If `--manifest-source` is omitted, init stores the cleaned absolute path to
  the generated root `project.wtree.yml`. If supplied, it is validated and
  stored exactly according to the source rules below.
- Init generates `.wtree.yml` and `project.wtree.yml` as one logical
  publication under existing registry/project lock ordering. It also retains
  the existing responsibility to ensure `/.wtree.yml` in root `.gitignore`.
  The portable manifest is never ignored automatically.
- Complete deterministic preflight occurs before writes. Failure after any
  config/ignore/registry/state publication restores exact prior bytes and
  existence or reports complete rollback evidence. `--dry-run` renders all
  proposed files and repository metadata without mutation.
- Init never stages, commits, tags, or pushes. Human success tells the author
  to review and commit `.gitignore` and `project.wtree.yml`.

### Manifest-source fetching

- A manifest source is parsed before I/O as either a local path or an absolute
  `http`/`https` URL. Other URL schemes are invalid as manifest sources and
  are never passed to Git.
- Local sources are read from a cleaned absolute path. Directories, symlinks,
  device files, and files larger than 1 MiB are rejected.
- HTTP(S) uses a dedicated standard-library client with a 30-second total
  timeout, at most five redirects, default TLS verification, a 1 MiB response
  limit, and no ambient cookies. HTTPS-to-HTTP downgrade redirects are
  rejected. Non-2xx status, excess redirects/body size, invalid URLs, and
  read/timeouts are classified without exposing response bodies or secrets.
- Manifest-source URLs containing userinfo are rejected because the exact
  source is persisted. Diagnostics redact URL userinfo defensively even on
  rejected input and redact credential-shaped repository URLs.
- HTTP content type is advisory, not authoritative; the body must pass strict
  YAML validation. Relative repository clone URLs are rejected in v1 rather
  than resolved relative to either caller cwd or manifest URL.
- Tests use `httptest` and local repositories only. No test depends on public
  network, user credentials, proxies, global Git configuration, or DNS.

### Clone destination and plan

- With an explicit destination, resolve it relative to the caller cwd and
  clean/canonicalize its existing parent. Without one, use
  `./<manifest-project-name>` only when the name is a safe single filesystem
  component; otherwise fail and request an explicit destination.
- The final destination must not exist. Existing empty directories are not
  adopted in v1. Reject symlinks, aliases of registered project/workspace
  paths, root/broad paths, a missing or non-directory/non-writable nearest
  parent, and path escape through existing symlink ancestors.
- Clone planning fetches/reads and strictly validates the manifest, checks
  destination and registry project-ID collisions, resolves every default
  branch to a full advertised commit with read-only `git ls-remote`, and builds
  a deterministic parent-first repository/action plan containing those
  observed commits. It performs no clone, directory, temp-file, lock, registry,
  or state mutation.
- `--dry-run` is a complete read-only plan. It may contact declared manifest
  and Git remotes, but it creates no destination/staging directory or lock.
  Human output lists source, destination, repositories, remotes, branches,
  mounts, and intended verification, then ends with `No changes made.`
- Stable JSON includes plan version, operation `clone`, normalized manifest
  source, destination, project identity, and deterministic repositories and
  steps. URLs are emitted only when credential-free; errors remain one JSON
  envelope on stdout and no human stderr.

### Clone staging, execution, and verification

- Real clone revalidates all local preconditions before the first effect, then
  creates one hidden, uniquely named staging directory in the final
  destination's existing parent so final publication can use a same-directory
  atomic rename. It does not hold the global registry lock during network Git
  operations.
- Clone the root and children into staging in parent-first order. Use argument
  arrays and the hardened environment; never invoke a shell. Fetch and check
  out each plan's exact advertised commit, failing rather than substituting a
  newer branch tip if the remote moves or no longer serves that object.
  Configure the recorded remote name and create the local default branch at
  that commit, tracking the exact recorded merge ref. Do not silently
  substitute a remote default branch.
- Before cloning a child, inspect its already-cloned immediate parent's
  selected commit and require the effective mount to be ignored by committed
  `.gitignore` content owned by that parent. Local/global excludes do not
  qualify. A missing rule aborts before the child path exists.
- After cloning each repository, verify clone URL configuration, local branch,
  upstream remote/merge ref, clean state, full initial-commit identity set,
  parent-relative mount, and absence of submodules.
- After cloning the root, require its tracked `project.wtree.yml` at the
  selected default-branch commit to be byte-identical to the fetched manifest.
  This binds a standalone/local/HTTP manifest to the root history and rejects
  unpublished or stale manifests.
- Once all repositories pass, write ignored local `.wtree.yml` into staging
  with source paths, manifest path/source, local worktree setting, and default
  checkout data. Verify `/.wtree.yml` is effective from committed root content;
  clone does not edit the published checkout's `.gitignore`.
- Immediately before publication, acquire the existing registry-then-project
  locks and revalidate the destination, registry conflicts, and local path
  identities. Atomically rename the fully assembled staged root to the final
  destination, recalculate/verify canonical Git common directories at final
  paths, then atomically publish the default workspace state and registry
  entry while retaining those locks. Remote refs are not re-read: execution is
  bound to the exact commits captured in the immutable plan.
- The operation owns only its unique staging directory, final destination
  created by its rename, registry key, and default state written during this
  invocation. Rollback removes/restores only those exact objects after
  containment and identity checks. It never deletes a pre-existing path.
- A failure before final rename removes staging. A failure after rename rolls
  back registry/state and removes the known-created destination only after
  re-verifying its project/repository identities. Cleanup failure records and
  reports every retained path/state entry through existing rollback-incomplete
  conventions.
- Cancellation is honored at safe boundaries; cleanup runs without the
  canceled context. Verbose progress names bounded actions and repository IDs
  on stderr, with all URLs redacted. JSON mode suppresses human progress.

### Registry, state, and compatibility

- A cloned project becomes the `default` workspace only after all checkouts
  are verified at final paths. State records each actual branch, `HEAD`, mount,
  path, and detached flag using existing schema version 1.
- Registry entries use the cloned final `.wtree.yml` path and final common Git
  directory identities. Existing project ID, config path, repository identity,
  workspace path, or storage-name conflicts fail without overwriting.
- Local config adds optional manifest metadata without changing config version
  1. Portable manifest has its own independently versioned schema 1. Registry,
  workspace, recovery, and workspace-plan versions do not change.
- Existing locally initialized projects without manifest metadata continue to
  resolve and all current workspace commands remain compatible.
- Clone does not create a workspace recovery record unless failure occurs
  after durable registry/state publication and rollback is incomplete. Any
  recovery representation must identify operation `clone` without weakening
  readers of version-one recovery data.

### Authority and non-goals

- Do not implement `update`, `sync`, release locks, named URL profiles,
  relative repository URLs, interactive credential prompts, sparse/partial
  clone, shallow clone, LFS orchestration, submodules, or adoption of existing
  destinations.
- Do not stage, commit, tag, push, publish, install, or release. Clone is a
  local filesystem/registry operation even when it reads network sources.
- Do not add third-party dependencies. Use the standard HTTP library and
  existing YAML, Git, config, filesystem, lock, transaction, render, and test
  boundaries.
- Preserve unrelated current changes, including other unexecuted idea/spec/
  plan documents. Never edit another plan's durable run ledger.
- A reviewer request to add update/sync or another deferred feature is outside
  scope and requires user authorization; it is not a remediation attempt.
- Ordinary network test simulation, platform behavior, or failing tests are
  not external blockers. A genuine blocker is unavailable required tooling/CI
  evidence or an irreconcilable conflict with preserved user work.

## Stable contracts to establish early

### Portable manifest model

- Owner: a dedicated `internal/config` portable-manifest model/codec, separate
  from local `ProjectConfig`.
- Consumers: init authoring, clone source loader/planner, clone verifier, CLI
  JSON/human rendering, and future update/sync implementations.
- Invariant: strict versioned decoding, deterministic encoding, complete tree
  validation, credential-free portable values, and no local checkout,
  worktree-administration, or data-directory paths. Explicit absolute local or
  `file://` clone URLs remain permitted bootstrap transports.
- Evidence: round-trip/golden/fuzz tests for malformed YAML, unknown fields or
  versions, hierarchy/mount/branch/URL/identity errors, deterministic maps,
  and credential rejection/redaction.
- Migration: portable schema starts at version 1; local config v1 gains an
  optional manifest block; no existing file is rewritten on read.

### Git upstream and clone boundary

- Owner: `internal/git` exposes typed read-only upstream/remote/initial-commit/
  ls-remote facts and explicit clone/configure operations.
- Consumers: init and clone services only. CLI and config packages never run
  Git or parse Git command output.
- Invariant: commands use locale-neutral non-interactive environments,
  argument arrays, bounded stderr, no shell, no implicit `origin`, and exact
  repository context.
- Evidence: parser/unit tests, hostile environment tests, local bare-remote
  integration, missing/ambiguous upstreams, differently named remote branch,
  URL redaction, cancellation, and operation failure context.

### Manifest source loader

- Owner: a narrow service/adapter boundary parses local versus HTTP(S) input,
  normalizes the persisted source, performs bounded reads, and returns bytes
  plus redacted provenance.
- Consumers: clone planner now and sync later; callers cannot bypass its size,
  timeout, redirect, TLS, credential, or source-kind rules.
- Invariant: fetching is read-only, bounded, deterministic after bytes arrive,
  and safe to report in human/JSON errors.
- Evidence: local file and `httptest` fixtures covering all response, redirect,
  timeout, truncation, userinfo, and redaction cases without public network.

### Immutable clone plan and transaction

- Owner: `internal/service` owns clone planning/execution; a focused internal
  plan type owns stable JSON validation/action ordering; existing lock/store/
  transaction packages retain their generic responsibilities.
- Consumers: clone CLI dry-run/execution and renderers.
- Invariant: plan exact advertised commits before mutation; revalidate local
  facts before effects and again under publication locks; private same-parent
  staging; parent-first exact-commit clone and ignore verification; verify
  before atomic destination publication; register last; exact reverse cleanup.
- Evidence: decoded plan contracts, read-only dry-run assertions, effect-level
  injected failures, concurrency, cancellation, symlink/containment attacks,
  and real three-level clone E2E.
- Migration: clone plan starts at version 1 independently of workspace plans;
  persistent store schemas remain version 1.

## Architecture and dependency boundaries

```text
internal/cli
    │ parse/render only
    ▼
internal/service
    ├── manifest source loader ──> net/http or local read-only filesystem
    ├── init manifest author ────> config codec + Git facts
    └── clone planner/executor ──> clone plan + transaction coordination
                                      │
                 ┌────────────────────┼────────────────────┐
                 ▼                    ▼                    ▼
           internal/git       internal/config      lock/store/fsutil
                 │                    │                    │
                 └──────────── Git/filesystem/network ─────┘
```

- Portable config cannot import service, Git, registry, or local runtime
  concerns.
- Source loading cannot parse manifests or mutate destinations.
- Git adapter cannot decide project hierarchy, destination policy, or render
  output.
- Clone planning is read-only. Execution consumes an immutable validated plan
  and does not reinterpret raw CLI values.
- Registration and state publication remain behind existing store and lock
  boundaries. No second registry writer or ad hoc JSON mutation is allowed.
- Human and JSON renderers receive structured plans/results/errors and never
  inspect Git, network, or filesystem state.

## Global definition of done

Every milestone must satisfy all applicable items below before approval:

- Record RED → GREEN → REFACTOR evidence for each behavioral slice. Include
  success, invalid input, no-mutation, rollback, concurrency, cancellation,
  credential-redaction, and portability cases relevant to the milestone.
- All Git and HTTP tests are hermetic and use temporary local/bare repositories
  or `httptest`; they disable user/system Git configuration and never require
  public network, credentials, DNS, proxy state, or user home mutation.
- Dry-run tests compare destination, registry, state, locks, refs, indexes,
  configs, and relevant file bytes/metadata before and after.
- Existing user work and unrelated dirty files are preserved. No file under
  `docs/ai/runs/` changes except this plan's ledger during an authorized run.
- Public command/help/how-to/README/tutorial/spec/traceability and JSON
  contracts agree by the milestone that exposes behavior. No documentation
  presents deferred update/sync as implemented.
- No dependency, executable version, persistent schema version, release,
  commit, stage, tag, push, or publication change occurs outside the fixed
  decisions.
- Run the milestone's focused commands plus:

  ```sh
  gofmt -w <changed-go-files>
  git diff --check
  go vet ./...
  go test ./...
  go test -race ./...
  make check
  ./scripts/release-build_test.sh
  ```

- Linux, macOS, and Windows CI remain green. Platform-sensitive atomic rename,
  path, file-mode, executable, and symlink behavior requires named CI evidence
  when it cannot be exercised locally.
- The independent reviewer approves the complete milestone with no unresolved
  material findings, and the main agent repeats all required verification.

## Milestones

### [x] M00 — Specify and validate portable/local manifest contracts

Specification coverage: portable manifest clone specification §§2–4 and 10.

Scope:

- Integrate `docs/spec/portable-manifest-clone.md` with the general
  `docs/spec/wtree.spec.md` and traceability without duplicating or weakening
  its authoritative schema, source, init, clone, transaction, error, JSON, and
  non-goal contracts. Do not claim implementation before the enforcing code
  and tests exist.
- Implement separate strict portable-manifest types/codecs and additive local
  manifest metadata in `internal/config`, with deterministic serialization and
  validation boundaries.
- Implement pure validation for project/repository IDs, one-root acyclic
  hierarchy, parent-relative safe mounts, default/upstream branch refs, remote
  names, clone URL classes, credential/control-character rejection, and sorted
  initial commit sets.
- Add explicit migration/compatibility tests proving existing local v1 config
  remains readable and byte-untouched, while portable versions/unknown fields
  are independently rejected.
- Do not change init or register `clone` in this milestone.

Test-first slices:

1. Strictly decode and deterministically re-encode the canonical three-level
   manifest; repeated writes are byte-identical and map order is stable.
2. Reject zero/multiple roots, missing parents, cycles, duplicate/unsafe IDs,
   invalid root/child mounts, collisions, malformed refs/remotes, empty or
   abbreviated initial commits, null required collections, and multiple YAML
   documents.
3. Accept credential-free SSH/scp, HTTP(S), absolute local, and `file://` clone
   URLs; reject HTTP userinfo, controls/newlines, relative URLs, and values that
   could be mistaken for CLI options or shell fragments without executing
   anything.
4. Read old local config without manifest metadata, round-trip new optional
   metadata, reject unknown/newer versions, and prove read-only operations do
   not rewrite either form.
5. Fuzz strict manifest decoding and URL/ref/mount validation without panic,
   unbounded allocation, or acceptance of unsafe paths/credentials.

Verification:

- `go test ./internal/config ./internal/domain ./internal/pathutil -run 'Manifest|Portable|Project|Mount' -count=1`
- `go test ./internal/config ./internal/domain ./internal/pathutil -run 'Manifest|Portable' -race -count=1`
- `go test ./internal/config -run '^Fuzz' -fuzz=Fuzz -fuzztime=5s`
- Apply every command in the global definition of done.

Exit criteria: the portable/local schemas and compatibility policy are
authoritative, strict, deterministic, fuzz-safe, and independently reviewable;
no current CLI behavior has changed.

### [x] M01 — Establish Git upstream, identity, and clone operations

Specification coverage: portable manifest clone specification §§3–4 and 7–9.

Scope:

- Extend `git.Git` with typed facts for configured upstream remote/full merge
  ref, remote fetch URL, sorted root commits, remote branch advertisement, and
  tracked manifest bytes at a commit.
- Add hardened clone/configure primitives sufficient to clone into an exact
  path with a named remote, fetch/check out an exact planned commit, create the
  recorded local branch at that commit tracking the recorded merge ref, and
  verify URL/upstream/clean/submodule facts.
- Preserve locale-neutral, optional-lock/non-interactive environment behavior,
  bounded stderr, context cancellation, argument-array execution, and URL
  redaction. Never pass manifest-source URLs to Git.
- Extend hermetic Git fixtures for local bare remotes, differently named
  remotes/local-vs-remote branches, multiple root commits, absent/ambiguous
  upstreams, and injected command failures.
- Do not expose CLI or mutate config/state in this milestone.

Test-first slices:

1. Discover exact upstream remote, merge ref, fetch URL, local branch, and
   sorted initial commits from a pushed repository; compare local `HEAD` with
   the advertised upstream commit and fail clearly when detached, ahead,
   behind, diverged, upstream is missing, remote is missing, or merge
   configuration is invalid.
2. Resolve a declared remote branch without cloning and distinguish absent ref,
   authentication/transport failure, cancellation, and malformed output.
3. Clone a local bare remote under a non-`origin` name at an exact advertised
   commit, configure a local branch tracking a differently named remote branch,
   and verify every resulting fact; move/delete the branch after planning and
   prove execution either obtains the planned object or fails without using a
   replacement tip.
4. Reject/diagnose submodules, wrong initial roots, stale URL configuration,
   dirty results, and absent tracked `project.wtree.yml` content.
5. Prove hostile global/system Git config, credential helpers, prompts, hooks,
   locale, and shell-shaped URL/path values cannot alter command semantics or
   leak secrets in errors.

Verification:

- `go test ./internal/git ./internal/testutil -run 'Upstream|Remote|Clone|Initial|Manifest' -count=1`
- `go test ./internal/git ./internal/testutil -run 'Upstream|Remote|Clone|Initial|Manifest' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: init and clone can consume one complete typed, hermetic Git
boundary for upstream discovery, remote preflight, cloning, configuration, and
identity verification; no service or CLI directly parses Git output.

### [x] M02 — Publish portable manifests transactionally from `init`

Specification coverage: portable manifest clone specification §§2–4.

Scope:

- Extend init request/result and CLI flags for `--manifest-source` and repeated
  `--clone-url id=url`, using shared override parsing and deterministic errors.
- Discover upstream/clone/initial-commit metadata for every repository and
  construct both local and portable configs from one immutable init plan.
- Preflight all repositories, URLs, manifest source, registry collisions,
  `.gitignore`, target files, and writer feasibility before any publication.
- Extend init's locked publication/rollback so `.gitignore`, `.wtree.yml`,
  `project.wtree.yml`, registry, and default state become one logical result;
  preserve exact prior bytes/existence on injected failure.
- Render complete human/JSON dry-run and success output, with URL redaction and
  explicit review/commit guidance; never stage, commit, or push.
- Update init help/how-to and focused README authoring instructions in the same
  milestone. Do not advertise update/sync as implemented.

Test-first slices:

1. Initialize a root-only and a three-level project backed by local bare
   remotes; verify deterministic portable/local configs, source persistence,
   upstream metadata, initial roots, default state, and registry identities.
2. Missing/detached/ambiguous upstream, missing remote URL, invalid URL,
   unknown/duplicate override, mismatched branch, unpushed initial history, or
   credential-bearing value fails before all mutation with repository context.
3. `--clone-url` changes only the portable bootstrap URL; custom local and
   HTTP(S) manifest sources normalize/persist correctly without fetching them
   during authoring.
4. Dry-run JSON/human output contains the complete proposed metadata, creates
   no file/lock/state/ref/index mutation, and redacts every rejected secret.
5. Inject failure/cancellation at each ignore/local-config/portable-config/
   registry/default-state boundary and prove exact reverse rollback or complete
   rollback-incomplete evidence.
6. Concurrent/repeated init retains existing conflict semantics and never
   publishes two project IDs or mixed local/portable generations.

Verification:

- `go test ./internal/service ./internal/cli -run 'Init.*Manifest|Manifest.*Init|Initializer|CloneURL' -count=1`
- `go test ./internal/service ./internal/cli -run 'Init.*Manifest|Manifest.*Init' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: one `wtree init` safely and deterministically publishes a
reviewable portable manifest plus compatible local state from verified pushed
repositories, with full dry-run and rollback evidence.

### [x] M03 — Build bounded manifest loading and immutable clone planning

Specification coverage: portable manifest clone specification §§5–7.

Scope:

- Implement the single local/HTTP(S) manifest source parser/loader with exact
  normalization, size/time/redirect/TLS/userinfo policies and redacted errors.
- Define validated immutable clone plan/result types with stable version-one
  JSON, parent-first repositories/actions, source provenance, destination,
  remote refs, exact advertised commits, mounts, and verification expectations.
- Implement complete read-only planning: load/decode manifest, validate graph
  and values, resolve destination/default, inspect registry collisions, query
  every remote branch, and accumulate deterministic repository-scoped errors.
- Guarantee planning and dry-run create no destination/staging/temp/lock/state
  artifacts or Git mutations. Network reads are permitted and reported.
- Add service-level seams for HTTP client, filesystem facts, registry read,
  and Git remote facts without making production callers construct unsafe
  partial services.
- Do not register the CLI command or execute clones in this milestone.

Test-first slices:

1. Load equivalent local and HTTP manifests and return normalized persisted
   sources; reject directories/symlinks/oversize files, unsupported schemes,
   URL userinfo, status errors, timeout, redirect loops/excess, downgrade, and
   oversized/chunked responses with redacted bounded diagnostics.
2. Plan explicit and default destinations, including spaces/Unicode; reject
   unsafe project-name defaults, existing destinations, symlink ancestors,
   unwritable/non-directory parents, broad paths, and registered aliases.
3. Plan root-only and three-level repositories in stable parent-first order,
   record the full commit advertised for differently named remote branches,
   and reject one missing/failed remote before any filesystem mutation.
4. Decode JSON to the stable contract and prove all arrays/order/source values;
   credential-shaped invalid inputs never appear unredacted.
5. Compare destination parent, registry/state/lock trees, Git refs/indexes, and
   timestamps before/after plan and dry-run, including failure paths.
6. Race concurrent planners against registry/destination changes and ensure the
   plan captures facts for later locked revalidation without claiming mutation
   safety prematurely.

Verification:

- `go test ./internal/service ./internal/config -run 'ManifestSource|ClonePlan|CloneDryRun' -count=1`
- `go test ./internal/service -run 'ManifestSource|ClonePlan' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: local and HTTP(S) sources produce one fully validated,
deterministic clone plan; every unsafe/unavailable input fails read-only; the
executor needs no raw CLI or unvalidated manifest decisions.

### [x] M04 — Execute clone through private staging and atomic publication

Specification coverage: portable manifest clone specification §§8–10.

Scope:

- Implement clone execution with local pre-effect revalidation, unique
  same-parent private staging, exact-planned-commit parent-first Git effects,
  publication-locked conflict/path revalidation, and progress events. Do not
  retain the global registry lock across remote access.
- Verify root tracked-manifest byte identity, each immediate parent's committed
  ignore rule before its child path exists, repository initial roots, URL,
  branch/upstream, mount, clean status, and submodule absence.
- Write/verify local `.wtree.yml` in staging, atomically rename the complete
  root into the final destination, re-resolve final common Git directories,
  then publish default state and registry last under established lock order.
- Build exact reversible transaction effects and recovery evidence for staging,
  destination, registry, and state; revalidate containment and identities
  before cleanup and never remove pre-existing paths.
- Handle cancellation at every safe boundary and run cleanup without the
  canceled context. Keep human/JSON rendering out of the service.
- Provide injected filesystem/Git/store/lock seams and real local-remote E2E
  fixtures. Do not yet register the public CLI command.

Test-first slices:

1. Clone root-only and three-level projects from local bare remotes, including
   non-origin remote names and different upstream branch names; verify final
   config/state/registry, paths, identities, clean status, and no staging
   residue.
2. Reject stale/unpublished root manifest, a moved/deleted branch whose planned
   commit cannot be obtained, missing immediate-parent ignore, wrong initial
   roots, submodules, dirty/unexpected branch/upstream, and mount conflicts
   before final publication; never silently use a replacement branch tip.
3. Inject failure before/after every staging creation, repository clone,
   verification, local-config write, destination rename, final identity check,
   state write, and registry write; prove exact cleanup/restoration or complete
   recovery evidence.
4. Cancel at every effect boundary and prove cleanup completes; force cleanup
   may target only identity-verified paths created by this transaction.
5. Race same/different destination and project-ID clones; exactly one conflicting
   operation wins, unrelated clones remain consistent, and long remote
   operations do not unnecessarily serialize on the global registry lock.
6. Simulate symlink swaps and concurrent destination/registry changes between
   planning and locked execution; fail without deleting or overwriting the
   attacker/user path.

Verification:

- `go test ./internal/service ./internal/git ./internal/store -run 'Clone|ManifestIdentity' -count=1`
- `go test ./internal/service ./internal/git ./internal/store -run 'Clone|ManifestIdentity' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: the service can materialize and register a complete verified
multi-repository project transactionally from an immutable plan, and every
effect/failure/cancellation/concurrency boundary has safety evidence.

### [x] M05 — Expose `wtree clone` and align public documentation

Specification coverage: portable manifest clone specification §§1 and 5–10.

Scope:

- Register `clone`, exact arguments/flags, option rejection, runtime path
  resolution, dry-run, JSON, verbose progress, clean/incomplete rollback
  diagnostics, and stable exit mapping through the M03/M04 services.
- Implement deterministic human plan/success output and decoded JSON contract
  tests. Success names the project, final destination, repository count, and
  stored manifest source without printing secrets.
- Add complete root/per-command help and command/global `--how-to`; ensure every
  advertised clone example parses and executes in hermetic tests.
- Rewrite README and tutorial so clone is documented as available, while
  `update`, `sync`, and release locking are explicitly future ideas rather than
  commands users are instructed to run.
- Add local-file and `httptest` black-box CLI E2Es, including nested invocation
  context, explicit/default destination, dry-run, JSON, errors, rollback, and
  path/repo/status usability immediately after clone.
- Update specification and traceability from placeholders to exact enforcing
  packages/tests. Keep idea documents labeled as ideas and preserve unrelated
  plan/spec documents.

Test-first slices:

1. `wtree clone manifest destination` and default-destination forms complete
   through the public process boundary; the resulting project supports `path`,
   `repo path`, `status`, and later workspace creation.
2. HTTP source, custom worktree root/data dir, verbose human, dry-run human,
   dry-run JSON, success JSON, and operational/validation JSON errors obey
   stdout/stderr and redaction contracts.
3. Reject missing/extra args, root `--project`, `--force`, `--mount`, `--from`,
   unsupported source schemes, existing destinations, and unknown flags with
   stable exit codes and no mutation.
4. Every help/how-to/README/tutorial clone example is executable; no shipped
   document claims update, sync, or release-lock commands currently exist.
5. Clone a three-level fixture from a served manifest, verify every mount is
   ignored by its parent and no outer `git add .` can stage an embedded-repo
   gitlink, then exercise normal workspace lifecycle commands.

Verification:

- `go test ./cmd/wtree ./internal/cli ./internal/service -run 'Clone|Help|HowTo|EndToEnd' -count=1`
- `go test ./cmd/wtree ./internal/cli ./internal/service -run 'Clone' -race -count=1`
- Apply every command in the global definition of done.

Exit criteria: the installed CLI truthfully exposes a safe local/HTTP clone
workflow; all examples work; cloned projects are immediately usable by current
commands; deferred commands are no longer advertised as implemented; and all
prior milestones remain green.

### [x] M06 — Perform final portability, security, and acceptance review

Specification coverage: the complete portable manifest clone specification
and completed clone traceability mapping.

Scope:

- Have the reviewer perform a clean-room pass from the idea, fixed decisions,
  updated specification, and public CLI rather than only reviewing accumulated
  diffs.
- Re-run all unit, integration, race, fuzz-smoke, CLI, release-build, and
  end-to-end checks; inspect Linux/macOS/Windows CI for platform-specific
  staging/rename/path/mode/symlink behavior.
- Audit credential and bounded-input handling across manifest source, redirects,
  repository URLs, Git errors, progress, JSON, human output, and recovery data.
- Audit every destructive cleanup target, containment/identity recheck, lock
  order, durable publication boundary, cancellation path, and injected failure
  result.
- Verify config/store backward compatibility, portable manifest determinism,
  current version `0.2.0`, documentation truthfulness, and complete
  traceability. Do not publish artifacts or create commits.

Test-first slices:

1. Run a clean-room author → publish-to-local-remotes → local clone → HTTP
   clone workflow and compare resulting repository identities, branches,
   mounts, configs, and status.
2. Re-run the adversarial source/path/URL/symlink/concurrency/cancellation/
   rollback matrix and confirm no credential leak, pre-existing-path deletion,
   partial registration, or mixed project generation.
3. Generate the same portable manifest repeatedly on every available platform
   and confirm byte identity; use named CI evidence for remaining OS-specific
   cases.
4. Build release artifacts locally, run each applicable binary/version/help
   smoke check, and verify no publication side effect.

Verification:

- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`
- `make check`
- `./scripts/release-build_test.sh`
- `go test ./internal/config -run '^Fuzz' -fuzz=Fuzz -fuzztime=5s`
- `go test ./internal/pathutil -run '^Fuzz' -fuzz=Fuzz -fuzztime=5s`
- `git diff --check`

Exit criteria: every clone requirement has implementation and automated or
named CI evidence; the reviewer approves security, rollback, portability,
compatibility, docs, and traceability; all earlier milestones remain green;
and no update/sync/release-lock implementation or external publication was
introduced.

## Execution log

Append entries during execution; do not rewrite earlier evidence.

| Date | Milestone | Verification | Review | Commit |
|---|---|---|---|---|
| 2026-08-16 | M00 | Focused config/domain/path tests, focused race, 5-second config fuzz, vet, full tests, full race, `make check`, and release-build passed. M00-scoped whitespace check passed; global whitespace check was obstructed only by preserved unrelated `docs/ideas/workflow/final-reviewer.md`. | Approved by normal reviewer after user-authorized R1/R6 remediation; R1–R6 resolved with no material findings. | Not committed (not authorized). |
| 2026-08-16 | M01 | Focused Git/testutil tests and race, vet, full tests, full race, `make check`, and release-build passed. M01-scoped whitespace passed; global whitespace retained only the unrelated idea-file obstruction. | Approved by normal reviewer after R1–R3 remediation for hook suppression, optional-lock remote facts, and complete adversarial coverage. | Not committed (not authorized). |
| 2026-08-16 | M02 | Plan-focused init/manifest tests and race, repeated rollback injection, Windows cross-build, diff checks, vet, uncached full tests, uncached full race, `make check`, and release-build passed. Main post-approval verification repeated focused/race, diff, vet, release, full/full-race, and `make check` successfully. | Approved by normal reviewer after R1–R7 remediation, including exact owned-ignore source attribution, umask-aware creation, durable modes, and deterministic rename-boundary rollback. | Not committed (not authorized). |
| 2026-08-16 | M03 | Exact/expanded/repeated focused normal/race, scoped whitespace, vet, uncached full/full-race, `make check`, release-build, and Windows cross-build passed. Main post-approval verification repeated focused/race, scoped whitespace, vet, release, full/full-race, and `make check`; global whitespace remained obstructed only by unrelated `docs/ideas/bugs.md`. | Approved by normal reviewer after R1–R5 remediation for ancestor safety, self-contained ignore actions, genuine plan-result outcomes, stable registry generations, and bounded remote diagnostics. | Not committed (not authorized). |
| 2026-08-16 | M04 | Exact/expanded focused normal/race, repeated adversarial race, scoped whitespace, vet, uncached full/full-race, `make check`, release-build, and Windows cross-build passed. Main post-approval verification repeated focused/race, scoped whitespace, vet, release, full/full-race, and `make check`; global whitespace remained obstructed only by unrelated `docs/ideas/bugs.md`. | Approved by normal reviewer after R1–R6 remediation for identity-bound store rollback, staging publication, complete cleanup refusal, exact config/recovery collision handling, and terminal progress semantics. | Not committed (not authorized). |
| 2026-08-17 | M05 | Public CLI/help/how-to/docs/local+HTTP E2E and M04 root-metadata remediation focused normal/race passed. Clean full normal/race, vet, `make check`, release build, and Windows AMD64 cross-build passed; global whitespace remained obstructed only by unrelated `docs/ideas/bugs.md`. | Approved by normal reviewer after M05-R1 remediation: atomic rename is a path transition only; real post-rename root mutation yields rollback-incomplete and retained destination. | Not committed (not authorized). |
| 2026-08-17 | M06 | Clean-room acceptance, adversarial transaction/credential/portability review, full normal/race, vet, check, release, fuzz smoke, and Windows AMD64 evidence passed. | Approved after reset M06-R1 review: both pre- and post-capture replacements retain attacker bytes and report rollback-incomplete; R3/R4 remain resolved. | Not committed (not authorized). |
