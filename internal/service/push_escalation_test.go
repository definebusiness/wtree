package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

// pushEscalationFixture is deliberately a real three-repository forest. The
// repositories are distinct Git stores and the checkout paths are nested, so
// first, middle, and last observations cannot accidentally address one store.
type pushEscalationFixture struct {
	project       domain.Project
	workspace     domain.Workspace
	dataRoot      string
	remotes       map[string]string
	markers       map[string]string
	checkoutPaths map[string]string
}

func TestPushReadyPreservesCompleteAuthoritySnapshot(t *testing.T) {
	fixture := newPushEscalationFixture(t)
	pushEscalationClearMarkers(t, fixture)
	before := pushEscalationAuthoritySnapshot(t, fixture)
	var callbacks []PushRepositoryResult
	value, err := NewPushService().Push(context.Background(), fixture.project, fixture.workspace, PushRequest{OnComplete: func(entry PushRepositoryResult) error {
		callbacks = append(callbacks, entry)
		return nil
	}})
	if err != nil || value.Status != PushStatusReady || pushCallbackIDs(callbacks) != "root,middle,leaf" {
		t.Fatalf("ready forest = %#v, %v callbacks=%#v", value, err, callbacks)
	}
	for _, entry := range value.Repositories {
		if entry.Status != PushStatusReady || entry.ObservedCommit != entry.Head || len(entry.Findings) != 0 || entry.Failure != nil {
			t.Fatalf("ready row = %#v", entry)
		}
	}
	pushEscalationAssertAuthority(t, fixture, before, "ready")
}

func TestPushEveryFindingPreservesCompleteAuthoritySnapshot(t *testing.T) {
	tests := []struct {
		name string
		want string
		edit func(*testing.T, *pushEscalationFixture) gitadapter.Git
	}{
		{"dirty", "dirty", func(t *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			mustPushWrite(t, filepath.Join(pushEscalationCheckout(fixture.workspace, "middle").ResolvedPath, "dirty.txt"), "dirty\n")
			return gitadapter.NewAdapter("git")
		}},
		{"ahead", "ahead", func(t *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			pushCommit(t, pushEscalationCheckout(fixture.workspace, "middle").ResolvedPath, "ahead.txt", "ahead\n", "ahead")
			return gitadapter.NewAdapter("git")
		}},
		{"behind", "behind", func(t *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			path := pushEscalationCheckout(fixture.workspace, "middle").ResolvedPath
			pushAdvanceRemote(t, path)
			pushShellGit(t, path, "fetch", "origin", "main")
			return gitadapter.NewAdapter("git")
		}},
		{"diverged", "diverged", func(t *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			path := pushEscalationCheckout(fixture.workspace, "middle").ResolvedPath
			pushCommit(t, path, "local.txt", "local\n", "local")
			pushAdvanceRemote(t, path)
			pushShellGit(t, path, "fetch", "origin", "main")
			return gitadapter.NewAdapter("git")
		}},
		{"detached", "detached", func(t *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			pushShellGit(t, pushEscalationCheckout(fixture.workspace, "middle").ResolvedPath, "checkout", "--detach")
			return gitadapter.NewAdapter("git")
		}},
		{"missing upstream", "missing-upstream", func(t *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			pushShellGit(t, pushEscalationCheckout(fixture.workspace, "middle").ResolvedPath, "config", "--unset", "branch.push-middle.remote")
			return gitadapter.NewAdapter("git")
		}},
		{"missing repository", "missing-repository", func(t *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			checkout := pushEscalationCheckout(fixture.workspace, "leaf")
			if err := os.Rename(checkout.ResolvedPath, checkout.ResolvedPath+"-absent"); err != nil {
				t.Fatal(err)
			}
			return gitadapter.NewAdapter("git")
		}},
		{"partial workspace direct", "partial-workspace", func(_ *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			fixture.workspace.Partial = true
			fixture.workspace.MissingRepositoryIDs = []string{"leaf"}
			fixture.workspace.Checkouts = []domain.Checkout{
				pushEscalationCheckout(fixture.workspace, "root"),
				pushEscalationCheckout(fixture.workspace, "middle"),
			}
			return gitadapter.NewAdapter("git")
		}},
		{"unpublished head", "unpublished-head", func(_ *testing.T, _ *pushEscalationFixture) gitadapter.Git {
			return &pushEscalationRemoteGit{Git: gitadapter.NewAdapter("git"), replaceOn: 2}
		}},
		{"identity mismatch", "identity-mismatch", func(_ *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			for index := range fixture.project.Repositories {
				if fixture.project.Repositories[index].ID == "middle" {
					fixture.project.Repositories[index].CommonGitDir = filepath.Join(fixture.dataRoot, "wrong-common-git-dir")
				}
			}
			return gitadapter.NewAdapter("git")
		}},
		{"metadata commit unavailable", "metadata-commit-unavailable", func(_ *testing.T, fixture *pushEscalationFixture) gitadapter.Git {
			for index := range fixture.workspace.Checkouts {
				if fixture.workspace.Checkouts[index].RepositoryID == "middle" {
					fixture.workspace.Checkouts[index].Head = strings.Repeat("f", 40)
				}
			}
			return gitadapter.NewAdapter("git")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPushEscalationFixture(t)
			git := test.edit(t, &fixture)
			pushEscalationClearMarkers(t, fixture)
			before := pushEscalationAuthoritySnapshot(t, fixture)
			value, err := NewPushServiceWith(git).Push(context.Background(), fixture.project, fixture.workspace, PushRequest{})
			if err == nil || value.Status != PushStatusBlocked || !pushHasFinding(value, test.want) {
				t.Fatalf("Push(%s) = %#v, %v; want blocked %q", test.name, value, err, test.want)
			}
			if test.want == "partial-workspace" && (!pushEscalationRepositoryHasFinding(value, "middle", "partial-workspace") || !pushEscalationRepositoryHasFinding(value, "leaf", "missing-repository")) {
				t.Fatalf("partial workspace was not asserted directly alongside missing repository: %#v", value)
			}
			pushEscalationAssertAuthority(t, fixture, before, test.name)
		})
	}
}

