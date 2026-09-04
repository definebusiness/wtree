package cli

import (
	"fmt"
	"io"
)

const globalHowTo = `WTREE HOW-TO

1. What wtree is
   wtree manages one logical workspace across independent, nested Git repositories.
2. Initialize and publish an existing project
   Configure and push every upstream, then run wtree init and commit project.wtree.yml.
3. Keep local configuration private
   init creates or updates .gitignore for /.wtree.yml; project.wtree.yml is portable and tracked.
4. Configure worktree storage
   Use config set worktrees.root <path>, or pass --worktree-root when creating.
5. Clone a published project
   Run: wtree clone ./project.wtree.yml ./product
6. Preview a clone without changing anything
   Run: wtree clone https://example.invalid/project.wtree.yml --dry-run --json
7. Create a workspace
   Run: wtree create feature/login
8. Create from HEAD
   The default --from HEAD creates branches from each repository's current HEAD.
9. Create from another branch/ref
   Run: wtree create feature/login --from main
10. Override nested repository mounts
   Run: wtree create feature/login --mount backend=api
   Create automatically ensures the effective /api/ rule in the new parent
   worktree before adding the child; it never changes the source checkout.
11. Work inside a workspace
   Jump to a branch workspace: cd "$(wtree path feature/login)"
   Jump back to the original clone (the default workspace): cd "$(wtree path default)"
12. Resolve workspace paths
    Run: wtree path feature/login
13. Resolve repository paths
    Run: wtree repo path backend
14. Inspect status
    Run: wtree status feature/login --json
	    STATUS reports working-tree and structural state; UPSTREAM reports
	    last-fetched local upstream facts. Local drift is compared with the
	    locally tracked manifest when available. status does not fetch or contact remotes.
15. Import an existing workspace
    Run: wtree import /path/to/workspace --name feature/login
16. Import renamed nested checkouts
    Import maps checkouts by Git identity, so a configured backend may be mounted as api.
17. Remove a workspace
    Run: wtree remove feature/login. Branches and retained state remain for checkout.
18. Restore an existing branch with checkout
    Run: wtree checkout feature/login
19. Delete workspace and branches
    Run: wtree delete feature/login. Use --force only for the named safety overrides.
20. Diagnose inconsistencies
    Run: wtree doctor feature/login. Use --fix only for listed safe repairs.
21. Use --dry-run
    Add --dry-run to render and validate an operation without mutation.
22. Use --json
    Add --json where supported for machine-readable output; errors use a stable envelope.
23. Use wtree from nested directories
    wtree resolves the project from the current repository identity and workspace state.
24. Use --project explicitly
    Run: wtree <command> --project /path/to/project when context is ambiguous.
25. AI coding agent workflow
    Create, resolve with path, work in the checkout, inspect status, then remove or delete.
26. Inspect registered projects
    Run: wtree project list. It reports registry inconsistencies without changing projects or Git data.
27. Prune only a stale registry registration
    Run: wtree project prune <project-id> --dry-run. It removes no Git worktree,
    repository, project config, workspace state, recovery data, or lock file.
28. Intentionally unregister a project registration
    Run: wtree project unregister <project-id> --dry-run. It retains every project
    artifact; the retained local config can register the project again after a later
    mutating command is run from that project.
29. Run a direct command across a workspace
    Run: wtree exec -- go test ./...
    exec has no implicit shell; arguments are literal. Use sh -c explicitly
    when needed. It cannot roll back effects made by the invoked program.
30. Refresh configured upstream facts explicitly
    Run: wtree fetch --dry-run --json
    fetch contacts only each configured remote/ref and updates only its
    remote-tracking ref. It never moves a local branch, HEAD, or worktree;
    earlier successful fetches remain if a later repository fails. status
    remains network-free and reports last-fetched facts.
31. Check manual publication readiness
    Run: wtree push --json
    push reports whether every checkout is already at its exact configured
    upstream tip. It never runs git push, fetches, or creates refs or tags;
    publication remains manual until a future publishing workflow is specified.
32. Manage lifecycle hooks safely
    Local .wtree.yml version 3 may define post-create hooks. They run after a
    successful create unless --no-hooks is supplied. Portable project.wtree.yml
    version 3 may define post-clone hooks, but clone runs them only for that
    invocation when --run-hooks is supplied. shared_hooks are inert until
    explicitly installed locally with wtree hooks install. Use wtree hooks list
    to inspect definitions, wtree hooks share post-create to distribute a
    portable shared definition, and wtree hooks retry <workspace> only to resume
    a recorded incomplete run. Keep every hook idempotent: an interruption after
    a child side effect can require that hook to run again. Commands are direct
    argument arrays, not shell text; review literal command arguments before
    sharing. Portable hooks receive a sanitized environment. wtree never stores
    environments, literal command arguments, executable paths, or command output
    in durable hook-run records or execution-result/error JSON. hooks list and
    create/clone plan or dry-run inspection intentionally show configured or
    resolved executables and literal arguments.
33. Important safety semantics
    Every available mutation preflights first; destructive reconciliation requires explicit intent.
34. Update a project safely
    Run: wtree update. It captures one complete snapshot and only then applies
    the transaction. Use --dry-run to render the plan. Both modes refuse dirty, divergent, missing,
    structurally inconsistent, unresolved-operation, and unsafe repository-set
    changes. It never relocates an existing checkout or deletes a repository
    removed from the manifest; removed repositories remain retained unmanaged.
35. Compose reproducible release source
    Run: wtree release lock v1.4.0 --dry-run, then wtree release lock v1.4.0.
    The lock records the exact non-base commits in one clean local workspace;
    it does not fetch, commit, tag, push, publish, deploy, or claim an atomic
    cross-repository snapshot. Review and commit the lock yourself.
36. Publish child reachability before the base release
    A local post-release hook may tag a child, but it is trusted caller
    automation. Push reviewed child commits and tags before committing and
    tagging the base release. Matching child tags are safe to rerun; a tag at
    a different commit is a collision and must be resolved without moving it.
37. Materialize exact source in CI
    Start from a clean CI-provided base checkout containing the tracked
    project.wtree.yml and project.wtree.lock.yml, then run:
    wtree release materialize project.wtree.lock.yml
    Git-owned noninteractive authentication (for example an SSH agent,
    askpass helper, or configured credential helper) obtains advertised refs.
    wtree stores no credentials and has no credential flags. Materialization
    creates exact detached children and runs no lifecycle or post-materialize
    hook. It is the verification boundary: there is no release verify command.
38. Run ordinary CI work explicitly
    After successful materialization, run build/test/package/publish steps as
    explicit CI commands, for example: wtree exec -- go test ./... . wtree is
    reproducible source composition, not a release-management platform.
`

