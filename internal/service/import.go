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

	"github.com/definebusiness/wtree/internal/discovery"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
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
	LogicalRoot          string             `json:"logicalRoot"`
	BaseRepository       string             `json:"baseRepository"`
	Partial              bool               `json:"partial,omitempty"`
	MissingRepositoryIDs []string           `json:"missingRepositoryIds,omitempty"`
	Repositories         []ImportRepository `json:"repositories"`
}

type ImportRepository struct {
	ID           string `json:"id"`
	ParentID     string `json:"parentId,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Head         string `json:"head"`
	Detached     bool   `json:"detached,omitempty"`
	Mount        string `json:"mount"`
	Path         string `json:"path"`
	ResolvedPath string `json:"resolvedPath"`
}

// importStateReceipt is proof that one writer invocation published one exact
// regular-file generation. A zero receipt never authorizes deletion.
type importStateReceipt struct{ snapshot cloneFileSnapshot }

type importWorkspaceWriter func(string, store.WorkspaceState) (importStateReceipt, error)

type workspaceImporterDependencies struct {
	Git              gitadapter.Git
	Locker           ProjectLocker
	WriteWorkspace   importWorkspaceWriter
	WriteRecoveryCAS func(string, store.RecoveryRecord, func() error) error
	RemoveFileCAS    func(string, func() error) error
}

type WorkspaceImporter struct {
	git              gitadapter.Git
	locker           ProjectLocker
	writeWorkspace   importWorkspaceWriter
	writeRecoveryCAS func(string, store.RecoveryRecord, func() error) error
	removeFileCAS    func(string, func() error) error
	lockTimeout      time.Duration
}

func NewWorkspaceImporter() *WorkspaceImporter {
	return newWorkspaceImporterWithDependencies(workspaceImporterDependencies{})
}

func NewWorkspaceImporterWith(git gitadapter.Git, locker ProjectLocker, writeWorkspace func(string, store.WorkspaceState) error) *WorkspaceImporter {
	return newWorkspaceImporterWithDependencies(workspaceImporterDependencies{Git: git, Locker: locker, WriteWorkspace: importWorkspaceWriterFor(writeWorkspace)})
}

func newWorkspaceImporterWithDependencies(dependencies workspaceImporterDependencies) *WorkspaceImporter {
	if dependencies.Git == nil {
		dependencies.Git = gitadapter.NewAdapter("git")
	}
	if dependencies.Locker == nil {
		dependencies.Locker = lock.Manager{}
	}
	if dependencies.WriteWorkspace == nil {
		dependencies.WriteWorkspace = importWorkspaceWriterFor(store.WriteWorkspace)
	}
	if dependencies.WriteRecoveryCAS == nil {
		dependencies.WriteRecoveryCAS = store.WriteRecoveryCAS
	}
	if dependencies.RemoveFileCAS == nil {
		dependencies.RemoveFileCAS = func(path string, compare func() error) error {
			if compare != nil {
				if err := compare(); err != nil {
					return err
				}
			}
			return os.Remove(path)
		}
	}
	return &WorkspaceImporter{
		git:              dependencies.Git,
		locker:           dependencies.Locker,
		writeWorkspace:   dependencies.WriteWorkspace,
		writeRecoveryCAS: dependencies.WriteRecoveryCAS,
		removeFileCAS:    dependencies.RemoveFileCAS,
		lockTimeout:      time.Second,
	}
}

// importWorkspaceWriterFor converts the established store writer into a
// receipt-bearing boundary. A durability error after atomic replacement still
// receives a receipt; an arbitrary error does not.
func importWorkspaceWriterFor(write func(string, store.WorkspaceState) error) importWorkspaceWriter {
	return func(path string, state store.WorkspaceState) (importStateReceipt, error) {
		if write == nil {
			return importStateReceipt{}, errors.New("workspace state writer is not configured")
		}
		writeErr := write(path, state)
		if writeErr != nil && !fsutil.ReplacementCompleted(writeErr) {
			return importStateReceipt{}, writeErr
		}
		receipt, receiptErr := captureImportStateReceipt(path, state)
		return receipt, errors.Join(writeErr, receiptErr)
	}
}

func captureImportStateReceipt(path string, state store.WorkspaceState) (importStateReceipt, error) {
	expected, err := store.WorkspaceBytes(state)
	if err != nil {
		return importStateReceipt{}, err
	}
	snapshot, err := secureCloneFileSnapshot(path)
	if err != nil {
		return importStateReceipt{}, err
	}
	if snapshot.path != path || !cloneSnapshotHasExactBytes(snapshot, expected, 0o600) {
		return importStateReceipt{}, NewError(ErrorConflict, errors.New("workspace state writer did not publish the expected regular-file generation"))
	}
	return importStateReceipt{snapshot: snapshot}, nil
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
	if len(project.Repositories) > 1 && request.Path != "" && !hasObservedTopLevel(project, observed) {
		return ImportPlan{}, NewError(ErrorValidation, errors.New("explicit import root does not contain a declared top-level repository"))
	}
	name := request.Name
	if name == "" {
		var inferred bool
		name, inferred = inferImportName(root, observed)
		if !inferred {
			return ImportPlan{}, NewError(ErrorValidation, errors.New("cannot infer one workspace name from divergent or detached checkouts; provide --name"))
		}
	}
	value := ImportPlan{ProjectID: project.ID, WorkspaceID: pathutil.StorageName(name), WorkspaceName: name, RootPath: root, LogicalRoot: root, BaseRepository: project.BaseRepository, Repositories: make([]ImportRepository, 0, len(observed))}
	for _, repository := range project.ParentFirst() {
		checkout, found := observed[repository.ID]
		if !found {
			value.MissingRepositoryIDs = append(value.MissingRepositoryIDs, repository.ID)
			continue
		}
		mount := "."
		if repository.ParentID == "" {
			relative, err := filepath.Rel(root, checkout.Path)
			if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
				return ImportPlan{}, NewError(ErrorValidation, fmt.Errorf("top-level repository %q at %q is not below logical root %q", repository.ID, checkout.Path, root))
			}
			mount = filepath.ToSlash(relative)
		} else {
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
		value.Repositories = append(value.Repositories, ImportRepository{ID: repository.ID, ParentID: repository.ParentID, Branch: checkout.Branch, Head: checkout.Head, Detached: checkout.Detached, Mount: mount, Path: checkout.Path, ResolvedPath: checkout.Path})
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

func hasObservedTopLevel(project domain.Project, observed map[string]importCheckout) bool {
	for _, repository := range project.Repositories {
		if repository.ParentID == "" {
			if _, found := observed[repository.ID]; found {
				return true
			}
		}
	}
	return false
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
	if i.locker == nil || i.writeWorkspace == nil || i.writeRecoveryCAS == nil || i.removeFileCAS == nil {
		return ImportPlan{}, NewError(ErrorInternal, errors.New("workspace importer is not configured"))
	}
	handle, err := acquireProjectMutationAuthority(ctx, i.locker, request.DataDir, project.ID, i.lockTimeout)
	if err != nil {
		return ImportPlan{}, err
	}
	defer handle.Unlock()
	revalidated, err := i.PlanImport(ctx, project, request)
	if err != nil {
		return ImportPlan{}, err
	}
	if !importPlansEqual(value, revalidated) {
		return ImportPlan{}, NewError(ErrorConflict, errors.New("import observations changed during locked revalidation"))
	}
	recoveryPath := importRecoveryRecordPath(request.DataDir, project.ID, value.WorkspaceID)
	if err := requireMissingImportPublication(recoveryPath, "recovery record"); err != nil {
		return ImportPlan{}, err
	}
	statePath := WorkspaceStatePath(request.DataDir, project.ID, value.WorkspaceID)
	if err := requireMissingImportPublication(statePath, "workspace state"); err != nil {
		return ImportPlan{}, err
	}
	state := importWorkspaceState(value)
	receipt, writeErr := i.writeWorkspace(statePath, state)
	if writeErr != nil {
		return ImportPlan{}, i.finishImportStatePublicationFailure(request.DataDir, value, state, receipt, writeErr)
	}
	if err := validateImportStateReceipt(statePath, state, receipt); err != nil {
		return ImportPlan{}, i.finishImportStatePublicationFailure(request.DataDir, value, state, receipt, err)
	}
	return value, nil
}

func requireMissingImportPublication(path, label string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return NewError(ErrorInternal, fmt.Errorf("inspect import %s %q: %w", label, path, err))
	}
	return NewError(ErrorConflict, fmt.Errorf("import %s already exists at %q", label, path))
}

func importRecoveryRecordPath(dataDir, projectID, workspaceID string) string {
	return filepath.Join(dataDir, "projects", projectID, "recovery", workspaceID+".json")
}

func validateImportStateReceipt(path string, state store.WorkspaceState, receipt importStateReceipt) error {
	expected, err := store.WorkspaceBytes(state)
	if err != nil {
		return err
	}
	if receipt.snapshot.path != path || !cloneSnapshotHasExactBytes(receipt.snapshot, expected, 0o600) {
		return errors.New("workspace state publication has no exact owned receipt")
	}
	if err := revalidateCloneFileSnapshot(receipt.snapshot); err != nil {
		return fmt.Errorf("revalidate imported workspace state receipt: %w", err)
	}
	return nil
}

func (i *WorkspaceImporter) rollbackImportStatePublication(path string, state store.WorkspaceState, receipt importStateReceipt) error {
	_, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return fmt.Errorf("inspect failed import state publication: %w", statErr)
	}
	if err := validateImportStateReceipt(path, state, receipt); err != nil {
		return fmt.Errorf("preserving unowned import state generation: %w", err)
	}
	if err := i.removeFileCAS(path, func() error { return validateImportStateReceipt(path, state, receipt) }); err != nil {
		return fmt.Errorf("remove exact import state generation: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("workspace state was recreated after exact import cleanup; preserving it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("verify exact import state cleanup: %w", err)
	}
	return nil
}

func (i *WorkspaceImporter) finishImportStatePublicationFailure(dataDir string, value ImportPlan, state store.WorkspaceState, receipt importStateReceipt, cause error) error {
	statePath := WorkspaceStatePath(dataDir, value.ProjectID, value.WorkspaceID)
	cleanupErr := i.rollbackImportStatePublication(statePath, state, receipt)
	if cleanupErr == nil {
		return NewError(ErrorInternal, fmt.Errorf("persist imported workspace state: %w", cause))
	}
	recoveryPath := importRecoveryRecordPath(dataDir, value.ProjectID, value.WorkspaceID)
	recovery := store.RecoveryRecord{
		Version:         store.Version,
		ProjectID:       value.ProjectID,
		WorkspaceID:     value.WorkspaceID,
		Operation:       "import",
		FailedStep:      "commit-state",
		UnrevertedSteps: []string{"commit-state"},
		RollbackFailures: []store.RollbackFailure{{
			Step:  "commit-state",
			Error: cleanupErr.Error(),
		}},
	}
	recoveryErr := i.writeRecoveryCAS(recoveryPath, recovery, func() error {
		return requireMissingImportPublication(recoveryPath, "recovery record")
	})
	combined := errors.Join(fmt.Errorf("persist imported workspace state: %w", cause), cleanupErr)
	if recoveryErr != nil {
		combined = errors.Join(combined, fmt.Errorf("write import recovery record %q: %w", recoveryPath, recoveryErr))
	} else {
		combined = errors.Join(combined, fmt.Errorf("import recovery metadata: %q", recoveryPath))
	}
	return NewError(ErrorRollbackIncomplete, combined)
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
	// A non-checkout directory is an explicit logical-root boundary. Discovery
	// remains confined to it; validation later requires declared top-level Git
	// identities rather than guessing a parent from a base checkout.
	topLevel, err := i.git.TopLevel(ctx, abs)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", NewError(ErrorValidation, ctxErr)
		}
		canonical, canonicalErr := filepath.EvalSymlinks(abs)
		if canonicalErr != nil {
			return "", NewError(ErrorValidation, fmt.Errorf("canonicalize import root: %w", canonicalErr))
		}
		return canonical, nil
	}
	checkout, err := filepath.EvalSymlinks(topLevel)
	if err != nil {
		return "", NewError(ErrorValidation, fmt.Errorf("canonicalize import checkout: %w", err))
	}
	identity, err := i.git.CommonGitDir(ctx, checkout)
	if err != nil {
		return "", NewError(ErrorGit, fmt.Errorf("read Git identity at %q: %w", checkout, err))
	}
	repository, err := importRepositoryForIdentity(project, identity)
	if err != nil {
		return "", NewError(ErrorValidation, fmt.Errorf("import path %q: %w", path, err))
	}
	root, err := importLogicalRootForCheckout(project, repository.ID, checkout)
	if err != nil {
		return "", NewError(ErrorValidation, fmt.Errorf("import path %q: %w", path, err))
	}
	return root, nil
}

// importRepositoryForIdentity maps one nearest checkout identity to exactly
// one declared repository. A shared or unknown common Git directory is not
// enough evidence to infer a logical root.
func importRepositoryForIdentity(project domain.Project, identity string) (domain.Repository, error) {
	matched := domain.Repository{}
	for _, repository := range project.Repositories {
		if !sameCheckoutPath(repository.CommonGitDir, identity) {
			continue
		}
		if matched.ID != "" {
			return domain.Repository{}, fmt.Errorf("Git identity %q matches both %q and %q", identity, matched.ID, repository.ID)
		}
		matched = repository
	}
	if matched.ID == "" {
		return domain.Repository{}, fmt.Errorf("Git identity %q is not configured", identity)
	}
	return matched, nil
}

// importLogicalRootForCheckout inverts the declared parent-relative mount
// chain from the nearest checkout. It neither searches ancestors nor assumes
// that the configured base is a parent of sibling top-level repositories.
func importLogicalRootForCheckout(project domain.Project, repositoryID, checkout string) (string, error) {
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	relativeParts := make([]string, 0)
	for currentID := repositoryID; currentID != ""; {
		repository, found := repositories[currentID]
		if !found {
			return "", fmt.Errorf("repository %q is not configured", currentID)
		}
		if repository.DefaultMount != "." {
			relativeParts = append([]string{repository.DefaultMount}, relativeParts...)
		}
		currentID = repository.ParentID
	}
	relative := filepath.FromSlash(strings.Join(relativeParts, "/"))
	root := checkout
	if relative != "" {
		for range strings.Split(relative, string(filepath.Separator)) {
			parent := filepath.Dir(root)
			if parent == root {
				return "", fmt.Errorf("declared mount chain %q cannot invert checkout %q", relative, checkout)
			}
			root = parent
		}
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize inferred logical root: %w", err)
	}
	expectedCheckout := canonicalRoot
	if relative != "" {
		expectedCheckout = filepath.Join(canonicalRoot, relative)
	}
	if filepath.Clean(expectedCheckout) != filepath.Clean(checkout) {
		return "", fmt.Errorf("checkout %q does not match declared mount chain %q", checkout, filepath.ToSlash(relative))
	}
	return canonicalRoot, nil
}

func (i *WorkspaceImporter) observeImportCheckouts(ctx context.Context, project domain.Project, root string) (map[string]importCheckout, error) {
	identities := make(map[string]string, len(project.Repositories))
	for _, repository := range project.Repositories {
		identities[repository.CommonGitDir] = repository.ID
	}
	observed := make(map[string]importCheckout, len(project.Repositories))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		if path != root && entry.IsDir() && discovery.ShouldIgnorePath(relative, entry.Name(), project.DiscoveryIgnores) {
			return filepath.SkipDir
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			target, statErr := os.Stat(path)
			if statErr != nil {
				if os.IsNotExist(statErr) {
					return nil
				}
				return statErr
			}
			if target.IsDir() {
				return NewError(ErrorValidation, fmt.Errorf("import path contains symbolic-link directory %q", path))
			}
			return nil
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
	if left.ProjectID != right.ProjectID || left.WorkspaceID != right.WorkspaceID || left.WorkspaceName != right.WorkspaceName || left.RootPath != right.RootPath || left.LogicalRoot != right.LogicalRoot || left.BaseRepository != right.BaseRepository || left.Partial != right.Partial || !sameStringSlice(left.MissingRepositoryIDs, right.MissingRepositoryIDs) || len(left.Repositories) != len(right.Repositories) {
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
