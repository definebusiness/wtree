package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
)

var (
	ErrNoProjectContext = errors.New("No wtree project could be determined from the current directory.\n\nUse:\n  wtree init\nor:\n  wtree --project <path> <command>")
	ErrAmbiguousProject = errors.New("multiple wtree projects match the current repository; use --project")
	ErrStaleRegistry    = errors.New("wtree project registry contains stale project information")
)

// ResolveRequest identifies the invocation context. ProjectPath is an
// explicit project directory or .wtree.yml path and takes precedence over
// automatic discovery.
type ResolveRequest struct {
	Path        string
	ProjectPath string
	DataDir     string
}

// Resolution is the single context used by commands after project discovery.
// Repository paths are obtained exclusively through Workspace.ResolveRepository.
type Resolution struct {
	Project      domain.Project
	Workspace    domain.Workspace
	RepositoryID string
}

// Resolver discovers a project, its active workspace, and the current
// repository using explicit selection, local configuration, then Git identity.
type Resolver struct {
	git gitadapter.Git
}

func NewResolver() *Resolver {
	return &Resolver{git: gitadapter.NewAdapter("git")}
}

// ResolveProject resolves only the project aggregate. Import uses this lighter
// boundary because the target checkout is intentionally not yet workspace
// state and therefore cannot satisfy current-workspace inference.
func (r *Resolver) ResolveProject(ctx context.Context, request ResolveRequest) (domain.Project, error) {
	path, err := canonicalDirectory(request.Path)
	if err != nil {
		return domain.Project{}, err
	}
	registry, err := readRegistry(filepath.Join(request.DataDir, "registry.json"))
	if err != nil {
		return domain.Project{}, err
	}
	var project domain.Project
	switch {
	case request.ProjectPath != "":
		configPath, err := explicitConfigPath(request.ProjectPath)
		if err != nil {
			return domain.Project{}, err
		}
		project, err = r.loadProject(ctx, configPath)
		if err != nil {
			return domain.Project{}, err
		}
	case findConfigPath(path) != "":
		project, err = r.loadProject(ctx, findConfigPath(path))
		if err != nil {
			return domain.Project{}, err
		}
	default:
		project, err = r.projectFromGitIdentity(ctx, path, registry)
		if err != nil {
			return domain.Project{}, err
		}
	}
	return project, nil
}

// ReconcileProject records a relocated project only after a mutating command
// has completed its read-only preflight.
func (r *Resolver) ReconcileProject(ctx context.Context, dataDir string, project domain.Project) error {
	registry, err := readRegistry(filepath.Join(dataDir, "registry.json"))
	if err != nil {
		return err
	}
	return r.reconcileRegistry(ctx, dataDir, project, registry)
}

func (r *Resolver) Resolve(ctx context.Context, request ResolveRequest) (Resolution, error) {
	return r.resolve(ctx, request, true)
}

// ResolveReadOnly resolves the same project/workspace context without
// reconciling registry metadata. Inspection commands must use this boundary.
func (r *Resolver) ResolveReadOnly(ctx context.Context, request ResolveRequest) (Resolution, error) {
	return r.resolve(ctx, request, false)
}

