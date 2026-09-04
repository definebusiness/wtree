package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
)

const ReleaseLockFilename = "project.wtree.lock.yml"

// ReleaseLockRequest is deliberately local-only. Release lock observation
// never fetches, tags, pushes, or otherwise contacts a remote.
type ReleaseLockRequest struct {
	Project                domain.Project
	Workspace              domain.Workspace
	Name                   string
	Force, DryRun, NoHooks bool
	DataDir                string
	Environment            []string
	Windows                bool
}
type ReleaseLockResult struct {
	Version        int                           `json:"version"`
	Operation      string                        `json:"operation"`
	Status         string                        `json:"status"`
	ProjectID      string                        `json:"projectId"`
	ReleaseName    string                        `json:"releaseName"`
	LockPath       string                        `json:"lockPath"`
	ManifestSHA256 string                        `json:"manifestSha256"`
	DryRun         bool                          `json:"dryRun"`
	Repositories   []ReleaseLockRepositoryResult `json:"repositories"`
	HooksCompleted bool                          `json:"hooksCompleted"`
	HooksSkipped   bool                          `json:"hooksSkipped"`
	HookFailure    string                        `json:"hookFailure,omitempty"`
}
type ReleaseLockRepositoryResult struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

type releaseLockGit interface {
	CommonGitDir(context.Context, string) (string, error)
	Head(context.Context, string) (string, error)
	Status(context.Context, string) (gitadapter.Status, error)
}
type releaseTrackedFile interface {
	TrackedFile(context.Context, string, string, string) ([]byte, error)
}
type releaseAuthorityGit interface {
	ConfiguredRemoteURL(context.Context, string, string) (string, error)
	ContainsCommits(context.Context, string, []string) (bool, error)
}

type ReleaseLockService struct {
	git             releaseLockGit
	process         HookProcessAdapter
	readFile        func(string) ([]byte, error)
	lstat           func(string) (os.FileInfo, error)
	write           func(string, []byte, os.FileMode) error
	create          func(string, []byte, os.FileMode) error
	replaceExpected func(string, []byte, os.FileMode, os.FileInfo) error
	// beforeWrite is a hermetic test seam for the final target identity check.
	beforeWrite func()
}

func NewReleaseLockService() *ReleaseLockService { return NewReleaseLockServiceWith(nil, nil) }
func NewReleaseLockServiceWith(g releaseLockGit, process HookProcessAdapter) *ReleaseLockService {
	if g == nil {
		g = gitadapter.NewAdapter("git")
	}
	if process == nil {
		process = newHookProcessAdapter()
	}
	return &ReleaseLockService{git: g, process: process, readFile: os.ReadFile, lstat: os.Lstat, write: fsutil.WriteFileAtomicMode, create: fsutil.WriteFileAtomicCreateMode, replaceExpected: fsutil.WriteFileAtomicModeExpected}
}

