package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteVersion(t *testing.T) {
	for _, argument := range []string{"--version", "-v"} {
		t.Run(argument, func(t *testing.T) {
			result := testutil.RunCommand(t, cli.Execute, argument)

			if result.Err != nil {
				t.Fatalf("Execute() error = %v", result.Err)
			}
			if got, want := result.Stdout, "wtree 0.2.0\n"; got != want {
				t.Errorf("stdout = %q, want %q", got, want)
			}
			if result.Stderr != "" {
				t.Errorf("stderr = %q, want empty", result.Stderr)
			}
		})
	}
}

func TestInitDoesNotExposeRemovedAddIgnoreFlag(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", t.TempDir(), "--add-ignore")
	if result.Err == nil || cli.ExitCode(result.Err) != 2 {
		t.Fatalf("init --add-ignore = %#v, want invalid arguments", result)
	}
	if !strings.Contains(result.Err.Error(), "unknown flag: --add-ignore") {
		t.Fatalf("init --add-ignore error = %v", result.Err)
	}
	if _, err := os.Stat(filepath.Join(project.Path, ".wtree.yml")); !os.IsNotExist(err) {
		t.Fatalf("unsupported flag created local config: %v", err)
	}
}

func TestCreateHelpDescribesAutomaticIgnoreProtection(t *testing.T) {
	for _, arguments := range [][]string{{"create", "--help"}, {"create", "--how-to"}} {
		result := testutil.RunCommand(t, cli.Execute, arguments...)
		if result.Err != nil || !containsAll(result.Stdout, "automatically ensure", "--dry-run") || strings.Contains(result.Stdout, "committed parent .gitignore") {
			t.Fatalf("create documentation %v = %#v", arguments, result)
		}
	}
}

func TestExecuteCreateDryRunRootOnlyPreservesV1JSONPlan(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}

	result := testutil.RunCommand(t, cli.Execute,
		"create", "--project", project.Path, "feature/root", "--dry-run", "--data-dir", data, "--path", target, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("root create dry-run = %#v", result)
	}
	assertWorkspacePlanV1JSON(t, result.Stdout, "create", "feature/root", target,
		[]workspacePlanRepository{{ID: "root", Mount: ".", Path: target}},
		[]workspacePlanStep{
			{Action: "create_branch", RepositoryID: "root", Inverse: "delete_branch"},
			{Action: "add_worktree", RepositoryID: "root", Inverse: "remove_worktree"},
		})
}

