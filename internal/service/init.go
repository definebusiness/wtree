// Package service contains application use cases without CLI formatting.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
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
)

var ErrAlreadyInitialized = errors.New("project is already initialized")

// InitRequest is the complete authoring input. CloneURLOverrides deliberately
// remains a slice until preflight so duplicate command-line values are not
// silently lost by a map conversion.
type InitRequest struct {
	Path, DataDir, WorktreeRoot string
	DryRun                      bool
	Ignores                     []string
	ManifestSource              string
	CloneURLOverrides           []string
	AddIgnore                   bool
}

type InitResult struct {
	ProjectID        string                  `json:"projectId"`
	ConfigPath       string                  `json:"configPath"`
	ManifestPath     string                  `json:"manifestPath"`
	ManifestSource   string                  `json:"manifestSource"`
	Repositories     []discovery.Repository  `json:"repositories"`
	DryRun           bool                    `json:"dryRun"`
	LocalConfig      config.ProjectConfig    `json:"localConfig"`
	PortableManifest config.PortableManifest `json:"portableManifest"`
	IgnoreUpdates    []IgnoreUpdate          `json:"ignoreUpdates"`
}

// IgnoreUpdate is rendered by the CLI without exposing filesystem snapshots.
type IgnoreUpdate struct {
	RepositoryID string   `json:"repositoryId"`
	Path         string   `json:"path"`
	AddedRules   []string `json:"addedRules"`
}

type Initializer struct {
	git                           gitadapter.Git
	writeRegistry                 func(string, store.Registry) error
	writeWorkspace                func(string, store.WorkspaceState) error
	writeFile                     func(string, []byte, os.FileMode) error
	rename                        func(string, string) error
	remove                        func(string) error
	beforeLockedRegistryPreflight func()
	beforePublish                 func()
	beforeOwnedRemove             func(string)
	captureOwnedFile              func(string, string) error
	useStoreCAS                   bool
	useFileCAS                    bool
}

func NewInitializer() *Initializer { return newInitializer() }

func newInitializer() *Initializer {
	return &Initializer{git: gitadapter.NewAdapter("git"), writeRegistry: store.WriteRegistry, writeWorkspace: store.WriteWorkspace, rename: os.Rename, remove: os.Remove, captureOwnedFile: os.Rename, useStoreCAS: true, useFileCAS: true}
}

// Compatibility constructors remain narrow test seams for the pre-existing
// registry/state publication tests.
func NewInitializerWithRegistryWriter(writer func(string, store.Registry) error) *Initializer {
	i := newInitializer()
	i.writeRegistry = writer
	i.useStoreCAS = false
	return i
}
func NewInitializerWithWriters(registryWriter func(string, store.Registry) error, workspaceWriter func(string, store.WorkspaceState) error) *Initializer {
	i := newInitializer()
	i.writeRegistry, i.writeWorkspace = registryWriter, workspaceWriter
	i.useStoreCAS = false
	return i
}

// NewInitializerWithFileWriter is a narrow publication seam for hermetic
// failure tests. Production callers use NewInitializer.
func NewInitializerWithFileWriter(writer func(string, []byte, os.FileMode) error) *Initializer {
	i := newInitializer()
	i.writeFile = writer
	return i
}

// NewInitializerWithPublicationWriters combines the narrow durable-boundary
// seams used by hermetic init transaction tests.
func NewInitializerWithPublicationWriters(registryWriter func(string, store.Registry) error, workspaceWriter func(string, store.WorkspaceState) error, fileWriter func(string, []byte, os.FileMode) error) *Initializer {
	i := NewInitializerWithWriters(registryWriter, workspaceWriter)
	i.writeFile = fileWriter
	return i
}
func NewInitializerWithPublishers(registryWriter func(string, store.Registry) error, workspaceWriter func(string, store.WorkspaceState) error, rename func(string, string) error) *Initializer {
	i := NewInitializerWithWriters(registryWriter, workspaceWriter)
	i.rename = rename
	i.useFileCAS = false
	return i
}
func NewInitializerWithPublishersAndRemover(registryWriter func(string, store.Registry) error, workspaceWriter func(string, store.WorkspaceState) error, rename func(string, string) error, remove func(string) error) *Initializer {
	i := NewInitializerWithPublishers(registryWriter, workspaceWriter, rename)
	i.remove = remove
	return i
}

