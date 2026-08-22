package plan_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/plan"
)

func TestWorkspacePlanIsSerializableAndHasParentFirstReversibleSteps(t *testing.T) {
	value := plan.WorkspacePlan{
		Version:        plan.Version,
		Operation:      plan.Create,
		ProjectID:      "project",
		WorkspaceName:  "feature/login",
		WorkspaceID:    "feature-login",
		RootPath:       "/worktrees/project/feature-login",
		LogicalRoot:    "/worktrees/project/feature-login",
		BaseRepository: "root",
		Repositories: []plan.RepositoryPlan{
			{ID: "root", Base: "root-head", Branch: "feature/login", Mount: ".", Path: "/worktrees/project/feature-login"},
			{ID: "backend", ParentID: "root", Base: "backend-head", Branch: "feature/login", Mount: "api", Path: "/worktrees/project/feature-login/api"},
		},
		Steps: []plan.Step{
			{Action: plan.CreateBranch, RepositoryID: "root", Inverse: plan.DeleteBranch},
			{Action: plan.AddWorktree, RepositoryID: "root", Inverse: plan.RemoveWorktree},
			{Action: plan.CreateBranch, RepositoryID: "backend", Inverse: plan.DeleteBranch},
			{Action: plan.AddWorktree, RepositoryID: "backend", Inverse: plan.RemoveWorktree},
		},
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded plan.WorkspacePlan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, value) {
		t.Fatalf("JSON round trip = %#v, want %#v", decoded, value)
	}
}

func TestWorkspacePlanRejectsTamperedTopology(t *testing.T) {
	value := plan.WorkspacePlan{
		Version: plan.Version, Operation: plan.Create, ProjectID: "project", WorkspaceName: "feature", WorkspaceID: "feature", RootPath: "/workspace", LogicalRoot: "/workspace", BaseRepository: "root",
		Repositories: []plan.RepositoryPlan{{ID: "root", Base: "head", Branch: "feature", Mount: ".", Path: "/workspace"}},
		Steps:        []plan.Step{{Action: plan.CreateBranch, RepositoryID: "root", Inverse: plan.DeleteBranch}, {Action: plan.AddWorktree, RepositoryID: "root", Inverse: plan.RemoveWorktree}},
	}
	for name, mutate := range map[string]func(*plan.WorkspacePlan){
		"logical root": func(value *plan.WorkspacePlan) { value.LogicalRoot = "/other" },
		"base":         func(value *plan.WorkspacePlan) { value.BaseRepository = "missing" },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := value
			mutated.Repositories = append([]plan.RepositoryPlan(nil), value.Repositories...)
			mutated.Steps = append([]plan.Step(nil), value.Steps...)
			mutate(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatal("tampered topology accepted")
			}
		})
	}
}
