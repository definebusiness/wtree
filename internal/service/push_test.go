package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestPushReadyAndReadOnlyBoundary(t *testing.T) {
	project, workspace := pushWorkspace(t)
	before := pushSnapshot(t, workspace.Checkouts[0].ResolvedPath)
	value, err := NewPushService().Push(context.Background(), project, workspace, PushRequest{})
	if err != nil || value.Status != PushStatusReady || len(value.Repositories) != 1 || value.Repositories[0].Status != PushStatusReady || value.Repositories[0].ObservedCommit != value.Repositories[0].Head {
		t.Fatalf("push = %#v, %v", value, err)
	}
	if after := pushSnapshot(t, workspace.Checkouts[0].ResolvedPath); before != after {
		t.Fatalf("push changed checkout state:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestPushReadOnlySnapshotAcrossResultWindows(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(domain.Project, domain.Workspace) (PushResult, error)
	}{
		{"ready", func(project domain.Project, workspace domain.Workspace) (PushResult, error) {
			return NewPushService().Push(context.Background(), project, workspace, PushRequest{})
		}},
		{"remote failure", func(project domain.Project, workspace domain.Workspace) (PushResult, error) {
			return NewPushServiceWith(&pushFailingAdvertiseGit{Git: gitadapter.NewAdapter("git"), failOn: 1}).Push(context.Background(), project, workspace, PushRequest{})
		}},
		{"writer", func(project domain.Project, workspace domain.Workspace) (PushResult, error) {
			return NewPushService().Push(context.Background(), project, workspace, PushRequest{OnComplete: func(PushRepositoryResult) error { return errors.New("writer stopped") }})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := pushWorkspace(t)
			before := pushWorkspaceSnapshot(t, workspace)
			_, _ = test.run(project, workspace)
			if after := pushWorkspaceSnapshot(t, workspace); before != after {
				t.Fatalf("push %s mutated checkout authority:\nbefore=%s\nafter=%s", test.name, before, after)
			}
		})
	}
}

func TestPushDoesNotRunConfiguredFSMonitorHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fsmonitor hook fixture is POSIX-only")
	}
	project, workspace := pushWorkspace(t)
	marker, hook := filepath.Join(t.TempDir(), "fsmonitor-ran"), filepath.Join(t.TempDir(), "fsmonitor-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\nprintf '2\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pushShellGit(t, workspace.RootPath, "config", "core.fsmonitor", hook)
	value, err := NewPushService().Push(context.Background(), project, workspace, PushRequest{})
	if err != nil || value.Status != PushStatusReady {
		t.Fatalf("push with fsmonitor = %#v, %v", value, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("push ran configured fsmonitor hook: %v", err)
	}
}

func TestPushFindingContractAndM09Mappings(t *testing.T) {
	allowed := []string{"ahead", "behind", "detached", "dirty", "diverged", "identity-mismatch", "metadata-commit-unavailable", "missing-repository", "missing-upstream", "partial-workspace", "unpublished-head"}
	actual := make([]string, 0, len(pushFindingMessages))
	for code := range pushFindingMessages {
		actual = append(actual, code)
	}
	sort.Strings(actual)
	if strings.Join(actual, ",") != strings.Join(allowed, ",") {
		t.Fatalf("push finding codes = %v, want %v", actual, allowed)
	}
	for _, test := range []struct {
		name, want string
		prepare    func(*domain.Project, *domain.Workspace)
	}{
		{"missing repository", "missing-repository", func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Partial, workspace.MissingRepositoryIDs, workspace.Checkouts = true, []string{"child"}, workspace.Checkouts[1:]
		}},
		{"identity mismatch", "identity-mismatch", func(project *domain.Project, _ *domain.Workspace) {
			project.Repositories[0].CommonGitDir = "/wrong/common"
		}},
		{"metadata commit unavailable", "metadata-commit-unavailable", func(_ *domain.Project, workspace *domain.Workspace) {
			workspace.Checkouts[0].Head = strings.Repeat("f", 40)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := pushNestedWorkspace(t)
			test.prepare(&project, &workspace)
			value, err := NewPushService().Push(context.Background(), project, workspace, PushRequest{})
			if err == nil || !pushHasFinding(value, test.want) {
				t.Fatalf("%s = %#v, %v", test.name, value, err)
			}
		})
	}
}

