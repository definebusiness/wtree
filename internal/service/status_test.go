package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestStatusReconcilesCleanlinessAndStructuralDrift(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, workspace *domain.Workspace, root, backend testutil.GitRepository, target string)
		want   func(t *testing.T, value service.RepositoryStatus)
	}{
		{
			name: "clean renamed mount without upstream",
			change: func(_ *testing.T, _ *domain.Workspace, _ testutil.GitRepository, _ testutil.GitRepository, _ string) {
			},
			want: func(t *testing.T, value service.RepositoryStatus) {
				if value.Status != "clean" || !value.Clean || value.Upstream || value.Mount != "." {
					t.Fatalf("root status = %#v", value)
				}
			},
		},
		{
			name: "modified staged and untracked",
			change: func(t *testing.T, _ *domain.Workspace, _ testutil.GitRepository, _ testutil.GitRepository, target string) {
				checkout := testutil.GitRepository{Path: target}
				checkout.CommitFile("staged.txt", "first\n", "first")
				if err := os.WriteFile(filepath.Join(target, "staged.txt"), []byte("second\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				checkout.Run(t, "add", "staged.txt")
				if err := os.WriteFile(filepath.Join(target, "modified.txt"), []byte("modified\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, value service.RepositoryStatus) {
				if value.Status != "modified" || value.Clean || !value.Staged || !value.Untracked {
					t.Fatalf("dirty status = %#v", value)
				}
			},
		},
		{
			name: "missing checkout",
			change: func(t *testing.T, _ *domain.Workspace, root, _ testutil.GitRepository, target string) {
				root.Run(t, "worktree", "remove", "--force", target)
			},
			want: func(t *testing.T, value service.RepositoryStatus) {
				if value.Status != "missing" || !value.Missing {
					t.Fatalf("missing status = %#v", value)
				}
			},
		},
		{
			name: "wrong branch",
			change: func(t *testing.T, _ *domain.Workspace, _ testutil.GitRepository, _ testutil.GitRepository, target string) {
				testutil.GitRepository{Path: target}.Run(t, "checkout", "-b", "hotfix/test")
			},
			want: func(t *testing.T, value service.RepositoryStatus) {
				if value.Status != "branch-mismatch" || !value.BranchMismatch || value.Branch != "hotfix/test" {
					t.Fatalf("branch status = %#v", value)
				}
			},
		},
		{
			name: "detached head",
			change: func(t *testing.T, _ *domain.Workspace, _ testutil.GitRepository, _ testutil.GitRepository, target string) {
				testutil.GitRepository{Path: target}.Run(t, "checkout", "--detach")
			},
			want: func(t *testing.T, value service.RepositoryStatus) {
				if value.Status != "detached" || !value.Detached || value.Branch != "" {
					t.Fatalf("detached status = %#v", value)
				}
			},
		},
		{
			name: "mount mismatch",
			change: func(t *testing.T, workspace *domain.Workspace, _ testutil.GitRepository, _ testutil.GitRepository, target string) {
				nested := filepath.Join(target, "nested")
				if err := os.Mkdir(nested, 0o755); err != nil {
					t.Fatal(err)
				}
				for index := range workspace.Checkouts {
					if workspace.Checkouts[index].RepositoryID == "root" {
						workspace.Checkouts[index].ResolvedPath = nested
					}
				}
			},
			want: func(t *testing.T, value service.RepositoryStatus) {
				if value.Status != "mount-mismatch" || !value.MountMismatch {
					t.Fatalf("mount status = %#v", value)
				}
			},
		},
		{
			name: "unknown repository",
			change: func(t *testing.T, workspace *domain.Workspace, _ testutil.GitRepository, _ testutil.GitRepository, _ string) {
				other := testutil.NewPushedGitRepository(t)
				other.CommitFile("other.txt", "other\n", "other")
				for index := range workspace.Checkouts {
					if workspace.Checkouts[index].RepositoryID == "root" {
						workspace.Checkouts[index].ResolvedPath = other.Path
					}
				}
			},
			want: func(t *testing.T, value service.RepositoryStatus) {
				if value.Status != "unknown-repository" || !value.UnknownRepository {
					t.Fatalf("unknown status = %#v", value)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			target := filepath.Join(t.TempDir(), "workspace")
			if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/status", TargetPath: target, DataDir: data, Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}}}, nil); err != nil {
				t.Fatal(err)
			}
			workspace, err := service.RequireWorkspace(project, data, "feature/status")
			if err != nil {
				t.Fatal(err)
			}
			test.change(t, &workspace, root, backend, target)
			value, err := service.NewStatusService().Status(context.Background(), project, workspace)
			if err != nil {
				t.Fatal(err)
			}
			test.want(t, statusFor(t, value, "root"))
			if value.Repositories[1].Mount != "api" || value.Repositories[1].Path != filepath.Join(target, "api") {
				t.Fatalf("renamed backend = %#v", value.Repositories[1])
			}
		})
	}
}

func TestStatusPreservesDivergentImportedBranchesAndPartialState(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/status", TargetPath: target, DataDir: data, Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}}}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/status")
	if err != nil {
		t.Fatal(err)
	}
	testutil.GitRepository{Path: filepath.Join(target, "api")}.Run(t, "checkout", "-b", "backend/only")
	for index := range workspace.Checkouts {
		if workspace.Checkouts[index].RepositoryID == "backend" {
			workspace.Checkouts[index].Branch = "backend/only"
		}
	}
	value, err := service.NewStatusService().Status(context.Background(), project, workspace)
	if err != nil {
		t.Fatal(err)
	}
	backendStatus := statusFor(t, value, "backend")
	if backendStatus.Branch != "backend/only" || backendStatus.ExpectedBranch != "backend/only" || backendStatus.BranchMismatch || backendStatus.Status != "clean" {
		t.Fatalf("divergent backend status = %#v", backendStatus)
	}

	partial := domain.Workspace{Version: domain.CurrentVersion, ID: "partial", Name: "partial", RootPath: target, Partial: true, MissingRepositoryIDs: []string{"root"}, Checkouts: []domain.Checkout{{RepositoryID: "backend", Branch: "backend/only", Head: backendStatus.Head, Mount: "api", ResolvedPath: filepath.Join(target, "api")}}}
	partialValue, err := service.NewStatusService().Status(context.Background(), project, partial)
	if err != nil {
		t.Fatal(err)
	}
	if !partialValue.Partial || statusFor(t, partialValue, "root").Status != "missing" {
		t.Fatalf("partial status = %#v", partialValue)
	}
	_ = root
	_ = backend
}

func TestStatusReportsStaleState(t *testing.T) {
	project, _, _, _ := createFixture(t)
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "stale", Name: "stale", RootPath: t.TempDir()}
	value, err := service.NewStatusService().Status(context.Background(), project, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusFor(t, value, "root"); got.Status != "stale-state" || !got.StaleState {
		t.Fatalf("stale root = %#v", got)
	}
}

func TestStatusIgnoresManagedChildCheckoutWithSpacesAndUnicodeMount(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	mount := filepath.Join("api space", "δοκιμή")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/status", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: mount}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/status")
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.NewStatusService().Status(context.Background(), project, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if root := statusFor(t, value, "root"); root.Status != "clean" || !root.Clean {
		t.Fatalf("root status with managed unicode child = %#v", root)
	}
}

func TestStatusKeepsStagedRenameFromOutsideIntoManagedChild(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/status", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/status")
	if err != nil {
		t.Fatal(err)
	}
	rootCheckout := testutil.GitRepository{Path: target}
	object := runGitValue(t, target, "rev-parse", "HEAD:root.txt")
	rootCheckout.Run(t, "update-index", "--force-remove", "root.txt")
	rootCheckout.Run(t, "update-index", "--add", "--cacheinfo", "100644,"+object+",api/relocated.txt")
	gitStatus, err := gitadapter.NewAdapter("git").Status(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	stagedRename := false
	for _, entry := range gitStatus.Entries {
		stagedRename = stagedRename || entry.Index == 'R' && entry.Path == "api/relocated.txt" && entry.OriginalPath == "root.txt"
	}
	if !stagedRename {
		t.Fatalf("staged rename status = %#v", gitStatus)
	}
	value, err := service.NewStatusService().Status(context.Background(), project, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if root := statusFor(t, value, "root"); root.Status != "modified" || root.Clean || !root.Staged {
		t.Fatalf("outside-to-child rename was hidden: %#v", root)
	}
}

func TestStatusUsesOnlyLocalGitFactsAndDoesNotMutate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper fixture is POSIX-only")
	}
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/status", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api space"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/status")
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, project.ID, workspace.ID)
	registryPath := filepath.Join(data, "registry.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	backendPath, err := workspace.ResolveRepository("backend")
	if err != nil {
		t.Fatal(err)
	}
	testutil.GitRepository{Path: backendPath}.Run(t, "branch", "status-child-guard")
	refsBefore := snapshotStatusWorktreeRefs(t, workspace)
	if strings.Contains(refsBefore["root"], "refs/heads/status-child-guard") || !strings.Contains(refsBefore["backend"], "refs/heads/status-child-guard") {
		t.Fatalf("child-only ref snapshot = %#v", refsBefore)
	}
	indexBefore := snapshotStatusWorktreeIndexes(t, workspace)
	for _, repositoryID := range []string{"root", "backend"} {
		if _, found := indexBefore[repositoryID]; !found {
			t.Fatalf("missing index metadata for managed repository %q: %#v", repositoryID, indexBefore)
		}
	}
	wrapper := newRemoteRejectingGitWrapper(t)
	if _, err := service.NewStatusServiceWith(gitadapter.NewAdapter(wrapper)).Status(context.Background(), project, workspace); err != nil {
		t.Fatal(err)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("status mutated workspace state: before=%q after=%q error=%v", stateBefore, stateAfter, err)
	}
	registryAfter, err := os.ReadFile(registryPath)
	if err != nil || !bytes.Equal(registryBefore, registryAfter) {
		t.Fatalf("status mutated registry: before=%q after=%q error=%v", registryBefore, registryAfter, err)
	}
	assertStatusWorktreeRefsUnchanged(t, refsBefore, workspace)
	assertStatusWorktreeIndexesUnchanged(t, indexBefore, workspace)
}

func TestStatusClassifiesGitFactFailure(t *testing.T) {
	project, _, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/status", TargetPath: target, DataDir: data}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/status")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.NewStatusServiceWith(failingCurrentBranchGit{Git: gitadapter.NewAdapter("git")}).Status(context.Background(), project, workspace)
	var statusError *service.Error
	if !errors.As(err, &statusError) || statusError.Kind != service.ErrorGit {
		t.Fatalf("status error = %v, want %s", err, service.ErrorGit)
	}
}