var commandHowTo = map[string]string{
	"clone": `HOW TO: clone

Clone every repository declared by a committed portable manifest and register the verified checkout as the default workspace. The source may be a local file or an HTTP(S) URL. Dry-run reports observed remote commits; execution reports the actual commits checked out from the selected branches. Omit destination only when the manifest project name is a safe directory name.

EXAMPLES
  wtree clone ./project.wtree.yml ./product
  wtree clone https://example.invalid/project.wtree.yml --dry-run --json
  wtree clone ./project.wtree.yml --worktree-root /worktrees --data-dir /wtree-data
`,
	"update": `HOW TO: update

Reconcile the default workspace against its portable manifest source. Use
--from to select a replacement source; on successful execution it becomes the
new manifest.source. Use --dry-run to inspect the plan without mutation.

EXAMPLES
  wtree update
  wtree update --from ./next.wtree.yml --dry-run --json

The command refuses dirty, divergent, missing, structurally inconsistent,
unresolved-operation, and unsafe repository-set changes. Execution is journaled
and publishes matching local configuration, workspace state, registry, and
retained-checkout records only after repository effects succeed. Existing
checkouts are never relocated and repositories removed by a manifest remain
retained unmanaged; deletion requires a separately specified destructive
operation.
`,
	"init": `HOW TO: init

Discover the complete local repository tree after every repository has been pushed and connected to its intended upstream. Automatically protect every nested mount in its immediate parent .gitignore, write ignored .wtree.yml and portable project.wtree.yml, then review and commit the changed .gitignore files yourself. init never stages or commits them.

EXAMPLES
  wtree init
  wtree init --worktree-root /worktrees
  wtree init --clone-url backend=file:///repos/backend.git
  wtree init --manifest-source https://example.invalid/project.wtree.yml
  wtree init --dry-run --json

If .wtree.yml was removed but this checkout remains registered, init refuses to
publish another project ID. Inspect the registration with wtree project list.
Use wtree project prune <id> only for an objectively stale registration, or
wtree project unregister <id> for intentional registry-only removal; neither
operation deletes Git repositories, worktrees, configuration, or state. Once
the intended registration is explicitly removed, retry wtree init.
`,
	"create": `HOW TO: create

Create a synchronized branch and parent-first worktrees after preflight succeeds. Create automatically ensures each nested mount is ignored in its new parent worktree; --dry-run lists those requirements without mutation.

EXAMPLES
  wtree create feature/login
  wtree create feature/login --from main
  wtree create feature/login --mount backend=api --dry-run

Custom mounts are normalized and validated during preflight. Create ensures their literal rules in the new parent worktrees; it never changes source checkouts.
`,
	"import": `HOW TO: import

Observe an existing checkout tree and persist only its verified Git identities, mounts, branches, and HEADs.

EXAMPLES
  wtree import /work/login --name feature/login
  cd /work/login && wtree import --name feature/login
  wtree import /work/login --allow-partial --name feature/login --dry-run
`,
	"remove": `HOW TO: remove

Remove worktree directories child-first while retaining branches and workspace state for a later checkout.

EXAMPLES
  wtree remove feature/login
  wtree remove feature/login --dry-run
  wtree remove feature/login --force
`,
	"delete": `HOW TO: delete

Remove worktrees, delete branches, and remove retained state. Dirty or unmerged overrides are reported per repository.

EXAMPLES
  wtree delete feature/login
  wtree delete feature/login --dry-run
  wtree delete feature/login --force
`,
	"doctor": `HOW TO: doctor

Compare persisted expectations with Git and filesystem facts. --fix applies only the explicitly listed safe repairs.

EXAMPLES
  wtree doctor feature/login
  wtree doctor feature/login --json
  wtree doctor feature/login --fix --dry-run
`,
	"push": `HOW TO: push

Check whether every selected checkout is already ready for manual publication.
The command compares only the configured upstream ref and never runs git push,
fetches, creates refs or tags, or changes workspace state.

EXAMPLES
  wtree push
  wtree push --workspace feature/login --json

Resolve any reported readiness finding, then publish repositories manually.
Coordinated publication remains a future workflow.
`,
	"project": `HOW TO: project

Inspect globally registered projects and their registry diagnostics. To remove an objectively stale registration only, inspect its complete read-only plan with prune. To intentionally remove any exact registration, use unregister. Neither operation deletes Git worktrees, repositories, project configuration, workspace state, recovery data, or lock files. After unregister, the retained local configuration can register the project again when a later mutating command runs from it.

EXAMPLES
  wtree project list
  wtree project list --json
  wtree project prune stale-project-id --dry-run
  wtree project prune stale-project-id --json
  wtree project unregister project-id --dry-run
  wtree project unregister project-id --json
`,
	"hooks": `HOW TO: hooks

Inspect, explicitly share, install, and safely resume lifecycle hook setup.
Local .wtree.yml version 3 accepts only post-create hooks. They run after a
successful create unless --no-hooks is supplied. Portable project.wtree.yml
version 3 accepts post-clone hooks, but clone runs them only for the explicit
invocation that supplies --run-hooks. Portable shared_hooks are never executed
directly: install them into the ignored local configuration first.

EXAMPLES
  wtree hooks list
  wtree hooks share post-create
  wtree hooks install --missing
  wtree hooks retry <workspace>

Hook commands are direct argument arrays, not shell syntax. Make hooks
idempotent because an interruption after a child side effect can require a
hook to run again. Review literal command arguments before sharing. Portable
hooks use a sanitized environment; wtree never stores environments, literal
command arguments, executable paths, or command output in durable records or
execution-result/error JSON. hooks list and create/clone plan or dry-run
inspection intentionally show configured or resolved executables and literal
arguments. A retry resumes only a matching incomplete record and never
starts a fresh run or reruns a durably completed hook.
`,
	"release": `HOW TO: release

Release locks compose reproducible source; they are not a release-management
platform. First use a complete clean development workspace to preview and then
write its deterministic non-base revision overlay:

  wtree release lock v1.4.0 --dry-run
  wtree release lock v1.4.0

wtree does not commit, tag, push, publish, deploy, package, sign, or claim
an atomic cross-repository snapshot. A trusted local post-release hook may
tag a child repository after lock success. Keep that hook idempotent: matching
tags may be accepted, while a tag at another commit is a collision and is
never moved. Review and commit project.wtree.lock.yml yourself. Publish child
commits and tags before the base commit/tag, then create the base tag yourself.

In CI, check out that clean base commit/tag with its tracked manifest and lock:

  wtree release materialize project.wtree.lock.yml
  wtree exec -- go test ./...

Materialization fetches only advertised branch/tag refs and creates exact
detached non-base checkouts. Authentication is Git-owned and noninteractive:
an SSH agent, askpass helper, or configured credential helper can provide it.
wtree stores no credentials and provides no credential flags. A successful
materialization is the verification boundary; there is no release verify and
no post-materialize hook. Build, test, packaging, signing, publication, and
deployment remain explicit later CI actions.

TROUBLESHOOTING
  Authentication failure: configure Git's noninteractive SSH-agent, askpass,
  or credential-helper path; never put credentials in manifests or arguments.
  Unavailable commit: publish the child commit and tag before the base tag, so
  the commit is reachable from an advertised branch or tag.
  Manifest mismatch or dirty base: use the exact clean base commit that tracks
  both project.wtree.yml and project.wtree.lock.yml.
  Occupied destination or registered project: start from a fresh CI checkout;
  materialize does not repair or convert an existing workspace.
  Tag collision: do not move it; inspect the child commit and choose a new
  release name or resolve the mismatch.
  Hook failure: the lock can already be valid; fix the external condition and
  rerun the same lock command only when the hook is idempotent.
  Partial cleanup: retain the reported recovery evidence and resolve it before
  retrying; never treat an incomplete rollback as a complete workspace.
	Detached workspace: detached child checkouts are expected release inputs;
	use explicit later CI commands rather than branch-oriented development work.
`,
	"release lock": `HOW TO: release lock

Use wtree release lock <release-name> [workspace] only from one complete,
clean local workspace. --dry-run validates and renders the deterministic
revision overlay without writing it or running a hook. A real invocation writes
the lock, then runs trusted local-v3 post-release hooks unless --no-hooks is
given. wtree never fetches, commits, tags, pushes, publishes, or deploys.

Review and commit project.wtree.lock.yml yourself. Publish reviewed child
commits and tags before the base release commit/tag. Matching idempotent child
tags may be accepted; a tag at another commit is a collision and is never
moved. A clean tracked prior lock is normally replaced for the next release;
untracked or locally modified lock bytes require explicit --force.
`,
	"release materialize": `HOW TO: release materialize

Use wtree release materialize <lock-file> in a fresh, clean CI-provided base
checkout that tracks matching project.wtree.yml and project.wtree.lock.yml.
Git-owned noninteractive SSH-agent, askpass, or configured credential-helper
authentication can obtain advertised refs; wtree stores no credentials and has
no credential flags. The command creates exact detached children and never
runs lifecycle hooks or a post-materialize hook.

Success is the exact-source verification boundary. There is no release verify
command: run later build/test/package/publish work explicitly, for example
wtree exec -- go test ./....
`,
}

func renderHowToIfRequested(writer io.Writer, arguments []string) (bool, error) {
	count := 0
	for _, argument := range arguments {
		if argument == "--how-to" {
			count++
		}
	}
	if count == 0 {
		return false, nil
	}
	if count != 1 {
		return true, invalidArgumentsError{cause: fmt.Errorf("--how-to may be specified once as a terminal guide")}
	}
	if len(arguments) == 1 && arguments[0] == "--how-to" {
		_, err := fmt.Fprint(writer, globalHowTo)
		return true, err
	}
	if len(arguments) == 2 && arguments[1] == "--how-to" {
		if guide, found := commandHowTo[arguments[0]]; found {
			_, err := fmt.Fprint(writer, guide)
			return true, err
		}
	}
	if len(arguments) == 3 && arguments[2] == "--how-to" {
		if guide, found := commandHowTo[arguments[0]+" "+arguments[1]]; found {
			_, err := fmt.Fprint(writer, guide)
			return true, err
		}
	}
	return true, invalidArgumentsError{cause: fmt.Errorf("--how-to is valid only as `wtree --how-to`, `wtree {project,init,clone,update,create,import,remove,delete,doctor,hooks,release} --how-to`, or a documented release subcommand guide")}
}