func TestPushMapsAheadBehindDivergedAndUnpublishedHead(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, domain.Workspace)
		want    []string
	}{
		{"ahead", func(t *testing.T, workspace domain.Workspace) {
			pushCommit(t, workspace.RootPath, "ahead.txt", "ahead\n", "ahead")
		}, []string{"ahead", "unpublished-head"}},
		{"behind", func(t *testing.T, workspace domain.Workspace) {
			pushAdvanceRemote(t, workspace.RootPath)
			pushShellGit(t, workspace.RootPath, "fetch", "origin", "main")
		}, []string{"behind", "unpublished-head"}},
		{"diverged", func(t *testing.T, workspace domain.Workspace) {
			pushCommit(t, workspace.RootPath, "local.txt", "local\n", "local")
			pushAdvanceRemote(t, workspace.RootPath)
			pushShellGit(t, workspace.RootPath, "fetch", "origin", "main")
		}, []string{"diverged", "unpublished-head"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := pushWorkspace(t)
			test.prepare(t, workspace)
			value, err := NewPushService().Push(context.Background(), project, workspace, PushRequest{})
			if err == nil || value.Status != PushStatusBlocked {
				t.Fatalf("%s = %#v, %v", test.name, value, err)
			}
			for _, code := range test.want {
				if !pushHasFinding(value, code) {
					t.Errorf("%s findings = %#v, want %q", test.name, value.Repositories[0].Findings, code)
				}
			}
			for _, finding := range value.Repositories[0].Findings {
				if _, ok := pushFindingMessages[finding.Code]; !ok {
					t.Fatalf("uncontracted finding %#v", finding)
				}
			}
		})
	}
}

func pushCommit(t *testing.T, path, name, contents, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	pushShellGit(t, path, "add", "--", name)
	pushShellGit(t, path, "commit", "-m", message)
}

func pushAdvanceRemote(t *testing.T, path string) {
	t.Helper()
	upstream, err := gitadapter.NewAdapter("git").Upstream(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "remote-writer")
	command := exec.Command("git", "clone", "--quiet", "--branch", "main", upstream.FetchURL, clone)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone remote: %v: %s", err, output)
	}
	pushShellGit(t, clone, "config", "user.name", "wtree test")
	pushShellGit(t, clone, "config", "user.email", "wtree@example.invalid")
	pushCommit(t, clone, "remote.txt", "remote\n", "remote")
	pushShellGit(t, clone, "push", "origin", "main")
}

func pushHasFinding(value PushResult, want string) bool {
	for _, repository := range value.Repositories {
		for _, finding := range repository.Findings {
			if finding.Code == want {
				return true
			}
		}
	}
	return false
}

func TestPushReadyForCompleteNestedWorkspaceInParentFirstOrder(t *testing.T) {
	project, workspace := pushNestedWorkspace(t)
	value, err := NewPushService().Push(context.Background(), project, workspace, PushRequest{})
	if err != nil || value.Status != PushStatusReady || len(value.Repositories) != 2 || value.Repositories[0].ID != "root" || value.Repositories[1].ID != "child" || value.Repositories[0].Status != PushStatusReady || value.Repositories[1].Status != PushStatusReady {
		t.Fatalf("nested push = %#v, %v", value, err)
	}
}