// Init discovers and completely preflights an immutable local/portable
// authoring plan before it obtains publication locks or writes any target.
func (i *Initializer) Init(ctx context.Context, request InitRequest) (InitResult, error) {
	plan, err := i.plan(ctx, request)
	if err != nil {
		return InitResult{}, err
	}
	if request.DryRun {
		return plan.result(true), nil
	}

	registryLock, err := (lock.Manager{}).RegistryLock(ctx, request.DataDir, time.Second)
	if err != nil {
		return InitResult{}, err
	}
	defer registryLock.Unlock()
	if i.beforeLockedRegistryPreflight != nil {
		i.beforeLockedRegistryPreflight()
	}
	// The immutable plan remains valid only for the registry generation that
	// supplied its conflict decision. Recheck it while holding the registry
	// lock, before creating a project lock directory for a rejected init.
	lockedRegistry, _, err := loadRegistry(plan.registryPath)
	if err != nil {
		return InitResult{}, err
	}
	if err := rejectRegistrationConflicts(plan.configPath, plan.identityMap, lockedRegistry); err != nil {
		return InitResult{}, err
	}
	if _, exists := lockedRegistry.Projects[plan.id]; exists {
		return InitResult{}, NewError(ErrorConflict, fmt.Errorf("deterministic project ID %q is already registered", plan.id))
	}
	if err := revalidateSnapshot(snapshotForPath(plan.targetSnapshots, plan.registryPath)); err != nil {
		return InitResult{}, NewError(ErrorConflict, fmt.Errorf("project registry changed after preflight: %w", err))
	}
	projectLock, err := (lock.Manager{}).ProjectLock(ctx, request.DataDir, plan.id, time.Second)
	if err != nil {
		return InitResult{}, err
	}
	defer projectLock.Unlock()
	if i.beforePublish != nil {
		i.beforePublish()
	}
	if err := plan.publish(ctx, i); err != nil {
		return InitResult{}, err
	}
	return plan.result(false), nil
}

type initPlan struct {
	id, root, configPath, manifestPath, registryPath, workspacePath string
	configuration                                                   config.ProjectConfig
	manifest                                                        config.PortableManifest
	repositories                                                    []discovery.Repository
	identityMap                                                     map[string]string
	state                                                           store.WorkspaceState
	registry                                                        store.Registry
	registryHad                                                     bool
	ignoreWrites                                                    []fileWrite
	targetSnapshots                                                 []fileSnapshot
}
type fileWrite struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
	update IgnoreUpdate
}
type fileSnapshot struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
	info   os.FileInfo
}

