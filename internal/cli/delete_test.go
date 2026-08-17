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

func TestExecuteDeleteDryRunJSONDoesNotMutate(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/delete", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	result := testutil.RunCommand(t, cli.Execute, "delete", "--project", project.Path, "feature/delete", "--data-dir", data, "--dry-run", "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("delete dry-run = %#v", result)
	}
	var value struct {
		WorkspaceName string `json:"workspaceName"`
		Branches      []struct {
			RepositoryID string `json:"repositoryId"`
		} `json:"branches"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if value.WorkspaceName != "feature/delete" || len(value.Branches) != 1 || value.Branches[0].RepositoryID != "root" {
		t.Fatalf("delete JSON = %s", result.Stdout)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("dry-run deleted workspace: %v", err)
	}
}

func TestExecuteDeleteForceReportsDirtyAndUnmergedOverridesJSON(t *testing.T) {
	project := testutil.NewPushedGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "create", "--project", project.Path, "feature/delete", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	checkout := testutil.GitRepository{Path: target}
	checkout.CommitFile("unmerged.txt", "unmerged\n", "unmerged")
	if err := os.WriteFile(filepath.Join(target, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused := testutil.RunCommand(t, cli.Execute, "delete", "--project", project.Path, "feature/delete", "--data-dir", data, "--json")
	if refused.Err == nil || cli.ExitCode(refused.Err) != 7 || !strings.Contains(refused.Stdout, "\"code\":\"dirty_workspace\"") {
		t.Fatalf("dirty delete = %#v", refused)
	}
	forced := testutil.RunCommand(t, cli.Execute, "delete", "--project", project.Path, "feature/delete", "--data-dir", data, "--force", "--json")
	if forced.Err != nil || forced.Stderr != "" {
		t.Fatalf("forced delete = %#v", forced)
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
	if len(value.Overrides) != 2 || value.Overrides[0].Reason != "untracked files" || value.Overrides[1].Reason != "unmerged branch" {
		t.Fatalf("force delete JSON = %s", forced.Stdout)
	}
}
