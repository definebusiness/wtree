package service

// This file owns the deliberately small, future-facing companion baseline
// publication.  It does not use the workspace transaction: changing a
// baseline has no branch, worktree, or state effect.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
)

const repositoryBranchVersion = 1

type RepositoryBranchRequest struct {
	Project      domain.Project
	DataDir      string
	RepositoryID string
	Branch       string
	DryRun       bool
}

// RepositoryBranchPlan is immutable by convention: all byte slices are kept
// private and accessors return copies.  This stops callers from changing the
// generation which execution later compares.
type RepositoryBranchPlan struct {
	Version          int    `json:"version"`
	Operation        string `json:"operation"`
	ProjectID        string `json:"projectId"`
	RepositoryID     string `json:"repositoryId"`
	PreviousBaseline string `json:"previousBaseline"`
	Baseline         string `json:"baseline"`
	DataDir          string `json:"dataDir"`
	PortablePath     string `json:"portablePath"`
	LocalPath        string `json:"localPath"`
	private          repositoryBranchPrivate
}

type repositoryBranchPrivate struct {
	portableBefore, localBefore []byte
	portableAfter, localAfter   []byte
	portableMode, localMode     os.FileMode
	portableInfo, localInfo     os.FileInfo
	commonGitDir                string
	branchObjectID              string
	sourcePath                  string
	dataDir                     string
	projectID, repositoryID     string
	previousBaseline, baseline  string
	portablePath, localPath     string
}

func (p RepositoryBranchPlan) PortableBefore() []byte {
	return append([]byte(nil), p.private.portableBefore...)
}
func (p RepositoryBranchPlan) LocalBefore() []byte {
	return append([]byte(nil), p.private.localBefore...)
}
func (p RepositoryBranchPlan) PortableAfter() []byte {
	return append([]byte(nil), p.private.portableAfter...)
}
func (p RepositoryBranchPlan) LocalAfter() []byte {
	return append([]byte(nil), p.private.localAfter...)
}

func (p RepositoryBranchPlan) Changed() bool {
	return !bytes.Equal(p.private.portableBefore, p.private.portableAfter) || !bytes.Equal(p.private.localBefore, p.private.localAfter)
}

func (p RepositoryBranchPlan) Validate() error {
	if p.Version != repositoryBranchVersion || p.Operation != "repo-branch" || p.ProjectID != p.private.projectID || p.RepositoryID != p.private.repositoryID || p.PreviousBaseline != p.private.previousBaseline || p.Baseline != p.private.baseline || p.DataDir != p.private.dataDir || p.PortablePath != p.private.portablePath || p.LocalPath != p.private.localPath || p.private.projectID == "" || p.private.repositoryID == "" || p.private.previousBaseline == "" || p.private.baseline == "" || !filepath.IsAbs(p.private.dataDir) || !filepath.IsAbs(p.private.portablePath) || !filepath.IsAbs(p.private.localPath) || p.private.sourcePath == "" || p.private.commonGitDir == "" || p.private.branchObjectID == "" || p.private.portableInfo == nil || p.private.localInfo == nil || len(p.private.portableBefore) == 0 || len(p.private.localBefore) == 0 || len(p.private.portableAfter) == 0 || len(p.private.localAfter) == 0 {
		return NewError(ErrorValidation, errors.New("repository branch plan is incomplete"))
	}
	return nil
}

type RepositoryBranchResult struct {
	Version          int                      `json:"version"`
	Operation        string                   `json:"operation"`
	Status           string                   `json:"status"`
	DryRun           bool                     `json:"dryRun"`
	ProjectID        string                   `json:"projectId"`
	RepositoryID     string                   `json:"repositoryId"`
	PreviousBaseline string                   `json:"previousBaseline"`
	Baseline         string                   `json:"baseline"`
	PortableChanged  bool                     `json:"portableChanged"`
	LocalChanged     bool                     `json:"localChanged"`
	Failure          *RepositoryBranchFailure `json:"failure,omitempty"`
}

type RepositoryBranchFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RepositoryBranchService struct {
	git     gitadapter.Git
	locker  ProjectLocker
	read    func(string) ([]byte, error)
	lstat   func(string) (os.FileInfo, error)
	write   func(string, []byte, os.FileMode, os.FileInfo, func() error) error
	recover func(string, store.RecoveryRecord, func() error) error
}