func (i *Initializer) plan(ctx context.Context, request InitRequest) (initPlan, error) {
	root, err := filepath.Abs(request.Path)
	if err != nil {
		return initPlan{}, err
	}
	repositories, err := discovery.Discover(root, request.Ignores)
	if err != nil {
		return initPlan{}, err
	}
	root = repositories[0].Path
	configPath, manifestPath := filepath.Join(root, ".wtree.yml"), filepath.Join(root, "project.wtree.yml")
	if _, err := os.Lstat(configPath); err == nil {
		return initPlan{}, ErrAlreadyInitialized
	} else if !os.IsNotExist(err) {
		return initPlan{}, err
	}
	overrides, err := ParseCloneURLOverrides(request.CloneURLOverrides)
	if err != nil {
		return initPlan{}, NewError(ErrorValidation, err)
	}
	source, err := normalizeInitManifestSource(request.ManifestSource, manifestPath)
	if err != nil {
		return initPlan{}, NewError(ErrorValidation, err)
	}

	p := initPlan{root: root, configPath: configPath, manifestPath: manifestPath, registryPath: filepath.Join(request.DataDir, "registry.json"), repositories: repositories, identityMap: map[string]string{}}
	checkouts := map[string]store.CheckoutState{}
	byID := map[string]discovery.Repository{}
	for _, repository := range repositories {
		byID[repository.ID] = repository
	}
	for _, repository := range repositories {
		common, err := i.git.CommonGitDir(ctx, repository.Path)
		if err != nil {
			return initPlan{}, fmt.Errorf("inspect repository %q identity: %w", repository.ID, err)
		}
		if prior, exists := p.identityMap[common]; exists {
			return initPlan{}, fmt.Errorf("duplicate repository identity %q for repositories %q and %q", common, prior, repository.ID)
		}
		p.identityMap[common] = repository.ID
	}
	p.configuration = config.ProjectConfig{Version: config.Version, Project: config.Project{Name: filepath.Base(root)}, Repositories: map[string]config.Repository{}, Worktrees: config.Worktrees{Root: request.WorktreeRoot}, Discovery: config.Discovery{Ignore: request.Ignores}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: source}}
	p.manifest = config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "identity", Name: filepath.Base(root)}, Repositories: map[string]config.PortableRepository{}}
	for id := range overrides {
		if _, ok := byID[id]; !ok {
			return initPlan{}, NewError(ErrorValidation, fmt.Errorf("clone URL override references unknown repository %q", id))
		}
	}
	for _, repository := range repositories {
		facts, err := i.git.PublishedRepositoryFacts(ctx, repository.Path)
		if err != nil {
			return initPlan{}, NewError(ErrorGit, fmt.Errorf("verify repository %q published upstream: %w", repository.ID, err))
		}
		upstream, head, initial := facts.Upstream, facts.Head, facts.Roots
		relative, _ := filepath.Rel(root, repository.Path)
		if repository.ID == "root" {
			relative = "."
		}
		cloneURL := upstream.FetchURL
		if override, ok := overrides[repository.ID]; ok {
			cloneURL = override
		}
		if err := config.ValidateCloneURL(cloneURL); err != nil {
			return initPlan{}, NewError(ErrorValidation, fmt.Errorf("repository %q clone URL: %w", repository.ID, err))
		}
		p.configuration.Repositories[repository.ID] = config.Repository{Source: filepath.ToSlash(relative), Parent: repository.ParentID, DefaultMount: repository.Mount, DefaultBranch: upstream.LocalBranch}
		p.manifest.Repositories[repository.ID] = config.PortableRepository{Clone: config.CloneSource{Remote: upstream.Remote, URL: cloneURL}, Upstream: config.Upstream{Branch: upstream.LocalBranch, Remote: upstream.Remote, Merge: upstream.Merge}, Identity: config.RepositoryIdentity{InitialCommits: initial}, Parent: repository.ParentID, Mount: filepath.ToSlash(repository.Mount), DefaultBranch: upstream.LocalBranch}
		checkouts[repository.ID] = store.CheckoutState{Branch: upstream.LocalBranch, Mount: repository.Mount, ResolvedPath: repository.Path, Head: head}
	}
	if err := p.manifest.Validate(); err != nil {
		return initPlan{}, NewError(ErrorValidation, fmt.Errorf("validate portable manifest: %w", err))
	}
	registry, had, err := loadRegistry(p.registryPath)
	if err != nil {
		return initPlan{}, err
	}
	p.id, err = availableInitProjectID(deterministicInitProjectID(p.manifest.Repositories), request.DataDir, registry)
	if err != nil {
		return initPlan{}, err
	}
	p.configuration.Project.ID, p.manifest.Project.ID = p.id, p.id
	if err := rejectRegistrationConflicts(configPath, p.identityMap, registry); err != nil {
		return initPlan{}, err
	}
	p.registry, p.registryHad = cloneRegistry(registry), had
	if p.registry.Projects == nil {
		p.registry.Projects = map[string]store.RegistryProject{}
	}
	if _, exists := p.registry.Projects[p.id]; exists {
		return initPlan{}, NewError(ErrorConflict, fmt.Errorf("deterministic project ID %q is already registered", p.id))
	}
	p.registry.Projects[p.id] = store.RegistryProject{Name: p.configuration.Project.Name, ConfigPath: configPath, RepositoryIDs: p.identityMap}
	p.workspacePath = WorkspaceStatePath(request.DataDir, p.id, "default")
	p.state = store.WorkspaceState{Version: store.Version, ID: "default", Name: "default", Path: root, Repositories: checkouts}
	if err := i.preflightIgnores(ctx, &p, request.AddIgnore); err != nil {
		return initPlan{}, err
	}
	// Encode and snapshot all targets while the operation is still read-only.
	if _, err := config.MarshalProject(p.configuration); err != nil {
		return initPlan{}, err
	}
	if _, err := config.MarshalPortableManifest(p.manifest); err != nil {
		return initPlan{}, err
	}
	targets := make([]string, 0, len(p.ignoreWrites)+4)
	for _, write := range p.ignoreWrites {
		targets = append(targets, write.path)
	}
	targets = append(targets, configPath, manifestPath, p.registryPath, p.workspacePath)
	for _, path := range targets {
		if err := preflightTarget(path); err != nil {
			return initPlan{}, err
		}
		snapshot, err := snapshotFile(path)
		if err != nil {
			return initPlan{}, err
		}
		p.targetSnapshots = append(p.targetSnapshots, snapshot)
	}
	return p, nil
}

