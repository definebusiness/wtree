package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestFetchConfiguredRefUsesAuthenticatedGitBoundaryWithoutLeakingSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	source, remote := pushedRepository(t, "published")
	directory := t.TempDir()
	capture := filepath.Join(directory, "environment")
	binary := filepath.Join(directory, "git")
	canary := "companion-fetch-credential-canary"
	script := "#!/bin/sh\ncase \" $* \" in *\" fetch \"*) env > \"$WTREE_CAPTURE\"; echo helper-diagnostic-$ASKPASS_REQUIRED_SECRET >&2; exit 9;; esac\nexec git \"$@\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	adapter := git.NewAdapterWithEnv(binary, []string{"PATH=" + os.Getenv("PATH"), "WTREE_CAPTURE=" + capture, "SSH_AUTH_SOCK=/tmp/companion-agent", "GIT_ASKPASS=/tmp/companion-askpass", "ASKPASS_REQUIRED_SECRET=" + canary, "HOME=/tmp/companion-home", "GIT_CONFIG_GLOBAL=/tmp/companion-gitconfig", "GIT_TERMINAL_PROMPT=1"})
	if err := adapter.Clone(context.Background(), remote, target, "mirror"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.FetchConfiguredRef(context.Background(), target, "mirror", "refs/heads/published"); err == nil {
		t.Fatal("FetchConfiguredRef error = nil")
	} else if text := err.Error(); strings.Contains(text, canary) || strings.Contains(text, "helper-diagnostic") {
		t.Fatalf("FetchConfiguredRef leaked authentication material: %v", err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SSH_AUTH_SOCK=/tmp/companion-agent", "GIT_ASKPASS=/tmp/companion-askpass", "ASKPASS_REQUIRED_SECRET=" + canary, "HOME=/tmp/companion-home", "GIT_CONFIG_GLOBAL=/tmp/companion-gitconfig", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("authenticated fetch environment lacks %q: %s", want, data)
		}
	}
	_ = source
}

func TestFetchConfiguredRefObservesAndFetchesOnlyTheConfiguredRef(t *testing.T) {
	source, remote := pushedRepository(t, "published")
	target := filepath.Join(t.TempDir(), "target")
	adapter := git.NewAdapter("git")
	if err := adapter.Clone(context.Background(), remote, target, "mirror"); err != nil {
		t.Fatal(err)
	}
	beforeBranches := localBranchNames(t, target)
	beforeHead := readHeadOrEmpty(t, target)

	first, err := adapter.ObserveConfiguredRef(context.Background(), target, "mirror", "refs/heads/published")
	if err != nil || first.Commit != mustHead(t, source) || first.Remote != "mirror" || first.RemoteRef != "refs/heads/published" {
		t.Fatalf("ObserveConfiguredRef() = %#v, %v", first, err)
	}
	if got := localBranchNames(t, target); !sameStrings(got, beforeBranches) || readHeadOrEmpty(t, target) != beforeHead {
		t.Fatalf("observation mutated local checkout: branches=%v head=%q", got, readHeadOrEmpty(t, target))
	}

	source.CommitFile("moved.txt", "moved\n", "move selected ref")
	source.Run(t, "push", "publish", "main:refs/heads/published")
	moved := mustHead(t, source)
	second, err := adapter.ObserveConfiguredRef(context.Background(), target, "mirror", "refs/heads/published")
	if err != nil || second.Commit != moved || second.Commit == first.Commit {
		t.Fatalf("ObserveConfiguredRef() after move = %#v, %v", second, err)
	}
	fetched, err := adapter.FetchConfiguredRef(context.Background(), target, "mirror", "refs/heads/published")
	if err != nil || fetched.PreviousRemoteCommit != "" || fetched.ActualRemoteCommit != moved {
		t.Fatalf("FetchConfiguredRef() = %#v, %v", fetched, err)
	}
	if got := localBranchNames(t, target); !sameStrings(got, beforeBranches) || readHeadOrEmpty(t, target) != beforeHead {
		t.Fatalf("fetch mutated local branch or worktree: branches=%v head=%q", got, readHeadOrEmpty(t, target))
	}

	source.Run(t, "--git-dir", remote, "update-ref", "-d", "refs/heads/published")
	if _, err := adapter.ObserveConfiguredRef(context.Background(), target, "mirror", "refs/heads/published"); !errors.Is(err, git.ErrRemoteRefNotFound) {
		t.Fatalf("ObserveConfiguredRef(deleted) error = %v, want ErrRemoteRefNotFound", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.FetchConfiguredRef(ctx, target, "mirror", "refs/heads/published"); !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchConfiguredRef(cancelled) error = %v", err)
	}
}

func TestRestoreConfiguredRefUsesOnlyItsFetchedGeneration(t *testing.T) {
	source, remote := pushedRepository(t, "published")
	target := filepath.Join(t.TempDir(), "target")
	adapter := git.NewAdapter("git")
	if err := adapter.Clone(context.Background(), remote, target, "mirror"); err != nil {
		t.Fatal(err)
	}
	first, err := adapter.FetchConfiguredRef(context.Background(), target, "mirror", "refs/heads/published")
	if err != nil || first.PreviousRemoteCommit != "" {
		t.Fatalf("first configured fetch=%#v err=%v", first, err)
	}
	if err := adapter.RestoreConfiguredRef(context.Background(), target, first); err != nil {
		t.Fatalf("restore operation-created tracking ref: %v", err)
	}
	if _, err := adapter.ResolveRef(context.Background(), target, "refs/remotes/mirror/published"); err == nil {
		t.Fatal("restore retained operation-created tracking ref")
	}

	if _, err := adapter.FetchConfiguredRef(context.Background(), target, "mirror", "refs/heads/published"); err != nil {
		t.Fatal(err)
	}
	before, err := adapter.ResolveRef(context.Background(), target, "refs/remotes/mirror/published")
	if err != nil {
		t.Fatal(err)
	}
	source.CommitFile("moved.txt", "moved\n", "move selected ref")
	source.Run(t, "push", "publish", "main:refs/heads/published")
	moved, err := adapter.FetchConfiguredRef(context.Background(), target, "mirror", "refs/heads/published")
	if err != nil || moved.PreviousRemoteCommit != before {
		t.Fatalf("moved configured fetch=%#v err=%v", moved, err)
	}
	if err := adapter.RestoreConfiguredRef(context.Background(), target, moved); err != nil {
		t.Fatalf("restore previous remote-tracking generation: %v", err)
	}
	if got, err := adapter.ResolveRef(context.Background(), target, "refs/remotes/mirror/published"); err != nil || got != before {
		t.Fatalf("restored tracking ref=%q err=%v want=%q", got, err, before)
	}
	if err := adapter.RestoreConfiguredRef(context.Background(), target, moved); err == nil {
		t.Fatal("restore accepted a no-longer-owned tracking generation")
	}
}

func TestIsAncestorObservesOnlyLocalCommitReachability(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("one", "one\n", "one")
	ancestor := mustHead(t, repository)
	repository.CommitFile("two", "two\n", "two")
	descendant := mustHead(t, repository)
	adapter := git.NewAdapter("git")
	if found, err := adapter.IsAncestor(context.Background(), repository.Path, ancestor, descendant); err != nil || !found {
		t.Fatalf("IsAncestor(forward) = %t, %v", found, err)
	}
	if found, err := adapter.IsAncestor(context.Background(), repository.Path, descendant, ancestor); err != nil || found {
		t.Fatalf("IsAncestor(reverse) = %t, %v", found, err)
	}
}

func TestFastForwardRestoresOnlyItsOwnedGeneration(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("one", "one\n", "one")
	old := mustHead(t, repository)
	repository.CommitFile("two", "two\n", "two")
	newHead := mustHead(t, repository)
	repository.Run(t, "reset", "--hard", old)
	adapter := git.NewAdapter("git")

	receipt, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newHead)
	if err != nil || receipt.OldCommit != old || receipt.NewCommit != newHead || mustHead(t, repository) != newHead {
		t.Fatalf("FastForward() = %#v, %v", receipt, err)
	}
	if err := adapter.RestoreFastForward(context.Background(), repository.Path, receipt); err != nil || mustHead(t, repository) != old {
		t.Fatalf("RestoreFastForward() = %v, head=%q", err, mustHead(t, repository))
	}

	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", newHead, old); err == nil {
		t.Fatal("FastForward() accepted non-descendant")
	}
	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newHead); err != nil {
		t.Fatal(err)
	}
	repository.CommitFile("three", "three\n", "concurrent movement")
	concurrent := mustHead(t, repository)
	if err := adapter.RestoreFastForward(context.Background(), repository.Path, receipt); err == nil || mustHead(t, repository) != concurrent {
		t.Fatalf("RestoreFastForward() overwrote concurrent movement: %v head=%q", err, mustHead(t, repository))
	}

	repository.Run(t, "reset", "--hard", old)
	if err := os.WriteFile(filepath.Join(repository.Path, "dirty"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newHead); err == nil {
		t.Fatal("FastForward() accepted dirty worktree")
	}
}

