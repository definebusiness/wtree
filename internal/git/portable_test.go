package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestAdapterDiscoversUpstreamAndInitialCommits(t *testing.T) {
	repository, remote := pushedRepository(t, "published")
	adapter := git.NewAdapter("git")
	ctx := context.Background()

	upstream, err := adapter.Upstream(ctx, repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	if upstream.LocalBranch != "main" || upstream.Remote != "publish" || upstream.Merge != "refs/heads/published" || upstream.FetchURL != remote {
		t.Fatalf("Upstream() = %#v", upstream)
	}
	advertised, err := adapter.AdvertisedCommit(ctx, upstream.FetchURL, upstream.Merge)
	if err != nil || advertised != mustHead(t, repository) {
		t.Fatalf("AdvertisedCommit() = %q, %v; want HEAD", advertised, err)
	}
	if _, err := adapter.PublishedUpstream(ctx, repository.Path); err != nil {
		t.Fatalf("PublishedUpstream() error = %v", err)
	}
	roots, err := adapter.InitialCommits(ctx, repository.Path, "HEAD")
	if err != nil || len(roots) != 1 || roots[0] == "" {
		t.Fatalf("InitialCommits() = %q, %v", roots, err)
	}

	repository.Run(t, "checkout", "--detach")
	if _, err := adapter.Upstream(ctx, repository.Path); err == nil {
		t.Fatal("Upstream() on detached HEAD error = nil")
	}
}

func TestAdapterRejectsUnpublishedUpstreamStates(t *testing.T) {
	repository, remote := pushedRepository(t, "published")
	adapter := git.NewAdapter("git")
	repository.CommitFile("ahead.txt", "ahead\n", "ahead")
	if _, err := adapter.PublishedUpstream(context.Background(), repository.Path); err == nil {
		t.Fatal("PublishedUpstream(ahead) error = nil")
	}
	repository.Run(t, "reset", "--hard", "publish/published")
	repository.Run(t, "config", "branch.main.merge", "refs/heads/deleted")
	if _, err := adapter.PublishedUpstream(context.Background(), repository.Path); !errors.Is(err, git.ErrRemoteRefNotFound) {
		t.Fatalf("PublishedUpstream(deleted) error = %v, want missing ref", err)
	}
	repository.Run(t, "config", "branch.main.merge", "refs/tags/published")
	if _, err := adapter.PublishedUpstream(context.Background(), repository.Path); err == nil {
		t.Fatal("PublishedUpstream(malformed merge) error = nil")
	}
	repository.Run(t, "config", "branch.main.remote", "missing")
	if _, err := adapter.PublishedUpstream(context.Background(), repository.Path); err == nil {
		t.Fatal("PublishedUpstream(missing remote) error = nil")
	}
	repository.Run(t, "config", "--unset-all", "branch.main.remote")
	repository.Run(t, "config", "--add", "branch.main.remote", "publish")
	repository.Run(t, "config", "--add", "branch.main.remote", "other")
	if _, err := adapter.Upstream(context.Background(), repository.Path); err == nil {
		t.Fatal("Upstream(ambiguous remote) error = nil")
	}
	_ = remote
}

func TestAdapterRejectsBehindDivergedAndAbsentUpstreams(t *testing.T) {
	adapter := git.NewAdapter("git")

	behind, _ := pushedRepository(t, "published")
	behind.CommitFile("remote.txt", "remote\n", "remote")
	behind.Run(t, "push", "publish", "main:refs/heads/published")
	behind.Run(t, "reset", "--hard", "HEAD~1")
	if _, err := adapter.PublishedUpstream(context.Background(), behind.Path); err == nil {
		t.Fatal("PublishedUpstream(behind) error = nil")
	}

	diverged, _ := pushedRepository(t, "published")
	base := mustHead(t, diverged)
	diverged.CommitFile("local.txt", "local\n", "local")
	local := mustHead(t, diverged)
	diverged.Run(t, "reset", "--hard", base)
	diverged.CommitFile("remote.txt", "remote\n", "remote")
	diverged.Run(t, "push", "publish", "main:refs/heads/published")
	diverged.Run(t, "reset", "--hard", local)
	if _, err := adapter.PublishedUpstream(context.Background(), diverged.Path); err == nil {
		t.Fatal("PublishedUpstream(diverged) error = nil")
	}

	absent := testutil.NewGitRepository(t)
	absent.CommitFile("tracked", "value\n", "initial")
	if _, err := adapter.PublishedUpstream(context.Background(), absent.Path); err == nil {
		t.Fatal("PublishedUpstream(absent) error = nil")
	}
}

func TestAdapterAdvertisedCommitClassifiesAbsentAndCancellation(t *testing.T) {
	_, remote := pushedRepository(t, "main")
	adapter := git.NewAdapter("git")
	_, err := adapter.AdvertisedCommit(context.Background(), remote, "refs/heads/missing")
	if !errors.Is(err, git.ErrRemoteRefNotFound) {
		t.Fatalf("AdvertisedCommit(missing) error = %v, want ErrRemoteRefNotFound", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = adapter.AdvertisedCommit(ctx, remote, "refs/heads/main")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AdvertisedCommit(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestAdapterRemoteAdvertisementUsesOptionalLocksAndRedactsTransportFailure(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell wrapper fixture is POSIX-only")
	}
	observed := filepath.Join(t.TempDir(), "optional-locks")
	wrapper := filepath.Join(t.TempDir(), "failing-git")
	script := "#!/bin/sh\nprintf '%s' \"$GIT_OPTIONAL_LOCKS\" > " + observed + "\nprintf 'https://user:credential-canary@example.invalid/repo?credential-canary %09000d' >&2\nexit 2\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := git.NewAdapter(wrapper).AdvertisedCommit(context.Background(), "https://user:credential-canary@example.invalid/repo", "refs/heads/main")
	var gitError *git.Error
	if !errors.As(err, &gitError) || errors.Is(err, git.ErrRemoteRefNotFound) {
		t.Fatalf("AdvertisedCommit(transport) error = %T %v", err, err)
	}
	if strings.Contains(err.Error(), "credential-canary") || len(gitError.Stderr) > 8192 {
		t.Fatalf("transport error leaked or was unbounded: %v", err)
	}
	got, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "0" {
		t.Fatalf("GIT_OPTIONAL_LOCKS = %q, want 0", got)
	}
}

func TestAdapterClonesExactCommitWithNamedRemoteAndDifferentBranch(t *testing.T) {
	source, remote := pushedRepository(t, "published")
	planned := mustHead(t, source)
	adapter := git.NewAdapter("git")
	target := filepath.Join(t.TempDir(), "clone with spaces")
	if err := adapter.Clone(context.Background(), remote, target, "mirror"); err != nil {
		t.Fatal(err)
	}
	// Move the advertised branch after planning. The clone must continue to use
	// the immutable planned object rather than the newer remote tip.
	source.CommitFile("new-tip.txt", "new\n", "new remote tip")
	source.Run(t, "push", "publish", "main:refs/heads/published")
	if err := adapter.FetchCommit(context.Background(), target, "mirror", planned); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckoutTrackingBranch(context.Background(), target, "main", "mirror", "refs/heads/published", planned); err != nil {
		t.Fatal(err)
	}
	upstream, err := adapter.Upstream(context.Background(), target)
	if err != nil || upstream.LocalBranch != "main" || upstream.Remote != "mirror" || upstream.Merge != "refs/heads/published" || upstream.FetchURL != remote {
		t.Fatalf("clone Upstream() = %#v, %v", upstream, err)
	}
	if head := mustHead(t, testutil.GitRepository{Path: target}); head != planned {
		t.Fatalf("clone HEAD = %q, want %q", head, planned)
	}
	if clean, err := adapter.IsClean(context.Background(), target); err != nil || !clean {
		t.Fatalf("IsClean() = %t, %v", clean, err)
	}
}

func TestAdapterCloneCheckoutSuppressesHooksAndPreservesTracking(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("hook fixture is POSIX-only")
	}
	source, remote := pushedRepository(t, "published")
	planned := mustHead(t, source)
	adapter := git.NewAdapter("git")
	target := filepath.Join(t.TempDir(), "clone")
	if err := adapter.Clone(context.Background(), remote, target, "mirror"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "post-checkout-ran")
	hook := filepath.Join(target, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FetchCommit(context.Background(), target, "mirror", planned); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckoutTrackingBranch(context.Background(), target, "main", "mirror", "refs/heads/published", planned); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CheckoutTrackingBranch ran repository hook: %v", err)
	}
	upstream, err := adapter.Upstream(context.Background(), target)
	if err != nil || upstream.LocalBranch != "main" || upstream.Remote != "mirror" || upstream.Merge != "refs/heads/published" || mustHead(t, testutil.GitRepository{Path: target}) != planned {
		t.Fatalf("checkout tracking facts = %#v, %v", upstream, err)
	}
}

func TestAdapterFailsRatherThanReplacingPlannedCommitWhenBranchIsDeleted(t *testing.T) {
	source, remote := pushedRepository(t, "published")
	planned := mustHead(t, source)
	adapter := git.NewAdapter("git")
	target := filepath.Join(t.TempDir(), "clone")
	if err := adapter.Clone(context.Background(), remote, target, "mirror"); err != nil {
		t.Fatal(err)
	}
	// The immutable object is already obtainable, but the planned branch is no
	// longer advertised. Execution must fail, never select another branch tip.
	source.Run(t, "--git-dir", remote, "update-ref", "-d", "refs/heads/published")
	if err := adapter.FetchCommit(context.Background(), target, "mirror", planned); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckoutTrackingBranch(context.Background(), target, "main", "mirror", "refs/heads/published", planned); err == nil {
		t.Fatal("CheckoutTrackingBranch() after branch deletion error = nil")
	}
}

func TestAdapterRejectsMalformedRemoteOutputAndShellShapedDestination(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell wrapper fixture is POSIX-only")
	}
	wrapper := filepath.Join(t.TempDir(), "malformed-git")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf 'not-a-ref\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git.NewAdapter(wrapper).AdvertisedCommit(context.Background(), "/remote", "refs/heads/main"); err == nil {
		t.Fatal("AdvertisedCommit(malformed output) error = nil")
	}

	_, remote := pushedRepository(t, "main")
	marker := filepath.Join(t.TempDir(), "marker")
	destination := filepath.Join(t.TempDir(), "clone;touch "+marker)
	if err := git.NewAdapter("git").Clone(context.Background(), remote, destination, "named"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell-shaped destination executed a command: %v", err)
	}
}

func TestAdapterVerifiesInitialCommitsAndTrackedManifest(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("project.wtree.yml", "version: 1\n", "manifest")
	adapter := git.NewAdapter("git")
	head := mustHead(t, repository)
	contents, err := adapter.TrackedFile(context.Background(), repository.Path, head, "project.wtree.yml")
	if err != nil || string(contents) != "version: 1\n" {
		t.Fatalf("TrackedFile() = %q, %v", contents, err)
	}
	if _, err := adapter.TrackedFile(context.Background(), repository.Path, head, "missing.yml"); err == nil {
		t.Fatal("TrackedFile(missing) error = nil")
	}
	roots, err := adapter.InitialCommits(context.Background(), repository.Path, head)
	if err != nil || len(roots) != 1 {
		t.Fatalf("InitialCommits() = %q, %v", roots, err)
	}
	if contains, err := adapter.ContainsCommits(context.Background(), repository.Path, roots); err != nil || !contains {
		t.Fatalf("ContainsCommits(roots) = %t, %v", contains, err)
	}
	if contains, err := adapter.ContainsCommits(context.Background(), repository.Path, []string{"0123456789012345678901234567890123456789"}); err != nil || contains {
		t.Fatalf("ContainsCommits(wrong root) = %t, %v; want false, nil", contains, err)
	}

	repository.Run(t, "checkout", "--orphan", "other")
	repository.Run(t, "rm", "-rf", ".")
	repository.CommitFile("other.txt", "other\n", "other root")
	repository.Run(t, "checkout", "main")
	repository.Run(t, "merge", "--allow-unrelated-histories", "other", "-m", "merge roots")
	roots, err = adapter.InitialCommits(context.Background(), repository.Path, "HEAD")
	if err != nil || len(roots) != 2 || roots[0] >= roots[1] {
		t.Fatalf("InitialCommits(multiple) = %q, %v", roots, err)
	}
}

func TestAdapterRedactsRemoteCredentialsAndDoesNotRunHooks(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("hook fixture is POSIX-only")
	}
	failingGit := filepath.Join(t.TempDir(), "failing-git")
	if err := os.WriteFile(failingGit, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := git.NewAdapter(failingGit)
	secret := "credential-canary"
	_, err := adapter.AdvertisedCommit(context.Background(), "https://user:"+secret+"@example.invalid/repo.git?"+secret, "refs/heads/main")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential error = %v", err)
	}

	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "value\n", "initial")
	hookOutput := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(repository.Path, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+hookOutput+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The read-only fact command must not invoke a checkout hook.
	if _, err := git.NewAdapter("git").InitialCommits(context.Background(), repository.Path, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hookOutput); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only Git fact ran hook: %v", err)
	}
}

