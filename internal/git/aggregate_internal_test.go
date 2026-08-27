package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/testutil"
)

func TestFastForwardCompareAndSwapRejectsDeterministicConcurrentMovement(t *testing.T) {
	repository, old, newCommit, raceCommit := aggregateTransitionRepository(t)
	adapter := NewAdapter("git")
	defer resetFastForwardTestSeams()

	fastForwardBeforeRefUpdate = func() { repository.Run(t, "update-ref", "refs/heads/main", raceCommit, old) }
	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit); err == nil {
		t.Fatal("FastForward() accepted concurrent branch movement")
	}
	if got := aggregateInternalHead(t, repository.Path); got != raceCommit {
		t.Fatalf("FastForward() absorbed concurrent generation %q, got %q", raceCommit, got)
	}

	fastForwardBeforeRefUpdate = nil
	repository.Run(t, "reset", "--hard", old)
	receipt, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit)
	if err != nil {
		t.Fatal(err)
	}
	fastForwardBeforeRefUpdate = func() { repository.Run(t, "update-ref", "refs/heads/main", raceCommit, newCommit) }
	if err := adapter.RestoreFastForward(context.Background(), repository.Path, receipt); err == nil {
		t.Fatal("RestoreFastForward() accepted concurrent branch movement")
	}
	if got := aggregateInternalHead(t, repository.Path); got != raceCommit {
		t.Fatalf("RestoreFastForward() erased concurrent generation %q, got %q", raceCommit, got)
	}
}

func TestFetchConfiguredRefReturnsOwnedGenerationAfterMutatingErrorOrCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
	}{
		{name: "error-after-fetch"},
		{name: "cancellation-after-fetch", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, remote := aggregateConfiguredRefRepository(t)
			target := filepath.Join(t.TempDir(), "target")
			adapter := NewAdapter("git")
			if err := adapter.Clone(context.Background(), remote, target, "origin"); err != nil {
				t.Fatal(err)
			}
			first, err := adapter.FetchConfiguredRef(context.Background(), target, "origin", "refs/heads/main")
			if err != nil {
				t.Fatal(err)
			}
			source.CommitFile("moved", "moved\n", "move configured ref")
			source.Run(t, "push", "origin", "main")
			moved := aggregateInternalHead(t, source.Path)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			injected := errors.New("injected post-fetch error")
			configuredRefAfterFetch = func() error {
				if test.cancel {
					cancel()
					return nil
				}
				return injected
			}
			defer func() { configuredRefAfterFetch = nil }()
			receipt, fetchErr := adapter.FetchConfiguredRef(ctx, target, "origin", "refs/heads/main")
			if test.cancel {
				if !errors.Is(fetchErr, context.Canceled) {
					t.Fatalf("fetch error=%v, want cancellation", fetchErr)
				}
			} else if !errors.Is(fetchErr, injected) {
				t.Fatalf("fetch error=%v, want injected error", fetchErr)
			}
			if receipt.Remote != "origin" || receipt.RemoteRef != "refs/heads/main" || receipt.PreviousRemoteCommit != first.ActualRemoteCommit || receipt.ActualRemoteCommit != moved {
				t.Fatalf("partial configured-ref receipt=%#v", receipt)
			}
			if err := adapter.RestoreConfiguredRef(context.Background(), target, receipt); err != nil {
				t.Fatalf("restore partial configured-ref receipt: %v", err)
			}
			if got, err := adapter.ResolveRef(context.Background(), target, "refs/remotes/origin/main"); err != nil || got != first.ActualRemoteCommit {
				t.Fatalf("restored configured ref=%q err=%v want=%q", got, err, first.ActualRemoteCommit)
			}
		})
	}
}

