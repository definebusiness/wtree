package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/transaction"
)

// CloneExecutionResult describes a successfully published clone. Rendering is
// deliberately owned by the CLI milestone, not this transaction service.
type CloneExecutionResult struct {
	ProjectID    string
	Destination  string
	ConfigPath   string
	StatePath    string
	Repositories map[string]store.CheckoutState
}

// ClonePublicationLocker is the established registry-then-project lock
// boundary used only for final local publication, never remote access.
type ClonePublicationLocker interface {
	RegistryLock(context.Context, string, time.Duration) (lock.Handle, error)
	ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error)
}

// ClonePublicationReceipt is an opaque identity-bound proof that one injected
// writer invocation published a specific regular file generation. A zero
// receipt never authorizes rollback.
type ClonePublicationReceipt struct{ snapshot cloneFileSnapshot }

// CloneExecutorDependencies are complete effect seams. BeforeEffect and
// AfterEffect are deterministic test seams around every named boundary.
type CloneExecutorDependencies struct {
	Git               gitadapter.Git
	RegistryFacts     CloneRegistryFactsReader
	Locker            ClonePublicationLocker
	WriteConfig       func(string, config.ProjectConfig) error
	WriteWorkspaceCAS func(string, store.WorkspaceState, func() error) (ClonePublicationReceipt, error)
	WriteRegistryCAS  func(string, store.Registry, func() error) (ClonePublicationReceipt, error)
	WriteRecoveryCAS  func(string, store.RecoveryRecord, func() error) error
	WriteRawModeCAS   func(string, []byte, os.FileMode, func() error) error
	MkdirTemp         func(string, string) (string, error)
	Rename            func(string, string) error
	RemoveAll         func(string) error
	RemoveFileCAS     func(string, func() error) error
	Lstat             func(string) (os.FileInfo, error)
	EvalSymlinks      func(string) (string, error)
	BeforeEffect      func(string) error
	AfterEffect       func(string) error
}

type CloneExecutor struct{ dependencies CloneExecutorDependencies }

func NewCloneExecutor() *CloneExecutor { return NewCloneExecutorWith(CloneExecutorDependencies{}) }

