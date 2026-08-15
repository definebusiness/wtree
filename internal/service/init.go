// Package service contains application use cases without CLI formatting.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/discovery"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var ErrAlreadyInitialized = errors.New("project is already initialized")

type InitRequest struct {
	Path, DataDir, WorktreeRoot string
	DryRun                      bool
	Ignores                     []string
}
type InitResult struct {
	ProjectID    string
	ConfigPath   string
	Repositories []discovery.Repository
	DryRun       bool
}
type Initializer struct {
	git            gitadapter.Git
	writeRegistry  func(string, store.Registry) error
	writeWorkspace func(string, store.WorkspaceState) error
	rename         func(string, string) error
	remove         func(string) error
	// beforeLockedRegistryPreflight is a package-private test seam for the
	// registration change that can occur after the unlocked preflight.
	beforeLockedRegistryPreflight func()
}

func NewInitializer() *Initializer {
	return NewInitializerWithPublishers(store.WriteRegistry, store.WriteWorkspace, os.Rename)
}
func NewInitializerWithRegistryWriter(writer func(string, store.Registry) error) *Initializer {
	return NewInitializerWithPublishers(writer, store.WriteWorkspace, os.Rename)
}
func NewInitializerWithWriters(registryWriter func(string, store.Registry) error, workspaceWriter func(string, store.WorkspaceState) error) *Initializer {
	return NewInitializerWithPublishers(registryWriter, workspaceWriter, os.Rename)
}
func NewInitializerWithPublishers(registryWriter func(string, store.Registry) error, workspaceWriter func(string, store.WorkspaceState) error, rename func(string, string) error) *Initializer {
	return NewInitializerWithPublishersAndRemover(registryWriter, workspaceWriter, rename, os.Remove)
}
func NewInitializerWithPublishersAndRemover(registryWriter func(string, store.Registry) error, workspaceWriter func(string, store.WorkspaceState) error, rename func(string, string) error, remove func(string) error) *Initializer {
	return &Initializer{git: gitadapter.NewAdapter("git"), writeRegistry: registryWriter, writeWorkspace: workspaceWriter, rename: rename, remove: remove}
}
func (i *Initializer) Init(ctx context.Context, request InitRequest) (InitResult, error) {
	root, err := filepath.Abs(request.Path)
	if err != nil {
		return InitResult{}, err
	}
	repositories, err := discovery.Discover(root, request.Ignores)
	if err != nil {
		return InitResult{}, err
	}
	root = repositories[0].Path
	configPath := filepath.Join(root, ".wtree.yml")
	if _, err := os.Stat(configPath); err == nil {
		return InitResult{}, ErrAlreadyInitialized
	} else if !os.IsNotExist(err) {
		return InitResult{}, err
	}
	projectID := uuid.NewString()
	result := InitResult{ProjectID: projectID, ConfigPath: configPath, Repositories: repositories, DryRun: request.DryRun}
	configuration := config.ProjectConfig{Version: config.Version, Project: config.Project{ID: projectID, Name: filepath.Base(root)}, Repositories: map[string]config.Repository{}, Worktrees: config.Worktrees{Root: request.WorktreeRoot}, Discovery: config.Discovery{Ignore: request.Ignores}}
	identityMap := map[string]string{}
	defaultCheckouts := map[string]store.CheckoutState{}
	for _, repository := range repositories {
		common, err := i.git.CommonGitDir(ctx, repository.Path)
		if err != nil {
			return InitResult{}, err
		}
		if priorID, exists := identityMap[common]; exists {
			return InitResult{}, fmt.Errorf("duplicate repository identity %q for repositories %q and %q", common, priorID, repository.ID)
		}
		relative, _ := filepath.Rel(root, repository.Path)
		if repository.ID == "root" {
			relative = "."
		}
		branch, detached, err := i.git.CurrentBranch(ctx, repository.Path)
		if err != nil {
			return InitResult{}, err
		}
		head, err := i.git.Head(ctx, repository.Path)
		if err != nil {
			return InitResult{}, err
		}
		configuration.Repositories[repository.ID] = config.Repository{Source: filepath.ToSlash(relative), Parent: repository.ParentID, DefaultMount: repository.Mount, DefaultBranch: branch}
		defaultCheckouts[repository.ID] = store.CheckoutState{Branch: branch, Mount: repository.Mount, ResolvedPath: repository.Path, Head: head, Detached: detached}
		identityMap[common] = repository.ID
	}
	registryPath := filepath.Join(request.DataDir, "registry.json")
	if request.DryRun {
		registry, _, err := loadRegistry(registryPath)
		if err != nil {
			return InitResult{}, err
		}
		if err := rejectRegistrationConflicts(configPath, identityMap, registry); err != nil {
			return InitResult{}, err
		}
		return result, nil
	}
	registry, _, err := loadRegistry(registryPath)
	if err != nil {
		return InitResult{}, err
	}
	if err := rejectRegistrationConflicts(configPath, identityMap, registry); err != nil {
		return InitResult{}, err
	}
	registryLock, err := (lock.Manager{}).RegistryLock(ctx, request.DataDir, time.Second)
	if err != nil {
		return InitResult{}, err
	}
	defer registryLock.Unlock()
	if _, err := os.Stat(configPath); err == nil {
		return InitResult{}, ErrAlreadyInitialized
	} else if !os.IsNotExist(err) {
		return InitResult{}, err
	}
	if i.beforeLockedRegistryPreflight != nil {
		i.beforeLockedRegistryPreflight()
	}
	registry, hadRegistry, err := loadRegistry(registryPath)
	if err != nil {
		return InitResult{}, err
	}
	if err := rejectRegistrationConflicts(configPath, identityMap, registry); err != nil {
		return InitResult{}, err
	}
	projectLock, err := (lock.Manager{}).ProjectLock(ctx, request.DataDir, projectID, time.Second)
	if err != nil {
		return InitResult{}, err
	}
	defer projectLock.Unlock()
	previousRegistry := cloneRegistry(registry)
	registry = cloneRegistry(registry)
	if existing, ok := registry.Projects[projectID]; ok && existing.ConfigPath != configPath {
		return InitResult{}, fmt.Errorf("project ID collision for %q", projectID)
	}
	registry.Projects[projectID] = store.RegistryProject{Name: configuration.Project.Name, ConfigPath: configPath, RepositoryIDs: identityMap}
	data, err := yaml.Marshal(configuration)
	if err != nil {
		return InitResult{}, err
	}
	temporary, err := os.CreateTemp(root, ".wtree.yml-*")
	if err != nil {
		return InitResult{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = temporary.Write(data); err != nil {
		temporary.Close()
		return InitResult{}, err
	}
	if err = fsutil.Sync(temporary); err != nil {
		temporary.Close()
		return InitResult{}, err
	}
	if err = temporary.Close(); err != nil {
		return InitResult{}, err
	}
	if err = i.writeRegistry(registryPath, registry); err != nil {
		return InitResult{}, initPublicationError("publish project registry", err, i.rollbackRegistry(registryPath, previousRegistry, hadRegistry))
	}
	if err = i.rename(temporaryName, configPath); err != nil {
		return InitResult{}, initPublicationError("publish project config", err, i.rollbackInit(configPath, "", registryPath, previousRegistry, hadRegistry))
	}
	workspacePath := WorkspaceStatePath(request.DataDir, projectID, "default")
	if err := i.writeWorkspace(workspacePath, store.WorkspaceState{Version: store.Version, ID: "default", Name: "default", Path: root, Repositories: defaultCheckouts}); err != nil {
		return InitResult{}, initPublicationError("publish default workspace", err, i.rollbackInit(configPath, workspacePath, registryPath, previousRegistry, hadRegistry))
	}
	return result, nil
}

func rejectRegistrationConflicts(configPath string, repositoryIDs map[string]string, registry store.Registry) error {
	identities := make([]string, 0, len(repositoryIDs))
	for identity := range repositoryIDs {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	candidates := make([]RegistrationConflictCandidate, 0, len(registry.Projects))
	for id, project := range registry.Projects {
		registeredIdentities := make([]string, 0, len(project.RepositoryIDs))
		for identity := range project.RepositoryIDs {
			registeredIdentities = append(registeredIdentities, identity)
		}
		candidates = append(candidates, RegistrationConflictCandidate{ID: id, ConfigPath: project.ConfigPath, RepositoryIdentities: registeredIdentities})
	}
	if ids := RegistrationConflictIDs(configPath, identities, candidates); len(ids) != 0 {
		return NewError(ErrorConflict, fmt.Errorf("project registration conflicts with existing project IDs %s; inspect registrations with `wtree project list`", strings.Join(ids, ", ")))
	}
	return nil
}

func initPublicationError(operation string, cause, cleanup error) error {
	if cleanup != nil {
		return fmt.Errorf("%s: %w; cleanup failed: %v", operation, cause, cleanup)
	}
	return fmt.Errorf("%s: %w", operation, cause)
}

func loadRegistry(path string) (store.Registry, bool, error) {
	registry, err := store.ReadRegistry(path)
	if err == nil {
		return registry, true, nil
	}
	if os.IsNotExist(err) {
		return store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{}}, false, nil
	}
	return store.Registry{}, false, err
}

func cloneRegistry(value store.Registry) store.Registry {
	clone := store.Registry{Version: value.Version, Projects: make(map[string]store.RegistryProject, len(value.Projects))}
	for projectID, project := range value.Projects {
		identities := make(map[string]string, len(project.RepositoryIDs))
		for commonGitDir, repositoryID := range project.RepositoryIDs {
			identities[commonGitDir] = repositoryID
		}
		project.RepositoryIDs = identities
		clone.Projects[projectID] = project
	}
	return clone
}

func (i *Initializer) rollbackRegistry(path string, previous store.Registry, hadRegistry bool) error {
	if hadRegistry {
		return i.writeRegistry(path, previous)
	}
	if err := i.remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (i *Initializer) rollbackInit(configPath, workspacePath, registryPath string, previous store.Registry, hadRegistry bool) error {
	var cleanup []error
	if workspacePath != "" {
		if err := i.remove(workspacePath); err != nil && !os.IsNotExist(err) {
			cleanup = append(cleanup, fmt.Errorf("remove default workspace state: %w", err))
		}
	}
	if configPath != "" {
		if err := i.remove(configPath); err != nil && !os.IsNotExist(err) {
			cleanup = append(cleanup, fmt.Errorf("remove project config: %w", err))
		}
	}
	if err := i.rollbackRegistry(registryPath, previous, hadRegistry); err != nil {
		cleanup = append(cleanup, fmt.Errorf("restore project registry: %w", err))
	}
	return errors.Join(cleanup...)
}
