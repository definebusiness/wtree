package domain_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/marcel/wtree/internal/domain"
)

func TestProjectValidatesAndOrdersRepositoryHierarchy(t *testing.T) {
	project := domain.Project{
		Version: 1,
		ID:      "project-1",
		Name:    "product",
		Repositories: []domain.Repository{
			{ID: "shared", ParentID: "backend", DefaultMount: "shared", CommonGitDir: "/git/shared", SourcePath: "/source/backend/shared", DefaultBranch: "develop"},
			{ID: "root", DefaultMount: ".", CommonGitDir: "/git/root", SourcePath: "/source", DefaultBranch: "main"},
			{ID: "backend", ParentID: "root", DefaultMount: "api", CommonGitDir: "/git/backend", SourcePath: "/source/backend", DefaultBranch: "main"},
		},
	}

	if err := project.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got, want := ids(project.ParentFirst()), []string{"root", "backend", "shared"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ParentFirst() = %v, want %v", got, want)
	}
	if got, want := ids(project.ChildFirst()), []string{"shared", "backend", "root"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChildFirst() = %v, want %v", got, want)
	}
}

func TestProjectRejectsInvalidGraphAndMounts(t *testing.T) {
	for _, test := range []struct {
		name    string
		project domain.Project
		want    string
	}{
		{name: "duplicate ID", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "root", DefaultMount: "."}}}, want: "duplicated"},
		{name: "multiple roots", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "other", DefaultMount: "."}}}, want: "exactly one root"},
		{name: "unknown parent", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "child", ParentID: "gone", DefaultMount: "child"}}}, want: "unknown parent"},
		{name: "cycle", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "first", ParentID: "second", DefaultMount: "first"}, {ID: "second", ParentID: "first", DefaultMount: "second"}}}, want: "cycle"},
		{name: "unsafe child mount", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "child", ParentID: "root", DefaultMount: "../outside"}}}, want: "escapes"},
		{name: "moved root mount", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "elsewhere"}}}, want: "root repository mount"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.project.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func ids(repositories []domain.Repository) []string {
	ids := make([]string, len(repositories))
	for i, repository := range repositories {
		ids[i] = repository.ID
	}
	return ids
}
