package domain

import "fmt"

// Workspace is a versioned collection of repository checkouts for one logical
// workspace. Partial workspaces must list every missing repository explicitly.
type Workspace struct {
	Version              int
	ID                   string
	Name                 string
	RootPath             string
	Partial              bool
	MissingRepositoryIDs []string
	Checkouts            []Checkout
	Recovery             *Recovery
}

// Checkout records one repository's concrete branch or detached HEAD.
type Checkout struct {
	RepositoryID string
	Branch       string
	Head         string
	Detached     bool
	Mount        string
	ResolvedPath string
}

// Recovery records the incomplete operation that must be repaired before a
// workspace can again be treated as fully authoritative.
type Recovery struct {
	Version        int
	Operation      string
	FailedStep     string
	CompletedSteps []string
}

// ResolveRepository returns the persisted checkout path for repositoryID.
// Commands must use this rather than rebuilding paths from source mounts.
func (w Workspace) ResolveRepository(repositoryID string) (string, error) {
	for _, checkout := range w.Checkouts {
		if checkout.RepositoryID != repositoryID {
			continue
		}
		if checkout.ResolvedPath == "" {
			return "", fmt.Errorf("repository %q has no resolved checkout path", repositoryID)
		}
		return checkout.ResolvedPath, nil
	}
	return "", fmt.Errorf("workspace does not contain repository %q", repositoryID)
}

// Validate verifies membership and branch/HEAD invariants against project.
func (w Workspace) Validate(project Project) error {
	if err := project.Validate(); err != nil {
		return fmt.Errorf("validate project: %w", err)
	}
	if w.Version != CurrentVersion {
		return fmt.Errorf("workspace version %d is unsupported", w.Version)
	}
	if w.ID == "" {
		return fmt.Errorf("workspace ID is required")
	}
	if w.Name == "" {
		return fmt.Errorf("workspace name is required")
	}
	if w.RootPath == "" {
		return fmt.Errorf("workspace root path is required")
	}

	repositories := make(map[string]struct{}, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = struct{}{}
	}
	checkouts := make(map[string]struct{}, len(w.Checkouts))
	for _, checkout := range w.Checkouts {
		if _, exists := repositories[checkout.RepositoryID]; !exists {
			return fmt.Errorf("checkout has unknown repository %q", checkout.RepositoryID)
		}
		if _, exists := checkouts[checkout.RepositoryID]; exists {
			return fmt.Errorf("workspace has duplicate checkout for repository %q", checkout.RepositoryID)
		}
		checkouts[checkout.RepositoryID] = struct{}{}
		if checkout.Head == "" {
			return fmt.Errorf("checkout %q must record a HEAD", checkout.RepositoryID)
		}
		if checkout.Detached && checkout.Branch != "" {
			return fmt.Errorf("detached checkout %q must not record a branch", checkout.RepositoryID)
		}
		if !checkout.Detached && checkout.Branch == "" {
			return fmt.Errorf("attached checkout %q must record a branch", checkout.RepositoryID)
		}
	}

	missing := make(map[string]struct{}, len(w.MissingRepositoryIDs))
	for _, id := range w.MissingRepositoryIDs {
		if _, exists := repositories[id]; !exists {
			return fmt.Errorf("workspace marks unknown repository %q as missing", id)
		}
		if _, exists := missing[id]; exists {
			return fmt.Errorf("workspace lists repository %q as missing more than once", id)
		}
		if _, exists := checkouts[id]; exists {
			return fmt.Errorf("workspace has both checkout and missing entry for repository %q", id)
		}
		missing[id] = struct{}{}
	}
	if !w.Partial && len(missing) != 0 {
		return fmt.Errorf("complete workspace must not list missing repositories")
	}
	if w.Partial && len(missing) == 0 {
		return fmt.Errorf("partial workspace must explicitly list missing repositories")
	}
	for id := range repositories {
		if _, checkedOut := checkouts[id]; checkedOut {
			continue
		}
		if _, isMissing := missing[id]; isMissing {
			continue
		}
		return fmt.Errorf("workspace does not account for repository %q", id)
	}
	mounts := make(map[string]string, len(w.Checkouts))
	for _, checkout := range w.Checkouts {
		mounts[checkout.RepositoryID] = checkout.Mount
	}
	expectedPaths, err := project.EffectivePaths(w.RootPath, mounts)
	if err != nil {
		return fmt.Errorf("resolve checkout paths: %w", err)
	}
	for _, checkout := range w.Checkouts {
		if checkout.ResolvedPath == "" {
			return fmt.Errorf("checkout %q must record a resolved path", checkout.RepositoryID)
		}
		if checkout.ResolvedPath != expectedPaths[checkout.RepositoryID] {
			return fmt.Errorf("checkout %q resolved path %q does not match expected path %q", checkout.RepositoryID, checkout.ResolvedPath, expectedPaths[checkout.RepositoryID])
		}
	}
	return nil
}