func TestFastForwardRefAdvancesOnlyAnInactiveExpectedGeneration(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("one", "one\n", "one")
	old := mustHead(t, repository)
	repository.CommitFile("two", "two\n", "two")
	newHead := mustHead(t, repository)
	repository.Run(t, "branch", "side", old)
	adapter := git.NewAdapter("git")

	receipt, err := adapter.FastForwardRef(context.Background(), repository.Path, "side", old, newHead)
	if err != nil || receipt.OldCommit != old || receipt.NewCommit != newHead {
		t.Fatalf("FastForwardRef() = %#v, %v", receipt, err)
	}
	if got, err := adapter.ResolveRef(context.Background(), repository.Path, "refs/heads/side"); err != nil || got != newHead {
		t.Fatalf("side = %q, %v; want %q", got, err, newHead)
	}
	if err := adapter.RestoreFastForwardRef(context.Background(), repository.Path, receipt); err != nil {
		t.Fatalf("RestoreFastForwardRef() = %v", err)
	}
	if got, err := adapter.ResolveRef(context.Background(), repository.Path, "refs/heads/side"); err != nil || got != old {
		t.Fatalf("restored side = %q, %v; want %q", got, err, old)
	}
	receipt, err = adapter.FastForwardRef(context.Background(), repository.Path, "side", old, newHead)
	if err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "update-ref", "refs/heads/side", old, newHead)
	if err := adapter.RestoreFastForwardRef(context.Background(), repository.Path, receipt); err == nil {
		t.Fatal("RestoreFastForwardRef overwrote concurrent movement")
	}
	if got, err := adapter.ResolveRef(context.Background(), repository.Path, "refs/heads/side"); err != nil || got != old {
		t.Fatalf("concurrent side = %q, %v; want %q", got, err, old)
	}
	if _, err := adapter.FastForwardRef(context.Background(), repository.Path, "main", old, newHead); err == nil {
		t.Fatal("FastForwardRef accepted an attached branch")
	}
	if _, err := adapter.FastForwardRef(context.Background(), repository.Path, "side", newHead, old); err == nil {
		t.Fatal("FastForwardRef accepted divergent generation")
	}
}

func readHeadOrEmpty(t *testing.T, repository string) string {
	t.Helper()
	value, err := git.NewAdapter("git").Head(context.Background(), repository)
	if err == nil {
		return value
	}
	return ""
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