func TestPushOperationalFailureContinuesRealThreeRepositoryForestAndPreservesAuthority(t *testing.T) {
	fixture := newPushEscalationFixture(t)
	pushEscalationClearMarkers(t, fixture)
	before := pushEscalationAuthoritySnapshot(t, fixture)
	git := &pushEscalationRemoteGit{Git: gitadapter.NewAdapter("git"), failOn: 2, failure: errors.New("transport https://user:super-secret@example.invalid/private failed")}
	var callbacks []PushRepositoryResult
	value, err := NewPushServiceWith(git).Push(context.Background(), fixture.project, fixture.workspace, PushRequest{OnComplete: func(entry PushRepositoryResult) error {
		callbacks = append(callbacks, entry)
		return nil
	}})
	if err == nil || value.Status != PushStatusFailed || git.calls != 3 || pushCallbackIDs(callbacks) != "root,middle,leaf" || callbacks[0].Status != PushStatusReady || callbacks[1].Status != PushStatusFailed || callbacks[2].Status != PushStatusReady {
		t.Fatalf("continued operational failure = %#v, %v; calls=%d callbacks=%#v", value, err, git.calls, callbacks)
	}
	for _, public := range []string{err.Error(), callbacks[1].Failure.Message, fmt.Sprintf("%#v", value)} {
		if strings.Contains(public, "super-secret") || strings.Contains(public, "https://") {
			t.Fatalf("public failure leaked remote authority: %q", public)
		}
	}
	pushEscalationAssertAuthority(t, fixture, before, "operational failure")
}

