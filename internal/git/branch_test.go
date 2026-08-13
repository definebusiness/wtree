package git_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/marcel/wtree/internal/git"
	"github.com/marcel/wtree/internal/testutil"
)

func TestAdapterValidatesGitBranchGrammar(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	adapter := git.NewAdapter("git")
	for _, branch := range []string{".feature", "feature/", "foo/.bar", "foo..bar", "foo.lock", "foo@{bar", "feature//child"} {
		valid, err := adapter.ValidBranchName(context.Background(), repository.Path, branch)
		if err != nil || valid {
			t.Fatalf("ValidBranchName(%q) = %t, %v; want false, nil", branch, valid, err)
		}
	}
	for _, branch := range []string{"feature/login", "agent/task-123"} {
		valid, err := adapter.ValidBranchName(context.Background(), repository.Path, branch)
		if err != nil || !valid {
			t.Fatalf("ValidBranchName(%q) = %t, %v; want true, nil", branch, valid, err)
		}
	}
}

func TestAdapterBranchValidationPreservesOperationalFailures(t *testing.T) {
	adapter := git.NewAdapter("git")
	_, err := adapter.ValidBranchName(context.Background(), filepath.Join(t.TempDir(), "missing"), "feature/login")
	if err == nil {
		t.Fatal("ValidBranchName() error = nil, want missing repository failure")
	}
}