func TestPushCapturesOneManifestAuthorizedUpstreamBeforeRemoteObservation(t *testing.T) {
	project, workspace := pushNestedWorkspace(t)
	git := &pushAuthorityRecordingGit{Git: gitadapter.NewAdapter("git"), expectedUpstreams: map[string]gitadapter.Upstream{}}
	for _, checkout := range workspace.Checkouts {
		upstream, err := git.Git.Upstream(context.Background(), checkout.ResolvedPath)
		if err != nil {
			t.Fatal(err)
		}
		git.expectedUpstreams[checkout.ResolvedPath] = upstream
	}
	value, err := NewPushServiceWith(git).Push(context.Background(), project, workspace, PushRequest{})
	if err != nil || value.Status != PushStatusReady || len(git.upstreamCalls) != 2 || len(git.advertised) != 2 {
		t.Fatalf("authority capture = %#v, %v, upstream=%#v advertised=%#v", value, err, git.upstreamCalls, git.advertised)
	}
	for _, call := range git.advertised {
		if call.url == "https://undeclared.invalid/repository" || call.ref == "refs/heads/undeclared" {
			t.Fatalf("push contacted undeclared authority: %#v", call)
		}
	}
	// A second local capture would now return an undeclared authority. Push must
	// not take it: every configured upstream was captured exactly once before
	// the first ls-remote call.
	for path, count := range git.callsByPath {
		if count != 1 {
			t.Fatalf("Upstream(%q) calls = %d, want 1", path, count)
		}
	}
}

