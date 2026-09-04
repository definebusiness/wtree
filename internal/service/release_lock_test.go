package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/testutil"
)

type releaseLockGitFake struct {
	heads   map[string]string
	status  map[string]gitadapter.Status
	tracked map[string][]byte
}

func (f releaseLockGitFake) CommonGitDir(_ context.Context, p string) (string, error) {
	return p + "-git", nil
}
func (f releaseLockGitFake) Head(_ context.Context, p string) (string, error) { return f.heads[p], nil }
func (f releaseLockGitFake) Status(_ context.Context, p string) (gitadapter.Status, error) {
	return f.status[p], nil
}
func (f releaseLockGitFake) TrackedFile(_ context.Context, _ string, _ string, path string) ([]byte, error) {
	if v, ok := f.tracked[path]; ok {
		return v, nil
	}
	return nil, os.ErrNotExist
}

func TestReleaseLockCreatesReplacesAndProtectsCandidate(t *testing.T) {
	base := t.TempDir()
	api := filepath.Join(base, "api")
	if err := os.Mkdir(api, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "project.wtree.yml"), []byte(releaseLockManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(base, ".wtree.yml")
	if err := os.WriteFile(configPath, []byte(releaseLockLocalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{Version: domain.CurrentVersion, ID: "release-project", Name: "Release", BaseRepository: "root", LogicalRoot: base, ConfigPath: configPath, Repositories: []domain.Repository{{ID: "root", CommonGitDir: base + "-git", SourcePath: base, DefaultMount: "."}, {ID: "api", CommonGitDir: api + "-git", SourcePath: api, ParentID: "root", DefaultMount: "api"}}}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: base, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: strings.Repeat("1", 40), Mount: ".", ResolvedPath: base}, {RepositoryID: "api", Branch: "main", Head: strings.Repeat("2", 40), Mount: "api", ResolvedPath: api}}}
	fake := &releaseLockGitFake{heads: map[string]string{api: strings.Repeat("a", 40), base: strings.Repeat("b", 40)}, status: map[string]gitadapter.Status{base: {}, api: {}}}
	s := NewReleaseLockServiceWith(fake, nil)
	first, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", NoHooks: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "created" {
		t.Fatalf("first status=%q", first.Status)
	}
	second, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", NoHooks: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "unchanged" {
		t.Fatalf("second status=%q", second.Status)
	}
	tracked, readErr := os.ReadFile(filepath.Join(base, ReleaseLockFilename))
	if readErr != nil {
		t.Fatal(readErr)
	}
	fake.tracked = map[string][]byte{ReleaseLockFilename: tracked}
	fake.status[base] = gitadapter.Status{}
	trackedReplace, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v2", NoHooks: true})
	if err != nil || trackedReplace.Status != "replaced" {
		t.Fatalf("clean tracked replacement=%#v,%v", trackedReplace, err)
	}
	tracked, readErr = os.ReadFile(filepath.Join(base, ReleaseLockFilename))
	if readErr != nil {
		t.Fatal(readErr)
	}
	fake.tracked[ReleaseLockFilename] = tracked
	raced := []byte("new local generation\n")
	s.beforeWrite = func() {
		temporary := filepath.Join(base, "race")
		_ = os.WriteFile(temporary, raced, 0o600)
		_ = os.Rename(temporary, filepath.Join(base, ReleaseLockFilename))
		s.beforeWrite = nil
	}
	if _, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v3", NoHooks: true}); err == nil {
		t.Fatal("target identity race accepted")
	}
	if bytesAfter, readErr := os.ReadFile(filepath.Join(base, ReleaseLockFilename)); readErr != nil || !bytes.Equal(bytesAfter, raced) {
		t.Fatalf("race changed intervening bytes=%q err=%v", bytesAfter, readErr)
	}
	// Mutate after the service identity recheck, immediately before fsutil's
	// conditional exchange. The writer must restore this generation rather
	// than publishing over it.
	fake.tracked[ReleaseLockFilename] = raced
	originalReplace := s.replaceExpected
	s.replaceExpected = func(path string, data []byte, mode os.FileMode, expected os.FileInfo) error {
		temporary := filepath.Join(base, "final-race")
		if err := os.WriteFile(temporary, []byte("final intervening generation\n"), 0o600); err != nil {
			return err
		}
		if err := os.Rename(temporary, path); err != nil {
			return err
		}
		return fsutil.WriteFileAtomicModeExpected(path, data, mode, expected)
	}
	if _, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v3", NoHooks: true}); err == nil {
		t.Fatal("final publication race accepted")
	}
	s.replaceExpected = originalReplace
	if bytesAfter, readErr := os.ReadFile(filepath.Join(base, ReleaseLockFilename)); readErr != nil || string(bytesAfter) != "final intervening generation\n" {
		t.Fatalf("final race changed intervening bytes=%q err=%v", bytesAfter, readErr)
	}
	if err := os.WriteFile(filepath.Join(base, ReleaseLockFilename), []byte("local candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v2", NoHooks: true}); err == nil {
		t.Fatal("untracked candidate accepted")
	}
	if got, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v2", NoHooks: true, Force: true}); err != nil || got.Status != "replaced" {
		t.Fatalf("forced replacement=%#v,%v", got, err)
	}
}

