# Portable manifest clone specification

Status: implemented
Source idea: [Clone and synchronize a multi-repository project](../ideas/cloning-a-multi-repository-project.md)
Implementation plan: [Portable manifest clone implementation plan](../plans/portable-manifest-clone.md)
Current portable format: [Portable manifest v2 base-repository format](portable-manifest-v2-base-repository-format.md)

## 1. Purpose and scope

This specification defines the portable project manifest, its publication by
`wtree init`, and reconstruction of a complete multi-repository project with
`wtree clone`.

It covers local and HTTP(S) manifest sources. It does not specify `update`,
`sync`, release lock manifests, submodules, shallow or partial clones, or
adoption of existing destination directories.

The general safety, identity, path, transaction, output, and registry rules in
[`wtree.spec.md`](wtree.spec.md) continue to apply. The current automatic
ignore behavior for `init` and `create` is defined by the
[automatic nested mount ignore protection specification](automatic-nested-mount-ignore-protection.md).
The clone-specific committed-ignore requirement remains defined in section 8
below.

## 2. Local and portable configuration

`.wtree.yml` remains ignored, machine-local configuration. Its version-one
schema includes this optional block:

```yaml
manifest:
  path: project.wtree.yml
  source: /absolute/or/https/source
```

Existing version-one local configuration without `manifest` remains valid and
must not be rewritten merely because it is read. In version one,
`manifest.path` is exactly the clean root-relative path `project.wtree.yml`.
`manifest.source` is either a cleaned absolute local manifest path or the
normalized credential-free HTTP(S) URL from which the project was cloned.

`project.wtree.yml` is tracked, portable project configuration. It must not
contain local checkout paths, worktree-administration paths, data-directory
paths, common Git directories, credentials, or other implicit machine state.
An explicit absolute local path or `file://` URL is nevertheless permitted as
a repository clone transport.

## 3. Portable manifest schema

The current strict portable schema is version 2. The
[portable manifest v2 specification](portable-manifest-v2-base-repository-format.md)
defines the format transition and its deliberately limited topology. Its
schema is:

```yaml
version: 2
project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme-shop
  base_repository: root
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

The decoder must accept exactly one YAML document, reject unknown fields and
unsupported versions, and validate the entire project before any mutation.
Serialization must be deterministic: repository keys are written in lexical
order and initial commit arrays are sorted.

The repository graph must have exactly one root, contain no missing parents or
cycles, and use safe unique repository IDs and parent-relative mounts.
`project.base_repository` is required and names that sole root. The root mount
is `.` and every child mount is relative to its immediate parent. Sibling
repositories and a non-Git logical project root are not part of this format.

Each repository records exactly one clone remote and URL. The upstream remote
must equal the clone remote. `upstream.branch` is the local default branch;
`upstream.merge` is the full tracked remote ref and begins with
`refs/heads/`. The local and remote branch names may differ.

`identity.initial_commits` contains every full root commit reachable from the
selected default-branch commit. It is sorted and non-empty. Clone verification
requires the reconstructed history to contain every recorded root commit.

Credential-free HTTP(S), SSH/scp-style, absolute local, and `file://`
repository clone URLs are supported. Relative clone URLs, URL userinfo,
control characters, newlines, and credential-bearing forms are rejected.

## 4. Manifest publication by `init`

`wtree init [path]` supports:

```text
--clone-url <repository-id>=<url>   repeatable
--manifest-source <path-or-http(s)-url>
```

For every repository, init requires an attached current branch with a valid
configured upstream. It discovers the local branch, upstream remote, full
merge ref, remote fetch URL, and all initial commits without assuming the
remote is named `origin`.

The advertised upstream ref must equal the local default-branch `HEAD`. An
ahead, behind, diverged, deleted, or otherwise unpublished branch fails
preflight. Init must not publish a moving-branch manifest that differs from the
repository state it inspected.

`--clone-url` changes only the portable bootstrap URL of the named repository.
It does not bypass upstream discovery. Unknown or duplicate overrides are
invalid.

If `--manifest-source` is omitted, local configuration stores the cleaned
absolute path to the generated root `project.wtree.yml`. If supplied, the
value is validated and stored according to the manifest-source rules below;
init does not fetch it.