func TestPushCancellationTableCoversEveryObservationAndPosition(t *testing.T) {
	fixture := newPushEscalationFixture(t)
	pushEscalationClearMarkers(t, fixture)
	before := pushEscalationAuthoritySnapshot(t, fixture)
	causes := []error{
		context.Canceled,
		fmt.Errorf("wrapped cancellation: %w", context.Canceled),
		context.DeadlineExceeded,
		fmt.Errorf("wrapped deadline: %w", context.DeadlineExceeded),
	}
	local := []struct {
		seam       string
		occurrence int
	}{
		{"manifest-common-git-dir", 1},
		{"manifest-head", 1},
		{"tracked-file", 1},
		{"common-git-dir", 3},
		{"top-level", 2},
		{"current-branch", 2},
		{"head", 3},
		{"status", 2},
		{"contains-commits", 2},
		{"ahead-behind", 2},
		{"upstream", 2},
	}
	for index, row := range local {
		t.Run("local "+row.seam, func(t *testing.T) {
			git := &pushEscalationCancelSeamGit{Git: gitadapter.NewAdapter("git"), seam: row.seam, occurrence: row.occurrence, cause: causes[index%len(causes)]}
			callbacks, value, err := pushEscalationRunWithCallbacks(context.Background(), fixture, git, 0, nil)
			pushEscalationAssertCanceled(t, row.seam, value, err, causes[index%len(causes)], callbacks, 0, git.remoteCalls)
			pushEscalationAssertAuthority(t, fixture, before, row.seam)
		})
	}
	for position := 1; position <= 3; position++ {
		t.Run(fmt.Sprintf("remote position %d", position), func(t *testing.T) {
			cause := causes[(position+1)%len(causes)]
			git := &pushEscalationRemoteGit{Git: gitadapter.NewAdapter("git"), failOn: position, failure: cause}
			callbacks, value, err := pushEscalationRunWithCallbacks(context.Background(), fixture, git, 0, nil)
			pushEscalationAssertCanceled(t, "remote", value, err, cause, callbacks, position, git.calls)
			pushEscalationAssertAuthority(t, fixture, before, "remote cancellation")
		})
	}
	for position := 1; position <= 3; position++ {
		t.Run(fmt.Sprintf("callback position %d", position), func(t *testing.T) {
			cause := causes[(position+2)%len(causes)]
			git := &pushEscalationRemoteGit{Git: gitadapter.NewAdapter("git")}
			callbacks, value, err := pushEscalationRunWithCallbacks(context.Background(), fixture, git, position, cause)
			pushEscalationAssertCanceled(t, "callback", value, err, cause, callbacks, position, git.calls)
			pushEscalationAssertAuthority(t, fixture, before, "callback cancellation")
		})
	}
}

func TestPushWriterFirstMiddleLastPreservesIdentitySuffixAndAuthority(t *testing.T) {
	fixture := newPushEscalationFixture(t)
	pushEscalationClearMarkers(t, fixture)
	before := pushEscalationAuthoritySnapshot(t, fixture)
	for position := 1; position <= 3; position++ {
		t.Run(fmt.Sprintf("position %d", position), func(t *testing.T) {
			sentinel := fmt.Errorf("writer-%d", position)
			git := &pushEscalationRemoteGit{Git: gitadapter.NewAdapter("git")}
			callbacks, value, err := pushEscalationRunWithCallbacks(context.Background(), fixture, git, position, sentinel)
			if !errors.Is(err, sentinel) || git.calls != position || len(callbacks) != position || pushCallbackIDs(callbacks) != strings.Join([]string{"root", "middle", "leaf"}[:position], ",") || value.Failure == nil || value.Failure.Message != "push readiness output failed" {
				t.Fatalf("writer %d = %#v, %v; calls=%d callbacks=%#v", position, value, err, git.calls, callbacks)
			}
			for index := position; index < len(value.Repositories); index++ {
				if value.Repositories[index].Status != PushStatusCanceled {
					t.Fatalf("writer %d suffix[%d] = %#v", position, index, value.Repositories[index])
				}
			}
			pushEscalationAssertAuthority(t, fixture, before, "writer")
		})
	}
}

func pushEscalationRunWithCallbacks(ctx context.Context, fixture pushEscalationFixture, git gitadapter.Git, failOn int, callbackError error) ([]PushRepositoryResult, PushResult, error) {
	var callbacks []PushRepositoryResult
	value, err := NewPushServiceWith(git).Push(ctx, fixture.project, fixture.workspace, PushRequest{OnComplete: func(entry PushRepositoryResult) error {
		callbacks = append(callbacks, entry)
		if failOn != 0 && len(callbacks) == failOn {
			return callbackError
		}
		return nil
	}})
	return callbacks, value, err
}

func pushEscalationAssertCanceled(t *testing.T, label string, value PushResult, err, cause error, callbacks []PushRepositoryResult, wantRemote, gotRemote int) {
	t.Helper()
	if !errors.Is(err, cause) || value.Status != PushStatusFailed || value.Failure == nil || value.Failure.Message != "push readiness canceled" || gotRemote != wantRemote || pushCallbackIDs(callbacks) != "root,middle,leaf" {
		t.Fatalf("%s cancellation = %#v, %v; calls=%d callbacks=%#v", label, value, err, gotRemote, callbacks)
	}
	for index, entry := range value.Repositories {
		if entry.Status != PushStatusCanceled || entry.Failure == nil || entry.Failure.Message != "push readiness canceled" {
			t.Fatalf("%s left row %d planned/ready: %#v", label, index, entry)
		}
	}
}

