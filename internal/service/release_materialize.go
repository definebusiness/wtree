package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
)

// ReleaseMaterializeRequest contains only local paths and process environment.
// Authentication material stays inside the Git child process environment.
type ReleaseMaterializeRequest struct {
	LockPath string
	DataDir  string
	DryRun   bool
}

type ReleaseMaterializeRepositoryResult struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	Expected string `json:"expectedRevision"`
	Observed string `json:"observedRevision"`
	Path     string `json:"path"`
}

type ReleaseMaterializeResult struct {
	Version        int                                  `json:"version"`
	Operation      string                               `json:"operation"`
	Status         string                               `json:"status"`
	ProjectID      string                               `json:"projectId"`
	ReleaseName    string                               `json:"releaseName"`
	LockPath       string                               `json:"lockPath"`
	ManifestSHA256 string                               `json:"manifestSha256"`
	DryRun         bool                                 `json:"dryRun"`
	Repositories   []ReleaseMaterializeRepositoryResult `json:"repositories"`
}

// ReleaseMaterializeService reconstructs children around, and never mutates,
// a caller-provided base checkout.
type ReleaseMaterializeService struct {
	git *gitadapter.Adapter
	// The following seams are deliberately package-private: they bracket the
	// final publication boundary for deterministic ownership/rollback tests.
	beforePublish            func() error
	afterPublish             func(string) error
	removeAll                func(string) error
	beforeStaging            func() error
	beforeStagingQuarantine  func(string) error
	beforeChildQuarantine    func(string) error
	beforeGroupingQuarantine func(string) error
	beforeFileRemoval        func(string) error
	wrapStagingLease         func(cloneStagingLease) cloneStagingLease
	removeStagingQuarantine  func(string) error
	writeCAS                 func(cloneFileSnapshot, []byte, func() error) (ClonePublicationReceipt, error)
	registrationCandidates   func(context.Context, string, store.Registry) []RegistrationConflictCandidate
}

func NewReleaseMaterializeService() *ReleaseMaterializeService {
	return &ReleaseMaterializeService{git: gitadapter.NewAdapter("git"), removeAll: os.RemoveAll}
}