func (i *Initializer) preflightIgnores(ctx context.Context, p *initPlan, add bool) error {
	changes := map[string]*fileWrite{}
	addRule := func(repositoryID, path, mount string) error {
		w, found := changes[path]
		if !found {
			data, mode, err := readOptionalFile(path)
			if err != nil {
				return err
			}
			_, statErr := os.Lstat(path)
			w = &fileWrite{path: path, data: data, mode: mode, exists: statErr == nil, update: IgnoreUpdate{RepositoryID: repositoryID, Path: path}}
			changes[path] = w
		}
		rule := gitIgnoreRule(mount)
		newline := gitIgnoreNewline(w.data)
		if len(w.data) != 0 && !hasGitIgnoreNewline(w.data) {
			w.data = append(w.data, []byte(newline)...)
		}
		w.data = append(w.data, []byte(rule+newline)...)
		w.update.AddedRules = append(w.update.AddedRules, rule)
		return nil
	}
	// Local configuration stays ignored, while project.wtree.yml is intentionally visible.
	rootIgnore := filepath.Join(p.root, ".gitignore")
	ignored, err := i.git.IsIgnoredWorkingTree(ctx, p.root, ".wtree.yml")
	if err != nil {
		return NewError(ErrorGit, fmt.Errorf("inspect root local configuration ignore: %w", err))
	}
	if !ignored {
		if err := addRule("root", rootIgnore, ".wtree.yml"); err != nil {
			return err
		}
	}
	children := append([]discovery.Repository(nil), p.repositories...)
	sort.SliceStable(children, func(left, right int) bool {
		leftDepth, rightDepth := repositoryDepth(children[left], p.repositories), repositoryDepth(children[right], p.repositories)
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return children[left].ID < children[right].ID
	})
	for _, child := range children {
		if child.ParentID == "" {
			continue
		}
		parent := p.repositories[0]
		for _, candidate := range p.repositories {
			if candidate.ID == child.ParentID {
				parent = candidate
				break
			}
		}
		ignored, err := i.git.IsIgnoredAt(ctx, parent.Path, "HEAD", child.Mount)
		if err != nil {
			return NewError(ErrorGit, fmt.Errorf("verify mount %q for repository %q: %w", child.Mount, child.ID, err))
		}
		if !ignored && !add {
			return NewError(ErrorValidation, fmt.Errorf("mount %q for repository %q is not ignored by parent repository %q; Run `wtree add-ignore`, commit the changed .gitignore files, and retry; or rerun `wtree init --add-ignore` to initialize and add the rules now.", child.Mount, child.ID, parent.ID))
		}
		if !ignored {
			workingIgnored, err := i.git.IsIgnoredWorkingTree(ctx, parent.Path, child.Mount)
			if err != nil {
				return NewError(ErrorGit, fmt.Errorf("inspect working ignore for mount %q in repository %q: %w", child.Mount, parent.ID, err))
			}
			if workingIgnored {
				continue
			}
			if err := addRule(parent.ID, filepath.Join(parent.Path, ".gitignore"), filepath.ToSlash(child.Mount)); err != nil {
				return err
			}
		}
	}
	for _, write := range changes {
		if len(write.update.AddedRules) != 0 {
			p.ignoreWrites = append(p.ignoreWrites, *write)
		}
	}
	sortIgnoreWrites(p.ignoreWrites, p.repositories)
	return nil
}

func sortIgnoreWrites(writes []fileWrite, repositories []discovery.Repository) {
	sort.Slice(writes, func(a, b int) bool {
		leftID, rightID := writes[a].update.RepositoryID, writes[b].update.RepositoryID
		leftDepth, rightDepth := 0, 0
		for _, repository := range repositories {
			if repository.ID == leftID {
				leftDepth = repositoryDepth(repository, repositories)
			}
			if repository.ID == rightID {
				rightDepth = repositoryDepth(repository, repositories)
			}
		}
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		if leftID != rightID {
			return leftID < rightID
		}
		return writes[a].path < writes[b].path
	})
}

