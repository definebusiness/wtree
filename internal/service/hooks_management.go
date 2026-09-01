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

const HookManagementResultVersion = 1

type HookManagementRequest struct {
	Project domain.Project
	DataDir string
}
type HookShareRequest struct {
	HookManagementRequest
	Event string
	Force bool
}
type HookInstallRequest struct {
	HookManagementRequest
	Force   bool
	Missing bool
}

type HookListResult struct {
	Version   int             `json:"version"`
	Operation string          `json:"operation"`
	Status    string          `json:"status"`
	Groups    []HookListGroup `json:"groups"`
}
type HookListGroup struct {
	Source string          `json:"source"`
	Events []HookListEvent `json:"events"`
}
type HookListEvent struct {
	Event      string              `json:"event"`
	Comparison *HookListComparison `json:"comparison,omitempty"`
	Hooks      []HookListEntry     `json:"hooks"`
}
type HookListComparison struct {
	Source string `json:"source"`
	State  string `json:"state"`
}
type HookListEntry struct {
	ID              string   `json:"id"`
	Repository      string   `json:"repository"`
	Command         []string `json:"command"`
	Timeout         string   `json:"timeout"`
	ExecutionPolicy string   `json:"executionPolicy"`
}
type HookMutationResult struct {
	Version     int      `json:"version"`
	Operation   string   `json:"operation"`
	Status      string   `json:"status"`
	Changed     bool     `json:"changed"`
	Added       []string `json:"added"`
	Replaced    []string `json:"replaced"`
	Unchanged   []string `json:"unchanged"`
	Skipped     []string `json:"skipped"`
	Conflicting []string `json:"conflicting"`
}

type HookManagementService struct {
	locker      ProjectLocker
	lockTimeout time.Duration
	writeAtomic func(string, []byte, os.FileMode, fsutil.AtomicStepHook) error
	tracked     interface {
		WorkingFileTracked(context.Context, string, string) (bool, error)
	}
	environment []string
}

func NewHookManagementService() *HookManagementService {
	return &HookManagementService{locker: lock.Manager{}, lockTimeout: time.Second, writeAtomic: fsutil.WriteFileAtomicModeWithHook, tracked: gitadapter.NewAdapter("git"), environment: os.Environ()}
}

func (s *HookManagementService) List(ctx context.Context, request HookManagementRequest) (HookListResult, error) {
	if err := ctx.Err(); err != nil {
		return HookListResult{}, err
	}
	sources, err := captureHookManagementSources(ctx, request)
	if err != nil {
		return HookListResult{}, err
	}
	return buildHookList(sources.local, sources.manifest)
}

func (s *HookManagementService) Share(ctx context.Context, request HookShareRequest) (HookMutationResult, error) {
	result := newHookMutationResult("hooks-share")
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if request.Event != config.HookEventPostCreate {
		return result, NewError(ErrorValidation, fmt.Errorf("hooks share supports only event %q", config.HookEventPostCreate))
	}
	sources, err := captureHookManagementSources(ctx, request.HookManagementRequest)
	if err != nil {
		return result, err
	}
	local, manifest := sources.local, sources.manifest
	localHooks, exists := local.Hooks[request.Event]
	if !exists {
		return result, NewError(ErrorValidation, fmt.Errorf("local hook event %q is unavailable", request.Event))
	}
	if err := s.validateSharePortability(ctx, request.Project, local, manifest, request.Event, localHooks); err != nil {
		return result, err
	}
	if shared, exists := manifest.SharedHooks[request.Event]; exists {
		equal, compareErr := config.HookEventsEqual(request.Event, localHooks, shared, local.Project.BaseRepository)
		if compareErr != nil {
			return result, NewError(ErrorValidation, compareErr)
		}
		if equal {
			result.Unchanged = []string{request.Event}
			return result, nil
		}
		if !request.Force {
			return result, NewError(ErrorConflict, fmt.Errorf("hooks share event %q conflicts with the shared definition; rerun with --force", request.Event))
		}
		result.Replaced = []string{request.Event}
	} else {
		result.Added = []string{request.Event}
	}
	manifest = cloneHookManifest(manifest)
	if manifest.SharedHooks == nil {
		manifest.SharedHooks = make(config.HookEvents)
	}
	manifest.SharedHooks[request.Event] = cloneHookSlice(localHooks)
	if manifest.Version == config.PortableManifestVersion {
		manifest.Version = config.PortableManifestVersion3
	}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		return result, NewError(ErrorValidation, errors.New("shared hook definition is invalid"))
	}
	if err := s.replaceHookManagementFile(ctx, request.HookManagementRequest, sources.manifestPath, sources, data, func() error {
		return s.validateSharePortability(ctx, request.Project, local, manifest, request.Event, localHooks)
	}, func(generation []byte) error {
		_, err := config.LoadPortableManifest(generation)
		return err
	}); err != nil {
		return result, err
	}
	result.Changed = true
	return result, nil
}

