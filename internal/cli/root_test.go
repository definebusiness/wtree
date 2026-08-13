package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcel/wtree/internal/cli"
	gitadapter "github.com/marcel/wtree/internal/git"
	"github.com/marcel/wtree/internal/store"
	"github.com/marcel/wtree/internal/testutil"
)

func TestExecuteVersion(t *testing.T) {
	for _, argument := range []string{"--version", "-v"} {
		t.Run(argument, func(t *testing.T) {
			result := testutil.RunCommand(t, cli.Execute, argument)

			if result.Err != nil {
				t.Fatalf("Execute() error = %v", result.Err)
			}
			if got, want := result.Stdout, "wtree 0.1.0\n"; got != want {
				t.Errorf("stdout = %q, want %q", got, want)
			}
			if result.Stderr != "" {
				t.Errorf("stderr = %q, want empty", result.Stderr)
			}
		})
	}
}

func TestExecuteCreateDryRunRendersDeterministicJSONPlanWithoutMutation(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(project.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	result := testutil.RunCommand(t, cli.Execute,
		"--project", project.Path, "create", "feature/login", "--dry-run", "--data-dir", data, "--path", target, "--mount", "backend=api", "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("create dry-run = %#v", result)
	}
	var value struct {
		Operation    string `json:"operation"`
		RootPath     string `json:"rootPath"`
		Repositories []struct {
			ID    string `json:"id"`
			Mount string `json:"mount"`
		} `json:"repositories"`
		Steps []struct {
			Action       string `json:"action"`
			RepositoryID string `json:"repositoryId"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if value.Operation != "create" || value.RootPath != target || len(value.Repositories) != 2 || value.Repositories[1].ID != "backend" || value.Repositories[1].Mount != "api" || len(value.Steps) != 4 {
		t.Fatalf("create plan = %#v", value)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run created target: %v", err)
	}
}

func TestExecuteCreateCreatesBranchWorktreeAndState(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}

	result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", "feature/login", "--data-dir", data, "--path", target)
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("create error = %v, result = %#v", result.Err, result)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatalf("created worktree .git: %v", err)
	}
	project.Run(t, "show-ref", "--verify", "--quiet", "refs/heads/feature/login")
}

func TestExecuteCreateFromJSONVerboseAndForceContracts(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("first.txt", "first\n", "first")
	project.CommitFile("second.txt", "second\n", "second")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	target := filepath.Join(t.TempDir(), "workspace")
	jsonResult := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", "feature/base", "--from", "HEAD~1", "--data-dir", data, "--path", target, "--json", "--verbose")
	if jsonResult.Err != nil || jsonResult.Stderr != "" {
		t.Fatalf("JSON create = %#v", jsonResult)
	}
	var value struct {
		Operation string `json:"operation"`
		Steps     []any  `json:"steps"`
	}
	if err := json.Unmarshal([]byte(jsonResult.Stdout), &value); err != nil {
		t.Fatalf("decode create JSON: %v", err)
	}
	if value.Operation != "create" || len(value.Steps) != 2 || strings.Contains(jsonResult.Stdout, "execute_started") {
		t.Fatalf("JSON create output = %q", jsonResult.Stdout)
	}
	if _, err := os.Stat(filepath.Join(target, "second.txt")); !os.IsNotExist(err) {
		t.Fatalf("--from HEAD~1 checkout contains second commit file: %v", err)
	}

	forceTarget := filepath.Join(t.TempDir(), "force")
	force := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", "feature/force", "--data-dir", data, "--path", forceTarget, "--force")
	if force.Err == nil || cli.ExitCode(force.Err) != 2 {
		t.Fatalf("create --force = %#v, want invalid arguments", force)
	}
	if _, err := os.Stat(forceTarget); !os.IsNotExist(err) {
		t.Fatalf("--force created target: %v", err)
	}
}

func TestExecuteCreateVerboseHumanOutput(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", "feature/verbose", "--data-dir", data, "--path", target, "--verbose")
	if result.Err != nil || strings.Contains(result.Stdout, "execute_started") || !strings.Contains(result.Stdout, "Created workspace: feature/verbose\n") || !strings.Contains(result.Stderr, "execute_started create_branch:root\n") {
		t.Fatalf("verbose create = %#v", result)
	}
}

func TestExecuteContextCancellationStopsCreateBeforeMutation(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := testutil.RunCommand(t, func(args []string, stdout, stderr io.Writer) error {
		return cli.ExecuteContext(ctx, args, stdout, stderr)
	}, "--project", project.Path, "create", "feature/canceled", "--data-dir", data, "--path", target)
	if result.Err == nil {
		t.Fatal("canceled create succeeded")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("canceled create made target: %v", err)
	}
	if exists, err := gitadapter.NewAdapter("git").BranchExists(context.Background(), project.Path, "feature/canceled"); err != nil || exists {
		t.Fatalf("canceled branch exists=%t error=%v", exists, err)
	}
}

func TestExecuteCreateDryRunRejectsGitInvalidBranchNames(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	for _, branch := range []string{".feature", "feature/", "foo/.bar", "foo..bar", "foo.lock", "foo@{bar", "feature//child"} {
		t.Run(branch, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "workspace")
			result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", branch, "--dry-run", "--data-dir", data, "--path", target, "--json")
			if result.Err == nil || cli.ExitCode(result.Err) != 5 {
				t.Fatalf("create %q = %#v, want validation error", branch, result)
			}
			if !strings.Contains(result.Stdout, "\"code\":\"validation\"") || strings.Contains(result.Stdout, "\"operation\":\"create\"") {
				t.Fatalf("create %q stdout = %q, want validation envelope only", branch, result.Stdout)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("create %q made target: %v", branch, err)
			}
		})
	}
}

func TestExecuteCheckoutDryRunAndRejectsUnsupportedFrom(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	project.Run(t, "branch", "feature")
	result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "checkout", "feature", "--dry-run", "--data-dir", data, "--path", target)
	if result.Err != nil || !strings.Contains(result.Stdout, "Operation: checkout\n") || !strings.Contains(result.Stdout, "No changes made.\n") || result.Stderr != "" {
		t.Fatalf("checkout dry-run = %#v", result)
	}
	unsupported := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "checkout", "feature", "--from", "main", "--dry-run", "--data-dir", data, "--path", target)
	if unsupported.Err == nil || cli.ExitCode(unsupported.Err) != 2 {
		t.Fatalf("checkout --from = %#v, want invalid arguments", unsupported)
	}
}

func TestExecuteCreateDryRunAppliesRepeatedMountsToNestedRepositories(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	shared := testutil.NewGitRepository(t)
	shared.CommitFile("shared.txt", "shared\n", "shared")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(shared.Path, filepath.Join(backendPath, "shared")); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	result := testutil.RunCommand(t, cli.Execute,
		"--project", root.Path, "create", "feature", "--dry-run", "--data-dir", data, "--path", target,
		"--mount", "backend=api", "--mount", "shared=common", "--json")
	if result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	var value struct {
		Repositories []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if got, want := value.Repositories[2].Path, filepath.Join(target, "api", "common"); got != want {
		t.Fatalf("shared path = %q, want %q", got, want)
	}
}

func TestExecuteInitDryRunJSON(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	result := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", t.TempDir(), "--dry-run", "--json")
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !strings.Contains(result.Stdout, "\"dryRun\":true") || result.Stderr != "" {
		t.Fatalf("output=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestExecuteInitRejectsRootProjectSelector(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	for _, arguments := range [][]string{
		{"--project", repository.Path, "init", repository.Path},
		{"init", repository.Path, "--project", repository.Path},
	} {
		t.Run(strings.Join(arguments[:1], " "), func(t *testing.T) {
			data := t.TempDir()
			result := testutil.RunCommand(t, cli.Execute, append(arguments, "--data-dir", data)...)
			if result.Err == nil || cli.ExitCode(result.Err) != 2 {
				t.Fatalf("init --project = %#v, want invalid arguments", result)
			}
			if _, err := os.Stat(filepath.Join(repository.Path, ".wtree.yml")); !os.IsNotExist(err) {
				t.Fatalf("init --project wrote project config: %v", err)
			}
		})
	}
}

func TestInspectionAndDryRunPlansDoNotReconcileStaleRegistry(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", "feature/retained", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	project.Run(t, "branch", "feature/checkout")
	registryPath := filepath.Join(data, "registry.json")
	registry, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	for id, entry := range registry.Projects {
		entry.ConfigPath = filepath.Join(t.TempDir(), "moved", ".wtree.yml")
		registry.Projects[id] = entry
	}
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"--project", project.Path}
	tests := []struct {
		name string
		args []string
	}{
		{name: "list", args: []string{"list", "--data-dir", data}},
		{name: "status", args: []string{"status", "feature/retained", "--data-dir", data}},
		{name: "path", args: []string{"path", "feature/retained", "--data-dir", data}},
		{name: "repo path", args: []string{"repo", "path", "root", "--data-dir", data}},
		{name: "repo get", args: []string{"repo", "get", "root", "--data-dir", data, "--json"}},
		{name: "create dry run", args: []string{"create", "feature/new", "--data-dir", data, "--path", filepath.Join(t.TempDir(), "new"), "--dry-run"}},
		{name: "checkout dry run", args: []string{"checkout", "feature/checkout", "--data-dir", data, "--path", filepath.Join(t.TempDir(), "checkout"), "--dry-run"}},
		{name: "remove dry run", args: []string{"remove", "feature/retained", "--data-dir", data, "--dry-run"}},
		{name: "delete dry run", args: []string{"delete", "feature/retained", "--data-dir", data, "--dry-run"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := testutil.RunCommand(t, cli.Execute, append(append([]string(nil), base...), test.args...)...)
			if result.Err != nil {
				t.Fatalf("%s = %#v", test.name, result)
			}
			after, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("%s rewrote registry:\n before=%q\n after=%q", test.name, before, after)
			}
		})
	}
}

func TestCreateReconcilesOnlyAfterSuccessfulPreflight(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	registryPath := filepath.Join(data, "registry.json")
	registry, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	for id, entry := range registry.Projects {
		entry.ConfigPath = filepath.Join(t.TempDir(), "moved", ".wtree.yml")
		registry.Projects[id] = entry
	}
	if err := store.WriteRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", ".invalid", "--data-dir", data, "--path", filepath.Join(t.TempDir(), "invalid"))
	if invalid.Err == nil || cli.ExitCode(invalid.Err) != 5 {
		t.Fatalf("invalid create = %#v", invalid)
	}
	afterInvalid, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterInvalid) != string(before) {
		t.Fatalf("failed preflight rewrote registry: before=%q after=%q", before, afterInvalid)
	}
	valid := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", "feature/valid", "--data-dir", data, "--path", filepath.Join(t.TempDir(), "valid"))
	if valid.Err != nil {
		t.Fatalf("valid create = %#v", valid)
	}
	reconciled, err := store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	configPath, err := filepath.EvalSymlinks(filepath.Join(project.Path, ".wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range reconciled.Projects {
		if entry.ConfigPath != configPath {
			t.Fatalf("registry configPath = %q, want project config", entry.ConfigPath)
		}
	}
}

func TestExecuteInitHelpDocumentsDiscoveryAndDefaultWorkspace(t *testing.T) {
	result := testutil.RunCommand(t, cli.Execute, "init", "--help")
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	for _, want := range []string{"independent nested Git repositories", "default workspace", "--worktree-root", "--dry-run", "--json"} {
		if !strings.Contains(result.Stdout, want) {
			t.Fatalf("init help missing %q:\n%s", want, result.Stdout)
		}
	}
	if result.Stderr != "" {
		t.Fatalf("stderr = %q, want empty", result.Stderr)
	}
}

func TestExecuteJSONErrorUsesEnvelope(t *testing.T) {
	for _, jsonFlag := range []string{"--json", "--json=true"} {
		t.Run(jsonFlag, func(t *testing.T) {
			result := testutil.RunCommand(t, cli.Execute, jsonFlag, "missing-command")
			if result.Err == nil || !strings.Contains(result.Stdout, "\"success\":false") || !strings.Contains(result.Stdout, "\"error\"") {
				t.Fatalf("result=%#v", result)
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != "invalid_arguments" {
				t.Fatalf("JSON error code = %q, want invalid_arguments", envelope.Error.Code)
			}
		})
	}
}

func TestExecuteInitPreflightErrorUsesOnlyTheJSONEnvelope(t *testing.T) {
	result := testutil.RunCommand(t, cli.Execute, "init", t.TempDir(), "--data-dir", t.TempDir(), "--json")
	if result.Err == nil || result.Stderr != "" {
		t.Fatalf("result=%#v", result)
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &envelope); err != nil || envelope.Success || envelope.Error.Code == "" || envelope.Error.Message == "" {
		t.Fatalf("stdout=%q envelope=%#v error=%v", result.Stdout, envelope, err)
	}
	if got := cli.ExitCode(result.Err); got != 1 {
		t.Fatalf("ExitCode() = %d, want operational failure 1", got)
	}
}

func TestExecuteInitUsesEnvironmentDataHome(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WTREE_DATA_HOME", data)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	result := testutil.RunCommand(t, cli.Execute, "init", repository.Path)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if _, err := os.Stat(filepath.Join(data, "registry.json")); err != nil {
		t.Fatalf("registry was not written to WTREE_DATA_HOME: %v", err)
	}
}

func TestExecuteHelp(t *testing.T) {
	for _, argument := range []string{"-h", "--help"} {
		t.Run(argument, func(t *testing.T) {
			result := testutil.RunCommand(t, cli.Execute, argument)

			if result.Err != nil {
				t.Fatalf("Execute() error = %v", result.Err)
			}
			if !strings.Contains(result.Stdout, "manage synchronized Git workspaces") {
				t.Errorf("help did not describe wtree:\n%s", result.Stdout)
			}
			if !strings.Contains(result.Stdout, "--project") || !strings.Contains(result.Stdout, "-p") {
				t.Errorf("help did not document explicit project selection:\n%s", result.Stdout)
			}
			if result.Stderr != "" {
				t.Errorf("stderr = %q, want empty", result.Stderr)
			}
		})
	}
}

func TestExecuteInvalidInvocationReturnsInvalidArguments(t *testing.T) {
	result := testutil.RunCommand(t, cli.Execute, "unknown-command")

	if result.Err == nil {
		t.Fatal("Execute() error = nil, want invalid arguments error")
	}
	if got := cli.ExitCode(result.Err); got != 2 {
		t.Errorf("ExitCode() = %d, want 2", got)
	}
	if !strings.Contains(result.Err.Error(), "unknown command") {
		t.Errorf("error = %q, want unknown command context", result.Err)
	}
	if result.Stdout != "" {
		t.Errorf("stdout = %q, want empty", result.Stdout)
	}
}

func TestExecuteVersionAndHelpRejectTrailingArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--version", "x"},
		{"-v", "x"},
		{"--help", "x"},
		{"-h", "x"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			result := testutil.RunCommand(t, cli.Execute, args...)

			if result.Err == nil {
				t.Fatal("Execute() error = nil, want invalid arguments error")
			}
			if got := cli.ExitCode(result.Err); got != 2 {
				t.Errorf("ExitCode() = %d, want 2", got)
			}
			if !strings.Contains(result.Err.Error(), "does not accept arguments") {
				t.Errorf("error = %q, want invalid-argument context", result.Err)
			}
			if result.Stdout != "" {
				t.Errorf("stdout = %q, want empty", result.Stdout)
			}
		})
	}
}