func TestReleaseLockDryRunDoesNotWriteOrRunHooks(t *testing.T) {
	// The complete matrix above proves real writes; this check specifically
	// retains dry-run's no-mutation contract for a missing target.
	base := t.TempDir()
	api := filepath.Join(base, "api")
	_ = os.Mkdir(api, 0o700)
	_ = os.WriteFile(filepath.Join(base, "project.wtree.yml"), []byte(releaseLockManifest), 0o600)
	configPath := filepath.Join(base, ".wtree.yml")
	_ = os.WriteFile(configPath, []byte(releaseLockLocalConfig), 0o600)
	project := domain.Project{Version: 1, ID: "release-project", Name: "Release", BaseRepository: "root", LogicalRoot: base, ConfigPath: configPath, Repositories: []domain.Repository{{ID: "root", CommonGitDir: base + "-git", SourcePath: base, DefaultMount: "."}, {ID: "api", CommonGitDir: api + "-git", SourcePath: api, ParentID: "root", DefaultMount: "api"}}}
	workspace := domain.Workspace{Version: 1, ID: "default", Name: "default", RootPath: base, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: strings.Repeat("1", 40), Mount: ".", ResolvedPath: base}, {RepositoryID: "api", Branch: "main", Head: strings.Repeat("2", 40), Mount: "api", ResolvedPath: api}}}
	s := NewReleaseLockServiceWith(releaseLockGitFake{heads: map[string]string{api: strings.Repeat("a", 40), base: strings.Repeat("b", 40)}, status: map[string]gitadapter.Status{base: {}, api: {}}}, nil)
	got, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", DryRun: true})
	if err != nil || got.Status != "created" {
		t.Fatalf("dry run=%#v,%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(base, ReleaseLockFilename)); !os.IsNotExist(err) {
		t.Fatal("dry run wrote lock")
	}
}

func TestReleaseLockHonorsCanceledContextBeforeAnyObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewReleaseLockServiceWith(releaseLockGitFake{}, nil).Lock(ctx, ReleaseLockRequest{})
	if !errors.Is(err, context.Canceled) || result.LockPath != "" {
		t.Fatalf("canceled release lock = %#v, %v", result, err)
	}
}