func TestPushDoesNotRedirectRemoteObservationAfterConcurrentConfigDrift(t *testing.T) {
	project, workspace := pushWorkspace(t)
	upstream, err := gitadapter.NewAdapter("git").Upstream(context.Background(), workspace.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	git := &pushAuthorityRecordingGit{Git: gitadapter.NewAdapter("git"), expectedUpstreams: map[string]gitadapter.Upstream{workspace.RootPath: upstream}}
	git.onAdvertise = func() {
		pushShellGit(t, workspace.RootPath, "config", "branch.main.remote", "undeclared")
		pushShellGit(t, workspace.RootPath, "config", "branch.main.merge", "refs/heads/undeclared")
	}
	value, err := NewPushServiceWith(git).Push(context.Background(), project, workspace, PushRequest{})
	if err != nil || value.Status != PushStatusReady || len(git.advertised) != 1 || git.advertised[0] != (pushAuthorityCall{url: upstream.FetchURL, ref: upstream.Merge}) {
		t.Fatalf("config drift push = %#v, %v, advertised=%#v", value, err, git.advertised)
	}
}

func TestPushReadinessFindingsAndRemoteFailure(t *testing.T) {
	for _, test := range []struct {
		name, want string
		mutate     func(*testing.T, domain.Workspace)
	}{
		{"dirty", "dirty", func(t *testing.T, workspace domain.Workspace) {
			if err := os.WriteFile(filepath.Join(workspace.RootPath, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"detached", "detached", func(t *testing.T, workspace domain.Workspace) {
			pushShellGit(t, workspace.RootPath, "checkout", "--detach")
		}},
		{"missing-upstream", "missing-upstream", func(t *testing.T, workspace domain.Workspace) {
			pushShellGit(t, workspace.RootPath, "config", "--unset", "branch.main.remote")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := pushWorkspace(t)
			test.mutate(t, workspace)
			value, err := NewPushService().Push(context.Background(), project, workspace, PushRequest{})
			if err == nil || value.Status != PushStatusBlocked || len(value.Repositories[0].Findings) == 0 || value.Repositories[0].Findings[0].Code != test.want {
				t.Fatalf("push %s = %#v, %v", test.name, value, err)
			}
		})
	}
	t.Run("remote failure is redacted and continues", func(t *testing.T) {
		project, workspace := pushWorkspace(t)
		git := &pushRemoteFailureGit{Git: gitadapter.NewAdapter("git")}
		value, err := NewPushServiceWith(git).Push(context.Background(), project, workspace, PushRequest{})
		if err == nil || value.Status != PushStatusFailed || value.Repositories[0].Failure == nil || value.Repositories[0].Failure.Message != "Git observation failed" || strings.Contains(value.Repositories[0].Failure.Message, "secret") {
			t.Fatalf("push failed remote = %#v, %v", value, err)
		}
	})
}

func TestPushCancellationAndWriterStopRemoteSuffix(t *testing.T) {
	project, workspace := pushWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	git := &pushCancelGit{Git: gitadapter.NewAdapter("git"), cancel: cancel}
	value, err := NewPushServiceWith(git).Push(ctx, project, workspace, PushRequest{})
	if !errors.Is(err, context.Canceled) || value.Status != PushStatusFailed || value.Repositories[0].Status != PushStatusCanceled {
		t.Fatalf("canceled push = %#v, %v", value, err)
	}

	project, workspace = pushWorkspace(t)
	value, err = NewPushService().Push(context.Background(), project, workspace, PushRequest{OnComplete: func(PushRepositoryResult) error { return errors.New("writer stopped") }})
	if err == nil || value.Status != PushStatusFailed || value.Failure == nil || value.Failure.Message != "push readiness output failed" {
		t.Fatalf("writer push = %#v, %v", value, err)
	}
}

func TestPushCancellationAndWriterStopParentFirstRemoteSuffix(t *testing.T) {
	t.Run("cancellation after first remote observation", func(t *testing.T) {
		project, workspace := pushNestedWorkspace(t)
		ctx, cancel := context.WithCancel(context.Background())
		git := &pushCancelAfterAdvertiseGit{Git: gitadapter.NewAdapter("git"), cancel: cancel}
		value, err := NewPushServiceWith(git).Push(ctx, project, workspace, PushRequest{})
		if !errors.Is(err, context.Canceled) || git.calls != 1 || len(value.Repositories) != 2 || value.Repositories[0].Status != PushStatusCanceled || value.Repositories[1].Status != PushStatusCanceled {
			t.Fatalf("remote cancellation = %#v, %v, calls=%d", value, err, git.calls)
		}
	})
	t.Run("writer after parent", func(t *testing.T) {
		project, workspace := pushNestedWorkspace(t)
		git := &pushAdvertiseRecordingGit{Git: gitadapter.NewAdapter("git")}
		value, err := NewPushServiceWith(git).Push(context.Background(), project, workspace, PushRequest{OnComplete: func(PushRepositoryResult) error { return errors.New("writer stopped") }})
		if err == nil || git.calls != 1 || len(value.Repositories) != 2 || value.Repositories[1].Status != PushStatusCanceled || value.Failure == nil || value.Failure.Message != "push readiness output failed" {
			t.Fatalf("writer suffix = %#v, %v, calls=%d", value, err, git.calls)
		}
	})
}

func TestPushCancellationAndWriterCallbacksSettleEveryRowExactlyOnce(t *testing.T) {
	for _, test := range []struct {
		name             string
		cancelOn, failOn int
	}{
		{"cancel first remote", 1, 0},
		{"cancel last remote", 2, 0},
		{"writer first row", 0, 1},
		{"writer last row", 0, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, workspace := pushNestedWorkspace(t)
			var callbacks []PushRepositoryResult
			if test.cancelOn != 0 {
				ctx, cancel := context.WithCancel(context.Background())
				git := &pushCancelAfterAdvertiseGit{Git: gitadapter.NewAdapter("git"), cancel: cancel, cancelOn: test.cancelOn}
				value, err := NewPushServiceWith(git).Push(ctx, project, workspace, PushRequest{OnComplete: func(entry PushRepositoryResult) error { callbacks = append(callbacks, entry); return nil }})
				if !errors.Is(err, context.Canceled) || git.calls != test.cancelOn || pushCallbackIDs(callbacks) != "root,child" || value.Repositories[1].Status != PushStatusCanceled {
					t.Fatalf("cancel = %#v, %v, calls=%d callbacks=%#v", value, err, git.calls, callbacks)
				}
				return
			}
			git := &pushAdvertiseRecordingGit{Git: gitadapter.NewAdapter("git")}
			value, err := NewPushServiceWith(git).Push(context.Background(), project, workspace, PushRequest{OnComplete: func(entry PushRepositoryResult) error {
				callbacks = append(callbacks, entry)
				if len(callbacks) == test.failOn {
					return errors.New("writer stopped")
				}
				return nil
			}})
			if err == nil || git.calls != test.failOn || len(callbacks) != test.failOn || value.Failure == nil || value.Failure.Message != "push readiness output failed" {
				t.Fatalf("writer = %#v, %v, calls=%d callbacks=%#v", value, err, git.calls, callbacks)
			}
		})
	}
}

func TestPushContinuesAfterOrdinaryRemoteFailureWithoutLeakingDiagnostics(t *testing.T) {
	project, workspace := pushNestedWorkspace(t)
	git := &pushFailingAdvertiseGit{Git: gitadapter.NewAdapter("git"), failOn: 1}
	var callbacks []PushRepositoryResult
	value, err := NewPushServiceWith(git).Push(context.Background(), project, workspace, PushRequest{OnComplete: func(entry PushRepositoryResult) error { callbacks = append(callbacks, entry); return nil }})
	if err == nil || value.Status != PushStatusFailed || git.calls != 2 || pushCallbackIDs(callbacks) != "root,child" || callbacks[0].Status != PushStatusFailed || callbacks[1].Status != PushStatusReady || callbacks[0].Failure == nil || callbacks[0].Failure.Message != "Git observation failed" {
		t.Fatalf("continued failure = %#v, %v, calls=%d callbacks=%#v", value, err, git.calls, callbacks)
	}
	for _, value := range []string{err.Error(), callbacks[0].Failure.Message} {
		if strings.Contains(value, "super-secret") || strings.Contains(value, "https://") {
			t.Fatalf("remote diagnostic leaked: %q", value)
		}
	}
}

func pushCallbackIDs(entries []PushRepositoryResult) string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return strings.Join(ids, ",")
}

type pushRemoteFailureGit struct{ gitadapter.Git }

func (g *pushRemoteFailureGit) AdvertisedCommit(context.Context, string, string) (string, error) {
	return "", errors.New("transport https://user:super-secret@example.invalid/repo failed")
}

type pushCancelGit struct {
	gitadapter.Git
	cancel func()
}

type pushCancelAfterAdvertiseGit struct {
	gitadapter.Git
	cancel   func()
	calls    int
	cancelOn int
}

func (g *pushCancelAfterAdvertiseGit) AdvertisedCommit(ctx context.Context, url, ref string) (string, error) {
	g.calls++
	commit, err := g.Git.AdvertisedCommit(ctx, url, ref)
	if g.cancelOn == 0 || g.calls == g.cancelOn {
		g.cancel()
	}
	return commit, err
}

type pushAdvertiseRecordingGit struct {
	gitadapter.Git
	calls int
}

func (g *pushAdvertiseRecordingGit) AdvertisedCommit(ctx context.Context, url, ref string) (string, error) {
	g.calls++
	return g.Git.AdvertisedCommit(ctx, url, ref)
}

type pushFailingAdvertiseGit struct {
	gitadapter.Git
	calls, failOn int
}

func (g *pushFailingAdvertiseGit) AdvertisedCommit(ctx context.Context, url, ref string) (string, error) {
	g.calls++
	if g.calls == g.failOn {
		return "", errors.New("transport https://user:super-secret@example.invalid/repository failed")
	}
	return g.Git.AdvertisedCommit(ctx, url, ref)
}

type pushAuthorityCall struct{ url, ref string }

type pushAuthorityRecordingGit struct {
	gitadapter.Git
	expectedUpstreams map[string]gitadapter.Upstream
	upstreamCalls     []string
	callsByPath       map[string]int
	advertised        []pushAuthorityCall
	onAdvertise       func()
}

func (g *pushAuthorityRecordingGit) Upstream(ctx context.Context, path string) (gitadapter.Upstream, error) {
	if g.callsByPath == nil {
		g.callsByPath = map[string]int{}
	}
	g.callsByPath[path]++
	g.upstreamCalls = append(g.upstreamCalls, path)
	if g.callsByPath[path] > 1 {
		return gitadapter.Upstream{LocalBranch: "other", Remote: "other", Merge: "refs/heads/undeclared", FetchURL: "https://undeclared.invalid/repository"}, nil
	}
	return g.Git.Upstream(ctx, path)
}

func (g *pushAuthorityRecordingGit) AdvertisedCommit(ctx context.Context, url, ref string) (string, error) {
	g.advertised = append(g.advertised, pushAuthorityCall{url: url, ref: ref})
	if len(g.upstreamCalls) != len(g.expectedUpstreams) {
		return "", errors.New("remote observation began before local plan completed")
	}
	if g.onAdvertise != nil {
		callback := g.onAdvertise
		g.onAdvertise = nil
		callback()
	}
	return g.Git.AdvertisedCommit(ctx, url, ref)
}

func (g *pushCancelGit) Head(ctx context.Context, path string) (string, error) {
	g.cancel()
	return g.Git.Head(ctx, path)
}

func pushWorkspace(t *testing.T) (domain.Project, domain.Workspace) {
	t.Helper()
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("root.txt", "root\n", "root")
	repository.CommitFile(".gitignore", ".wtree.yml\n", "ignore local configuration")
	git := gitadapter.NewAdapter("git")
	ctx := context.Background()
	common, err := git.CommonGitDir(ctx, repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	head, err := git.Head(ctx, repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := git.Upstream(ctx, repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := git.InitialCommits(ctx, repository.Path, head)
	if err != nil {
		t.Fatal(err)
	}
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "push-project", Name: "push project", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{
		"root": {Clone: config.CloneSource{Remote: upstream.Remote, URL: upstream.FetchURL}, Upstream: config.Upstream{Branch: "main", Remote: upstream.Remote, Merge: upstream.Merge}, Identity: config.RepositoryIdentity{InitialCommits: roots}, Mount: ".", DefaultBranch: "main"},
	}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.Path, "project.wtree.yml"), manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "add", "project.wtree.yml")
	repository.Run(t, "commit", "-m", "add manifest")
	repository.Run(t, "push", "origin", "main")
	configPath := filepath.Join(repository.Path, ".wtree.yml")
	local := config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: "push-project", Name: "push project", BaseRepository: "root"}, LogicalRoot: ".", Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: ".", DefaultBranch: "main"}}, Worktrees: config.Worktrees{Root: filepath.Join(t.TempDir(), "worktrees")}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: filepath.Join(repository.Path, "project.wtree.yml")}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	head, err = git.Head(ctx, repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Project{Version: domain.CurrentVersion, ID: "push-project", Name: "push project", ConfigPath: configPath, LogicalRoot: repository.Path, BaseRepository: "root", Repositories: []domain.Repository{{ID: "root", CommonGitDir: common, SourcePath: repository.Path, DefaultMount: ".", DefaultBranch: "main"}}}, domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: repository.Path, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: head, Mount: ".", ResolvedPath: repository.Path}}}
}