func NewCloneExecutorWith(dependencies CloneExecutorDependencies) *CloneExecutor {
	if dependencies.Git == nil {
		dependencies.Git = gitadapter.NewAdapter("git")
	}
	if dependencies.RegistryFacts == nil {
		dependencies.RegistryFacts = newCloneRegistryFactsReader()
	}
	if dependencies.Locker == nil {
		dependencies.Locker = lock.Manager{}
	}
	if dependencies.WriteConfig == nil {
		dependencies.WriteConfig = config.WriteProjectFile
	}
	if dependencies.WriteWorkspaceCAS == nil {
		dependencies.WriteWorkspaceCAS = func(path string, value store.WorkspaceState, compare func() error) (ClonePublicationReceipt, error) {
			if err := store.WriteWorkspaceCAS(path, value, compare); err != nil {
				return ClonePublicationReceipt{}, err
			}
			snapshot, err := secureCloneFileSnapshot(path)
			return ClonePublicationReceipt{snapshot: snapshot}, err
		}
	}
	if dependencies.WriteRegistryCAS == nil {
		dependencies.WriteRegistryCAS = func(path string, value store.Registry, compare func() error) (ClonePublicationReceipt, error) {
			if err := store.WriteRegistryCAS(path, value, compare); err != nil {
				return ClonePublicationReceipt{}, err
			}
			snapshot, err := secureCloneFileSnapshot(path)
			return ClonePublicationReceipt{snapshot: snapshot}, err
		}
	}
	if dependencies.WriteRecoveryCAS == nil {
		dependencies.WriteRecoveryCAS = store.WriteRecoveryCAS
	}
	if dependencies.WriteRawModeCAS == nil {
		dependencies.WriteRawModeCAS = func(path string, data []byte, mode os.FileMode, compare func() error) error {
			return fsutil.WriteFileAtomicModeWithHook(path, data, mode, func(step string) error {
				if step == "before-rename" && compare != nil {
					return compare()
				}
				return nil
			})
		}
	}
	if dependencies.MkdirTemp == nil {
		dependencies.MkdirTemp = os.MkdirTemp
	}
	if dependencies.Rename == nil {
		dependencies.Rename = os.Rename
	}
	if dependencies.RemoveAll == nil {
		dependencies.RemoveAll = os.RemoveAll
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
	if dependencies.Lstat == nil {
		dependencies.Lstat = os.Lstat
	}
	if dependencies.EvalSymlinks == nil {
		dependencies.EvalSymlinks = filepath.EvalSymlinks
	}
	return &CloneExecutor{dependencies: dependencies}
}

// Execute materializes only decisions already owned by an immutable M03 plan.
func (executor *CloneExecutor) Execute(ctx context.Context, plan ClonePlan, progress func(transaction.Event)) (result CloneExecutionResult, returnErr error) {
	effects := newCloneEffectProgress(progress)
	progress = effects.report
	defer func() { effects.complete(returnErr) }()
	if executor == nil {
		return CloneExecutionResult{}, NewError(ErrorInternal, errors.New("clone executor is not configured"))
	}
	plan = clonePlanCopy(plan)
	if err := plan.Validate(); err != nil {
		return CloneExecutionResult{}, NewError(ErrorValidation, fmt.Errorf("validate clone plan: %w", err))
	}
	if len(plan.ManifestBytes()) == 0 {
		return CloneExecutionResult{}, NewError(ErrorValidation, errors.New("clone plan does not own validated manifest bytes"))
	}
	if err := executor.revalidateLocal(plan, true); err != nil {
		return CloneExecutionResult{}, classifyCloneExecutionError("revalidate clone plan", err)
	}
	preEffectRegistry, err := executor.dependencies.RegistryFacts.Read(plan.DataDir)
	if err != nil {
		return CloneExecutionResult{}, classifyCloneExecutionError("revalidate clone registry", err)
	}
	if err := validateCloneRegistryFacts(plan.Project.ID, plan.Destination.Path, preEffectRegistry, osCloneFileSystemFacts{}); err != nil {
		return CloneExecutionResult{}, err
	}
	ancestorIdentities := make([]clonePathIdentity, 0, len(plan.Destination.AncestorFacts))
	for _, fact := range plan.Destination.AncestorFacts {
		identity, err := captureClonePathIdentity(fact.Path)
		if err != nil {
			return CloneExecutionResult{}, classifyCloneExecutionError("capture clone ancestor identity", err)
		}
		ancestorIdentities = append(ancestorIdentities, identity)
	}
	if err := executor.before(ctx, progress, "staging-create"); err != nil {
		return CloneExecutionResult{}, cleanCloneError(err)
	}

	prefix := "." + filepath.Base(plan.Destination.Path) + ".wtree-clone-"
	staging, err := executor.dependencies.MkdirTemp(plan.Destination.CanonicalParent, prefix)
	if err != nil {
		return CloneExecutionResult{}, NewError(ErrorInternal, fmt.Errorf("create private clone staging: %w", err))
	}
	staging = filepath.Clean(staging)
	owned, err := executor.dependencies.Lstat(staging)
	if err != nil || !owned.IsDir() || owned.Mode().Perm()&0o077 != 0 || filepath.Dir(staging) != plan.Destination.CanonicalParent || !strings.HasPrefix(filepath.Base(staging), prefix) {
		return CloneExecutionResult{}, cleanCloneError(errors.New("staging creator returned an unsafe path"))
	}
	published := false
	ownershipCompromised := false
	identities := map[string]string{}
	var treeInventory cloneTreeInventory
	inventoryReady := false
	var configPublished cloneFileSnapshot
	expectedFinalIdentities := map[string]string{}
	var recordRecovery func(string, string, error) error
	cleanup := func(cause error) error {
		if ownershipCompromised {
			return errors.Join(cause, NewError(ErrorRollbackIncomplete, errors.New("clone staging or parent identity changed; preserving all paths")))
		}
		if cleanupErr := executor.removeOwnedTree(context.WithoutCancel(ctx), plan, staging, owned, published, inventoryReady, treeInventory, configPublished, expectedFinalIdentities); cleanupErr != nil {
			recoveryErr := error(nil)
			if published && recordRecovery != nil {
				recoveryErr = recordRecovery("cleanup", "destination", cleanupErr)
			}
			return errors.Join(cause, NewError(ErrorRollbackIncomplete, cleanupErr), recoveryErr)
		}
		if hasCloneErrorKind(cause, ErrorRollbackIncomplete) {
			return cause
		}
		return NewCleanRollbackError(cause)
	}
	if err := executor.after(ctx, progress, "staging-create"); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}

	paths := make(map[string]string, len(plan.Repositories))
	checkouts := make(map[string]store.CheckoutState, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		if err := ctx.Err(); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		path := staging
		if repository.Parent != "" {
			parent := planRepository(plan, repository.Parent)
			parentPath := paths[repository.Parent]
			if err := executor.before(ctx, progress, "repository-"+repository.ID+"-parent-ignore"); err != nil {
				return CloneExecutionResult{}, cleanup(err)
			}
			ignored, err := executor.dependencies.Git.IsIgnoredAt(ctx, parentPath, parent.AdvertisedCommit, repository.Mount)
			if err != nil || !ignored {
				if err == nil {
					err = fmt.Errorf("mount %q is not ignored by committed immediate-parent content", repository.Mount)
				}
				return CloneExecutionResult{}, cleanup(NewError(ErrorValidation, fmt.Errorf("verify repository %q parent ignore: %w", repository.ID, err)))
			}
			if err := executor.after(ctx, progress, "repository-"+repository.ID+"-parent-ignore"); err != nil {
				return CloneExecutionResult{}, cleanup(err)
			}
			path = filepath.Join(parentPath, filepath.FromSlash(repository.Mount))
			if _, err := executor.dependencies.Lstat(path); err == nil || !os.IsNotExist(err) {
				return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, fmt.Errorf("repository %q mount already exists", repository.ID)))
			}
		}
		if err := executor.before(ctx, progress, "repository-"+repository.ID+"-clone"); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		if err := executor.dependencies.Git.Clone(ctx, repository.CloneURL, path, repository.CloneRemote); err != nil {
			return CloneExecutionResult{}, cleanup(NewError(ErrorGit, fmt.Errorf("clone repository %q: %w", repository.ID, err)))
		}
		if err := executor.after(ctx, progress, "repository-"+repository.ID+"-clone"); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		if err := executor.before(ctx, progress, "repository-"+repository.ID+"-fetch"); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		if err := executor.dependencies.Git.FetchCommit(ctx, path, repository.CloneRemote, repository.AdvertisedCommit); err != nil {
			return CloneExecutionResult{}, cleanup(NewError(ErrorGit, fmt.Errorf("fetch planned commit for repository %q: %w", repository.ID, err)))
		}
		if err := executor.after(ctx, progress, "repository-"+repository.ID+"-fetch"); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		if err := executor.before(ctx, progress, "repository-"+repository.ID+"-checkout"); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		if err := executor.dependencies.Git.CheckoutTrackingBranch(ctx, path, repository.LocalBranch, repository.CloneRemote, repository.RemoteRef, repository.AdvertisedCommit); err != nil {
			return CloneExecutionResult{}, cleanup(NewError(ErrorGit, fmt.Errorf("checkout repository %q: %w", repository.ID, err)))
		}
		if err := executor.after(ctx, progress, "repository-"+repository.ID+"-checkout"); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		if err := executor.before(ctx, progress, "repository-"+repository.ID+"-verify"); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		common, head, err := executor.verifyRepository(ctx, plan, repository, path)
		if err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		if err := executor.after(ctx, progress, "repository-"+repository.ID+"-verify"); err != nil {
			return CloneExecutionResult{}, cleanup(err)
		}
		if previous, exists := identities[common]; exists {
			return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, fmt.Errorf("repositories %q and %q share a Git identity", previous, repository.ID)))
		}
		identities[common] = repository.ID
		paths[repository.ID] = path
		checkouts[repository.ID] = store.CheckoutState{Branch: repository.LocalBranch, Mount: repository.Mount, ResolvedPath: finalRepositoryPath(plan, repository.ID), Head: head}
	}

	configuration := cloneLocalConfiguration(plan)
	configPath := filepath.Join(staging, ".wtree.yml")
	expectedConfigBytes, err := config.MarshalProject(configuration)
	if err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorInternal, fmt.Errorf("encode local clone config: %w", err)))
	}
	configBefore, err := secureCloneFileSnapshot(configPath)
	if err != nil || configBefore.exists {
		if err == nil {
			err = errors.New("clone root already contains .wtree.yml")
		}
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, err))
	}
	if err := executor.before(ctx, progress, "local-config-write"); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	if err := revalidateCloneFileSnapshot(configBefore); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	if err := executor.dependencies.WriteConfig(configPath, configuration); err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorInternal, fmt.Errorf("write local clone config: %w", err)))
	}
	configPublished, err = secureCloneFileSnapshot(configPath)
	if err != nil || !configPublished.exists || !bytes.Equal(configPublished.data, expectedConfigBytes) || configPublished.mode.Perm() != 0o600 {
		return CloneExecutionResult{}, cleanup(NewError(ErrorInternal, errors.New("local clone config bytes, type, identity, or mode differ from plan")))
	}
	ignored, err := executor.dependencies.Git.IsIgnoredAt(ctx, staging, plan.Repositories[0].AdvertisedCommit, ".wtree.yml")
	if err != nil || !ignored {
		return CloneExecutionResult{}, cleanup(NewError(ErrorValidation, errors.New("committed root content does not ignore /.wtree.yml")))
	}
	if err := executor.afterValidated(ctx, progress, "local-config-write", func() error { return revalidateCloneFileSnapshot(configPublished) }); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	treeInventory, err = captureCloneTree(staging)
	if err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorInternal, fmt.Errorf("inventory staged clone: %w", err)))
	}
	inventoryReady = true
	configPublished.path = filepath.Join(plan.Destination.Path, ".wtree.yml")
	expectedFinalIdentities = translateCloneIdentities(identities, staging, plan.Destination.Path)

	if err := executor.before(ctx, progress, "publication-lock"); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	registryHandle, err := executor.dependencies.Locker.RegistryLock(ctx, plan.DataDir, time.Second)
	if err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, fmt.Errorf("acquire clone registry lock: %w", err)))
	}
	defer registryHandle.Unlock()
	projectHandle, err := executor.dependencies.Locker.ProjectLock(ctx, plan.DataDir, plan.Project.ID, time.Second)
	if err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, fmt.Errorf("acquire clone project lock: %w", err)))
	}
	defer projectHandle.Unlock()
	if err := executor.after(ctx, progress, "publication-lock"); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	if err := executor.revalidateLocal(plan, false); err != nil {
		return CloneExecutionResult{}, cleanup(classifyCloneExecutionError("publication revalidation", err))
	}

	registryPath := filepath.Join(plan.DataDir, "registry.json")
	statePath := WorkspaceStatePath(plan.DataDir, plan.Project.ID, "default")
	recoveryPath := filepath.Join(plan.DataDir, "projects", plan.Project.ID, "recovery", "default.json")
	registryGeneration, err := secureCloneFileSnapshot(registryPath)
	if err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, err))
	}
	stateGeneration, err := secureCloneFileSnapshot(statePath)
	if err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, err))
	}
	recoveryGeneration, err := secureCloneFileSnapshot(recoveryPath)
	if err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, err))
	}
	if recoveryGeneration.exists {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, errors.New("clone recovery record already exists")))
	}
	var recoveryOwned cloneFileSnapshot
	recordRecovery = func(failedStep, retainedStep string, failure error) error {
		return executor.writeCloneRecoveryCAS(plan, recoveryGeneration, &recoveryOwned, failedStep, retainedStep, failure)
	}
	registryFacts, err := executor.dependencies.RegistryFacts.Read(plan.DataDir)
	if err != nil {
		return CloneExecutionResult{}, cleanup(classifyCloneExecutionError("read publication registry", err))
	}
	// The generation is planning provenance, not a global serialization token:
	// unrelated clones may publish while remotes are being contacted. Re-run
	// the complete collision check against the stable locked generation.
	if err := validateCloneRegistryFacts(plan.Project.ID, plan.Destination.Path, registryFacts, osCloneFileSystemFacts{}); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	registryAgain, registryErr := secureCloneFileSnapshot(registryPath)
	stateAgain, stateErr := secureCloneFileSnapshot(statePath)
	recoveryAgain, recoveryErr := secureCloneFileSnapshot(recoveryPath)
	if registryErr != nil || stateErr != nil || recoveryErr != nil || !sameCloneFileSnapshot(registryGeneration, registryAgain) || !sameCloneFileSnapshot(stateGeneration, stateAgain) || !sameCloneFileSnapshot(recoveryGeneration, recoveryAgain) {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, errors.New("state, registry, or recovery generation changed during locked capture")))
	}
	if registryGeneration.exists && registryFacts.RegistrySHA256 != bytesSHA256(registryGeneration.data) || !registryGeneration.exists && registryFacts.RegistrySHA256 != "absent" {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, errors.New("registry collision facts do not match captured bytes")))
	}
	if stateGeneration.exists {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, errors.New("default workspace state already exists")))
	}
	if err := rejectRegistrationConflicts(filepath.Join(plan.Destination.Path, ".wtree.yml"), expectedFinalIdentities, registryFacts.Registry); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	if _, exists := registryFacts.Registry.Projects[plan.Project.ID]; exists {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, errors.New("project ID was registered concurrently")))
	}

	if err := executor.before(ctx, progress, "destination-rename"); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	for _, identity := range ancestorIdentities {
		if err := revalidateClonePathIdentity(identity); err != nil {
			ownershipCompromised = true
			return CloneExecutionResult{}, cleanup(err)
		}
	}
	stagingNow, err := executor.dependencies.Lstat(staging)
	if err != nil || !stagingNow.IsDir() || stagingNow.Mode()&os.ModeSymlink != 0 || !os.SameFile(owned, stagingNow) {
		ownershipCompromised = true
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, errors.New("staging identity changed before destination publication")))
	}
	if err := executor.revalidateLocal(plan, false); err != nil {
		return CloneExecutionResult{}, cleanup(classifyCloneExecutionError("destination rename revalidation", err))
	}
	if err := executor.dependencies.Rename(staging, plan.Destination.Path); err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, fmt.Errorf("publish clone destination: %w", err)))
	}
	published = true
	if err := translateCloneRootAfterRename(plan.Destination.Path, &treeInventory); err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, fmt.Errorf("capture published clone root: %w", err)))
	}
	if err := executor.afterValidated(ctx, progress, "destination-rename", func() error { return revalidateCloneTree(plan.Destination.Path, treeInventory) }); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	if err := executor.before(ctx, progress, "final-identity"); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	finalIdentities, err := executor.finalIdentities(ctx, plan)
	if err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	identities = finalIdentities
	if err := executor.afterValidated(ctx, progress, "final-identity", func() error { return revalidateCloneTree(plan.Destination.Path, treeInventory) }); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}

	state := store.WorkspaceState{Version: store.Version, ID: "default", Name: "default", Path: plan.Destination.Path, Repositories: checkouts}
	stateBytes := mustWorkspaceBytes(state)
	if err := executor.before(ctx, progress, "state-write"); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	if err := revalidateCloneTree(plan.Destination.Path, treeInventory); err != nil {
		return CloneExecutionResult{}, cleanup(NewError(ErrorConflict, err))
	}
	if err := revalidateClonePublicationGeneration(registryGeneration, stateGeneration, recoveryGeneration); err != nil {
		return CloneExecutionResult{}, cleanup(err)
	}
	stateCompare := func() error {
		if err := revalidateCloneTree(plan.Destination.Path, treeInventory); err != nil {
			return err
		}
		return revalidateClonePublicationGeneration(registryGeneration, stateGeneration, recoveryGeneration)
	}
	stateReceipt, stateWriteErr := executor.dependencies.WriteWorkspaceCAS(statePath, state, stateCompare)
	if stateWriteErr != nil {
		stateErr := executor.rollbackPublicationReceipt(stateGeneration, stateReceipt, stateBytes, 0o600)
		failure := errors.Join(NewError(ErrorInternal, fmt.Errorf("publish default workspace state: %w", stateWriteErr)), stateErr)
		if stateErr != nil {
			failure = errors.Join(failure, NewError(ErrorRollbackIncomplete, errors.New("clone state rollback incomplete")), recordRecovery("state-write", "state", stateErr))
		}
		return CloneExecutionResult{}, cleanup(failure)
	}
	stateOwned := stateReceipt.snapshot
	if !validClonePublicationReceipt(stateReceipt, statePath, stateBytes, 0o600) {
		stateErr := executor.rollbackPublicationReceipt(stateGeneration, stateReceipt, stateBytes, 0o600)
		return CloneExecutionResult{}, cleanup(errors.Join(NewError(ErrorConflict, errors.New("workspace state writer did not return an exact owned receipt")), stateErr))
	}
	if err := executor.afterValidated(ctx, progress, "state-write", func() error { return revalidateCloneFileSnapshot(stateOwned) }); err != nil {
		stateErr := executor.removeExactCloneSnapshot(stateOwned)
		failure := errors.Join(err, stateErr)
		if stateErr != nil {
			failure = errors.Join(failure, NewError(ErrorRollbackIncomplete, stateErr), recordRecovery("state-write", "state", stateErr))
		}
		return CloneExecutionResult{}, cleanup(failure)
	}
	stateWritten := true
	rollbackState := func() error {
		if !stateWritten {
			return nil
		}
		return executor.removeExactCloneSnapshot(stateOwned)
	}

	registry := cloneRegistry(registryFacts.Registry)
	if registry.Projects == nil {
		registry.Projects = map[string]store.RegistryProject{}
	}
	registry.Projects[plan.Project.ID] = store.RegistryProject{Name: plan.Project.Name, ConfigPath: filepath.Join(plan.Destination.Path, ".wtree.yml"), RepositoryIDs: identities}
	registryBytes := mustRegistryBytes(registry)
	if err := executor.before(ctx, progress, "registry-write"); err != nil {
		stateErr := rollbackState()
		failure := errors.Join(err, stateErr)
		if stateErr != nil {
			failure = errors.Join(failure, NewError(ErrorRollbackIncomplete, errors.New("clone state rollback incomplete")), recordRecovery("registry-write", "state", stateErr))
		}
		return CloneExecutionResult{}, cleanup(failure)
	}
	if err := revalidateCloneTree(plan.Destination.Path, treeInventory); err != nil {
		stateErr := rollbackState()
		return CloneExecutionResult{}, cleanup(errors.Join(NewError(ErrorConflict, err), stateErr))
	}
	if err := revalidateClonePublicationGeneration(registryGeneration, stateOwned, recoveryGeneration); err != nil {
		stateErr := rollbackState()
		return CloneExecutionResult{}, cleanup(errors.Join(err, stateErr))
	}
	registryCompare := func() error {
		if err := revalidateCloneTree(plan.Destination.Path, treeInventory); err != nil {
			return err
		}
		return revalidateClonePublicationGeneration(registryGeneration, stateOwned, recoveryGeneration)
	}
	registryReceipt, registryWriteErr := executor.dependencies.WriteRegistryCAS(registryPath, registry, registryCompare)
	if registryWriteErr != nil {
		stateErr := rollbackState()
		registryErr := executor.rollbackPublicationReceipt(registryGeneration, registryReceipt, registryBytes, 0o600)
		failure := errors.Join(NewError(ErrorInternal, fmt.Errorf("publish clone registry: %w", registryWriteErr)), stateErr, registryErr)
		if stateErr != nil || registryErr != nil {
			recoveryErr := error(nil)
			if registryErr != nil {
				recoveryErr = errors.Join(recoveryErr, recordRecovery("registry-write", "registry", registryErr))
			}
			if stateErr != nil {
				recoveryErr = errors.Join(recoveryErr, recordRecovery("registry-write", "state", stateErr))
			}
			failure = errors.Join(failure, NewError(ErrorRollbackIncomplete, errors.New("clone store rollback incomplete")), recoveryErr)
		}
		return CloneExecutionResult{}, cleanup(failure)
	}
	registryOwned := registryReceipt.snapshot
	if !validClonePublicationReceipt(registryReceipt, registryPath, registryBytes, 0o600) {
		registryErr := executor.rollbackPublicationReceipt(registryGeneration, registryReceipt, registryBytes, 0o600)
		stateErr := rollbackState()
		return CloneExecutionResult{}, cleanup(errors.Join(NewError(ErrorConflict, errors.New("registry writer did not return an exact owned receipt")), registryErr, stateErr))
	}
	if err := executor.afterValidated(ctx, progress, "registry-write", func() error { return revalidateCloneFileSnapshot(registryOwned) }); err != nil {
		registryErr := executor.restoreExactRegistrySnapshot(registryGeneration, registryOwned)
		stateErr := rollbackState()
		failure := errors.Join(err, registryErr, stateErr)
		if registryErr != nil || stateErr != nil {
			recoveryErr := error(nil)
			if registryErr != nil {
				recoveryErr = errors.Join(recoveryErr, recordRecovery("registry-write", "registry", registryErr))
			}
			if stateErr != nil {
				recoveryErr = errors.Join(recoveryErr, recordRecovery("registry-write", "state", stateErr))
			}
			failure = errors.Join(failure, NewError(ErrorRollbackIncomplete, errors.New("clone store rollback incomplete")), recoveryErr)
		}
		return CloneExecutionResult{}, cleanup(failure)
	}
	if err := revalidateCloneTree(plan.Destination.Path, treeInventory); err != nil {
		registryErr := executor.restoreExactRegistrySnapshot(registryGeneration, registryOwned)
		stateErr := rollbackState()
		return CloneExecutionResult{}, cleanup(errors.Join(NewError(ErrorConflict, err), registryErr, stateErr))
	}
	stateWritten = false
	return CloneExecutionResult{ProjectID: plan.Project.ID, Destination: plan.Destination.Path, ConfigPath: filepath.Join(plan.Destination.Path, ".wtree.yml"), StatePath: statePath, Repositories: checkouts}, nil
}