func TestReleaseLockWorkspacePreconditionsAllowDetachedAndRejectUnsafeInputs(t *testing.T) {
	base := t.TempDir()
	api := filepath.Join(base, "api")
	_ = os.Mkdir(api, 0o700)
	_ = os.WriteFile(filepath.Join(base, "project.wtree.yml"), []byte(releaseLockManifest), 0o600)
	configPath := filepath.Join(base, ".wtree.yml")
	_ = os.WriteFile(configPath, []byte(releaseLockLocalConfig), 0o600)
	project := domain.Project{Version: 1, ID: "release-project", Name: "Release", BaseRepository: "root", LogicalRoot: base, ConfigPath: configPath, Repositories: []domain.Repository{{ID: "root", CommonGitDir: base + "-git", SourcePath: base, DefaultMount: "."}, {ID: "api", CommonGitDir: api + "-git", SourcePath: api, ParentID: "root", DefaultMount: "api"}}}
	workspace := domain.Workspace{Version: 1, ID: "default", Name: "default", RootPath: base, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: strings.Repeat("1", 40), Mount: ".", ResolvedPath: base}, {RepositoryID: "api", Detached: true, Head: strings.Repeat("2", 40), Mount: "api", ResolvedPath: api}}}
	clean := releaseLockGitFake{heads: map[string]string{api: strings.Repeat("a", 40), base: strings.Repeat("b", 40)}, status: map[string]gitadapter.Status{base: {}, api: {}}}
	if _, err := NewReleaseLockServiceWith(clean, nil).Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", DryRun: true}); err != nil {
		t.Fatalf("detached workspace rejected: %v", err)
	}
	partial := workspace
	partial.Partial = true
	partial.MissingRepositoryIDs = []string{"api"}
	partial.Checkouts = partial.Checkouts[:1]
	if _, err := NewReleaseLockServiceWith(clean, nil).Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: partial, Name: "v1", DryRun: true}); err == nil {
		t.Fatal("partial workspace accepted")
	}
	dirty := clean
	dirty.status = map[string]gitadapter.Status{base: {}, api: {Entries: []gitadapter.StatusEntry{{Path: "work", Index: 'M'}}}}
	if _, err := NewReleaseLockServiceWith(dirty, nil).Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", DryRun: true}); err == nil {
		t.Fatal("dirty workspace accepted")
	}
	wrong := project
	wrong.Repositories = append([]domain.Repository(nil), project.Repositories...)
	wrong.Repositories[1].CommonGitDir = "wrong"
	if _, err := NewReleaseLockServiceWith(clean, nil).Lock(context.Background(), ReleaseLockRequest{Project: wrong, Workspace: workspace, Name: "v1", DryRun: true}); err == nil {
		t.Fatal("wrong identity accepted")
	}
	mismounted := workspace
	mismounted.Checkouts = append([]domain.Checkout(nil), workspace.Checkouts...)
	mismounted.Checkouts[1].ResolvedPath = filepath.Join(base, "wrong")
	if _, err := NewReleaseLockServiceWith(clean, nil).Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: mismounted, Name: "v1", DryRun: true}); err == nil {
		t.Fatal("mismounted workspace accepted")
	}
}

func TestReleaseLockRejectsUnsafeExistingTargets(t *testing.T) {
	root := t.TempDir()
	service := NewReleaseLockServiceWith(releaseLockGitFake{}, nil)
	for _, name := range []string{"directory", "symlink"} {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(root, name)
			if name == "directory" {
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink(filepath.Join(root, "missing"), target); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if _, err := service.disposition(context.Background(), root, "", false, target, []byte("candidate\n"), true); err == nil {
				t.Fatalf("unsafe %s accepted", name)
			}
		})
	}
}

func TestReleaseLockDetectsTargetIdentityChangeBeforeWrite(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ReleaseLockFilename)
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewReleaseLockServiceWith(releaseLockGitFake{}, nil)
	expected, exists, err := service.targetIdentity(target)
	if err != nil || !exists {
		t.Fatalf("initial identity=%v exists=%t", err, exists)
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, target); err != nil {
		t.Fatal(err)
	}
	if err := service.requireSameTarget(target, expected, exists); err == nil {
		t.Fatal("target identity change accepted")
	}
}