type pushEscalationRemoteGit struct {
	gitadapter.Git
	calls, failOn, replaceOn int
	failure                  error
}

func (g *pushEscalationRemoteGit) AdvertisedCommit(ctx context.Context, url, ref string) (string, error) {
	g.calls++
	if g.calls == g.failOn {
		return "", g.failure
	}
	if g.calls == g.replaceOn {
		return strings.Repeat("f", 40), nil
	}
	return g.Git.AdvertisedCommit(ctx, url, ref)
}

type pushEscalationCancelSeamGit struct {
	gitadapter.Git
	seam        string
	occurrence  int
	cause       error
	seen        map[string]int
	remoteCalls int
}

func (g *pushEscalationCancelSeamGit) hit(seam string) error {
	if g.seen == nil {
		g.seen = map[string]int{}
	}
	g.seen[seam]++
	if g.seam == seam && g.seen[seam] == g.occurrence {
		return g.cause
	}
	return nil
}

func (g *pushEscalationCancelSeamGit) CommonGitDir(ctx context.Context, path string) (string, error) {
	seam := "common-git-dir"
	if g.seam == "manifest-common-git-dir" {
		seam = "manifest-common-git-dir"
	}
	if err := g.hit(seam); err != nil {
		return "", err
	}
	return g.Git.CommonGitDir(ctx, path)
}
func (g *pushEscalationCancelSeamGit) Head(ctx context.Context, path string) (string, error) {
	seam := "head"
	if g.seam == "manifest-head" {
		seam = "manifest-head"
	}
	if err := g.hit(seam); err != nil {
		return "", err
	}
	return g.Git.Head(ctx, path)
}
func (g *pushEscalationCancelSeamGit) TrackedFile(ctx context.Context, path, commit, name string) ([]byte, error) {
	if err := g.hit("tracked-file"); err != nil {
		return nil, err
	}
	return g.Git.TrackedFile(ctx, path, commit, name)
}
func (g *pushEscalationCancelSeamGit) TopLevel(ctx context.Context, path string) (string, error) {
	if err := g.hit("top-level"); err != nil {
		return "", err
	}
	return g.Git.TopLevel(ctx, path)
}
func (g *pushEscalationCancelSeamGit) CurrentBranch(ctx context.Context, path string) (string, bool, error) {
	if err := g.hit("current-branch"); err != nil {
		return "", false, err
	}
	return g.Git.CurrentBranch(ctx, path)
}
func (g *pushEscalationCancelSeamGit) Status(ctx context.Context, path string) (gitadapter.Status, error) {
	if err := g.hit("status"); err != nil {
		return gitadapter.Status{}, err
	}
	return g.Git.Status(ctx, path)
}
func (g *pushEscalationCancelSeamGit) ContainsCommits(ctx context.Context, path string, commits []string) (bool, error) {
	if err := g.hit("contains-commits"); err != nil {
		return false, err
	}
	return g.Git.ContainsCommits(ctx, path, commits)
}
func (g *pushEscalationCancelSeamGit) AheadBehind(ctx context.Context, path string) (int, int, bool, error) {
	if err := g.hit("ahead-behind"); err != nil {
		return 0, 0, false, err
	}
	return g.Git.AheadBehind(ctx, path)
}
func (g *pushEscalationCancelSeamGit) Upstream(ctx context.Context, path string) (gitadapter.Upstream, error) {
	if err := g.hit("upstream"); err != nil {
		return gitadapter.Upstream{}, err
	}
	return g.Git.Upstream(ctx, path)
}
func (g *pushEscalationCancelSeamGit) AdvertisedCommit(ctx context.Context, url, ref string) (string, error) {
	g.remoteCalls++
	return g.Git.AdvertisedCommit(ctx, url, ref)
}