func TestStatusWithDataDirUsesTrackedManifestWithoutRemoteOrMutation(t *testing.T) {
	project, root, _, data := createFixture(t)
	// Initialisation intentionally leaves the portable manifest uncommitted.
	// Commit it here so this test exercises the authoritative local-only path,
	// rather than the compatibility fallback used by older projects.
	root.Run(t, "add", "-f", ".wtree.yml", "project.wtree.yml")
	root.Run(t, "commit", "-m", "track portable manifest")
	workspace, err := service.RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	statePath := service.WorkspaceStatePath(data, project.ID, "default")
	registryPath := filepath.Join(data, "registry.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := snapshotStatusWorktreeRefs(t, workspace)
	indexesBefore := snapshotStatusWorktreeIndexes(t, workspace)
	value, err := service.NewStatusServiceWith(gitadapter.NewAdapter(newRemoteRejectingGitWrapper(t))).StatusWithDataDir(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	rootStatus := statusFor(t, value, project.BaseRepository)
	if !rootStatus.HeadMismatch || rootStatus.ExpectedHead == "" || rootStatus.Head == rootStatus.ExpectedHead {
		t.Fatalf("root expected/actual HEAD facts = %#v", rootStatus)
	}
	foundHeadDrift := false
	for _, drift := range value.Drift {
		if drift.ID == project.BaseRepository && drift.Origin == "checkout" && drift.Check == "head" {
			foundHeadDrift = true
		}
	}
	if !foundHeadDrift {
		t.Fatalf("status drift lacks expected HEAD mismatch: %#v", value.Drift)
	}
	stateAfter, stateErr := os.ReadFile(statePath)
	registryAfter, registryErr := os.ReadFile(registryPath)
	if stateErr != nil || registryErr != nil || !bytes.Equal(stateBefore, stateAfter) || !bytes.Equal(registryBefore, registryAfter) {
		t.Fatalf("status changed durable state: state=%v registry=%v", stateErr, registryErr)
	}
	assertStatusWorktreeRefsUnchanged(t, refsBefore, workspace)
	assertStatusWorktreeIndexesUnchanged(t, indexesBefore, workspace)
}

func TestStatusWithDataDirTrackedManifestFallbackStillReportsRecovery(t *testing.T) {
	project, _, _, data := createFixture(t)
	workspace, err := service.RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "default.json")
	if err := store.WriteRecovery(recoveryPath, store.RecoveryRecord{ProjectID: project.ID, WorkspaceID: "default", Operation: "update", FailedStep: "publish"}); err != nil {
		t.Fatal(err)
	}
	value, err := service.NewStatusServiceWith(gitadapter.NewAdapter(newRemoteRejectingGitWrapper(t))).StatusWithDataDir(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Drift) != 1 || value.Drift[0].Origin != "operation" || value.Drift[0].Check != "update-recovery-record" || value.Drift[0].Status != "incomplete-operation" {
		t.Fatalf("fallback drift = %#v", value.Drift)
	}
}

func TestStatusWithDataDirTrackedManifestProjectsAbsentCheckoutOnce(t *testing.T) {
	project, root, backend, data := createFixture(t)
	root.Run(t, "add", "-f", ".wtree.yml", "project.wtree.yml")
	root.Run(t, "commit", "-m", "track portable manifest")
	defaultStatePath := service.WorkspaceStatePath(data, project.ID, "default")
	defaultStateBytes, err := os.ReadFile(defaultStatePath)
	if err != nil {
		t.Fatal(err)
	}
	defaultState, err := store.DecodeWorkspace(defaultStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	base := defaultState.Repositories[project.BaseRepository]
	base.Head = runGitValue(t, root.Path, "rev-parse", "HEAD")
	defaultState.Repositories[project.BaseRepository] = base
	if err := store.WriteWorkspace(defaultStatePath, defaultState); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/absent", TargetPath: target, DataDir: data, Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}}}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/absent")
	if err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(target, "api")
	backend.Run(t, "worktree", "remove", "--force", childPath)
	rootOnly := statusWorkspaceRepositories(workspace, project.BaseRepository)
	refsBefore := snapshotStatusWorktreeRefs(t, rootOnly)
	indexesBefore := snapshotStatusWorktreeIndexes(t, rootOnly)
	value, err := service.NewStatusServiceWith(gitadapter.NewAdapter(newRemoteRejectingGitWrapper(t))).StatusWithDataDir(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	child := statusFor(t, value, "backend")
	if !child.Missing || child.Status != "missing" || child.Upstream || child.ActualIdentity != "" {
		t.Fatalf("absent repository status = %#v", child)
	}
	assertExactlyStatusDrift(t, value, "backend", "manifest", "checkout", "declared-absent")
	assertStatusWorktreeRefsUnchanged(t, refsBefore, rootOnly)
	assertStatusWorktreeIndexesUnchanged(t, indexesBefore, rootOnly)
}

func TestStatusWithDataDirTrackedManifestProjectsReplacementIdentityOnce(t *testing.T) {
	project, root, backend, data := createFixture(t)
	root.Run(t, "add", "-f", ".wtree.yml", "project.wtree.yml")
	root.Run(t, "commit", "-m", "track portable manifest")
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/replacement", TargetPath: target, DataDir: data, Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}}}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/replacement")
	if err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(target, "api")
	backend.Run(t, "worktree", "remove", "--force", childPath)
	replacement := testutil.NewPushedGitRepository(t)
	replacement.CommitFile("replacement.txt", "replacement\n", "replacement")
	if err := os.Rename(replacement.Path, childPath); err != nil {
		t.Fatal(err)
	}
	replacementIdentity, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), childPath)
	if err != nil {
		t.Fatal(err)
	}
	rootOnly := statusWorkspaceRepositories(workspace, project.BaseRepository)
	refsBefore := snapshotStatusWorktreeRefs(t, rootOnly)
	indexesBefore := snapshotStatusWorktreeIndexes(t, rootOnly)
	value, err := service.NewStatusServiceWith(gitadapter.NewAdapter(newRemoteRejectingGitWrapper(t))).StatusWithDataDir(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	child := statusFor(t, value, "backend")
	if !child.UnknownRepository || !child.IdentityMismatch || child.Status != "unknown-repository" || child.Upstream || child.ActualIdentity != replacementIdentity || child.ActualIdentity == child.ExpectedIdentity {
		t.Fatalf("replacement repository status = %#v", child)
	}
	assertExactlyStatusDrift(t, value, "backend", "checkout", "identity", "mismatch")
	assertStatusWorktreeRefsUnchanged(t, refsBefore, rootOnly)
	assertStatusWorktreeIndexesUnchanged(t, indexesBefore, rootOnly)
}