type cloneEffectProgress struct {
	emit   func(transaction.Event)
	active string
}

func newCloneEffectProgress(emit func(transaction.Event)) *cloneEffectProgress {
	return &cloneEffectProgress{emit: emit}
}

func (progress *cloneEffectProgress) report(event transaction.Event) {
	switch event.Kind {
	case transaction.ExecuteStarted:
		progress.active = event.Step
	case transaction.ExecuteSucceeded, transaction.ExecuteFailed:
		if progress.active == event.Step {
			progress.active = ""
		}
	}
	if progress.emit != nil {
		progress.emit(event)
	}
}

func (progress *cloneEffectProgress) complete(err error) {
	if progress.active == "" {
		return
	}
	if err == nil {
		err = errors.New("clone effect returned without a terminal outcome")
	}
	progress.report(transaction.Event{Kind: transaction.ExecuteFailed, Step: progress.active, Err: err})
}

func (executor *CloneExecutor) before(ctx context.Context, progress func(transaction.Event), name string) error {
	if progress != nil {
		progress(transaction.Event{Kind: transaction.ExecuteStarted, Step: name})
	}
	if err := ctx.Err(); err != nil {
		if progress != nil {
			progress(transaction.Event{Kind: transaction.ExecuteFailed, Step: name, Err: err})
		}
		return err
	}
	if executor.dependencies.BeforeEffect != nil {
		if err := executor.dependencies.BeforeEffect(name); err != nil {
			if progress != nil {
				progress(transaction.Event{Kind: transaction.ExecuteFailed, Step: name, Err: err})
			}
			return err
		}
	}
	return nil
}

