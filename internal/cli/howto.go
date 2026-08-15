package cli

import (
	"fmt"
	"io"
)

const globalHowTo = `WTREE HOW-TO

1. What wtree is
   wtree manages one logical workspace across independent, nested Git repositories.
2. Clone a published multi-repository project
   Run: wtree clone ./project.wtree.yml, or pass an HTTP(S) manifest URL.
3. Initialize and publish an existing project
   Configure and push every upstream, then run wtree init and commit project.wtree.yml.
4. Keep local configuration private
   init creates or updates .gitignore for /.wtree.yml; project.wtree.yml is portable and tracked.
5. Refresh the portable manifest from local repositories
   Run: wtree update. Review the old/new table before confirming the update.
6. Synchronize a clone from its published manifest
   Run: wtree sync. Use --from <path-or-URL> to replace the stored manifest source.
7. Preview manifest changes for tools
   Run: wtree update --json --dry-run or wtree sync --json --dry-run.
8. Configure worktree storage
   Use config set worktrees.root <path>, or pass --worktree-root when creating.
9. Create a workspace
   Run: wtree create feature/login
10. Create from HEAD
   The default --from HEAD creates branches from each repository's current HEAD.
11. Create from another branch/ref
   Run: wtree create feature/login --from main
12. Override nested repository mounts
   Run: wtree create feature/login --mount backend=api
   Commit an effective /api/ rule to the parent repository's .gitignore first.
13. Work inside a workspace
   Jump to a branch workspace: cd "$(wtree path feature/login)"
   Jump back to the original clone (the default workspace): cd "$(wtree path default)"
14. Resolve workspace paths
    Run: wtree path feature/login
15. Resolve repository paths
    Run: wtree repo path backend
16. Inspect status
    Run: wtree status feature/login --json
17. Import an existing workspace
    Run: wtree import /path/to/workspace --name feature/login
18. Import renamed nested checkouts
    Import maps checkouts by Git identity, so a configured backend may be mounted as api.
19. Remove a workspace
    Run: wtree remove feature/login. Branches and retained state remain for checkout.
20. Restore an existing branch with checkout
    Run: wtree checkout feature/login
21. Delete workspace and branches
    Run: wtree delete feature/login. Use --force only for the named safety overrides.
22. Diagnose inconsistencies
    Run: wtree doctor feature/login. Use --fix only for listed safe repairs.
23. Use --dry-run
    Add --dry-run to render and validate an operation without mutation.
24. Use --json
    Add --json where supported for machine-readable output; errors use a stable envelope.
25. Use wtree from nested directories
    wtree resolves the project from the current repository identity and workspace state.
26. Use --project explicitly
    Run: wtree <command> --project /path/to/project when context is ambiguous.
27. AI coding agent workflow
    Create, resolve with path, work in the checkout, inspect status, then remove or delete.
28. Inspect registered projects
    Run: wtree project list. It reports registry inconsistencies without changing projects or Git data.
29. Prune only a stale registry registration
    Run: wtree project prune <project-id> --dry-run. It removes no Git worktree,
    repository, project config, workspace state, recovery data, or lock file.
30. Intentionally unregister a project registration
    Run: wtree project unregister <project-id> --dry-run. It retains every project
    artifact; the retained local config can register the project again after a later
    mutating command is run from that project.
31. Important safety semantics
    Clone, update, and sync preflight first; destructive reconciliation requires explicit intent.
`

var commandHowTo = map[string]string{
	"init": `HOW TO: init

Discover the complete local repository tree after every repository has been pushed and connected to its intended upstream. Write ignored .wtree.yml and portable project.wtree.yml, and ensure /.wtree.yml is in the root .gitignore.

EXAMPLES
  wtree init
  wtree init --worktree-root /worktrees
  wtree init --dry-run --json

If .wtree.yml was removed but this checkout remains registered, init refuses to
publish another project ID. Inspect the registration with wtree project list.
Use wtree project prune <id> only for an objectively stale registration, or
wtree project unregister <id> for intentional registry-only removal; neither
operation deletes Git repositories, worktrees, configuration, or state. Once
the intended registration is explicitly removed, retry wtree init.
`,
	"clone": `HOW TO: clone

Create and register a complete multi-repository checkout from a local portable manifest or an HTTP(S) URL. The exact manifest source is saved for later sync operations.

EXAMPLES
  wtree clone ./project.wtree.yml
  wtree clone https://git.example.com/acme/project.wtree.yml ./acme-shop
  wtree clone ./project.wtree.yml ./acme-shop --dry-run
`,
	"update": `HOW TO: update

Reinspect local repositories and compare the result with .wtree.yml and project.wtree.yml. Interactive output asks before writing; JSON without --dry-run applies non-interactively.

EXAMPLES
  wtree update
  wtree update --dry-run
  wtree update --json --dry-run
  wtree update --json
`,
	"sync": `HOW TO: sync

Read the stored portable manifest source and reconcile this clone's repositories and local configuration to it. --from replaces the stored source after a successful sync.

EXAMPLES
  wtree sync
  wtree sync --dry-run
  wtree sync --from ./replacement.wtree.yml
  wtree sync --json --dry-run
`,
	"create": `HOW TO: create

Create a synchronized branch and parent-first worktrees after preflight succeeds.

EXAMPLES
  wtree create feature/login
  wtree create feature/login --from main
  wtree create feature/login --mount backend=api --dry-run

Custom mounts must be ignored by a committed parent .gitignore at the selected base.
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
	return true, invalidArgumentsError{cause: fmt.Errorf("--how-to is valid only as `wtree --how-to` or `wtree {project,init,clone,update,sync,create,import,remove,delete,doctor} --how-to`")}
}
