package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/pathutil"
)

const ClonePlanVersion = 2

type ClonePlanRequest struct {
	ManifestSource string
	Destination    string
	CWD            string
	DataDir        string
	WorktreeRoot   string
}

type ClonePlanSource struct {
	Kind   ManifestSourceKind `json:"kind"`
	Value  string             `json:"value"`
	SHA256 string             `json:"sha256"`
}

type CloneDestinationFacts struct {
	Path                     string          `json:"path"`
	Parent                   string          `json:"parent"`
	CanonicalParent          string          `json:"canonicalParent"`
	ParentMode               uint32          `json:"parentMode"`
	ParentModTime            string          `json:"parentModTime"`
	AncestorFacts            []ClonePathFact `json:"ancestorFacts"`
	DestinationDidNotExist   bool            `json:"destinationDidNotExist"`
	RegistrySHA256           string          `json:"registrySha256"`
	RegistryGenerationSHA256 string          `json:"registryGenerationSha256"`
}

type ClonePathFact struct {
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"`
	ModTime string `json:"modTime"`
}

type CloneVerification struct {
	TrackedManifestExact  bool     `json:"trackedManifestExact"`
	InitialCommits        []string `json:"initialCommits"`
	CleanWorktree         bool     `json:"cleanWorktree"`
	NoSubmodules          bool     `json:"noSubmodules"`
	CommittedParentIgnore bool     `json:"committedParentIgnore"`
}

type ClonePlanRepository struct {
	ID             string            `json:"id"`
	Parent         string            `json:"parent"`
	Mount          string            `json:"mount"`
	Path           string            `json:"path"`
	CloneRemote    string            `json:"cloneRemote"`
	CloneURL       string            `json:"cloneUrl"`
	LocalBranch    string            `json:"localBranch"`
	RemoteRef      string            `json:"remoteRef"`
	ObservedCommit string            `json:"observedCommit"`
	Verification   CloneVerification `json:"verification"`
}

type ClonePlanAction struct {
	Sequence            int      `json:"sequence"`
	Action              string   `json:"action"`
	RepositoryID        string   `json:"repositoryId,omitempty"`
	Path                string   `json:"path,omitempty"`
	ParentRepositoryID  string   `json:"parentRepositoryId,omitempty"`
	ParentPath          string   `json:"parentPath,omitempty"`
	ChildMount          string   `json:"childMount,omitempty"`
	IgnoreRuleSubject   string   `json:"ignoreRuleSubject,omitempty"`
	ChildInitialCommits []string `json:"childInitialCommits,omitempty"`
}

// ClonePlan is a complete read-only observation for a later executor. Private
// manifest bytes are copied on access and deliberately excluded from JSON.
type ClonePlan struct {
	Version        int                    `json:"version"`
	Operation      string                 `json:"operation"`
	Source         ClonePlanSource        `json:"source"`
	Destination    CloneDestinationFacts  `json:"destination"`
	Project        config.PortableProject `json:"project"`
	LogicalRoot    string                 `json:"logicalRoot"`
	BaseRepository string                 `json:"baseRepository"`
	Repositories   []ClonePlanRepository  `json:"repositories"`
	Actions        []ClonePlanAction      `json:"actions"`
	WorktreeRoot   string                 `json:"worktreeRoot,omitempty"`
	DataDir        string                 `json:"dataDir"`
	manifestData   []byte
}

func (plan ClonePlan) ManifestBytes() []byte { return append([]byte(nil), plan.manifestData...) }