func TestExecuteCreateDryRunRendersDeterministicJSONPlanWithoutMutation(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(project.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	ignorePath := filepath.Join(project.Path, ".gitignore")
	beforeIgnore, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeIgnoreInfo, err := os.Stat(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	beforeRegistry, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	human := testutil.RunCommand(t, cli.Execute,
		"create", "--project", project.Path, "feature/login", "--dry-run", "--data-dir", data, "--path", target, "--mount", "backend=api")
	if human.Err != nil || human.Stderr != "" || !containsAll(human.Stdout,
		"Automatic ignore protection (execution will ensure):", filepath.Join(target, ".gitignore"), "/api/", "No changes made. Dry run performs no mutation.") {
		t.Fatalf("human create dry-run = %#v", human)
	}
	result := testutil.RunCommand(t, cli.Execute,
		"create", "--project", project.Path, "feature/login", "--dry-run", "--data-dir", data, "--path", target, "--mount", "backend=api", "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("create dry-run = %#v", result)
	}
	assertWorkspacePlanV1JSON(t, result.Stdout, "create", "feature/login", target,
		[]workspacePlanRepository{
			{ID: "root", Mount: ".", Path: target},
			{ID: "backend", ParentID: "root", Mount: "api", Path: filepath.Join(target, "api")},
		},
		[]workspacePlanStep{
			{Action: "create_branch", RepositoryID: "root", Inverse: "delete_branch"},
			{Action: "add_worktree", RepositoryID: "root", Inverse: "remove_worktree"},
			{Action: "create_branch", RepositoryID: "backend", Inverse: "delete_branch"},
			{Action: "add_worktree", RepositoryID: "backend", Inverse: "remove_worktree"},
		})
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run created target: %v", err)
	}
	afterIgnore, err := os.ReadFile(ignorePath)
	if err != nil || string(afterIgnore) != string(beforeIgnore) {
		t.Fatalf("dry-run changed source ignore file: before=%q after=%q err=%v", beforeIgnore, afterIgnore, err)
	}
	afterIgnoreInfo, err := os.Stat(ignorePath)
	if err != nil || !afterIgnoreInfo.ModTime().Equal(beforeIgnoreInfo.ModTime()) {
		t.Fatalf("dry-run changed source ignore mtime: before=%v after=%v err=%v", beforeIgnoreInfo.ModTime(), afterIgnoreInfo.ModTime(), err)
	}
	afterRegistry, err := os.ReadFile(registryPath)
	if err != nil || string(afterRegistry) != string(beforeRegistry) {
		t.Fatalf("dry-run changed registry: before=%q after=%q err=%v", beforeRegistry, afterRegistry, err)
	}
	if exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), project.Path, "feature/login"); branchErr != nil || exists {
		t.Fatalf("dry-run created branch: exists=%t error=%v", exists, branchErr)
	}
	resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: project.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.WorkspaceStatePath(data, resolution.Project.ID, "feature-login")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created workspace state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "projects", resolution.Project.ID, "recovery", "feature-login.json")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created recovery state: %v", err)
	}
}

func TestExecuteCreateCreatesBranchWorktreeAndState(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}

	result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/login", "--data-dir", data, "--path", target)
	if result.Err != nil || result.Stderr != "" || !strings.Contains(result.Stdout, "Every nested mount was already protected.\n") {
		t.Fatalf("create error = %v, result = %#v", result.Err, result)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatalf("created worktree .git: %v", err)
	}
	project.Run(t, "show-ref", "--verify", "--quiet", "refs/heads/feature/login")
}

func TestExecuteCreateReportsActualAutomaticIgnoreUpdates(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	if err := os.Rename(backend.Path, filepath.Join(root.Path, "backend")); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.CommitFile(".gitignore", "# preserved\n", "base without nested mount rule")

	result := testutil.RunCommand(t, cli.Execute, "create", "--project", root.Path, "feature/ignore-output", "--data-dir", data, "--path", target)
	if result.Err != nil || result.Stderr != "" || !containsAll(result.Stdout,
		"Created workspace: feature/ignore-output", "Changed .gitignore files:", filepath.Join(target, ".gitignore"), "/backend/", "Review and commit .gitignore changes; wtree did not stage or commit them.") {
		t.Fatalf("create output = %#v", result)
	}
	if got, err := os.ReadFile(filepath.Join(target, ".gitignore")); err != nil || string(got) != "# preserved\n/backend/\n" {
		t.Fatalf("created ignore = %q, %v", got, err)
	}
}

func TestExecuteCreateFromJSONVerboseAndForceContracts(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("first.txt", "first\n", "first")
	project.CommitFile("second.txt", "second\n", "second")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	target := filepath.Join(t.TempDir(), "workspace")
	jsonResult := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/base", "--from", "HEAD~1", "--data-dir", data, "--path", target, "--json", "--verbose")
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
	force := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/force", "--data-dir", data, "--path", forceTarget, "--force")
	if force.Err == nil || cli.ExitCode(force.Err) != 2 {
		t.Fatalf("create --force = %#v, want invalid arguments", force)
	}
	if _, err := os.Stat(forceTarget); !os.IsNotExist(err) {
		t.Fatalf("--force created target: %v", err)
	}
}