func NewRepositoryBranchService() *RepositoryBranchService {
	return NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Lstat,
		func(path string, data []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
			if compare != nil {
				if err := compare(); err != nil {
					return err
				}
			}
			return fsutil.WriteFileAtomicModeExpected(path, data, mode, expected)
		}, store.WriteRecoveryCAS)
}

// NewRepositoryBranchServiceWith is intentionally exposed for hermetic tests
// of every publication and recovery boundary.
func NewRepositoryBranchServiceWith(git gitadapter.Git, locker ProjectLocker, read func(string) ([]byte, error), lstat func(string) (os.FileInfo, error), write func(string, []byte, os.FileMode, os.FileInfo, func() error) error, recover func(string, store.RecoveryRecord, func() error) error) *RepositoryBranchService {
	if git == nil {
		git = gitadapter.NewAdapter("git")
	}
	if locker == nil {
		locker = lock.Manager{}
	}
	if read == nil {
		read = os.ReadFile
	}
	if lstat == nil {
		lstat = os.Lstat
	}
	if write == nil {
		write = func(path string, data []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
			if compare != nil {
				if err := compare(); err != nil {
					return err
				}
			}
			return fsutil.WriteFileAtomicModeExpected(path, data, mode, expected)
		}
	}
	if recover == nil {
		recover = store.WriteRecoveryCAS
	}
	return &RepositoryBranchService{git: git, locker: locker, read: read, lstat: lstat, write: write, recover: recover}
}

func (s *RepositoryBranchService) Plan(ctx context.Context, request RepositoryBranchRequest) (RepositoryBranchPlan, error) {
	if s == nil {
		s = NewRepositoryBranchService()
	}
	if err := ctx.Err(); err != nil {
		return RepositoryBranchPlan{}, err
	}
	return s.capture(ctx, request)
}

func (s *RepositoryBranchService) Execute(ctx context.Context, request RepositoryBranchRequest) (RepositoryBranchResult, error) {
	plan, err := s.Plan(ctx, request)
	if err != nil {
		return RepositoryBranchResult{}, err
	}
	return s.ExecutePlan(ctx, request, plan)
}

// ExecutePlan is the plan-bound mutation boundary. It exists so a caller can
// render a frozen dry plan and still have execution reject any changed source
// generation or Git fact rather than silently substituting a new plan.
func (s *RepositoryBranchService) ExecutePlan(ctx context.Context, request RepositoryBranchRequest, plan RepositoryBranchPlan) (RepositoryBranchResult, error) {
	if s == nil {
		s = NewRepositoryBranchService()
	}
	if err := plan.Validate(); err != nil {
		return RepositoryBranchResult{}, err
	}
	dataDir, err := filepath.Abs(request.DataDir)
	if err != nil {
		return RepositoryBranchResult{}, NewError(ErrorValidation, fmt.Errorf("canonicalize data directory: %w", err))
	}
	if request.Project.ID != plan.private.projectID || request.RepositoryID != plan.private.repositoryID || request.Branch != plan.private.baseline || dataDir != plan.private.dataDir {
		return RepositoryBranchResult{}, NewError(ErrorValidation, errors.New("repository branch request does not match its plan"))
	}
	result := repositoryBranchResult(plan, request.DryRun)
	if request.DryRun {
		return result, nil
	}
	handle, err := acquireProjectMutationAuthority(ctx, s.locker, plan.private.dataDir, plan.private.projectID, time.Second)
	if err != nil {
		return RepositoryBranchResult{}, err
	}
	defer handle.Unlock()
	if blocked, checkErr := hasProjectRecovery(plan.private.dataDir, plan.private.projectID); checkErr != nil {
		return RepositoryBranchResult{}, NewError(ErrorConflict, fmt.Errorf("inspect project recovery: %w", checkErr))
	} else if blocked {
		return RepositoryBranchResult{}, NewError(ErrorConflict, errors.New("project recovery is present; recover it before changing a companion baseline"))
	}
	if err := s.revalidate(ctx, request, plan); err != nil {
		return RepositoryBranchResult{}, err
	}
	if !plan.Changed() {
		return result, nil
	}
	if err := s.publish(ctx, plan, plan.private.dataDir); err != nil {
		return RepositoryBranchResult{}, err
	}
	return result, nil
}