func (plan ClonePlan) JSON() ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (plan ClonePlan) Validate() error {
	if plan.Version != ClonePlanVersion || plan.Operation != "clone" {
		return errors.New("invalid clone plan version or operation")
	}
	if plan.Source.Value == "" || plan.Source.SHA256 == "" || plan.Destination.Path == "" || plan.DataDir == "" || !plan.Destination.DestinationDidNotExist || plan.LogicalRoot != plan.Destination.Path || plan.BaseRepository != plan.Project.BaseRepository {
		return errors.New("incomplete clone plan provenance or destination facts")
	}
	if _, err := hex.DecodeString(plan.Source.SHA256); err != nil || len(plan.Source.SHA256) != sha256.Size*2 {
		return errors.New("invalid clone plan manifest digest")
	}
	if err := config.ValidateManifestSource(plan.Source.Value); err != nil {
		return fmt.Errorf("invalid clone plan manifest source: %w", err)
	}
	isHTTPSource := strings.HasPrefix(strings.ToLower(plan.Source.Value), "http://") || strings.HasPrefix(strings.ToLower(plan.Source.Value), "https://")
	if plan.Source.Kind != ManifestSourceLocal && plan.Source.Kind != ManifestSourceHTTP || (plan.Source.Kind == ManifestSourceHTTP) != isHTTPSource {
		return errors.New("invalid clone plan manifest source kind")
	}
	if !filepath.IsAbs(plan.Destination.Path) || filepath.Clean(plan.Destination.Path) != plan.Destination.Path || filepath.Dir(plan.Destination.Path) != plan.Destination.CanonicalParent || plan.Destination.Parent == "" || plan.Destination.ParentMode == 0 || plan.Destination.ParentModTime == "" || len(plan.Destination.AncestorFacts) == 0 || plan.Destination.RegistrySHA256 == "" || plan.Destination.RegistryGenerationSHA256 == "" {
		return errors.New("invalid clone plan destination observation")
	}
	if plan.Destination.RegistrySHA256 != "absent" && !validSHA256(plan.Destination.RegistrySHA256) || !validSHA256(plan.Destination.RegistryGenerationSHA256) {
		return errors.New("invalid clone plan registry generation digest")
	}
	if _, err := time.Parse(time.RFC3339Nano, plan.Destination.ParentModTime); err != nil {
		return errors.New("invalid clone plan destination timestamp")
	}
	for index, fact := range plan.Destination.AncestorFacts {
		mode := os.FileMode(fact.Mode)
		if !filepath.IsAbs(fact.Path) || filepath.Clean(fact.Path) != fact.Path || !mode.IsDir() || mode&os.ModeSymlink != 0 || fact.ModTime == "" {
			return errors.New("invalid clone destination ancestor observation")
		}
		if _, err := time.Parse(time.RFC3339Nano, fact.ModTime); err != nil {
			return errors.New("invalid clone destination ancestor timestamp")
		}
		if index > 0 && filepath.Dir(fact.Path) != plan.Destination.AncestorFacts[index-1].Path {
			return errors.New("invalid clone destination ancestor chain")
		}
	}
	lastAncestor := plan.Destination.AncestorFacts[len(plan.Destination.AncestorFacts)-1]
	if lastAncestor.Path != plan.Destination.Parent || lastAncestor.Mode != plan.Destination.ParentMode || lastAncestor.ModTime != plan.Destination.ParentModTime {
		return errors.New("clone destination parent and ancestor facts disagree")
	}
	if !filepath.IsAbs(plan.DataDir) || filepath.Clean(plan.DataDir) != plan.DataDir {
		return errors.New("invalid clone plan data directory")
	}
	if len(plan.Repositories) == 0 || len(plan.Actions) == 0 {
		return errors.New("clone plan has no repositories or actions")
	}
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: plan.Project, Repositories: make(map[string]config.PortableRepository, len(plan.Repositories))}
	seen := map[string]bool{}
	paths := map[string]string{}
	for _, repository := range plan.Repositories {
		if repository.ID == "" || !validClonePlanObjectID(repository.ObservedCommit) || seen[repository.ID] || (repository.Parent != "" && !seen[repository.Parent]) {
			return fmt.Errorf("invalid parent-first clone repository %q", repository.ID)
		}
		if repository.Verification.TrackedManifestExact != (repository.ID == plan.BaseRepository) || repository.Verification.CommittedParentIgnore != (repository.Parent != "") || !repository.Verification.CleanWorktree || !repository.Verification.NoSubmodules {
			return fmt.Errorf("invalid clone verification or path for repository %q", repository.ID)
		}
		manifest.Repositories[repository.ID] = config.PortableRepository{Clone: config.CloneSource{Remote: repository.CloneRemote, URL: repository.CloneURL}, Upstream: config.Upstream{Branch: repository.LocalBranch, Remote: repository.CloneRemote, Merge: repository.RemoteRef}, Identity: config.RepositoryIdentity{InitialCommits: append([]string(nil), repository.Verification.InitialCommits...)}, Parent: repository.Parent, Mount: repository.Mount, DefaultBranch: repository.LocalBranch}
		seen[repository.ID] = true
		paths[repository.ID] = repository.Path
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("invalid clone plan repository contract: %w", err)
	}
	project := cloneDomainProject(manifest)
	effectivePaths, err := project.EffectivePaths(plan.Destination.Path, nil)
	if err != nil {
		return fmt.Errorf("invalid clone plan effective paths: %w", err)
	}
	for id, path := range effectivePaths {
		if paths[id] != path {
			return fmt.Errorf("invalid clone path for repository %q", id)
		}
	}
	if expected := clonePlanActions(plan.Repositories, plan.Destination.Path, plan.BaseRepository); !reflect.DeepEqual(plan.Actions, expected) {
		return errors.New("invalid clone action ordering or contract")
	}
	if plan.manifestData != nil {
		digest := sha256.Sum256(plan.manifestData)
		if hex.EncodeToString(digest[:]) != plan.Source.SHA256 {
			return errors.New("clone plan manifest bytes do not match their digest")
		}
		decoded, err := config.LoadPortableManifest(plan.manifestData)
		if err != nil || !reflect.DeepEqual(decoded, manifest) {
			return errors.New("clone plan decisions do not match its validated manifest bytes")
		}
	}
	return nil
}

