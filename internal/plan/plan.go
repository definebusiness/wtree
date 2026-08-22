// Package plan defines immutable, serializable workspace operation plans.
package plan

import "fmt"

const Version = 1

type Operation string

const (
	Create   Operation = "create"
	Checkout Operation = "checkout"
)

type Action string

const (
	CreateBranch   Action = "create_branch"
	DeleteBranch   Action = "delete_branch"
	AddWorktree    Action = "add_worktree"
	RemoveWorktree Action = "remove_worktree"
)

// WorkspacePlan is a complete, validated operation description. Execution is
// deliberately outside this package so it can be rendered, serialized, and
// dry-run without side effects.
type WorkspacePlan struct {
	Version        int              `json:"version"`
	Operation      Operation        `json:"operation"`
	ProjectID      string           `json:"projectId"`
	WorkspaceName  string           `json:"workspaceName"`
	WorkspaceID    string           `json:"workspaceId"`
	RootPath       string           `json:"rootPath"`
	LogicalRoot    string           `json:"logicalRoot"`
	BaseRepository string           `json:"baseRepository"`
	Repositories   []RepositoryPlan `json:"repositories"`
	Steps          []Step           `json:"steps"`
}

type RepositoryPlan struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId,omitempty"`
	Base     string `json:"base"`
	Branch   string `json:"branch"`
	Mount    string `json:"mount"`
	Path     string `json:"path"`
}

type Step struct {
	Action       Action `json:"action"`
	RepositoryID string `json:"repositoryId"`
	Inverse      Action `json:"inverse"`
}

func (p WorkspacePlan) Validate() error {
	if p.Version != Version {
		return fmt.Errorf("unsupported plan version %d", p.Version)
	}
	if p.Operation != Create && p.Operation != Checkout {
		return fmt.Errorf("unsupported plan operation %q", p.Operation)
	}
	if p.ProjectID == "" || p.WorkspaceName == "" || p.WorkspaceID == "" || p.RootPath == "" || p.LogicalRoot == "" || p.BaseRepository == "" {
		return fmt.Errorf("plan project, workspace name, workspace ID, root path, logical root, and base repository are required")
	}
	if p.LogicalRoot != p.RootPath {
		return fmt.Errorf("plan logical root %q must equal root path %q", p.LogicalRoot, p.RootPath)
	}
	if len(p.Repositories) == 0 {
		return fmt.Errorf("plan must include repositories")
	}
	seen := make(map[string]struct{}, len(p.Repositories))
	for _, repository := range p.Repositories {
		if repository.ID == "" || repository.Base == "" || repository.Branch == "" || repository.Mount == "" || repository.Path == "" {
			return fmt.Errorf("plan repository fields are required")
		}
		if _, exists := seen[repository.ID]; exists {
			return fmt.Errorf("plan has duplicate repository %q", repository.ID)
		}
		seen[repository.ID] = struct{}{}
	}
	base, found := repositoryByID(p.Repositories, p.BaseRepository)
	if !found || base.ParentID != "" {
		return fmt.Errorf("plan base repository %q must be a declared top-level repository", p.BaseRepository)
	}
	if p.Operation == Create && len(p.Steps) != len(p.Repositories)*2 {
		return fmt.Errorf("create plan has %d steps, want %d", len(p.Steps), len(p.Repositories)*2)
	}
	if p.Operation == Checkout && len(p.Steps) != len(p.Repositories) {
		return fmt.Errorf("checkout plan has %d steps, want %d", len(p.Steps), len(p.Repositories))
	}
	for _, step := range p.Steps {
		if _, exists := seen[step.RepositoryID]; !exists {
			return fmt.Errorf("plan step has unknown repository %q", step.RepositoryID)
		}
		if step.Action == "" || step.Inverse == "" {
			return fmt.Errorf("plan step action and inverse are required")
		}
	}
	return nil
}

func repositoryByID(repositories []RepositoryPlan, id string) (RepositoryPlan, bool) {
	for _, repository := range repositories {
		if repository.ID == id {
			return repository, true
		}
	}
	return RepositoryPlan{}, false
}
