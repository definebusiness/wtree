package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

// This regression was added after the explicit logical-root implementation;
// it is first-run GREEN coverage rather than a reconstructed RED.
func TestWorkspaceImporterPlansExplicitPlainForestLogicalRoot(t *testing.T) {
	project, createData := forestWorkspaceProject(t)
	root := filepath.Join(t.TempDir(), "plain logical root")
	request := forestWorkspaceRequest(root, createData, "feature/import-forest")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, request, nil); err != nil {
		t.Fatalf("create forest workspace: %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize logical root: %v", err)
	}
	data := t.TempDir()
	plan, err := NewWorkspaceImporter().PlanImport(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data})
	if err != nil {
		t.Fatalf("plan import: %v", err)
	}
	if plan.LogicalRoot != canonicalRoot || plan.RootPath != canonicalRoot || plan.BaseRepository != "api" || len(plan.Repositories) != 5 {
		t.Fatalf("import plan topology = %#v", plan)
	}
	wantIDs := []string{"api", "web", "alpha", "beta", "gamma"}
	wantMounts := []string{"services/api", "grouped/web", "components/alpha", "deep/beta", "tools/gamma"}
	wantParents := []string{"", "", "api", "alpha", "beta"}
	for index, repository := range plan.Repositories {
		if repository.ID != wantIDs[index] || repository.ParentID != wantParents[index] || repository.Mount != wantMounts[index] || repository.Path != repository.ResolvedPath {
			t.Fatalf("repository %d = %#v", index, repository)
		}
	}
	if _, err := NewWorkspaceImporter().Import(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data}); err != nil {
		t.Fatalf("import: %v", err)
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, plan.WorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Path != canonicalRoot || len(state.Repositories) != len(plan.Repositories) {
		t.Fatalf("state = %#v", state)
	}
	for _, repository := range plan.Repositories {
		checkout, found := state.Repositories[repository.ID]
		if !found || checkout.Mount != repository.Mount || checkout.ResolvedPath != repository.ResolvedPath {
			t.Fatalf("state checkout %q = %#v", repository.ID, checkout)
		}
	}
}

func TestWorkspaceImporterAppliesSharedDiscoveryIgnores(t *testing.T) {
	project, _ := rootWorkspaceProject(t)
	project.DiscoveryIgnores = []string{"generated/**"}
	data := t.TempDir()
	placeUnknown := func(relative string) {
		t.Helper()
		unknown := filepath.Join(project.LogicalRoot, filepath.FromSlash(relative))
		foreign := testutil.NewGitRepository(t)
		if err := os.MkdirAll(filepath.Dir(unknown), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(foreign.Path, unknown); err != nil {
			t.Fatalf("place unknown checkout %q: %v", relative, err)
		}
	}
	placeUnknown("node_modules/unrelated") // built-in default
	placeUnknown("generated/cache")        // strict-local configured glob
	if _, err := NewWorkspaceImporter().PlanImport(context.Background(), project, ImportRequest{Path: project.LogicalRoot, Name: "imported", DataDir: data}); err != nil {
		t.Fatalf("PlanImport() with ignored checkouts: %v", err)
	}
	placeUnknown("outside")
	if _, err := NewWorkspaceImporter().PlanImport(context.Background(), project, ImportRequest{Path: project.LogicalRoot, Name: "imported", DataDir: data}); err == nil {
		t.Fatal("PlanImport() succeeded for unknown checkout outside ignored paths")
	}
}

func TestWorkspaceImporterInfersPlainForestLogicalRootFromCheckoutContexts(t *testing.T) {
	project, createData := forestWorkspaceProject(t)
	project = forestWorkspaceProjectWithGroupedMounts(project)
	root := filepath.Join(t.TempDir(), "plain logical root")
	request := forestWorkspaceRequest(root, createData, "feature/import-context")
	if _, err := NewWorkspaceCreator().Create(context.Background(), project, request, nil); err != nil {
		t.Fatalf("create forest workspace: %v", err)
	}
	data := t.TempDir()
	importer := NewWorkspaceImporter()
	explicit, err := importer.PlanImport(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data})
	if err != nil {
		t.Fatalf("plan explicit logical root: %v", err)
	}
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "base", path: filepath.Join(root, "services", "api")},
		{name: "sibling", path: filepath.Join(root, "grouped", "web")},
		{name: "nested", path: filepath.Join(root, "services", "api", "components", "alpha")},
		{name: "deepest nested", path: filepath.Join(root, "services", "api", "components", "alpha", "deep", "beta", "tools", "gamma")},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := importer.PlanImport(context.Background(), project, ImportRequest{Path: test.path, Name: "imported", DataDir: data})
			if err != nil {
				t.Fatalf("plan import from %q: %v", test.path, err)
			}
			if !importPlansEqual(explicit, value) {
				t.Fatalf("plan from %q = %#v, want %#v", test.path, value, explicit)
			}
		})
	}
}

