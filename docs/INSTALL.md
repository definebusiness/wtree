# Install wtree

## Build from source

Install the Go version declared in `go.mod`, then run:

```sh
go install ./cmd/wtree
wtree --version
wtree --help
wtree --how-to
```

## Build local release artifacts

Release builds are local only; they never publish artifacts or create a
release. Select an explicit version and run:

```sh
VERSION=1.2.3 make release
```

Artifacts are written to `dist/` with deterministic names:

```text
dist/wtree_1.2.3_linux_amd64
dist/wtree_1.2.3_darwin_amd64
dist/wtree_1.2.3_windows_amd64.exe
dist/SHA256SUMS
dist/LICENSE
dist/NOTICE
```

Use `DIST_DIR=/path/to/output` to select another output directory. Verify the
download or locally-built file with the SHA-256 value in `SHA256SUMS`, then put
the binary on `PATH`. On Windows the executable name ends in `.exe`.

Reusing `DIST_DIR` is safe: a successful build replaces only prior files whose
names match the documented Linux/macOS/Windows artifact schema, plus its own
`SHA256SUMS`, `LICENSE`, and `NOTICE`. Other files, including other `wtree_*`
names that do not match that schema, are preserved.

## Publish a public GitHub release

The `github-release` target builds the exact local release artifact set and
passes it to GitHub CLI. It does not create or push tags, change repository
visibility, commit changes, or push branches. It refuses to continue unless:

- the worktree is clean;
- `v<VERSION>` exists locally and identifies `HEAD`;
- the same tag is already present at the configured `REMOTE` (default
  `origin`) and identifies the same commit;
- `gh` is authenticated and the selected GitHub repository is public; and
- every expected artifact exists and passes `SHA256SUMS` verification.

Authenticate GitHub CLI, prepare the reviewed release commit, then create and
push an annotated tag:

```sh
gh auth login -h github.com
make check
git tag -a v1.2.3 -m 'wtree v1.2.3'
git push origin v1.2.3
```

Draft creation is the safe default:

```sh
VERSION=1.2.3 make github-release
gh release view v1.2.3 --web
```

After reviewing its notes and six uploaded assets, publish the draft through
GitHub CLI or the GitHub web interface:

```sh
gh release edit v1.2.3 --draft=false --latest
```

Set `PUBLISH=1` only when the release should become public immediately:

```sh
VERSION=1.2.3 PUBLISH=1 make github-release
```

`GH_REPO=owner/name` selects an explicit GitHub repository,
`REMOTE=upstream` selects the Git remote used for tag verification, and
`DIST_DIR=/path/to/output` selects the local artifact directory. The publisher
never makes a private repository public; change visibility separately only
after auditing the complete Git history, Actions logs, and repository data.

Publishing uses `gh release create --verify-tag --generate-notes`. It refuses
an absent remote tag instead of allowing GitHub CLI to create one from a
default branch. A failed or repeated creation remains visible for manual
inspection; the script never deletes, overwrites, or replaces an existing
release or asset.

`wtree` is licensed under the MIT License. The source and each locally built
release directory include `LICENSE` and `NOTICE`; the notice identifies Define
Business LTD as copyright holder and Marcel Linnenfelser with Codex as author.
Both notice files are included in `SHA256SUMS` alongside the binaries.

## Lifecycle-hook commands in an installed binary

Installed binaries include `wtree hooks list`, `wtree hooks share`, `wtree
hooks install`, and `wtree hooks retry`, plus clone's `--run-hooks` flag and
create's `--no-hooks` flag. Use `wtree hooks --how-to` and `wtree hooks --help`
to inspect the exact installed contract.
See the [lifecycle-hook tutorial](../tutorial/LIFECYCLE-HOOKS.md) for a complete
version-3 YAML example and the inspect/share/install/retry workflow.

Hook-free local and portable configuration remains version 2. Lifecycle hooks
are version 3 declarations: local `hooks.post-create` is trusted local setup,
portable `hooks.post-clone` needs an explicit `wtree clone --run-hooks`, and
portable `shared_hooks.post-create` is inert until `wtree hooks install` copies
it into local configuration. Version 4 adds an optional `companion: true`
repository marker while retaining the version-3 hook wire. A companion uses its
configured `default_branch` as its create baseline; `--from` remains an
ordinary-repository source. Adopt it by committing a tracked version-4 manifest
and running `wtree update`, or by cloning that manifest. No install or clone
action authorizes a shared hook by itself.

For the complete v4 migration and safety sequence, see the executable
[companion tutorial](../tutorial/COMPANIONS.md). It documents the separate
future-only `wtree repo branch` and best-effort `wtree companion update`
commands, their JSON v1 envelopes, and the absence of implicit fetching,
pushing, merging, rebasing, resets, force updates, or credential storage.

When a hook fails, the workspace remains published and the binary reports a
bounded retry command. Run `wtree status <workspace>` and `wtree doctor
<workspace>` first, then use `wtree hooks retry <workspace>` only after fixing
the cause. Hook programs must be idempotent. They are direct argument arrays,
not shell text. A separator-bearing portable executable must be source-relative,
tracked, and contained; a bare command uses sanitized `PATH` and `PATHEXT` on
Windows. Durable records and execution-result/error JSON omit command output,
arguments, executable paths, and environment values. List and plan/dry-run
inspection output intentionally exposes configured/resolved executables and
literal arguments for review.