func newPushEscalationFixture(t *testing.T) pushEscalationFixture {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	middle := testutil.NewPushedGitRepository(t)
	leaf := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	root.CommitFile(".gitignore", ".wtree.yml\n/middle/\n", "ignore local authority and middle")
	middle.CommitFile("middle.txt", "middle\n", "middle")
	middle.CommitFile(".gitignore", "/leaf/\n", "ignore leaf")
	leaf.CommitFile("leaf.txt", "leaf\n", "leaf")
	git := gitadapter.NewAdapter("git")
	ctx := context.Background()
	type facts struct {
		common, head string
		upstream     gitadapter.Upstream
		roots        []string
	}
	read := func(repository testutil.PushedGitRepository) facts {
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
		return facts{common: common, head: head, upstream: upstream, roots: roots}
	}
	rootFacts, middleFacts, leafFacts := read(root), read(middle), read(leaf)
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "push-escalation", Name: "push escalation", BaseRepository: "root"}, Repositories: map[string]config.PortableRepository{
		"root":   {Clone: config.CloneSource{Remote: rootFacts.upstream.Remote, URL: rootFacts.upstream.FetchURL}, Upstream: config.Upstream{Branch: "main", Remote: rootFacts.upstream.Remote, Merge: rootFacts.upstream.Merge}, Identity: config.RepositoryIdentity{InitialCommits: rootFacts.roots}, Mount: ".", DefaultBranch: "main"},
		"middle": {Clone: config.CloneSource{Remote: middleFacts.upstream.Remote, URL: middleFacts.upstream.FetchURL}, Upstream: config.Upstream{Branch: "main", Remote: middleFacts.upstream.Remote, Merge: middleFacts.upstream.Merge}, Identity: config.RepositoryIdentity{InitialCommits: middleFacts.roots}, Parent: "root", Mount: "middle", DefaultBranch: "main"},
		"leaf":   {Clone: config.CloneSource{Remote: leafFacts.upstream.Remote, URL: leafFacts.upstream.FetchURL}, Upstream: config.Upstream{Branch: "main", Remote: leafFacts.upstream.Remote, Merge: leafFacts.upstream.Merge}, Identity: config.RepositoryIdentity{InitialCommits: leafFacts.roots}, Parent: "middle", Mount: "leaf", DefaultBranch: "main"},
	}}
	manifestBytes, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	mustPushWriteBytes(t, filepath.Join(root.Path, "project.wtree.yml"), manifestBytes)
	root.Run(t, "add", "project.wtree.yml")
	root.Run(t, "commit", "-m", "add manifest")
	root.Run(t, "push", "origin", "main")
	middlePath := filepath.Join(root.Path, "middle")
	middle.Run(t, "worktree", "add", "-b", "push-middle", middlePath, "HEAD")
	pushShellGit(t, middlePath, "config", "branch.push-middle.remote", "origin")
	pushShellGit(t, middlePath, "config", "branch.push-middle.merge", "refs/heads/main")
	leafPath := filepath.Join(middlePath, "leaf")
	leaf.Run(t, "worktree", "add", "-b", "push-leaf", leafPath, "HEAD")
	pushShellGit(t, leafPath, "config", "branch.push-leaf.remote", "origin")
	pushShellGit(t, leafPath, "config", "branch.push-leaf.merge", "refs/heads/main")
	configPath := filepath.Join(root.Path, ".wtree.yml")
	local := config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: "push-escalation", Name: "push escalation", BaseRepository: "root"}, LogicalRoot: ".", Repositories: map[string]config.Repository{
		"root":   {Source: ".", DefaultMount: ".", DefaultBranch: "main"},
		"middle": {Source: "middle", Parent: "root", DefaultMount: "middle", DefaultBranch: "main"},
		"leaf":   {Source: filepath.Join("middle", "leaf"), Parent: "middle", DefaultMount: "leaf", DefaultBranch: "main"},
	}, Worktrees: config.Worktrees{Root: filepath.Join(t.TempDir(), "worktrees")}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: filepath.Join(root.Path, "project.wtree.yml")}}
	if err := config.WriteProjectFile(configPath, local); err != nil {
		t.Fatal(err)
	}
	rootHead, err := git.Head(ctx, root.Path)
	if err != nil {
		t.Fatal(err)
	}
	middleHead, err := git.Head(ctx, middlePath)
	if err != nil {
		t.Fatal(err)
	}
	leafHead, err := git.Head(ctx, leafPath)
	if err != nil {
		t.Fatal(err)
	}
	project := domain.Project{Version: domain.CurrentVersion, ID: "push-escalation", Name: "push escalation", ConfigPath: configPath, LogicalRoot: root.Path, BaseRepository: "root", Repositories: []domain.Repository{
		{ID: "leaf", ParentID: "middle", CommonGitDir: leafFacts.common, SourcePath: leaf.Path, DefaultMount: "leaf", DefaultBranch: "main"},
		{ID: "root", CommonGitDir: rootFacts.common, SourcePath: root.Path, DefaultMount: ".", DefaultBranch: "main"},
		{ID: "middle", ParentID: "root", CommonGitDir: middleFacts.common, SourcePath: middle.Path, DefaultMount: "middle", DefaultBranch: "main"},
	}}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: root.Path, Checkouts: []domain.Checkout{
		{RepositoryID: "leaf", Branch: "push-leaf", Head: leafHead, Mount: "leaf", ResolvedPath: leafPath},
		{RepositoryID: "root", Branch: "main", Head: rootHead, Mount: ".", ResolvedPath: root.Path},
		{RepositoryID: "middle", Branch: "push-middle", Head: middleHead, Mount: "middle", ResolvedPath: middlePath},
	}}
	fixture := pushEscalationFixture{
		project:       project,
		workspace:     workspace,
		dataRoot:      t.TempDir(),
		remotes:       map[string]string{"root": rootFacts.upstream.FetchURL, "middle": middleFacts.upstream.FetchURL, "leaf": leafFacts.upstream.FetchURL},
		markers:       map[string]string{},
		checkoutPaths: map[string]string{"root": root.Path, "middle": middlePath, "leaf": leafPath},
	}
	pushEscalationSeedResolverAuthority(t, &fixture)
	for _, id := range []string{"root", "middle", "leaf"} {
		checkout := pushEscalationCheckout(workspace, id)
		marker := filepath.Join(t.TempDir(), id+"-fsmonitor-marker")
		hook := filepath.Join(t.TempDir(), id+"-fsmonitor-hook")
		mustPushWrite(t, hook, "#!/bin/sh\ntouch \""+marker+"\"\nprintf '2\\n'\n")
		if err := os.Chmod(hook, 0o755); err != nil {
			t.Fatal(err)
		}
		pushShellGit(t, checkout.ResolvedPath, "config", "core.fsmonitor", hook)
		fixture.markers[id] = marker
	}
	return fixture
}