func validSHA256(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil && len(value) == sha256.Size*2 && value == strings.ToLower(value)
}

func validClonePlanObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

type CloneRemoteFacts interface {
	AdvertisedCommit(context.Context, string, string) (string, error)
}

type ClonePlannerDependencies struct {
	Loader           *ManifestSourceLoader
	RemoteFacts      CloneRemoteFacts
	RegistryFacts    CloneRegistryFactsReader
	FileSystem       CloneFileSystemFacts
	BeforeRemoteRead func(string)
}

type CloneFileSystemFacts interface {
	Getwd() (string, error)
	Abs(string) (string, error)
	Lstat(string) (os.FileInfo, error)
	EvalSymlinks(string) (string, error)
}

type osCloneFileSystemFacts struct{}

func (osCloneFileSystemFacts) Getwd() (string, error)                  { return os.Getwd() }
func (osCloneFileSystemFacts) Abs(value string) (string, error)        { return filepath.Abs(value) }
func (osCloneFileSystemFacts) Lstat(value string) (os.FileInfo, error) { return os.Lstat(value) }
func (osCloneFileSystemFacts) EvalSymlinks(value string) (string, error) {
	return filepath.EvalSymlinks(value)
}

type ClonePlanner struct {
	loader           *ManifestSourceLoader
	remote           CloneRemoteFacts
	registryFacts    CloneRegistryFactsReader
	fs               CloneFileSystemFacts
	beforeRemoteRead func(string)
}

func NewClonePlanner() *ClonePlanner { return NewClonePlannerWith(ClonePlannerDependencies{}) }

// NewClonePlannerWith fills every omitted dependency with its safe production
// implementation, so a caller cannot accidentally construct a partial planner.
func NewClonePlannerWith(dependencies ClonePlannerDependencies) *ClonePlanner {
	if dependencies.Loader == nil {
		dependencies.Loader = NewManifestSourceLoader()
	}
	if dependencies.RemoteFacts == nil {
		dependencies.RemoteFacts = gitadapter.NewAdapter("git")
	}
	if dependencies.RegistryFacts == nil {
		dependencies.RegistryFacts = newCloneRegistryFactsReader()
	}
	if dependencies.FileSystem == nil {
		dependencies.FileSystem = osCloneFileSystemFacts{}
	}
	return &ClonePlanner{loader: dependencies.Loader, remote: dependencies.RemoteFacts, registryFacts: dependencies.RegistryFacts, fs: dependencies.FileSystem, beforeRemoteRead: dependencies.BeforeRemoteRead}
}