func repositoryBranchResult(plan RepositoryBranchPlan, dryRun bool) RepositoryBranchResult {
	result := RepositoryBranchResult{Version: repositoryBranchVersion, Operation: "repo-branch", DryRun: dryRun, ProjectID: plan.private.projectID, RepositoryID: plan.private.repositoryID, PreviousBaseline: plan.private.previousBaseline, Baseline: plan.private.baseline, PortableChanged: !bytes.Equal(plan.private.portableBefore, plan.private.portableAfter), LocalChanged: !bytes.Equal(plan.private.localBefore, plan.private.localAfter)}
	if !plan.Changed() {
		result.Status = "unchanged"
	} else if dryRun {
		result.Status = "planned"
	} else {
		result.Status = "completed"
	}
	return result
}

func (s *RepositoryBranchService) capture(ctx context.Context, request RepositoryBranchRequest) (RepositoryBranchPlan, error) {
	if request.DataDir == "" || request.Project.ID == "" || request.RepositoryID == "" || request.Branch == "" {
		return RepositoryBranchPlan{}, NewError(ErrorValidation, errors.New("project, data directory, repository, and branch are required"))
	}
	dataDir, err := filepath.Abs(request.DataDir)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorValidation, fmt.Errorf("canonicalize data directory: %w", err))
	}
	if blocked, err := hasProjectRecovery(dataDir, request.Project.ID); err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, fmt.Errorf("inspect project recovery: %w", err))
	} else if blocked {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("project recovery is present; recover it before changing a companion baseline"))
	}
	repository, found := repositoryBranchRepository(request.Project, request.RepositoryID)
	if !found {
		return RepositoryBranchPlan{}, NewError(ErrorValidation, fmt.Errorf("unknown repository %q", request.RepositoryID))
	}
	if !repository.Companion {
		return RepositoryBranchPlan{}, NewError(ErrorValidation, fmt.Errorf("repository %q is not a companion", request.RepositoryID))
	}
	valid, err := s.git.ValidBranchName(ctx, repository.SourcePath, request.Branch)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorGit, fmt.Errorf("validate branch %q: %w", request.Branch, err))
	}
	if !valid {
		return RepositoryBranchPlan{}, NewError(ErrorValidation, fmt.Errorf("branch %q is invalid", request.Branch))
	}
	common, err := s.git.CommonGitDir(ctx, repository.SourcePath)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorGit, fmt.Errorf("observe repository identity: %w", err))
	}
	if common != repository.CommonGitDir {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("repository Git identity changed"))
	}
	exists, err := s.git.BranchExists(ctx, repository.SourcePath, request.Branch)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorGit, fmt.Errorf("observe branch %q: %w", request.Branch, err))
	}
	if !exists {
		return RepositoryBranchPlan{}, NewError(ErrorValidation, fmt.Errorf("branch %q does not exist locally", request.Branch))
	}
	branchObjectID, err := s.git.ResolveRef(ctx, repository.SourcePath, "refs/heads/"+request.Branch)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorGit, fmt.Errorf("resolve branch %q: %w", request.Branch, err))
	}
	localInfo, err := s.lstat(request.Project.ConfigPath)
	if err != nil || !localInfo.Mode().IsRegular() {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("local configuration must be a regular file"))
	}
	localData, err := s.read(request.Project.ConfigPath)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, fmt.Errorf("read local configuration: %w", err))
	}
	local, err := config.LoadProject(localData)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, fmt.Errorf("decode local configuration: %w", err))
	}
	if local.Version != config.ProjectConfigVersion4 {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("companion local configuration must be version 4"))
	}
	manifestPath := filepath.Join(filepath.Dir(request.Project.ConfigPath), filepath.FromSlash(local.Manifest.Path))
	portableInfo, err := s.lstat(manifestPath)
	if err != nil || !portableInfo.Mode().IsRegular() {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("portable manifest must be a regular file"))
	}
	portableData, err := s.read(manifestPath)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, fmt.Errorf("read portable manifest: %w", err))
	}
	portable, err := config.LoadPortableManifest(portableData)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, fmt.Errorf("decode portable manifest: %w", err))
	}
	if portable.Version != config.PortableManifestVersion4 {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("companion portable manifest must be version 4"))
	}
	localRepo, localOK := local.Repositories[request.RepositoryID]
	portableRepo, portableOK := portable.Repositories[request.RepositoryID]
	if !localOK || !portableOK || !localRepo.Companion || !portableRepo.Companion || localRepo.DefaultBranch != repository.DefaultBranch || portableRepo.DefaultBranch != repository.DefaultBranch || !repositoryBranchAuthorityAligned(portable, local, request.Project) || !repositoryBranchSourcesAligned(local, request.Project) {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("local and portable companion baseline authority disagrees"))
	}
	if current, snapshotErr := s.lstat(request.Project.ConfigPath); snapshotErr != nil || !current.Mode().IsRegular() || !os.SameFile(localInfo, current) || current.Mode().Perm() != localInfo.Mode().Perm() {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("local configuration changed while capturing snapshot"))
	}
	if current, snapshotErr := s.lstat(manifestPath); snapshotErr != nil || !current.Mode().IsRegular() || !os.SameFile(portableInfo, current) || current.Mode().Perm() != portableInfo.Mode().Perm() {
		return RepositoryBranchPlan{}, NewError(ErrorConflict, errors.New("portable manifest changed while capturing snapshot"))
	}
	localRepo.DefaultBranch = request.Branch
	local.Repositories[request.RepositoryID] = localRepo
	portableRepo.DefaultBranch, portableRepo.Upstream.Branch = request.Branch, request.Branch
	portable.Repositories[request.RepositoryID] = portableRepo
	localAfter, err := config.MarshalProject(local)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorValidation, fmt.Errorf("encode local configuration: %w", err))
	}
	portableAfter, err := config.MarshalPortableManifest(portable)
	if err != nil {
		return RepositoryBranchPlan{}, NewError(ErrorValidation, fmt.Errorf("encode portable manifest: %w", err))
	}
	return RepositoryBranchPlan{Version: repositoryBranchVersion, Operation: "repo-branch", ProjectID: request.Project.ID, RepositoryID: request.RepositoryID, PreviousBaseline: repository.DefaultBranch, Baseline: request.Branch, DataDir: dataDir, PortablePath: manifestPath, LocalPath: request.Project.ConfigPath, private: repositoryBranchPrivate{portableBefore: append([]byte(nil), portableData...), localBefore: append([]byte(nil), localData...), portableAfter: portableAfter, localAfter: localAfter, portableMode: portableInfo.Mode().Perm(), localMode: localInfo.Mode().Perm(), portableInfo: portableInfo, localInfo: localInfo, commonGitDir: common, branchObjectID: branchObjectID, sourcePath: repository.SourcePath, dataDir: dataDir, projectID: request.Project.ID, repositoryID: request.RepositoryID, previousBaseline: repository.DefaultBranch, baseline: request.Branch, portablePath: manifestPath, localPath: request.Project.ConfigPath}}, nil
}