func (s *ReleaseMaterializeService) Materialize(ctx context.Context, q ReleaseMaterializeRequest) (result ReleaseMaterializeResult, returnErr error) {
	if s == nil || s.git == nil {
		return ReleaseMaterializeResult{}, NewError(ErrorInternal, errors.New("release materialize service is not configured"))
	}
	if err := ctx.Err(); err != nil {
		return ReleaseMaterializeResult{}, err
	}
	if q.LockPath == "" || q.DataDir == "" {
		return ReleaseMaterializeResult{}, NewError(ErrorInvalidArguments, errors.New("lock path and data directory are required"))
	}
	lockPath, err := filepath.Abs(q.LockPath)
	if err != nil {
		return ReleaseMaterializeResult{}, NewError(ErrorValidation, fmt.Errorf("resolve lock path: %w", err))
	}
	base := filepath.Dir(lockPath)
	if filepath.Base(lockPath) != ReleaseLockFilename {
		return ReleaseMaterializeResult{}, NewError(ErrorValidation, errors.New("release materialize requires project.wtree.lock.yml"))
	}
	manifestPath := filepath.Join(base, "project.wtree.yml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return ReleaseMaterializeResult{}, NewError(ErrorValidation, errors.New("portable manifest is unavailable from the base checkout"))
	}
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		return ReleaseMaterializeResult{}, NewError(ErrorValidation, errors.New("release lock is unavailable from the base checkout"))
	}
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		return ReleaseMaterializeResult{}, NewError(ErrorValidation, fmt.Errorf("load portable manifest: %w", err))
	}
	locked, err := config.LoadReleaseLock(lockBytes)
	if err != nil {
		return ReleaseMaterializeResult{}, NewError(ErrorValidation, fmt.Errorf("load release lock: %w", err))
	}
	nonBaseIDs := make([]string, 0, len(manifest.Repositories)-1)
	for id := range manifest.Repositories {
		if id != manifest.Project.BaseRepository {
			nonBaseIDs = append(nonBaseIDs, id)
		}
	}
	if err := locked.ValidateFor(manifest.Project.ID, manifestBytes, nonBaseIDs); err != nil {
		return ReleaseMaterializeResult{}, NewError(ErrorConflict, fmt.Errorf("release lock does not match portable manifest: %w", err))
	}
	baseHead, err := s.validateBase(ctx, base, manifest, manifestBytes, lockBytes)
	if err != nil {
		return ReleaseMaterializeResult{}, err
	}
	project := cloneDomainProject(manifest)
	project.LogicalRoot = base
	paths, err := project.EffectivePaths(base, nil)
	if err != nil {
		return ReleaseMaterializeResult{}, NewError(ErrorValidation, fmt.Errorf("resolve release topology: %w", err))
	}
	canonicalPath, pathErr := filepath.EvalSymlinks(paths[manifest.Project.BaseRepository])
	if canonicalBase, canonicalErr := filepath.EvalSymlinks(base); canonicalErr != nil || pathErr != nil || filepath.Clean(canonicalPath) != filepath.Clean(canonicalBase) {
		return ReleaseMaterializeResult{}, NewError(ErrorConflict, errors.New("caller base mount differs from portable manifest"))
	}
	order, err := portableRepositoryOrder(manifest)
	if err != nil {
		return ReleaseMaterializeResult{}, NewError(ErrorValidation, err)
	}
	result = ReleaseMaterializeResult{Version: 1, Operation: "release-materialize", Status: "planned", ProjectID: manifest.Project.ID, ReleaseName: locked.Release.Name, LockPath: lockPath, ManifestSHA256: locked.Project.ManifestSHA256, DryRun: q.DryRun, Repositories: []ReleaseMaterializeRepositoryResult{}}
	for _, id := range order {
		if id == manifest.Project.BaseRepository {
			result.Repositories = append(result.Repositories, ReleaseMaterializeRepositoryResult{ID: id, Role: "caller-provided-base", Status: "adopted", Expected: baseHead, Observed: baseHead, Path: paths[id]})
		} else {
			result.Repositories = append(result.Repositories, ReleaseMaterializeRepositoryResult{ID: id, Role: "materialized-child", Status: "planned", Expected: locked.Repositories[id].Revision, Path: paths[id]})
		}
	}
	if err := s.validateLocalPreconditions(ctx, base, baseHead, manifest, project, paths, q.DataDir); err != nil {
		return result, err
	}
	if q.DryRun {
		return result, nil
	}
	if s.beforeStaging != nil {
		if err := s.beforeStaging(); err != nil {
			return result, err
		}
	}
	stagingRecoveryPath := filepath.Join(q.DataDir, "projects", project.ID, "recovery", "default.json")
	stagingRecoveryBefore, recoveryErr := secureCloneFileSnapshot(stagingRecoveryPath)
	if recoveryErr != nil || stagingRecoveryBefore.exists {
		return result, NewError(ErrorConflict, errors.Join(errors.New("release recovery authority changed before staging"), recoveryErr))
	}

	stagingParent := filepath.Dir(base)
	stagingParentInfo, parentErr := os.Lstat(stagingParent)
	if parentErr != nil || !stagingParentInfo.IsDir() || stagingParentInfo.Mode()&os.ModeSymlink != 0 {
		return result, NewError(ErrorConflict, fmt.Errorf("capture release staging parent: %w", parentErr))
	}
	staging, stagingOwned, stagingParentOwned, stagingLease, err := createCloneStaging(stagingParent, ".wtree-release-", stagingParentInfo, os.MkdirTemp, os.Lstat)
	if err != nil {
		return result, NewError(ErrorInternal, fmt.Errorf("create private release staging: %w", err))
	}
	if s.wrapStagingLease != nil {
		stagingLease = s.wrapStagingLease(stagingLease)
	}
	defer func() {
		cleanupEvidence, cleanupErr := releaseMaterializeCleanupStaging(staging, stagingOwned, stagingParentOwned, stagingLease, s.beforeStagingQuarantine, s.removeStagingQuarantine)
		if cleanupErr == nil {
			return
		}
		result.Status = "failed"
		stagingCause := fmt.Errorf("release staging cleanup incomplete: %w", cleanupErr)
		recoveryErr := s.writeMaterializeStagingRecovery(
			ctx, stagingRecoveryBefore, base, baseHead, manifest, manifestBytes, lockBytes,
			q.DataDir, project.ID, cleanupEvidence,
		)
		if recoveryErr != nil {
			stagingCause = errors.Join(stagingCause, fmt.Errorf("staging recovery could not be recorded: %w", recoveryErr))
		}
		returnErr = NewError(ErrorRollbackIncomplete, errors.Join(returnErr, stagingCause))
	}()
	staged := make(map[string]string, len(result.Repositories))
	for _, id := range order {
		if id == manifest.Project.BaseRepository {
			continue
		}
		repository := manifest.Repositories[id]
		final := paths[id]
		relative, relErr := filepath.Rel(base, final)
		if relErr != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." {
			return result, NewError(ErrorValidation, fmt.Errorf("repository %q has unsafe release mount", id))
		}
		stage := filepath.Join(staging, relative)
		if err := os.MkdirAll(filepath.Dir(stage), 0o700); err != nil {
			return result, NewError(ErrorInternal, fmt.Errorf("prepare staging for repository %q: %w", id, err))
		}
		if repository.Parent != "" {
			parentPath := base
			parentHead := baseHead
			if repository.Parent != manifest.Project.BaseRepository {
				parentPath, parentHead = staged[repository.Parent], materializeRepository(result.Repositories, repository.Parent).Observed
			}
			ignored, ignoreErr := s.git.IsIgnoredAt(ctx, parentPath, parentHead, repository.Mount)
			if ignoreErr != nil || !ignored {
				return result, NewError(ErrorValidation, fmt.Errorf("repository %q mount is not ignored by its committed parent: %w", id, ignoreErr))
			}
		}
		if err := s.git.Clone(ctx, repository.Clone.URL, stage, repository.Clone.Remote); err != nil {
			return result, NewError(ErrorGit, fmt.Errorf("stage repository %q: %w", id, err))
		}
		if err := s.git.FetchAdvertisedRefs(ctx, stage, repository.Clone.Remote); err != nil {
			return result, NewError(ErrorGit, fmt.Errorf("fetch advertised refs for repository %q: %w", id, err))
		}
		head, checkErr := s.git.CheckoutDetached(ctx, stage, locked.Repositories[id].Revision)
		if checkErr != nil {
			return result, NewError(ErrorGit, fmt.Errorf("checkout locked revision for repository %q: %w", id, checkErr))
		}
		if head != locked.Repositories[id].Revision {
			return result, NewError(ErrorConflict, fmt.Errorf("repository %q checked out a revision other than its lock", id))
		}
		contains, containsErr := s.git.ContainsCommits(ctx, stage, repository.Identity.InitialCommits)
		clean, cleanErr := s.git.IsClean(ctx, stage)
		submodules, submoduleErr := s.git.HasSubmodules(ctx, stage)
		if containsErr != nil || !contains || cleanErr != nil || !clean || submoduleErr != nil || submodules {
			return result, NewError(ErrorValidation, fmt.Errorf("verify staged repository %q", id))
		}
		entry := materializeRepository(result.Repositories, id)
		entry.Observed = head
		entry.Status = "materialized"
		staged[id] = stage
	}
	// Every child is now proven before this first final mount is made public.
	if err := s.publish(ctx, base, baseHead, manifest, manifestBytes, lockBytes, project, paths, staged, result.Repositories, q.DataDir); err != nil {
		return result, err
	}
	result.Status = "completed"
	return result, nil
}

type releaseMaterializeStagingCleanupEvidence struct {
	stagingPath         string
	retainedPath        string
	privateTree         bool
	quarantine          bool
	authorityIncomplete bool
}