Init constructs `.wtree.yml` and `project.wtree.yml` from one immutable plan
and publishes them as one logical transaction with `.gitignore`, default
workspace state, and registry changes. It retains responsibility for ensuring
that root `.gitignore` ignores `/.wtree.yml`; it must not ignore the portable
manifest.

Complete preflight precedes every write. Failure restores the exact previous
bytes and existence of every affected file and store entry or reports complete
rollback evidence. `--dry-run` renders the proposed local and portable data
without mutation.

Init never stages, commits, tags, or pushes. Human success output tells the
user to review and commit `.gitignore` and `project.wtree.yml`.

## 5. Clone command

The command surface is:

```text
wtree clone <manifest-source> [destination]
  [--worktree-root <path>]
  [--data-dir <path>]
  [--dry-run]
  [--json]
  [--verbose]
```

`clone` rejects root `--project`, `--force`, `--mount`, and `--from`. It does
not accept discovery-ignore options because the manifest defines the complete
repository set.

With an explicit destination, clone resolves it relative to the caller's
working directory and canonicalizes its existing parent. Without one, it uses
`./<project.name>` only if the name is a safe single filesystem component.
Otherwise the caller must supply a destination.

The destination must not exist. Clone does not adopt an existing empty
directory. It rejects unsafe or broad paths, symlinks, aliases of registered
project/workspace paths, missing or unsuitable parents, and escapes through
existing symlink ancestors.

## 6. Manifest sources

A manifest source is either a local path or an absolute `http` or `https` URL.
Other URL schemes are invalid and are never passed to Git.

Local sources are read from cleaned absolute paths. Directories, symlinks,
devices, and files larger than 1 MiB are rejected.

HTTP(S) fetching uses a dedicated client with:

- a 30-second total timeout;
- at most five redirects;
- normal TLS certificate verification;
- no ambient cookies;
- a 1 MiB response limit; and
- rejection of HTTPS-to-HTTP downgrade redirects.

Non-2xx responses, invalid URLs, timeouts, redirect failures, and oversized
responses fail without exposing response bodies or secrets. Content type is
advisory; the response must pass strict YAML decoding.

Manifest-source URLs containing userinfo are rejected because the normalized
source is persisted. Diagnostics defensively redact URL userinfo and
credential-shaped repository URLs.

## 7. Read-only clone planning

Planning must:

1. load and strictly validate the manifest;
2. validate the destination and registry project identity;
3. resolve each declared upstream branch using read-only `git ls-remote`;
4. record the full advertised commit for every repository; and
5. produce a deterministic parent-first repository and action plan.

Planning performs no clone, directory, temporary-file, lock, registry, state,
ref, index, or configuration mutation.

`--dry-run` performs this complete plan and may contact declared manifest and
Git remotes. Human output lists the source, destination, repositories,
remotes, branches, mounts, observed commits, and verification steps, then ends
with `No changes made.` An observed commit is the selected branch tip reported
during preflight; it is diagnostic information, not an execution promise.

Clone plan and dry-run-result JSON use version two. They include operation
`clone`, normalized source, destination, project identity, deterministic
repositories with `observedCommit`, and ordered actions. They do not emit
`exactCommit` or `parentCommit` as execution claims. Completed clone JSON
reports the actual checked-out commit for every repository. Human stderr
remains empty in JSON mode, and errors use one JSON envelope on stdout.

## 8. Clone execution and verification

Real clone revalidates local preconditions and creates a uniquely named hidden
staging directory in the destination's existing parent. It must not hold the
global registry lock during remote Git operations.

Repositories are cloned into staging in parent-first order. Commands use
argument arrays and the hardened non-interactive Git environment, never a
shell. Execution initializes the configured remote without accepting an
implicit branch, fetches only the manifest-selected remote ref, and checks out
the tip obtained by that execution-time fetch. The remote symbolic `HEAD`
must not select or create a local branch. A selected branch may advance after
preflight and before the execution fetch; ordinary clone uses the newly
fetched tip rather than the earlier observed commit. A deleted selected ref,
or a replacement whose actual checkout cannot satisfy verification, fails
safely without fallback to another branch or stale preflight object.

The configured local default branch starts at the actual checked-out commit
and tracks the recorded remote and merge ref. Clone must not substitute the
remote's default branch.