func repositoryBranchSourcesAligned(local config.ProjectConfig, project domain.Project) bool {
	for id, localRepository := range local.Repositories {
		repository, found := repositoryBranchRepository(project, id)
		if !found || repository.SourcePath == "" {
			return false
		}
		candidate, err := filepath.EvalSymlinks(filepath.Join(project.LogicalRoot, filepath.FromSlash(localRepository.Source)))
		if err != nil || candidate != repository.SourcePath {
			return false
		}
	}
	return true
}

// repositoryBranchAuthorityAligned intentionally compares the complete
// overlapping portable/local/project authority, not merely the selected row.
// A baseline command must never bless a pre-existing split elsewhere.
func repositoryBranchAuthorityAligned(portable config.PortableManifest, local config.ProjectConfig, project domain.Project) bool {
	if portable.Version != config.PortableManifestVersion4 || local.Version != config.ProjectConfigVersion4 || portable.Project.ID != local.Project.ID || portable.Project.Name != local.Project.Name || portable.Project.BaseRepository != local.Project.BaseRepository || portable.Project.ID != project.ID || portable.Project.Name != project.Name || portable.Project.BaseRepository != project.BaseRepository || len(portable.Repositories) != len(local.Repositories) || len(portable.Repositories) != len(project.Repositories) {
		return false
	}
	for id, portableRepository := range portable.Repositories {
		localRepository, localOK := local.Repositories[id]
		projectRepository, projectOK := repositoryBranchRepository(project, id)
		if !localOK || !projectOK || portableRepository.Upstream.Branch != portableRepository.DefaultBranch || localRepository.Parent != portableRepository.Parent || localRepository.DefaultMount != portableRepository.Mount || localRepository.DefaultBranch != portableRepository.DefaultBranch || localRepository.Companion != portableRepository.Companion || projectRepository.ParentID != portableRepository.Parent || projectRepository.DefaultMount != portableRepository.Mount || projectRepository.DefaultBranch != portableRepository.DefaultBranch || projectRepository.Companion != portableRepository.Companion {
			return false
		}
	}
	return true
}

