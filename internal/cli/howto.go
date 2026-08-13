package cli

import (
	"fmt"
	"io"
)

const globalHowTo = `WTREE HOW-TO

1. What wtree is
   wtree manages one logical workspace across independent, nested Git repositories.
2. Initialize a project
   Run: wtree init
3. What repository discovery does
   init finds independent nested Git repositories and records their Git identities.
4. Configure worktree storage
   Use config set worktrees.root <path>, or pass --worktree-root when creating.
5. Create a workspace
   Run: wtree create feature/login
6. Create from HEAD
   The default --from HEAD creates branches from each repository's current HEAD.
7. Create from another branch/ref
   Run: wtree create feature/login --from main
8. Override nested repository mounts
   Run: wtree create feature/login --mount backend=api
9. Work inside a workspace
   Run: cd "$(wtree path feature/login)"
10. Resolve workspace paths
    Run: wtree path feature/login
11. Resolve repository paths
    Run: wtree repo path backend
12. Inspect status
    Run: wtree status feature/login --json
13. Import an existing workspace
    Run: wtree import /path/to/workspace --name feature/login
14. Import renamed nested checkouts
    Import maps checkouts by Git identity, so a configured backend may be mounted as api.
15. Remove a workspace
    Run: wtree remove feature/login. Branches and retained state remain for checkout.
16. Restore an existing branch with checkout
    Run: wtree checkout feature/login
17. Delete workspace and branches
    Run: wtree delete feature/login. Use --force only for the named safety overrides.
18. Diagnose inconsistencies
    Run: wtree doctor feature/login. Use --fix only for listed safe repairs.
19. Use --dry-run
    Add --dry-run to render and validate an operation without mutation.
20. Use --json
    Add --json where supported for machine-readable output; errors use a stable envelope.
21. Use wtree from nested directories
    wtree resolves the project from the current repository identity and workspace state.
22. Use --project explicitly
    Run: wtree --project /path/to/project <command> when context is ambiguous.
23. AI coding agent workflow
    Create, resolve with path, work in the checkout, inspect status, then remove or delete.
24. Important safety semantics
    Create preflights first; remove retains state; delete and force operations require explicit intent.
`

var commandHowTo = map[string]string{
	"create": `HOW TO: create

Create a synchronized branch and parent-first worktrees after preflight succeeds.

EXAMPLES
  wtree create feature/login
  wtree create feature/login --from main
  wtree create feature/login --mount backend=api --dry-run
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
	return true, invalidArgumentsError{cause: fmt.Errorf("--how-to is valid only as `wtree --how-to` or `wtree {create,import,remove,delete,doctor} --how-to`")}
}
