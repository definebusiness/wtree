package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/cli"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestExecuteStatusRendersCleanWorkspaceJSON(t *testing.T) {
	project := testutil.NewGitRepository(t)
	project.CommitFile("root.txt", "root\n", "root")
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", project.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "create", "feature/status", "--data-dir", data, "--path", target); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	// State directories are keyed by project ID; obtain it from the project
	// configuration rather than reconstructing a checkout path.
	resolution, err := service.NewResolver().Resolve(context.Background(), service.ResolveRequest{Path: project.Path, ProjectPath: project.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(resolution.Project, data, "feature/status")
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, resolution.Project.ID, workspace.ID)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	result := testutil.RunCommand(t, cli.Execute, "--project", project.Path, "status", "feature/status", "--data-dir", data, "--json")
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("status = %#v", result)
	}
	var value struct {
		Workspace    string `json:"workspace"`
		Repositories []struct {
			ID     string `json:"id"`
			Branch string `json:"branch"`
			Path   string `json:"path"`
			Clean  bool   `json:"clean"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
	if value.Workspace != "feature/status" || len(value.Repositories) != 1 || value.Repositories[0].ID != "root" || value.Repositories[0].Branch != "feature/status" || value.Repositories[0].Path != target || !value.Repositories[0].Clean {
		t.Fatalf("status JSON = %s", result.Stdout)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("status mutated state: before=%q after=%q error=%v", before, after, err)
	}
}

func TestExecuteStatusInfersCurrentWorkspaceAndRendersHumanTable(t *testing.T) {
	root := testutil.NewGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backendPath := filepath.Join(root.Path, "backend")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	data, target := t.TempDir(), filepath.Join(t.TempDir(), "workspace")
	if result := testutil.RunCommand(t, cli.Execute, "init", root.Path, "--data-dir", data); result.Err != nil {
		t.Fatalf("init = %#v", result)
	}
	if result := testutil.RunCommand(t, cli.Execute, "--project", root.Path, "create", "feature/inferred", "--data-dir", data, "--path", target, "--mount", "backend=api"); result.Err != nil {
		t.Fatalf("create = %#v", result)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(target, "api")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	result := testutil.RunCommand(t, cli.Execute, "status", "--data-dir", data)
	if result.Err != nil || result.Stderr != "" {
		t.Fatalf("status = %#v", result)
	}
	want := "Workspace: feature/inferred\n\nREPOSITORY  BRANCH            MOUNT  STATUS\nroot        feature/inferred  .      clean\nbackend     feature/inferred  api    clean\n"
	if result.Stdout != want {
		t.Fatalf("status stdout = %q, want %q", result.Stdout, want)
	}
}