// pushEscalationSeedResolverAuthority creates the same registered, persisted
// authority that the public read-only resolver consumes.  The snapshot tests
// must not use arbitrary sentinel files that Push cannot legally observe.
func pushEscalationSeedResolverAuthority(t *testing.T, fixture *pushEscalationFixture) {
	t.Helper()
	repositoryIDs := make(map[string]string, len(fixture.project.Repositories))
	for _, repository := range fixture.project.Repositories {
		repositoryIDs[repository.CommonGitDir] = repository.ID
	}
	if err := store.WriteRegistry(filepath.Join(fixture.dataRoot, "registry.json"), store.Registry{Projects: map[string]store.RegistryProject{
		fixture.project.ID: {Name: fixture.project.Name, ConfigPath: fixture.project.ConfigPath, RepositoryIDs: repositoryIDs},
	}}); err != nil {
		t.Fatal(err)
	}
	state := pushEscalationWorkspaceState(fixture.workspace)
	if err := store.WriteWorkspace(WorkspaceStatePath(fixture.dataRoot, fixture.project.ID, "default"), state); err != nil {
		t.Fatal(err)
	}
	named := state
	named.ID, named.Name = "named", "named"
	named.Path = filepath.Join(fixture.dataRoot, "named-workspace")
	for id, checkout := range named.Repositories {
		switch id {
		case "root":
			checkout.ResolvedPath = named.Path
		case "middle":
			checkout.ResolvedPath = filepath.Join(named.Path, "middle")
		case "leaf":
			checkout.ResolvedPath = filepath.Join(named.Path, "middle", "leaf")
		}
		named.Repositories[id] = checkout
	}
	if err := store.WriteWorkspace(WorkspaceStatePath(fixture.dataRoot, fixture.project.ID, named.ID), named); err != nil {
		t.Fatal(err)
	}
	reconciliation, err := EncodeUpdateReconciliation(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRawAtomic(filepath.Join(fixture.dataRoot, "projects", fixture.project.ID, "reconciliation.json"), reconciliation); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	journalPath, err := UpdateJournalPath(fixture.dataRoot, fixture.project.ID, "push-evidence")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(fixture.project.LogicalRoot, "project.wtree.yml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	request := UpdateExecutionRequest{DataDir: fixture.dataRoot, ProjectID: fixture.project.ID, OperationID: "push-evidence"}
	backups, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "tracked-manifest", path: manifestPath}}, map[string][]byte{"tracked-manifest": manifest})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUpdateBackups(request, backups); err != nil {
		t.Fatal(err)
	}
	journal := UpdateJournal{Version: UpdateJournalVersion, OperationID: "push-evidence", ProjectID: fixture.project.ID, PlanDigest: digest, Generations: UpdatePlanGenerations{CurrentManifestSHA256: digest, CandidateManifestSHA256: digest, LocalConfigSHA256: digest, RegistrySHA256: digest, DefaultStateSHA256: digest}, Backups: backupMetadata(backups), RollbackState: "active", Progress: []UpdateJournalEffect{}}
	if err := writeNewUpdateJournal(NewUpdateExecutor(), journalPath, journal); err != nil {
		t.Fatal(err)
	}
	recoveryPath := RecoveryRecordPath(fixture.dataRoot, plan.WorkspacePlan{ProjectID: fixture.project.ID, WorkspaceID: named.ID})
	if err := store.WriteRecovery(recoveryPath, store.RecoveryRecord{Version: store.Version, ProjectID: fixture.project.ID, WorkspaceID: named.ID, Operation: "update", FailedStep: "terminal-cleanup", CompletedSteps: []string{"repository-effects-terminal"}, UnrevertedSteps: []string{"terminal-cleanup"}}); err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().ResolveReadOnly(context.Background(), ResolveRequest{Path: fixture.project.LogicalRoot, ProjectPath: fixture.project.LogicalRoot, DataDir: fixture.dataRoot})
	if err != nil {
		t.Fatalf("resolve seeded push authority: %v", err)
	}
	fixture.project, fixture.workspace = resolution.Project, resolution.Workspace
}