func (s *RepositoryBranchService) revalidate(ctx context.Context, request RepositoryBranchRequest, plan RepositoryBranchPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	portable, err := s.read(plan.private.portablePath)
	if err != nil || !bytes.Equal(portable, plan.private.portableBefore) {
		return NewError(ErrorConflict, errors.New("portable manifest changed before publication"))
	}
	local, err := s.read(plan.private.localPath)
	if err != nil || !bytes.Equal(local, plan.private.localBefore) {
		return NewError(ErrorConflict, errors.New("local configuration changed before publication"))
	}
	portableInfo, err := s.lstat(plan.private.portablePath)
	if err != nil || !portableInfo.Mode().IsRegular() || !os.SameFile(portableInfo, plan.private.portableInfo) || portableInfo.Mode().Perm() != plan.private.portableMode {
		return NewError(ErrorConflict, errors.New("portable manifest identity or mode changed before publication"))
	}
	localInfo, err := s.lstat(plan.private.localPath)
	if err != nil || !localInfo.Mode().IsRegular() || !os.SameFile(localInfo, plan.private.localInfo) || localInfo.Mode().Perm() != plan.private.localMode {
		return NewError(ErrorConflict, errors.New("local configuration identity or mode changed before publication"))
	}
	common, err := s.git.CommonGitDir(ctx, plan.private.sourcePath)
	if err != nil || common != plan.private.commonGitDir {
		return NewError(ErrorConflict, errors.New("repository Git identity changed before publication"))
	}
	valid, err := s.git.ValidBranchName(ctx, plan.private.sourcePath, plan.private.baseline)
	if err != nil || !valid {
		return NewError(ErrorConflict, errors.New("branch changed before publication"))
	}
	exists, err := s.git.BranchExists(ctx, plan.private.sourcePath, plan.private.baseline)
	if err != nil || !exists {
		return NewError(ErrorConflict, errors.New("branch changed before publication"))
	}
	branchObjectID, err := s.git.ResolveRef(ctx, plan.private.sourcePath, "refs/heads/"+plan.private.baseline)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return NewError(ErrorGit, fmt.Errorf("resolve branch %q before publication: %w", plan.private.baseline, err))
	}
	if branchObjectID != plan.private.branchObjectID {
		return NewError(ErrorConflict, errors.New("branch tip changed before publication"))
	}
	return nil
}