func releaseMaterializeCleanupStaging(staging string, owned, parent os.FileInfo, lease cloneStagingLease, beforeQuarantine func(string) error, removeQuarantine func(string) error) (evidence releaseMaterializeStagingCleanupEvidence, returnErr error) {
	evidence = releaseMaterializeStagingCleanupEvidence{stagingPath: staging, retainedPath: staging, privateTree: true}
	if removeQuarantine == nil {
		removeQuarantine = os.Remove
	}
	var quarantine string
	defer func() {
		if quarantine != "" {
			if err := removeQuarantine(quarantine); err != nil && !os.IsNotExist(err) {
				evidence.retainedPath = quarantine
				evidence.quarantine = true
				returnErr = errors.Join(returnErr, fmt.Errorf("remove release staging quarantine: %w", err))
			} else {
				evidence.quarantine = false
			}
		}
		if lease == nil {
			evidence.authorityIncomplete = true
			returnErr = errors.Join(returnErr, errors.New("release staging lease is unavailable"))
		} else if err := lease.closeAll(); err != nil {
			evidence.authorityIncomplete = true
			returnErr = errors.Join(returnErr, fmt.Errorf("close release staging lease: %w", err))
		}
		if returnErr == nil {
			evidence = releaseMaterializeStagingCleanupEvidence{}
		}
	}()
	if lease == nil {
		return evidence, errors.New("preserve release staging without ownership lease")
	}
	if err := lease.releaseChild(staging, owned, parent, os.Lstat); err != nil {
		return evidence, fmt.Errorf("preserve substituted release staging root: %w", err)
	}
	tree, err := captureCloneTree(staging)
	if err != nil {
		return evidence, fmt.Errorf("preserve uninventoryable release staging root: %w", err)
	}
	if err := revalidateCloneTree(staging, tree); err != nil {
		return evidence, fmt.Errorf("preserve changed release staging root: %w", err)
	}
	quarantine, err = os.MkdirTemp(filepath.Dir(staging), ".wtree-release-staging-rollback-")
	if err != nil {
		return evidence, fmt.Errorf("allocate release staging quarantine: %w", err)
	}
	evidence.quarantine = true
	ownedPath := filepath.Join(quarantine, "owned")
	if beforeQuarantine != nil {
		if err := beforeQuarantine(staging); err != nil {
			return evidence, err
		}
	}
	if err := fsutil.RenameNoReplace(staging, ownedPath); err != nil {
		return evidence, fmt.Errorf("capture release staging ownership: %w", err)
	}
	evidence.retainedPath = ownedPath
	moved, err := os.Lstat(ownedPath)
	if err != nil || !moved.IsDir() || moved.Mode()&os.ModeSymlink != 0 || !os.SameFile(owned, moved) || revalidateCloneTree(ownedPath, tree) != nil {
		restoreErr := restoreMaterializeChild(staging, ownedPath, errors.New("release staging root changed at quarantine boundary"))
		if _, statErr := os.Lstat(staging); statErr == nil {
			evidence.retainedPath = staging
		}
		return evidence, restoreErr
	}
	if err := os.RemoveAll(ownedPath); err != nil {
		return evidence, fmt.Errorf("destroy isolated release staging: %w", err)
	}
	evidence.privateTree = false
	evidence.retainedPath = quarantine
	if err := removeQuarantine(quarantine); err != nil && !os.IsNotExist(err) {
		return evidence, err
	}
	quarantine = ""
	evidence.quarantine = false
	evidence.retainedPath = ""
	return evidence, nil
}

func (s *ReleaseMaterializeService) writeMaterializeStagingRecovery(ctx context.Context, expected cloneFileSnapshot, base, baseHead string, manifest config.PortableManifest, manifestBytes, lockBytes []byte, dataDir, projectID string, evidence releaseMaterializeStagingCleanupEvidence) (returnErr error) {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	projectHandle, err := acquireProjectMutationAuthority(recoveryContext, lock.Manager{}, dataDir, projectID, time.Second)
	if err != nil {
		return err
	}
	defer func() {
		if err := projectHandle.Unlock(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release staging recovery unlock: %w", err))
		}
	}()

	value := store.RecoveryRecord{
		Version:        store.Version,
		ProjectID:      projectID,
		WorkspaceID:    "default",
		Operation:      "release-materialize",
		FailedStep:     "staging-cleanup",
		CompletedSteps: []string{"staged"},
	}
	if evidence.privateTree {
		value.UnrevertedSteps = append(value.UnrevertedSteps, "private-staging")
		value.RollbackFailures = append(value.RollbackFailures, store.RollbackFailure{Step: "private-staging", Error: fmt.Sprintf("inspect retained private staging at %q and remove it only after verifying ownership", evidence.retainedPath)})
	}
	if evidence.quarantine && !evidence.privateTree {
		value.UnrevertedSteps = append(value.UnrevertedSteps, "staging-quarantine")
		value.RollbackFailures = append(value.RollbackFailures, store.RollbackFailure{Step: "staging-quarantine", Error: fmt.Sprintf("inspect the retained staging quarantine at %q; the private checkout tree was already destroyed", evidence.retainedPath)})
	}
	if evidence.authorityIncomplete {
		value.UnrevertedSteps = append(value.UnrevertedSteps, "staging-authority")
		value.RollbackFailures = append(value.RollbackFailures, store.RollbackFailure{Step: "staging-authority", Error: fmt.Sprintf("verify that platform cleanup authority for staging location %q is released; this entry does not claim a retained private checkout tree", evidence.stagingPath)})
	}
	if len(value.RollbackFailures) == 0 {
		return errors.New("staging cleanup failed without retained-resource evidence")
	}
	data, err := store.RecoveryBytes(value)
	if err != nil {
		return err
	}
	authority := func() error {
		currentManifest, manifestErr := os.ReadFile(filepath.Join(base, "project.wtree.yml"))
		currentLock, lockErr := os.ReadFile(filepath.Join(base, ReleaseLockFilename))
		if manifestErr != nil || lockErr != nil || !bytes.Equal(currentManifest, manifestBytes) || !bytes.Equal(currentLock, lockBytes) {
			return errors.New("base authority changed before staging recovery publication")
		}
		if baseErr := s.revalidateMaterializeBase(recoveryContext, base, baseHead, manifest, manifestBytes, lockBytes); baseErr != nil {
			return baseErr
		}
		return revalidateCloneFileSnapshot(expected)
	}
	receipt, writeErr := s.writeMaterializeCAS(expected, data, authority, nil)
	if writeErr != nil {
		return writeErr
	}
	if !validClonePublicationReceipt(receipt, expected.path, data, 0o600) {
		return errors.New("staging recovery writer did not return exact owned receipt")
	}
	return nil
}

func materializeRepository(values []ReleaseMaterializeRepositoryResult, id string) *ReleaseMaterializeRepositoryResult {
	for index := range values {
		if values[index].ID == id {
			return &values[index]
		}
	}
	return nil
}