func (executor *CloneExecutor) after(ctx context.Context, progress func(transaction.Event), name string) error {
	return executor.afterValidated(ctx, progress, name, nil)
}

func (executor *CloneExecutor) afterValidated(ctx context.Context, progress func(transaction.Event), name string, validate func() error) error {
	var err error
	if executor.dependencies.AfterEffect != nil {
		err = executor.dependencies.AfterEffect(name)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && validate != nil {
		err = validate()
	}
	if err != nil {
		if progress != nil {
			progress(transaction.Event{Kind: transaction.ExecuteFailed, Step: name, Err: err})
		}
		return err
	}
	if progress != nil {
		progress(transaction.Event{Kind: transaction.ExecuteSucceeded, Step: name})
	}
	return nil
}

func (executor *CloneExecutor) revalidateLocal(plan ClonePlan, exactFacts bool) error {
	if _, err := executor.dependencies.Lstat(plan.Destination.Path); err == nil {
		return NewError(ErrorConflict, errors.New("clone destination already exists"))
	} else if !os.IsNotExist(err) {
		return err
	}
	canonical, err := executor.dependencies.EvalSymlinks(plan.Destination.Parent)
	if err != nil || filepath.Clean(canonical) != plan.Destination.CanonicalParent {
		return NewError(ErrorConflict, errors.New("clone destination parent identity changed"))
	}
	for _, fact := range plan.Destination.AncestorFacts {
		info, err := executor.dependencies.Lstat(fact.Path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return NewError(ErrorConflict, fmt.Errorf("clone destination ancestor %q changed", fact.Path))
		}
		if exactFacts && (uint32(info.Mode()) != fact.Mode || info.ModTime().UTC().Format(time.RFC3339Nano) != fact.ModTime) {
			return NewError(ErrorConflict, fmt.Errorf("clone destination ancestor %q changed", fact.Path))
		}
	}
	return nil
}

func (executor *CloneExecutor) verifyRepository(ctx context.Context, plan ClonePlan, repository ClonePlanRepository, path string) (string, string, error) {
	info, statErr := executor.dependencies.Lstat(path)
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", NewError(ErrorConflict, fmt.Errorf("repository %q mount is not an owned real directory", repository.ID))
	}
	top, err := executor.dependencies.Git.TopLevel(ctx, path)
	if err != nil || filepath.Clean(top) != filepath.Clean(path) {
		return "", "", NewError(ErrorConflict, fmt.Errorf("repository %q has an unexpected checkout root", repository.ID))
	}
	head, err := executor.dependencies.Git.Head(ctx, path)
	if err != nil || head != repository.AdvertisedCommit {
		return "", "", NewError(ErrorGit, fmt.Errorf("repository %q is not at its planned commit", repository.ID))
	}
	branch, detached, err := executor.dependencies.Git.CurrentBranch(ctx, path)
	if err != nil || detached || branch != repository.LocalBranch {
		return "", "", NewError(ErrorGit, fmt.Errorf("repository %q has unexpected branch", repository.ID))
	}
	upstream, err := executor.dependencies.Git.Upstream(ctx, path)
	if err != nil || upstream.LocalBranch != repository.LocalBranch || upstream.Remote != repository.CloneRemote || upstream.Merge != repository.RemoteRef || upstream.FetchURL != repository.CloneURL {
		return "", "", NewError(ErrorGit, fmt.Errorf("repository %q has unexpected upstream", repository.ID))
	}
	contains, err := executor.dependencies.Git.ContainsCommits(ctx, path, repository.Verification.InitialCommits)
	if err != nil || !contains {
		return "", "", NewError(ErrorValidation, fmt.Errorf("repository %q does not contain every manifest identity root", repository.ID))
	}
	clean, err := executor.dependencies.Git.IsClean(ctx, path)
	if err != nil || !clean {
		return "", "", NewError(ErrorDirtyWorkspace, fmt.Errorf("repository %q clone is dirty", repository.ID))
	}
	hasSubmodules, err := executor.dependencies.Git.HasSubmodules(ctx, path)
	if err != nil || hasSubmodules {
		return "", "", NewError(ErrorValidation, fmt.Errorf("repository %q contains submodules", repository.ID))
	}
	if repository.Parent == "" {
		tracked, err := executor.dependencies.Git.TrackedFile(ctx, path, head, "project.wtree.yml")
		if err != nil || !bytes.Equal(tracked, plan.ManifestBytes()) {
			return "", "", NewError(ErrorValidation, errors.New("root tracked manifest does not equal the fetched manifest"))
		}
	}
	common, err := executor.dependencies.Git.CommonGitDir(ctx, path)
	if err != nil {
		return "", "", NewError(ErrorGit, err)
	}
	return common, head, nil
}