func forestWorkspaceProjectWithGroupedMounts(project domain.Project) domain.Project {
	mounts := map[string]string{
		"api":   "services/api",
		"web":   "grouped/web",
		"alpha": "components/alpha",
		"beta":  "deep/beta",
		"gamma": "tools/gamma",
	}
	for index := range project.Repositories {
		project.Repositories[index].DefaultMount = mounts[project.Repositories[index].ID]
	}
	return project
}

func TestWorkspaceImporterRejectsContradictoryPlainForestObservations(t *testing.T) {
	for _, test := range []struct {
		name         string
		allowPartial bool
		mutate       func(*testing.T, domain.Project, string, map[string]string)
		missing      []string
	}{
		{
			name:    "missing leaf requires and permits only explicit partial import",
			missing: []string{"gamma"},
			mutate: func(t *testing.T, project domain.Project, _ string, paths map[string]string) {
				t.Helper()
				if err := gitadapter.NewAdapter("git").RemoveWorktree(context.Background(), repositoryByID(t, project, "gamma").SourcePath, paths["gamma"], true); err != nil {
					t.Fatalf("remove leaf worktree: %v", err)
				}
			},
		},
		{
			name:    "missing top level requires and permits only explicit partial import",
			missing: []string{"web"},
			mutate: func(t *testing.T, project domain.Project, _ string, paths map[string]string) {
				t.Helper()
				if err := gitadapter.NewAdapter("git").RemoveWorktree(context.Background(), repositoryByID(t, project, "web").SourcePath, paths["web"], true); err != nil {
					t.Fatalf("remove top-level worktree: %v", err)
				}
			},
		},
		{
			name: "observed child without declared parent is not partial",
			mutate: func(t *testing.T, project domain.Project, root string, paths map[string]string) {
				t.Helper()
				orphan := filepath.Join(root, "orphan-alpha")
				if err := os.Rename(paths["alpha"], orphan); err != nil {
					t.Fatalf("move child aside: %v", err)
				}
				if err := gitadapter.NewAdapter("git").RemoveWorktree(context.Background(), repositoryByID(t, project, "api").SourcePath, paths["api"], true); err != nil {
					t.Fatalf("remove declared parent: %v", err)
				}
			},
			allowPartial: true,
		},
		{
			name: "unknown checkout is not partial",
			mutate: func(t *testing.T, _ domain.Project, root string, _ map[string]string) {
				t.Helper()
				unknown := testutil.NewPushedGitRepository(t)
				unknown.CommitFile("unknown.txt", "unknown\n", "unknown")
				if err := os.Rename(unknown.Path, filepath.Join(root, "unknown")); err != nil {
					t.Fatalf("place unknown checkout: %v", err)
				}
			},
			allowPartial: true,
		},
		{
			name: "duplicate configured identity is not partial",
			mutate: func(t *testing.T, project domain.Project, root string, _ map[string]string) {
				t.Helper()
				repository := repositoryByID(t, project, "api")
				adapter := gitadapter.NewAdapter("git")
				head, err := adapter.Head(context.Background(), repository.SourcePath)
				if err != nil {
					t.Fatal(err)
				}
				if err := adapter.CreateBranch(context.Background(), repository.SourcePath, "feature/import-duplicate", head); err != nil {
					t.Fatal(err)
				}
				if err := adapter.AddWorktree(context.Background(), repository.SourcePath, filepath.Join(root, "duplicate-api"), "feature/import-duplicate"); err != nil {
					t.Fatal(err)
				}
			},
			allowPartial: true,
		},
		{
			name: "symlinked declared checkout is contradictory not partial",
			mutate: func(t *testing.T, _ domain.Project, _ string, paths map[string]string) {
				t.Helper()
				external := testutil.NewPushedGitRepository(t)
				external.CommitFile("external.txt", "external\n", "external")
				if err := os.Rename(paths["gamma"], paths["gamma"]+"-displaced"); err != nil {
					t.Fatalf("displace checkout: %v", err)
				}
				if err := os.Symlink(external.Path, paths["gamma"]); err != nil {
					t.Fatalf("install external checkout symlink: %v", err)
				}
			},
			allowPartial: true,
		},
		{
			name: "nested checkout under wrong parent is contradictory",
			mutate: func(t *testing.T, _ domain.Project, _ string, paths map[string]string) {
				t.Helper()
				wrongPath := filepath.Join(paths["web"], "components", "alpha")
				if err := os.MkdirAll(filepath.Dir(wrongPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(paths["alpha"], wrongPath); err != nil {
					t.Fatalf("move child under wrong parent: %v", err)
				}
			},
			allowPartial: true,
		},
		{
			name: "known checkout cannot invert declared mount chain",
			mutate: func(t *testing.T, _ domain.Project, root string, paths map[string]string) {
				t.Helper()
				wrongPath := filepath.Join(root, "misplaced-api")
				if err := os.Rename(paths["api"], wrongPath); err != nil {
					t.Fatalf("move top-level checkout: %v", err)
				}
				paths["start"] = wrongPath
			},
			allowPartial: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, root, paths := importForestWorkspace(t)
			test.mutate(t, project, root, paths)
			path := root
			if paths["start"] != "" {
				path = paths["start"]
			}
			if len(test.missing) > 0 {
				requiredData := t.TempDir()
				if value, err := NewWorkspaceImporter().Import(context.Background(), project, ImportRequest{Path: path, Name: "required", DataDir: requiredData}); err == nil {
					t.Fatalf("full import unexpectedly succeeded: %#v", value)
				}
				assertNoImportedWorkspaceState(t, requiredData, project.ID)

				data := t.TempDir()
				value, err := NewWorkspaceImporter().Import(context.Background(), project, ImportRequest{Path: path, Name: "imported", AllowPartial: true, DataDir: data})
				if err != nil {
					t.Fatalf("partial import: %v", err)
				}
				if !value.Partial || !sameStringSlice(value.MissingRepositoryIDs, test.missing) {
					t.Fatalf("partial import = %#v", value)
				}
				state, stateErr := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, value.WorkspaceID))
				if stateErr != nil || !state.Partial || !sameStringSlice(state.MissingRepositoryIDs, test.missing) {
					t.Fatalf("partial state = %#v, %v", state, stateErr)
				}
				return
			}
			data := t.TempDir()
			value, err := NewWorkspaceImporter().Import(context.Background(), project, ImportRequest{Path: path, Name: "imported", AllowPartial: test.allowPartial, DataDir: data})
			if err == nil {
				t.Fatalf("import unexpectedly succeeded: %#v", value)
			}
			assertNoImportedWorkspaceState(t, data, project.ID)
		})
	}
}

