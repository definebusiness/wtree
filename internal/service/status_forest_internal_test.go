package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
)

// This regression was added after the read-only topology fields; it is
// first-run GREEN coverage rather than a reconstructed RED.
func TestStatusReportsForestTopologyDeterministically(t *testing.T) {
	parallelM07RealGitTest(t)
	project, data := forestWorkspaceProject(t)
	root := filepath.Join(t.TempDir(), "status forest")
	created, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(root, data, "feature/status-forest"), nil)
	if err != nil {
		t.Fatalf("create forest workspace: %v", err)
	}
	resolution, err := NewResolver().ResolveReadOnly(context.Background(), ResolveRequest{Path: root, ProjectPath: project.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	statePath := WorkspaceStatePath(data, project.ID, created.WorkspaceID)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	status, err := NewStatusService().Status(context.Background(), project, resolution.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if status.LogicalRoot != resolution.Workspace.RootPath || status.BaseRepository != project.BaseRepository {
		t.Fatalf("direct topology = %#v", status)
	}
	assertForestStatusTopology(t, project, resolution.Workspace, status)
	reversed := project
	for left, right := 0, len(reversed.Repositories)-1; left < right; left, right = left+1, right-1 {
		reversed.Repositories[left], reversed.Repositories[right] = reversed.Repositories[right], reversed.Repositories[left]
	}
	second, err := NewStatusService().Status(context.Background(), reversed, resolution.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(status)
	secondJSON, _ := json.Marshal(second)
	if !reflect.DeepEqual(firstJSON, secondJSON) {
		t.Fatalf("status order changed after repository permutation\n%s\n%s", firstJSON, secondJSON)
	}
	if err := os.WriteFile(filepath.Join(workspaceCheckout(t, resolution.Workspace, "gamma").ResolvedPath, "status-drift.txt"), []byte("drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drift, err := NewStatusService().Status(context.Background(), project, resolution.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if repositoryStatusByID(t, drift, "gamma").Status != "modified" || repositoryStatusByID(t, drift, "gamma").ParentID != "beta" {
		t.Fatalf("nested drift status = %#v", repositoryStatusByID(t, drift, "gamma"))
	}
	after, err := os.ReadFile(statePath)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("status mutated state: %v", err)
	}
	t.Run("doctor and inventory topology", func(t *testing.T) {
		assertDoctorAndInventoryReportForestTopologyWithoutMutation(t, project, data)
	})
}

func assertForestStatusTopology(t *testing.T, project domain.Project, workspace domain.Workspace, status WorkspaceStatus) {
	t.Helper()
	if len(status.Repositories) != len(project.Repositories) {
		t.Fatalf("repositories = %#v", status.Repositories)
	}
	for index, repository := range project.ParentFirst() {
		got := status.Repositories[index]
		checkout := workspaceCheckout(t, workspace, repository.ID)
		if got.ID != repository.ID || got.ParentID != repository.ParentID || got.Mount != checkout.Mount || got.ResolvedPath != checkout.ResolvedPath {
			t.Fatalf("status repository %d = %#v checkout=%#v", index, got, checkout)
		}
	}
}

func workspaceCheckout(t *testing.T, workspace domain.Workspace, id string) domain.Checkout {
	t.Helper()
	for _, checkout := range workspace.Checkouts {
		if checkout.RepositoryID == id {
			return checkout
		}
	}
	t.Fatalf("workspace missing %q", id)
	return domain.Checkout{}
}

func repositoryStatusByID(t *testing.T, value WorkspaceStatus, id string) RepositoryStatus {
	t.Helper()
	for _, repository := range value.Repositories {
		if repository.ID == id {
			return repository
		}
	}
	t.Fatalf("status missing %q", id)
	return RepositoryStatus{}
}
