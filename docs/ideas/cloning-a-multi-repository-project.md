# Idea: clone and synchronize a multi-repository project

Status: design idea; not an accepted specification or implementation plan.

## Summary

`wtree` should distinguish between two configurations:

- `.wtree.yml` is the ignored, machine-local project configuration used by
  normal `wtree` commands.
- `project.wtree.yml` is a portable manifest that can be committed at the root
  of the parent repository and distributed to other developers.

After the developer who assembles a multi-repository project has pushed every
repository and configured each checkout to track the correct upstream, one
`wtree init` run should collect everything needed to reconstruct the project.
It should write both configurations and ensure that `/.wtree.yml` is ignored:

```sh
wtree init
git add .gitignore project.wtree.yml
git commit -m "Publish the wtree project manifest"
git push
```

Another developer can then reconstruct the complete checkout tree from either
a local manifest path or a URL whose response body is the manifest:

```sh
wtree clone ./project.wtree.yml ./acme-shop
wtree clone https://git.example.com/acme/project.wtree.yml ./acme-shop
```

Each repository remains independent, with its own remote, branches, and Git
worktree administration. This is deliberately different from Git submodules.

## Local configuration and portable manifest

The existing `source` field is a path to a known local checkout. It is useful
for administering worktrees but is not portable to another machine. The local
configuration may also contain machine-specific settings such as the worktree
root and registry state. It must therefore remain separate from the portable
manifest.

An initialized checkout could have this local `.wtree.yml`:

```yaml
version: 1

project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme-shop

manifest:
  path: project.wtree.yml
  source: https://git.example.com/acme/project.wtree.yml

repositories:
  root:
    source: .
    parent: null
    mount: .
    default_branch: main
  backend:
    source: backend
    parent: root
    mount: backend
    default_branch: main
```

`manifest.path` is the portable manifest in the root checkout that a project
maintainer updates and commits. `manifest.source` is the exact local path or
URL from which this clone was bootstrapped. `wtree clone` must store that
source so later synchronization does not require it to be entered again.

For the developer who originally runs `wtree init`, `manifest.source` defaults
to the generated `project.wtree.yml` path. It can be selected during
initialization, changed explicitly, or replaced during synchronization:

```sh
wtree init --manifest-source https://git.example.com/acme/project.wtree.yml
wtree config set manifest.source https://mirror.example.com/acme/project.wtree.yml
wtree sync --from ./replacement.wtree.yml
```

`wtree sync --from` uses the supplied source for that operation and, after a
successful confirmed synchronization, persists it as the new
`manifest.source`.

The portable `project.wtree.yml` contains no local source checkout paths or
worktree storage settings. It records the repository hierarchy and all data
needed to clone and verify it:

```yaml
version: 1

project:
  id: 3f97ab90-0d41-4bd1-84a8-4df70dbcd221
  name: acme-shop

repositories:
  root:
    clone:
      remote: origin
      url: git@github.com:acme/acme-shop.git
    upstream:
      branch: main
      remote: origin
      merge: refs/heads/main
    parent: null
    mount: .
    default_branch: main

  backend:
    clone:
      remote: upstream
      url: https://github.com/acme/java-backend.git
    upstream:
      branch: main
      remote: upstream
      merge: refs/heads/main
    parent: root
    mount: backend
    default_branch: main
```

The exact recorded URL is the bootstrap address. `wtree` must not silently
derive alternative SSH or HTTPS forms because those forms may use different
credentials, host aliases, or network routes.

## Initialization and upstream discovery

The project author must push every repository and connect the relevant local
branch to its intended upstream before running `wtree init`. For every
discovered repository, initialization should inspect Git and record:

- its hierarchy, stable logical ID, and default mount;
- its default branch;
- the upstream branch, remote name, and fetch URL used to clone it; and
- the data needed to verify the repository after cloning.

The configured upstream of the repository's current branch is the default
source of truth. An explicit per-repository override may be supplied when the
desired bootstrap remote differs. Initialization must fail with a diagnostic
listing the relevant branches and remotes when an upstream is absent or
ambiguous; it must not silently assume `origin`.

`wtree init` must also look for `.gitignore` in the root repository. If the
file does not exist, it creates it. If the anchored `/.wtree.yml` rule is not
already effective, it adds that rule without removing, reordering, or
rewriting unrelated user entries. A dry run reports the proposed change but
does not create or modify either configuration or `.gitignore`.

Only the local file is ignored. `project.wtree.yml` remains visible to Git so
the maintainer can review, commit, and publish it. The portable manifest must
not contain credentials embedded in URLs; diagnostics and rendered diffs must
redact any credentials encountered.

## Clone sources and source identity

`wtree clone <manifest-source> [destination]` accepts either:

- a local filesystem path; or
- an `http` or `https` URL that directly returns the manifest contents.

The source kind is parsed explicitly as a supported URL or a path; `wtree`
must not pass a manifest URL to Git as though it were a repository URL. URL
fetches need bounded redirects and response sizes, TLS verification, and
diagnostics that do not expose credentials. Relative repository URLs, if they
are supported later, must be defined relative to the manifest source rather
than the caller's working directory.

The exact source supplied to `clone` is stored in local `.wtree.yml`. For a
local path, `wtree` should store a cleaned absolute path so a later `sync`
behaves consistently from any working directory. A URL should retain its
fetch semantics while being normalized only where doing so cannot change
authentication or routing.

## Clone transaction

The clone operation should preflight every deterministic condition before it
changes the destination:

1. Fetch or read the manifest and validate its schema, repository tree,
   mounts, default branches, upstreams, and clone URLs.
2. Resolve the destination. It must not exist, or must satisfy a narrowly
   defined empty-directory policy.