func (r *Resolver) resolve(ctx context.Context, request ResolveRequest, reconcile bool) (Resolution, error) {
	path, err := canonicalDirectory(request.Path)
	if err != nil {
		return Resolution{}, err
	}
	registry, err := readRegistry(filepath.Join(request.DataDir, "registry.json"))
	if err != nil {
		return Resolution{}, err
	}

	var project domain.Project
	localConfig := false
	switch {
	case request.ProjectPath != "":
		configPath, err := explicitConfigPath(request.ProjectPath)
		if err != nil {
			return Resolution{}, err
		}
		project, err = r.loadProject(ctx, configPath)
		if err != nil {
			return Resolution{}, err
		}
		localConfig = true
	default:
		if configPath := findConfigPath(path); configPath != "" {
			projectID, err := projectIDFromConfig(configPath)
			if err != nil {
				return Resolution{}, err
			}
			registeredProject, found, registeredErr := r.registeredProject(ctx, projectID, registry)
			if registeredErr != nil {
				return Resolution{}, registeredErr
			}
			if found {
				project = registeredProject
			} else {
				project, err = r.loadProject(ctx, configPath)
				if err != nil {
					return Resolution{}, err
				}
				localConfig = true
			}
		} else {
			persistedProject, found, resolveErr := r.projectForPersistedWorkspace(ctx, request.DataDir, path, registry)
			if resolveErr != nil {
				return Resolution{}, resolveErr
			}
			if found {
				project = persistedProject
			} else {
				project, err = r.projectFromGitIdentity(ctx, path, registry)
				if err != nil {
					return Resolution{}, err
				}
			}
		}
	}
	if localConfig && reconcile {
		if err := r.reconcileRegistry(ctx, request.DataDir, project, registry); err != nil {
			return Resolution{}, err
		}
	}

	commonGitDir, err := r.git.CommonGitDir(ctx, path)
	if err != nil {
		if localConfig {
			workspace, workspaceErr := r.defaultWorkspace(ctx, request.DataDir, project)
			if workspaceErr != nil {
				return Resolution{}, workspaceErr
			}
			return Resolution{Project: project, Workspace: workspace}, nil
		}
		return Resolution{}, fmt.Errorf("determine current Git repository: %w", err)
	}
	repositoryID := ""
	for _, repository := range project.Repositories {
		if repository.CommonGitDir == commonGitDir {
			repositoryID = repository.ID
			break
		}
	}
	if repositoryID == "" {
		if request.ProjectPath != "" {
			workspace, workspaceErr := r.defaultWorkspace(ctx, request.DataDir, project)
			if workspaceErr != nil {
				return Resolution{}, workspaceErr
			}
			return Resolution{Project: project, Workspace: workspace}, nil
		}
		return Resolution{}, fmt.Errorf("%w: current Git identity is not part of project %q", ErrStaleRegistry, project.ID)
	}
	topLevel, err := r.git.TopLevel(ctx, path)
	if err != nil {
		return Resolution{}, fmt.Errorf("determine current checkout: %w", err)
	}
	workspace, err := r.workspaceForCheckout(ctx, request.DataDir, project, repositoryID, topLevel)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{Project: project, Workspace: workspace, RepositoryID: repositoryID}, nil
}

func projectIDFromConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read project configuration %q: %w", path, err)
	}
	configuration, err := config.LoadProject(data)
	if err != nil {
		return "", fmt.Errorf("read project configuration %q: %w", path, err)
	}
	return configuration.Project.ID, nil
}

func (r *Resolver) projectForPersistedWorkspace(ctx context.Context, dataDir, path string, registry store.Registry) (domain.Project, bool, error) {
	commonGitDir, err := r.git.CommonGitDir(ctx, path)
	if err != nil {
		return domain.Project{}, false, nil
	}
	topLevel, err := r.git.TopLevel(ctx, path)
	if err != nil {
		return domain.Project{}, false, nil
	}
	topLevel, err = filepath.EvalSymlinks(topLevel)
	if err != nil {
		return domain.Project{}, false, fmt.Errorf("canonicalize current checkout: %w", err)
	}
	var candidates []string
	for projectID, registered := range registry.Projects {
		repositoryID, found := registered.RepositoryIDs[commonGitDir]
		if !found {
			continue
		}
		matches, err := workspaceStateMatches(WorkspaceStateDirectory(dataDir, projectID), repositoryID, topLevel)
		if err != nil {
			return domain.Project{}, false, err
		}
		if matches {
			candidates = append(candidates, projectID)
		}
	}
	if len(candidates) == 0 {
		return domain.Project{}, false, nil
	}
	sort.Strings(candidates)
	if len(candidates) > 1 {
		return domain.Project{}, false, fmt.Errorf("%w: %v", ErrAmbiguousProject, candidates)
	}
	project, err := r.projectFromGitIdentity(ctx, path, registry)
	if err != nil {
		return domain.Project{}, false, err
	}
	return project, true, nil
}

func workspaceStateMatches(stateDir, repositoryID, topLevel string) (bool, error) {
	entries, err := os.ReadDir(stateDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read workspace state: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		state, err := store.ReadWorkspace(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			return false, fmt.Errorf("read workspace state %q: %w", entry.Name(), err)
		}
		if err := validateDefaultWorkspaceState(entry.Name(), state); err != nil {
			return false, err
		}
		checkout, found := state.Repositories[repositoryID]
		if !found {
			continue
		}
		checkoutPath, err := filepath.EvalSymlinks(checkout.ResolvedPath)
		if err == nil && checkoutPath == topLevel {
			return true, nil
		}
	}
	return false, nil
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("invocation path %q is not a directory", path)
	}
	return filepath.EvalSymlinks(abs)
}

func explicitConfigPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("explicit project %q: %w", path, err)
	}
	if info.IsDir() {
		abs = filepath.Join(abs, ".wtree.yml")
	}
	if filepath.Base(abs) != ".wtree.yml" {
		return "", fmt.Errorf("explicit project %q must be a directory or .wtree.yml", path)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("explicit project configuration %q: %w", abs, err)
	}
	return filepath.EvalSymlinks(abs)
}

func findConfigPath(path string) string {
	for {
		candidate := filepath.Join(path, ".wtree.yml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			canonical, err := filepath.EvalSymlinks(candidate)
			if err == nil {
				return canonical
			}
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}

func readRegistry(path string) (store.Registry, error) {
	registry, err := store.ReadRegistry(path)
	if os.IsNotExist(err) {
		return store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{}}, nil
	}
	if err != nil {
		return store.Registry{}, fmt.Errorf("read wtree project registry: %w", err)
	}
	return registry, nil
}

func (r *Resolver) projectFromGitIdentity(ctx context.Context, path string, registry store.Registry) (domain.Project, error) {
	commonGitDir, err := r.git.CommonGitDir(ctx, path)
	if err != nil {
		return domain.Project{}, ErrNoProjectContext
	}
	var candidates []string
	for projectID, project := range registry.Projects {
		if _, found := project.RepositoryIDs[commonGitDir]; found {
			candidates = append(candidates, projectID)
		}
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return domain.Project{}, ErrNoProjectContext
	}
	if len(candidates) > 1 {
		return domain.Project{}, fmt.Errorf("%w: %v", ErrAmbiguousProject, candidates)
	}
	project, found, err := r.registeredProject(ctx, candidates[0], registry)
	if err != nil {
		return domain.Project{}, err
	}
	if !found {
		registered := registry.Projects[candidates[0]]
		return domain.Project{}, fmt.Errorf("%w: project %q configuration %q is unavailable", ErrStaleRegistry, candidates[0], registered.ConfigPath)
	}
	return project, nil
}

// registeredProject loads an available registry entry only after verifying that
// its project ID and current Git identities still agree with the registry.
// A missing config is reported separately so an upward-discovered config can
// relocate the project after it has independently established the project ID.
func (r *Resolver) registeredProject(ctx context.Context, projectID string, registry store.Registry) (domain.Project, bool, error) {
	registered, exists := registry.Projects[projectID]
	if !exists {
		return domain.Project{}, false, nil
	}
	if _, err := os.Stat(registered.ConfigPath); err != nil {
		if os.IsNotExist(err) {
			return domain.Project{}, false, nil
		}
		return domain.Project{}, false, fmt.Errorf("%w: verify project %q configuration %q: %v", ErrStaleRegistry, projectID, registered.ConfigPath, err)
	}
	project, err := r.loadProject(ctx, registered.ConfigPath)
	if err != nil {
		return domain.Project{}, false, fmt.Errorf("%w: load project %q configuration: %v", ErrStaleRegistry, projectID, err)
	}
	if project.ID != projectID {
		return domain.Project{}, false, fmt.Errorf("%w: registry project %q config declares project %q", ErrStaleRegistry, projectID, project.ID)
	}
	if !sameRepositoryIDs(repositoryIDs(project), registered.RepositoryIDs) {
		return domain.Project{}, false, fmt.Errorf("%w: project %q Git identities do not match its configuration", ErrStaleRegistry, project.ID)
	}
	return project, true, nil
}

func (r *Resolver) loadProject(ctx context.Context, configPath string) (domain.Project, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return domain.Project{}, fmt.Errorf("read project configuration %q: %w", configPath, err)
	}
	configuration, err := config.LoadProject(data)
	if err != nil {
		return domain.Project{}, fmt.Errorf("read project configuration %q: %w", configPath, err)
	}
	configPath, err = filepath.EvalSymlinks(configPath)
	if err != nil {
		return domain.Project{}, fmt.Errorf("canonicalize project configuration: %w", err)
	}
	ids := make([]string, 0, len(configuration.Repositories))
	for id := range configuration.Repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	project := domain.Project{Version: configuration.Version, ID: configuration.Project.ID, Name: configuration.Project.Name, ConfigPath: configPath, Repositories: make([]domain.Repository, 0, len(ids))}
	for _, id := range ids {
		repository := configuration.Repositories[id]
		sourcePath, err := sourcePath(filepath.Dir(configPath), repository.Source)
		if err != nil {
			return domain.Project{}, fmt.Errorf("project repository %q source: %w", id, err)
		}
		commonGitDir, err := r.git.CommonGitDir(ctx, sourcePath)
		if err != nil {
			return domain.Project{}, fmt.Errorf("project repository %q source %q: %w", id, sourcePath, err)
		}
		for _, existing := range project.Repositories {
			if existing.CommonGitDir == commonGitDir {
				return domain.Project{}, fmt.Errorf("project repositories %q and %q share Git identity %q", existing.ID, id, commonGitDir)
			}
		}
		project.Repositories = append(project.Repositories, domain.Repository{ID: id, CommonGitDir: commonGitDir, SourcePath: sourcePath, ParentID: repository.Parent, DefaultMount: repository.DefaultMount, DefaultBranch: repository.DefaultBranch})
	}
	if err := project.Validate(); err != nil {
		return domain.Project{}, fmt.Errorf("validate project configuration %q: %w", configPath, err)
	}
	return project, nil
}

