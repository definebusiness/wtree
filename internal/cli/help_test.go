package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestRootHelpDescribesCommandsConceptsSafetyAndExamples(t *testing.T) {
	result := testutil.RunCommand(t, cli.Execute, "--help")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("root help = %#v", result)
	}
	for _, want := range []string{
		"USAGE", "GLOBAL OPTIONS", "COMMANDS", "CONCEPTS", "WORKTREE LOCATION", "EXAMPLES", "EXIT CODES",
		"init", "import", "create", "checkout", "list", "status", "path", "repo", "remove", "delete", "doctor", "config",
		"project", "workspace", "repository identity", "wtree create feature/login", "wtree <command> --help",
	} {
		if !strings.Contains(result.Stdout, want) {
			t.Errorf("root help missing %q:\n%s", want, result.Stdout)
		}
	}
}

func TestHowToCoversAllTopicsAndCommandGuides(t *testing.T) {
	guide := testutil.RunCommand(t, cli.Execute, "--how-to")
	if guide.Err != nil || guide.Stderr != "" {
		t.Fatalf("global how-to = %#v", guide)
	}
	for _, want := range []string{
		"What wtree is", "Clone a published multi-repository project", "Initialize and publish an existing project", "Keep local configuration private", "Refresh the portable manifest from local repositories", "Synchronize a clone from its published manifest", "Preview manifest changes for tools", "Configure worktree storage", "Create a workspace", "Create from HEAD", "Create from another branch/ref", "Override nested repository mounts", "Work inside a workspace", "Resolve workspace paths", "Resolve repository paths", "Inspect status", "Import an existing workspace", "Import renamed nested checkouts", "Remove a workspace", "Restore an existing branch with checkout", "Delete workspace and branches", "Diagnose inconsistencies", "Use --dry-run", "Use --json", "Use wtree from nested directories", "Use --project explicitly", "AI coding agent workflow", "Inspect registered projects", "Prune only a stale registry registration", "Intentionally unregister a project registration", "Important safety semantics",
	} {
		if !strings.Contains(guide.Stdout, want) {
			t.Errorf("global how-to missing %q", want)
		}
	}
	for _, want := range []string{`cd "$(wtree path feature/login)"`, `cd "$(wtree path default)"`} {
		if !strings.Contains(guide.Stdout, want) {
			t.Errorf("global how-to missing workspace jump %q", want)
		}
	}
	for _, command := range []string{"project", "init", "clone", "update", "sync", "create", "import", "remove", "delete", "doctor"} {
		result := testutil.RunCommand(t, cli.Execute, command, "--how-to")
		if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, "HOW TO: "+command) || !strings.Contains(result.Stdout, "EXAMPLES") {
			t.Errorf("%s how-to = %#v", command, result)
		}
	}
}

func TestHowToIsValidatedTerminalCommand(t *testing.T) {
	for _, arguments := range [][]string{
		{"status", "--how-to"},
		{"unknown", "--how-to"},
		{"create", "--how-to", "feature/login"},
		{"--how-to", "--json"},
		{"create", "--how-to", "--dry-run"},
	} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil || cli.ExitCode(result.Err) != 2 {
			t.Errorf("invalid how-to %v = %#v, want invalid arguments", arguments, result)
		}
	}
}

func TestDetailedCommandHelpAndUnsupportedOptionMatrix(t *testing.T) {
	for _, command := range []string{"init", "import", "create", "checkout", "list", "status", "path", "repo", "remove", "delete", "doctor", "config"} {
		result := testutil.RunCommand(t, cli.Execute, command, "--help")
		if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, "USAGE") || !strings.Contains(result.Stdout, "EXAMPLES") || !strings.Contains(result.Stdout, "SAFETY AND OUTPUT") || !strings.Contains(result.Stdout, "EXIT CODES") {
			t.Errorf("%s help = %#v", command, result)
		}
	}
	for _, arguments := range [][]string{{"config", "get", "--help"}, {"config", "set", "--help"}, {"config", "unset", "--help"}, {"config", "list", "--help"}, {"repo", "path", "--help"}, {"repo", "get", "--help"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, "USAGE") || !strings.Contains(result.Stdout, "EXAMPLES") || !strings.Contains(result.Stdout, "SAFETY AND OUTPUT") || !strings.Contains(result.Stdout, "EXIT CODES") {
			t.Errorf("%v help = %#v", arguments, result)
		}
	}
	for _, arguments := range [][]string{{"list", "--dry-run"}, {"path", "missing", "--json"}, {"doctor", "--verbose"}, {"doctor", "--dry-run"}, {"create", "feature", "--force"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err == nil || cli.ExitCode(result.Err) != 2 {
			t.Errorf("unsupported %v = %#v, want invalid arguments", arguments, result)
		}
	}
}