func (s *ReleaseMaterializeService) validateBase(ctx context.Context, base string, manifest config.PortableManifest, manifestBytes, lockBytes []byte) (string, error) {
	top, err := s.git.TopLevel(ctx, base)
	canonicalBase, canonicalErr := filepath.EvalSymlinks(base)
	canonicalTop, topErr := filepath.EvalSymlinks(top)
	if err != nil || canonicalErr != nil || topErr != nil || filepath.Clean(canonicalTop) != filepath.Clean(canonicalBase) {
		return "", NewError(ErrorValidation, errors.New("release materialize must run from the base checkout root"))
	}
	head, err := s.git.Head(ctx, base)
	if err != nil {
		return "", NewError(ErrorGit, err)
	}
	clean, err := s.git.IsClean(ctx, base)
	if err != nil || !clean {
		return "", NewError(ErrorDirtyWorkspace, errors.New("base checkout must be clean"))
	}
	for _, item := range []struct {
		path string
		data []byte
	}{{"project.wtree.yml", manifestBytes}, {ReleaseLockFilename, lockBytes}} {
		tracked, trackedErr := s.git.TrackedFile(ctx, base, head, item.path)
		if trackedErr != nil || !bytes.Equal(tracked, item.data) {
			return "", NewError(ErrorConflict, fmt.Errorf("base tracked %s differs from working bytes", item.path))
		}
	}
	has, err := s.git.HasSubmodules(ctx, base)
	if err != nil || has {
		return "", NewError(ErrorValidation, errors.New("base checkout must not contain submodules"))
	}
	contains, containsErr := s.git.ContainsCommits(ctx, base, manifest.Repositories[manifest.Project.BaseRepository].Identity.InitialCommits)
	if containsErr != nil || !contains {
		return "", NewError(ErrorConflict, errors.New("caller base does not match portable identity roots"))
	}
	return head, nil
}

func (s *ReleaseMaterializeService) validateLocalPreconditions(ctx context.Context, base string, baseHead string, manifest config.PortableManifest, project domain.Project, paths map[string]string, dataDir string) error {
	if _, err := os.Lstat(filepath.Join(base, ".wtree.yml")); !os.IsNotExist(err) {
		return NewError(ErrorConflict, errors.New("base local configuration already exists"))
	}
	ignored, err := s.git.IsIgnoredAt(ctx, base, baseHead, ".wtree.yml")
	if err != nil || !ignored {
		return NewError(ErrorValidation, errors.New("base committed content must ignore /.wtree.yml"))
	}
	if _, err := os.Lstat(WorkspaceStatePath(dataDir, project.ID, "default")); !os.IsNotExist(err) {
		return NewError(ErrorConflict, errors.New("default workspace state already exists"))
	}
	if _, err := os.Lstat(filepath.Join(dataDir, "projects", project.ID, "recovery", "default.json")); !os.IsNotExist(err) {
		return NewError(ErrorConflict, errors.New("release recovery record already exists"))
	}
	registry, err := readRegistry(filepath.Join(dataDir, "registry.json"))
	if err != nil {
		return NewError(ErrorValidation, err)
	}
	if _, exists := registry.Projects[project.ID]; exists {
		return NewError(ErrorConflict, errors.New("project is already registered"))
	}
	for id, path := range paths {
		if id == manifest.Project.BaseRepository {
			continue
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return NewError(ErrorConflict, fmt.Errorf("repository %q destination already exists", id))
		}
	}
	return nil
}

