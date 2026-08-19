package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteImportDryRunJSONFromExternalWorkspace(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "external")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	root.Run(t, "branch", "feature/import")
	root.Run(t, "worktree", "add", target, "feature/import")
	result := testutil.RunCommand(t, cli.Execute, "import", "--project", root.Path, target, "--name", "imported", "--data-dir", data, "--dry-run", "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("import dry-run = %#v", result)
	}
	var value struct {
		WorkspaceName string `json:"workspaceName"`
		Repositories  []struct {
			ID string `json:"id"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if value.WorkspaceName != "imported" || len(value.Repositories) != 1 || value.Repositories[0].ID != "root" {
		t.Fatalf("import JSON = %s", result.Stdout)
	}
}

func TestExecuteImportFromCurrentExternalWorkspacePersistsObservedPath(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "external")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	project.Run(t, "branch", "feature/import")
	project.Run(t, "worktree", "add", target, "feature/import")
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	result := testutil.RunCommand(t, cli.Execute, "import", "--name", "current-import", "--data-dir", data, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("current import = %#v", result)
	}
	var value struct {
		WorkspaceName string `json:"workspaceName"`
		RootPath      string `json:"rootPath"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil || value.WorkspaceName != "current-import" || value.RootPath != canonical {
		t.Fatalf("current import = %s value=%#v canonical=%q error=%v", result.Stdout, value, canonical, err)
	}
}

func TestExecuteImportRequiresNameForDivergentCheckouts(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(project.Path, "api")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	backend.Path = backendPath
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "external")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	project.Run(t, "branch", "feature/root")
	project.Run(t, "worktree", "add", target, "feature/root")
	backend.Run(t, "branch", "feature/backend")
	backend.Run(t, "worktree", "add", filepath.Join(target, "api"), "feature/backend")

	unnamed := testutil.RunCommand(t, cli.Execute, "import", "--project", project.Path, target, "--data-dir", data)
	if unnamed.Err == nil || cli.ExitCode(unnamed.Err) != 5 || unnamed.Stdout != "" || unnamed.Stderr != "" {
		t.Fatalf("unnamed import = %#v", unnamed)
	}
	named := testutil.RunCommand(t, cli.Execute, "import", "--project", project.Path, target, "--name", "named", "--data-dir", data, "--json")
	if named.Err != nil || named.Stderr != "" {
		t.Fatalf("named import = %#v", named)
	}
}

func TestExecuteImportDryRunDoesNotRewriteStaleRegistry(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		command   func(project, target, data string) []string
		inProject bool
	}{
		{
			name: "explicit project",
			command: func(project, target, data string) []string {
				return []string{"import", "--project", project, target, "--name", "imported", "--data-dir", data, "--dry-run", "--json"}
			},
		},
		{
			name: "local project",
			command: func(_ string, target, data string) []string {
				return []string{"import", target, "--name", "imported", "--data-dir", data, "--dry-run", "--json"}
			},
			inProject: true,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project := testutil.NewPushedGitRepository(t)
			project.CommitFile("root.txt", "root\n", "root")
			data, target := t.TempDir(), filepath.Join(t.TempDir(), "external")
			if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
				t.Fatalf("init = %#v", result)
			}
			project.Run(t, "branch", "feature/import")
			project.Run(t, "worktree", "add", target, "feature/import")
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
			if scenario.inProject {
				previous, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chdir(project.Path); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chdir(previous) })
			}
			result := testutil.RunCommand(t, cli.Execute, scenario.command(project.Path, target, data)...)
			if result.Err != nil || result.Stderr != "" {
				t.Fatalf("import dry-run = %#v", result)
			}
			after, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("dry-run rewrote registry\nbefore: %s\nafter: %s", before, after)
			}
		})
	}
}

func TestExecuteImportReconcilesStaleRegistryOnlyWhenExecuting(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "external")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	project.Run(t, "branch", "feature/import")
	project.Run(t, "worktree", "add", target, "feature/import")
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
	if result := testutil.RunCommand(t, cli.Execute, "import", "--project", project.Path, target, "--name", "imported", "--data-dir", data); result.Err != nil {
		t.Fatalf("executing import = %#v", result)
	}
	registry, err = store.ReadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	wantConfig, err := filepath.EvalSymlinks(filepath.Join(project.Path, ".wtree.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range registry.Projects {
		if entry.ConfigPath != wantConfig {
			t.Fatalf("registry config path = %q, want project config", entry.ConfigPath)
		}
	}
}