func pushNestedWorkspace(t *testing.T) (domain.Project, domain.Workspace) {
	t.Helper()
	root, child := testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	root.CommitFile(".gitignore", ".wtree.yml\n/backend/\n", "ignore local configuration and child")
	child.CommitFile("child.txt", "child\n", "child")
	git, ctx := gitadapter.NewAdapter("git"), context.Background()
	rootCommon, err := git.CommonGitDir(ctx, root.Path)
	if err != nil {
		t.Fatal(err)
	}
	childCommon, err := git.CommonGitDir(ctx, child.Path)
	if err != nil {
		t.Fatal(err)
	}
	rootUpstream, err := git.Upstream(ctx, root.Path)
	if err != nil {
		t.Fatal(err)
	}
	childUpstream, err := git.Upstream(ctx, child.Path)
	if err != nil {
		t.Fatal(err)
	}
	rootHead, err := git.Head(ctx, root.Path)
	if err != nil {
		t.Fatal(err)
	}
	rootRoots, err := git.InitialCommits(ctx, root.Path, rootHead)
	if err != nil {
		t.Fatal(err)
	}
	childHead, err := git.Head(ctx, child.Path)
	if err != nil {
		t.Fatal(err)
	}
	childRoots, err := git.InitialCommits(ctx, child.Path, childHead)
	if err != nil {
		t.Fatal(err)
	}
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "nested-push", Name: "nested push", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{
		"root":  {Clone: config.CloneSource{Remote: rootUpstream.Remote, URL: rootUpstream.FetchURL}, Upstream: config.Upstream{Branch: "main", Remote: rootUpstream.Remote, Merge: rootUpstream.Merge}, Identity: config.RepositoryIdentity{InitialCommits: rootRoots}, Mount: ".", DefaultBranch: "main"},
		"child": {Clone: config.CloneSource{Remote: childUpstream.Remote, URL: childUpstream.FetchURL}, Upstream: config.Upstream{Branch: "main", Remote: childUpstream.Remote, Merge: childUpstream.Merge}, Identity: config.RepositoryIdentity{InitialCommits: childRoots}, Parent: "root", Mount: "backend", DefaultBranch: "main"},
	}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.Path, "project.wtree.yml"), manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	root.Run(t, "add", "project.wtree.yml")
	root.Run(t, "commit", "-m", "add manifest")
	root.Run(t, "push", "origin", "main")
	childPath := filepath.Join(root.Path, "backend")
	child.Run(t, "worktree", "add", "-b", "nested-push", childPath, "HEAD")
	pushShellGit(t, childPath, "config", "branch.nested-push.remote", "origin")
	pushShellGit(t, childPath, "config", "branch.nested-push.merge", "refs/heads/main")
	childHead, err = git.Head(ctx, childPath)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root.Path, ".wtree.yml")
	local := config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: "nested-push", Name: "nested push", BaseRepository: "root"}, LogicalRoot: ".", Repositories: map[string]config.Repository{"root": {Source: ".", DefaultMount: ".", DefaultBranch: "main"}, "child": {Source: "backend", Parent: "root", DefaultMount: "backend", DefaultBranch: "main"}}, Worktrees: config.Worktrees{Root: filepath.Join(t.TempDir(), "worktrees")}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: filepath.Join(root.Path, "project.wtree.yml")}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	rootHead, err = git.Head(ctx, root.Path)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Project{Version: domain.CurrentVersion, ID: "nested-push", Name: "nested push", ConfigPath: configPath, LogicalRoot: root.Path, BaseRepository: "root", Repositories: []domain.Repository{{ID: "child", ParentID: "root", CommonGitDir: childCommon, SourcePath: child.Path, DefaultMount: "backend", DefaultBranch: "main"}, {ID: "root", CommonGitDir: rootCommon, SourcePath: root.Path, DefaultMount: ".", DefaultBranch: "main"}}}, domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: root.Path, Checkouts: []domain.Checkout{{RepositoryID: "child", Branch: "nested-push", Head: childHead, Mount: "backend", ResolvedPath: childPath}, {RepositoryID: "root", Branch: "main", Head: rootHead, Mount: ".", ResolvedPath: root.Path}}}
}

