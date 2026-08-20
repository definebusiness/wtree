package service_test

import (
	"bytes"
	"context"
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