func (planner *ClonePlanner) Plan(ctx context.Context, request ClonePlanRequest) (ClonePlan, error) {
	if planner == nil {
		planner = NewClonePlanner()
	}
	plan, _, err := planner.planAttempt(ctx, request)
	return plan, err
}

type clonePlanningAttempt struct {
	requestSource *CloneResultRequestSource
	source        *ClonePlanSource
	stage         CloneResultStage
}

func (planner *ClonePlanner) planAttempt(ctx context.Context, request ClonePlanRequest) (ClonePlan, clonePlanningAttempt, error) {
	attempt := clonePlanningAttempt{stage: CloneResultStageSource}
	kind, normalized, err := planner.loader.normalize(request.ManifestSource)
	if err != nil {
		return ClonePlan{}, attempt, NewError(ErrorValidation, fmt.Errorf("manifest source %q: %w", redactManifestSource(request.ManifestSource), err))
	}
	attempt.requestSource = &CloneResultRequestSource{Kind: kind, Value: normalized}
	loaded, err := planner.loader.loadNormalized(ctx, kind, normalized)
	if err != nil {
		return ClonePlan{}, attempt, err
	}
	loadedSource := cloneResultSourceFromLoaded(loaded)
	attempt.source = &loadedSource
	attempt.stage = CloneResultStageDecode
	manifest, err := config.LoadPortableManifest(loaded.Bytes())
	if err != nil {
		return ClonePlan{}, attempt, NewError(ErrorValidation, fmt.Errorf("decode manifest %q: logical-root manifest format is required: %s", loaded.Source, boundedRedactedDiagnostic(err.Error())))
	}
	attempt.stage = CloneResultStageDestination
	destination, err := inspectCloneDestination(request, manifest.Project.Name, planner.fs)
	if err != nil {
		return ClonePlan{}, attempt, NewError(ErrorValidation, err)
	}
	attempt.stage = CloneResultStageRegistry
	if request.DataDir == "" {
		return ClonePlan{}, attempt, NewError(ErrorValidation, errors.New("data directory is required for clone planning"))
	}
	dataDir, err := planner.fs.Abs(request.DataDir)
	if err != nil {
		return ClonePlan{}, attempt, NewError(ErrorValidation, fmt.Errorf("resolve data directory: %w", err))
	}
	dataDir = filepath.Clean(dataDir)
	registry, err := planner.registryFacts.Read(dataDir)
	if err != nil {
		var applicationError *Error
		if errors.As(err, &applicationError) {
			return ClonePlan{}, attempt, applicationError
		}
		return ClonePlan{}, attempt, NewError(ErrorValidation, fmt.Errorf("read project registry: %w", err))
	}
	destination.RegistrySHA256 = registry.RegistrySHA256
	destination.RegistryGenerationSHA256 = registry.GenerationSHA256
	if err := validateCloneRegistryFacts(manifest.Project.ID, destination.Path, registry, planner.fs); err != nil {
		return ClonePlan{}, attempt, err
	}

	order, err := portableRepositoryOrder(manifest)
	if err != nil {
		return ClonePlan{}, attempt, NewError(ErrorValidation, err)
	}
	attempt.stage = CloneResultStageRemote
	repositories := make([]ClonePlanRepository, 0, len(order))
	project := cloneDomainProject(manifest)
	paths, err := project.EffectivePaths(destination.Path, nil)
	if err != nil {
		return ClonePlan{}, attempt, NewError(ErrorValidation, fmt.Errorf("resolve clone forest paths: %w", err))
	}
	var remoteErrors []cloneRemoteFailure
	for _, id := range order {
		repository := manifest.Repositories[id]
		path := paths[id]
		if planner.beforeRemoteRead != nil {
			planner.beforeRemoteRead(id)
		}
		commit, remoteErr := planner.remote.AdvertisedCommit(ctx, repository.Clone.URL, repository.Upstream.Merge)
		if remoteErr != nil {
			remoteErrors = append(remoteErrors, cloneRemoteFailure{RepositoryID: id, RemoteRef: repository.Upstream.Merge, Diagnostic: remoteErr.Error()})
		}
		repositories = append(repositories, ClonePlanRepository{
			ID: id, Parent: repository.Parent, Mount: repository.Mount, Path: path,
			CloneRemote: repository.Clone.Remote, CloneURL: repository.Clone.URL,
			LocalBranch: repository.DefaultBranch, RemoteRef: repository.Upstream.Merge,
			ObservedCommit: commit,
			Verification:   CloneVerification{TrackedManifestExact: id == manifest.Project.BaseRepository, InitialCommits: append([]string(nil), repository.Identity.InitialCommits...), CleanWorktree: true, NoSubmodules: true, CommittedParentIgnore: repository.Parent != ""},
		})
	}
	if len(remoteErrors) != 0 {
		return ClonePlan{}, attempt, NewError(ErrorGit, errors.New(formatCloneRemoteFailures(remoteErrors, len(order))))
	}
	actions := clonePlanActions(repositories, destination.Path, manifest.Project.BaseRepository)
	digest := sha256.Sum256(loaded.Bytes())
	plan := ClonePlan{
		Version: ClonePlanVersion, Operation: "clone",
		Source:      ClonePlanSource{Kind: loaded.Kind, Value: loaded.Source, SHA256: hex.EncodeToString(digest[:])},
		Destination: destination, Project: manifest.Project, LogicalRoot: destination.Path, BaseRepository: manifest.Project.BaseRepository, Repositories: repositories,
		Actions: actions, WorktreeRoot: request.WorktreeRoot, DataDir: dataDir, manifestData: loaded.Bytes(),
	}
	if err := plan.Validate(); err != nil {
		attempt.stage = CloneResultStageInternal
		return ClonePlan{}, attempt, NewError(ErrorInternal, err)
	}
	return plan, attempt, nil
}

