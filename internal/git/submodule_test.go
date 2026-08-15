package git_test

import (
	"context"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestAdapterDetectsSubmodules(t *testing.T) {
	parent := testutil.NewGitRepository(t)
	parent.CommitFile("parent.txt", "parent\n", "initial")
	child := testutil.NewGitRepository(t)
	child.CommitFile("child.txt", "child\n", "initial")
	parent.Run(t, "-c", "protocol.file.allow=always", "submodule", "add", child.Path, "modules/child")

	hasSubmodules, err := git.NewAdapter("git").HasSubmodules(context.Background(), parent.Path)
	if err != nil {
		t.Fatalf("HasSubmodules() error = %v", err)
	}
	if !hasSubmodules {
		t.Fatal("HasSubmodules() = false, want true")
	}
}