func sourcePath(configRoot, source string) (string, error) {
	if source == "" {
		return "", errors.New("source is required")
	}
	if filepath.IsAbs(source) {
		return "", fmt.Errorf("source %q must be relative", source)
	}
	path, err := filepath.Abs(filepath.Join(configRoot, filepath.FromSlash(source)))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

func repositoryIDs(project domain.Project) map[string]string {
	identities := make(map[string]string, len(project.Repositories))
	for _, repository := range project.Repositories {
		identities[repository.CommonGitDir] = repository.ID
	}
	return identities
}

func sameRepositoryIDs(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for identity, repositoryID := range left {
		if right[identity] != repositoryID {
			return false
		}
	}
	return true
}

func (r *Resolver) reconcileRegistry(ctx context.Context, dataDir string, project domain.Project, registry store.Registry) error {
	registered, exists := registry.Projects[project.ID]
	needsReconciliation, err := registryNeedsReconciliation(registered, exists, project)
	if err != nil {
		return err
	}
	if !needsReconciliation {
		return nil
	}
	registryLock, err := (lock.Manager{}).RegistryLock(ctx, dataDir, time.Second)
	if err != nil {
		return err
	}
	defer registryLock.Unlock()
	path := filepath.Join(dataDir, "registry.json")
	current, err := readRegistry(path)
	if err != nil {
		return err
	}
	registered, exists = current.Projects[project.ID]
	needsReconciliation, err = registryNeedsReconciliation(registered, exists, project)
	if err != nil {
		return err
	}
	if !needsReconciliation {
		return nil
	}
	registered.ConfigPath = project.ConfigPath
	registered.RepositoryIDs = repositoryIDs(project)
	current.Projects[project.ID] = registered
	if err := store.WriteRegistry(path, current); err != nil {
		return fmt.Errorf("update relocated project registry entry: %w", err)
	}
	return nil
}

func registryNeedsReconciliation(registered store.RegistryProject, exists bool, project domain.Project) (bool, error) {
	if !exists {
		return false, nil
	}
	if registered.ConfigPath == project.ConfigPath && sameRepositoryIDs(registered.RepositoryIDs, repositoryIDs(project)) {
		return false, nil
	}
	if registered.ConfigPath != project.ConfigPath {
		if _, err := os.Stat(registered.ConfigPath); err == nil {
			return false, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("verify registered project configuration %q: %w", registered.ConfigPath, err)
		}
	}
	return true, nil
}

func (r *Resolver) workspaceForCheckout(ctx context.Context, dataDir string, project domain.Project, repositoryID, topLevel string) (domain.Workspace, error) {
	topLevel, err := filepath.EvalSymlinks(topLevel)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("canonicalize current checkout: %w", err)
	}
	stateDir := WorkspaceStateDirectory(dataDir, project.ID)
	entries, err := os.ReadDir(stateDir)
	if err != nil && !os.IsNotExist(err) {
		return domain.Workspace{}, fmt.Errorf("read workspace state: %w", err)
	}
	var matches []domain.Workspace
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		state, err := store.ReadWorkspace(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			return domain.Workspace{}, fmt.Errorf("read workspace state %q: %w", entry.Name(), err)
		}
		if err := validateDefaultWorkspaceState(entry.Name(), state); err != nil {
			return domain.Workspace{}, err
		}
		workspace, err := workspaceFromState(state)
		if err != nil {
			return domain.Workspace{}, err
		}
		path, err := workspace.ResolveRepository(repositoryID)
		if err != nil {
			continue
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		if path == topLevel {
			if err := workspace.Validate(project); err != nil {
				return domain.Workspace{}, fmt.Errorf("validate workspace state %q: %w", entry.Name(), err)
			}
			matches = append(matches, workspace)
		}
	}
	if len(matches) > 1 {
		return domain.Workspace{}, fmt.Errorf("multiple workspace states match current checkout; repair workspace state")
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	workspace, err := r.defaultWorkspace(ctx, dataDir, project)
	if err != nil {
		return domain.Workspace{}, err
	}
	path, err := workspace.ResolveRepository(repositoryID)
	if err != nil || path != topLevel {
		return domain.Workspace{}, fmt.Errorf("current checkout is not part of a known workspace")
	}
	return workspace, nil
}

func workspaceFromState(state store.WorkspaceState) (domain.Workspace, error) {
	ids := make([]string, 0, len(state.Repositories))
	for repositoryID := range state.Repositories {
		ids = append(ids, repositoryID)
	}
	sort.Strings(ids)
	workspace := domain.Workspace{Version: state.Version, ID: state.ID, Name: state.Name, RootPath: state.Path, Partial: state.Partial, MissingRepositoryIDs: append([]string(nil), state.MissingRepositoryIDs...), Checkouts: make([]domain.Checkout, 0, len(ids))}
	for _, repositoryID := range ids {
		checkout := state.Repositories[repositoryID]
		workspace.Checkouts = append(workspace.Checkouts, domain.Checkout{RepositoryID: repositoryID, Branch: checkout.Branch, Mount: checkout.Mount, ResolvedPath: checkout.ResolvedPath, Head: checkout.Head, Detached: checkout.Detached})
	}
	return workspace, nil
}

func (r *Resolver) defaultWorkspace(ctx context.Context, dataDir string, project domain.Project) (domain.Workspace, error) {
	path := WorkspaceStatePath(dataDir, project.ID, "default")
	state, err := store.ReadWorkspace(path)
	if err == nil {
		if err := validateDefaultWorkspaceState(filepath.Base(path), state); err != nil {
			return domain.Workspace{}, err
		}
		workspace, err := workspaceFromState(state)
		if err != nil {
			return domain.Workspace{}, err
		}
		if err := workspace.Validate(project); err != nil {
			return domain.Workspace{}, fmt.Errorf("validate default workspace state: %w", err)
		}
		if sourceDefaultWorkspace(project, workspace) {
			return workspace, nil
		}
		return r.sourceWorkspace(ctx, project)
	}
	if !os.IsNotExist(err) {
		return domain.Workspace{}, fmt.Errorf("read default workspace state: %w", err)
	}
	return r.sourceWorkspace(ctx, project)
}

func sourceDefaultWorkspace(project domain.Project, workspace domain.Workspace) bool {
	for _, repository := range project.Repositories {
		path, err := workspace.ResolveRepository(repository.ID)
		if err != nil || path != repository.SourcePath {
			return false
		}
	}
	return true
}

func validateDefaultWorkspaceState(filename string, state store.WorkspaceState) error {
	if filename == "default.json" && (state.ID != "default" || state.Name != "default") {
		return fmt.Errorf("default workspace state must use ID and name \"default\"")
	}
	return nil
}

func (r *Resolver) sourceWorkspace(ctx context.Context, project domain.Project) (domain.Workspace, error) {
	var root string
	checkouts := make([]domain.Checkout, 0, len(project.Repositories))
	for _, repository := range project.ParentFirst() {
		if repository.ParentID == "" {
			root = repository.SourcePath
		}
		branch, detached, err := r.git.CurrentBranch(ctx, repository.SourcePath)
		if err != nil {
			return domain.Workspace{}, err
		}
		head, err := r.git.Head(ctx, repository.SourcePath)
		if err != nil {
			return domain.Workspace{}, err
		}
		checkouts = append(checkouts, domain.Checkout{RepositoryID: repository.ID, Branch: branch, Head: head, Detached: detached, Mount: repository.DefaultMount, ResolvedPath: repository.SourcePath})
	}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: root, Checkouts: checkouts}
	if err := workspace.Validate(project); err != nil {
		return domain.Workspace{}, fmt.Errorf("validate default workspace: %w", err)
	}
	return workspace, nil
}