func (s *ReleaseMaterializeService) publish(ctx context.Context, base, baseHead string, manifest config.PortableManifest, manifestBytes, lockBytes []byte, project domain.Project, paths, staged map[string]string, results []ReleaseMaterializeRepositoryResult, dataDir string) (returnErr error) {
	if s.removeAll == nil {
		s.removeAll = os.RemoveAll
	}
	registryHandle, err := (lock.Manager{}).RegistryLock(ctx, dataDir, time.Second)
	if err != nil {
		return NewError(ErrorConflict, fmt.Errorf("acquire release registry lock: %w", err))
	}
	defer registryHandle.Unlock()
	projectHandle, err := acquireProjectMutationAuthority(ctx, lock.Manager{}, dataDir, project.ID, time.Second)
	if err != nil {
		return err
	}
	defer projectHandle.Unlock()
	// Recheck mutable local authority at the final publication boundary, after
	// every remote child has been staged and verified but before any mount is
	// exposed.
	if s.beforePublish != nil {
		if err := s.beforePublish(); err != nil {
			return err
		}
	}
	currentManifest, manifestErr := os.ReadFile(filepath.Join(base, "project.wtree.yml"))
	currentLock, lockErr := os.ReadFile(filepath.Join(base, ReleaseLockFilename))
	if manifestErr != nil || lockErr != nil || !bytes.Equal(currentManifest, manifestBytes) || !bytes.Equal(currentLock, lockBytes) {
		return NewError(ErrorConflict, errors.New("base manifest or release lock changed during staging"))
	}
	currentHead, authorityErr := s.validateBase(ctx, base, manifest, manifestBytes, lockBytes)
	if authorityErr != nil || currentHead != baseHead {
		return NewError(ErrorConflict, errors.New("caller base authority changed during staging"))
	}
	if err := s.validateLocalPreconditions(ctx, base, baseHead, manifest, project, paths, dataDir); err != nil {
		return err
	}
	if err := s.validateRegistrationConflicts(ctx, dataDir, project, manifest, paths, staged); err != nil {
		return err
	}
	// Capture every mutable publication file while the project and registry
	// authorities are held.  These snapshots are both the CAS baselines and the
	// only generations this operation is permitted to restore or remove.
	configPath := filepath.Join(base, ".wtree.yml")
	statePath := WorkspaceStatePath(dataDir, project.ID, "default")
	registryPath := filepath.Join(dataDir, "registry.json")
	recoveryPath := filepath.Join(dataDir, "projects", project.ID, "recovery", "default.json")
	configBefore, configErr := secureCloneFileSnapshot(configPath)
	stateBefore, stateErr := secureCloneFileSnapshot(statePath)
	registryBefore, registryErr := secureCloneFileSnapshot(registryPath)
	recoveryBefore, recoveryErr := secureCloneFileSnapshot(recoveryPath)
	if configErr != nil || stateErr != nil || registryErr != nil || recoveryErr != nil {
		return NewError(ErrorConflict, errors.Join(configErr, stateErr, registryErr, recoveryErr))
	}
	if configBefore.exists || stateBefore.exists || recoveryBefore.exists {
		return NewError(ErrorConflict, errors.New("release local publication already exists"))
	}

	var children []materializeChildReceipt
	var groupingCreated []clonePathIdentity
	var configReceipt, stateReceipt, registryReceipt ClonePublicationReceipt
	var recoveryOwned cloneFileSnapshot
	rollback := func(cause error) error {
		var failures []error
		if registryReceipt.snapshot.exists {
			if err := rollbackMaterializePublication(registryBefore, registryReceipt, s.beforeFileRemoval); err != nil {
				failures = append(failures, fmt.Errorf("registry: %w", err))
			}
		}
		if stateReceipt.snapshot.exists {
			if err := rollbackMaterializePublication(stateBefore, stateReceipt, s.beforeFileRemoval); err != nil {
				failures = append(failures, fmt.Errorf("state: %w", err))
			}
		}
		if configReceipt.snapshot.exists {
			if err := rollbackMaterializePublication(configBefore, configReceipt, s.beforeFileRemoval); err != nil {
				failures = append(failures, fmt.Errorf("configuration: %w", err))
			}
		}
		for index := len(children) - 1; index >= 0; index-- {
			if err := s.removeMaterializeChild(ctx, children[index]); err != nil {
				failures = append(failures, fmt.Errorf("repository %q: %w", children[index].id, err))
			}
		}
		for index := len(groupingCreated) - 1; index >= 0; index-- {
			grouping := groupingCreated[index]
			if err := removeMaterializeGrouping(base, grouping, s.beforeGroupingQuarantine); err != nil {
				failures = append(failures, fmt.Errorf("grouping %q: %w", grouping.path, err))
			}
		}
		if len(failures) == 0 {
			return cause
		}
		recoveryErr := s.writeMaterializeRecovery(recoveryBefore, &recoveryOwned, ctx, base, baseHead, manifest, manifestBytes, lockBytes, children, configReceipt, stateReceipt, registryReceipt, dataDir, project.ID, failures)
		combined := errors.Join(cause, NewError(ErrorRollbackIncomplete, errors.Join(failures...)))
		if recoveryErr != nil {
			combined = errors.Join(combined, fmt.Errorf("recovery could not be recorded: %w", recoveryErr))
		}
		return combined
	}
	revalidate := func() error {
		currentManifest, manifestErr := os.ReadFile(filepath.Join(base, "project.wtree.yml"))
		currentLock, lockErr := os.ReadFile(filepath.Join(base, ReleaseLockFilename))
		if manifestErr != nil || lockErr != nil || !bytes.Equal(currentManifest, manifestBytes) || !bytes.Equal(currentLock, lockBytes) {
			return NewError(ErrorConflict, errors.New("base manifest or release lock changed during publication"))
		}
		if authorityErr := s.revalidateMaterializeBase(ctx, base, baseHead, manifest, manifestBytes, lockBytes); authorityErr != nil {
			return authorityErr
		}
		for _, child := range children {
			if err := s.revalidateMaterializeChild(ctx, child); err != nil {
				return err
			}
		}
		for _, snapshot := range []cloneFileSnapshot{configBefore, stateBefore, registryBefore, recoveryBefore} {
			if snapshot.path == configReceipt.snapshot.path && configReceipt.snapshot.exists {
				snapshot = configReceipt.snapshot
			}
			if snapshot.path == stateReceipt.snapshot.path && stateReceipt.snapshot.exists {
				snapshot = stateReceipt.snapshot
			}
			if snapshot.path == registryReceipt.snapshot.path && registryReceipt.snapshot.exists {
				snapshot = registryReceipt.snapshot
			}
			if err := revalidateCloneFileSnapshot(snapshot); err != nil {
				return err
			}
		}
		return nil
	}
	// Rename only roots whose parent is the base or a sibling top-level parent;
	// nested staged checkouts travel inside their already verified parent tree.
	for _, id := range portableIDsParentFirst(manifest) {
		repository := manifest.Repositories[id]
		if id == manifest.Project.BaseRepository || (repository.Parent != manifest.Project.BaseRepository && repository.Parent != "") {
			continue
		}
		grouping, err := materializePrepareGrouping(filepath.Dir(paths[id]), base)
		if err != nil {
			return rollback(NewError(ErrorConflict, err))
		}
		groupingCreated = append(groupingCreated, grouping...)
		if err := revalidate(); err != nil {
			return rollback(err)
		}
		stageTree, err := captureCloneTree(staged[id])
		if err != nil {
			return rollback(NewError(ErrorInternal, fmt.Errorf("inventory staged repository %q: %w", id, err)))
		}
		parentIdentity, err := captureClonePathIdentity(filepath.Dir(paths[id]))
		if err != nil {
			return rollback(NewError(ErrorConflict, err))
		}
		if _, err := os.Lstat(paths[id]); !os.IsNotExist(err) {
			return rollback(NewError(ErrorConflict, fmt.Errorf("repository %q destination already exists", id)))
		}
		if err := fsutil.RenameNoReplace(staged[id], paths[id]); err != nil {
			return rollback(NewError(ErrorConflict, fmt.Errorf("publish repository %q: %w", id, err)))
		}
		renameInfo, renameInfoErr := os.Lstat(paths[id])
		if renameInfoErr != nil {
			return rollback(NewError(ErrorConflict, fmt.Errorf("capture published repository %q: %w", id, renameInfoErr)))
		}
		if err := translateCloneRootAfterRename(paths[id], &stageTree, renameInfo); err != nil {
			return rollback(NewError(ErrorConflict, fmt.Errorf("capture published repository %q: %w", id, err)))
		}
		common, err := s.git.CommonGitDir(ctx, paths[id])
		if err != nil {
			return rollback(NewError(ErrorGit, err))
		}
		child := materializeChildReceipt{id: id, path: paths[id], parent: parentIdentity, grouping: grouping, tree: stageTree, commonGit: common}
		if err := s.revalidateMaterializeChild(ctx, child); err != nil {
			return rollback(err)
		}
		children = append(children, child)
		if s.afterPublish != nil {
			if err := s.afterPublish(id); err != nil {
				return rollback(err)
			}
		}
	}
	configuration := releaseLocalConfiguration(manifest, base, paths)
	configBytes, err := config.MarshalProject(configuration)
	if err != nil {
		return rollback(NewError(ErrorInternal, err))
	}
	configReceipt, err = s.writeMaterializeCAS(configBefore, configBytes, revalidate, func(temp string, info os.FileInfo) error {
		if err := s.revalidateMaterializeBaseWithTemp(ctx, base, baseHead, manifest, manifestBytes, lockBytes, temp, info); err != nil {
			return err
		}
		for _, child := range children {
			if err := s.revalidateMaterializeChild(ctx, child); err != nil {
				return err
			}
		}
		for _, snapshot := range []cloneFileSnapshot{configBefore, stateBefore, registryBefore, recoveryBefore} {
			if err := revalidateCloneFileSnapshot(snapshot); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !validClonePublicationReceipt(configReceipt, configPath, configBytes, 0o600) {
		return rollback(NewError(ErrorInternal, fmt.Errorf("write release local configuration: %w", err)))
	}
	baseBranch, baseDetached, baseBranchErr := s.git.CurrentBranch(ctx, base)
	if baseBranchErr != nil {
		return rollback(NewError(ErrorGit, fmt.Errorf("read base checkout branch: %w", baseBranchErr)))
	}
	checkouts := map[string]store.CheckoutState{manifest.Project.BaseRepository: {Branch: baseBranch, Detached: baseDetached, Mount: manifest.Repositories[manifest.Project.BaseRepository].Mount, ResolvedPath: base, Head: baseHead}}
	identities := map[string]string{}
	for _, id := range portableIDsParentFirst(manifest) {
		path := paths[id]
		common, err := s.git.CommonGitDir(ctx, path)
		if err != nil {
			return rollback(NewError(ErrorGit, err))
		}
		if prior, exists := identities[common]; exists {
			return rollback(NewError(ErrorConflict, fmt.Errorf("repositories %q and %q share a Git identity", prior, id)))
		}
		identities[common] = id
		if id != manifest.Project.BaseRepository {
			item := materializeRepository(results, id)
			checkouts[id] = store.CheckoutState{Mount: manifest.Repositories[id].Mount, ResolvedPath: path, Head: item.Observed, Detached: true}
		}
	}
	state := store.WorkspaceState{Version: store.Version, ID: "default", Name: "default", Path: base, Repositories: checkouts}
	stateBytes, err := store.WorkspaceBytes(state)
	if err != nil {
		return rollback(NewError(ErrorInternal, err))
	}
	stateReceipt, err = s.writeMaterializeCAS(stateBefore, stateBytes, revalidate, nil)
	if err != nil || !validClonePublicationReceipt(stateReceipt, statePath, stateBytes, 0o600) {
		return rollback(NewError(ErrorInternal, fmt.Errorf("publish workspace state: %w", err)))
	}
	registry, err := readRegistry(registryPath)
	if err != nil {
		return rollback(NewError(ErrorInternal, err))
	}
	if registry.Projects == nil {
		registry.Projects = map[string]store.RegistryProject{}
	}
	registry.Projects[project.ID] = store.RegistryProject{Name: manifest.Project.Name, ConfigPath: filepath.Join(base, ".wtree.yml"), RepositoryIDs: identities}
	registryBytes, err := store.RegistryBytes(registry)
	if err != nil {
		return rollback(NewError(ErrorInternal, err))
	}
	registryReceipt, err = s.writeMaterializeCAS(registryBefore, registryBytes, revalidate, nil)
	if err != nil || !validClonePublicationReceipt(registryReceipt, registryPath, registryBytes, 0o600) {
		return rollback(NewError(ErrorInternal, fmt.Errorf("publish registry: %w", err)))
	}
	return nil
}

func (s *ReleaseMaterializeService) validateRegistrationConflicts(ctx context.Context, dataDir string, project domain.Project, manifest config.PortableManifest, paths, staged map[string]string) error {
	registry, err := readRegistry(filepath.Join(dataDir, "registry.json"))
	if err != nil {
		return NewError(ErrorValidation, err)
	}
	identities := make([]string, 0, len(manifest.Repositories))
	for _, id := range portableIDsParentFirst(manifest) {
		path := paths[id]
		if id != manifest.Project.BaseRepository {
			path = staged[id]
		}
		identity, identityErr := s.git.CommonGitDir(ctx, path)
		if identityErr != nil {
			return NewError(ErrorGit, fmt.Errorf("read staged repository %q identity: %w", id, identityErr))
		}
		identities = append(identities, identity)
	}
	topLevels := []string{}
	for _, repository := range project.ParentFirst() {
		if repository.ParentID == "" {
			topLevels = append(topLevels, paths[repository.ID])
		}
	}
	candidate := RegistrationConflictCandidate{ID: project.ID, ConfigPath: filepath.Join(paths[manifest.Project.BaseRepository], ".wtree.yml"), RepositoryIdentities: identities, LogicalRoot: paths[manifest.Project.BaseRepository], TopLevelPaths: topLevels}
	candidates := registeredConflictCandidates(ctx, dataDir, registry)
	if s.registrationCandidates != nil {
		candidates = s.registrationCandidates(ctx, dataDir, registry)
	}
	if ids := RegistrationConflictIDsForTarget(candidate, candidates); len(ids) != 0 {
		return NewError(ErrorConflict, fmt.Errorf("release materialization registration conflicts with existing project IDs %s", strings.Join(ids, ", ")))
	}
	return nil
}

// materializeChildReceipt is deliberately private: it ties an injected child
// mount to the exact tree and Git identity that release materialize staged.
// Rollback may remove only this receipt, never a later checkout at the same
// path.
type materializeChildReceipt struct {
	id        string
	path      string
	parent    clonePathIdentity
	grouping  []clonePathIdentity
	tree      cloneTreeInventory
	commonGit string
}

func materializePrepareGrouping(parent, root string) ([]clonePathIdentity, error) {
	parent, root = filepath.Clean(parent), filepath.Clean(root)
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, fmt.Errorf("release grouping directory %q escapes base %q", parent, root)
	}
	if relative == "." {
		return nil, nil
	}
	current := root
	created := []clonePathIdentity{}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
				return nil, fmt.Errorf("create release grouping directory %q: %w", current, err)
			}
			info, statErr = os.Lstat(current)
			if statErr != nil {
				return nil, statErr
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("release grouping directory %q is not a real directory", current)
			}
			created = append(created, clonePathIdentity{path: current, info: info})
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("release grouping directory %q is not a real directory", current)
		}
	}
	return created, nil
}