func (s *ReleaseLockService) Lock(ctx context.Context, q ReleaseLockRequest) (ReleaseLockResult, error) {
	result := ReleaseLockResult{Version: 1, Operation: "release-lock", ProjectID: q.Project.ID, ReleaseName: q.Name, DryRun: q.DryRun, Repositories: []ReleaseLockRepositoryResult{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := q.Project.Validate(); err != nil {
		return result, NewError(ErrorValidation, err)
	}
	if q.Workspace.Partial {
		return result, NewError(ErrorValidation, errors.New("release lock requires a complete workspace"))
	}
	if err := q.Workspace.Validate(q.Project); err != nil {
		return result, NewError(ErrorValidation, err)
	}
	if q.Name == "" || strings.IndexFunc(q.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return result, NewError(ErrorInvalidArguments, errors.New("release name is required and must not contain control characters"))
	}
	base, err := q.Workspace.ResolveRepository(q.Project.BaseRepository)
	if err != nil {
		return result, NewError(ErrorValidation, err)
	}
	manifestPath := filepath.Join(base, "project.wtree.yml")
	manifestBytes, err := s.readFile(manifestPath)
	if err != nil {
		return result, NewError(ErrorValidation, errors.New("portable manifest is unavailable from the base checkout"))
	}
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		return result, NewError(ErrorValidation, fmt.Errorf("load portable manifest: %w", err))
	}
	if manifest.Project.ID != q.Project.ID || manifest.Project.BaseRepository != q.Project.BaseRepository {
		return result, NewError(ErrorConflict, errors.New("portable manifest project identity does not match workspace"))
	}
	nonBase := make([]string, 0, len(q.Project.Repositories)-1)
	baseHead := ""
	baseLockDirty := false
	for _, repo := range q.Project.Repositories {
		if _, ok := manifest.Repositories[repo.ID]; !ok {
			return result, NewError(ErrorConflict, errors.New("portable manifest repository set does not match workspace"))
		}
		if repo.ID != q.Project.BaseRepository {
			nonBase = append(nonBase, repo.ID)
		}
	}
	if len(manifest.Repositories) != len(q.Project.Repositories) {
		return result, NewError(ErrorConflict, errors.New("portable manifest repository set does not match workspace"))
	}
	// The portable manifest defines the immutable repository graph. Reconcile
	// every registered fact before observing commits; a matching ID alone is
	// not sufficient authority for a release lock.
	for _, repo := range q.Project.Repositories {
		portable := manifest.Repositories[repo.ID]
		if repo.ParentID != portable.Parent || repo.DefaultMount != portable.Mount || repo.DefaultBranch != "" && repo.DefaultBranch != portable.DefaultBranch {
			return result, NewError(ErrorConflict, fmt.Errorf("portable manifest authority differs for repository %q", repo.ID))
		}
	}
	// Workspace validation intentionally accepts persisted mount overrides for
	// ordinary workspace operations. A release lock instead binds to the
	// portable graph, so every selected checkout must retain that graph's mount
	// and the corresponding authoritative resolved path before we observe Git.
	portableMounts := make(map[string]string, len(manifest.Repositories))
	for id, repo := range manifest.Repositories {
		portableMounts[id] = repo.Mount
	}
	portablePaths, pathErr := q.Project.EffectivePaths(q.Workspace.RootPath, portableMounts)
	if pathErr != nil {
		return result, NewError(ErrorConflict, fmt.Errorf("resolve portable repository topology: %w", pathErr))
	}
	for _, checkout := range q.Workspace.Checkouts {
		portable := manifest.Repositories[checkout.RepositoryID]
		if checkout.Mount != portable.Mount || checkout.ResolvedPath != portablePaths[checkout.RepositoryID] {
			return result, NewError(ErrorConflict, fmt.Errorf("workspace checkout %q differs from portable manifest topology", checkout.RepositoryID))
		}
	}
	sort.Strings(nonBase)
	for _, repo := range q.Project.Repositories {
		path, pathErr := q.Workspace.ResolveRepository(repo.ID)
		if pathErr != nil {
			return result, NewError(ErrorValidation, pathErr)
		}
		common, commonErr := s.git.CommonGitDir(ctx, path)
		if commonErr != nil || filepath.Clean(common) != filepath.Clean(repo.CommonGitDir) {
			return result, NewError(ErrorConflict, fmt.Errorf("repository %q identity does not match workspace", repo.ID))
		}
		if authority, ok := s.git.(releaseAuthorityGit); ok {
			portable := manifest.Repositories[repo.ID]
			url, urlErr := authority.ConfiguredRemoteURL(ctx, path, portable.Clone.Remote)
			if urlErr != nil || url != portable.Clone.URL {
				return result, NewError(ErrorConflict, fmt.Errorf("repository %q clone remote differs from portable manifest", repo.ID))
			}
			contains, containsErr := authority.ContainsCommits(ctx, path, portable.Identity.InitialCommits)
			if containsErr != nil || !contains {
				return result, NewError(ErrorConflict, fmt.Errorf("repository %q does not match portable identity roots", repo.ID))
			}
		}
		status, statusErr := s.git.Status(ctx, path)
		if statusErr != nil {
			return result, NewError(ErrorGit, statusErr)
		}
		for _, entry := range status.Entries {
			if repo.ID != q.Project.BaseRepository || filepath.ToSlash(entry.Path) != ReleaseLockFilename {
				return result, NewError(ErrorDirtyWorkspace, fmt.Errorf("repository %q has uncommitted changes", repo.ID))
			}
			baseLockDirty = true
		}
		if repo.ID == q.Project.BaseRepository {
			baseHead, err = s.git.Head(ctx, path)
			if err != nil {
				return result, NewError(ErrorGit, err)
			}
		}
		if repo.ID != q.Project.BaseRepository {
			head, headErr := s.git.Head(ctx, path)
			if headErr != nil {
				return result, NewError(ErrorGit, headErr)
			}
			result.Repositories = append(result.Repositories, ReleaseLockRepositoryResult{ID: repo.ID, Revision: head})
		}
	}
	sort.Slice(result.Repositories, func(i, j int) bool { return result.Repositories[i].ID < result.Repositories[j].ID })
	manifestLock := config.ReleaseLock{Version: config.ReleaseLockVersion, Project: config.ReleaseLockProject{ID: q.Project.ID, ManifestSHA256: config.ReleaseManifestSHA256(manifestBytes)}, Release: config.ReleaseLockRelease{Name: q.Name}, Repositories: map[string]config.ReleaseLockRepository{}}
	for _, item := range result.Repositories {
		manifestLock.Repositories[item.ID] = config.ReleaseLockRepository{Revision: item.Revision}
	}
	candidate, err := config.MarshalReleaseLock(manifestLock)
	if err != nil {
		return result, NewError(ErrorInternal, err)
	}
	lockPath := filepath.Join(base, ReleaseLockFilename)
	result.LockPath = lockPath
	result.ManifestSHA256 = manifestLock.Project.ManifestSHA256
	if q.DryRun {
		disposition, dispositionErr := s.disposition(ctx, base, baseHead, baseLockDirty, lockPath, candidate, q.Force)
		if dispositionErr != nil {
			return result, dispositionErr
		}
		result.Status = disposition
		return result, nil
	}
	var hookPlan HookPlan
	hooksApplicable := false
	if !q.NoHooks {
		hookPlan, hooksApplicable, err = s.prepareHooks(ctx, q, result.Repositories)
		if err != nil {
			return result, err
		}
	}
	var projectLock lock.Handle
	if !q.DryRun && q.DataDir != "" {
		projectLock, err = (lock.Manager{}).ProjectLock(ctx, q.DataDir, q.Project.ID, time.Second)
		if err != nil {
			return result, NewError(ErrorConflict, fmt.Errorf("acquire release lock authority: %w", err))
		}
	}
	defer func() {
		if projectLock != nil {
			_ = projectLock.Unlock()
		}
	}()
	disposition, err := s.disposition(ctx, base, baseHead, baseLockDirty, lockPath, candidate, q.Force)
	if err != nil {
		return result, err
	}
	result.Status = disposition
	if disposition != "unchanged" {
		expected, expectedExists, identityErr := s.targetIdentity(lockPath)
		if identityErr != nil {
			return result, identityErr
		}
		if s.beforeWrite != nil {
			s.beforeWrite()
		}
		if identityErr := s.requireSameTarget(lockPath, expected, expectedExists); identityErr != nil {
			return result, identityErr
		}
		writer := s.write
		if disposition == "created" {
			writer = s.create
		}
		if disposition == "replaced" {
			if err := s.replaceExpected(lockPath, candidate, 0o600, expected); err != nil {
				return result, NewError(ErrorConflict, fmt.Errorf("write release lock: %w", err))
			}
		} else if err := writer(lockPath, candidate, 0o600); err != nil {
			return result, NewError(ErrorConflict, fmt.Errorf("write release lock: %w", err))
		}
	}
	// Hooks deliberately run after the mutation authority is released: their
	// caller-owned side effects are not part of the lock-file transaction.
	if projectLock != nil {
		_ = projectLock.Unlock()
		projectLock = nil
	}
	if q.NoHooks {
		result.HooksSkipped = true
		return result, nil
	}
	if !hooksApplicable {
		return result, nil
	}
	failure, err := s.runHooks(ctx, q, hookPlan)
	if failure != "" {
		result.HookFailure = failure
		return result, NewError(ErrorSetupIncomplete, &SetupIncompleteError{Details: SetupIncompleteDetails{Operation: "release lock", CoreStatus: "succeeded", SetupStatus: "failed", Event: config.HookEventPostRelease, HookID: failure, RetryCommand: "wtree release lock " + q.Name}, Cause: err})
	}
	if err != nil {
		return result, err
	}
	result.HooksCompleted = true
	return result, nil
}

func (s *ReleaseLockService) targetIdentity(target string) (os.FileInfo, bool, error) {
	info, err := s.lstat(target)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, NewError(ErrorConflict, fmt.Errorf("inspect release lock target: %w", err))
	}
	if !info.Mode().IsRegular() {
		return nil, false, NewError(ErrorConflict, errors.New("release lock target must be a regular file"))
	}
	return info, true, nil
}
func (s *ReleaseLockService) requireSameTarget(target string, expected os.FileInfo, expectedExists bool) error {
	actual, exists, err := s.targetIdentity(target)
	if err != nil {
		return err
	}
	if exists != expectedExists || exists && !os.SameFile(expected, actual) {
		return NewError(ErrorConflict, errors.New("release lock target changed before atomic write"))
	}
	return nil
}

