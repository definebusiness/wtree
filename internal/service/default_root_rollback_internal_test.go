package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
)

// This regression exercises the default (<worktree-root>/<project>/<name>)
// target, whose missing ancestors are not covered by explicit-target tests.
func TestWorkspaceDefaultRootRollbackCleansOwnedAncestors(t *testing.T) {
	for _, operation := range []struct {
		name string
		kind plan.Operation
		run  func(*WorkspaceCreator, context.Context, domain.Project, WorkspacePlanRequest) error
	}{
		{name: "create", kind: plan.Create, run: func(c *WorkspaceCreator, ctx context.Context, p domain.Project, r WorkspacePlanRequest) error {
			_, err := c.Create(ctx, p, r, nil)
			return err
		}},
		{name: "checkout", kind: plan.Checkout, run: func(c *WorkspaceCreator, ctx context.Context, p domain.Project, r WorkspacePlanRequest) error {
			_, err := c.Checkout(ctx, p, r, nil)
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			project, data := rootWorkspaceProject(t)
			adapter := gitadapter.NewAdapter("git")
			branch := "feature/default-rollback-" + operation.name
			head, err := adapter.Head(context.Background(), project.Repositories[0].SourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if operation.name == "checkout" {
				if err := adapter.CreateBranch(context.Background(), project.Repositories[0].SourcePath, branch, head); err != nil {
					t.Fatal(err)
				}
			}
			// Keep the default-root path itself short enough to exercise Git's
			// rollback behavior rather than Git-for-Windows' internal GIT_DIR
			// buffer limit for deeply named Go test temp directories.
			boundary, err := os.MkdirTemp("", "wtree-rb-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(boundary) })
			marker := filepath.Join(boundary, "keep.txt")
			markerBytes := []byte("nearest pre-existing boundary\n")
			if err := os.WriteFile(marker, markerBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			boundaryBefore, err := os.Lstat(boundary)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(boundary, "missing-worktree-root")
			request := WorkspacePlanRequest{Operation: operation.kind, WorkspaceName: branch, WorktreeRoot: root, DataDir: data}
			planned, err := NewWorkspacePlanner().Plan(context.Background(), project, request)
			if err != nil {
				t.Fatal(err)
			}
			transaction := NewWorkspaceTransactionWith(lock.Manager{}, func(string, store.WorkspaceState) error { return errors.New("injected state publication failure") }, store.WriteRecovery, os.Remove)
			err = operation.run(NewWorkspaceCreatorWith(adapter, transaction), context.Background(), project, request)
			if err == nil || !HasCleanRollback(err) {
				t.Fatalf("%s error = %v, want clean rollback", operation.name, err)
			}
			projectDirectory := filepath.Join(root, project.ID)
			if _, statErr := os.Lstat(projectDirectory); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("owned project ancestor remains: %v", statErr)
			}
			if _, statErr := os.Lstat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("owned worktree root remains: %v", statErr)
			}
			if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, request.WorkspaceName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("state remains: %v", statErr)
			}
			if _, statErr := os.Lstat(RecoveryRecordPath(data, planned)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("recovery remains after clean rollback: %v", statErr)
			}
			boundaryAfter, statErr := os.Lstat(boundary)
			if statErr != nil || !os.SameFile(boundaryBefore, boundaryAfter) {
				t.Fatalf("nearest boundary changed: before=%v after=%v err=%v", boundaryBefore, boundaryAfter, statErr)
			}
			if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != string(markerBytes) {
				t.Fatalf("nearest boundary marker = %q, err=%v", got, readErr)
			}
			if operation.name == "create" {
				exists, branchErr := adapter.BranchExists(context.Background(), project.Repositories[0].SourcePath, branch)
				if branchErr != nil || exists {
					t.Fatalf("create branch retained: exists=%t err=%v", exists, branchErr)
				}
			}
			if err := operation.run(NewWorkspaceCreator(), context.Background(), project, request); err != nil {
				t.Fatalf("retry %s: %v", operation.name, err)
			}
		})
	}
}
