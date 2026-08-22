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

func TestExecuteRemoveDryRunJSONDoesNotMutate(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/remove", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	result := testutil.RunCommand(t, cli.Execute, "remove", "--project", project.Path, "feature/remove", "--data-dir", data, "--dry-run", "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("remove dry-run = %#v", result)
	}
	var value struct {
		WorkspaceName  string `json:"workspaceName"`
		LogicalRoot    string `json:"logicalRoot"`
		BaseRepository string `json:"baseRepository"`
		Repositories   []struct {
			ID           string `json:"id"`
			ResolvedPath string `json:"resolvedPath"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if value.WorkspaceName != "feature/remove" || value.LogicalRoot != target || value.BaseRepository != "root" || len(value.Repositories) != 1 || value.Repositories[0].ID != "root" || value.Repositories[0].ResolvedPath != target {
		t.Fatalf("remove JSON = %s", result.Stdout)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("dry-run removed workspace: %v", err)
	}
}

func TestExecuteRemoveRefusesDirtyWorkspaceUnlessForceAndReportsOverrideJSON(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/remove", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	if err := os.WriteFile(filepath.Join(target, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused := testutil.RunCommand(t, cli.Execute, "remove", "--project", project.Path, "feature/remove", "--data-dir", data, "--json")
	if refused.Err == nil || cli.ExitCode(refused.Err) != 7 || !strings.Contains(refused.Stdout, "\"code\":\"dirty_workspace\"") {
		t.Fatalf("dirty remove = %#v", refused)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("dirty refusal removed workspace: %v", err)
	}
	forced := testutil.RunCommand(t, cli.Execute, "remove", "--project", project.Path, "feature/remove", "--data-dir", data, "--force", "--json")
	if forced.Err != nil || forced.Stderr != "" {
		t.Fatalf("forced remove = %#v", forced)
	}
	var value struct {
		Overrides []struct {
			RepositoryID string `json:"repositoryId"`
			Reason       string `json:"reason"`
		} `json:"overrides"`
	}
	if err := json.Unmarshal([]byte(forced.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Overrides) != 1 || value.Overrides[0].RepositoryID != "root" || value.Overrides[0].Reason != "untracked files" {
		t.Fatalf("force JSON = %s", forced.Stdout)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("forced remove kept workspace: %v", err)
	}
}