func (s *ReleaseLockService) disposition(ctx context.Context, base, baseHead string, baseLockDirty bool, target string, candidate []byte, force bool) (string, error) {
	info, err := s.lstat(target)
	if os.IsNotExist(err) {
		if g, ok := s.git.(releaseTrackedFile); ok && baseHead != "" {
			if _, trackedErr := g.TrackedFile(ctx, base, baseHead, ReleaseLockFilename); trackedErr == nil && baseLockDirty && !force {
				return "", NewError(ErrorConflict, errors.New("release lock is locally deleted; use --force to replace it"))
			}
		}
		return "created", nil
	}
	if err != nil {
		return "", NewError(ErrorConflict, fmt.Errorf("inspect release lock target: %w", err))
	}
	if !info.Mode().IsRegular() {
		return "", NewError(ErrorConflict, errors.New("release lock target must be a regular file"))
	}
	current, err := s.readFile(target)
	if err != nil {
		return "", NewError(ErrorConflict, errors.New("read release lock target"))
	}
	if baseLockDirty && !force {
		return "", NewError(ErrorConflict, errors.New("release lock is locally modified or staged for deletion; use --force to replace it"))
	}
	if bytes.Equal(current, candidate) && !baseLockDirty {
		return "unchanged", nil
	}
	tracked := false
	if g, ok := s.git.(releaseTrackedFile); ok {
		trackedBytes, trackedErr := g.TrackedFile(ctx, base, baseHead, ReleaseLockFilename)
		if trackedErr == nil {
			tracked = true
			if !bytes.Equal(current, trackedBytes) {
				tracked = false
			}
		}
	}
	if !tracked && !force {
		return "", NewError(ErrorConflict, errors.New("release lock is untracked or locally modified; use --force to replace it"))
	}
	return "replaced", nil
}