func TestFastForwardRefusesConcurrentFilesystemGenerationsWithoutDataLoss(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, testutil.GitRepository)
		path    string
		want    string
		wantRef string
	}{
		{name: "tracked edit", path: "one", want: "user edit\n", wantRef: "target", prepare: func(t *testing.T, repository testutil.GitRepository) {
			if err := os.WriteFile(filepath.Join(repository.Path, "one"), []byte("user edit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "untracked collision", path: "two", want: "untracked\n", wantRef: "target", prepare: func(t *testing.T, repository testutil.GitRepository) {
			if err := os.WriteFile(filepath.Join(repository.Path, "two"), []byte("untracked\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "ignored collision", path: "two", want: "ignored\n", wantRef: "source", prepare: func(t *testing.T, repository testutil.GitRepository) {
			if err := os.WriteFile(filepath.Join(repository.Path, ".git", "info", "exclude"), []byte("two\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repository.Path, "two"), []byte("ignored\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, old, newCommit, _ := aggregateTransitionRepository(t)
			adapter := NewAdapter("git")
			defer resetFastForwardTestSeams()
			fastForwardAfterRefUpdate = func() { test.prepare(t, repository) }

			if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit); err == nil {
				t.Fatal("FastForward() accepted a concurrent filesystem generation")
			}
			value, err := os.ReadFile(filepath.Join(repository.Path, test.path))
			if err != nil || string(value) != test.want {
				t.Fatalf("concurrent data = %q, %v; want %q", value, err, test.want)
			}
			wantRef := newCommit
			if test.wantRef == "source" {
				wantRef = old
			}
			if got := aggregateInternalHead(t, repository.Path); got != wantRef {
				t.Fatalf("safe failed-transition ref = %q, want %q", got, wantRef)
			}
		})
	}
}

func TestFastForwardFailureCleanupIsRefOnlyAndOwnershipGuarded(t *testing.T) {
	repository, old, newCommit, raceCommit := aggregateTransitionRepository(t)
	adapter := NewAdapter("git")
	defer resetFastForwardTestSeams()

	fastForwardMaterialize = func(*Adapter, context.Context, string, string, string) error { return errInjectedMaterialization }
	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit); !errors.Is(err, errInjectedMaterialization) {
		t.Fatalf("FastForward() error = %v", err)
	}
	if got := aggregateInternalHead(t, repository.Path); got != old {
		t.Fatalf("clean failed materialization did not restore only the source ref: %q", got)
	}
	if err := adapter.assertOwnedBranchGeneration(context.Background(), repository.Path, "main", old); err != nil {
		t.Fatalf("source filesystem/index changed on clean failure: %v", err)
	}

	fastForwardMaterialize = func(adapter *Adapter, ctx context.Context, path, from, to string) error {
		if err := adapter.materializeAttachedWorktree(ctx, path, from, to); err != nil {
			return err
		}
		return errInjectedMaterialization
	}
	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit); err == nil {
		t.Fatal("FastForward() accepted injected post-materialization failure")
	}
	if got := aggregateInternalHead(t, repository.Path); got != newCommit {
		t.Fatalf("cleanup rewrote ref despite target materialization: %q", got)
	}
	if err := adapter.assertOwnedBranchGeneration(context.Background(), repository.Path, "main", newCommit); err != nil {
		t.Fatalf("target generation was destructively reconciled: %v", err)
	}

	repository.Run(t, "reset", "--hard", old)
	fastForwardMaterialize = func(_ *Adapter, _ context.Context, path, _, _ string) error {
		if err := os.WriteFile(filepath.Join(path, "one"), []byte("partial user data\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return errInjectedMaterialization
	}
	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit); err == nil {
		t.Fatal("FastForward() accepted partial failure")
	}
	value, err := os.ReadFile(filepath.Join(repository.Path, "one"))
	if err != nil || string(value) != "partial user data\n" {
		t.Fatalf("partial concurrent data was overwritten: %q, %v", value, err)
	}

	repository.Run(t, "reset", "--hard", old)
	fastForwardMaterialize = func(_ *Adapter, _ context.Context, _ string, _, _ string) error {
		repository.Run(t, "update-ref", "refs/heads/main", raceCommit, newCommit)
		return errInjectedMaterialization
	}
	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit); err == nil {
		t.Fatal("FastForward() accepted ref ownership loss")
	}
	if got := aggregateInternalHead(t, repository.Path); got != raceCommit {
		t.Fatalf("cleanup erased concurrent ref %q: %q", raceCommit, got)
	}
}

func TestFastForwardCancellationUsesOwnershipSafeCleanup(t *testing.T) {
	repository, old, newCommit, _ := aggregateTransitionRepository(t)
	adapter := NewAdapter("git")
	defer resetFastForwardTestSeams()
	ctx, cancel := context.WithCancel(context.Background())
	fastForwardAfterRefUpdate = cancel

	if _, err := adapter.FastForward(ctx, repository.Path, "main", old, newCommit); !errors.Is(err, context.Canceled) {
		t.Fatalf("FastForward() error = %v, want context cancellation", err)
	}
	if got := aggregateInternalHead(t, repository.Path); got != old {
		t.Fatalf("cancellation cleanup did not restore owned ref: %q", got)
	}
	if err := adapter.assertOwnedBranchGeneration(context.Background(), repository.Path, "main", old); err != nil {
		t.Fatalf("cancellation changed source generation: %v", err)
	}
}

func TestRestoreFastForwardRefusesConcurrentTrackedEditWithoutDataLoss(t *testing.T) {
	repository, old, newCommit, _ := aggregateTransitionRepository(t)
	adapter := NewAdapter("git")
	defer resetFastForwardTestSeams()
	receipt, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit)
	if err != nil {
		t.Fatal(err)
	}
	fastForwardAfterRefUpdate = func() {
		if err := os.WriteFile(filepath.Join(repository.Path, "two"), []byte("inverse concurrent edit\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := adapter.RestoreFastForward(context.Background(), repository.Path, receipt); err == nil {
		t.Fatal("RestoreFastForward() accepted concurrent tracked edit")
	}
	value, err := os.ReadFile(filepath.Join(repository.Path, "two"))
	if err != nil || string(value) != "inverse concurrent edit\n" {
		t.Fatalf("inverse concurrent data was overwritten: %q, %v", value, err)
	}
	if got := aggregateInternalHead(t, repository.Path); got != old {
		t.Fatalf("inverse target ref unexpectedly changed: %q", got)
	}
}

func TestFastForwardMaterializerRechecksIgnoredHazards(t *testing.T) {
	repository, old, newCommit, _ := aggregateTransitionRepository(t)
	adapter := NewAdapter("git")
	defer resetFastForwardTestSeams()
	fastForwardMaterialize = func(adapter *Adapter, ctx context.Context, path, from, to string) error {
		if err := os.WriteFile(filepath.Join(path, ".git", "info", "exclude"), []byte("two\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "two"), []byte("last-window ignored data\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return adapter.materializeAttachedWorktree(ctx, path, from, to)
	}
	if _, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit); err == nil {
		t.Fatal("FastForward() accepted an ignored hazard at the materializer boundary")
	}
	value, err := os.ReadFile(filepath.Join(repository.Path, "two"))
	if err != nil || string(value) != "last-window ignored data\n" {
		t.Fatalf("last-window ignored data was overwritten: %q, %v", value, err)
	}
	if got := aggregateInternalHead(t, repository.Path); got != old {
		t.Fatalf("final-window ignored collision did not restore source ref: %q", got)
	}
}

func TestFastForwardAndRestorePreserveIgnoredProjectAndNestedCheckoutState(t *testing.T) {
	repository, old, newCommit := aggregateIgnoredStateTransitionRepository(t)
	adapter := NewAdapter("git")
	projectFile := filepath.Join(repository.Path, ".wtree.yml")
	nestedFile := filepath.Join(repository.Path, "nested", "marker")
	if err := os.WriteFile(projectFile, []byte("project-local: retained\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := aggregateRunGit(filepath.Dir(nestedFile), "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nestedFile, []byte("nested checkout retained\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	receipt, err := adapter.FastForward(context.Background(), repository.Path, "main", old, newCommit)
	if err != nil || aggregateInternalHead(t, repository.Path) != newCommit {
		t.Fatalf("FastForward() = %#v, %v", receipt, err)
	}
	assertAggregateFile(t, projectFile, "project-local: retained\n")
	assertAggregateFile(t, nestedFile, "nested checkout retained\n")
	if err := adapter.RestoreFastForward(context.Background(), repository.Path, receipt); err != nil || aggregateInternalHead(t, repository.Path) != old {
		t.Fatalf("RestoreFastForward() = %v", err)
	}
	assertAggregateFile(t, projectFile, "project-local: retained\n")
	assertAggregateFile(t, nestedFile, "nested checkout retained\n")
}

func TestFastForwardSupportsSHA256ObjectFormat(t *testing.T) {
	repository := t.TempDir()
	if output, err := aggregateRunGit(repository, "init", "--object-format=sha256", "-b", "main"); err != nil {
		t.Skipf("Git SHA-256 repositories unavailable: %v (%s)", err, output)
	}
	if _, err := aggregateRunGit(repository, "config", "user.name", "Test User"); err != nil {
		t.Fatal(err)
	}
	if _, err := aggregateRunGit(repository, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "one"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := aggregateRunGit(repository, "add", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := aggregateRunGit(repository, "commit", "-m", "one"); err != nil {
		t.Fatal(err)
	}
	old := aggregateInternalHead(t, repository)
	if len(old) != 64 {
		t.Fatalf("SHA-256 HEAD length = %d", len(old))
	}
	if err := os.WriteFile(filepath.Join(repository, "two"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := aggregateRunGit(repository, "add", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := aggregateRunGit(repository, "commit", "-m", "two"); err != nil {
		t.Fatal(err)
	}
	newCommit := aggregateInternalHead(t, repository)
	if _, err := aggregateRunGit(repository, "reset", "--hard", old); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter("git")
	receipt, err := adapter.FastForward(context.Background(), repository, "main", old, newCommit)
	if err != nil || aggregateInternalHead(t, repository) != newCommit {
		t.Fatalf("SHA-256 FastForward() = %#v, %v", receipt, err)
	}
	if err := adapter.RestoreFastForward(context.Background(), repository, receipt); err != nil || aggregateInternalHead(t, repository) != old {
		t.Fatalf("SHA-256 RestoreFastForward() = %v", err)
	}
}

var errInjectedMaterialization = errors.New("injected materialization failure")

func resetFastForwardTestSeams() {
	fastForwardBeforeRefUpdate = nil
	fastForwardAfterRefUpdate = nil
	fastForwardMaterialize = func(adapter *Adapter, ctx context.Context, repository, fromCommit, toCommit string) error {
		return adapter.materializeAttachedWorktree(ctx, repository, fromCommit, toCommit)
	}
}

func aggregateTransitionRepository(t *testing.T) (testutil.GitRepository, string, string, string) {
	t.Helper()
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("one", "one\n", "one")
	old := aggregateInternalHead(t, repository.Path)
	repository.CommitFile("two", "two\n", "two")
	newCommit := aggregateInternalHead(t, repository.Path)
	repository.Run(t, "branch", "candidate", newCommit)
	repository.Run(t, "checkout", "-b", "race", newCommit)
	repository.CommitFile("race", "race\n", "race")
	raceCommit := aggregateInternalHead(t, repository.Path)
	repository.Run(t, "checkout", "main")
	repository.Run(t, "reset", "--hard", old)
	return repository, old, newCommit, raceCommit
}

func aggregateConfiguredRefRepository(t *testing.T) (testutil.GitRepository, string) {
	t.Helper()
	repository := testutil.NewGitRepository(t)
	remote := testutil.NewBareGitRemote(t)
	repository.Run(t, "remote", "add", "origin", remote)
	repository.CommitFile("initial", "initial\n", "initial configured ref")
	repository.Run(t, "push", "-u", "origin", "main")
	return repository, remote
}

func aggregateIgnoredStateTransitionRepository(t *testing.T) (testutil.GitRepository, string, string) {
	t.Helper()
	repository := testutil.NewGitRepository(t)
	repository.CommitFile(".gitignore", ".wtree.yml\nnested/\n", "ignore project-local state")
	repository.CommitFile("one", "one\n", "one")
	old := aggregateInternalHead(t, repository.Path)
	repository.CommitFile("two", "two\n", "two")
	newCommit := aggregateInternalHead(t, repository.Path)
	repository.Run(t, "reset", "--hard", old)
	return repository, old, newCommit
}

func assertAggregateFile(t *testing.T, path, want string) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != want {
		t.Fatalf("file %q = %q, %v; want %q", path, actual, err, want)
	}
}

func aggregateInternalHead(t *testing.T, repository string) string {
	t.Helper()
	head, err := NewAdapter("git").Head(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func aggregateRunGit(repository string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = repository
	output, err := command.CombinedOutput()
	return string(output), err
}