func (executor *CloneExecutor) finalIdentities(ctx context.Context, plan ClonePlan) (map[string]string, error) {
	identities := make(map[string]string, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		path := finalRepositoryPath(plan, repository.ID)
		common, head, err := executor.verifyRepository(ctx, plan, repository, path)
		if err != nil {
			return nil, err
		}
		if head != repository.AdvertisedCommit {
			return nil, NewError(ErrorConflict, errors.New("final repository identity changed"))
		}
		if previous, exists := identities[common]; exists {
			return nil, NewError(ErrorConflict, fmt.Errorf("repositories %q and %q share final Git identity", previous, repository.ID))
		}
		identities[common] = repository.ID
	}
	return identities, nil
}

func (executor *CloneExecutor) removeOwnedTree(ctx context.Context, plan ClonePlan, staging string, created os.FileInfo, published, inventoryReady bool, inventory cloneTreeInventory, configSnapshot cloneFileSnapshot, expectedIdentities map[string]string) error {
	target := staging
	if published {
		target = plan.Destination.Path
	}
	if filepath.Clean(filepath.Dir(target)) != filepath.Clean(plan.Destination.CanonicalParent) {
		return errors.New("refuse clone cleanup outside destination parent")
	}
	info, err := executor.dependencies.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !os.SameFile(created, info) || !info.IsDir() {
		return fmt.Errorf("refuse cleanup of path whose identity changed: %s", target)
	}
	if published {
		if !inventoryReady || len(expectedIdentities) != len(plan.Repositories) {
			return errors.New("refuse published destination cleanup without complete owned inventory and identities")
		}
		if err := revalidateCloneTree(target, inventory); err != nil {
			return fmt.Errorf("refuse cleanup after destination changed: %w", err)
		}
		if configSnapshot.path != filepath.Join(target, ".wtree.yml") || revalidateCloneFileSnapshot(configSnapshot) != nil {
			return errors.New("refuse published destination cleanup after config identity changed")
		}
		configuration, err := config.ReadProjectFile(filepath.Join(target, ".wtree.yml"))
		if err != nil || configuration.Project.ID != plan.Project.ID || len(configuration.Repositories) != len(plan.Repositories) {
			return errors.New("refuse published destination cleanup after project identity changed")
		}
		seen := make(map[string]bool, len(plan.Repositories))
		for _, repository := range plan.Repositories {
			path := finalRepositoryPath(plan, repository.ID)
			common, err := executor.dependencies.Git.CommonGitDir(ctx, path)
			if err != nil || expectedIdentities[common] != repository.ID {
				return fmt.Errorf("refuse cleanup after repository %q Git identity changed", repository.ID)
			}
			seen[repository.ID] = true
		}
		if len(seen) != len(expectedIdentities) {
			return errors.New("refuse cleanup after repository identity set changed")
		}
	}
	return executor.dependencies.RemoveAll(target)
}