func pushEscalationWorkspaceState(workspace domain.Workspace) store.WorkspaceState {
	checkouts := make(map[string]store.CheckoutState, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		checkouts[checkout.RepositoryID] = store.CheckoutState{Branch: checkout.Branch, Mount: checkout.Mount, ResolvedPath: checkout.ResolvedPath, Head: checkout.Head, Detached: checkout.Detached}
	}
	return store.WorkspaceState{Version: store.Version, ID: workspace.ID, Name: workspace.Name, Path: workspace.RootPath, Partial: workspace.Partial, MissingRepositoryIDs: append([]string(nil), workspace.MissingRepositoryIDs...), Repositories: checkouts}
}

func pushEscalationAuthoritySnapshot(t *testing.T, fixture pushEscalationFixture) string {
	t.Helper()
	parts := []string{
		"project-tree\n" + pushTreeSnapshot(t, fixture.project.LogicalRoot),
		"data-tree\n" + pushTreeSnapshot(t, fixture.dataRoot),
	}
	for _, id := range []string{"root", "middle", "leaf"} {
		parts = append(parts, "checkout:"+id+"\n"+pushEscalationCheckoutSnapshot(t, fixture.checkoutPaths[id]))
		parts = append(parts, "remote:"+id+"\n"+pushEscalationRemoteSnapshot(t, fixture.remotes[id]))
		parts = append(parts, "fsmonitor-marker:"+id+"="+pushEscalationPathSnapshot(t, fixture.markers[id]))
	}
	for _, path := range []string{
		filepath.Join(fixture.dataRoot, "registry.json"),
		filepath.Join(fixture.dataRoot, "state", fixture.project.ID, "default.json"),
		filepath.Join(fixture.dataRoot, "state", fixture.project.ID, "named.json"),
		filepath.Join(fixture.dataRoot, "projects", fixture.project.ID, "reconciliation.json"),
		filepath.Join(fixture.dataRoot, "projects", fixture.project.ID, "update"),
		filepath.Join(fixture.dataRoot, "projects", fixture.project.ID, "recovery"),
		filepath.Join(fixture.dataRoot, "locks", "registry.lock"),
		filepath.Join(fixture.dataRoot, "locks", fixture.project.ID+".lock"),
		filepath.Join(fixture.dataRoot, "staging"),
		filepath.Join(fixture.dataRoot, "tmp"),
	} {
		parts = append(parts, "authority:"+path+"="+pushEscalationPathSnapshot(t, path))
	}
	journalPath, err := UpdateJournalPath(fixture.dataRoot, fixture.project.ID, "push-evidence")
	if err != nil {
		t.Fatal(err)
	}
	parts = append(parts, "journal="+pushEscalationPathSnapshot(t, journalPath))
	parts = append(parts, "journal-backup="+pushEscalationPathSnapshot(t, filepath.Join(filepath.Dir(journalPath), "backups", "tracked-manifest.bin")))
	sort.Strings(parts)
	return strings.Join(parts, "\n\x00\n")
}