func (s *ReleaseMaterializeService) revalidateMaterializeChild(ctx context.Context, receipt materializeChildReceipt) error {
	if err := revalidateClonePathIdentity(receipt.parent); err != nil {
		return err
	}
	for _, grouping := range receipt.grouping {
		if err := revalidateClonePathIdentity(grouping); err != nil {
			return err
		}
	}
	if err := revalidateCloneTree(receipt.path, receipt.tree); err != nil {
		return NewError(ErrorConflict, fmt.Errorf("published repository %q changed: %w", receipt.id, err))
	}
	common, err := s.git.CommonGitDir(ctx, receipt.path)
	if err != nil || filepath.Clean(common) != filepath.Clean(receipt.commonGit) {
		return NewError(ErrorConflict, fmt.Errorf("published repository %q Git identity changed", receipt.id))
	}
	return nil
}

func (s *ReleaseMaterializeService) removeMaterializeChild(ctx context.Context, receipt materializeChildReceipt) error {
	if err := s.revalidateMaterializeChild(ctx, receipt); err != nil {
		return err
	}
	// Keep rollback evidence beside the caller checkout, not inside it: an
	// otherwise clean base must not observe our own quarantine as untracked.
	quarantine, err := os.MkdirTemp(filepath.Dir(receipt.parent.path), ".wtree-release-rollback-")
	if err != nil {
		return fmt.Errorf("allocate release child quarantine: %w", err)
	}
	quarantineInfo, err := captureClonePathIdentity(quarantine)
	if err != nil {
		return err
	}
	ownedPath := filepath.Join(quarantine, "owned")
	if s.beforeChildQuarantine != nil {
		if err := s.beforeChildQuarantine(receipt.path); err != nil {
			return err
		}
	}
	if err := fsutil.RenameNoReplace(receipt.path, ownedPath); err != nil {
		return fmt.Errorf("capture release child for rollback: %w", err)
	}
	moved := receipt
	moved.path = ownedPath
	if relative, relErr := filepath.Rel(receipt.path, receipt.commonGit); relErr == nil && (relative == "." || !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		moved.commonGit = filepath.Join(ownedPath, relative)
	}
	moved.parent, err = captureClonePathIdentity(quarantine)
	if err != nil {
		return restoreMaterializeChild(receipt.path, ownedPath, err)
	}
	moved.grouping = nil
	if err := s.revalidateMaterializeChild(ctx, moved); err != nil {
		return restoreMaterializeChild(receipt.path, ownedPath, err)
	}
	if err := revalidateClonePathIdentity(quarantineInfo); err != nil {
		return restoreMaterializeChild(receipt.path, ownedPath, err)
	}
	if err := s.removeAll(ownedPath); err != nil {
		return fmt.Errorf("destroy isolated release child: %w", err)
	}
	if err := os.Remove(quarantine); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Lstat(receipt.path); err == nil {
		return errors.New("release child public path was recreated during rollback")
	}
	// A directory created solely to group this checkout is removed only while
	// it is still the exact empty generation we made.  Existing directories and
	// directories populated by another actor are left intact.
	for index := len(receipt.grouping) - 1; index >= 0; index-- {
		grouping := receipt.grouping[index]
		if err := revalidateClonePathIdentity(grouping); err != nil {
			return err
		}
		entries, err := os.ReadDir(grouping.path)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			continue
		}
		if err := removeMaterializeGrouping(receipt.parent.path, grouping, s.beforeGroupingQuarantine); err != nil {
			return err
		}
	}
	return nil
}