// PlanningResult runs the complete read-only planner and returns a validated
// status-specific result even when planning stops before a ClonePlan exists.
func (planner *ClonePlanner) PlanningResult(ctx context.Context, request ClonePlanRequest) (CloneResult, error) {
	if planner == nil {
		planner = NewClonePlanner()
	}
	plan, attempt, err := planner.planAttempt(ctx, request)
	if err == nil {
		return NewCloneResult(plan)
	}
	return newCloneFailureResult(attempt.requestSource, attempt.source, attempt.stage, err)
}

type cloneRemoteFailure struct {
	RepositoryID string
	RemoteRef    string
	Diagnostic   string
}

func formatCloneRemoteFailures(failures []cloneRemoteFailure, totalRemotes int) string {
	sort.Slice(failures, func(left, right int) bool {
		if failures[left].RepositoryID != failures[right].RepositoryID {
			return failures[left].RepositoryID < failures[right].RepositoryID
		}
		return failures[left].RemoteRef < failures[right].RemoteRef
	})
	const maxFailures = 32
	const maxTotal = 16384
	const maxDiagnostic = 384
	parts := make([]string, 0, minInt(len(failures), maxFailures)+1)
	used := 0
	included := 0
	for _, failure := range failures {
		if included == maxFailures {
			break
		}
		part := fmt.Sprintf("repository %q remote branch %q: %s", failure.RepositoryID, failure.RemoteRef, boundedRedactedDiagnosticLimit(failure.Diagnostic, maxDiagnostic))
		separator := 0
		if len(parts) != 0 {
			separator = 2
		}
		// Reserve enough space for a deterministic omission marker.
		if used+separator+len(part)+96 > maxTotal {
			break
		}
		parts = append(parts, part)
		used += separator + len(part)
		included++
	}
	if included < len(failures) {
		parts = append(parts, fmt.Sprintf("... %d additional repository remote errors omitted (all %d remotes were queried)", len(failures)-included, totalRemotes))
	}
	return strings.Join(parts, "; ")
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// DryRun intentionally shares Plan's complete read-only path.
func (planner *ClonePlanner) DryRun(ctx context.Context, request ClonePlanRequest) (ClonePlan, error) {
	return planner.Plan(ctx, request)
}

func inspectCloneDestination(request ClonePlanRequest, projectName string, filesystem CloneFileSystemFacts) (CloneDestinationFacts, error) {
	lexicalCWD := request.CWD
	if lexicalCWD == "" {
		var err error
		lexicalCWD, err = filesystem.Getwd()
		if err != nil {
			return CloneDestinationFacts{}, fmt.Errorf("resolve caller working directory: %w", err)
		}
	}
	lexicalCWD, err := filesystem.Abs(lexicalCWD)
	if err != nil {
		return CloneDestinationFacts{}, fmt.Errorf("resolve caller working directory: %w", err)
	}
	lexicalCWD = filepath.Clean(lexicalCWD)
	canonicalCWD := lexicalCWD
	if resolved, resolveErr := filesystem.EvalSymlinks(lexicalCWD); resolveErr == nil {
		canonicalCWD = resolved
	} else {
		return CloneDestinationFacts{}, fmt.Errorf("canonicalize caller working directory: %w", resolveErr)
	}
	value := request.Destination
	if value == "" {
		if err := config.ValidatePortableMount(projectName, pathutil.ChildMount); err != nil || filepath.Base(projectName) != projectName {
			return CloneDestinationFacts{}, errors.New("manifest project name is not a safe destination component; provide an explicit destination")
		}
		value = projectName
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(canonicalCWD, value)
	}
	value = filepath.Clean(value)
	if pathWithin(lexicalCWD, value) {
		relative, _ := filepath.Rel(lexicalCWD, value)
		value = filepath.Join(canonicalCWD, relative)
	}
	volume := filepath.VolumeName(value)
	if value == filepath.Clean(volume+string(filepath.Separator)) || value == "." {
		return CloneDestinationFacts{}, fmt.Errorf("clone destination %q is too broad", value)
	}
	if _, statErr := filesystem.Lstat(value); statErr == nil {
		return CloneDestinationFacts{}, fmt.Errorf("clone destination %q already exists", value)
	} else if !os.IsNotExist(statErr) {
		return CloneDestinationFacts{}, fmt.Errorf("inspect clone destination %q: %w", value, statErr)
	}
	parent := filepath.Dir(value)
	ancestorBase := volumeRoot(value)
	if pathWithin(lexicalCWD, parent) {
		ancestorBase = lexicalCWD
	} else if pathWithin(canonicalCWD, parent) {
		ancestorBase = canonicalCWD
	}
	ancestorFacts, err := inspectCloneAncestors(ancestorBase, parent, filesystem)
	if err != nil {
		return CloneDestinationFacts{}, err
	}
	info, err := filesystem.Lstat(parent)
	if err != nil {
		return CloneDestinationFacts{}, fmt.Errorf("clone destination parent %q is unavailable: %w", parent, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return CloneDestinationFacts{}, fmt.Errorf("clone destination parent %q must be a real directory", parent)
	}
	if info.Mode().Perm()&0o222 == 0 {
		return CloneDestinationFacts{}, fmt.Errorf("clone destination parent %q is not writable", parent)
	}
	canonicalParent, err := filesystem.EvalSymlinks(parent)
	if err != nil {
		return CloneDestinationFacts{}, fmt.Errorf("canonicalize clone destination parent %q: %w", parent, err)
	}
	canonicalPath := filepath.Join(canonicalParent, filepath.Base(value))
	return CloneDestinationFacts{Path: canonicalPath, Parent: parent, CanonicalParent: canonicalParent, ParentMode: uint32(info.Mode()), ParentModTime: info.ModTime().UTC().Format(time.RFC3339Nano), AncestorFacts: ancestorFacts, DestinationDidNotExist: true}, nil
}

func inspectCloneAncestors(base, parent string, filesystem CloneFileSystemFacts) ([]ClonePathFact, error) {
	base, parent = filepath.Clean(base), filepath.Clean(parent)
	if !pathWithin(base, parent) {
		return nil, fmt.Errorf("clone destination parent %q is outside allowed base %q", parent, base)
	}
	relative, err := filepath.Rel(base, parent)
	if err != nil {
		return nil, fmt.Errorf("resolve clone destination ancestors: %w", err)
	}
	paths := []string{base}
	current := base
	if relative != "." {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			paths = append(paths, current)
		}
	}
	facts := make([]ClonePathFact, 0, len(paths))
	for _, path := range paths {
		info, err := filesystem.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect clone destination ancestor %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("clone destination ancestor %q must be a real directory without symlinks or reparse points", path)
		}
		resolved, err := filesystem.EvalSymlinks(path)
		if err != nil || !sameCloneObservedPath(resolved, path) {
			return nil, fmt.Errorf("clone destination ancestor %q resolves through a symlink, junction, or reparse point", path)
		}
		facts = append(facts, ClonePathFact{Path: path, Mode: uint32(info.Mode()), ModTime: info.ModTime().UTC().Format(time.RFC3339Nano)})
	}
	return facts, nil
}