3. Verify from the root commit that every child mount is ignored by the root
   repository's tracked `.gitignore` content.
4. Clone the root at its configured default branch and configure its recorded
   remote and upstream.
5. Clone children in parent-first order into their configured mounts and
   configure their recorded remotes and upstreams.
6. Verify every checkout's Git identity, resolved mount, branch, and upstream.
7. Write the ignored local `.wtree.yml`, including `manifest.source`, and only
   then register the result as the default `wtree` workspace.

The root `.gitignore` check must happen before child directories are created.
Otherwise an interrupted operation could make the root checkout appear dirty.
Projects must commit ignore rules for child mounts, for example:

```gitignore
/backend/
/frontend/
```

Before registration, `wtree` owns only directories created during this
invocation. If cloning fails, it may remove those known-created directories in
reverse order only after confirming they remain beneath the requested
destination. It must never remove a pre-existing destination or use a broad
recursive target assembled from an unchecked mount.

## Updating from the local repositories

`wtree update` is the authoring direction. It reinspects the local repository
tree and Git configuration, then proposes changes to both local `.wtree.yml`
and the root checkout's `project.wtree.yml`. This captures repositories that
were added or removed, changed mounts or default branches, and changed clone
or upstream information.

By default it displays a table comparing old and newly discovered values,
including unchanged context where useful, and asks the user to confirm or
reject the complete update. Rejection leaves both files unchanged. Acceptance
writes them as one logical transaction using temporary files and atomic
replacement; if both files cannot be replaced consistently, the prior content
must be restored and the failure reported.

```text
REPOSITORY  FIELD             OLD                         NEW
backend     clone.remote      origin                      upstream
backend     clone.url         git@example:old/api.git     git@example:new/api.git
frontend    repository        absent                      added
```

For tools, JSON mode uses the same discovery, comparison, and validation:

```sh
wtree update --json --dry-run   # print differences; write nothing
wtree update --json             # apply without an interactive prompt
```

The JSON document must state whether changes were found and applied and expose
stable repository, field, old-value, and new-value entries. A normal
`--dry-run` also shows the table without prompting or writing.

`wtree update` updates files in the working tree; it never commits or pushes
them. The maintainer reviews and publishes `project.wtree.yml` through the
normal Git workflow.

## Synchronizing a clone from its portable manifest

`wtree sync` is the consuming direction and is intentionally separate from
`update`. It reads `manifest.source` from local `.wtree.yml`, fetches or reads
the portable manifest again, and reconciles the local configuration and
checkout tree to it:

```sh
wtree sync
wtree sync --dry-run
wtree sync --from https://git.example.com/acme/project.wtree.yml
```

The name `sync` describes reconciliation of the complete multi-repository
project and avoids implying that `wtree` is merely running `git pull` in each
repository. A sync may add or remove repositories, change mounts, remotes,
upstreams, or default branches, and update the local configuration. It does
not merge or rebase ordinary repository commits unless a future option
explicitly requests that behavior.

Like `update`, interactive `sync` first shows an old/new table and asks for
confirmation. `--dry-run` performs all safe preflight and shows the proposed
changes without prompting or mutation. `--json --dry-run` returns the stable
machine-readable diff; `--json` applies a validated change without an
interactive prompt and reports the result.

Before applying, `sync` must preflight the complete transition. It may clone a
new repository parent-first. It may remove or relocate an old checkout only
when that checkout is clean, has the expected Git identity, and the user has
confirmed the explicit removal or move. Dirty worktrees, unpushed commits,
mount collisions, unreachable manifests or remotes, and identity mismatches
abort the entire logical update without modifying the project. Cleanup and
rollback follow the same containment rules as `clone`.

## Failure and safety policy

Network and credential failures are expected. Diagnostics should identify the
repository ID, redacted clone URL, destination mount, and Git failure without
exposing secrets. Configuration changes should preserve comments where the
configuration writer supports them; otherwise the command must clearly show
that the generated files will be normalized.

All three mutating operations perform exhaustive preflight before mutation:

- `clone` creates a new project from a portable manifest;
- `update` publishes observed local repository metadata into the two files;
- `sync` consumes a portable manifest into an existing project.

No operation should leave the local and portable views partially updated or
register a project before its repository tree has been verified.

## Open questions

1. Which repository identity evidence should the portable manifest record so
   a clone can detect a plausible but incorrect remote without preventing
   legitimate repository migration?
2. Should clone and sync support a manifest-pinned commit in addition to each
   repository's default branch?
3. Should multiple named URL profiles such as `ssh`, `https`, and an internal
   mirror be supported after the single-URL workflow is established?
4. Which credential-free HTTP caching metadata, if any, should be retained for
   conditional manifest fetches?

## Recommended incremental delivery

1. Split local and portable configuration models, add clone/upstream metadata,
   strict parsing, URL redaction, and `.gitignore` maintenance during `init`.
2. Make `init` discover configured upstreams and atomically generate both
   `.wtree.yml` and `project.wtree.yml`.
3. Add `wtree update` with interactive old/new comparison, JSON and dry-run
   modes, and transactional writes.
4. Add `wtree clone <path-or-URL> [destination]` with exhaustive preflight,
   parent-first cloning, verification, local source persistence, and rollback.
5. Add `wtree sync [--from <path-or-URL>]` with comparison, safe repository
   reconciliation, source replacement, JSON/dry-run modes, and rollback.
6. Cover local and HTTP manifests, missing or ambiguous upstreams, ignore-file
   creation and editing, credential redaction, child failures, dirty or
   pre-existing destinations, add/remove/move syncs, and registry rollback in
   hermetic integration fixtures.
