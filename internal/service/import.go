package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/store"
)

// ImportRequest describes an existing checkout tree to record. Import only
// observes Git facts; it never creates branches or rewrites checkouts.
type ImportRequest struct {
	Path         string
	Name         string
	AllowPartial bool
	DataDir      string
}

// ImportPlan is the serializable observation that becomes workspace state.
type ImportPlan struct {
	ProjectID            string             `json:"projectId"`
	WorkspaceID          string             `json:"workspaceId"`
	WorkspaceName        string             `json:"workspaceName"`
	RootPath             string             `json:"rootPath"`
	Partial              bool               `json:"partial,omitempty"`
	MissingRepositoryIDs []string           `json:"missingRepositoryIds,omitempty"`
	Repositories         []ImportRepository `json:"repositories"`
}

type ImportRepository struct {
	ID       string `json:"id"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head"`
	Detached bool   `json:"detached,omitempty"`
	Mount    string `json:"mount"`
	Path     string `json:"path"`
}

type WorkspaceImporter struct {
	git            gitadapter.Git
	locker         ProjectLocker
	writeWorkspace func(string, store.WorkspaceState) error
	lockTimeout    time.Duration
}

func NewWorkspaceImporter() *WorkspaceImporter {
	return NewWorkspaceImporterWith(gitadapter.NewAdapter("git"), lock.Manager{}, store.WriteWorkspace)
}

func NewWorkspaceImporterWith(git gitadapter.Git, locker ProjectLocker, writeWorkspace func(string, store.WorkspaceState) error) *WorkspaceImporter {
	return &WorkspaceImporter{git: git, locker: locker, writeWorkspace: writeWorkspace, lockTimeout: time.Second}
}

// PlanImport discovers actual worktrees by configured common Git identity and
// derives mounts from their observed parent-relative placement.
func (i *WorkspaceImporter) PlanImport(ctx context.Context, project domain.Project, request ImportRequest) (ImportPlan, error) {
	if i == nil || i.git == nil {
		return ImportPlan{}, NewError(ErrorInternal, errors.New("workspace importer is not configured"))
	}
	if err := project.Validate(); err != nil {
		return ImportPlan{}, NewError(ErrorValidation, fmt.Errorf("validate project: %w", err))
	}
	root, err := i.importRoot(ctx, project, request.Path)
	if err != nil {
		return ImportPlan{}, err
	}
	observed, err := i.observeImportCheckouts(ctx, project, root)
	if err != nil {
		return ImportPlan{}, err
	}
	name := request.Name
	if name == "" {
		var inferred bool
		name, inferred = inferImportName(root, observed)
		if !inferred {
			return ImportPlan{}, NewError(ErrorValidation, errors.New("cannot infer one workspace name from divergent or detached checkouts; provide --name"))
		}
	}
	value := ImportPlan{ProjectID: project.ID, WorkspaceID: pathutil.StorageName(name), WorkspaceName: name, RootPath: root, Repositories: make([]ImportRepository, 0, len(observed))}
	for _, repository := range project.ParentFirst() {
		checkout, found := observed[repository.ID]
		if !found {
			value.MissingRepositoryIDs = append(value.MissingRepositoryIDs, repository.ID)
			continue
		}
		mount := "."
		if repository.ParentID != "" {
			parent, found := observed[repository.ParentID]
			if !found {
				return ImportPlan{}, NewError(ErrorValidation, fmt.Errorf("repository %q was found but configured parent %q is missing", repository.ID, repository.ParentID))
			}
			relative, err := filepath.Rel(parent.Path, checkout.Path)
			if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return ImportPlan{}, NewError(ErrorValidation, fmt.Errorf("repository %q at %q is not nested under configured parent %q at %q", repository.ID, checkout.Path, repository.ParentID, parent.Path))
			}
			mount = filepath.ToSlash(relative)
		}
		value.Repositories = append(value.Repositories, ImportRepository{ID: repository.ID, Branch: checkout.Branch, Head: checkout.Head, Detached: checkout.Detached, Mount: mount, Path: checkout.Path})
	}
	sort.Strings(value.MissingRepositoryIDs)
	if len(value.MissingRepositoryIDs) > 0 && !request.AllowPartial {
		return ImportPlan{}, NewError(ErrorValidation, fmt.Errorf("workspace contains %d of %d configured repositories; missing %s (use --allow-partial to record this explicitly)", len(value.Repositories), len(project.Repositories), strings.Join(value.MissingRepositoryIDs, ", ")))
	}
	value.Partial = len(value.MissingRepositoryIDs) > 0
	if err := importPlanWorkspace(value).Validate(project); err != nil {
		return ImportPlan{}, NewError(ErrorValidation, fmt.Errorf("validate imported workspace: %w", err))
	}
	if err := checkImportCollision(project, request.DataDir, value); err != nil {
		return ImportPlan{}, err
	}
	return value, nil
}