func sameCloneObservedPath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func pathWithin(base, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func volumeRoot(path string) string {
	return filepath.Clean(filepath.VolumeName(path) + string(filepath.Separator))
}

func validateCloneRegistryFacts(projectID, destination string, snapshot CloneRegistrySnapshot, filesystem CloneFileSystemFacts) error {
	if registered, exists := snapshot.Registry.Projects[projectID]; exists {
		return NewError(ErrorConflict, fmt.Errorf("project ID %q is already registered at %q", projectID, registered.ConfigPath))
	}
	aliases := map[string][]string{}
	projectIDs := make([]string, 0, len(snapshot.Registry.Projects))
	for id := range snapshot.Registry.Projects {
		projectIDs = append(projectIDs, id)
	}
	sort.Strings(projectIDs)
	for _, id := range projectIDs {
		project := snapshot.Registry.Projects[id]
		if project.ConfigPath != "" {
			path := canonicalCloneAlias(filepath.Dir(project.ConfigPath), filesystem)
			aliases[path] = append(aliases[path], "registered project "+id)
		}
	}
	for _, workspace := range snapshot.Workspaces {
		path := canonicalCloneAlias(workspace.State.Path, filesystem)
		aliases[path] = append(aliases[path], "registered workspace "+workspace.ProjectID+"/"+workspace.State.ID+" (file "+workspace.FileName+")")
	}
	owners := aliases[canonicalCloneAlias(destination, filesystem)]
	if len(owners) != 0 {
		return NewError(ErrorConflict, fmt.Errorf("clone destination %q aliases %s", destination, formatCloneAliasOwners(owners)))
	}
	return nil
}

func formatCloneAliasOwners(owners []string) string {
	sort.Strings(owners)
	const maxOwners = 32
	parts := make([]string, 0, minInt(len(owners), maxOwners)+1)
	for index, owner := range owners {
		if index == maxOwners {
			parts = append(parts, fmt.Sprintf("... %d additional alias owners omitted", len(owners)-index))
			break
		}
		parts = append(parts, boundedRedactedDiagnosticLimit(owner, 256))
	}
	return strings.Join(parts, ", ")
}

func canonicalCloneAlias(value string, filesystem CloneFileSystemFacts) string {
	abs, err := filesystem.Abs(value)
	if err != nil {
		return filepath.Clean(value)
	}
	abs = filepath.Clean(abs)
	current := abs
	var suffix []string
	for {
		if resolved, resolveErr := filesystem.EvalSymlinks(current); resolveErr == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
}

func portableRepositoryOrder(manifest config.PortableManifest) ([]string, error) {
	remaining := make(map[string]config.PortableRepository, len(manifest.Repositories))
	for id, repository := range manifest.Repositories {
		remaining[id] = repository
	}
	var order []string
	seen := map[string]bool{}
	for len(remaining) > 0 {
		var ready []string
		for id, repository := range remaining {
			if repository.Parent == "" || seen[repository.Parent] {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			return nil, errors.New("portable repository hierarchy cannot be ordered")
		}
		sort.Slice(ready, func(left, right int) bool {
			if manifest.Repositories[ready[left]].Parent == "" && ready[left] == manifest.Project.BaseRepository {
				return true
			}
			if manifest.Repositories[ready[right]].Parent == "" && ready[right] == manifest.Project.BaseRepository {
				return false
			}
			return ready[left] < ready[right]
		})
		for _, id := range ready {
			order = append(order, id)
			seen[id] = true
			delete(remaining, id)
		}
	}
	return order, nil
}

func cloneDomainProject(manifest config.PortableManifest) domain.Project {
	repositories := make([]domain.Repository, 0, len(manifest.Repositories))
	for id, repository := range manifest.Repositories {
		repositories = append(repositories, domain.Repository{ID: id, ParentID: repository.Parent, DefaultMount: repository.Mount, DefaultBranch: repository.DefaultBranch})
	}
	return domain.Project{Version: domain.CurrentVersion, ID: manifest.Project.ID, Name: manifest.Project.Name, BaseRepository: manifest.Project.BaseRepository, Repositories: repositories}
}

func clonePlanActions(repositories []ClonePlanRepository, destination, baseRepository string) []ClonePlanAction {
	var actions []ClonePlanAction
	byID := make(map[string]ClonePlanRepository, len(repositories))
	for _, repository := range repositories {
		byID[repository.ID] = repository
	}
	add := func(action, id, path string) {
		actions = append(actions, ClonePlanAction{Sequence: len(actions) + 1, Action: action, RepositoryID: id, Path: path})
	}
	createdGrouping := map[string]bool{}
	for _, repository := range repositories {
		parentPath := destination
		if repository.Parent != "" {
			parentPath = byID[repository.Parent].Path
		}
		mountPath := filepath.FromSlash(repository.Mount)
		directories := []string{}
		for directory := filepath.Dir(mountPath); directory != "." && directory != string(filepath.Separator); directory = filepath.Dir(directory) {
			directories = append(directories, directory)
		}
		for index := len(directories) - 1; index >= 0; index-- {
			path := filepath.Join(parentPath, directories[index])
			if !createdGrouping[path] {
				add("create_grouping_directory", "", path)
				createdGrouping[path] = true
			}
		}
		if repository.Parent != "" {
			parent := byID[repository.Parent]
			actions = append(actions, ClonePlanAction{
				Sequence: len(actions) + 1, Action: "verify_parent_ignore",
				RepositoryID: repository.ID, Path: repository.Path,
				ParentRepositoryID: parent.ID, ParentPath: parent.Path,
				ChildMount: repository.Mount, IgnoreRuleSubject: filepath.ToSlash(repository.Mount),
				ChildInitialCommits: append([]string(nil), repository.Verification.InitialCommits...),
			})
		}
		add("clone_repository", repository.ID, repository.Path)
		add("fetch_selected_branch", repository.ID, repository.Path)
		add("checkout_selected_branch", repository.ID, repository.Path)
		add("verify_repository", repository.ID, repository.Path)
		if repository.ID == baseRepository {
			add("verify_tracked_manifest", repository.ID, repository.Path)
			add("verify_base_metadata_ignore", repository.ID, repository.Path)
		}
	}
	add("write_local_configuration", "", filepath.Join(byID[baseRepository].Path, ".wtree.yml"))
	add("publish_destination", "", destination)
	add("publish_workspace_state", "", destination)
	add("register_project", "", destination)
	return actions
}