func repositoryDepth(repository discovery.Repository, all []discovery.Repository) int {
	depth := 0
	parent := repository.ParentID
	for parent != "" {
		depth++
		for _, candidate := range all {
			if candidate.ID == parent {
				parent = candidate.ParentID
				break
			}
		}
	}
	return depth
}

func gitIgnoreRule(mount string) string {
	var escaped strings.Builder
	escaped.WriteByte('/')
	for _, character := range filepath.ToSlash(mount) {
		switch character {
		case '\\', '#', '!', '*', '?', '[', ']', ' ', '\t':
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(character)
	}
	if mount != ".wtree.yml" {
		escaped.WriteByte('/')
	}
	return escaped.String()
}

func hasGitIgnoreNewline(data []byte) bool {
	return len(data) != 0 && data[len(data)-1] == '\n'
}

func gitIgnoreNewline(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) && !bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\n")) {
		return "\r\n"
	}
	return "\n"
}

func (p initPlan) publish(ctx context.Context, i *Initializer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	local, err := config.MarshalProject(p.configuration)
	if err != nil {
		return err
	}
	portable, err := config.MarshalPortableManifest(p.manifest)
	if err != nil {
		return err
	}
	registry, err := store.RegistryBytes(p.registry)
	if err != nil {
		return err
	}
	workspace, err := store.WorkspaceBytes(p.state)
	if err != nil {
		return err
	}
	if err := revalidateSnapshots(p.targetSnapshots); err != nil {
		return NewError(ErrorConflict, err)
	}
	owned := make([]fileSnapshot, 0, len(p.targetSnapshots))
	preserved := make([]error, 0)
	recordOwned := func(path string, expected []byte) error {
		snapshot, err := publishedSnapshot(path, expected)
		if err != nil {
			return err
		}
		owned = append(owned, snapshot)
		return nil
	}
	recordUnownedChange := func(path string) {
		if err := revalidateSnapshot(snapshotForPath(p.targetSnapshots, path)); err != nil {
			preserved = append(preserved, fmt.Errorf("preserve %s after unverified publication: %w", path, err))
		}
	}
	recordFailedPublication := func(path string, expected []byte) {
		if err := recordOwned(path, expected); err != nil {
			recordUnownedChange(path)
		}
	}
	rollback := func(cause error) error {
		rollbackErr := errors.Join(restoreOwnedSnapshots(i, p.targetSnapshots, owned), errors.Join(preserved...))
		if rollbackErr != nil {
			return NewError(ErrorRollbackIncomplete, fmt.Errorf("publish init: %w; cleanup failed: %v", cause, rollbackErr))
		}
		return NewCleanRollbackError(cause)
	}
	for _, write := range p.ignoreWrites {
		if err := i.writePublishedFile(snapshotForPath(p.targetSnapshots, write.path), write.data, write.mode, write.exists); err != nil {
			recordFailedPublication(write.path, write.data)
			return rollback(fmt.Errorf("publish %s: %w", write.path, err))
		}
		if err := recordOwned(write.path, write.data); err != nil {
			recordUnownedChange(write.path)
			return rollback(NewError(ErrorConflict, fmt.Errorf("publish %s: %w", write.path, err)))
		}
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
	}
	if err := i.writePublishedFile(snapshotForPath(p.targetSnapshots, p.configPath), local, 0o600, false); err != nil {
		recordFailedPublication(p.configPath, local)
		return rollback(fmt.Errorf("publish local config: %w", err))
	}
	if err := recordOwned(p.configPath, local); err != nil {
		recordUnownedChange(p.configPath)
		return rollback(NewError(ErrorConflict, fmt.Errorf("publish local config: %w", err)))
	}
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	if err := i.writePublishedFile(snapshotForPath(p.targetSnapshots, p.manifestPath), portable, 0o644, snapshotExists(p.targetSnapshots, p.manifestPath)); err != nil {
		recordFailedPublication(p.manifestPath, portable)
		return rollback(fmt.Errorf("publish portable manifest: %w", err))
	}
	if err := recordOwned(p.manifestPath, portable); err != nil {
		recordUnownedChange(p.manifestPath)
		return rollback(NewError(ErrorConflict, fmt.Errorf("publish portable manifest: %w", err)))
	}
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	if err := i.writePublishedRegistry(snapshotForPath(p.targetSnapshots, p.registryPath), p.registryPath, p.registry); err != nil {
		recordFailedPublication(p.registryPath, registry)
		return rollback(fmt.Errorf("publish project registry: %w", err))
	}
	if err := recordOwned(p.registryPath, registry); err != nil {
		recordUnownedChange(p.registryPath)
		return rollback(NewError(ErrorConflict, fmt.Errorf("publish project registry: %w", err)))
	}
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	if err := i.writePublishedWorkspace(snapshotForPath(p.targetSnapshots, p.workspacePath), p.workspacePath, p.state); err != nil {
		recordFailedPublication(p.workspacePath, workspace)
		return rollback(fmt.Errorf("publish default workspace: %w", err))
	}
	if err := recordOwned(p.workspacePath, workspace); err != nil {
		recordUnownedChange(p.workspacePath)
		return rollback(NewError(ErrorConflict, fmt.Errorf("publish default workspace: %w", err)))
	}
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	return nil
}