func importForestWorkspace(t *testing.T) (domain.Project, string, map[string]string) {
	t.Helper()
	project, createData := forestWorkspaceProject(t)
	project = forestWorkspaceProjectWithGroupedMounts(project)
	root := filepath.Join(t.TempDir(), "plain logical root")
	value, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(root, createData, "feature/import-matrix"), nil)
	if err != nil {
		t.Fatalf("create forest workspace: %v", err)
	}
	paths := make(map[string]string, len(value.Repositories))
	for _, repository := range value.Repositories {
		paths[repository.ID] = repository.Path
	}
	return project, root, paths
}

func repositoryByID(t *testing.T, project domain.Project, id string) domain.Repository {
	t.Helper()
	for _, repository := range project.Repositories {
		if repository.ID == id {
			return repository
		}
	}
	t.Fatalf("repository %q is not configured", id)
	return domain.Repository{}
}

func assertNoImportedWorkspaceState(t *testing.T, data, projectID string) {
	t.Helper()
	if _, err := os.Stat(WorkspaceStateDirectory(data, projectID)); !os.IsNotExist(err) {
		t.Fatalf("import wrote workspace state directory: %v", err)
	}
}

func TestWorkspaceImporterForestPublicationFailuresLeaveNoState(t *testing.T) {
	project, root, paths := importForestWorkspace(t)
	t.Run("plan import is a dry run", func(t *testing.T) {
		data := t.TempDir()
		if _, err := NewWorkspaceImporter().PlanImport(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data}); err != nil {
			t.Fatal(err)
		}
		assertNoImportArtifacts(t, data, project.ID)
	})
	t.Run("already canceled", func(t *testing.T) {
		data := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewWorkspaceImporter().Import(ctx, project, ImportRequest{Path: root, Name: "imported", DataDir: data})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled import error = %v", err)
		}
		assertNoImportArtifacts(t, data, project.ID)
	})
	t.Run("in flight observation cancellation", func(t *testing.T) {
		data := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		blocking := &importBlockingGit{Git: gitadapter.NewAdapter("git"), path: paths["web"], entered: make(chan struct{})}
		importer := NewWorkspaceImporterWith(blocking, lock.Manager{}, store.WriteWorkspace)
		done := make(chan error, 1)
		go func() {
			_, err := importer.Import(ctx, project, ImportRequest{Path: root, Name: "imported", DataDir: data})
			done <- err
		}()
		select {
		case <-blocking.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight observation did not reach non-base checkout")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("in-flight canceled import error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight import did not return after cancellation")
		}
		assertNoImportArtifacts(t, data, project.ID)
	})
	t.Run("lock contention", func(t *testing.T) {
		data := t.TempDir()
		importer := NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), importContendedLocker{}, store.WriteWorkspace)
		_, err := importer.Import(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data})
		assertImportFailureKind(t, err, ErrorConflict)
		assertNoImportArtifacts(t, data, project.ID)
	})
	t.Run("locked reobservation rejects changed sibling", func(t *testing.T) {
		project, root, paths := importForestWorkspace(t)
		data := t.TempDir()
		locker := &importMutatingLocker{mutate: func() {
			if err := gitadapter.NewAdapter("git").RemoveWorktree(context.Background(), repositoryByID(t, project, "web").SourcePath, paths["web"], true); err != nil {
				t.Fatalf("remove sibling during lock acquisition: %v", err)
			}
		}}
		importer := NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), locker, store.WriteWorkspace)
		_, err := importer.Import(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data})
		assertImportFailureKind(t, err, ErrorValidation)
		assertNoImportArtifacts(t, data, project.ID)
	})
	t.Run("concurrent same import has one publication", func(t *testing.T) {
		data := t.TempDir()
		start := make(chan struct{})
		results := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				_, err := NewWorkspaceImporter().Import(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data})
				results <- err
			}()
		}
		close(start)
		var successes, conflicts int
		for range 2 {
			err := <-results
			if err == nil {
				successes++
				continue
			}
			var application *Error
			if errors.As(err, &application) && application.Kind == ErrorConflict {
				conflicts++
				continue
			}
			t.Fatalf("concurrent import error = %v", err)
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("concurrent import outcomes = %d successes, %d conflicts", successes, conflicts)
		}
		state, err := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, "imported-5f54227b74fbba7743c47cd286b4873f"))
		if err != nil || len(state.Repositories) != len(project.Repositories) || state.Path == "" {
			t.Fatalf("concurrent imported state = %#v, %v", state, err)
		}
	})
	t.Run("state writer failure", func(t *testing.T) {
		data := t.TempDir()
		importer := NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), lock.Manager{}, func(string, store.WorkspaceState) error {
			return errors.New("injected state writer failure")
		})
		if _, err := importer.Import(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data}); err == nil {
			t.Fatal("state-writer failure import succeeded")
		}
		assertNoImportArtifacts(t, data, project.ID)
	})
	t.Run("canceled after lock acquisition", func(t *testing.T) {
		data := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		importer := NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), importCancelingLocker{cancel: cancel}, store.WriteWorkspace)
		_, err := importer.Import(ctx, project, ImportRequest{Path: root, Name: "imported", DataDir: data})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("post-lock cancellation error = %v", err)
		}
		assertNoImportArtifacts(t, data, project.ID)
	})
}