func TestExecuteCreateVerboseHumanOutput(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/verbose", "--data-dir", data, "--path", target, "--verbose")
	if result.Err != nil || strings.Contains(result.Stdout, "execute_started") || !strings.Contains(result.Stdout, "Created workspace: feature/verbose\n") || !strings.Contains(result.Stderr, "execute_started create_branch:root\n") {
		t.Fatalf("verbose create = %#v", result)
	}
}

func TestExecuteContextCancellationStopsCreateBeforeMutation(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := testutil.RunCommand(t, func(args []string, stdout, stderr io.Writer) error {
		return cli.ExecuteContext(ctx, args, stdout, stderr)
	}, "create", "--project", project.Path, "feature/canceled", "--data-dir", data, "--path", target)
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
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data := t.TempDir()
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	for _, branch := range []string{".feature", "feature/", "foo/.bar", "foo..bar", "foo.lock", "foo@{bar", "feature//child"} {
		t.Run(branch, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "workspace")
			result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, branch, "--dry-run", "--data-dir", data, "--path", target, "--json")
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
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	project.Run(t, "branch", "feature")
	result := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature", "--dry-run", "--data-dir", data, "--path", target)
	if result.Err != nil || !strings.Contains(result.Stdout, "Operation: checkout\n") || !strings.Contains(result.Stdout, "No changes made.\n") || result.Stderr != "" {
		t.Fatalf("checkout dry-run = %#v", result)
	}
	jsonResult := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature", "--dry-run", "--data-dir", data, "--path", target, "--json")
	if jsonResult.Err != nil || jsonResult.Stderr != "" {
		t.Fatalf("checkout JSON dry-run = %#v", jsonResult)
	}
	assertWorkspacePlanV1JSON(t, jsonResult.Stdout, "checkout", "feature", target,
		[]workspacePlanRepository{{ID: "root", Mount: ".", Path: target}},
		[]workspacePlanStep{{Action: "add_worktree", RepositoryID: "root", Inverse: "remove_worktree"}})
	unsupported := testutil.RunCommand(t, cli.Execute, "checkout", "--project", project.Path, "feature", "--from", "main", "--dry-run", "--data-dir", data, "--path", target)
	if unsupported.Err == nil || cli.ExitCode(unsupported.Err) != 2 {
		t.Fatalf("checkout --from = %#v, want invalid arguments", unsupported)
	}
}

func TestExecuteCreateDryRunNestedDefaultMountPreservesV1JSONPlan(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}

	result := testutil.RunCommand(t, cli.Execute,
		"create", "--project", root.Path, "feature/default", "--dry-run", "--data-dir", data, "--path", target, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("nested default create dry-run = %#v", result)
	}
	assertWorkspacePlanV1JSON(t, result.Stdout, "create", "feature/default", target,
		[]workspacePlanRepository{
			{ID: "root", Mount: ".", Path: target},
			{ID: "backend", ParentID: "root", Mount: "backend", Path: filepath.Join(target, "backend")},
		},
		[]workspacePlanStep{
			{Action: "create_branch", RepositoryID: "root", Inverse: "delete_branch"},
			{Action: "add_worktree", RepositoryID: "root", Inverse: "remove_worktree"},
			{Action: "create_branch", RepositoryID: "backend", Inverse: "delete_branch"},
			{Action: "add_worktree", RepositoryID: "backend", Inverse: "remove_worktree"},
		})
}