func (executor *CloneExecutor) writeCloneRecoveryCAS(plan ClonePlan, original cloneFileSnapshot, owned *cloneFileSnapshot, failedStep, retainedStep string, failure error) error {
	value := store.RecoveryRecord{
		Version: store.Version, ProjectID: plan.Project.ID, WorkspaceID: "default", Operation: "clone",
		FailedStep: failedStep, UnrevertedSteps: []string{retainedStep},
		RollbackFailures: []store.RollbackFailure{{Step: retainedStep, Error: failure.Error()}},
	}
	expected := original
	if owned != nil && owned.exists {
		expected = *owned
		prior, err := store.DecodeRecovery(owned.data)
		if err != nil || prior.Operation != "clone" || prior.ProjectID != plan.Project.ID || prior.WorkspaceID != "default" {
			return errors.New("owned clone recovery record is invalid")
		}
		value.CompletedSteps = append(value.CompletedSteps, prior.CompletedSteps...)
		value.UnrevertedSteps = append(prior.UnrevertedSteps, value.UnrevertedSteps...)
		value.RollbackFailures = append(prior.RollbackFailures, value.RollbackFailures...)
	}
	if err := revalidateCloneFileSnapshot(expected); err != nil {
		return fmt.Errorf("recovery generation changed; preserving it: %w", err)
	}
	if executor.dependencies.BeforeEffect != nil {
		if err := executor.dependencies.BeforeEffect("recovery-write"); err != nil {
			return err
		}
	}
	if err := revalidateCloneFileSnapshot(expected); err != nil {
		return fmt.Errorf("recovery generation changed before CAS: %w", err)
	}
	if err := executor.dependencies.WriteRecoveryCAS(original.path, value, func() error { return revalidateCloneFileSnapshot(expected) }); err != nil {
		return fmt.Errorf("write clone recovery evidence: %w", err)
	}
	data, err := store.RecoveryBytes(value)
	if err != nil {
		return err
	}
	published, err := secureCloneFileSnapshot(original.path)
	if err != nil || !cloneSnapshotHasExactBytes(published, data, 0o600) {
		return errors.Join(errors.New("recovery writer did not publish exact owned bytes"), err)
	}
	if owned != nil {
		*owned = published
	}
	return nil
}

