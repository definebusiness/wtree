package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

// TestWorkspaceSelectionCheckoutPreconditions proves that checkout preserves
// the authority selected from the captured state generation at every mutation
// boundary. The locker injection fires after planning and while the project
// mutation lock is held, before any add-worktree step can run.
func TestWorkspaceSelectionCheckoutPreconditions(t *testing.T) {
	for _, boundary := range []string{"before-reconciliation", "under-transaction-lock"} {
		for _, change := range []string{"removal", "replacement"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				project, root, backend, data := createFixture(t)
				target := filepath.Join(t.TempDir(), "retained")
				if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
					WorkspaceName: "feature/frozen", TargetPath: target, DataDir: data,
					Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}},
				}, nil); err != nil {
					t.Fatal(err)
				}
				workspace, err := service.RequireWorkspace(project, data, "feature/frozen")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, false, nil); err != nil {
					t.Fatal(err)
				}
				selection, err := service.NewWorkspaceSelector().SelectCheckout(context.Background(), project, service.WorkspaceSelectionRequest{
					DataDir: data, Query: "frozen", Mode: service.WorkspaceSelectionSubstring, Policy: service.WorkspaceSelectionCheckout,
				})
				if err != nil || selection.Workspace == nil || selection.Workspace.Name != "feature/frozen" {
					t.Fatalf("SelectCheckout = %#v, %v", selection, err)
				}
				statePath := service.WorkspaceStatePath(data, project.ID, workspace.ID)
				mutated := 0
				mutate := func() {
					mutated++
					if change == "removal" {
						if err := os.Remove(statePath); err != nil {
							t.Fatalf("remove selected state: %v", err)
						}
						return
					}
					state, err := store.ReadWorkspace(statePath)
					if err != nil {
						t.Fatalf("read selected state for replacement: %v", err)
					}
					state.Path = filepath.Join(t.TempDir(), "replacement")
					if err := os.MkdirAll(filepath.Join(state.Path, "api"), 0o755); err != nil {
						t.Fatalf("make replacement root: %v", err)
					}
					for id, checkout := range state.Repositories {
						checkout.ResolvedPath = state.Path
						if id == "backend" {
							checkout.ResolvedPath = filepath.Join(state.Path, "api")
						}
						state.Repositories[id] = checkout
					}
					if err := store.WriteWorkspace(statePath, state); err != nil {
						t.Fatalf("replace selected state: %v", err)
					}
				}
				request := service.WorkspaceCheckoutRequest{WorkspaceName: selection.Workspace.Name, DataDir: data, SelectedWorkspace: selection.Workspace, Precondition: selection.Precondition}
				if boundary == "before-reconciliation" {
					mutate()
					err = service.NewResolver().ReconcileCheckout(context.Background(), data, project, selection.Precondition)
				} else {
					transaction := service.NewWorkspaceTransactionWith(mutatingCreateLocker{mutate: mutate}, store.WriteWorkspace, store.WriteRecovery, os.Remove)
					_, err = service.NewWorkspaceCreatorWith(gitadapter.NewAdapter("git"), transaction).CheckoutWorkspace(context.Background(), project, request, nil)
				}
				var application *service.Error
				if err == nil || !errors.As(err, &application) || application.Kind != service.ErrorConflict {
					t.Fatalf("%s stale checkout = %v, want conflict", boundary, err)
				}
				if mutated != 1 {
					t.Fatalf("%s mutation count=%d, want 1", boundary, mutated)
				}
				for _, path := range []string{target, filepath.Join(target, "api")} {
					if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatalf("%s stale authority created %q: %v", boundary, path, statErr)
					}
				}
				for _, repository := range []string{root.Path, backend.Path} {
					exists, branchErr := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository, "feature/frozen")
					if branchErr != nil || !exists {
						t.Fatalf("%s changed retained branch at %q: exists=%t err=%v", boundary, repository, exists, branchErr)
					}
				}
			})
		}
	}
}

func TestWorkspaceSelectionCheckoutExactBranchAbsencePrecondition(t *testing.T) {
	project, root, backend, data := createFixture(t)
	root.Run(t, "branch", "feature/branch-only")
	backend.Run(t, "branch", "feature/branch-only")
	selector := service.NewWorkspaceSelector()
	selection, err := selector.SelectExactBranch(context.Background(), project, data, "feature/branch-only")
	if err != nil || selection.Workspace != nil {
		t.Fatalf("SelectExactBranch = %#v, %v", selection, err)
	}
	// A state that appears after exact branch selection cannot switch the
	// target to its mounts or root at either reconciliation or execution.
	state := store.WorkspaceState{Version: store.Version, ID: "appeared", Name: "feature/branch-only", Path: filepath.Join(t.TempDir(), "appeared"), Repositories: map[string]store.CheckoutState{}}
	// Use a valid state shape after proving the exact branch selection was
	// already captured; its appearance must still be a conflict.
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), root.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(state.Path, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	state.Repositories["root"] = store.CheckoutState{Branch: "feature/branch-only", Head: head, Mount: ".", ResolvedPath: state.Path}
	backendHead, err := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
	if err != nil {
		t.Fatal(err)
	}
	state.Repositories["backend"] = store.CheckoutState{Branch: "feature/branch-only", Head: backendHead, Mount: "api", ResolvedPath: filepath.Join(state.Path, "api")}
	if err := store.WriteWorkspace(service.WorkspaceStatePath(data, project.ID, state.ID), state); err != nil {
		t.Fatal(err)
	}
	err = service.NewResolver().ReconcileCheckout(context.Background(), data, project, selection.Precondition)
	var application *service.Error
	if err == nil || !errors.As(err, &application) || application.Kind != service.ErrorConflict {
		t.Fatalf("appeared exact state reconciliation = %v, want conflict", err)
	}
}