// Import writes exactly the previously validated observation under the project
// lock. Re-discovery under the lock prevents stale facts from being persisted.
func (i *WorkspaceImporter) Import(ctx context.Context, project domain.Project, request ImportRequest) (ImportPlan, error) {
	value, err := i.PlanImport(ctx, project, request)
	if err != nil {
		return ImportPlan{}, err
	}
	if request.DataDir == "" {
		return ImportPlan{}, NewError(ErrorValidation, errors.New("data directory is required"))
	}
	if i.locker == nil || i.writeWorkspace == nil {
		return ImportPlan{}, NewError(ErrorInternal, errors.New("workspace importer is not configured"))
	}
	handle, err := i.locker.ProjectLock(ctx, request.DataDir, project.ID, i.lockTimeout)
	if err != nil {
		return ImportPlan{}, NewError(ErrorConflict, fmt.Errorf("acquire project mutation lock: %w", err))
	}
	defer handle.Unlock()
	revalidated, err := i.PlanImport(ctx, project, request)
	if err != nil {
		return ImportPlan{}, err
	}
	if !importPlansEqual(value, revalidated) {
		return ImportPlan{}, NewError(ErrorConflict, errors.New("import observations changed during locked revalidation"))
	}
	state := importWorkspaceState(value)
	if err := i.writeWorkspace(WorkspaceStatePath(request.DataDir, project.ID, value.WorkspaceID), state); err != nil {
		return ImportPlan{}, NewError(ErrorInternal, fmt.Errorf("persist imported workspace state: %w", err))
	}
	return value, nil
}

type importCheckout struct {
	Path     string
	Branch   string
	Head     string
	Detached bool
}

func (i *WorkspaceImporter) importRoot(ctx context.Context, project domain.Project, path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", NewError(ErrorValidation, fmt.Errorf("resolve import path: %w", err))
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return "", NewError(ErrorValidation, fmt.Errorf("import path %q: %w", path, err))
	}
	rootIdentity := ""
	for _, repository := range project.Repositories {
		if repository.ParentID == "" {
			rootIdentity = repository.CommonGitDir
			break
		}
	}
	for candidate := abs; ; candidate = filepath.Dir(candidate) {
		topLevel, err := i.git.TopLevel(ctx, candidate)
		if err == nil {
			identity, identityErr := i.git.CommonGitDir(ctx, topLevel)
			if identityErr == nil && identity == rootIdentity {
				canonical, canonicalErr := filepath.EvalSymlinks(topLevel)
				if canonicalErr != nil {
					return "", NewError(ErrorValidation, fmt.Errorf("canonicalize import root: %w", canonicalErr))
				}
				return canonical, nil
			}
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	return "", NewError(ErrorValidation, fmt.Errorf("import path %q is not inside a checkout of the configured root repository", path))
}

func (i *WorkspaceImporter) observeImportCheckouts(ctx context.Context, project domain.Project, root string) (map[string]importCheckout, error) {
	identities := make(map[string]string, len(project.Repositories))
	for _, repository := range project.Repositories {
		identities[repository.CommonGitDir] = repository.ID
	}
	observed := make(map[string]importCheckout, len(project.Repositories))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.IsDir() || !hasGitMarker(path) {
			return nil
		}
		topLevel, err := i.git.TopLevel(ctx, path)
		if err != nil || !sameCheckoutPath(topLevel, path) {
			return nil
		}
		identity, err := i.git.CommonGitDir(ctx, path)
		if err != nil {
			return fmt.Errorf("read Git identity at %q: %w", path, err)
		}
		repositoryID, known := identities[identity]
		if !known {
			return NewError(ErrorValidation, fmt.Errorf("unknown Git repository at %q", path))
		}
		if prior, exists := observed[repositoryID]; exists {
			return NewError(ErrorValidation, fmt.Errorf("configured repository %q appears more than once at %q and %q", repositoryID, prior.Path, path))
		}
		branch, detached, err := i.git.CurrentBranch(ctx, path)
		if err != nil {
			return fmt.Errorf("read branch at %q: %w", path, err)
		}
		head, err := i.git.Head(ctx, path)
		if err != nil {
			return fmt.Errorf("read HEAD at %q: %w", path, err)
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("canonicalize checkout %q: %w", path, err)
		}
		observed[repositoryID] = importCheckout{Path: canonical, Branch: branch, Head: head, Detached: detached}
		return nil
	})
	if err != nil {
		var application *Error
		if errors.As(err, &application) {
			return nil, err
		}
		return nil, NewError(ErrorGit, fmt.Errorf("scan import path %q: %w", root, err))
	}
	return observed, nil
}

