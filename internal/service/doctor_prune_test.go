package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
)

func TestDoctorPrunesOnlyRegisteredMissingWorktreeMetadata(t *testing.T) {
	project, root, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "gone")
	root.Run(t, "branch", "feature/gone")
	root.Run(t, "worktree", "add", target, "feature/gone")
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, project.ID, "gone")
	if err := store.WriteWorkspace(statePath, store.WorkspaceState{ID: "gone", Name: "gone", Path: target, Partial: true, MissingRepositoryIDs: []string{"backend"}, Repositories: map[string]store.CheckoutState{"root": {Branch: "feature/gone", Head: head, Mount: ".", ResolvedPath: target}}}); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	doctor := service.NewDoctorService()
	report, err := doctor.Doctor(context.Background(), project, workspace, data)
	if err != nil || !doctorHasRepair(report, "prune-worktree-metadata") {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, err := doctor.Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := gitadapter.NewAdapter("git").ListWorktrees(context.Background(), root.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := doctor.Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data}); err != nil {
		t.Fatal(err)
	}
	for _, worktree := range mustWorktrees(t, root.Path) {
		if filepath.Clean(worktree) == filepath.Clean(target) {
			t.Fatalf("stale worktree remains %q", target)
		}
	}
}

func mustWorktrees(t *testing.T, path string) []string {
	t.Helper()
	values, err := gitadapter.NewAdapter("git").ListWorktrees(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(values))
	for _, value := range values {
		paths = append(paths, value.Path)
	}
	return paths
}