func hasCloneErrorKind(err error, kind ErrorKind) bool {
	if err == nil {
		return false
	}
	if application, ok := err.(*Error); ok && application.Kind == kind {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if hasCloneErrorKind(child, kind) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return hasCloneErrorKind(wrapped.Unwrap(), kind)
	}
	return false
}

func cloneLocalConfiguration(plan ClonePlan) config.ProjectConfig {
	repositories := make(map[string]config.Repository, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		source := "."
		if repository.Parent != "" {
			relative, _ := filepath.Rel(plan.Destination.Path, repository.Path)
			source = filepath.ToSlash(relative)
		}
		repositories[repository.ID] = config.Repository{Source: source, Parent: repository.Parent, DefaultMount: repository.Mount, DefaultBranch: repository.LocalBranch}
	}
	return config.ProjectConfig{Version: config.Version, Project: config.Project{ID: plan.Project.ID, Name: plan.Project.Name}, Repositories: repositories, Worktrees: config.Worktrees{Root: plan.WorktreeRoot}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: plan.Source.Value}}
}

func planRepository(plan ClonePlan, id string) ClonePlanRepository {
	for _, repository := range plan.Repositories {
		if repository.ID == id {
			return repository
		}
	}
	return ClonePlanRepository{}
}
func finalRepositoryPath(plan ClonePlan, id string) string { return planRepository(plan, id).Path }
func translateCloneIdentities(identities map[string]string, staging, destination string) map[string]string {
	translated := make(map[string]string, len(identities))
	for identity, id := range identities {
		value := identity
		if identity == staging || pathWithin(staging, identity) {
			relative, err := filepath.Rel(staging, identity)
			if err == nil {
				value = filepath.Join(destination, relative)
			}
		}
		translated[filepath.Clean(value)] = id
	}
	return translated
}
func mustWorkspaceBytes(value store.WorkspaceState) []byte {
	data, _ := store.WorkspaceBytes(value)
	return data
}
func mustRegistryBytes(value store.Registry) []byte {
	data, _ := store.RegistryBytes(value)
	return data
}