func TestWorkspaceSelectionCheckoutExactBranchAppearanceUnderLock(t *testing.T) {
	project, root, backend, data := createFixture(t)
	root.Run(t, "branch", "feature/appears-under-lock")
	backend.Run(t, "branch", "feature/appears-under-lock")
	selection, err := service.NewWorkspaceSelector().SelectExactBranch(context.Background(), project, data, "feature/appears-under-lock")
	if err != nil || selection.Workspace != nil {
		t.Fatalf("SelectExactBranch = %#v, %v", selection, err)
	}
	target := filepath.Join(t.TempDir(), "branch-target")
	appeared := 0
	transaction := service.NewWorkspaceTransactionWith(mutatingCreateLocker{mutate: func() {
		appeared++
		head, headErr := gitadapter.NewAdapter("git").Head(context.Background(), root.Path)
		if headErr != nil {
			t.Fatalf("root head: %v", headErr)
		}
		backendHead, headErr := gitadapter.NewAdapter("git").Head(context.Background(), backend.Path)
		if headErr != nil {
			t.Fatalf("backend head: %v", headErr)
		}
		stateRoot := filepath.Join(t.TempDir(), "appeared")
		if err := os.MkdirAll(filepath.Join(stateRoot, "api"), 0o755); err != nil {
			t.Fatalf("make appeared state root: %v", err)
		}
		if err := store.WriteWorkspace(service.WorkspaceStatePath(data, project.ID, "appeared-under-lock"), store.WorkspaceState{
			Version: store.Version, ID: "appeared-under-lock", Name: "feature/appears-under-lock", Path: stateRoot,
			Repositories: map[string]store.CheckoutState{
				"root":    {Branch: "feature/appears-under-lock", Head: head, Mount: ".", ResolvedPath: stateRoot},
				"backend": {Branch: "feature/appears-under-lock", Head: backendHead, Mount: "api", ResolvedPath: filepath.Join(stateRoot, "api")},
			},
		}); err != nil {
			t.Fatalf("write appeared state: %v", err)
		}
	}}, store.WriteWorkspace, store.WriteRecovery, os.Remove)
	_, err = service.NewWorkspaceCreatorWith(gitadapter.NewAdapter("git"), transaction).CheckoutWorkspace(context.Background(), project, service.WorkspaceCheckoutRequest{
		WorkspaceName: "feature/appears-under-lock", TargetPath: target, DataDir: data, Precondition: selection.Precondition,
	}, nil)
	var application *service.Error
	if err == nil || !errors.As(err, &application) || application.Kind != service.ErrorConflict {
		t.Fatalf("branch-only appearance under lock = %v, want conflict", err)
	}
	if appeared != 1 {
		t.Fatalf("appearance injection count=%d, want 1", appeared)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("appeared state retargeted exact branch checkout to %q: %v", target, statErr)
	}
}