func TestWorkspaceImporterStatePublicationReceiptsPreserveUnownedGenerations(t *testing.T) {
	project, _ := rootWorkspaceProject(t)
	request := func(data, name string) ImportRequest {
		return ImportRequest{Path: project.LogicalRoot, Name: name, DataDir: data}
	}

	t.Run("post replacement error removes exact owned state", func(t *testing.T) {
		data := t.TempDir()
		writer := importWorkspaceWriterFor(func(path string, state store.WorkspaceState) error {
			encoded, err := store.WorkspaceBytes(state)
			if err != nil {
				return err
			}
			return fsutil.WriteFileAtomicModeWithHook(path, encoded, 0o600, func(step string) error {
				if step == "dir-sync" {
					return errors.New("injected directory sync failure")
				}
				return nil
			})
		})
		importer := newWorkspaceImporterWithDependencies(workspaceImporterDependencies{Git: gitadapter.NewAdapter("git"), Locker: lock.Manager{}, WriteWorkspace: writer})
		name := "post-replacement"
		_, err := importer.Import(context.Background(), project, request(data, name))
		assertImportFailureKind(t, err, ErrorInternal)
		if _, statErr := os.Lstat(WorkspaceStatePath(data, project.ID, pathutil.StorageName(name))); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("exact state generation remains: %v", statErr)
		}
		if _, statErr := os.Lstat(importRecoveryRecordPath(data, project.ID, pathutil.StorageName(name))); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unexpected recovery remains: %v", statErr)
		}
		if _, err := NewWorkspaceImporter().Import(context.Background(), project, request(data, name)); err != nil {
			t.Fatalf("retry after exact cleanup: %v", err)
		}
	})

	t.Run("pre replacement error has no artifacts", func(t *testing.T) {
		data := t.TempDir()
		importer := NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), lock.Manager{}, func(string, store.WorkspaceState) error {
			return errors.New("injected pre-replacement failure")
		})
		_, err := importer.Import(context.Background(), project, request(data, "pre-replacement"))
		assertImportFailureKind(t, err, ErrorInternal)
		assertNoImportArtifacts(t, data, project.ID)
	})

	t.Run("erroring writer without receipt preserves state and records recovery", func(t *testing.T) {
		data := t.TempDir()
		name := "missing-receipt"
		importer := NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), lock.Manager{}, func(path string, state store.WorkspaceState) error {
			if err := store.WriteWorkspace(path, state); err != nil {
				return err
			}
			return errors.New("writer lost its publication receipt")
		})
		_, err := importer.Import(context.Background(), project, request(data, name))
		assertImportFailureKind(t, err, ErrorRollbackIncomplete)
		assertImportRecovery(t, data, project.ID, pathutil.StorageName(name))
		if _, readErr := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, pathutil.StorageName(name))); readErr != nil {
			t.Fatalf("uncertain state was removed: %v", readErr)
		}
	})

	t.Run("cleanup failure preserves owned state and records recovery", func(t *testing.T) {
		data := t.TempDir()
		name := "cleanup-failure"
		baseWriter := importWorkspaceWriterFor(store.WriteWorkspace)
		writer := func(path string, state store.WorkspaceState) (importStateReceipt, error) {
			receipt, err := baseWriter(path, state)
			return receipt, errors.Join(err, errors.New("injected post-write failure"))
		}
		importer := newWorkspaceImporterWithDependencies(workspaceImporterDependencies{
			Git: gitadapter.NewAdapter("git"), Locker: lock.Manager{}, WriteWorkspace: writer,
			RemoveFileCAS: func(string, func() error) error { return errors.New("injected exact cleanup failure") },
		})
		_, err := importer.Import(context.Background(), project, request(data, name))
		assertImportFailureKind(t, err, ErrorRollbackIncomplete)
		assertImportRecovery(t, data, project.ID, pathutil.StorageName(name))
		if _, readErr := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, pathutil.StorageName(name))); readErr != nil {
			t.Fatalf("owned state was removed after cleanup failure: %v", readErr)
		}
	})

	t.Run("concurrent replacement at cleanup is preserved", func(t *testing.T) {
		data := t.TempDir()
		name := "concurrent-replacement"
		workspaceID := pathutil.StorageName(name)
		attacker := store.WorkspaceState{Version: store.Version, ID: workspaceID, Name: "attacker", Path: t.TempDir(), Repositories: map[string]store.CheckoutState{}}
		baseWriter := importWorkspaceWriterFor(store.WriteWorkspace)
		writer := func(path string, state store.WorkspaceState) (importStateReceipt, error) {
			receipt, err := baseWriter(path, state)
			return receipt, errors.Join(err, errors.New("injected post-write failure"))
		}
		importer := newWorkspaceImporterWithDependencies(workspaceImporterDependencies{
			Git: gitadapter.NewAdapter("git"), Locker: lock.Manager{}, WriteWorkspace: writer,
			RemoveFileCAS: func(path string, compare func() error) error {
				if err := store.WriteWorkspace(path, attacker); err != nil {
					return err
				}
				return compare()
			},
		})
		_, err := importer.Import(context.Background(), project, request(data, name))
		assertImportFailureKind(t, err, ErrorRollbackIncomplete)
		assertImportRecovery(t, data, project.ID, workspaceID)
		actual, readErr := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, workspaceID))
		if readErr != nil || actual.Name != attacker.Name || actual.Path != attacker.Path {
			t.Fatalf("concurrent state = %#v, %v", actual, readErr)
		}
	})

	t.Run("recovery write failure is surfaced without deletion", func(t *testing.T) {
		data := t.TempDir()
		name := "recovery-failure"
		workspaceID := pathutil.StorageName(name)
		writer := func(path string, state store.WorkspaceState) (importStateReceipt, error) {
			if err := store.WriteWorkspace(path, state); err != nil {
				return importStateReceipt{}, err
			}
			return importStateReceipt{}, errors.New("injected missing receipt")
		}
		importer := newWorkspaceImporterWithDependencies(workspaceImporterDependencies{
			Git: gitadapter.NewAdapter("git"), Locker: lock.Manager{}, WriteWorkspace: writer,
			WriteRecoveryCAS: func(string, store.RecoveryRecord, func() error) error {
				return errors.New("injected recovery write failure")
			},
		})
		_, err := importer.Import(context.Background(), project, request(data, name))
		assertImportFailureKind(t, err, ErrorRollbackIncomplete)
		if !strings.Contains(err.Error(), "injected recovery write failure") {
			t.Fatalf("recovery failure was not surfaced: %v", err)
		}
		if _, readErr := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, workspaceID)); readErr != nil {
			t.Fatalf("uncertain state was removed: %v", readErr)
		}
	})
}