func (executor *CloneExecutor) removeExactCloneSnapshot(expected cloneFileSnapshot) error {
	current, err := secureCloneFileSnapshot(expected.path)
	if err != nil {
		return err
	}
	if !sameCloneFileSnapshot(expected, current) {
		return errors.New("refuse to remove concurrently replaced transaction file")
	}
	return executor.dependencies.RemoveFileCAS(expected.path, func() error { return revalidateCloneFileSnapshot(expected) })
}

func validClonePublicationReceipt(receipt ClonePublicationReceipt, path string, data []byte, mode os.FileMode) bool {
	snapshot := receipt.snapshot
	return snapshot.path == path && snapshot.info != nil && cloneSnapshotHasExactBytes(snapshot, data, mode)
}

func (executor *CloneExecutor) rollbackPublicationReceipt(original cloneFileSnapshot, receipt ClonePublicationReceipt, attempted []byte, mode os.FileMode) error {
	if !validClonePublicationReceipt(receipt, original.path, attempted, mode) {
		current, err := secureCloneFileSnapshot(original.path)
		if err != nil {
			return err
		}
		if sameCloneFileSnapshot(original, current) {
			return nil
		}
		return errors.New("writer returned no valid owned receipt; preserving current publication file")
	}
	owned := receipt.snapshot
	if err := revalidateCloneFileSnapshot(owned); err != nil {
		return errors.New("receipt generation changed; preserving current publication file")
	}
	if original.exists {
		return executor.dependencies.WriteRawModeCAS(original.path, original.data, original.mode.Perm(), func() error {
			return revalidateCloneFileSnapshot(owned)
		})
	}
	return executor.dependencies.RemoveFileCAS(owned.path, func() error { return revalidateCloneFileSnapshot(owned) })
}

func (executor *CloneExecutor) restoreExactRegistrySnapshot(original, owned cloneFileSnapshot) error {
	if err := revalidateCloneFileSnapshot(owned); err != nil {
		return errors.New("refuse to restore concurrently replaced registry")
	}
	if original.exists {
		return executor.dependencies.WriteRawModeCAS(original.path, original.data, original.mode.Perm(), func() error { return revalidateCloneFileSnapshot(owned) })
	}
	return executor.dependencies.RemoveFileCAS(owned.path, func() error { return revalidateCloneFileSnapshot(owned) })
}

func cleanCloneError(err error) error {
	var application *Error
	if !errors.As(err, &application) {
		err = NewError(ErrorInternal, err)
	}
	return NewCleanRollbackError(err)
}
func classifyCloneExecutionError(prefix string, err error) error {
	var application *Error
	if errors.As(err, &application) {
		return err
	}
	return NewError(ErrorConflict, fmt.Errorf("%s: %w", prefix, err))
}

// Stable repository order is useful to renderers consuming the result without
// exposing a map's iteration order.
func (result CloneExecutionResult) RepositoryIDs() []string {
	ids := make([]string, 0, len(result.Repositories))
	for id := range result.Repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