func (s *RepositoryBranchService) publish(ctx context.Context, plan RepositoryBranchPlan, dataDir string) error {
	written := make([]repositoryBranchFile, 0, 2)
	targets := []repositoryBranchFile{{path: plan.private.portablePath, before: plan.private.portableBefore, after: plan.private.portableAfter, mode: plan.private.portableMode, beforeInfo: plan.private.portableInfo, name: "portable"}, {path: plan.private.localPath, before: plan.private.localBefore, after: plan.private.localAfter, mode: plan.private.localMode, beforeInfo: plan.private.localInfo, name: "local"}}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return s.cancelPublication(dataDir, plan, "before-publish-"+target.name, written, err)
		}
		err := s.write(target.path, target.after, target.mode, target.beforeInfo, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return s.verifyPublicationGenerations(targets, written)
		})
		outcome := fsutil.AtomicOutcome(err)
		if err == nil || outcome.ReplacementCompleted {
			if info, observed := s.repositoryBranchGeneration(target.path, target.after, target.mode); observed {
				target.afterInfo = info
				written = append(written, target)
			} else {
				// A nil result is an installed-generation acknowledgement.  If
				// the secure receipt cannot be captured afterwards, we cannot
				// safely CAS-restore it or pretend the target was untouched.
				return s.uncertainPublication(dataDir, plan, "publish-"+target.name, written, target, outcome.AuxiliaryPaths, err)
			}
		}
		if len(outcome.AuxiliaryPaths) != 0 {
			return s.auxiliaryPublication(dataDir, plan, "publish-"+target.name, written, outcome.AuxiliaryPaths, err)
		}
		if err != nil {
			rollback := s.rollback(written)
			if rollback.Err == nil {
				return NewError(ErrorConflict, NewCleanRollbackError(fmt.Errorf("publish %s configuration: %w", target.name, err)))
			}
			recordErr := s.recordRecovery(dataDir, plan, "publish-"+target.name, written, rollback)
			return NewError(ErrorRollbackIncomplete, errors.Join(fmt.Errorf("publish %s configuration: %w", target.name, err), rollback.Err, recordErr))
		}
	}
	if err := ctx.Err(); err != nil {
		return s.cancelPublication(dataDir, plan, "before-verify", written, err)
	}
	if err := s.verifyPublicationGenerations(targets, written); err != nil {
		rollback := s.rollback(written)
		if rollback.Err == nil {
			return NewError(ErrorConflict, NewCleanRollbackError(fmt.Errorf("verify published configuration: %w", err)))
		}
		recordErr := s.recordRecovery(dataDir, plan, "verify", written, rollback)
		return NewError(ErrorRollbackIncomplete, errors.Join(fmt.Errorf("verify published configuration: %w", err), rollback.Err, recordErr))
	}
	return nil
}

func (s *RepositoryBranchService) auxiliaryPublication(dataDir string, plan RepositoryBranchPlan, step string, written []repositoryBranchFile, paths []string, cause error) error {
	rollback := s.rollback(written)
	for _, path := range paths {
		rollback.Residual = append(rollback.Residual, repositoryBranchFile{name: "auxiliary:" + path})
		rollback.Failures = append(rollback.Failures, store.RollbackFailure{Step: "retain-auxiliary:" + path, Error: "atomic replacement retained an auxiliary generation"})
	}
	rollback.Err = errors.Join(rollback.Err, errors.New("atomic replacement retained auxiliary generation"))
	recordErr := s.recordRecovery(dataDir, plan, step, written, rollback)
	return NewError(ErrorRollbackIncomplete, errors.Join(cause, rollback.Err, recordErr))
}

// cancelPublication still restores generations already written: cancellation
// revokes further publication authority, not our duty to leave no split.
func (s *RepositoryBranchService) cancelPublication(dataDir string, plan RepositoryBranchPlan, step string, written []repositoryBranchFile, cause error) error {
	if len(written) == 0 {
		return cause
	}
	rollback := s.rollback(written)
	if rollback.Err == nil {
		return NewCleanRollbackError(cause)
	}
	recordErr := s.recordRecovery(dataDir, plan, step, written, rollback)
	return NewError(ErrorRollbackIncomplete, errors.Join(cause, rollback.Err, recordErr))
}

type repositoryBranchFile struct {
	path          string
	before, after []byte
	mode          os.FileMode
	beforeInfo    os.FileInfo
	afterInfo     os.FileInfo
	name          string
}

func (s *RepositoryBranchService) repositoryBranchGeneration(path string, data []byte, mode os.FileMode) (os.FileInfo, bool) {
	current, err := s.read(path)
	info, statErr := s.lstat(path)
	return info, err == nil && statErr == nil && info.Mode().IsRegular() && info.Mode().Perm() == mode && bytes.Equal(current, data)
}