func assertImportRecovery(t *testing.T, data, projectID, workspaceID string) {
	t.Helper()
	recovery, err := store.ReadRecovery(importRecoveryRecordPath(data, projectID, workspaceID))
	if err != nil {
		t.Fatalf("read import recovery: %v", err)
	}
	if recovery.Operation != "import" || recovery.FailedStep != "commit-state" || len(recovery.UnrevertedSteps) != 1 || recovery.UnrevertedSteps[0] != "commit-state" || len(recovery.RollbackFailures) != 1 || recovery.RollbackFailures[0].Step != "commit-state" {
		t.Fatalf("import recovery = %#v", recovery)
	}
}

func assertNoImportArtifacts(t *testing.T, data, projectID string) {
	t.Helper()
	assertNoImportedWorkspaceState(t, data, projectID)
	if _, err := os.Stat(filepath.Join(data, "projects", projectID, "recovery")); !os.IsNotExist(err) {
		t.Fatalf("import wrote recovery artifacts: %v", err)
	}
}

func assertImportFailureKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var application *Error
	if !errors.As(err, &application) || application.Kind != want {
		t.Fatalf("import error = %v, want %v", err, want)
	}
}

type importBlockingGit struct {
	gitadapter.Git
	path    string
	entered chan struct{}
	once    sync.Once
}