func TestExecuteCreateDryRunAppliesRepeatedMountsToNestedRepositories(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	shared := testutil.NewPushedGitRepository(t)
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
	root.CommitFile(".gitignore", "/api/\n", "ignore custom backend mount")
	backend.Path = backendPath
	backend.CommitFile(".gitignore", "/common/\n", "ignore custom shared mount")
	result := testutil.RunCommand(t, cli.Execute,
		"create", "--project", root.Path, "feature", "--dry-run", "--data-dir", data, "--path", target,
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
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	result := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", t.TempDir(), "--dry-run", "--json")
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !strings.Contains(result.Stdout, "\"dryRun\":true") || result.Stderr != "" {
		t.Fatalf("output=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestExecuteInitRejectsRegisteredMissingConfigUntilExplicitRegistryCleanup(t *testing.T) {
	for _, operation := range []string{"prune", "unregister"} {
		t.Run(operation, func(t *testing.T) {
			repository := testutil.NewPushedGitRepository(t)
			repository.CommitFile("readme", "x\n", "initial")
			data := t.TempDir()
			if result := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", data); result.Err != nil {
				t.Fatalf("first init = %#v", result)
			}
			registryPath := filepath.Join(data, "registry.json")
			registry, err := store.ReadRegistry(registryPath)
			if err != nil || len(registry.Projects) != 1 {
				t.Fatalf("first registry = %#v, %v", registry, err)
			}
			var firstID string
			for id := range registry.Projects {
				firstID = id
			}
			if err := os.Remove(filepath.Join(repository.Path, ".wtree.yml")); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, dryRun := range []bool{true, false} {
				arguments := []string{"init", repository.Path, "--data-dir", data, "--json"}
				if dryRun {
					arguments = append(arguments, "--dry-run")
				}
				result := testutil.RunCommand(t, cli.Execute, arguments...)
				var envelope struct {
					Error struct {
						Code, Message string
					} `json:"error"`
				}
				if result.Err == nil || result.Stderr != "" || cli.ExitCode(result.Err) != 8 || json.Unmarshal([]byte(result.Stdout), &envelope) != nil || envelope.Error.Code != "conflict" || !strings.Contains(envelope.Error.Message, firstID) || !strings.Contains(envelope.Error.Message, "wtree project list") {
					t.Fatalf("duplicate init dryRun=%t = %#v envelope=%#v", dryRun, result, envelope)
				}
				after, readErr := os.ReadFile(registryPath)
				if readErr != nil || string(after) != string(before) {
					t.Fatalf("duplicate init rewrote registry: %v", readErr)
				}
			}
			if result := testutil.RunCommand(t, cli.Execute, "project", operation, firstID, "--data-dir", data); result.Err != nil {
				t.Fatalf("%s = %#v", operation, result)
			}
			if result := testutil.RunCommand(t, cli.Execute, "init", repository.Path, "--data-dir", data); result.Err != nil {
				t.Fatalf("init after explicit cleanup = %#v", result)
			}
			registry, err = store.ReadRegistry(registryPath)
			_, oldRegistrationRemains := registry.Projects[firstID]
			if err != nil || len(registry.Projects) != 1 || oldRegistrationRemains {
				t.Fatalf("retry registry = %#v, %v", registry, err)
			}
		})
	}
}

func TestExecuteInitRejectsRootProjectSelector(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	for _, arguments := range [][]string{
		{"init", "--project", repository.Path, repository.Path},
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
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/retained", "--data-dir", data, "--path", target); result.Err != nil {
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
			arguments := append(append([]string(nil), test.args...), "--project", project.Path)
			result := testutil.RunCommand(t, cli.Execute, arguments...)
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
	project := testutil.NewPushedGitRepository(t)
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
	invalid := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, ".invalid", "--data-dir", data, "--path", filepath.Join(t.TempDir(), "invalid"))
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
	valid := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/valid", "--data-dir", data, "--path", filepath.Join(t.TempDir(), "valid"))
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
	for _, want := range []string{"independent nested Git repositories", "default workspace", "wtree project list", "--worktree-root", "--dry-run", "--json"} {
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
	repository := testutil.NewPushedGitRepository(t)
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

type workspacePlanRepository struct {
	ID       string
	ParentID string
	Mount    string
	Path     string
}

type workspacePlanStep struct {
	Action       string `json:"action"`
	RepositoryID string `json:"repositoryId"`
	Inverse      string `json:"inverse"`
}

func assertWorkspacePlanV1JSON(t *testing.T, output, operation, workspaceName, rootPath string, repositories []workspacePlanRepository, steps []workspacePlanStep) {
	t.Helper()
	if bytes.Contains(bytes.ToLower([]byte(output)), []byte("ignore")) {
		t.Fatalf("workspace v1 JSON contains ignore metadata: %s", output)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatalf("decode workspace v1 JSON: %v", err)
	}
	assertJSONKeys(t, document, "version", "operation", "projectId", "workspaceName", "workspaceId", "rootPath", "logicalRoot", "baseRepository", "repositories", "steps")

	var value struct {
		Version        int               `json:"version"`
		Operation      string            `json:"operation"`
		ProjectID      string            `json:"projectId"`
		WorkspaceName  string            `json:"workspaceName"`
		WorkspaceID    string            `json:"workspaceId"`
		RootPath       string            `json:"rootPath"`
		LogicalRoot    string            `json:"logicalRoot"`
		BaseRepository string            `json:"baseRepository"`
		Repositories   []json.RawMessage `json:"repositories"`
		Steps          []json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		t.Fatalf("decode workspace v1 JSON fields: %v", err)
	}
	if value.Version != 1 || value.Operation != operation || value.ProjectID == "" || value.WorkspaceName != workspaceName || value.WorkspaceID == "" || value.RootPath != rootPath || value.LogicalRoot != rootPath || value.BaseRepository == "" {
		t.Fatalf("workspace v1 JSON fields = %#v, want version/operation/project/workspace/root %d/%q/non-empty/%q/%q", value, 1, operation, workspaceName, rootPath)
	}
	if len(value.Repositories) != len(repositories) {
		t.Fatalf("workspace v1 JSON repositories = %d, want %d", len(value.Repositories), len(repositories))
	}
	for index, want := range repositories {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value.Repositories[index], &object); err != nil {
			t.Fatalf("decode repository %d: %v", index, err)
		}
		keys := []string{"id", "base", "branch", "mount", "path"}
		if want.ParentID != "" {
			keys = append(keys, "parentId")
		}
		assertJSONKeys(t, object, keys...)
		var repository struct {
			ID       string `json:"id"`
			ParentID string `json:"parentId"`
			Base     string `json:"base"`
			Branch   string `json:"branch"`
			Mount    string `json:"mount"`
			Path     string `json:"path"`
		}
		if err := json.Unmarshal(value.Repositories[index], &repository); err != nil {
			t.Fatalf("decode repository %d fields: %v", index, err)
		}
		if repository.ID != want.ID || repository.ParentID != want.ParentID || repository.Base == "" || repository.Branch != workspaceName || repository.Mount != want.Mount || repository.Path != want.Path {
			t.Fatalf("workspace v1 JSON repository %d = %#v, want id/parent/non-empty-base/branch/mount/path %#v", index, repository, want)
		}
	}

	if len(value.Steps) != len(steps) {
		t.Fatalf("workspace v1 JSON steps = %d, want %d", len(value.Steps), len(steps))
	}
	for index, want := range steps {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value.Steps[index], &object); err != nil {
			t.Fatalf("decode step %d: %v", index, err)
		}
		assertJSONKeys(t, object, "action", "repositoryId", "inverse")
		var step workspacePlanStep
		if err := json.Unmarshal(value.Steps[index], &step); err != nil {
			t.Fatalf("decode step %d fields: %v", index, err)
		}
		if step != want {
			t.Fatalf("workspace v1 JSON step %d = %#v, want %#v", index, step, want)
		}
	}
}

func assertJSONKeys(t *testing.T, object map[string]json.RawMessage, keys ...string) {
	t.Helper()
	if len(object) != len(keys) {
		t.Fatalf("JSON keys = %v, want %v", jsonObjectKeys(object), keys)
	}
	for _, key := range keys {
		if _, found := object[key]; !found {
			t.Fatalf("JSON keys = %v, want key %q", jsonObjectKeys(object), key)
		}
	}
}

func jsonObjectKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}
