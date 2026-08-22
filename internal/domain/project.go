// Package domain defines the pure, versioned wtree model and its invariants.
package domain

import (
	"fmt"
	"sort"

	"github.com/definebusiness/wtree/internal/pathutil"
)

const CurrentVersion = 1

// Project is the versioned aggregate of repositories managed as one workspace.
type Project struct {
	Version        int
	ID             string
	Name           string
	ConfigPath     string
	BaseRepository string
	LogicalRoot    string
	// DiscoveryIgnores is an internal, non-persisted fact loaded from strict
	// local configuration. It is deliberately excluded from versioned wire
	// models and is consumed only by discovery/import observation.
	DiscoveryIgnores []string
	Repositories     []Repository
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
		kind := pathutil.ChildMount
		if repository.ParentID == "" {
			kind = pathutil.TopLevelMount
		}
		resolved, err := pathutil.ResolveMount(workspaceRoot, parentPath, mount, kind)
		if err != nil {
			return nil, fmt.Errorf("repository %q mount: %w", repository.ID, err)
		}
		canonicalResolved, err := pathutil.CanonicalPotentialPath(resolved)
		if err != nil {
			return nil, fmt.Errorf("canonicalize repository %q mount: %w", repository.ID, err)
		}
		for otherID, otherPath := range paths {
			canonicalOther, err := pathutil.CanonicalPotentialPath(otherPath)
			if err != nil {
				return nil, fmt.Errorf("canonicalize repository %q mount: %w", otherID, err)
			}
			if pathutil.CaseFoldedPathEqual(canonicalOther, canonicalResolved) {
				return nil, fmt.Errorf("repository mount %q duplicates %q", repository.ID, otherID)
			}
			if pathsOverlap(canonicalOther, canonicalResolved) && !isAncestor(repositories, otherID, repository.ID) {
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
	return pathutil.CaseFoldedPathOverlap(left, right)
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

// Validate confirms that the repository hierarchy is a non-empty acyclic
// forest with one declared top-level metadata authority.
func (p Project) Validate() error {
	if p.Version != CurrentVersion {
		return fmt.Errorf("project version %d is unsupported", p.Version)
	}
	if p.ID == "" {
		return fmt.Errorf("project ID is required")
	}
	if len(p.Repositories) == 0 {
		return fmt.Errorf("project must contain at least one top-level repository")
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
		kind := pathutil.ChildMount
		if repository.ParentID == "" {
			kind = pathutil.TopLevelMount
		}
		if err := pathutil.ValidateMount(repository.DefaultMount, kind); err != nil {
			return fmt.Errorf("repository %q mount: %w", repository.ID, err)
		}
	}
	if rootCount == 0 {
		return fmt.Errorf("project must contain at least one top-level repository")
	}

	for _, repository := range p.Repositories {
		if repository.ParentID == "" {
			continue
		}
		if _, exists := repositories[repository.ParentID]; !exists {
			return fmt.Errorf("repository %q has unknown parent %q", repository.ID, repository.ParentID)
		}
	}
	baseID := p.BaseRepository
	// Legacy in-memory root-Git callers did not carry a base field. Inferring
	// the sole top-level repository preserves that domain compatibility; local
	// configuration v1 is still rejected by the strict v2 config loader.
	if baseID == "" && rootCount == 1 {
		for _, repository := range p.Repositories {
			if repository.ParentID == "" {
				baseID = repository.ID
				break
			}
		}
	}
	if baseID == "" {
		return fmt.Errorf("project base repository is required for a forest")
	}
	base, exists := repositories[baseID]
	if !exists {
		return fmt.Errorf("project base repository %q is not declared", baseID)
	}
	if base.ParentID != "" {
		return fmt.Errorf("project base repository %q must be top-level", baseID)
	}
	for _, repository := range p.Repositories {
		if repository.ParentID == "" && repository.DefaultMount == "." && rootCount != 1 {
			return fmt.Errorf("top-level mount %q is valid only as the sole top-level repository", ".")
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

// ParentFirst returns increasing depth then lexical repository ID.
func (p Project) ParentFirst() []Repository {
	ordered := append([]Repository(nil), p.Repositories...)
	depths := p.depths()
	sort.Slice(ordered, func(left, right int) bool {
		if depths[ordered[left].ID] != depths[ordered[right].ID] {
			return depths[ordered[left].ID] < depths[ordered[right].ID]
		}
		return ordered[left].ID < ordered[right].ID
	})
	return ordered
}

// MetadataFirst returns parent-first order with the declared base first among
// top-level repositories. Deeper levels retain their normal order.
func (p Project) MetadataFirst() []Repository {
	ordered := p.ParentFirst()
	baseID := p.metadataBaseID()
	for index, repository := range ordered {
		if repository.ID == baseID {
			copy(ordered[1:index+1], ordered[0:index])
			ordered[0] = repository
			break
		}
	}
	return ordered
}

// ChildFirst returns decreasing depth then reverse lexical repository ID.
func (p Project) ChildFirst() []Repository {
	ordered := append([]Repository(nil), p.Repositories...)
	depths := p.depths()
	sort.Slice(ordered, func(left, right int) bool {
		if depths[ordered[left].ID] != depths[ordered[right].ID] {
			return depths[ordered[left].ID] > depths[ordered[right].ID]
		}
		return ordered[left].ID > ordered[right].ID
	})
	return ordered
}

func (p Project) metadataBaseID() string {
	if p.BaseRepository != "" {
		return p.BaseRepository
	}
	for _, repository := range p.Repositories {
		if repository.ParentID == "" {
			return repository.ID
		}
	}
	return ""
}

func (p Project) depths() map[string]int {
	repositories := make(map[string]Repository, len(p.Repositories))
	for _, repository := range p.Repositories {
		repositories[repository.ID] = repository
	}
	depths := make(map[string]int, len(repositories))
	var depth func(string) int
	depth = func(id string) int {
		if value, exists := depths[id]; exists {
			return value
		}
		repository := repositories[id]
		if repository.ParentID == "" {
			depths[id] = 0
			return 0
		}
		value := depth(repository.ParentID) + 1
		depths[id] = value
		return value
	}
	for id := range repositories {
		depth(id)
	}
	return depths
}
