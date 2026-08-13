package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/marcel/wtree/internal/git"
	"github.com/marcel/wtree/internal/lock"
	"github.com/marcel/wtree/internal/service"
	"github.com/marcel/wtree/internal/store"
	"github.com/marcel/wtree/internal/testutil"
)

func TestWorkspaceImporterImportsRenamedMountAndDivergentBranchesByGitIdentity(t *testing.T) {
	project, root, backend, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "external-workspace")
	root.Run(t, "branch", "feature/import-root")
	root.Run(t, "worktree", "add", target, "feature/import-root")
	backend.Run(t, "branch", "feature/import-backend")
	backend.Run(t, "worktree", "add", filepath.Join(target, "api"), "feature/import-backend")
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}

	value, err := service.NewWorkspaceImporter().Import(context.Background(), project, service.ImportRequest{Path: target, Name: "imported", DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if value.WorkspaceName != "imported" || len(value.Repositories) != 2 || value.Repositories[0].ID != "root" || value.Repositories[0].Branch != "feature/import-root" || value.Repositories[1].ID != "backend" || value.Repositories[1].Branch != "feature/import-backend" || value.Repositories[1].Mount != "api" || value.Repositories[1].Path != filepath.Join(canonicalTarget, "api") {
		t.Fatalf("import result = %#v", value)
	}
	workspace, err := service.RequireWorkspace(project, data, "imported")
	if err != nil {
		t.Fatal(err)
	}
	backendPath, err := workspace.ResolveRepository("backend")
	if err != nil || backendPath != filepath.Join(canonicalTarget, "api") {
		t.Fatalf("imported backend path = %q, %v", backendPath, err)
	}
}

func TestWorkspaceImporterRejectsIncompleteUnlessAllowPartial(t *testing.T) {
	project, root, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "external")
	root.Run(t, "branch", "feature/partial")
	root.Run(t, "worktree", "add", target, "feature/partial")
	request := service.ImportRequest{Path: target, Name: "partial", DataDir: data}
	if _, err := service.NewWorkspaceImporter().PlanImport(context.Background(), project, request); !importErrorKind(err, service.ErrorValidation) {
		t.Fatalf("incomplete import = %v, want validation", err)
	}
	request.AllowPartial = true
	value, err := service.NewWorkspaceImporter().Import(context.Background(), project, request)
	if err != nil {
		t.Fatal(err)
	}
	if !value.Partial || len(value.MissingRepositoryIDs) != 1 || value.MissingRepositoryIDs[0] != "backend" {
		t.Fatalf("partial plan = %#v", value)
	}
	workspace, err := service.RequireWorkspace(project, data, "partial")
	if err != nil || !workspace.Partial || len(workspace.MissingRepositoryIDs) != 1 {
		t.Fatalf("partial state = %#v, %v", workspace, err)
	}
}

func TestWorkspaceImporterPreservesDetachedHeadAndInferredDirectoryName(t *testing.T) {
	project, root, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "detached workspace")
	root.Run(t, "branch", "feature/detached")
	root.Run(t, "worktree", "add", target, "feature/detached")
	testutil.GitRepository{Path: target}.Run(t, "checkout", "--detach")
	value, err := service.NewWorkspaceImporter().PlanImport(context.Background(), project, service.ImportRequest{Path: target, AllowPartial: true, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if value.WorkspaceName != "detached workspace" || len(value.Repositories) != 1 || !value.Repositories[0].Detached || value.Repositories[0].Branch != "" || value.Repositories[0].Head == "" {
		t.Fatalf("detached import = %#v", value)
	}
}

func TestWorkspaceImporterRequiresExplicitNameForDivergentOrDetachedCheckouts(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		detach bool
	}{
		{name: "divergent branches"},
		{name: "detached checkout", detach: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			target := filepath.Join(t.TempDir(), "external")
			root.Run(t, "branch", "feature/root")
			root.Run(t, "worktree", "add", target, "feature/root")
			backend.Run(t, "branch", "feature/backend")
			backend.Run(t, "worktree", "add", filepath.Join(target, "api"), "feature/backend")
			if scenario.detach {
				testutil.GitRepository{Path: target}.Run(t, "checkout", "--detach")
			}

			_, err := service.NewWorkspaceImporter().PlanImport(context.Background(), project, service.ImportRequest{Path: target, DataDir: data})
			if !importErrorKind(err, service.ErrorValidation) || !strings.Contains(err.Error(), "--name") {
				t.Fatalf("unnamed import = %v, want validation requesting --name", err)
			}
			value, err := service.NewWorkspaceImporter().PlanImport(context.Background(), project, service.ImportRequest{Path: target, Name: "explicit", DataDir: data})
			if err != nil || value.WorkspaceName != "explicit" {
				t.Fatalf("named import = %#v, %v", value, err)
			}
		})
	}
}