func TestWorkspaceSelectionCheckoutRestoresForestCompanionCanonicalTarget(t *testing.T) {
	project, root, backend, shared, data := createThreeLevelFixture(t)
	for index := range project.Repositories {
		if project.Repositories[index].ID == "backend" {
			project.Repositories[index].Companion = true
			project.Repositories[index].DefaultBranch = "main"
		}
	}
	target := filepath.Join(t.TempDir(), "forest")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{
		WorkspaceName: "feature/forest-canonical", TargetPath: target, DataDir: data,
		Mounts: []service.MountOverride{{RepositoryID: "backend", Mount: "api"}, {RepositoryID: "shared", Mount: "common"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/forest-canonical")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, false, nil); err != nil {
		t.Fatal(err)
	}
	selector := service.NewWorkspaceSelector()
	shorthand, err := selector.SelectCheckout(context.Background(), project, service.WorkspaceSelectionRequest{DataDir: data, Query: "forest-can", Mode: service.WorkspaceSelectionSubstring, Policy: service.WorkspaceSelectionCheckout})
	if err != nil || shorthand.Workspace == nil || shorthand.Workspace.Name != "feature/forest-canonical" {
		t.Fatalf("shorthand forest selection = %#v, %v", shorthand, err)
	}
	exact, err := selector.SelectCheckout(context.Background(), project, service.WorkspaceSelectionRequest{DataDir: data, Query: workspace.ID, Mode: service.WorkspaceSelectionExact, Policy: service.WorkspaceSelectionCheckout})
	if err != nil || exact.Workspace == nil || exact.Workspace.Name != shorthand.Workspace.Name || exact.Workspace.RootPath != shorthand.Workspace.RootPath {
		t.Fatalf("exact ID forest selection = %#v, %v; shorthand=%#v", exact, err, shorthand)
	}
	planValue, err := service.NewWorkspaceCreator().PlanCheckout(context.Background(), project, service.WorkspaceCheckoutRequest{WorkspaceName: shorthand.Workspace.Name, DataDir: data, SelectedWorkspace: shorthand.Workspace, Precondition: shorthand.Precondition})
	if err != nil || planValue.WorkspaceName != "feature/forest-canonical" || planValue.RootPath != target {
		t.Fatalf("shorthand forest plan = %#v, %v", planValue, err)
	}
	if _, err := service.NewWorkspaceCreator().CheckoutWorkspace(context.Background(), project, service.WorkspaceCheckoutRequest{WorkspaceName: shorthand.Workspace.Name, DataDir: data, SelectedWorkspace: shorthand.Workspace, Precondition: shorthand.Precondition}, nil); err != nil {
		t.Fatalf("shorthand forest checkout = %v", err)
	}
	for _, path := range []string{target, filepath.Join(target, "api"), filepath.Join(target, "api", "common")} {
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("restored forest checkout %q: %v", path, err)
		}
	}
	for _, repository := range []string{root.Path, backend.Path, shared.Path} {
		exists, err := gitadapter.NewAdapter("git").BranchExists(context.Background(), repository, "feature/forest-canonical")
		if err != nil || !exists {
			t.Fatalf("retained forest branch at %q: exists=%t err=%v", repository, exists, err)
		}
	}
}

func TestWorkspaceSelectionCheckoutRestoresPlainMultiTopLevelForest(t *testing.T) {
	logicalRoot := t.TempDir()
	api, web := testutil.NewPushedGitRepository(t), testutil.NewPushedGitRepository(t)
	api.CommitFile("api.txt", "api\n", "api")
	web.CommitFile("web.txt", "web\n", "web")
	apiPath, webPath := filepath.Join(logicalRoot, "services", "api"), filepath.Join(logicalRoot, "clients", "web")
	if err := os.MkdirAll(filepath.Dir(apiPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(webPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(api.Path, apiPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(web.Path, webPath); err != nil {
		t.Fatal(err)
	}
	api.Path, web.Path = apiPath, webPath
	data := t.TempDir()
	initialized, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: logicalRoot, DataDir: data, BaseRepository: "api"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.NewResolver().ResolveProject(context.Background(), service.ResolveRequest{Path: logicalRoot, ProjectPath: initialized.ConfigPath, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "plain-forest")
	if _, err := service.NewWorkspaceCreator().Create(context.Background(), project, service.WorkspacePlanRequest{WorkspaceName: "feature/plain-forest", TargetPath: target, DataDir: data, Mounts: []service.MountOverride{{RepositoryID: "api", Mount: "services/api"}, {RepositoryID: "web", Mount: "clients/web"}}}, nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.RequireWorkspace(project, data, "feature/plain-forest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewWorkspaceRemover().Remove(context.Background(), project, workspace, data, false, nil); err != nil {
		t.Fatal(err)
	}
	selector := service.NewWorkspaceSelector()
	shorthand, err := selector.SelectCheckout(context.Background(), project, service.WorkspaceSelectionRequest{DataDir: data, Query: "plain-for", Mode: service.WorkspaceSelectionSubstring, Policy: service.WorkspaceSelectionCheckout})
	if err != nil || shorthand.Workspace == nil || shorthand.Workspace.Name != "feature/plain-forest" {
		t.Fatalf("plain forest shorthand = %#v, %v", shorthand, err)
	}
	exact, err := selector.SelectCheckout(context.Background(), project, service.WorkspaceSelectionRequest{DataDir: data, Query: workspace.ID, Mode: service.WorkspaceSelectionExact, Policy: service.WorkspaceSelectionCheckout})
	if err != nil || exact.Workspace == nil || exact.Workspace.RootPath != shorthand.Workspace.RootPath {
		t.Fatalf("plain forest exact ID = %#v, %v", exact, err)
	}
	if _, err := service.NewWorkspaceCreator().CheckoutWorkspace(context.Background(), project, service.WorkspaceCheckoutRequest{WorkspaceName: shorthand.Workspace.Name, DataDir: data, SelectedWorkspace: shorthand.Workspace, Precondition: shorthand.Precondition}, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(target, "services", "api"), filepath.Join(target, "clients", "web")} {
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			t.Fatalf("restored plain forest %q: %v", path, err)
		}
	}
}
