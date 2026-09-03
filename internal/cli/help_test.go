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
	if strings.Contains(result.Stdout, "\t") {
		t.Fatalf("root help contains tab-indented entries:\n%s", result.Stdout)
	}
	for _, want := range []string{
		"USAGE", "GLOBAL OPTIONS", "COMMANDS", "CONCEPTS", "WORKTREE LOCATION", "EXAMPLES", "EXIT CODES",
		"10 lifecycle-hook setup incomplete",
		"init", "clone", "import", "create", "checkout", "list", "status", "path", "repo", "remove", "delete", "doctor", "config", "hooks",
		"project", "repository forest", "base repository", "top-level mounts are logical-root-relative", "metadata and ignores", "inspection and recovery", "repository identity", "wtree clone ./project.wtree.yml ./product --dry-run", "wtree create feature/login", "wtree <command> --help",
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
		"What wtree is", "Initialize and publish an existing project", "Keep local configuration private", "Configure worktree storage", "Clone a published project", "Preview a clone without changing anything", "Create a workspace", "Create from HEAD", "Create from another branch/ref", "Override nested repository mounts", "Work inside a workspace", "Resolve workspace paths", "Resolve repository paths", "Inspect status", "Import an existing workspace", "Import renamed nested checkouts", "Remove a workspace", "Restore an existing branch with checkout", "Delete workspace and branches", "Diagnose inconsistencies", "Use --dry-run", "Use --json", "Use wtree from nested directories", "Use --project explicitly", "AI coding agent workflow", "Inspect registered projects", "Prune only a stale registry registration", "Intentionally unregister a project registration", "Manage lifecycle hooks safely", "Important safety semantics",
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
	for _, command := range []string{"project", "init", "clone", "create", "import", "remove", "delete", "doctor", "hooks"} {
		result := testutil.RunCommand(t, cli.Execute, command, "--how-to")
		if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, "HOW TO: "+command) || !strings.Contains(result.Stdout, "EXAMPLES") {
			t.Errorf("%s how-to = %#v", command, result)
		}
	}
}

func TestHookHelpAndHowToDescribeConsentRetryAndSecretBoundaries(t *testing.T) {
	for _, arguments := range [][]string{{"hooks", "--help"}, {"hooks", "retry", "--help"}, {"clone", "--help"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err != nil || result.Stderr != "" {
			t.Fatalf("%v help = %#v", arguments, result)
		}
	}
	guide := testutil.RunCommand(t, cli.Execute, "hooks", "--how-to")
	if guide.Err != nil || guide.Stderr != "" {
		t.Fatalf("hooks how-to = %#v", guide)
	}
	for _, want := range []string{
		"post-create", "post-clone", "--run-hooks", "shared_hooks", "wtree hooks retry <workspace>",
		"idempotent", "environment", "literal command arguments", "command output",
	} {
		if !strings.Contains(guide.Stdout, want) {
			t.Errorf("hooks how-to missing %q", want)
		}
	}
	global := testutil.RunCommand(t, cli.Execute, "--how-to")
	for _, want := range []string{"execution-result/error JSON", "hooks list and\n    create/clone plan or dry-run inspection intentionally show"} {
		if global.Err != nil || !strings.Contains(global.Stdout, want) {
			t.Errorf("global how-to missing qualified hook privacy contract %q", want)
		}
	}
	if strings.Contains(global.Stdout, "in durable hook-run records or JSON results") || strings.Contains(guide.Stdout, "in durable records or\nJSON results") {
		t.Error("installed how-to retains unqualified JSON-results privacy claim")
	}
}

func TestLifecycleHookPublicDocumentationKeepsInstalledContractAligned(t *testing.T) {
	root := testRepositoryRoot(t)
	for path, required := range map[string][]string{
		"README.md": {
			"Lifecycle hooks: explicit local consent", "version 3", "--run-hooks", "shared_hooks", "--no-hooks", "wtree hooks retry", "execution-result/error JSON", "sanitized effective `PATH`", "hooks list",
		},
		"docs/INSTALL.md": {
			"Lifecycle-hook commands in an installed binary", "wtree hooks --how-to", "--run-hooks", "execution-result/error JSON", "PATHEXT", "../tutorial/LIFECYCLE-HOOKS.md",
		},
		"docs/TROUBLESHOOTING.md": {
			"A lifecycle hook left setup incomplete", "wtree hooks retry", "starts a fresh run", "shared_hooks", "hook-free", "explicitly version 3",
		},
		"tutorial/LIFECYCLE-HOOKS.md": {
			"make tutorial-test", "TestLifecycleHookTutorialAcceptance", "tracked `sh` fixture on Unix", "native `.exe` test-binary helper on Windows", "`.cmd` fixtures remain availability/PATHEXT-only", "generated Go helpers", "--run-hooks", "--no-hooks", "PATHEXT",
		},
		"Makefile": {
			"lifecycle-hook-tutorial-test:", "HookRunnerSerializesConcurrentSameEvent", "HookRunRecordRoundTripAndPrivacy",
		},
	} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, want := range required {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
		if path == "tutorial/LIFECYCLE-HOOKS.md" && strings.Contains(string(data), "Go helper processes only") {
			t.Errorf("%s retains the contradictory Go-helper-only fixture claim", path)
		}
	}
}

func TestPublicDocumentationTracksCurrentCLIExtensions(t *testing.T) {
	root := testRepositoryRoot(t)
	checks := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path:     "README.md",
			required: []string{"tutorial/LIFECYCLE-HOOKS.md", "mutating commands that support it", "download-backend-modules"},
			forbidden: []string{
				"Use `--dry-run` on mutating commands to validate",
			},
		},
		{
			path:     "tutorial/README.md",
			required: []string{"lifecycle-hook tutorial", "LIFECYCLE-HOOKS.md"},
		},
		{
			path:     "tutorial/ALL-COMMANDS.md",
			required: []string{"every hook-free `wtree` command", "wtree hooks list/share/install/retry", "make tutorial-test"},
			forbidden: []string{
				"This tutorial exercises every `wtree` command",
				"This tutorial exercises every current command",
			},
		},
		{
			path:     "tutorial/LIFECYCLE-HOOKS.md",
			required: []string{"Hands-on configuration", "download-backend-modules", "wtree hooks install --missing", "wtree clone ./project.wtree.yml ./product --run-hooks"},
		},
		{
			path:     "docs/spec/wtree.spec.md",
			required: []string{"version: 2", "one or more independent top-level Git repositories", "adds the implemented `update`, `exec`, `fetch`, and non-publishing `push`"},
			forbidden: []string{
				"`update`, `sync`, and release locking remain future work.",
				"version: 1\n\nproject:",
			},
		},
		{
			path:     "docs/spec/wtree.traceability.md",
			required: []string{"Aggregate command extension", "later aggregate extensions implement `update`, `exec`, `fetch`, and non-publishing `push`"},
			forbidden: []string{
				"update/sync/release locking remain future work",
			},
		},
	}
	for _, check := range checks {
		data, err := os.ReadFile(filepath.Join(root, check.path))
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		for _, required := range check.required {
			if !strings.Contains(body, required) {
				t.Errorf("%s missing current documentation contract %q", check.path, required)
			}
		}
		for _, forbidden := range check.forbidden {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s retains stale documentation contract %q", check.path, forbidden)
			}
		}
	}
}

func testRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate repository go.mod")
		}
		directory = parent
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
	for _, command := range []string{"init", "clone", "import", "create", "checkout", "list", "status", "path", "repo", "remove", "delete", "doctor", "config", "hooks"} {
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

func TestStatusHelpExplainsLastFetchedUpstreamFacts(t *testing.T) {
	result := testutil.RunCommand(t, cli.Execute, "status", "--help")
	if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, "last-fetched local upstream facts") || !strings.Contains(result.Stdout, "does not fetch or contact remotes") {
		t.Fatalf("status help = %#v", result)
	}
}

func TestCommandHelpExplainsImplementedForestSemantics(t *testing.T) {
	for _, value := range []struct {
		arguments []string
		want      string
	}{
		{[]string{"clone", "--help"}, "complete repository forest"},
		{[]string{"import", "--help"}, "base, sibling, or nested checkout"},
		{[]string{"checkout", "--help"}, "Top-level mounts are logical-root-relative"},
		{[]string{"status", "--help"}, "deterministic parent-first order"},
		{[]string{"doctor", "--help"}, "unresolved rollback visibility"},
		{[]string{"remove", "--help"}, "child-first order"},
		{[]string{"delete", "--help"}, "grouping-directory contents are preserved"},
	} {
		result := testutil.RunCommand(t, cli.Execute, value.arguments...)
		if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, value.want) {
			t.Errorf("%v help = %#v, want %q", value.arguments, result, value.want)
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
	cloneHelp := testutil.RunCommand(t, cli.Execute, "clone", "--help")
	if cloneHelp.Err != nil || strings.Contains(cloneHelp.Stdout, "--project") {
		t.Fatalf("clone help advertises rejected --project selector: %#v", cloneHelp)
	}

	configHelp := testutil.RunCommand(t, cli.Execute, "config", "get", "--help")
	if configHelp.Err != nil {
		t.Fatalf("config get help = %#v", configHelp)
	}
	for _, want := range []string{
		"wtree config -p <path> get <key>",
		"wtree config get <key> --project",
		"wtree config -p <path> get <key> --project",
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
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("readme", "x\n", "initial")
	isolateCLIPathEnvironment(t)
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	projectValue := filepath.Join(t.TempDir(), "project-worktrees")
	set := testutil.RunCommand(t, cli.Execute,
		"config", "-p", project.Path, "set", "worktrees.root", projectValue, "--project")
	if set.Err != nil {
		t.Fatalf("documented project-scope set = %#v", set)
	}
	get := testutil.RunCommand(t, cli.Execute,
		"config", "-p", project.Path, "get", "worktrees.root", "--project", "--json")
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
		{"init"}, {"clone"}, {"import"}, {"create"}, {"checkout"}, {"list"}, {"status"}, {"path"},
		{"repo"}, {"repo", "path"}, {"repo", "get"}, {"remove"}, {"delete"}, {"doctor"},
		{"config"}, {"config", "get"}, {"config", "set"}, {"config", "unset"}, {"config", "list"},
		{"hooks"}, {"hooks", "list"}, {"hooks", "share"}, {"hooks", "install"},
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
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	secret := "wtree-test-secret-not-for-output"
	t.Setenv("WTREE_TEST_SECRET", secret)
	result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/redacted", "--data-dir", data, "--path", target, "--verbose")
	if result.Err != nil || result.Stderr == "" || strings.Contains(result.Stdout, secret) || strings.Contains(result.Stderr, secret) {
		t.Fatalf("verbose output = %#v", result)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}