func TestWorkspaceImporterRejectsUnknownAndDuplicateGitIdentities(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		setup func(t *testing.T, root, backend testutil.GitRepository, target string)
	}{
		{name: "unknown", setup: func(t *testing.T, _ testutil.GitRepository, _ testutil.GitRepository, target string) {
			unknown := testutil.NewGitRepository(t)
			unknown.CommitFile("unknown.txt", "unknown\n", "unknown")
			if err := os.Rename(unknown.Path, filepath.Join(target, "unknown")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "duplicate", setup: func(t *testing.T, _ testutil.GitRepository, backend testutil.GitRepository, target string) {
			backend.Run(t, "branch", "feature/duplicate")
			backend.Run(t, "worktree", "add", filepath.Join(target, "duplicate"), "feature/duplicate")
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			target := filepath.Join(t.TempDir(), "external")
			root.Run(t, "branch", "feature/import")
			root.Run(t, "worktree", "add", target, "feature/import")
			backend.Run(t, "branch", "feature/backend")
			backend.Run(t, "worktree", "add", filepath.Join(target, "api"), "feature/backend")
			scenario.setup(t, root, backend, target)
			if _, err := service.NewWorkspaceImporter().PlanImport(context.Background(), project, service.ImportRequest{Path: target, Name: "imported", DataDir: data}); !importErrorKind(err, service.ErrorValidation) {
				t.Fatalf("%s import = %v, want validation", scenario.name, err)
			}
		})
	}
}

func TestWorkspaceImporterRejectsExistingNameAndPersistsAtomicallyUnderLock(t *testing.T) {
	project, root, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "external")
	root.Run(t, "branch", "feature/import")
	root.Run(t, "worktree", "add", target, "feature/import")
	request := service.ImportRequest{Path: target, Name: "imported", AllowPartial: true, DataDir: data}
	if _, err := service.NewWorkspaceImporter().Import(context.Background(), project, request); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewWorkspaceImporter().PlanImport(context.Background(), project, request); !importErrorKind(err, service.ErrorConflict) {
		t.Fatalf("duplicate import = %v, want conflict", err)
	}
	otherTarget := filepath.Join(t.TempDir(), "other")
	root.Run(t, "branch", "feature/other")
	root.Run(t, "worktree", "add", otherTarget, "feature/other")
	failing := service.NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), lock.Manager{}, func(string, store.WorkspaceState) error { return errors.New("injected atomic write failure") })
	_, err := failing.Import(context.Background(), project, service.ImportRequest{Path: otherTarget, Name: "other", AllowPartial: true, DataDir: data})
	if !importErrorKind(err, service.ErrorInternal) {
		t.Fatalf("failed atomic import = %v, want internal", err)
	}
	if _, err := service.RequireWorkspace(project, data, "other"); !importErrorKind(err, service.ErrorWorkspaceNotFound) {
		t.Fatalf("failed import persisted state: %v", err)
	}
}

func TestWorkspaceImporterDoesNotPersistWhenProjectLockIsContended(t *testing.T) {
	project, root, _, data := createFixture(t)
	target := filepath.Join(t.TempDir(), "external")
	root.Run(t, "branch", "feature/locked")
	root.Run(t, "worktree", "add", target, "feature/locked")
	importer := service.NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), contendedProjectLocker{}, store.WriteWorkspace)
	_, err := importer.Import(context.Background(), project, service.ImportRequest{Path: target, Name: "locked", AllowPartial: true, DataDir: data})
	if !importErrorKind(err, service.ErrorConflict) {
		t.Fatalf("contended import = %v, want conflict", err)
	}
	if _, err := service.RequireWorkspace(project, data, "locked"); !importErrorKind(err, service.ErrorWorkspaceNotFound) {
		t.Fatalf("contended import persisted state: %v", err)
	}
}

func TestWorkspaceImporterDerivesRenamedDescendantMounts(t *testing.T) {
	project, root, backend, shared, data := createThreeLevelFixture(t)
	target := filepath.Join(t.TempDir(), "external")
	for _, repository := range []struct {
		repo   testutil.GitRepository
		branch string
		path   string
	}{
		{root, "feature/root", target},
		{backend, "feature/backend", filepath.Join(target, "api")},
		{shared, "feature/shared", filepath.Join(target, "api", "common")},
	} {
		repository.repo.Run(t, "branch", repository.branch)
		repository.repo.Run(t, "worktree", "add", repository.path, repository.branch)
	}
	value, err := service.NewWorkspaceImporter().PlanImport(context.Background(), project, service.ImportRequest{Path: target, Name: "nested", DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if value.Repositories[1].Mount != "api" || value.Repositories[2].Mount != "common" || value.Repositories[2].Branch != "feature/shared" {
		t.Fatalf("nested import = %#v", value)
	}
}

func importErrorKind(err error, want service.ErrorKind) bool {
	var application *service.Error
	return errors.As(err, &application) && application.Kind == want
}
