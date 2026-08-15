package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestAdapterMutationOperationsIncludingForceForms(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("initial.txt", "initial\n", "initial")
	adapter := git.NewAdapter("git")
	context := context.Background()

	if err := adapter.CreateBranch(context, repository.Path, "removable", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if exists, err := adapter.BranchExists(context, repository.Path, "removable"); err != nil || !exists {
		t.Fatalf("BranchExists() = %t, %v", exists, err)
	}
	if err := adapter.DeleteBranch(context, repository.Path, "removable", false); err != nil {
		t.Fatal(err)
	}

	if err := adapter.CreateBranch(context, repository.Path, "force-delete", "HEAD"); err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "checkout", "force-delete")
	repository.CommitFile("unmerged.txt", "unmerged\n", "unmerged")
	repository.Run(t, "checkout", "main")
	if err := adapter.DeleteBranch(context, repository.Path, "force-delete", false); err == nil {
		t.Fatal("DeleteBranch(force=false) error = nil, want unmerged branch refusal")
	}
	if err := adapter.DeleteBranch(context, repository.Path, "force-delete", true); err != nil {
		t.Fatalf("DeleteBranch(force=true) error = %v", err)
	}

	if err := adapter.CreateBranch(context, repository.Path, "worktree", "HEAD"); err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(t.TempDir(), "worktree")
	if err := adapter.AddWorktree(context, repository.Path, worktreePath, "worktree"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RemoveWorktree(context, repository.Path, worktreePath, false); err != nil {
		t.Fatal(err)
	}

	if err := adapter.CreateBranch(context, repository.Path, "force-worktree", "HEAD"); err != nil {
		t.Fatal(err)
	}
	forcePath := filepath.Join(t.TempDir(), "force-worktree")
	if err := adapter.AddWorktree(context, repository.Path, forcePath, "force-worktree"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forcePath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RemoveWorktree(context, repository.Path, forcePath, false); err == nil {
		t.Fatal("RemoveWorktree(force=false) error = nil, want dirty worktree refusal")
	}
	if err := adapter.RemoveWorktree(context, repository.Path, forcePath, true); err != nil {
		t.Fatalf("RemoveWorktree(force=true) error = %v", err)
	}
	if err := adapter.WorktreePrune(context, repository.Path); err != nil {
		t.Fatal(err)
	}
	if err := adapter.WorktreeRepair(context, repository.Path); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterReturnsTypedErrors(t *testing.T) {
	adapter := git.NewAdapter("git")
	_, err := adapter.TopLevel(context.Background(), t.TempDir())
	var gitError *git.Error
	if !errors.As(err, &gitError) {
		t.Fatalf("error = %T %v, want *git.Error", err, err)
	}
	if gitError.Repository == "" || len(gitError.Command) == 0 || gitError.ExitCode == 0 {
		t.Errorf("typed error = %#v, want command/repository/non-zero exit", gitError)
	}
}
