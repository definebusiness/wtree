# Install wtree

## Build from source

Install the Go version declared in `go.mod`, then run:

```sh
go install ./cmd/wtree
wtree --version
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
