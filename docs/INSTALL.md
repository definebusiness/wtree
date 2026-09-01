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

`wtree` is licensed under the MIT License. The source and each locally built
release directory include `LICENSE` and `NOTICE`; the notice identifies Define
Business LTD as copyright holder and Marcel Linnenfelser with Codex as author.
Both notice files are included in `SHA256SUMS` alongside the binaries.

## Lifecycle-hook commands in an installed binary

Installed binaries include `wtree hooks list`, `wtree hooks share`, `wtree
hooks install`, and `wtree hooks retry`, plus clone's `--run-hooks` flag and
create's `--no-hooks` flag. Use `wtree hooks --how-to` and `wtree hooks --help`
to inspect the exact installed contract.

Hook-free local and portable configuration remains version 2. Lifecycle hooks
are version 3 declarations: local `hooks.post-create` is trusted local setup,
portable `hooks.post-clone` needs an explicit `wtree clone --run-hooks`, and
portable `shared_hooks.post-create` is inert until `wtree hooks install` copies
it into local configuration. No install or clone action authorizes a shared
hook by itself.

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