func (g *importBlockingGit) CommonGitDir(ctx context.Context, repository string) (string, error) {
	if sameCheckoutPath(repository, g.path) {
		g.once.Do(func() { close(g.entered) })
		<-ctx.Done()
		return "", ctx.Err()
	}
	return g.Git.CommonGitDir(ctx, repository)
}

type importContendedLocker struct{}

func (importContendedLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	return nil, errors.New("held by another import")
}

type importMutatingLocker struct {
	mutate func()
	once   sync.Once
}

func (l *importMutatingLocker) ProjectLock(ctx context.Context, data, projectID string, timeout time.Duration) (lock.Handle, error) {
	l.once.Do(l.mutate)
	return lock.Manager{}.ProjectLock(ctx, data, projectID, timeout)
}

type importCancelingLocker struct{ cancel context.CancelFunc }

func (l importCancelingLocker) ProjectLock(ctx context.Context, data, projectID string, timeout time.Duration) (lock.Handle, error) {
	handle, err := (lock.Manager{}).ProjectLock(ctx, data, projectID, timeout)
	if err == nil {
		l.cancel()
	}
	return handle, err
}

func TestWorkspaceImporterForestResolvesEveryImportedContextWithoutMutation(t *testing.T) {
	project, registeredData := forestWorkspaceProject(t)
	creationData := t.TempDir()
	root := filepath.Join(t.TempDir(), "imported plain logical root")
	created, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(root, creationData, "feature/import-resolve"), nil)
	if err != nil {
		t.Fatalf("create importable forest workspace: %v", err)
	}
	imported, err := NewWorkspaceImporter().Import(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: registeredData})
	if err != nil {
		t.Fatalf("import forest workspace: %v", err)
	}
	statePath := WorkspaceStatePath(registeredData, project.ID, imported.WorkspaceID)
	registryPath := filepath.Join(registeredData, "registry.json")
	registryBefore, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]string, len(created.Repositories))
	for _, repository := range created.Repositories {
		paths[repository.ID] = repository.Path
	}
	subdirectory := filepath.Join(paths["gamma"], "inside")
	if err := os.Mkdir(subdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver()
	for _, test := range []struct {
		name       string
		path       string
		repository string
	}{
		{name: "logical root", path: root},
		{name: "base", path: paths["api"], repository: "api"},
		{name: "sibling", path: paths["web"], repository: "web"},
		{name: "alpha", path: paths["alpha"], repository: "alpha"},
		{name: "beta", path: paths["beta"], repository: "beta"},
		{name: "gamma", path: paths["gamma"], repository: "gamma"},
		{name: "inside gamma", path: subdirectory, repository: "gamma"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := ResolveRequest{Path: test.path, DataDir: registeredData}
			resolvedProject, err := resolver.ResolveProject(context.Background(), request)
			if err != nil {
				t.Fatalf("ResolveProject: %v", err)
			}
			readOnly, err := resolver.ResolveReadOnly(context.Background(), request)
			if err != nil {
				t.Fatalf("ResolveReadOnly: %v", err)
			}
			normal, err := resolver.Resolve(context.Background(), request)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			for _, value := range []domain.Project{resolvedProject, readOnly.Project, normal.Project} {
				if value.ID != project.ID || value.BaseRepository != project.BaseRepository || value.LogicalRoot != project.LogicalRoot {
					t.Fatalf("resolved project = %#v", value)
				}
			}
			for _, value := range []domain.Workspace{readOnly.Workspace, normal.Workspace} {
				if value.ID != imported.WorkspaceID || value.RootPath != state.Path {
					t.Fatalf("resolved workspace = %#v", value)
				}
				for repositoryID, checkout := range state.Repositories {
					path, pathErr := value.ResolveRepository(repositoryID)
					if pathErr != nil || path != checkout.ResolvedPath {
						t.Fatalf("ResolveRepository(%q) = %q, %v; want %q", repositoryID, path, pathErr, checkout.ResolvedPath)
					}
				}
			}
			if readOnly.RepositoryID != test.repository || normal.RepositoryID != test.repository {
				t.Fatalf("repository selection = read-only %q, normal %q; want %q", readOnly.RepositoryID, normal.RepositoryID, test.repository)
			}
		})
	}
	registryAfter, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(registryBefore, registryAfter) || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("consistent resolution mutated registry or imported workspace state")
	}
}

