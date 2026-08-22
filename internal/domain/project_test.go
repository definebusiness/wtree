package domain_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
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

func TestProjectValidatesForestAndUsesStableOrders(t *testing.T) {
	project := domain.Project{
		Version:        domain.CurrentVersion,
		ID:             "project",
		BaseRepository: "z-base",
		Repositories: []domain.Repository{
			{ID: "deep", ParentID: "child", DefaultMount: "deep"},
			{ID: "a-top", DefaultMount: "apps/frontend"},
			{ID: "z-base", DefaultMount: "apps/backend"},
			{ID: "child", ParentID: "z-base", DefaultMount: "services/api"},
		},
	}
	if err := project.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got, want := ids(project.ParentFirst()), []string{"a-top", "z-base", "child", "deep"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ParentFirst() = %v, want %v", got, want)
	}
	if got, want := ids(project.MetadataFirst()), []string{"z-base", "a-top", "child", "deep"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MetadataFirst() = %v, want %v", got, want)
	}
	if got, want := ids(project.ChildFirst()), []string{"deep", "child", "z-base", "a-top"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ChildFirst() = %v, want %v", got, want)
	}
}

func TestProjectForestRequiresDeclaredTopLevelBase(t *testing.T) {
	base := domain.Project{Version: domain.CurrentVersion, ID: "p", Repositories: []domain.Repository{{ID: "a", DefaultMount: "apps/a"}, {ID: "b", DefaultMount: "apps/b"}}}
	for _, test := range []struct {
		name string
		base string
		want string
	}{
		{"missing", "", "base repository is required"},
		{"unknown", "missing", "is not declared"},
		{"nested", "child", "must be top-level"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := base
			project.BaseRepository = test.base
			if test.name == "nested" {
				project.Repositories = append(project.Repositories, domain.Repository{ID: "child", ParentID: "a", DefaultMount: "child"})
			}
			if err := project.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProjectForestOrdersDoNotDependOnInputPermutation(t *testing.T) {
	repositories := []domain.Repository{
		{ID: "base", DefaultMount: "services/base"},
		{ID: "sibling", DefaultMount: "services/sibling"},
		{ID: "child-z", ParentID: "base", DefaultMount: "z"},
		{ID: "child-a", ParentID: "base", DefaultMount: "a"},
		{ID: "deep", ParentID: "child-a", DefaultMount: "deep"},
	}
	for _, permutation := range repositoryPermutations(repositories) {
		project := domain.Project{Version: domain.CurrentVersion, ID: "p", BaseRepository: "base", Repositories: permutation}
		if err := project.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if got, want := ids(project.ParentFirst()), []string{"base", "sibling", "child-a", "child-z", "deep"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ParentFirst() = %v, want %v", got, want)
		}
		if got, want := ids(project.MetadataFirst()), []string{"base", "sibling", "child-a", "child-z", "deep"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("MetadataFirst() = %v, want %v", got, want)
		}
		if got, want := ids(project.ChildFirst()), []string{"deep", "child-z", "child-a", "sibling", "base"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ChildFirst() = %v, want %v", got, want)
		}
	}
}

func repositoryPermutations(values []domain.Repository) [][]domain.Repository {
	if len(values) == 0 {
		return [][]domain.Repository{{}}
	}
	permutations := make([][]domain.Repository, 0)
	for index, value := range values {
		remainder := append([]domain.Repository(nil), values[:index]...)
		remainder = append(remainder, values[index+1:]...)
		for _, suffix := range repositoryPermutations(remainder) {
			permutation := append([]domain.Repository{value}, suffix...)
			permutations = append(permutations, permutation)
		}
	}
	return permutations
}

func TestProjectRejectsInvalidGraphAndMounts(t *testing.T) {
	for _, test := range []struct {
		name    string
		project domain.Project
		want    string
	}{
		{name: "duplicate ID", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "root", DefaultMount: "."}}}, want: "duplicated"},
		{name: "dot plus another top-level", project: domain.Project{Version: 1, ID: "p", BaseRepository: "root", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "other", DefaultMount: "apps/other"}}}, want: "sole top-level"},
		{name: "unknown parent", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "child", ParentID: "gone", DefaultMount: "child"}}}, want: "unknown parent"},
		{name: "cycle", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "first", ParentID: "second", DefaultMount: "first"}, {ID: "second", ParentID: "first", DefaultMount: "second"}}}, want: "cycle"},
		{name: "unsafe child mount", project: domain.Project{Version: 1, ID: "p", Repositories: []domain.Repository{{ID: "root", DefaultMount: "."}, {ID: "child", ParentID: "root", DefaultMount: "../outside"}}}, want: "escapes"},
		{name: "empty top-level mount", project: domain.Project{Version: 1, ID: "p", BaseRepository: "root", Repositories: []domain.Repository{{ID: "root", DefaultMount: ""}}}, want: "required"},
		{name: "canonical alias", project: domain.Project{Version: 1, ID: "p", BaseRepository: "root", Repositories: []domain.Repository{{ID: "root", DefaultMount: "apps/../root"}}}, want: "clean"},
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