func snapshotExists(snapshots []fileSnapshot, path string) bool {
	for _, snapshot := range snapshots {
		if snapshot.path == path {
			return snapshot.exists
		}
	}
	return false
}

func snapshotForPath(snapshots []fileSnapshot, path string) fileSnapshot {
	for _, snapshot := range snapshots {
		if snapshot.path == path {
			return snapshot
		}
	}
	return fileSnapshot{path: path}
}

func (i *Initializer) writePublishedFile(before fileSnapshot, data []byte, mode os.FileMode, exists bool) error {
	if i.writeFile != nil {
		return i.writeFile(before.path, data, mode)
	}
	compare := func() error { return revalidateSnapshot(before) }
	if !exists {
		if !i.useFileCAS {
			return fsutil.WriteFileAtomicCreateModeWithReplace(before.path, data, mode, i.rename)
		}
		return fsutil.WriteFileAtomicCreateModeWithHook(before.path, data, mode, func(step string) error {
			if step == "before-rename" {
				return compare()
			}
			return nil
		})
	}
	if !i.useFileCAS {
		return fsutil.WriteFileAtomicModeWithReplace(before.path, data, mode, i.rename)
	}
	return fsutil.WriteFileAtomicModeWithHook(before.path, data, mode, func(step string) error {
		if step == "before-rename" {
			return compare()
		}
		return nil
	})
}

func (i *Initializer) writePublishedRegistry(before fileSnapshot, path string, value store.Registry) error {
	if !i.useStoreCAS {
		return i.writeRegistry(path, value)
	}
	return store.WriteRegistryCAS(path, value, func() error { return revalidateSnapshot(before) })
}

func (i *Initializer) writePublishedWorkspace(before fileSnapshot, path string, value store.WorkspaceState) error {
	if !i.useStoreCAS {
		return i.writeWorkspace(path, value)
	}
	return store.WriteWorkspaceCAS(path, value, func() error { return revalidateSnapshot(before) })
}

func (p initPlan) result(dry bool) InitResult {
	updates := make([]IgnoreUpdate, 0, len(p.ignoreWrites))
	for _, write := range p.ignoreWrites {
		updates = append(updates, write.update)
	}
	return InitResult{ProjectID: p.id, ConfigPath: p.configPath, ManifestPath: p.manifestPath, ManifestSource: p.configuration.Manifest.Source, Repositories: p.repositories, DryRun: dry, LocalConfig: p.configuration, PortableManifest: p.manifest, IgnoreUpdates: updates}
}

func ParseCloneURLOverrides(values []string) (map[string]string, error) {
	result := map[string]string{}
	for _, value := range values {
		id, raw, found := strings.Cut(value, "=")
		if !found || id == "" || raw == "" {
			return nil, fmt.Errorf("clone URL override must use repository-id=url")
		}
		if _, exists := result[id]; exists {
			return nil, fmt.Errorf("clone URL override for repository %q is duplicated", id)
		}
		if err := config.ValidatePortableID(id); err != nil {
			return nil, fmt.Errorf("clone URL override repository %q: %w", id, err)
		}
		if err := config.ValidateCloneURL(raw); err != nil {
			return nil, fmt.Errorf("clone URL override repository %q: %w", id, err)
		}
		result[id] = raw
	}
	return result, nil
}

func normalizeInitManifestSource(value, defaultPath string) (string, error) {
	if value == "" {
		return filepath.Clean(defaultPath), nil
	}
	if strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("invalid manifest source")
		}
		parsed.Scheme, parsed.Host = strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host)
		source := parsed.String()
		if err := config.ValidateManifestSource(source); err != nil {
			return "", err
		}
		return source, nil
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if err := config.ValidateManifestSource(absolute); err != nil {
		return "", err
	}
	return absolute, nil
}

