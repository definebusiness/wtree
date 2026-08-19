package domain_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
)

func TestWorkspaceValidatesCompleteAndExplicitPartialMembership(t *testing.T) {
	project := testProject()
	rootPath := filepath.Join(t.TempDir(), "workspaces", "feature-login")
	paths, err := project.EffectivePaths(rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	complete := domain.Workspace{
		Version:  1,
		ID:       "workspace-1",
		Name:     "feature/login",
		RootPath: rootPath,
		Checkouts: []domain.Checkout{
			{RepositoryID: "root", Branch: "feature/login", Head: "abc", Mount: ".", ResolvedPath: paths["root"]},
			{RepositoryID: "backend", Branch: "feature/login", Head: "def", Mount: "api", ResolvedPath: paths["backend"]},
			{RepositoryID: "shared", Branch: "feature/login", Head: "ghi", Mount: "common", ResolvedPath: paths["shared"]},
		},
	}
	if err := complete.Validate(project); err != nil {
		t.Fatalf("complete workspace Validate() error = %v", err)
	}

	partial := complete
	partial.Partial = true
	partial.Checkouts = partial.Checkouts[:2]
	partial.MissingRepositoryIDs = []string{"shared"}
	if err := partial.Validate(project); err != nil {
		t.Fatalf("partial workspace Validate() error = %v", err)
	}
}

func TestWorkspaceRejectsMissingUnsafeOrMismatchedPaths(t *testing.T) {
	base := domain.Workspace{
		Version:  1,
		ID:       "workspace-1",
		Name:     "feature/login",
		RootPath: filepath.Join(t.TempDir(), "workspace"),
		Checkouts: []domain.Checkout{
			{RepositoryID: "root", Branch: "feature/login", Head: "abc", Mount: "."},
			{RepositoryID: "backend", Branch: "feature/login", Head: "def", Mount: "api"},
			{RepositoryID: "shared", Branch: "feature/login", Head: "ghi", Mount: "common"},
		},
	}
	paths, err := testProject().EffectivePaths(base.RootPath, nil)
	if err != nil {
		t.Fatalf("EffectivePaths() error = %v", err)
	}
	for index := range base.Checkouts {
		base.Checkouts[index].ResolvedPath = paths[base.Checkouts[index].RepositoryID]
	}

	for _, test := range []struct {
		name string
		edit func(*domain.Workspace)
		want string
	}{
		{name: "missing mount", edit: func(workspace *domain.Workspace) { workspace.Checkouts[1].Mount = "" }, want: "mount"},
		{name: "unsafe mount", edit: func(workspace *domain.Workspace) { workspace.Checkouts[1].Mount = "../outside" }, want: "escapes"},
		{name: "mismatched resolved path", edit: func(workspace *domain.Workspace) {
			workspace.Checkouts[1].ResolvedPath = filepath.Join(workspace.RootPath, "wrong")
		}, want: "resolved path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := base
			workspace.Checkouts = append([]domain.Checkout(nil), base.Checkouts...)
			test.edit(&workspace)
			err := workspace.Validate(testProject())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWorkspaceRejectsInconsistentCheckoutMembershipAndHeads(t *testing.T) {
	project := testProject()
	workspace := domain.Workspace{
		Version:  1,
		ID:       "workspace-1",
		Name:     "feature/login",
		RootPath: "/workspaces/feature-login",
		Checkouts: []domain.Checkout{
			{RepositoryID: "root", Branch: "feature/login", Head: "abc", Mount: "."},
			{RepositoryID: "backend", Branch: "", Head: "def", Mount: "api"},
		},
	}

	if err := workspace.Validate(project); err == nil || !strings.Contains(err.Error(), "branch") {
		t.Fatalf("Validate() error = %v, want branch consistency error", err)
	}
}

func TestWorkspaceResolveRepositoryUsesPersistedCheckoutPath(t *testing.T) {
	workspace := domain.Workspace{
		Checkouts: []domain.Checkout{
			{RepositoryID: "root", ResolvedPath: "/workspaces/feature-login"},
			{RepositoryID: "backend", ResolvedPath: "/renamed-mount/api"},
		},
	}

	path, err := workspace.ResolveRepository("backend")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := path, "/renamed-mount/api"; got != want {
		t.Fatalf("ResolveRepository(backend) = %q, want %q", got, want)
	}
	if _, err := workspace.ResolveRepository("missing"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("ResolveRepository(missing) error = %v, want unknown repository", err)
	}
}

func testProject() domain.Project {
	return domain.Project{
		Version: 1,
		ID:      "project-1",
		Repositories: []domain.Repository{
			{ID: "root", DefaultMount: "."},
			{ID: "backend", ParentID: "root", DefaultMount: "api"},
			{ID: "shared", ParentID: "backend", DefaultMount: "common"},
		},
	}
}
