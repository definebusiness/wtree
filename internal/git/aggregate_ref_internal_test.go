package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/testutil"
)

func TestFastForwardRefFailsClosedWhenBranchActivatesAtBoundaries(t *testing.T) {
	for _, point := range []string{"before", "after"} {
		t.Run(point, func(t *testing.T) {
			repository := testutil.NewGitRepository(t)
			repository.CommitFile("one", "one\n", "one")
			old, err := NewAdapter("git").Head(context.Background(), repository.Path)
			if err != nil {
				t.Fatal(err)
			}
			repository.CommitFile("two", "two\n", "two")
			newHead, err := NewAdapter("git").Head(context.Background(), repository.Path)
			if err != nil {
				t.Fatal(err)
			}
			repository.Run(t, "branch", "side", old)
			if point == "before" {
				fastForwardRefBeforeRefUpdate = func() { repository.Run(t, "checkout", "side") }
				defer func() { fastForwardRefBeforeRefUpdate = nil }()
			} else {
				fastForwardRefAfterRefUpdate = func() { repository.Run(t, "checkout", "side") }
				defer func() { fastForwardRefAfterRefUpdate = nil }()
			}
			receipt, err := NewAdapter("git").FastForwardRef(context.Background(), repository.Path, "side", old, newHead)
			if err == nil {
				t.Fatal("activation race was reported as success")
			}
			if point == "before" && receipt != (FastForwardReceipt{}) {
				t.Fatalf("pre-CAS activation returned receipt %#v", receipt)
			}
			if point == "after" && receipt.NewCommit != newHead {
				t.Fatalf("post-CAS activation lost owned receipt %#v", receipt)
			}
		})
	}
}

// A cancellation observed after the exact ref CAS cannot safely rewind the
// branch blindly. The receipt therefore remains an ownership fact for the
// caller's recovery path and a later fresh-context restore may use it.
func TestFastForwardRefReturnsOwnedReceiptWhenCanceledAfterCAS(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("one", "one\n", "one")
	old, err := NewAdapter("git").Head(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	repository.CommitFile("two", "two\n", "two")
	next, err := NewAdapter("git").Head(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "branch", "side", old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fastForwardRefAfterRefUpdate = cancel
	defer func() { fastForwardRefAfterRefUpdate = nil }()
	receipt, err := NewAdapter("git").FastForwardRef(ctx, repository.Path, "side", old, next)
	if err == nil || receipt.Branch != "side" || receipt.OldCommit != old || receipt.NewCommit != next {
		t.Fatalf("FastForwardRef() receipt=%#v err=%v", receipt, err)
	}
	if got, resolveErr := NewAdapter("git").ResolveRef(context.Background(), repository.Path, "refs/heads/side"); resolveErr != nil || got != next {
		t.Fatalf("side after cancellation=%q err=%v want=%q", got, resolveErr, next)
	}
	if err := NewAdapter("git").RestoreFastForwardRef(context.Background(), repository.Path, receipt); err != nil {
		t.Fatalf("RestoreFastForwardRef() = %v", err)
	}
}

func TestFastForwardRefDoesNotMaterializeCheckoutOrRunHooks(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("one", "one\n", "one")
	old, err := NewAdapter("git").Head(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	repository.CommitFile("two", "two\n", "two")
	next, err := NewAdapter("git").Head(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "branch", "side", old)
	hookEnabled := os.PathSeparator != '\\'
	if hookEnabled {
		hook := filepath.Join(repository.Path, ".git", "hooks", "post-checkout")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 97\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	beforeWorktree, err := os.ReadFile(filepath.Join(repository.Path, "two"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := NewAdapter("git").FastForwardRef(context.Background(), repository.Path, "side", old, next)
	if err != nil {
		t.Fatal(err)
	}
	if got := aggregateInternalHead(t, repository.Path); got != next {
		t.Fatalf("source HEAD=%q want=%q", got, next)
	}
	if after, readErr := os.ReadFile(filepath.Join(repository.Path, "two")); readErr != nil || string(after) != string(beforeWorktree) {
		t.Fatalf("source worktree changed=%q err=%v", after, readErr)
	}
	if err := NewAdapter("git").RestoreFastForwardRef(context.Background(), repository.Path, receipt); err != nil {
		t.Fatal(err)
	}
}