func readOptionalFile(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, 0o644, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	return data, info.Mode().Perm(), err
}
func preflightTarget(path string) error {
	directory := filepath.Dir(path)
	for {
		info, err := os.Stat(directory)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("preflight target %q: parent is not a directory", path)
			}
			if info.Mode().Perm()&0o200 == 0 {
				return fmt.Errorf("preflight target %q: parent is not writable", path)
			}
			break
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("preflight target %q: %w", path, err)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return fmt.Errorf("preflight target %q: no existing parent", path)
		}
		directory = parent
	}
	if _, _, err := readOptionalFile(path); err != nil {
		return err
	}
	return nil
}
func snapshotFile(path string) (fileSnapshot, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return fileSnapshot{path: path, mode: 0o644}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("%s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{path: path, data: data, mode: info.Mode().Perm(), exists: true, info: info}, nil
}
func revalidateSnapshots(snapshots []fileSnapshot) error {
	for _, planned := range snapshots {
		if err := revalidateSnapshot(planned); err != nil {
			return fmt.Errorf("publication target %q changed after preflight: %w", planned.path, err)
		}
	}
	return nil
}

func revalidateSnapshot(expected fileSnapshot) error {
	current, err := snapshotFile(expected.path)
	if err != nil {
		return err
	}
	if !sameSnapshot(expected, current) {
		return errors.New("generation changed")
	}
	return nil
}

func sameSnapshot(left, right fileSnapshot) bool {
	if left.path != right.path || left.exists != right.exists || left.mode != right.mode || !bytes.Equal(left.data, right.data) {
		return false
	}
	return !left.exists || left.info == nil || right.info == nil || os.SameFile(left.info, right.info)
}

func publishedSnapshot(path string, data []byte) (fileSnapshot, error) {
	snapshot, err := snapshotFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	if !snapshot.exists || !bytes.Equal(snapshot.data, data) {
		return fileSnapshot{}, errors.New("writer did not leave the planned generation")
	}
	return snapshot, nil
}

func restoreOwnedSnapshots(i *Initializer, originals, owned []fileSnapshot) error {
	var failures []error
	for n := len(owned) - 1; n >= 0; n-- {
		ownedSnapshot := owned[n]
		snapshot := snapshotForPath(originals, ownedSnapshot.path)
		var err error
		if err = revalidateSnapshot(ownedSnapshot); err != nil {
			failures = append(failures, fmt.Errorf("restore %s: refusing concurrently replaced generation: %w", snapshot.path, err))
			continue
		}
		if snapshot.exists {
			err = fsutil.WriteFileAtomicModeWithHook(snapshot.path, snapshot.data, snapshot.mode, func(step string) error {
				if step == "before-rename" {
					return revalidateSnapshot(ownedSnapshot)
				}
				return nil
			})
		} else {
			err = i.removeOwnedFile(ownedSnapshot)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("restore %s: %w", snapshot.path, err))
		}
	}
	return errors.Join(failures...)
}

// removeOwnedFile removes the public name by atomically capturing its current
// generation in a transaction-private directory. The quarantined generation
// and now-empty public name are both validated after that rename boundary:
// the public path is never passed to an unlink after a pathname check. A
// replacement captured at the boundary is linked back without overwriting
// any newer path and makes the rollback incomplete.
func (i *Initializer) removeOwnedFile(owned fileSnapshot) (result error) {
	quarantineDir, err := os.MkdirTemp(filepath.Dir(owned.path), ".wtree-init-rollback-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(quarantineDir); err != nil && !os.IsNotExist(err) {
			result = errors.Join(result, fmt.Errorf("remove rollback quarantine %s: %w", quarantineDir, err))
		}
	}()
	capturedPath := filepath.Join(quarantineDir, filepath.Base(owned.path))
	if i.beforeOwnedRemove != nil {
		i.beforeOwnedRemove(owned.path)
	}
	if err := i.captureOwnedFile(owned.path, capturedPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("refusing changed generation at removal boundary: %w", err)
		}
		return err
	}
	captured := owned
	captured.path = capturedPath
	if err := revalidateSnapshot(captured); err != nil {
		restoreErr := restoreCapturedFile(i, capturedPath, owned.path)
		return errors.Join(fmt.Errorf("refusing concurrently replaced generation at removal boundary: %w", err), restoreErr)
	}
	// snapshotFile represents a missing file with its default mode. Preserve
	// that representation here so the successfully emptied public path is not
	// mistaken for a concurrent change merely because a zero-value snapshot has
	// a different mode.
	if err := revalidateSnapshot(fileSnapshot{path: owned.path, mode: 0o644}); err != nil {
		cleanupErr := i.remove(capturedPath)
		return errors.Join(fmt.Errorf("refusing concurrently replaced public generation after capture: %w", err), cleanupErr)
	}
	if err := i.remove(capturedPath); err != nil && !os.IsNotExist(err) {
		restoreErr := restoreCapturedFile(i, capturedPath, owned.path)
		return errors.Join(err, restoreErr)
	}
	return nil
}