func pushEscalationCheckoutSnapshot(t *testing.T, path string) string {
	t.Helper()
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return "ABSENT"
	} else if err != nil {
		t.Fatal(err)
	}
	readGitPath := func(name string) string {
		resolved := strings.TrimSpace(pushEscalationGitOutput(t, path, "rev-parse", "--path-format=absolute", "--git-path", name))
		return pushEscalationPathSnapshot(t, resolved)
	}
	writeTree := pushEscalationGitOutput(t, path, "write-tree")
	return strings.Join([]string{
		"worktree=" + pushTreeSnapshot(t, path),
		"HEAD-commit=" + pushEscalationGitOutput(t, path, "rev-parse", "HEAD"),
		"HEAD-file=" + readGitPath("HEAD"),
		"refs=" + pushEscalationGitOutput(t, path, "for-each-ref", "--format=%(refname):%(objectname)"),
		"index=" + readGitPath("index"),
		"write-tree=" + writeTree,
		"local-config=" + pushEscalationGitOutput(t, path, "config", "--local", "--list", "--show-origin"),
		"config-file=" + readGitPath("config"),
		"FETCH_HEAD=" + readGitPath("FETCH_HEAD"),
	}, "\n\x00\n")
}

func pushEscalationRemoteSnapshot(t *testing.T, path string) string {
	t.Helper()
	command := func(args ...string) string {
		cmd := append([]string{"--git-dir=" + path}, args...)
		return pushEscalationGitOutput(t, "", cmd...)
	}
	return strings.Join([]string{
		"HEAD=" + pushEscalationPathSnapshot(t, filepath.Join(path, "HEAD")),
		"config=" + pushEscalationPathSnapshot(t, filepath.Join(path, "config")),
		"packed-refs=" + pushEscalationPathSnapshot(t, filepath.Join(path, "packed-refs")),
		"refs=" + command("for-each-ref", "--format=%(refname):%(objectname)"),
	}, "\n\x00\n")
}

func pushEscalationGitOutput(t *testing.T, path string, args ...string) string {
	t.Helper()
	if path != "" {
		args = append([]string{"-C", path}, args...)
	}
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(output)
}

func pushEscalationPathSnapshot(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "ABSENT"
	}
	if err != nil {
		t.Fatal(err)
	}
	value := fmt.Sprintf("PRESENT:type=%s:mode=%#o", pushEscalationFileType(info.Mode()), info.Mode())
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			t.Fatal(err)
		}
		return value + ":target=" + target
	}
	if info.IsDir() {
		return value + "\n" + pushEscalationTreeIncludingGit(t, path)
	}
	if info.Mode().IsRegular() {
		bytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return value + ":bytes=" + string(bytes)
	}
	return value
}

func pushEscalationTreeIncludingGit(t *testing.T, root string) string {
	t.Helper()
	entries := []string{}
	if err := filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		value := filepath.ToSlash(relative) + ":type=" + pushEscalationFileType(info.Mode()) + fmt.Sprintf(":mode=%#o", info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":target=" + target
		} else if info.Mode().IsRegular() {
			bytes, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":bytes=" + string(bytes)
		}
		entries = append(entries, value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

func pushEscalationFileType(mode os.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode.IsRegular():
		return "regular"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	default:
		return mode.Type().String()
	}
}

func pushEscalationAssertAuthority(t *testing.T, fixture pushEscalationFixture, before, label string) {
	t.Helper()
	after := pushEscalationAuthoritySnapshot(t, fixture)
	if after != before {
		t.Fatalf("push %s mutated authority: before=%x after=%x", label, sha256.Sum256([]byte(before)), sha256.Sum256([]byte(after)))
	}
}

func pushEscalationClearMarkers(t *testing.T, fixture pushEscalationFixture) {
	t.Helper()
	for _, marker := range fixture.markers {
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func pushEscalationCheckout(workspace domain.Workspace, id string) domain.Checkout {
	for _, checkout := range workspace.Checkouts {
		if checkout.RepositoryID == id {
			return checkout
		}
	}
	panic("missing escalation checkout " + id)
}

func pushEscalationRepositoryHasFinding(value PushResult, id, code string) bool {
	for _, repository := range value.Repositories {
		if repository.ID != id {
			continue
		}
		for _, finding := range repository.Findings {
			if finding.Code == code {
				return true
			}
		}
	}
	return false
}

func mustPushWrite(t *testing.T, path, value string) {
	t.Helper()
	mustPushWriteBytes(t, path, []byte(value))
}
func mustPushWriteBytes(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
}