func TestDetailedNestedHelpUsesFullCommandPathAndHidesInternalFlags(t *testing.T) {
	for _, arguments := range [][]string{{"config", "get", "--help"}, {"config", "list", "--help"}, {"repo", "get", "--help"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err != nil {
			t.Fatalf("%v help = %#v", arguments, result)
		}
		wantTitle := "wtree " + strings.Join(arguments[:2], " ")
		if !strings.HasPrefix(result.Stdout, wantTitle+"\n") {
			t.Errorf("%v help title = %q, want %q", arguments, result.Stdout, wantTitle)
		}
		if strings.Contains(result.Stdout, "--project-scope") {
			t.Errorf("%v exposes hidden --project-scope flag:\n%s", arguments, result.Stdout)
		}
	}
}

func TestHelpAccuratelyDocumentsProjectSelectorAndConfigScopeMarker(t *testing.T) {
	initHelp := testutil.RunCommand(t, cli.Execute, "init", "--help")
	if initHelp.Err != nil {
		t.Fatalf("init help = %#v", initHelp)
	}
	if strings.Contains(initHelp.Stdout, "--project") {
		t.Fatalf("init help advertises rejected --project selector:\n%s", initHelp.Stdout)
	}

	configHelp := testutil.RunCommand(t, cli.Execute, "config", "get", "--help")
	if configHelp.Err != nil {
		t.Fatalf("config get help = %#v", configHelp)
	}
	for _, want := range []string{
		"wtree --project <path> config get <key>",
		"wtree config get <key> --project",
		"wtree --project <path> config get <key> --project",
	} {
		if !strings.Contains(configHelp.Stdout, want) {
			t.Errorf("config get help missing %q:\n%s", want, configHelp.Stdout)
		}
	}
	if strings.Contains(configHelp.Stdout, "explicit project directory or .wtree.yml path") {
		t.Fatalf("config child help presents root selector as a command-local option:\n%s", configHelp.Stdout)
	}
}

func TestDocumentedConfigProjectFormsExecuteWithTheirStatedSemantics(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("readme", "x\n", "initial")
	isolateCLIPathEnvironment(t)
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	projectValue := filepath.Join(t.TempDir(), "project-worktrees")
	set := testutil.RunCommand(t, cli.Execute,
		"--project", project.Path, "config", "set", "--project", "worktrees.root", projectValue)
	if set.Err != nil {
		t.Fatalf("documented project-scope set = %#v", set)
	}
	get := testutil.RunCommand(t, cli.Execute,
		"--project", project.Path, "config", "get", "worktrees.root", "--project", "--json")
	if get.Err != nil || get.Stderr != "" {
		t.Fatalf("documented combined get = %#v", get)
	}
	var value struct {
		Value  string `json:"value"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(get.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if value.Value != projectValue || value.Source != "project" {
		t.Fatalf("combined form got %#v, want project-scoped value", value)
	}
}

func TestEveryPrintedWTREEExampleParsesAsAnExecutableCommand(t *testing.T) {
	helpCommands := [][]string{
		nil,
		{"project"}, {"project", "list"}, {"project", "prune"}, {"project", "unregister"},
		{"init"}, {"import"}, {"create"}, {"checkout"}, {"list"}, {"status"}, {"path"},
		{"repo"}, {"repo", "path"}, {"repo", "get"}, {"remove"}, {"delete"}, {"doctor"},
		{"config"}, {"config", "get"}, {"config", "set"}, {"config", "unset"}, {"config", "list"},
	}
	for _, command := range helpCommands {
		result := testutil.RunCommand(t, cli.Execute, append(append([]string(nil), command...), "--help")...)
		if result.Err != nil {
			t.Fatalf("%v help = %#v", command, result)
		}
		for _, line := range strings.Split(result.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "wtree ") || strings.Contains(line, "<command>") {
				continue
			}
			arguments := strings.Fields(strings.TrimPrefix(line, "wtree "))
			if len(arguments) == 1 && arguments[0] == "--how-to" {
				parsed := testutil.RunCommand(t, cli.Execute, arguments...)
				if parsed.Err != nil {
					t.Errorf("example %q = %#v", line, parsed)
				}
				continue
			}
			parsed := testutil.RunCommand(t, cli.Execute, append(arguments, "--help")...)
			if parsed.Err != nil || parsed.Stderr != "" {
				t.Errorf("example %q does not parse as executable command: %#v", line, parsed)
			}
		}
	}
}

func TestVerboseProgressNeverLeaksEnvironmentValues(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	secret := "wtree-test-secret-not-for-output"
	t.Setenv("WTREE_TEST_SECRET", secret)
	result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", "feature/redacted", "--data-dir", data, "--path", target, "--verbose")
	if result.Err != nil || result.Stderr == "" || strings.Contains(result.Stdout, secret) || strings.Contains(result.Stderr, secret) {
		t.Fatalf("verbose output = %#v", result)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}