func pushSnapshot(t *testing.T, path string) string {
	t.Helper()
	fetchHeadPath := strings.TrimSpace(pushGitOutput(t, path, "rev-parse", "--path-format=absolute", "--git-path", "FETCH_HEAD"))
	fetchHead, err := os.ReadFile(fetchHeadPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Join([]string{pushTreeSnapshot(t, path), pushGitOutput(t, path, "rev-parse", "HEAD"), pushGitOutput(t, path, "for-each-ref", "--format=%(refname):%(objectname)"), pushGitOutput(t, path, "write-tree"), pushGitOutput(t, path, "config", "--list", "--local"), string(fetchHead)}, "\x00")
}

func pushWorkspaceSnapshot(t *testing.T, workspace domain.Workspace) string {
	t.Helper()
	parts := make([]string, 0, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		parts = append(parts, checkout.RepositoryID+"="+pushSnapshot(t, checkout.ResolvedPath))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

func pushTreeSnapshot(t *testing.T, root string) string {
	t.Helper()
	entries := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if relative == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		value := filepath.ToSlash(relative) + ":" + fmt.Sprintf("%#o", info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":link=" + target
		} else if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":bytes=" + string(data)
		}
		entries = append(entries, value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}
func pushGitOutput(t *testing.T, path string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(output)
}
func pushShellGit(t *testing.T, path string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