func TestWorkspaceImporterPartialForestResolvesObservedCheckoutOnly(t *testing.T) {
	project, registeredData := forestWorkspaceProject(t)
	creationData := t.TempDir()
	root := filepath.Join(t.TempDir(), "partial imported logical root")
	created, err := NewWorkspaceCreator().Create(context.Background(), project, forestWorkspaceRequest(root, creationData, "feature/import-partial"), nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]string, len(created.Repositories))
	for _, repository := range created.Repositories {
		paths[repository.ID] = repository.Path
	}
	if err := gitadapter.NewAdapter("git").RemoveWorktree(context.Background(), repositoryByID(t, project, "gamma").SourcePath, paths["gamma"], true); err != nil {
		t.Fatal(err)
	}
	imported, err := NewWorkspaceImporter().Import(context.Background(), project, ImportRequest{Path: root, Name: "partial", AllowPartial: true, DataDir: registeredData})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().ResolveReadOnly(context.Background(), ResolveRequest{Path: paths["beta"], DataDir: registeredData})
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Workspace.Partial || resolution.Workspace.ID != imported.WorkspaceID || resolution.RepositoryID != "beta" {
		t.Fatalf("partial resolution = %#v", resolution)
	}
	if _, err := resolution.Workspace.ResolveRepository("gamma"); err == nil {
		t.Fatal("partial workspace resolved absent repository gamma")
	}
}