func TestStatusWithDataDirKeepsDefaultIdentityDriftSeparateFromHealthySelectedWorkspace(t *testing.T) {
	project, root, backend, data := createFixture(t)
	root.Run(t, "add", "-f", ".wtree.yml", "project.wtree.yml")
	root.Run(t, "commit", "-m", "track portable manifest")
	defaultStatePath := service.WorkspaceStatePath(data, project.ID, "default")
	defaultStateBytes, err := os.ReadFile(defaultStatePath)
	if err != nil {
		t.Fatal(err)
	}
	defaultState, err := store.DecodeWorkspace(defaultStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	base := defaultState.Repositories[project.BaseRepository]
	base.Head = runGitValue(t, root.Path, "rev-parse", "HEAD")
	defaultState.Repositories[project.BaseRepository] = base
	if err := store.WriteWorkspace(defaultStatePath, defaultState); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/healthy", TargetPath: target, DataDir: data, Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}}}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/healthy")
	if err != nil {
		t.Fatal(err)
	}
	defaultWorkspace, err := service.RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	fakeIdentity := filepath.Join(t.TempDir(), "replacement.git")
	defaultBackendPath := backend.Path
	refsBefore := snapshotStatusWorktreeRefs(t, workspace)
	indexesBefore := snapshotStatusWorktreeIndexes(t, workspace)
	defaultRefsBefore := snapshotStatusWorktreeRefs(t, defaultWorkspace)
	defaultIndexesBefore := snapshotStatusWorktreeIndexes(t, defaultWorkspace)
	localOnlyGit := defaultIdentityMismatchingGit{
		Git:      gitadapter.NewAdapter(newRemoteRejectingGitWrapper(t)),
		path:     defaultBackendPath,
		identity: fakeIdentity,
	}
	value, err := service.NewStatusServiceWith(localOnlyGit).StatusWithDataDir(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	child := statusFor(t, value, "backend")
	if child.ActualIdentity != child.ExpectedIdentity || child.IdentityMismatch || child.UnknownRepository || child.Status != "clean" || !child.Clean || child.Branch != child.ExpectedBranch {
		t.Fatalf("healthy selected repository was contaminated by default drift: %#v", child)
	}
	if statusHasDrift(value, "checkout", "identity") {
		t.Fatalf("selected identity drift was synthesized: %#v", value.Drift)
	}
	assertExactlyStatusDrift(t, value, "backend", "default-checkout", "identity", "mismatch")
	if len(value.Drift) != 1 {
		t.Fatalf("default-only drift was not canonical and ParentFirst: %#v", value.Drift)
	}
	assertStatusWorktreeRefsUnchanged(t, refsBefore, workspace)
	assertStatusWorktreeIndexesUnchanged(t, indexesBefore, workspace)
	assertStatusWorktreeRefsUnchanged(t, defaultRefsBefore, defaultWorkspace)
	assertStatusWorktreeIndexesUnchanged(t, defaultIndexesBefore, defaultWorkspace)
}

func TestStatusWithDataDirProjectsTrackedLocalAuthorityAndOperationEvidence(t *testing.T) {
	project, root, _, data := createFixture(t)
	root.Run(t, "add", "-f", ".wtree.yml", "project.wtree.yml")
	root.Run(t, "commit", "-m", "track portable manifest")
	workspace, err := service.RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(data, "registry.json")
	defaultPath := service.WorkspaceStatePath(data, project.ID, "default")
	configPath := project.ConfigPath
	registry, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	stateValue, err := store.DecodeWorkspace(state)
	if err != nil {
		t.Fatal(err)
	}
	base := stateValue.Repositories[project.BaseRepository]
	base.Head = runGitValue(t, root.Path, "rev-parse", "HEAD")
	stateValue.Repositories[project.BaseRepository] = base
	if err := store.WriteWorkspace(defaultPath, stateValue); err != nil {
		t.Fatal(err)
	}
	state, err = os.ReadFile(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err = service.RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	reconciliationPath := filepath.Join(data, "projects", project.ID, "reconciliation.json")
	reconciliation, err := service.EncodeUpdateReconciliation([]service.UpdateRetainedFact{{RepositoryID: "retained", Path: filepath.Join(data, "retained"), CommonGitDir: filepath.Join(data, "retained.git")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(reconciliationPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reconciliationPath, reconciliation, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRecovery(filepath.Join(data, "projects", project.ID, "recovery", "default.json"), store.RecoveryRecord{ProjectID: project.ID, WorkspaceID: "default", Operation: "update", FailedStep: "publish"}); err != nil {
		t.Fatal(err)
	}
	writeStatusActiveJournal(t, data, project.ID)
	assertCheck := func(want string) {
		t.Helper()
		value, statusErr := service.NewStatusServiceWith(gitadapter.NewAdapter(newRemoteRejectingGitWrapper(t))).StatusWithDataDir(context.Background(), project, workspace, data)
		if statusErr != nil {
			t.Fatalf("status %s: %v", want, statusErr)
		}
		if !statusHasDrift(value, "authority", want) || !statusHasDrift(value, "retained", "retained-unmanaged") || !statusHasDrift(value, "operation", "update-in-progress") || !statusHasDrift(value, "operation", "update-recovery-record") {
			t.Fatalf("status %s did not preserve independent retained/recovery/update evidence: %#v", want, value.Drift)
		}
	}
	value, err := service.NewStatusServiceWith(gitadapter.NewAdapter(newRemoteRejectingGitWrapper(t))).StatusWithDataDir(context.Background(), project, workspace, data)
	if err != nil {
		t.Fatal(err)
	}
	if !statusHasDrift(value, "retained", "retained-unmanaged") || !statusHasDrift(value, "operation", "update-in-progress") || !statusHasDrift(value, "operation", "update-recovery-record") {
		t.Fatalf("tracked status drift lacks retained/recovery/update evidence: %#v", value.Drift)
	}
	if err := os.WriteFile(registryPath, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertCheck("registry-generation")
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultPath, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertCheck("default-state")
	if err := os.WriteFile(defaultPath, state, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("not yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertCheck("local-config")
	if err := os.WriteFile(configPath, configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	badState := filepath.Join(filepath.Dir(defaultPath), "unexpected.json")
	if err := os.WriteFile(badState, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The shared collector reports an invalid extra state generation through
	// the default-state authority before it can safely decode a workspace;
	// independently inventorying the durable operation evidence remains safe.
	assertCheck("default-state")
}

func writeStatusActiveJournal(t *testing.T, dataDir, projectID string) {
	t.Helper()
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	path, err := service.UpdateJournalPath(dataDir, projectID, "update-0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	journal := service.UpdateJournal{Version: service.UpdateJournalVersion, OperationID: "update-0123456789abcdef01234567", ProjectID: projectID, PlanDigest: digest, Generations: service.UpdatePlanGenerations{CurrentManifestSHA256: digest, CandidateManifestSHA256: digest, LocalConfigSHA256: digest, RegistrySHA256: digest, DefaultStateSHA256: digest}, RollbackState: "active"}
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func statusHasDrift(value service.WorkspaceStatus, origin, check string) bool {
	for _, drift := range value.Drift {
		if drift.Origin == origin && drift.Check == check {
			return true
		}
	}
	return false
}

func assertExactlyStatusDrift(t *testing.T, value service.WorkspaceStatus, id, origin, check, status string) {
	t.Helper()
	count := 0
	for _, drift := range value.Drift {
		if drift.ID == id && drift.Origin == origin && drift.Check == check && drift.Status == status {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("status drift %s/%s/%s/%s count=%d all=%#v", id, origin, check, status, count, value.Drift)
	}
}

func statusWorkspaceRepositories(workspace domain.Workspace, ids ...string) domain.Workspace {
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	result := workspace
	result.Checkouts = nil
	for _, checkout := range workspace.Checkouts {
		if allowed[checkout.RepositoryID] {
			result.Checkouts = append(result.Checkouts, checkout)
		}
	}
	return result
}

func TestWorkspaceStatusLegacyJSONBytesRemainIdenticalWithoutAdditiveFacts(t *testing.T) {
	type legacyRepository struct {
		ID                string `json:"id"`
		ParentID          string `json:"parentId,omitempty"`
		Branch            string `json:"branch,omitempty"`
		ExpectedBranch    string `json:"expectedBranch,omitempty"`
		Head              string `json:"head,omitempty"`
		Mount             string `json:"mount,omitempty"`
		Path              string `json:"path,omitempty"`
		ResolvedPath      string `json:"resolvedPath,omitempty"`
		Clean             bool   `json:"clean"`
		Staged            bool   `json:"staged,omitempty"`
		Modified          bool   `json:"modified,omitempty"`
		Untracked         bool   `json:"untracked,omitempty"`
		Missing           bool   `json:"missing,omitempty"`
		BranchMismatch    bool   `json:"branchMismatch,omitempty"`
		MountMismatch     bool   `json:"mountMismatch,omitempty"`
		Detached          bool   `json:"detached,omitempty"`
		UnknownRepository bool   `json:"unknownRepository,omitempty"`
		StaleState        bool   `json:"staleState,omitempty"`
		Ahead             int    `json:"ahead,omitempty"`
		Behind            int    `json:"behind,omitempty"`
		Upstream          bool   `json:"upstream,omitempty"`
		Status            string `json:"status"`
	}
	type legacyWorkspace struct {
		Workspace            string             `json:"workspace"`
		LogicalRoot          string             `json:"logicalRoot,omitempty"`
		BaseRepository       string             `json:"baseRepository,omitempty"`
		Partial              bool               `json:"partial,omitempty"`
		MissingRepositoryIDs []string           `json:"missingRepositoryIds,omitempty"`
		Repositories         []legacyRepository `json:"repositories"`
	}
	legacy := legacyWorkspace{Workspace: "default", LogicalRoot: "/tree", BaseRepository: "root", Partial: true, MissingRepositoryIDs: []string{"missing"}, Repositories: []legacyRepository{{ID: "root", ParentID: "parent", Branch: "main", ExpectedBranch: "main", Head: "0123", Mount: ".", Path: "/tree", ResolvedPath: "/tree", Clean: true, Staged: true, Modified: true, Untracked: true, Missing: true, BranchMismatch: true, MountMismatch: true, Detached: true, UnknownRepository: true, StaleState: true, Ahead: 1, Behind: 2, Upstream: true, Status: "unknown-repository"}}}
	current := service.WorkspaceStatus{Workspace: legacy.Workspace, LogicalRoot: legacy.LogicalRoot, BaseRepository: legacy.BaseRepository, Partial: legacy.Partial, MissingRepositoryIDs: legacy.MissingRepositoryIDs, Repositories: []service.RepositoryStatus{{ID: "root", ParentID: "parent", Branch: "main", ExpectedBranch: "main", Head: "0123", Mount: ".", Path: "/tree", ResolvedPath: "/tree", Clean: true, Staged: true, Modified: true, Untracked: true, Missing: true, BranchMismatch: true, MountMismatch: true, Detached: true, UnknownRepository: true, StaleState: true, Ahead: 1, Behind: 2, Upstream: true, Status: "unknown-repository"}}}
	want, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("legacy JSON changed:\n got %s\nwant %s", got, want)
	}
}

type failingCurrentBranchGit struct{ gitadapter.Git }

func (failingCurrentBranchGit) CurrentBranch(context.Context, string) (string, bool, error) {
	return "", false, errors.New("injected current-branch failure")
}

type statusWorktreeIndexMetadata struct {
	path        string
	modTimeNano int64
	size        int64
}

func snapshotStatusWorktreeIndexes(t *testing.T, workspace domain.Workspace) map[string]statusWorktreeIndexMetadata {
	t.Helper()
	indexes := make(map[string]statusWorktreeIndexMetadata, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		path, err := workspace.ResolveRepository(checkout.RepositoryID)
		if err != nil {
			t.Fatal(err)
		}
		gitDir := runGitValue(t, path, "rev-parse", "--git-dir")
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(path, gitDir)
		}
		indexPath := filepath.Join(gitDir, "index")
		info, err := os.Stat(indexPath)
		if err != nil {
			t.Fatalf("stat index for %q at %q: %v", checkout.RepositoryID, indexPath, err)
		}
		indexes[checkout.RepositoryID] = statusWorktreeIndexMetadata{path: indexPath, modTimeNano: info.ModTime().UnixNano(), size: info.Size()}
	}
	return indexes
}

func assertStatusWorktreeIndexesUnchanged(t *testing.T, before map[string]statusWorktreeIndexMetadata, workspace domain.Workspace) {
	t.Helper()
	after := snapshotStatusWorktreeIndexes(t, workspace)
	if len(after) != len(before) {
		t.Fatalf("status index count: before=%#v after=%#v", before, after)
	}
	for repositoryID, beforeMetadata := range before {
		if afterMetadata, found := after[repositoryID]; !found || afterMetadata != beforeMetadata {
			t.Fatalf("status mutated index metadata for %q: before=%#v after=%#v", repositoryID, beforeMetadata, afterMetadata)
		}
	}
}

func snapshotStatusWorktreeRefs(t *testing.T, workspace domain.Workspace) map[string]string {
	t.Helper()
	refs := make(map[string]string, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		path, err := workspace.ResolveRepository(checkout.RepositoryID)
		if err != nil {
			t.Fatal(err)
		}
		refs[checkout.RepositoryID] = runGitValue(t, path, "show-ref")
	}
	return refs
}

func assertStatusWorktreeRefsUnchanged(t *testing.T, before map[string]string, workspace domain.Workspace) {
	t.Helper()
	after := snapshotStatusWorktreeRefs(t, workspace)
	if len(after) != len(before) {
		t.Fatalf("status ref snapshot count: before=%#v after=%#v", before, after)
	}
	for repositoryID, beforeRefs := range before {
		if afterRefs, found := after[repositoryID]; !found || afterRefs != beforeRefs {
			t.Fatalf("status mutated refs for %q: before=%q after=%q", repositoryID, beforeRefs, afterRefs)
		}
	}
}

func newRemoteRejectingGitWrapper(t *testing.T) string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	const script = `#!/bin/sh
for argument in "$@"; do
	case "$argument" in
	fetch|ls-remote)
		exit 97
		;;
	esac
done
exec git "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

type defaultIdentityMismatchingGit struct {
	gitadapter.Git
	path     string
	identity string
}

func (git defaultIdentityMismatchingGit) CommonGitDir(ctx context.Context, repository string) (string, error) {
	observed, observedErr := filepath.EvalSymlinks(repository)
	want, wantErr := filepath.EvalSymlinks(git.path)
	if observedErr == nil && wantErr == nil && filepath.Clean(observed) == filepath.Clean(want) {
		return git.identity, nil
	}
	return git.Git.CommonGitDir(ctx, repository)
}

func TestStatusRemoteRejectingGitWrapperRejectsRemoteArgumentsAnywhere(t *testing.T) {
	wrapper := newRemoteRejectingGitWrapper(t)
	for _, arguments := range [][]string{{"fetch"}, {"-C", t.TempDir(), "ls-remote"}} {
		command := exec.Command(wrapper, arguments...)
		if err := command.Run(); err == nil {
			t.Fatalf("remote-rejecting wrapper allowed %v", arguments)
		} else if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 97 {
			t.Fatalf("remote-rejecting wrapper %v error = %v, want exit 97", arguments, err)
		}
	}
}

func runGitValue(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return strings.TrimSpace(string(output))
}

func statusFor(t *testing.T, value service.WorkspaceStatus, id string) service.RepositoryStatus {
	t.Helper()
	for _, repository := range value.Repositories {
		if repository.ID == id {
			return repository
		}
	}
	t.Fatalf("repository %q not found in %#v", id, value)
	return service.RepositoryStatus{}
}