func (s *ReleaseLockService) prepareHooks(ctx context.Context, q ReleaseLockRequest, repositories []ReleaseLockRepositoryResult) (HookPlan, bool, error) {
	configBytes, err := s.readFile(q.Project.ConfigPath)
	if err != nil {
		return HookPlan{}, false, NewError(ErrorValidation, errors.New("local configuration is unavailable"))
	}
	local, err := config.LoadProject(configBytes)
	if err != nil {
		return HookPlan{}, false, NewError(ErrorValidation, err)
	}
	if local.Project.ID != q.Project.ID || local.Project.BaseRepository != q.Project.BaseRepository || len(local.Repositories) != len(q.Project.Repositories) {
		return HookPlan{}, false, NewError(ErrorConflict, errors.New("local hook configuration project does not match workspace"))
	}
	raw := local.Hooks[config.HookEventPostRelease]
	if len(raw) == 0 {
		return HookPlan{}, false, nil
	}
	hooks, err := config.CanonicalHookEvent(config.HookEventPostRelease, raw, q.Project.BaseRepository)
	if err != nil {
		return HookPlan{}, false, NewError(ErrorValidation, err)
	}
	byID := map[string]string{}
	for _, r := range repositories {
		byID[r.ID] = r.Revision
	}
	entries := make([]hookPlanInputEntry, 0, len(hooks))
	for _, h := range hooks {
		target, err := q.Workspace.ResolveRepository(h.Repository)
		if err != nil {
			return HookPlan{}, false, NewError(ErrorValidation, err)
		}
		head := byID[h.Repository]
		if h.Repository == q.Project.BaseRepository {
			head, err = s.git.Head(ctx, target)
			if err != nil {
				return HookPlan{}, false, NewError(ErrorGit, err)
			}
		}
		source := ""
		for _, r := range q.Project.Repositories {
			if r.ID == h.Repository {
				source = r.SourcePath
			}
		}
		if source == "" {
			return HookPlan{}, false, NewError(ErrorValidation, errors.New("hook repository missing"))
		}
		entries = append(entries, hookPlanInputEntry{ID: h.ID, Repository: h.Repository, SourceRepository: source, TargetRepository: target, Head: head, ConfiguredExecutable: h.Command[0], Arguments: append([]string{}, h.Command[1:]...), Timeout: h.Timeout, Availability: "deferred"})
	}
	input := hookPlanInput{Operation: "release-lock", Source: "local", Event: config.HookEventPostRelease, Policy: "automatic", ProjectID: q.Project.ID, ProjectName: q.Project.Name, BaseRepository: q.Project.BaseRepository, WorkspaceID: q.Workspace.ID, WorkspaceName: q.Workspace.Name, SourceLogicalRoot: q.Project.LogicalRoot, TargetLogicalRoot: q.Workspace.RootPath, ReleaseName: q.Name, SourceBytes: configBytes, WorkspaceStateBytes: []byte(q.Workspace.ID), Entries: entries}
	provisional, err := newHookPlan(input)
	if err != nil {
		return HookPlan{}, false, NewError(ErrorInternal, err)
	}
	for index := range entries {
		env, envErr := buildHookEnvironment(HookEnvironmentLocal, q.Windows || runtime.GOOS == "windows", q.Environment, provisional, index)
		if envErr != nil {
			return HookPlan{}, false, NewError(ErrorValidation, envErr)
		}
		directory := entries[index].TargetRepository
		if !filepath.IsAbs(entries[index].ConfiguredExecutable) && containsPathSeparator(entries[index].ConfiguredExecutable) {
			directory = entries[index].SourceRepository
		}
		fact, resolveErr := s.process.Resolve(ctx, HookExecutableRequest{Program: entries[index].ConfiguredExecutable, Directory: directory, Environment: env})
		if resolveErr != nil || !fact.Available {
			return HookPlan{}, false, NewError(ErrorValidation, errors.New("post-release hook executable is unavailable"))
		}
		if trusted, trustErr := trustedLocalSourceExecutable(entries[index].SourceRepository, entries[index].ConfiguredExecutable, fact.Resolved); trustErr != nil {
			return HookPlan{}, false, NewError(ErrorValidation, errors.New("post-release hook executable escapes its source repository"))
		} else {
			fact.Resolved = trusted
		}
		entries[index].ResolvedExecutable, entries[index].Availability = fact.Resolved, "available"
	}
	input.Entries = entries
	hp, err := newHookPlan(input)
	if err != nil {
		return HookPlan{}, false, NewError(ErrorInternal, err)
	}
	return hp, true, nil
}

func (s *ReleaseLockService) runHooks(ctx context.Context, q ReleaseLockRequest, hp HookPlan) (string, error) {
	for i, entry := range hp.authority.entries {
		env, err := buildHookEnvironment(HookEnvironmentLocal, q.Windows || runtime.GOOS == "windows", q.Environment, hp, i)
		if err != nil {
			return "", NewError(ErrorValidation, err)
		}
		run, err := s.process.Run(ctx, HookProcessRequest{Program: entry.ResolvedExecutable, Arguments: entry.Arguments, Directory: entry.TargetRepository, Environment: env, Timeout: entry.Timeout, Event: config.HookEventPostRelease, HookID: entry.ID, Sink: io.Discard})
		if err != nil {
			return entry.ID, err
		}
		if run.ExitCode != 0 {
			return entry.ID, nil
		}
	}
	return "", nil
}