func (s *RepositoryBranchService) uncertainPublication(dataDir string, plan RepositoryBranchPlan, step string, written []repositoryBranchFile, current repositoryBranchFile, auxiliaryPaths []string, cause error) error {
	installed := append(append([]repositoryBranchFile(nil), written...), current)
	// Previously written files have a receipt and can be safely CAS-restored.
	// The current file has no receipt, so it remains explicitly unreverted.
	rollback := s.rollback(written)
	rollback.Residual = append(rollback.Residual, current)
	rollback.Failures = append(rollback.Failures, store.RollbackFailure{Step: "observe-" + current.name, Error: "published generation receipt could not be captured"})
	for _, path := range auxiliaryPaths {
		rollback.Residual = append(rollback.Residual, repositoryBranchFile{name: "auxiliary:" + path})
		rollback.Failures = append(rollback.Failures, store.RollbackFailure{Step: "retain-auxiliary:" + path, Error: "atomic replacement retained an auxiliary generation"})
	}
	rollback.Err = errors.Join(rollback.Err, errors.New("published generation receipt could not be captured"))
	recordErr := s.recordRecovery(dataDir, plan, step, installed, rollback)
	return NewError(ErrorRollbackIncomplete, errors.Join(cause, errors.New("publication replacement outcome is unproven"), rollback.Err, recordErr))
}

func repositoryBranchNames(files []repositoryBranchFile) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.name)
	}
	return names
}

func (s *RepositoryBranchService) verifyPublicationGenerations(targets, written []repositoryBranchFile) error {
	for _, target := range targets {
		expected := target.before
		for _, published := range written {
			if published.path == target.path {
				expected = target.after
				break
			}
		}
		current, err := s.read(target.path)
		info, statErr := s.lstat(target.path)
		if err != nil || statErr != nil || !bytes.Equal(current, expected) || info.Mode().Perm() != target.mode {
			return errors.New("configuration generation changed during publication")
		}
	}
	return nil
}

type repositoryBranchRollback struct {
	Residual []repositoryBranchFile
	Failures []store.RollbackFailure
	Err      error
}

func (s *RepositoryBranchService) rollback(files []repositoryBranchFile) repositoryBranchRollback {
	result := repositoryBranchRollback{Residual: []repositoryBranchFile{}, Failures: []store.RollbackFailure{}}
	for index := len(files) - 1; index >= 0; index-- {
		file := files[index]
		err := s.write(file.path, file.before, file.mode, file.afterInfo, func() error {
			current, readErr := s.read(file.path)
			if readErr != nil || !bytes.Equal(current, file.after) {
				return errors.New("published generation is no longer owned")
			}
			return nil
		})
		outcome := fsutil.AtomicOutcome(err)
		if len(outcome.AuxiliaryPaths) != 0 {
			for _, path := range outcome.AuxiliaryPaths {
				result.Residual = append(result.Residual, repositoryBranchFile{name: "auxiliary:" + path})
				result.Failures = append(result.Failures, store.RollbackFailure{Step: "retain-auxiliary:" + path, Error: "atomic rollback retained an auxiliary generation"})
				result.Err = errors.Join(result.Err, errors.New("atomic rollback retained auxiliary generation"))
			}
		}
		if err != nil && outcome.ReplacementCompleted {
			if _, restored := s.repositoryBranchGeneration(file.path, file.before, file.mode); restored {
				continue
			}
		}
		if err != nil {
			result.Residual = append(result.Residual, file)
			result.Failures = append(result.Failures, store.RollbackFailure{Step: "restore-" + file.name, Error: err.Error()})
			result.Err = errors.Join(result.Err, err)
		}
	}
	return result
}

func (s *RepositoryBranchService) recordRecovery(dataDir string, plan RepositoryBranchPlan, step string, written []repositoryBranchFile, rollback repositoryBranchRollback) error {
	path := filepath.Join(dataDir, "projects", plan.private.projectID, "recovery", "repo-branch-"+plan.private.repositoryID+".json")
	return s.recover(path, store.RecoveryRecord{Version: store.Version, ProjectID: plan.private.projectID, WorkspaceID: "repo-branch-" + plan.private.repositoryID, Operation: "repo-branch", FailedStep: step, CompletedSteps: repositoryBranchNames(written), UnrevertedSteps: repositoryBranchNames(rollback.Residual), RollbackFailures: append([]store.RollbackFailure(nil), rollback.Failures...)}, func() error {
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect recovery generation: %w", err)
		}
		return errors.New("recovery generation already exists")
	})
}

func repositoryBranchRepository(project domain.Project, id string) (domain.Repository, bool) {
	for _, repository := range project.Repositories {
		if repository.ID == id {
			return repository, true
		}
	}
	return domain.Repository{}, false
}