func TestReleaseLockObservesAttachedAndDetachedLocalRepositoriesWithoutFetch(t *testing.T) {
	testutil.RequireIntegration(t, "release lock local Git fixture")
	base := testutil.NewGitRepository(t)
	api := testutil.NewGitRepository(t)
	api.CommitFile("api.txt", "api\n", "api")
	apiPath := filepath.Join(base.Path, "api")
	if err := os.Rename(api.Path, apiPath); err != nil {
		t.Fatal(err)
	}
	api = testutil.GitRepository{Path: apiPath}
	base.CommitFile(".gitignore", ".wtree.yml\napi/\n", "ignore local state")
	adapter := gitadapter.NewAdapter("git")
	rootRoots, err := adapter.InitialCommits(context.Background(), base.Path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	apiRoots, err := adapter.InitialCommits(context.Background(), api.Path, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	manifest := strings.Replace(releaseLockManifest, "0123456789abcdef0123456789abcdef01234567", rootRoots[0], 1)
	manifest = strings.Replace(manifest, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", apiRoots[0], 1)
	base.CommitFile("project.wtree.yml", manifest, "manifest")
	base.CommitFile(".gitmodules", "[submodule \"api\"]\n\tpath = api\n\turl = /definitely/not/a/submodule-remote\n", "submodule declaration")
	base.Run(t, "add", "-f", "api")
	base.Run(t, "commit", "-m", "record nested api as submodule")
	base.Run(t, "config", "submodule.recurse", "true")
	if err := os.WriteFile(filepath.Join(base.Path, ".wtree.yml"), []byte(releaseLockLocalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "unexpected-hook")
	if runtime.GOOS != "windows" {
		hooks := filepath.Join(t.TempDir(), "hooks")
		if err := os.Mkdir(hooks, 0o700); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(hooks, "post-checkout")
		if err := os.WriteFile(script, []byte("#!/bin/sh\n: > '"+marker+"'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		base.Run(t, "config", "core.hooksPath", hooks)
	}
	base.Run(t, "remote", "add", "unreachable", "/definitely/not/a/remote")
	base.Run(t, "remote", "add", "origin", "https://example.test/root.git")
	api.Run(t, "remote", "add", "origin", "https://example.test/api.git")
	rootGit, err := adapter.CommonGitDir(context.Background(), base.Path)
	if err != nil {
		t.Fatal(err)
	}
	apiGit, err := adapter.CommonGitDir(context.Background(), api.Path)
	if err != nil {
		t.Fatal(err)
	}
	rootHead, err := adapter.Head(context.Background(), base.Path)
	if err != nil {
		t.Fatal(err)
	}
	apiHead, err := adapter.Head(context.Background(), api.Path)
	if err != nil {
		t.Fatal(err)
	}
	project := domain.Project{Version: 1, ID: "release-project", Name: "Release", BaseRepository: "root", LogicalRoot: base.Path, ConfigPath: filepath.Join(base.Path, ".wtree.yml"), Repositories: []domain.Repository{{ID: "root", CommonGitDir: rootGit, SourcePath: base.Path, DefaultMount: "."}, {ID: "api", CommonGitDir: apiGit, SourcePath: api.Path, ParentID: "root", DefaultMount: "api"}}}
	workspace := domain.Workspace{Version: 1, ID: "default", Name: "default", RootPath: base.Path, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: rootHead, Mount: ".", ResolvedPath: base.Path}, {RepositoryID: "api", Branch: "main", Head: apiHead, Mount: "api", ResolvedPath: api.Path}}}
	result, err := NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", NoHooks: true})
	if err != nil || result.Status != "created" {
		t.Fatalf("attached local observation=%#v,%v", result, err)
	}
	if _, markerErr := os.Stat(marker); !os.IsNotExist(markerErr) {
		t.Fatalf("release lock ran a repository hook: %v", markerErr)
	}
	createdV1, err := os.ReadFile(filepath.Join(base.Path, ReleaseLockFilename))
	if err != nil {
		t.Fatal(err)
	}
	// A named workspace can persist a valid mount override. Materialize the
	// same repository at backend so this is not rejected only because a path is
	// absent; it must not bind a lock to the portable graph's api mount merely
	// because project and repository IDs still agree.
	backendPath := filepath.Join(base.Path, "backend")
	excludePath := filepath.Join(base.Path, ".git", "info", "exclude")
	if err := os.WriteFile(excludePath, []byte("backend/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api.Run(t, "worktree", "add", "--detach", backendPath, "HEAD")
	named := workspace
	named.ID, named.Name = "backend", "backend"
	named.Checkouts = append([]domain.Checkout(nil), workspace.Checkouts...)
	named.Checkouts[1].Mount = "backend"
	named.Checkouts[1].ResolvedPath = backendPath
	named.Checkouts[1].Branch = ""
	named.Checkouts[1].Detached = true
	if _, overrideErr := NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: named, Name: "v1", NoHooks: true}); overrideErr == nil {
		t.Fatal("named workspace mount override accepted")
	}
	if lockBytes, lockErr := os.ReadFile(filepath.Join(base.Path, ReleaseLockFilename)); lockErr != nil || !bytes.Equal(lockBytes, createdV1) {
		t.Fatalf("mount override mutated release lock: %q, %v", lockBytes, lockErr)
	}
	if _, markerErr := os.Stat(marker); !os.IsNotExist(markerErr) {
		t.Fatalf("mount override rejection ran a repository hook: %v", markerErr)
	}
	base.Run(t, "add", ReleaseLockFilename)
	base.Run(t, "commit", "-m", "record release lock")
	committedV1, err := os.ReadFile(filepath.Join(base.Path, ReleaseLockFilename))
	if err != nil {
		t.Fatal(err)
	}
	base.Run(t, "rm", "--cached", ReleaseLockFilename)
	assertStagedDeletion := func() {
		t.Helper()
		status, statusErr := adapter.Status(context.Background(), base.Path)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		for _, entry := range status.Entries {
			if entry.Path == ReleaseLockFilename && entry.Index == 'D' {
				return
			}
		}
		t.Fatalf("release lock staged deletion was not retained: %#v", status.Entries)
	}
	assertStagedDeletion()
	if _, identicalDeletionErr := NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", NoHooks: true}); identicalDeletionErr == nil {
		t.Fatal("identical staged lock deletion accepted")
	}
	if lockBytes, lockErr := os.ReadFile(filepath.Join(base.Path, ReleaseLockFilename)); lockErr != nil || !bytes.Equal(lockBytes, committedV1) {
		t.Fatalf("unforced identical deletion changed working lock: %q, %v", lockBytes, lockErr)
	}
	assertStagedDeletion()
	if forceResult, forceErr := NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", NoHooks: true, Force: true}); forceErr != nil || forceResult.Status != "replaced" {
		t.Fatalf("forced identical staged deletion=%#v,%v", forceResult, forceErr)
	}
	if lockBytes, lockErr := os.ReadFile(filepath.Join(base.Path, ReleaseLockFilename)); lockErr != nil || !bytes.Equal(lockBytes, committedV1) {
		t.Fatalf("forced recreation changed lock bytes: %q, %v", lockBytes, lockErr)
	}
	assertStagedDeletion()
	base.Run(t, "reset", "HEAD", "--", ReleaseLockFilename)
	if err := os.Remove(filepath.Join(base.Path, ReleaseLockFilename)); err != nil {
		t.Fatal(err)
	}
	if _, deletionErr := NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v2", NoHooks: true}); deletionErr == nil {
		t.Fatal("unstaged tracked lock deletion accepted")
	}
	if _, deletionErr := NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v2", NoHooks: true, Force: true}); deletionErr != nil {
		t.Fatalf("force did not replace deleted tracked lock: %v", deletionErr)
	}
	base.Run(t, "checkout", "HEAD", "--", ReleaseLockFilename)
	base.Run(t, "rm", "--cached", ReleaseLockFilename)
	if _, deletionErr := NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v2", NoHooks: true}); deletionErr == nil {
		t.Fatal("staged tracked lock deletion accepted")
	}
	base.Run(t, "reset", "HEAD", "--", ReleaseLockFilename)
	result, err = NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v2", NoHooks: true})
	if err != nil || result.Status != "replaced" {
		t.Fatalf("clean tracked next release=%#v,%v", result, err)
	}
	base.Run(t, "add", ReleaseLockFilename)
	base.Run(t, "commit", "-m", "record release lock v2")
	api.Run(t, "checkout", "--detach")
	workspace.Checkouts[1].Branch = ""
	workspace.Checkouts[1].Detached = true
	result, err = NewReleaseLockService().Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v2", NoHooks: true})
	if err != nil || result.Status != "unchanged" {
		t.Fatalf("detached local observation=%#v,%v", result, err)
	}
}