func (s *HookManagementService) validateSharePortability(ctx context.Context, project domain.Project, local config.ProjectConfig, manifest config.PortableManifest, event string, hooks []config.Hook) error {
	probe := cloneHookManifest(manifest)
	probe.Version = config.PortableManifestVersion3
	probe.SharedHooks = config.HookEvents{event: cloneHookSlice(hooks)}
	if _, err := config.MarshalPortableManifest(probe); err != nil {
		return NewError(ErrorValidation, errors.New("portable hook definition is invalid"))
	}
	if s == nil || s.tracked == nil {
		return NewError(ErrorInternal, errors.New("hook tracking boundary is not configured"))
	}
	repositories := make(map[string]domain.Repository, len(project.Repositories))
	for _, repository := range project.Repositories {
		repositories[repository.ID] = repository
	}
	for _, hook := range hooks {
		repositoryID := hook.Repository
		if repositoryID == "" {
			repositoryID = local.Project.BaseRepository
		}
		repository, exists := repositories[repositoryID]
		if !exists {
			return NewError(ErrorValidation, fmt.Errorf("hook repository %q is unavailable", repositoryID))
		}
		executable := hook.Command[0]
		if !strings.Contains(executable, "/") && !strings.Contains(executable, `\`) {
			continue
		}
		relative, candidate, info, resolveErr := hookExecutablePath(repository.SourcePath, executable, runtime.GOOS == "windows", s.environment)
		if resolveErr != nil {
			return NewError(ErrorValidation, errors.New("portable hook executable is unavailable"))
		}
		root, rootErr := filepath.EvalSymlinks(repository.SourcePath)
		target, targetErr := filepath.EvalSymlinks(candidate)
		if rootErr != nil || targetErr != nil {
			return NewError(ErrorValidation, errors.New("portable hook executable is unavailable"))
		}
		rel, relErr := filepath.Rel(root, target)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return NewError(ErrorValidation, errors.New("portable hook executable escapes its repository"))
		}
		if !hookResolvedExecutableAvailable(info, runtime.GOOS == "windows") {
			return NewError(ErrorValidation, errors.New("portable hook executable is unavailable"))
		}
		tracked, trackErr := s.tracked.WorkingFileTracked(ctx, repository.SourcePath, filepath.ToSlash(relative))
		if trackErr != nil {
			return NewError(ErrorGit, errors.New("inspect portable hook tracking"))
		}
		if !tracked {
			return NewError(ErrorValidation, errors.New("portable hook executable is not tracked"))
		}
	}
	return nil
}

func (s *HookManagementService) Install(ctx context.Context, request HookInstallRequest) (HookMutationResult, error) {
	result := newHookMutationResult("hooks-install")
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if request.Force && request.Missing {
		return result, NewError(ErrorValidation, errors.New("hooks install --force and --missing are mutually exclusive"))
	}
	sources, err := captureHookManagementSources(ctx, request.HookManagementRequest)
	if err != nil {
		return result, err
	}
	local, manifest := sources.local, sources.manifest
	if len(manifest.SharedHooks) == 0 {
		return result, nil
	}
	names := hookEventNames(manifest.SharedHooks)
	for _, event := range names {
		shared := manifest.SharedHooks[event]
		current, exists := local.Hooks[event]
		if !exists {
			result.Added = append(result.Added, event)
			continue
		}
		equal, compareErr := config.HookEventsEqual(event, shared, current, local.Project.BaseRepository)
		if compareErr != nil {
			return result, NewError(ErrorValidation, compareErr)
		}
		if equal {
			result.Unchanged = append(result.Unchanged, event)
			continue
		}
		if request.Force {
			result.Replaced = append(result.Replaced, event)
			continue
		}
		result.Conflicting = append(result.Conflicting, event)
		if request.Missing {
			result.Skipped = append(result.Skipped, event)
		}
	}
	if len(result.Conflicting) != 0 && !request.Missing {
		return newHookMutationResult("hooks-install"), NewError(ErrorConflict, fmt.Errorf("hooks install has conflicting events: %s", strings.Join(result.Conflicting, ",")))
	}
	if len(result.Added) == 0 && len(result.Replaced) == 0 {
		return result, nil
	}
	local = cloneHookLocal(local)
	if local.Hooks == nil {
		local.Hooks = make(config.HookEvents)
	}
	for _, event := range append(append([]string(nil), result.Added...), result.Replaced...) {
		local.Hooks[event] = cloneHookSlice(manifest.SharedHooks[event])
	}
	if local.Version == config.ProjectConfigVersion {
		local.Version = config.ProjectConfigVersion3
	}
	data, err := config.MarshalProject(local)
	if err != nil {
		return result, NewError(ErrorValidation, errors.New("installed hook definition is invalid"))
	}
	if err := s.replaceHookManagementFile(ctx, request.HookManagementRequest, request.Project.ConfigPath, sources, data, nil, func(generation []byte) error {
		_, err := config.LoadProject(generation)
		return err
	}); err != nil {
		return result, err
	}
	result.Changed = true
	return result, nil
}

func newHookMutationResult(operation string) HookMutationResult {
	return HookMutationResult{Version: HookManagementResultVersion, Operation: operation, Status: "completed", Added: []string{}, Replaced: []string{}, Unchanged: []string{}, Skipped: []string{}, Conflicting: []string{}}
}

var errHookDefinitionGenerationChanged = errors.New("hook definition generation changed")

func (s *HookManagementService) replaceHookManagementFile(ctx context.Context, request HookManagementRequest, path string, sources hookManagementSources, data []byte, revalidate func() error, validate func([]byte) error) error {
	if s == nil || s.locker == nil {
		return NewError(ErrorInternal, errors.New("hook management service is not configured"))
	}
	if request.DataDir == "" {
		return NewError(ErrorValidation, errors.New("hook management requires project mutation authority"))
	}
	handle, err := acquireProjectMutationAuthority(ctx, s.locker, request.DataDir, request.Project.ID, s.lockTimeout)
	if err != nil {
		return err
	}
	defer handle.Unlock()
	if err := sources.verify(); err != nil {
		return NewError(ErrorConflict, errHookDefinitionGenerationChanged)
	}
	mode, err := sources.targetMode(path)
	if err != nil {
		return NewError(ErrorConflict, errHookDefinitionGenerationChanged)
	}
	if revalidate != nil {
		if err := revalidate(); err != nil {
			return err
		}
	}
	if s.writeAtomic == nil {
		return NewError(ErrorInternal, errors.New("hook management atomic writer is not configured"))
	}
	if err := s.writeAtomic(path, data, mode, func(step string) error {
		if step != "before-rename" {
			return nil
		}
		if err := sources.verify(); err != nil {
			return errHookDefinitionGenerationChanged
		}
		if revalidate != nil {
			if err := revalidate(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if errors.Is(err, errHookDefinitionGenerationChanged) {
			return NewError(ErrorConflict, errHookDefinitionGenerationChanged)
		}
		var serviceError *Error
		if errors.As(err, &serviceError) {
			return serviceError
		}
		if fsutil.ReplacementCompleted(err) {
			installed, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(installed, data) || validate(installed) != nil {
				return NewError(ErrorInternal, errors.New("validate installed hook definition generation"))
			}
			return NewError(ErrorInternal, errors.New("hook definition installed but durability confirmation failed"))
		}
		return NewError(ErrorInternal, fmt.Errorf("replace hook definition: %w", err))
	}
	installed, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(installed, data) {
		return NewError(ErrorInternal, errors.New("validate installed hook definition generation"))
	}
	if err := validate(installed); err != nil {
		return NewError(ErrorInternal, errors.New("validate installed hook definition generation"))
	}
	return nil
}

func hookExecutableAvailable(path string, info os.FileInfo) bool {
	return hookExecutableAvailableWithEnvironment(path, info, runtime.GOOS == "windows", nil)
}

func hookExecutableAvailableForPlatform(path string, info os.FileInfo, windows bool) bool {
	return hookExecutableAvailableWithEnvironment(path, info, windows, nil)
}

func hookExecutableAvailableWithEnvironment(path string, info os.FileInfo, windows bool, environment []string) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	if !windows {
		return info.Mode().Perm()&0o111 != 0
	}
	return hookWindowsExtensionAllowed(filepath.Ext(path), hookWindowsPATHEXT(environment))
}

func hookResolvedExecutableAvailable(info os.FileInfo, windows bool) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	return windows || info.Mode().Perm()&0o111 != 0
}

// hookExecutablePath mirrors direct Windows program resolution without ever
// starting a process. Commands without a path separator are intentionally not
// resolved here: their lookup is a future execution concern, not a share fact.
func hookExecutablePath(repositoryRoot, executable string, windows bool, environment []string) (string, string, os.FileInfo, error) {
	relative := strings.ReplaceAll(executable, `\`, "/")
	if !windows {
		path := filepath.Join(repositoryRoot, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil {
			return "", "", nil, err
		}
		return relative, path, info, nil
	}

	// Match exec.findExecutable: an explicit regular file wins regardless of
	// its suffix, but an absent (or directory) explicit path still receives
	// every effective PATHEXT suffix in order.
	candidates := make([]string, 0, len(hookWindowsPATHEXT(environment))+1)
	if filepath.Ext(relative) != "" {
		candidates = append(candidates, relative)
	}
	for _, extension := range hookWindowsPATHEXT(environment) {
		candidates = append(candidates, relative+extension)
	}
	for _, candidate := range candidates {
		path, info, err := hookCaseInsensitiveFile(filepath.Join(repositoryRoot, filepath.FromSlash(candidate)))
		if err == nil && info.Mode().IsRegular() {
			relativePath, relErr := filepath.Rel(repositoryRoot, path)
			if relErr != nil {
				return "", "", nil, relErr
			}
			return filepath.ToSlash(relativePath), path, info, nil
		}
	}
	return "", "", nil, os.ErrNotExist
}

func hookCaseInsensitiveFile(path string) (string, os.FileInfo, error) {
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		return "", nil, readErr
	}
	name := filepath.Base(path)
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			candidate := filepath.Join(filepath.Dir(path), entry.Name())
			info, statErr := os.Stat(candidate)
			if statErr == nil {
				return candidate, info, nil
			}
			return "", nil, statErr
		}
	}
	return "", nil, os.ErrNotExist
}

func hookWindowsPATHEXT(environment []string) []string {
	raw := ""
	if environment == nil {
		raw = os.Getenv("PATHEXT")
	} else {
		for _, entry := range environment {
			name, value, ok := strings.Cut(entry, "=")
			if ok && strings.EqualFold(name, "PATHEXT") {
				raw = value
				break
			}
		}
	}
	if raw == "" {
		raw = ".COM;.EXE;.BAT;.CMD"
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, 4)
	for _, extension := range strings.Split(raw, ";") {
		extension = strings.TrimSpace(extension)
		if extension == "" {
			continue
		}
		if !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		key := strings.ToLower(extension)
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			result = append(result, extension)
		}
	}
	return result
}

func hookWindowsExtensionAllowed(extension string, pathext []string) bool {
	for _, candidate := range pathext {
		if strings.EqualFold(extension, candidate) {
			return true
		}
	}
	return false
}

type hookManagementSources struct {
	local                     config.ProjectConfig
	manifest                  config.PortableManifest
	localBytes, manifestBytes []byte
	localPath, manifestPath   string
	localGeneration           hookFileGeneration
	manifestGeneration        hookFileGeneration
}

type hookFileGeneration struct {
	path string
	data []byte
	info os.FileInfo
}

func captureHookFileGeneration(path string) (hookFileGeneration, error) {
	link, err := os.Lstat(path)
	if err != nil {
		return hookFileGeneration{}, err
	}
	if !link.Mode().IsRegular() {
		return hookFileGeneration{}, errors.New("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return hookFileGeneration{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		return hookFileGeneration{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return hookFileGeneration{}, err
	}
	return hookFileGeneration{path: path, data: data, info: info}, nil
}

func (generation hookFileGeneration) verify() error {
	link, err := os.Lstat(generation.path)
	if err != nil || !link.Mode().IsRegular() {
		return errHookDefinitionGenerationChanged
	}
	file, err := os.Open(generation.path)
	if err != nil {
		return errHookDefinitionGenerationChanged
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(generation.info, info) || generation.info.Mode() != info.Mode() {
		return errHookDefinitionGenerationChanged
	}
	data, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(data, generation.data) {
		return errHookDefinitionGenerationChanged
	}
	return nil
}

func (sources hookManagementSources) verify() error {
	for _, generation := range []hookFileGeneration{sources.manifestGeneration, sources.localGeneration} {
		if generation.path == "" || generation.verify() != nil {
			return errHookDefinitionGenerationChanged
		}
	}
	return nil
}

func (sources hookManagementSources) targetMode(path string) (os.FileMode, error) {
	for _, generation := range []hookFileGeneration{sources.manifestGeneration, sources.localGeneration} {
		if filepath.Clean(generation.path) == filepath.Clean(path) {
			if err := generation.verify(); err != nil {
				return 0, err
			}
			return generation.info.Mode().Perm(), nil
		}
	}
	return 0, errHookDefinitionGenerationChanged
}

func captureHookManagementSources(_ context.Context, request HookManagementRequest) (hookManagementSources, error) {
	if err := request.Project.Validate(); err != nil {
		return hookManagementSources{}, NewError(ErrorValidation, fmt.Errorf("hook management project: %w", err))
	}
	localGeneration, err := captureHookFileGeneration(request.Project.ConfigPath)
	if err != nil {
		return hookManagementSources{}, hookManagementReadError("local configuration", err)
	}
	localBytes := localGeneration.data
	local, err := config.LoadProject(localBytes)
	if err != nil {
		return hookManagementSources{}, NewError(ErrorValidation, fmt.Errorf("read local hook configuration: %w", err))
	}
	if local.Project.ID != request.Project.ID || local.Project.BaseRepository != request.Project.BaseRepository {
		return hookManagementSources{}, NewError(ErrorConflict, errors.New("project configuration no longer matches the resolved project"))
	}
	manifestPath, err := hookManagementWorkingManifestPath(request.Project, local)
	if err != nil {
		return hookManagementSources{}, err
	}
	if err := hookManagementTopologyMatches(request.Project, local); err != nil {
		return hookManagementSources{}, err
	}
	manifestGeneration, err := captureHookFileGeneration(manifestPath)
	if err != nil {
		return hookManagementSources{}, hookManagementReadError("portable manifest", err)
	}
	manifestBytes := manifestGeneration.data
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		return hookManagementSources{}, NewError(ErrorValidation, fmt.Errorf("read portable hook manifest: %w", err))
	}
	if manifest.Project.ID != local.Project.ID || manifest.Project.BaseRepository != local.Project.BaseRepository {
		return hookManagementSources{}, NewError(ErrorConflict, errors.New("portable manifest no longer matches the local project"))
	}
	return hookManagementSources{
		local: local, manifest: manifest,
		localBytes: append([]byte(nil), localBytes...), manifestBytes: append([]byte(nil), manifestBytes...),
		localPath: request.Project.ConfigPath, manifestPath: manifestPath,
		localGeneration: localGeneration, manifestGeneration: manifestGeneration,
	}, nil
}

func hookManagementWorkingManifestPath(project domain.Project, local config.ProjectConfig) (string, error) {
	var base domain.Repository
	found := false
	for _, repository := range project.Repositories {
		if repository.ID == project.BaseRepository {
			base, found = repository, true
			break
		}
	}
	basePath, baseErr := filepath.EvalSymlinks(base.SourcePath)
	configPath, configErr := filepath.EvalSymlinks(project.ConfigPath)
	if !found || baseErr != nil || configErr != nil || filepath.Clean(requestedConfigPath(basePath)) != filepath.Clean(configPath) {
		return "", NewError(ErrorConflict, errors.New("resolved project configuration placement changed"))
	}
	if local.Manifest.Path == "" || filepath.IsAbs(local.Manifest.Path) || filepath.Clean(local.Manifest.Path) != local.Manifest.Path {
		return "", NewError(ErrorValidation, errors.New("working manifest path is invalid"))
	}
	path := filepath.Join(basePath, filepath.FromSlash(local.Manifest.Path))
	root, rootErr := filepath.EvalSymlinks(basePath)
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
	if rootErr != nil || parentErr != nil {
		return "", NewError(ErrorValidation, errors.New("working manifest path is unavailable"))
	}
	rel, relErr := filepath.Rel(root, parent)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", NewError(ErrorValidation, errors.New("working manifest path escapes base repository"))
	}
	return path, nil
}

func requestedConfigPath(base string) string { return filepath.Join(base, ".wtree.yml") }

func hookManagementTopologyMatches(project domain.Project, local config.ProjectConfig) error {
	configPath, configErr := filepath.EvalSymlinks(project.ConfigPath)
	projectRoot, projectRootErr := filepath.EvalSymlinks(project.LogicalRoot)
	if configErr != nil || projectRootErr != nil {
		return NewError(ErrorConflict, errors.New("project repository topology changed"))
	}
	configDirectory := filepath.Dir(configPath)
	logicalRoot, err := resolveLogicalRoot(configDirectory, local.LogicalRoot)
	if err != nil || filepath.Clean(logicalRoot) != filepath.Clean(projectRoot) {
		return NewError(ErrorConflict, errors.New("project repository topology changed"))
	}
	if len(project.Repositories) != len(local.Repositories) {
		return NewError(ErrorConflict, errors.New("project repository topology changed"))
	}
	for _, repository := range project.Repositories {
		configured, ok := local.Repositories[repository.ID]
		source, sourceErr := sourcePath(logicalRoot, configured.Source)
		repositorySource, repositoryErr := filepath.EvalSymlinks(repository.SourcePath)
		if !ok || sourceErr != nil || repositoryErr != nil || configured.Parent != repository.ParentID || configured.DefaultMount != repository.DefaultMount || configured.DefaultBranch != repository.DefaultBranch || filepath.Clean(source) != filepath.Clean(repositorySource) {
			return NewError(ErrorConflict, errors.New("project repository topology changed"))
		}
	}
	return nil
}

func hookEventNames(events config.HookEvents) []string {
	names := make([]string, 0, len(events))
	for event := range events {
		names = append(names, event)
	}
	sort.Strings(names)
	return names
}
func cloneHookSlice(hooks []config.Hook) []config.Hook {
	result := make([]config.Hook, len(hooks))
	for index, hook := range hooks {
		result[index] = hook
		result[index].Command = append([]string(nil), hook.Command...)
	}
	return result
}
func cloneHookEvents(events config.HookEvents) config.HookEvents {
	if events == nil {
		return nil
	}
	result := make(config.HookEvents, len(events))
	for event, hooks := range events {
		result[event] = cloneHookSlice(hooks)
	}
	return result
}
func cloneHookManifest(value config.PortableManifest) config.PortableManifest {
	value.Hooks = cloneHookEvents(value.Hooks)
	value.SharedHooks = cloneHookEvents(value.SharedHooks)
	return value
}
func cloneHookLocal(value config.ProjectConfig) config.ProjectConfig {
	value.Hooks = cloneHookEvents(value.Hooks)
	return value
}

func hookManagementReadError(kind string, err error) error {
	if os.IsNotExist(err) {
		return NewError(ErrorValidation, fmt.Errorf("%s is unavailable", kind))
	}
	return NewError(ErrorInternal, fmt.Errorf("read %s: %w", kind, err))
}

func buildHookList(local config.ProjectConfig, manifest config.PortableManifest) (HookListResult, error) {
	groups := make([]HookListGroup, 0, 3)
	portable, err := hookListEvents(manifest.Hooks, manifest.Project.BaseRepository, "authorized-post-clone", "", nil)
	if err != nil {
		return HookListResult{}, err
	}
	groups = append(groups, HookListGroup{Source: "portable", Events: portable})
	shared, err := hookListEvents(manifest.SharedHooks, manifest.Project.BaseRepository, "inert-until-installed", "local", local.Hooks)
	if err != nil {
		return HookListResult{}, err
	}
	groups = append(groups, HookListGroup{Source: "shared", Events: shared})
	localEvents, err := hookListEvents(local.Hooks, local.Project.BaseRepository, "automatic-post-create-unless-bypassed", "shared", manifest.SharedHooks)
	if err != nil {
		return HookListResult{}, err
	}
	groups = append(groups, HookListGroup{Source: "local", Events: localEvents})
	return HookListResult{Version: HookManagementResultVersion, Operation: "hooks-list", Status: "completed", Groups: groups}, nil
}

func hookListEvents(events config.HookEvents, base, policy, otherSource string, other config.HookEvents) ([]HookListEvent, error) {
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]HookListEvent, 0, len(names))
	for _, name := range names {
		canonical, err := config.CanonicalHookEvent(name, events[name], base)
		if err != nil {
			return nil, NewError(ErrorValidation, err)
		}
		entries := make([]HookListEntry, 0, len(canonical))
		for _, hook := range canonical {
			entries = append(entries, HookListEntry{ID: hook.ID, Repository: hook.Repository, Command: append([]string(nil), hook.Command...), Timeout: hook.Timeout.String(), ExecutionPolicy: policy})
		}
		event := HookListEvent{Event: name, Hooks: entries}
		if otherSource != "" {
			comparison := HookListComparison{Source: otherSource, State: "missing"}
			if otherHooks, exists := other[name]; exists {
				equal, compareErr := config.HookEventsEqual(name, events[name], otherHooks, base)
				if compareErr != nil {
					return nil, NewError(ErrorValidation, compareErr)
				}
				if equal {
					comparison.State = "identical"
				} else {
					comparison.State = "conflicting"
				}
			}
			event.Comparison = &comparison
		}
		result = append(result, event)
	}
	return result, nil
}