func removeMaterializeGrouping(base string, grouping clonePathIdentity, beforeQuarantine func(string) error) error {
	if _, err := os.Lstat(grouping.path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := revalidateClonePathIdentity(grouping); err != nil {
		return err
	}
	quarantine, err := os.MkdirTemp(filepath.Dir(base), ".wtree-release-group-rollback-")
	if err != nil {
		return err
	}
	owned := filepath.Join(quarantine, "owned")
	if beforeQuarantine != nil {
		if err := beforeQuarantine(grouping.path); err != nil {
			return err
		}
	}
	if err := fsutil.RenameNoReplace(grouping.path, owned); err != nil {
		return err
	}
	moved, err := os.Lstat(owned)
	if err != nil || !moved.IsDir() || moved.Mode()&os.ModeSymlink != 0 || !os.SameFile(grouping.info, moved) {
		return restoreMaterializeChild(grouping.path, owned, errors.New("grouping generation changed after quarantine"))
	}
	entries, err := os.ReadDir(owned)
	if err != nil || len(entries) != 0 {
		return restoreMaterializeChild(grouping.path, owned, errors.New("grouping directory changed after quarantine"))
	}
	if err := os.Remove(owned); err != nil {
		return err
	}
	if err := os.Remove(quarantine); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func restoreMaterializeChild(publicPath, ownedPath string, cause error) error {
	restoreErr := fsutil.RenameNoReplace(ownedPath, publicPath)
	if restoreErr != nil {
		return errors.Join(cause, fmt.Errorf("preserve captured child at %q: %w", ownedPath, restoreErr))
	}
	return cause
}

// writeMaterializeCAS retains a receipt even when the lower writer reports a
// post-replacement durability error.  The caller then restores only that
// exact generation, avoiding an unconditional overwrite of a foreign file.
func (s *ReleaseMaterializeService) writeMaterializeCAS(original cloneFileSnapshot, data []byte, compare func() error, final func(string, os.FileInfo) error) (ClonePublicationReceipt, error) {
	if s.writeCAS != nil {
		return s.writeCAS(original, data, compare)
	}
	return defaultMaterializeCAS(original, data, compare, final)
}

func defaultMaterializeCAS(original cloneFileSnapshot, data []byte, compare func() error, final func(string, os.FileInfo) error) (ClonePublicationReceipt, error) {
	if err := compare(); err != nil {
		return ClonePublicationReceipt{}, err
	}
	var writeErr error
	if original.exists {
		writeErr = fsutil.WriteFileAtomicModeExpected(original.path, data, 0o600, original.info)
	} else {
		writeErr = fsutil.WriteFileAtomicCreateModeNoReplaceWithOwnedTempHook(original.path, data, 0o600, nil, final)
	}
	published, captureErr := secureCloneFileSnapshot(original.path)
	if captureErr == nil && cloneSnapshotHasExactBytes(published, data, 0o600) {
		return ClonePublicationReceipt{snapshot: published}, writeErr
	}
	if writeErr != nil {
		return ClonePublicationReceipt{}, errors.Join(writeErr, captureErr)
	}
	return ClonePublicationReceipt{}, errors.Join(errors.New("release writer did not publish exact owned generation"), captureErr)
}

func rollbackMaterializePublication(original cloneFileSnapshot, receipt ClonePublicationReceipt, beforeRemoval func(string) error) error {
	owned := receipt.snapshot
	if owned.path == "" || !owned.exists {
		return nil
	}
	if err := revalidateCloneFileSnapshot(owned); err != nil {
		return errors.New("publication generation changed; preserving it")
	}
	if original.exists {
		err := fsutil.WriteFileAtomicModeExpected(owned.path, original.data, original.mode.Perm(), owned.info)
		if err != nil {
			return fmt.Errorf("restore exact publication generation: %w", err)
		}
		restored, err := secureCloneFileSnapshot(original.path)
		if err != nil || !cloneSnapshotHasExactBytes(restored, original.data, original.mode.Perm()) {
			return errors.Join(errors.New("publication restore did not retain exact prior generation"), err)
		}
		return nil
	}
	authority, err := fsutil.OpenPrivatePath(filepath.Dir(owned.path), nil, filepath.Base(owned.path), false)
	if err != nil {
		return fmt.Errorf("retain exact publication removal authority: %w", err)
	}
	defer authority.Close()
	return authority.RemoveWithHook(func(step string) error {
		if step == "before-quarantine" {
			if beforeRemoval != nil {
				if err := beforeRemoval(owned.path); err != nil {
					return err
				}
			}
			return revalidateCloneFileSnapshot(owned)
		}
		return nil
	})
}

func (s *ReleaseMaterializeService) writeMaterializeRecovery(expected cloneFileSnapshot, owned *cloneFileSnapshot, ctx context.Context, base, baseHead string, manifest config.PortableManifest, manifestBytes, lockBytes []byte, children []materializeChildReceipt, configReceipt, stateReceipt, registryReceipt ClonePublicationReceipt, dataDir, projectID string, failures []error) error {
	value := store.RecoveryRecord{Version: store.Version, ProjectID: projectID, WorkspaceID: "default", Operation: "release-materialize", FailedStep: "publication-rollback", CompletedSteps: []string{"staged"}, UnrevertedSteps: []string{"publication"}}
	for _, failure := range failures {
		value.RollbackFailures = append(value.RollbackFailures, store.RollbackFailure{Step: "publication", Error: failure.Error()})
	}
	data, err := store.RecoveryBytes(value)
	if err != nil {
		return err
	}
	authority := func() error {
		currentManifest, manifestErr := os.ReadFile(filepath.Join(base, "project.wtree.yml"))
		currentLock, lockErr := os.ReadFile(filepath.Join(base, ReleaseLockFilename))
		if manifestErr != nil || lockErr != nil || !bytes.Equal(currentManifest, manifestBytes) || !bytes.Equal(currentLock, lockBytes) {
			return errors.New("base authority changed before recovery publication")
		}
		if baseErr := s.revalidateMaterializeBase(ctx, base, baseHead, manifest, manifestBytes, lockBytes); baseErr != nil {
			return baseErr
		}
		return revalidateCloneFileSnapshot(expected)
	}
	receipt, writeErr := s.writeMaterializeCAS(expected, data, authority, nil)
	if writeErr != nil {
		return writeErr
	}
	if !validClonePublicationReceipt(receipt, expected.path, data, 0o600) {
		return errors.New("recovery writer did not return exact owned receipt")
	}
	*owned = receipt.snapshot
	return nil
}

// revalidateMaterializeBase repeats every caller-owned authority fact at each
// publication boundary. Required ignored child mounts remain clean, while any
// unrelated tracked or untracked base mutation stops publication.
func (s *ReleaseMaterializeService) revalidateMaterializeBase(ctx context.Context, base, baseHead string, manifest config.PortableManifest, manifestBytes, lockBytes []byte) error {
	return s.revalidateMaterializeBaseWithTemp(ctx, base, baseHead, manifest, manifestBytes, lockBytes, "", nil)
}

func (s *ReleaseMaterializeService) revalidateMaterializeBaseWithTemp(ctx context.Context, base, baseHead string, manifest config.PortableManifest, manifestBytes, lockBytes []byte, temporary string, temporaryInfo os.FileInfo) error {
	head, err := s.git.Head(ctx, base)
	if err != nil || head != baseHead {
		return NewError(ErrorConflict, errors.New("caller base head changed during publication"))
	}
	if temporary == "" {
		clean, cleanErr := s.git.IsClean(ctx, base)
		if cleanErr != nil || !clean {
			return NewError(ErrorConflict, errors.New("caller base became dirty during publication"))
		}
	} else {
		current, tempErr := os.Lstat(temporary)
		if tempErr != nil || temporaryInfo == nil || !current.Mode().IsRegular() || !os.SameFile(temporaryInfo, current) {
			return NewError(ErrorConflict, errors.New("owned configuration temporary changed before publication"))
		}
		status, statusErr := s.git.Status(ctx, base)
		if statusErr != nil || status.Staged || status.Modified || len(status.Entries) != 1 || !status.Entries[0].Untracked || status.Entries[0].Path != filepath.Base(temporary) || status.Entries[0].OriginalPath != "" {
			return NewError(ErrorConflict, errors.New("caller base became dirty during configuration publication"))
		}
	}
	for _, item := range []struct {
		path string
		data []byte
	}{{"project.wtree.yml", manifestBytes}, {ReleaseLockFilename, lockBytes}} {
		tracked, trackedErr := s.git.TrackedFile(ctx, base, baseHead, item.path)
		if trackedErr != nil || !bytes.Equal(tracked, item.data) {
			return NewError(ErrorConflict, fmt.Errorf("base tracked %s changed during publication", item.path))
		}
	}
	contains, containsErr := s.git.ContainsCommits(ctx, base, manifest.Repositories[manifest.Project.BaseRepository].Identity.InitialCommits)
	if containsErr != nil || !contains {
		return NewError(ErrorConflict, errors.New("caller base identity roots changed during publication"))
	}
	return nil
}

func portableIDsParentFirst(manifest config.PortableManifest) []string {
	ids, _ := portableRepositoryOrder(manifest)
	return ids
}

func releaseLocalConfiguration(manifest config.PortableManifest, base string, paths map[string]string) config.ProjectConfig {
	repositories := map[string]config.Repository{}
	for id, repository := range manifest.Repositories {
		relative, _ := filepath.Rel(base, paths[id])
		repositories[id] = config.Repository{Source: filepath.ToSlash(relative), Parent: repository.Parent, DefaultMount: repository.Mount, DefaultBranch: repository.DefaultBranch}
	}
	return config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: manifest.Project.ID, Name: manifest.Project.Name, BaseRepository: manifest.Project.BaseRepository}, LogicalRoot: ".", Repositories: repositories, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: filepath.Join(base, "project.wtree.yml")}}
}