// restoreCapturedFile uses a hard link so restoration can never overwrite a
// generation that appeared at the public path after capture. Once linked, the
// captured generation remains reachable even if quarantine cleanup fails.
func restoreCapturedFile(i *Initializer, capturedPath, targetPath string) error {
	if err := os.Link(capturedPath, targetPath); err != nil {
		return fmt.Errorf("retain captured generation %s at %s: %w", capturedPath, targetPath, err)
	}
	if err := i.remove(capturedPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove retained quarantine link %s: %w", capturedPath, err)
	}
	return nil
}

func deterministicInitProjectID(repositories map[string]config.PortableRepository) string {
	// The placeholder project is constant: its canonical encoding therefore
	// binds this ID only to portable repository facts, never a checkout path,
	// common Git directory, or display name.
	identity, err := config.MarshalPortableManifest(config.PortableManifest{
		Version:      config.PortableManifestVersion,
		Project:      config.PortableProject{ID: "identity", Name: "identity"},
		Repositories: repositories,
	})
	if err != nil {
		panic(fmt.Sprintf("validated portable repository facts cannot be encoded: %v", err))
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, append([]byte("wtree:init:v2\n"), identity...)).String()
}

// availableInitProjectID keeps portable identity stable for a new project,
// while never reusing retained state or lock storage after an intentional
// unregister/prune. A registered ID remains a conflict; only unregistered,
// retained storage receives a deterministic collision suffix.
func availableInitProjectID(base, dataDir string, registry store.Registry) (string, error) {
	for attempt := 0; attempt != 32; attempt++ {
		candidate := base
		if attempt != 0 {
			candidate = uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("wtree:init:v2:retained\n%s\n%d", base, attempt))).String()
		}
		if _, registered := registry.Projects[candidate]; registered {
			if attempt == 0 {
				return candidate, nil
			}
			continue
		}
		occupied, err := initIDHasRetainedArtifacts(dataDir, candidate)
		if err != nil {
			return "", err
		}
		if !occupied {
			return candidate, nil
		}
	}
	return "", NewError(ErrorConflict, fmt.Errorf("cannot allocate a collision-free project ID for retained project data"))
}

func initIDHasRetainedArtifacts(dataDir, projectID string) (bool, error) {
	for _, path := range []string{filepath.Join(dataDir, "projects", projectID), filepath.Join(dataDir, "state", projectID)} {
		_, err := os.Lstat(path)
		if err == nil {
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
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
		for common, id := range project.RepositoryIDs {
			identities[common] = id
		}
		project.RepositoryIDs = identities
		clone.Projects[projectID] = project
	}
	return clone
}
func rejectRegistrationConflicts(configPath string, repositoryIDs map[string]string, registry store.Registry) error {
	identities := make([]string, 0, len(repositoryIDs))
	for identity := range repositoryIDs {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	candidates := make([]RegistrationConflictCandidate, 0, len(registry.Projects))
	for id, project := range registry.Projects {
		registered := make([]string, 0, len(project.RepositoryIDs))
		for identity := range project.RepositoryIDs {
			registered = append(registered, identity)
		}
		candidates = append(candidates, RegistrationConflictCandidate{ID: id, ConfigPath: project.ConfigPath, RepositoryIdentities: registered})
	}
	if ids := RegistrationConflictIDs(configPath, identities, candidates); len(ids) != 0 {
		return NewError(ErrorConflict, fmt.Errorf("project registration conflicts with existing project IDs %s; inspect registrations with `wtree project list`", strings.Join(ids, ", ")))
	}
	return nil
}