func hasGitMarker(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func inferImportName(root string, observed map[string]importCheckout) (string, bool) {
	if len(observed) <= 1 {
		for _, checkout := range observed {
			if !checkout.Detached && checkout.Branch != "" {
				return checkout.Branch, true
			}
		}
		return filepath.Base(root), true
	}
	branch := ""
	for _, checkout := range observed {
		if checkout.Detached || checkout.Branch == "" {
			return "", false
		}
		if branch == "" {
			branch = checkout.Branch
			continue
		}
		if branch != checkout.Branch {
			return "", false
		}
	}
	if branch != "" {
		return branch, true
	}
	return "", false
}

func importPlanWorkspace(value ImportPlan) domain.Workspace {
	checkouts := make([]domain.Checkout, 0, len(value.Repositories))
	for _, repository := range value.Repositories {
		checkouts = append(checkouts, domain.Checkout{RepositoryID: repository.ID, Branch: repository.Branch, Head: repository.Head, Detached: repository.Detached, Mount: repository.Mount, ResolvedPath: repository.Path})
	}
	return domain.Workspace{Version: domain.CurrentVersion, ID: value.WorkspaceID, Name: value.WorkspaceName, RootPath: value.RootPath, Partial: value.Partial, MissingRepositoryIDs: append([]string(nil), value.MissingRepositoryIDs...), Checkouts: checkouts}
}

func importWorkspaceState(value ImportPlan) store.WorkspaceState {
	workspace := importPlanWorkspace(value)
	repositories := make(map[string]store.CheckoutState, len(workspace.Checkouts))
	for _, checkout := range workspace.Checkouts {
		repositories[checkout.RepositoryID] = store.CheckoutState{Branch: checkout.Branch, Head: checkout.Head, Detached: checkout.Detached, Mount: checkout.Mount, ResolvedPath: checkout.ResolvedPath}
	}
	return store.WorkspaceState{Version: store.Version, ID: workspace.ID, Name: workspace.Name, Path: workspace.RootPath, Partial: workspace.Partial, MissingRepositoryIDs: workspace.MissingRepositoryIDs, Repositories: repositories}
}

func checkImportCollision(project domain.Project, dataDir string, value ImportPlan) error {
	if dataDir == "" {
		return NewError(ErrorValidation, errors.New("data directory is required"))
	}
	entries, err := os.ReadDir(WorkspaceStateDirectory(dataDir, project.ID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return NewError(ErrorInternal, fmt.Errorf("read workspace state directory: %w", err))
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		state, err := store.ReadWorkspace(filepath.Join(WorkspaceStateDirectory(dataDir, project.ID), entry.Name()))
		if err != nil {
			return NewError(ErrorValidation, fmt.Errorf("read workspace state %q: %w", entry.Name(), err))
		}
		if state.Name == value.WorkspaceName || state.ID == value.WorkspaceID || sameCheckoutPath(state.Path, value.RootPath) {
			return NewError(ErrorConflict, fmt.Errorf("workspace %q or import root %q is already registered", value.WorkspaceName, value.RootPath))
		}
	}
	return nil
}

func importPlansEqual(left, right ImportPlan) bool {
	if left.ProjectID != right.ProjectID || left.WorkspaceID != right.WorkspaceID || left.WorkspaceName != right.WorkspaceName || left.RootPath != right.RootPath || left.Partial != right.Partial || !sameStringSlice(left.MissingRepositoryIDs, right.MissingRepositoryIDs) || len(left.Repositories) != len(right.Repositories) {
		return false
	}
	for index := range left.Repositories {
		if left.Repositories[index] != right.Repositories[index] {
			return false
		}
	}
	return true
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
