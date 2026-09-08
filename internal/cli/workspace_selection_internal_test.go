package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestWorkspaceSelectionExactFlagAllowlist enumerates every command leaf that
// does not intentionally own the path/status selector. The arguments satisfy
// each command's shape so an unknown --exact flag, rather than unrelated
// argument or project resolution validation, is the asserted boundary.
func TestWorkspaceSelectionExactFlagAllowlist(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"root", nil},
		{"project list", []string{"project", "list"}},
		{"project prune", []string{"project", "prune", "project-id"}},
		{"project unregister", []string{"project", "unregister", "project-id"}},
		{"init", []string{"init", "."}},
		{"clone", []string{"clone", "manifest", "destination"}},
		{"update", []string{"update"}},
		{"config get", []string{"config", "get", "worktrees.root"}},
		{"config set", []string{"config", "set", "worktrees.root", "/worktrees"}},
		{"config unset", []string{"config", "unset", "worktrees.root"}},
		{"config list", []string{"config", "list"}},
		{"hooks list", []string{"hooks", "list"}},
		{"hooks share", []string{"hooks", "share", "post-create"}},
		{"hooks install", []string{"hooks", "install"}},
		{"hooks retry", []string{"hooks", "retry", "workspace"}},
		{"release lock", []string{"release", "lock", "v1"}},
		{"release materialize", []string{"release", "materialize", "lock.yml", "destination"}},
		{"create", []string{"create", "workspace"}},
		{"remove", []string{"remove", "workspace"}},
		{"delete", []string{"delete", "workspace"}},
		{"import", []string{"import", "."}},
		{"doctor", []string{"doctor", "workspace"}},
		{"list", []string{"list"}},
		{"exec", []string{"exec", "--", "echo"}},
		{"fetch", []string{"fetch"}},
		{"push", []string{"push"}},
		{"repo path", []string{"repo", "path", "repository"}},
		{"repo get", []string{"repo", "get", "repository"}},
		{"repo branch", []string{"repo", "branch", "repository", "branch"}},
		{"companion update", []string{"companion", "update", "repository"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			arguments := append(append([]string{}, test.args...), "--exact")
			if test.name == "exec" {
				arguments = []string{"exec", "--exact", "--", "echo"}
			}
			var stdout, stderr bytes.Buffer
			err := Execute(arguments, &stdout, &stderr)
			if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "unknown flag: --exact") {
				t.Fatalf("%v = %v (exit %d), want unknown --exact invalid arguments", arguments, err, ExitCode(err))
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("%v wrote before unknown flag failure: stdout=%q stderr=%q", arguments, stdout.String(), stderr.String())
			}
		})
	}
}