Before creating a child checkout, clone inspects the already-cloned immediate
parent at its actual checked-out commit and requires that parent's committed
`.gitignore` content to ignore the effective child mount. Local and global
excludes do not qualify. A missing or ineffective rule fails before the child
path exists. Before retrying clone, users must add and commit the required
`.gitignore` rule using the ordinary repository workflow.

After each repository clone, verify:

- configured clone URL and remote name;
- local branch and upstream merge ref;
- clean worktree state;
- the complete initial-commit identity set;
- expected parent-relative mount; and
- absence of submodules.

The root's tracked `project.wtree.yml` at its actual checked-out commit must
be byte-identical to the fetched manifest. This rejects unpublished, stale, or
unrelated manifest copies. Identity, committed-ignore, tracked-manifest,
workspace-state, and completed-result checks are all bound to each
repository's actual `HEAD`.

After repository verification, clone writes ignored local `.wtree.yml` in
staging with the final source paths, manifest metadata, worktree-root setting,
and default checkout data. Committed root content must already ignore
`/.wtree.yml`; clone does not edit `.gitignore`.

## 9. Publication, state, and rollback

Immediately before publication, clone acquires locks in the established
registry-then-project order and revalidates destination absence, registry
conflicts, and local path identities. It does not re-read remote refs because
the selected refs have already been fetched and verified at their
execution-time tips.

The complete staged root is atomically renamed into the final destination.
Clone then verifies final canonical common Git directories and atomically
publishes default workspace state and the registry entry while retaining the
publication locks.

The default workspace records each actual branch, `HEAD`, mount, path, and
detached flag using the existing state schema. Registry data points to the
final `.wtree.yml` and final common Git directory identities. Existing project
ID, configuration-path, repository-identity, workspace-path, or storage-name
conflicts fail without overwrite.

The transaction owns only its unique staging path, the destination created by
its atomic rename, and store entries created by this invocation. Cleanup may
remove or restore only those exact objects after containment and identity
checks. It must never delete a pre-existing path.

Failure before publication removes staging. Failure after publication rolls
back registry/state and removes the known-created destination only after
identity revalidation. Any cleanup failure reports every retained artifact
through the established rollback-incomplete contract. Cancellation is checked
at safe boundaries and cleanup runs without the cancelled context.

## 10. Compatibility and non-goals

Local configuration remains at version 1 with optional manifest metadata. The
portable manifest uses schema version 2 and requires
`project.base_repository`, as defined by the
[portable manifest v2 specification](portable-manifest-v2-base-repository-format.md).
Registry, workspace state, workspace plan, and recovery schema versions do not
change.

Existing initialized projects whose local version-one configuration lacks
manifest metadata and all existing workspace commands remain compatible. A
successful clone becomes the default workspace only after every checkout and
final path has been verified.

This specification does not authorize third-party dependencies, repository
staging or commits, tags, pushes, publication, `update`, `sync`, release locks,
relative repository URLs, named URL profiles, interactive credentials,
submodules, sparse/partial/shallow clone, LFS orchestration, or destination
adoption.

## 11. Implementation traceability

| Contract | Enforcement | Focused evidence |
|---|---|---|
| portable and local schemas | `internal/config` | `portable_manifest_test.go`, `portable_manifest_fuzz_test.go` |
| init authoring and publication | `internal/service/init.go`, `internal/cli/root.go` | `init_manifest_test.go`, `init_publication_internal_test.go`, `init_manifest_test.go` in CLI |
| bounded local and HTTP sources | `internal/service/manifest_source.go` | `manifest_source_test.go` |
| read-only planning with version-two observed-commit JSON | `internal/service/clone_plan.go`, `internal/service/clone_result.go` | `clone_plan_test.go`, `clone_result_test.go`, registry-facts tests |
| selected-ref execution, actual-HEAD verification and result reporting, publication, and rollback | `internal/service/clone_execute.go`, `internal/git/portable.go` | clone-execute, clone-safety, Git portable, concurrency, and publication tests; [live-branch clone focused specification](clone-live-branch-and-upstream-status.md) |
| command grammar, output, help, and how-to | `internal/cli/clone.go`, `internal/cli/root.go`, `internal/cli/howto.go`, `internal/render` | clone CLI, help/how-to, error-envelope, and `cmd/wtree` process-boundary tests |
| nested local/served project lifecycle | public CLI boundary plus resolver/current workspace commands | three-level clone E2E covering parent ignores, `git add .`, `path`, `repo path`, `status`, and workspace creation |