type releaseHookProcessFake struct {
	resolves, runs int
	environments   [][]string
	ids            []string
	fail           bool
	output         string
	discard        bool
}

func (f *releaseHookProcessFake) Resolve(_ context.Context, r HookExecutableRequest) (HookExecutableFact, error) {
	f.resolves++
	f.environments = append(f.environments, append([]string(nil), r.Environment...))
	return HookExecutableFact{Resolved: filepath.Join(r.Directory, "tagger"), Available: true}, nil
}
func (f *releaseHookProcessFake) Run(_ context.Context, r HookProcessRequest) (HookProcessResult, error) {
	f.runs++
	f.ids = append(f.ids, r.HookID)
	f.environments = append(f.environments, append([]string(nil), r.Environment...))
	f.discard = r.Sink == io.Discard
	if f.output != "" {
		_, _ = io.WriteString(r.Sink, f.output)
	}
	if f.fail {
		return HookProcessResult{ExitCode: 1, Started: true}, nil
	}
	return HookProcessResult{Started: true}, nil
}

func TestReleaseLockPostReleasePreflightAndCoreFailureSemantics(t *testing.T) {
	base := t.TempDir()
	api := filepath.Join(base, "api")
	_ = os.Mkdir(api, 0o700)
	_ = os.WriteFile(filepath.Join(api, "tagger"), []byte("tagger"), 0o700)
	_ = os.WriteFile(filepath.Join(base, "project.wtree.yml"), []byte(releaseLockManifest), 0o600)
	configPath := filepath.Join(base, ".wtree.yml")
	_ = os.WriteFile(configPath, []byte(releaseLockLocalV3Config), 0o600)
	project := domain.Project{Version: 1, ID: "release-project", Name: "Release", BaseRepository: "root", LogicalRoot: base, ConfigPath: configPath, Repositories: []domain.Repository{{ID: "root", CommonGitDir: base + "-git", SourcePath: base, DefaultMount: "."}, {ID: "api", CommonGitDir: api + "-git", SourcePath: api, ParentID: "root", DefaultMount: "api"}}}
	workspace := domain.Workspace{Version: 1, ID: "default", Name: "default", RootPath: base, Checkouts: []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: strings.Repeat("1", 40), Mount: ".", ResolvedPath: base}, {RepositoryID: "api", Branch: "main", Head: strings.Repeat("2", 40), Mount: "api", ResolvedPath: api}}}
	git := releaseLockGitFake{heads: map[string]string{api: strings.Repeat("a", 40), base: strings.Repeat("b", 40)}, status: map[string]gitadapter.Status{base: {}, api: {}}}
	process := &releaseHookProcessFake{fail: true, output: strings.Repeat("token-DO-NOT-RENDER", 2048)}
	s := NewReleaseLockServiceWith(git, process)
	canary := "token-DO-NOT-RENDER"
	result, err := s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", Environment: []string{"PATH=/bin", "SECRET_CANARY=" + canary, "WTREE_RELEASE_NAME=forged", "WTREE_HEAD=forged"}})
	if err == nil || result.HookFailure != "tag" {
		t.Fatalf("hook failure result=%#v err=%v", result, err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("hook error leaked inherited secret: %v", err)
	}
	if !process.discard || strings.Contains(err.Error(), "token-DO-NOT-RENDER") {
		t.Fatalf("hook output was rendered or unbounded: discard=%t err=%v", process.discard, err)
	}
	if _, statErr := os.Stat(filepath.Join(base, ReleaseLockFilename)); statErr != nil {
		t.Fatalf("hook failure rolled back lock: %v", statErr)
	}
	if process.resolves != 2 || process.runs != 1 || strings.Join(process.ids, ",") != "tag" {
		t.Fatalf("hook calls = resolve %d run %d ids=%v", process.resolves, process.runs, process.ids)
	}
	if !containsEnvironment(process.environments[0], "WTREE_RELEASE_NAME=v1") || !containsEnvironment(process.environments[2], "WTREE_HEAD="+strings.Repeat("a", 40)) {
		t.Fatalf("authoritative environment=%#v", process.environments)
	}
	process.fail = false
	result, err = s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", Environment: []string{"PATH=/bin"}})
	if err != nil || result.Status != "unchanged" || strings.Join(process.ids, ",") != "tag,tag,tag2" {
		t.Fatalf("identical rerun=%#v err=%v ids=%v", result, err, process.ids)
	}
	process.resolves, process.runs = 0, 0
	_, err = s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", DryRun: true, Environment: []string{"PATH=/bin"}})
	if err != nil || process.resolves != 0 || process.runs != 0 {
		t.Fatalf("dry run preflighted hooks resolves=%d runs=%d err=%v", process.resolves, process.runs, err)
	}
	_, err = s.Lock(context.Background(), ReleaseLockRequest{Project: project, Workspace: workspace, Name: "v1", NoHooks: true, Environment: []string{"PATH=/bin"}})
	if err != nil || process.resolves != 0 || process.runs != 0 {
		t.Fatalf("no hooks preflighted hooks resolves=%d runs=%d err=%v", process.resolves, process.runs, err)
	}
}
func containsEnvironment(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

const releaseLockManifest = `version: 2
project:
  id: release-project
  name: Release
  base_repository: root
repositories:
  root:
    clone: {remote: origin, url: https://example.test/root.git}
    upstream: {branch: main, remote: origin, merge: refs/heads/main}
    identity: {initial_commits: [0123456789abcdef0123456789abcdef01234567]}
    parent: ""
    mount: .
    default_branch: main
  api:
    clone: {remote: origin, url: https://example.test/api.git}
    upstream: {branch: main, remote: origin, merge: refs/heads/main}
    identity: {initial_commits: [aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]}
    parent: root
    mount: api
    default_branch: main
`
const releaseLockLocalConfig = `version: 2
project: {id: release-project, name: Release, base_repository: root}
logical_root: .
repositories:
  root: {source: ., parent: "", mount: ., default_branch: main}
  api: {source: api, parent: root, mount: api, default_branch: main}
worktrees: {root: /tmp/worktrees}
manifest: {path: project.wtree.yml, source: https://example.test/project.wtree.yml}
`
const releaseLockLocalV3Config = `version: 3
project: {id: release-project, name: Release, base_repository: root}
logical_root: .
repositories:
  root: {source: ., parent: "", mount: ., default_branch: main}
  api: {source: api, parent: root, mount: api, default_branch: main}
worktrees: {root: /tmp/worktrees}
manifest: {path: project.wtree.yml, source: https://example.test/project.wtree.yml}
hooks:
  post-release:
    - id: tag
      repository: api
      command: [tagger]
      timeout: 1m
    - id: tag2
      repository: api
      command: [tagger]
      timeout: 1m
`
