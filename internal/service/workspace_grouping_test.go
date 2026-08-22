package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/plan"
)

func TestWorkspaceGroupingFailedPrepareCleansCallTwoAndCanRetry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	g := newWorkspaceGrouping(root, newWorkspaceFilesystem())
	step, _, err := g.step(plan.RepositoryPlan{ID: "api", Path: filepath.Join(root, "services", "api")}, root)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fs := g.filesystem
	fs.mkdir = func(path string, mode os.FileMode) error {
		calls++
		if calls == 2 {
			return errors.New("call two")
		}
		return os.Mkdir(path, mode)
	}
	g.filesystem = fs
	if err := step.Execute(context.Background()); err == nil {
		t.Fatal("prepare succeeded")
	}
	if err := step.RollbackFailedExecute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("residue: %v", err)
	}
	g = newWorkspaceGrouping(root, newWorkspaceFilesystem())
	step, _, _ = g.step(plan.RepositoryPlan{ID: "api", Path: filepath.Join(root, "services", "api")}, root)
	if err := step.Execute(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestWorkspaceGroupingCreatesAndRollsBackMissingDefaultRootAncestors(t *testing.T) {
	boundary := t.TempDir()
	worktreeRoot := filepath.Join(boundary, "worktrees")
	projectRoot := filepath.Join(worktreeRoot, "project-id")
	root := filepath.Join(projectRoot, "feature-id")
	g := newWorkspaceGrouping(root, newWorkspaceFilesystem())
	step, needed, err := g.step(plan.RepositoryPlan{ID: "api", Path: filepath.Join(root, "services", "api")}, root)
	if err != nil || !needed {
		t.Fatalf("grouping step = %#v, needed=%v, error=%v", step, needed, err)
	}
	if err := step.Execute(context.Background()); err != nil {
		t.Fatalf("prepare missing default ancestors: %v", err)
	}
	for _, path := range []string{worktreeRoot, projectRoot, root, filepath.Join(root, "services")} {
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Fatalf("created directory %q = %#v, %v", path, info, err)
		}
	}
	if err := step.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback missing default ancestors: %v", err)
	}
	if _, err := os.Lstat(worktreeRoot); !os.IsNotExist(err) {
		t.Fatalf("owned ancestor remains after rollback: %v", err)
	}
	if info, err := os.Lstat(boundary); err != nil || !info.IsDir() {
		t.Fatalf("pre-existing boundary changed: %#v, %v", info, err)
	}
}

func TestWorkspaceGroupingEEXISTWinnerIsNotOwned(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	g := newWorkspaceGrouping(root, newWorkspaceFilesystem())
	step, _, _ := g.step(plan.RepositoryPlan{ID: "api", Path: filepath.Join(root, "api")}, root)
	fs := g.filesystem
	fs.mkdir = func(path string, mode os.FileMode) error {
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
		return os.ErrExist
	}
	g.filesystem = fs
	if err := step.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := step.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("winner removed: %v", err)
	}
}

func TestWorkspaceGroupingRefusesDifferentRealDirectoryReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	g := newWorkspaceGrouping(root, newWorkspaceFilesystem())
	item := plan.RepositoryPlan{ID: "api", Path: filepath.Join(root, "api")}
	step, _, _ := g.step(item, root)
	if err := step.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := g.revalidate(item, root); err == nil {
		t.Fatal("revalidation accepted replacement")
	}
	if err := step.Rollback(context.Background()); err == nil {
		t.Fatal("rollback removed replacement")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("replacement missing: %v", err)
	}
}