func TestAdapterCloneUsesSanitizedEnvironment(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell wrapper fixture is POSIX-only")
	}
	_, remote := pushedRepository(t, "main")
	observed := filepath.Join(t.TempDir(), "environment")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s|%s' \"$GIT_CONFIG_GLOBAL\" \"$GIT_CONFIG_NOSYSTEM\" \"$GIT_TERMINAL_PROMPT\" \"$LC_ALL\" \"$GIT_ASKPASS\" > " + observed + "\nexec git \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "GIT_CONFIG_GLOBAL=/hostile", "GIT_CONFIG_NOSYSTEM=0", "GIT_TERMINAL_PROMPT=1", "LC_ALL=hostile", "GIT_ASKPASS=/hostile")
	if err := git.NewAdapterWithEnv(wrapper, environment).Clone(context.Background(), remote, filepath.Join(t.TempDir(), "clone"), "custom"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	want := os.DevNull + "|1|0|C|"
	if string(got) != want {
		t.Fatalf("sanitized clone environment = %q, want %q", got, want)
	}
}

func TestAdapterCloneAndCheckoutIgnoreHostileTemplateAndConfiguration(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("hook fixture is POSIX-only")
	}
	source, remote := pushedRepository(t, "published")
	planned := mustHead(t, source)
	template := filepath.Join(t.TempDir(), "template")
	if err := os.MkdirAll(filepath.Join(template, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "template-hook-ran")
	if err := os.WriteFile(filepath.Join(template, "hooks", "post-checkout"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostileConfig := filepath.Join(t.TempDir(), "hostile.gitconfig")
	if err := os.WriteFile(hostileConfig, []byte("[credential]\n\thelper = !touch "+marker+"\n[core]\n\thooksPath = "+filepath.Join(template, "hooks")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "GIT_TEMPLATE_DIR="+template, "GIT_CONFIG_GLOBAL="+hostileConfig, "GIT_ASKPASS=/hostile")
	adapter := git.NewAdapterWithEnv("git", environment)
	target := filepath.Join(t.TempDir(), "clone")
	if err := adapter.Clone(context.Background(), remote, target, "mirror"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git", "hooks", "post-checkout")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Clone honored hostile template: %v", err)
	}
	if err := adapter.FetchCommit(context.Background(), target, "mirror", planned); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckoutTrackingBranch(context.Background(), target, "main", "mirror", "refs/heads/published", planned); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutating Git command honored hostile config/helper/hook: %v", err)
	}
}

func TestAdapterUpstreamReportsStaleConfiguredFetchURL(t *testing.T) {
	repository, _ := pushedRepository(t, "published")
	stale := filepath.Join(t.TempDir(), "missing-remote.git")
	repository.Run(t, "remote", "set-url", "publish", stale)
	adapter := git.NewAdapter("git")
	upstream, err := adapter.Upstream(context.Background(), repository.Path)
	if err != nil || upstream.FetchURL != stale {
		t.Fatalf("Upstream() stale URL = %#v, %v; want %q", upstream, err, stale)
	}
	if _, err := adapter.PublishedUpstream(context.Background(), repository.Path); err == nil || errors.Is(err, git.ErrRemoteRefNotFound) {
		t.Fatalf("PublishedUpstream(stale URL) error = %v, want transport failure", err)
	}
}

func TestAdapterCloneVerificationFactsDetectDirtyAndSubmodules(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked", "value\n", "initial")
	if err := os.WriteFile(filepath.Join(repository.Path, "dirty"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if clean, err := git.NewAdapter("git").IsClean(context.Background(), repository.Path); err != nil || clean {
		t.Fatalf("IsClean(dirty) = %t, %v", clean, err)
	}

	parent := testutil.NewGitRepository(t)
	parent.CommitFile("parent", "value\n", "initial")
	child := testutil.NewGitRepository(t)
	child.CommitFile("child", "value\n", "initial")
	parent.Run(t, "-c", "protocol.file.allow=always", "submodule", "add", child.Path, "modules/child")
	if has, err := git.NewAdapter("git").HasSubmodules(context.Background(), parent.Path); err != nil || !has {
		t.Fatalf("HasSubmodules() = %t, %v", has, err)
	}
}

func pushedRepository(t *testing.T, remoteBranch string) (testutil.GitRepository, string) {
	t.Helper()
	repository := testutil.NewGitRepository(t)
	repository.CommitFile("tracked.txt", "initial\n", "initial")
	remote := testutil.NewBareGitRemote(t)
	repository.Run(t, "remote", "add", "publish", remote)
	repository.Run(t, "push", "publish", "main:refs/heads/"+remoteBranch)
	repository.Run(t, "config", "branch.main.remote", "publish")
	repository.Run(t, "config", "branch.main.merge", "refs/heads/"+remoteBranch)
	return repository, remote
}

func mustHead(t *testing.T, repository testutil.GitRepository) string {
	t.Helper()
	adapter := git.NewAdapter("git")
	head, err := adapter.Head(context.Background(), repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func TestAdapterCommandContextCancelsLongRemote(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell wrapper fixture is POSIX-only")
	}
	wrapper := filepath.Join(t.TempDir(), "slow-git")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := git.NewAdapter(wrapper).AdvertisedCommit(ctx, "/unused", "refs/heads/main")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AdvertisedCommit() error = %v, want deadline", err)
	}
}
