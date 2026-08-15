// Package domain defines the pure, versioned wtree model and its invariants.
package domain

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/definebusiness/wtree/internal/pathutil"
)

const CurrentVersion = 1

// Project is the versioned aggregate of repositories managed as one workspace.
type Project struct {
	Version      int
	ID           string
	Name         string
	ConfigPath   string
	Repositories []Repository
}

// EffectivePaths is the sole resolver for parent-relative repository mounts.
// mounts may override configured mounts by repository ID for one workspace.
func (p Project) EffectivePaths(workspaceRoot string, mounts map[string]string) (map[string]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	repositories := make(map[string]Repository, len(p.Repositories))
	for _, repository := range p.Repositories {
		repositories[repository.ID] = repository
	}
	for id := range mounts {
		if _, exists := repositories[id]; !exists {
			return nil, fmt.Errorf("mount override has unknown repository %q", id)
		}
	}

	paths := make(map[string]string, len(p.Repositories))
	for _, repository := range p.ParentFirst() {
		mount := repository.DefaultMount
		if override, exists := mounts[repository.ID]; exists {
			mount = override
		}
		parentPath := paths[repository.ParentID]
		resolved, err := pathutil.ResolveMount(workspaceRoot, parentPath, mount, repository.ParentID == "")
		if err != nil {
			return nil, fmt.Errorf("repository %q mount: %w", repository.ID, err)
		}
		for otherID, otherPath := range paths {
			if pathsOverlap(otherPath, resolved) && !isAncestor(repositories, otherID, repository.ID) {
				return nil, fmt.Errorf("repository mount %q conflicts with %q", repository.ID, otherID)
			}
		}
		paths[repository.ID] = resolved
	}
	return paths, nil
}

func isAncestor(repositories map[string]Repository, ancestorID, descendantID string) bool {
	for parentID := repositories[descendantID].ParentID; parentID != ""; parentID = repositories[parentID].ParentID {
		if parentID == ancestorID {
			return true
		}
	}
	return false
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return left == right || pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Repository is a path-independent source repository within a project.
type Repository struct {
	ID            string
	CommonGitDir  string
	SourcePath    string
	ParentID      string
	DefaultMount  string
	DefaultBranch string
}

// Validate confirms that the repository hierarchy has exactly one root and is
// a tree with stable, unique repository IDs.
func (p Project) Validate() error {
	if p.Version != CurrentVersion {
		return fmt.Errorf("project version %d is unsupported", p.Version)
	}
	if p.ID == "" {
		return fmt.Errorf("project ID is required")
	}
	if len(p.Repositories) == 0 {
		return fmt.Errorf("project must contain a root repository")
	}

	repositories := make(map[string]Repository, len(p.Repositories))
	rootCount := 0
	for _, repository := range p.Repositories {
		if repository.ID == "" {
			return fmt.Errorf("repository ID is required")
		}
		if _, exists := repositories[repository.ID]; exists {
			return fmt.Errorf("repository ID %q is duplicated", repository.ID)
		}
		repositories[repository.ID] = repository
		if repository.ParentID == "" {
			rootCount++
		}
		if err := pathutil.ValidateMount(repository.DefaultMount, repository.ParentID == ""); err != nil {
			return fmt.Errorf("repository %q mount: %w", repository.ID, err)
		}
	}
	if rootCount != 1 {
		return fmt.Errorf("project must contain exactly one root repository, got %d", rootCount)
	}

	for _, repository := range p.Repositories {
		if repository.ParentID == "" {
			continue
		}
		if _, exists := repositories[repository.ParentID]; !exists {
			return fmt.Errorf("repository %q has unknown parent %q", repository.ID, repository.ParentID)
		}
	}

	visiting := make(map[string]bool, len(repositories))
	visited := make(map[string]bool, len(repositories))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("repository hierarchy contains a cycle at %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		parentID := repositories[id].ParentID
		if parentID != "" {
			if err := visit(parentID); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range repositories {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// ParentFirst returns a deterministic topological order suitable for creation.
func (p Project) ParentFirst() []Repository {
	children := p.childrenByParent()
	root := p.root()
	ordered := make([]Repository, 0, len(p.Repositories))
	var appendChildren func(Repository)
	appendChildren = func(repository Repository) {
		ordered = append(ordered, repository)
		for _, child := range children[repository.ID] {
			appendChildren(child)
		}
	}
	if root.ID != "" {
		appendChildren(root)
	}
	return ordered
}

// ChildFirst returns the reverse dependency order suitable for removal.
func (p Project) ChildFirst() []Repository {
	ordered := p.ParentFirst()
	for left, right := 0, len(ordered)-1; left < right; left, right = left+1, right-1 {
		ordered[left], ordered[right] = ordered[right], ordered[left]
	}
	return ordered
}

func (p Project) root() Repository {
	for _, repository := range p.Repositories {
		if repository.ParentID == "" {
			return repository
		}
	}
	return Repository{}
}

func (p Project) childrenByParent() map[string][]Repository {
	children := make(map[string][]Repository)
	for _, repository := range p.Repositories {
		if repository.ParentID != "" {
			children[repository.ParentID] = append(children[repository.ParentID], repository)
		}
	}
	for parentID := range children {
		sort.Slice(children[parentID], func(left, right int) bool {
			return children[parentID][left].ID < children[parentID][right].ID
		})
	}
	return children
}