func TestWorkspaceImporterPlansRootGitGroupedChildrenDeterministically(t *testing.T) {
	project, creationData := rootGitGroupedImportProject(t)
	for index := range project.Repositories {
		switch project.Repositories[index].ID {
		case "backend":
			project.Repositories[index].DefaultMount = "packages with spaces/backend"
		case "shared":
			project.Repositories[index].DefaultMount = "tools/grand child"
		}
	}
	root := filepath.Join(t.TempDir(), "root git imported workspace")
	created, err := NewWorkspaceCreator().Create(context.Background(), project, WorkspacePlanRequest{
		WorkspaceName: "feature/root-git-import",
		TargetPath:    root,
		DataDir:       creationData,
		Mounts: []MountOverride{
			{RepositoryID: "backend", Mount: "packages with spaces/backend"},
			{RepositoryID: "shared", Mount: "tools/grand child"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("create root-Git workspace: %v", err)
	}
	paths := make(map[string]string, len(created.Repositories))
	for _, repository := range created.Repositories {
		canonicalPath, err := filepath.EvalSymlinks(repository.Path)
		if err != nil {
			t.Fatal(err)
		}
		paths[repository.ID] = canonicalPath
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	importer := NewWorkspaceImporter()
	rootPlan, err := importer.PlanImport(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data})
	if err != nil {
		t.Fatalf("plan root-Git import: %v", err)
	}
	deepPlan, err := importer.PlanImport(context.Background(), project, ImportRequest{Path: paths["shared"], Name: "imported", DataDir: data})
	if err != nil {
		t.Fatalf("plan deepest child import: %v", err)
	}
	reversed := project
	for left, right := 0, len(reversed.Repositories)-1; left < right; left, right = left+1, right-1 {
		reversed.Repositories[left], reversed.Repositories[right] = reversed.Repositories[right], reversed.Repositories[left]
	}
	reversedPlan, err := importer.PlanImport(context.Background(), reversed, ImportRequest{Path: root, Name: "imported", DataDir: data})
	if err != nil {
		t.Fatalf("plan reversed root-Git import: %v", err)
	}
	if !importPlansEqual(rootPlan, deepPlan) || !importPlansEqual(rootPlan, reversedPlan) {
		t.Fatalf("root %#v\ndeep %#v\nreversed %#v", rootPlan, deepPlan, reversedPlan)
	}
	rootJSON, err := json.Marshal(rootPlan)
	if err != nil {
		t.Fatal(err)
	}
	reversedJSON, err := json.Marshal(reversedPlan)
	if err != nil || !bytes.Equal(rootJSON, reversedJSON) {
		t.Fatalf("plan JSON = %s, reversed = %s, error = %v", rootJSON, reversedJSON, err)
	}
	want := []struct{ id, parent, mount string }{
		{"root", "", "."},
		{"backend", "root", "packages with spaces/backend"},
		{"shared", "backend", "tools/grand child"},
	}
	if rootPlan.BaseRepository != "root" || len(rootPlan.Repositories) != len(want) {
		t.Fatalf("root-Git plan = %#v", rootPlan)
	}
	for index, repository := range rootPlan.Repositories {
		if repository.ID != want[index].id || repository.ParentID != want[index].parent || repository.Mount != want[index].mount || repository.Path != repository.ResolvedPath || repository.Path != paths[repository.ID] {
			t.Fatalf("repository %d = %#v", index, repository)
		}
	}
	imported, err := importer.Import(context.Background(), project, ImportRequest{Path: root, Name: "imported", DataDir: data})
	if err != nil {
		t.Fatalf("import root-Git workspace: %v", err)
	}
	state, err := store.ReadWorkspace(WorkspaceStatePath(data, project.ID, imported.WorkspaceID))
	if err != nil || state.Path != canonicalRoot || len(state.Repositories) != len(want) {
		t.Fatalf("root-Git imported state = %#v, %v", state, err)
	}
	for _, repository := range rootPlan.Repositories {
		checkout, found := state.Repositories[repository.ID]
		if !found || checkout.Mount != repository.Mount || checkout.ResolvedPath != repository.ResolvedPath {
			t.Fatalf("state %q = %#v", repository.ID, checkout)
		}
	}
}

func rootGitGroupedImportProject(t *testing.T) (domain.Project, string) {
	t.Helper()
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("root.txt", "root\n", "root")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	shared := testutil.NewPushedGitRepository(t)
	shared.CommitFile("shared.txt", "shared\n", "shared")
	backendPath := filepath.Join(root.Path, "backend")
	sharedPath := filepath.Join(backendPath, "shared")
	if err := os.Rename(backend.Path, backendPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(shared.Path, sharedPath); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := NewInitializer().Init(context.Background(), InitRequest{Path: root.Path, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolver().Resolve(context.Background(), ResolveRequest{Path: root.Path, ProjectPath: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	return resolution.Project, data
}
