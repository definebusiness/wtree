# Release locks: local source to exact CI composition

This offline tutorial uses the Acme Shop base repository and its two independent
non-base repositories, `backend` and `frontend`. It records reproducible source
only. Build, test, package, sign, publish, deploy, and notify remain explicit
caller or CI actions; `wtree` never performs them implicitly.

Run the executable counterpart from the repository root:

```sh
make tutorial-test
```

The tutorial uses temporary local bare Git remotes and fake authentication
helpers. It never contacts a service or uses a real credential.

## 1. Start with a complete clean local workspace

Follow [the main tutorial](README.md) through cloning the fixture. It leaves
these variables available:

```sh
export WTREE_PROJECT="$WTREE_TUTORIAL/acme-shop"
export WTREE_DATA_HOME="$WTREE_TUTORIAL/wtree-data"
cd "$WTREE_PROJECT"
git status --short
git -C backend status --short
git -C frontend status --short
```

All three checks must be clean. A release lock captures the current local
non-base commits sequentially without fetching and does not claim an atomic
snapshot. A partial workspace, a dirty checkout, a manifest mismatch, or an
unexpected mount is refused rather than guessed at.

## 2. Declare local idempotent child tagging

The hook declaration belongs only in ignored local `.wtree.yml`, version 3.
Each non-base repository has its own declaration using the same program found
through `PATH`; never add this local automation to `project.wtree.yml`.

```yaml
version: 3
# Preserve the generated project, logical_root, repositories, worktrees, and
# manifest fields in .wtree.yml.
hooks:
  post-release:
    - id: tag-backend-release
      repository: backend
      command: [tag-wtree-release]
      timeout: 5m
    - id: tag-frontend-release
      repository: frontend
      command: [tag-wtree-release]
      timeout: 5m
```

The tutorial fixture places this idempotent helper on `PATH`. It validates
`WTREE_RELEASE_NAME`, creates an annotated tag at the authoritative
`WTREE_HEAD`, accepts an already matching peeled tag, and rejects a collision
without moving it. It never tags the base repository and never pushes.

## 3. Freeze and review a local release

Dry-run validates and renders the candidate without writing a lock or running
the hook. The real invocation writes `project.wtree.lock.yml` and only then
runs the trusted local hooks. `--no-hooks` is the intentional one-run bypass.

```sh
wtree release lock v1.4.0 --dry-run --json
wtree release lock v1.4.0
git diff -- project.wtree.lock.yml
git add project.wtree.lock.yml
git commit -m 'chore: lock release v1.4.0'
git tag -a v1.4.0 -m 'Release v1.4.0'
```

Rerunning `wtree release lock v1.4.0` accepts unchanged lock bytes and matching
child tags. A child tag resolving to another commit fails without moving it.
For the next release, once the prior lock is clean and tracked, an ordinary
`wtree release lock v1.4.1` replaces it without `--force`; an untracked or
locally modified lock is protected and requires an intentional `--force`.
If a hook fails, the lock may already be valid: inspect it, correct the
external cause, and rerun only an idempotent hook sequence.

## 4. Publish reachability in caller-controlled order

After review, publish the child commits and child tags before the base tag.
This is deliberately explicit and not atomic across repositories:

```sh
git -C backend push origin HEAD refs/tags/v1.4.0
git -C frontend push origin HEAD refs/tags/v1.4.0
git push origin HEAD refs/tags/v1.4.0
```

If a locked child commit is unavailable from an advertised branch or tag, CI
fails rather than directly fetching an object ID or substituting a branch tip.
Restore the child publication and rerun from a fresh base checkout.

## 5. Reconstruct exact source in clean CI

Check out the base tag into an empty CI directory. The base must be clean and
track byte-identical `project.wtree.yml` and `project.wtree.lock.yml`; child
destinations and prior registry/state must be absent.

```sh
git clone "$WTREE_TUTORIAL/origins/acme-shop.git" "$WTREE_TUTORIAL/ci/acme-shop"
cd "$WTREE_TUTORIAL/ci/acme-shop"
git checkout --detach v1.4.0
export WTREE_DATA_HOME="$WTREE_TUTORIAL/ci/wtree-data"
wtree release materialize project.wtree.lock.yml --json
git -C backend status --short --branch
git -C frontend status --short --branch
wtree exec -- git rev-parse HEAD
```

Materialization uses Git-owned noninteractive authentication. An SSH agent,
askpass helper, or configured credential helper may provide access; `wtree`
stores no secret and exposes no credential option. It fetches advertised refs,
creates exact detached children, and runs neither lifecycle hooks nor a
post-materialize hook. A successful command itself verifies the composition;
there is no `release verify` command.

Run normal CI work explicitly afterwards:

```sh
wtree exec -- go test ./...
# Then run the pipeline's own package, signing, publication, and deployment steps.
```

## Troubleshooting

- Authentication failure: configure the CI Git SSH-agent, askpass, or
  credential-helper path. Do not put secrets in manifests, locks, or arguments.
- Unavailable commit: publish the reviewed child commit/tag before the base
  tag; materialization only accepts advertised reachability.
- Manifest mismatch, dirty base, or occupied destination: start from the exact
  clean base tag in a fresh directory. Materialize does not repair or convert
  another workspace.
- Tag collision: do not force-move it. Compare its peeled commit with the
  expected child `WTREE_HEAD` and resolve the release identity.
- Partial cleanup: retain the recovery evidence and resolve the reported
  authority conflict before retrying; do not call the workspace complete.
- Detached children: this is intentional for release source. Use explicit CI
  commands, not branch-oriented development operations.

The broader [troubleshooting guide](../docs/TROUBLESHOOTING.md#release-lock-and-materialization)
covers the same boundaries alongside ordinary workspace recovery.
