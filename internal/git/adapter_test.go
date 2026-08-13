package git_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcel/wtree/internal/git"
	"github.com/marcel/wtree/internal/testutil"
)

func TestAdapterReadsHermeticRepositoryFacts(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme.txt", "initial\n", "initial")

	adapter := git.NewAdapter("git")
	context := context.Background()
	commonDir, err := adapter.CommonGitDir(context, repository.Path)
	if err != nil {
		t.Fatalf("CommonGitDir() error = %v", err)
	}
	if !filepath.IsAbs(commonDir) {
		t.Errorf("common Git dir = %q, want absolute", commonDir)
	}
	if _, err := adapter.TopLevel(context, repository.Path); err != nil {
		t.Fatalf("TopLevel() error = %v", err)
	}
	if branch, detached, err := adapter.CurrentBranch(context, repository.Path); err != nil || detached || branch == "" {
		t.Fatalf("CurrentBranch() = (%q, %t, %v), want attached branch", branch, detached, err)
	}
	if head, err := adapter.Head(context, repository.Path); err != nil || head == "" {
		t.Fatalf("Head() = (%q, %v)", head, err)
	}
	if status, err := adapter.Status(context, repository.Path); err != nil || len(status.Entries) != 0 {
		t.Fatalf("Status() = (%#v, %v), want clean", status, err)
	}
	if clean, err := adapter.IsClean(context, repository.Path); err != nil || !clean {
		t.Fatalf("IsClean() = %t, %v; want true", clean, err)
	}
}

func TestAdapterCanonicalizesCommonGitDirFromSymlinkedCheckout(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme.txt", "initial\n", "initial")
	link := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(repository.Path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	commonDir, err := git.NewAdapter("git").CommonGitDir(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	if commonDir != canonical || !filepath.IsAbs(commonDir) {
		t.Errorf("CommonGitDir() = %q, want canonical absolute %q", commonDir, canonical)
	}
}

func TestAdapterIgnoresHostileGlobalConfiguration(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("readme.txt", "initial\n", "initial")
	hostileConfig := filepath.Join(t.TempDir(), "hostile.gitconfig")
	if err := os.WriteFile(hostileConfig, []byte("[core]\n\tbare = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "GIT_CONFIG_GLOBAL="+hostileConfig, "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
	adapter := git.NewAdapterWithEnv("git", environment)

	if _, err := adapter.Status(context.Background(), repository.Path); err != nil {
		t.Fatalf("Status() honored hostile global configuration: %v", err)
	}
}

func TestAdapterCoversWorktreeDirtyDetachedAndMissingRefFacts(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	worktreePath := filepath.Join(t.TempDir(), "worktree with space")
	repository.Run(t, "branch", "feature")
	repository.Run(t, "worktree", "add", worktreePath, "feature")

	adapter := git.NewAdapter("git")
	context := context.Background()
	commonDir, err := adapter.CommonGitDir(context, repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	worktreeCommonDir, err := adapter.CommonGitDir(context, worktreePath)
	if err != nil || worktreeCommonDir != commonDir {
		t.Fatalf("worktree common dir = %q, %v; want %q", worktreeCommonDir, err, commonDir)
	}
	if checkedOut, err := adapter.BranchCheckedOut(context, repository.Path, "feature"); err != nil || !checkedOut {
		t.Fatalf("BranchCheckedOut() = %t, %v; want true", checkedOut, err)
	}

	if err := os.WriteFile(filepath.Join(repository.Path, "tracked.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "add", "tracked.txt")
	if err := os.WriteFile(filepath.Join(repository.Path, "tracked.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.Path, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := adapter.Status(context, repository.Path)
	if err != nil || !status.Staged || !status.Modified || !status.Untracked {
		t.Fatalf("Status() = %#v, %v; want staged/modified/untracked", status, err)
	}
	if _, err := adapter.ResolveRef(context, repository.Path, "refs/heads/not-a-ref"); err == nil {
		t.Fatal("ResolveRef() error = nil, want invalid ref error")
	}

	repository.Run(t, "checkout", "--detach", "HEAD")
	if branch, detached, err := adapter.CurrentBranch(context, repository.Path); err != nil || !detached || branch != "" {
		t.Fatalf("CurrentBranch() = (%q, %t, %v), want detached", branch, detached, err)
	}
}

func TestAdapterReportsOptionalUpstreamAheadBehind(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	adapter := git.NewAdapter("git")

	if ahead, behind, upstream, err := adapter.AheadBehind(context.Background(), repository.Path); err != nil || upstream || ahead != 0 || behind != 0 {
		t.Fatalf("AheadBehind without upstream = (%d, %d, %t, %v)", ahead, behind, upstream, err)
	}
	repository.Run(t, "branch", "upstream/main")
	repository.Run(t, "config", "branch.main.remote", ".")
	repository.Run(t, "config", "branch.main.merge", "refs/heads/upstream/main")
	repository.CommitFile("ahead.txt", "ahead\n", "ahead")
	ahead, behind, upstream, err := adapter.AheadBehind(context.Background(), repository.Path)
	if err != nil || !upstream || ahead != 1 || behind != 0 {
		t.Fatalf("AheadBehind with upstream = (%d, %d, %t, %v), want (1, 0, true, nil)", ahead, behind, upstream, err)
	}
}

func TestAdapterReportsWhetherBranchIsMergedIntoSourceHEAD(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	repository.Run(t, "branch", "merged")
	repository.Run(t, "branch", "unmerged")
	worktree := filepath.Join(t.TempDir(), "unmerged")
	repository.Run(t, "worktree", "add", worktree, "unmerged")
	testutil.GitRepository{Path: worktree}.CommitFile("unmerged.txt", "change\n", "change")
	adapter := git.NewAdapter("git")
	if merged, err := adapter.BranchMerged(context.Background(), repository.Path, "merged"); err != nil || !merged {
		t.Fatalf("BranchMerged merged = %t, %v", merged, err)
	}
	if merged, err := adapter.BranchMerged(context.Background(), repository.Path, "unmerged"); err != nil || merged {
		t.Fatalf("BranchMerged unmerged = %t, %v", merged, err)
	}
}

func TestAdapterStatusUsesOptionalLocksAndLeavesIndexMetadataUntouched(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell wrapper fixture is POSIX-only")
	}
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	tracked := filepath.Join(repository.Path, "tracked.txt")
	if err := os.Chtimes(tracked, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(repository.Path, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	observed := filepath.Join(t.TempDir(), "optional-locks")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$GIT_OPTIONAL_LOCKS\" > %q\nexec git \"$@\"\n", observed)), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git.NewAdapter(wrapper).Status(context.Background(), repository.Path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("status changed index metadata: before=%#v after=%#v", before, after)
	}
	value, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "0" {
		t.Fatalf("GIT_OPTIONAL_LOCKS = %q, want 0", value)
	}
}
