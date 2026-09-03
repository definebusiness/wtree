package config

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/definebusiness/wtree/internal/pathutil"
	"gopkg.in/yaml.v3"
)

// PortableManifestVersion is deliberately independent from the local
// ProjectConfig version: portable manifests evolve on their own contract.
const PortableManifestVersion = 2

// PortableManifest is the tracked, machine-independent project.wtree.yml
// contract. It deliberately does not reuse ProjectConfig, which contains
// local checkout and worktree settings.
type PortableManifest struct {
	Version      int                           `yaml:"version" json:"version"`
	Project      PortableProject               `yaml:"project" json:"project"`
	Repositories map[string]PortableRepository `yaml:"repositories" json:"repositories"`
	Hooks        HookEvents                    `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	SharedHooks  HookEvents                    `yaml:"shared_hooks,omitempty" json:"sharedHooks,omitempty"`
}

type PortableProject struct {
	ID             string `yaml:"id" json:"id"`
	Name           string `yaml:"name" json:"name"`
	BaseRepository string `yaml:"base_repository" json:"base_repository"`
}

type PortableRepository struct {
	Clone         CloneSource        `yaml:"clone" json:"clone"`
	Upstream      Upstream           `yaml:"upstream" json:"upstream"`
	Identity      RepositoryIdentity `yaml:"identity" json:"identity"`
	Parent        string             `yaml:"parent" json:"parent"`
	Mount         string             `yaml:"mount" json:"mount"`
	DefaultBranch string             `yaml:"default_branch" json:"defaultBranch"`
}

type CloneSource struct {
	Remote string `yaml:"remote" json:"remote"`
	URL    string `yaml:"url" json:"url"`
}

type Upstream struct {
	Branch string `yaml:"branch" json:"branch"`
	Remote string `yaml:"remote" json:"remote"`
	Merge  string `yaml:"merge" json:"merge"`
}

type RepositoryIdentity struct {
	InitialCommits []string `yaml:"initial_commits" json:"initialCommits"`
}

// LoadPortableManifest strictly decodes and validates one portable v2 YAML
// document. It never performs I/O or mutates its input.
func LoadPortableManifest(data []byte) (PortableManifest, error) {
	version, err := portableManifestVersion(data)
	if err != nil {
		return PortableManifest{}, err
	}
	if version != PortableManifestVersion && version != PortableManifestVersion3 {
		return PortableManifest{}, fmt.Errorf("unsupported portable manifest version %d: logical-root manifest format version %d is required", version, PortableManifestVersion)
	}
	var manifest PortableManifest
	if version == PortableManifestVersion {
		var v2 portableManifestV2
		err = strictYAML(data, &v2)
		manifest = PortableManifest{Version: v2.Version, Project: v2.Project, Repositories: v2.Repositories}
	} else {
		err = strictYAML(data, &manifest)
	}
	if err != nil {
		return PortableManifest{}, err
	}
	if version == PortableManifestVersion3 {
		if err := validateExplicitHookTimeouts(data); err != nil {
			return PortableManifest{}, err
		}
	}
	if err := manifest.Validate(); err != nil {
		return PortableManifest{}, err
	}
	return manifest, nil
}

// portableManifestV2 intentionally omits hook fields to preserve strict v2
// decoding even though the in-memory v3 representation contains them.
type portableManifestV2 struct {
	Version      int                           `yaml:"version" json:"version"`
	Project      PortableProject               `yaml:"project" json:"project"`
	Repositories map[string]PortableRepository `yaml:"repositories" json:"repositories"`
}

func portableManifestVersion(data []byte) (int, error) {
	var document struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return 0, err
	}
	return document.Version, nil
}

// MarshalPortableManifest writes canonical portable v2 YAML. Repository keys
// and identity roots are sorted so repeated output is byte-identical.
func MarshalPortableManifest(manifest PortableManifest) ([]byte, error) {
	canonical := manifest.canonical()
	if err := canonical.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(portableManifestYAML(canonical))
}

// MarshalProject is the in-memory counterpart of WriteProjectFile. It exists
// for callers that need an encoded local config without writing it.
func MarshalProject(value ProjectConfig) ([]byte, error) {
	if value.Version != ProjectConfigVersion && value.Version != ProjectConfigVersion3 {
		return nil, fmt.Errorf("unsupported project config version %d", value.Version)
	}
	// Preserve the v2 in-memory marshal contract exactly: callers that build a
	// prospective configuration may marshal it before the owning service
	// validates its topology. v3 must validate hooks before canonical output.
	if value.Version == ProjectConfigVersion {
		if err := ValidateManifestMetadata(value.Manifest); err != nil {
			return nil, err
		}
		if len(value.Hooks) != 0 {
			return nil, fmt.Errorf("local project config version %d does not support hooks", ProjectConfigVersion)
		}
	}
	if value.Version == ProjectConfigVersion3 {
		if err := value.Validate(); err != nil {
			return nil, err
		}
	}
	if value.Version == ProjectConfigVersion3 {
		return yaml.Marshal(projectConfigV3YAML(value))
	}
	return yaml.Marshal(value)
}

// Validate validates the portable schema and all pure cross-repository
// invariants before a caller performs any mutation.
func (manifest PortableManifest) Validate() error {
	if manifest.Version != PortableManifestVersion && manifest.Version != PortableManifestVersion3 {
		return fmt.Errorf("unsupported portable manifest version %d: logical-root manifest format version %d is required", manifest.Version, PortableManifestVersion)
	}
	if err := ValidatePortableID(manifest.Project.ID); err != nil {
		return fmt.Errorf("project ID: %w", err)
	}
	if manifest.Project.Name == "" || containsControl(manifest.Project.Name) {
		return fmt.Errorf("project name is required and must not contain control characters")
	}
	if err := ValidatePortableID(manifest.Project.BaseRepository); err != nil {
		return fmt.Errorf("project base repository: %w", err)
	}
	if len(manifest.Repositories) == 0 {
		return fmt.Errorf("portable manifest must contain at least one top-level repository")
	}

	topLevelCount := 0
	for id, repository := range manifest.Repositories {
		if err := ValidatePortableID(id); err != nil {
			return fmt.Errorf("repository ID %q: %w", id, err)
		}
		if repository.Parent == "" {
			topLevelCount++
		}
		if err := repository.validate(id); err != nil {
			return err
		}
	}
	if topLevelCount == 0 {
		return fmt.Errorf("portable manifest must contain at least one top-level repository")
	}
	if topLevelCount > 1 {
		for _, repository := range manifest.Repositories {
			if repository.Parent == "" && repository.Mount == "." {
				return fmt.Errorf("top-level mount %q is valid only as the sole top-level repository", ".")
			}
		}
	}
	if _, exists := manifest.Repositories[manifest.Project.BaseRepository]; !exists {
		return fmt.Errorf("project base repository %q is not declared", manifest.Project.BaseRepository)
	}
	if manifest.Repositories[manifest.Project.BaseRepository].Parent != "" {
		return fmt.Errorf("project base repository %q must be top-level", manifest.Project.BaseRepository)
	}
	if manifest.Version == PortableManifestVersion && (len(manifest.Hooks) != 0 || len(manifest.SharedHooks) != 0) {
		return fmt.Errorf("portable manifest version %d does not support hooks", PortableManifestVersion)
	}
	if manifest.Version == PortableManifestVersion3 {
		if err := validatePortableHookEvents(manifest.Hooks, manifest.Project.BaseRepository, manifest.Repositories, hookSourcePortable); err != nil {
			return err
		}
		if err := validatePortableHookEvents(manifest.SharedHooks, manifest.Project.BaseRepository, manifest.Repositories, hookSourceShared); err != nil {
			return err
		}
	}
	for id, repository := range manifest.Repositories {
		if repository.Parent != "" {
			if _, exists := manifest.Repositories[repository.Parent]; !exists {
				return fmt.Errorf("repository %q has unknown parent %q", id, repository.Parent)
			}
		}
	}

	paths := make(map[string]string, len(manifest.Repositories))
	visiting := make(map[string]bool, len(manifest.Repositories))
	var resolve func(string) (string, error)
	resolve = func(id string) (string, error) {
		if resolved, exists := paths[id]; exists {
			return resolved, nil
		}
		if visiting[id] {
			return "", fmt.Errorf("repository hierarchy contains a cycle at %q", id)
		}
		visiting[id] = true
		repository := manifest.Repositories[id]
		var resolved string
		if repository.Parent == "" {
			resolved = repository.Mount
		} else {
			parentPath, err := resolve(repository.Parent)
			if err != nil {
				return "", err
			}
			resolved = path.Join(parentPath, repository.Mount)
		}
		visiting[id] = false
		paths[id] = resolved
		return resolved, nil
	}
	for id := range manifest.Repositories {
		if _, err := resolve(id); err != nil {
			return err
		}
	}
	for leftID, leftPath := range paths {
		for rightID, rightPath := range paths {
			if leftID >= rightID || !portablePathsOverlap(leftPath, rightPath) {
				continue
			}
			if !portableAncestor(manifest.Repositories, leftID, rightID) && !portableAncestor(manifest.Repositories, rightID, leftID) {
				return fmt.Errorf("repository mount %q conflicts with %q", leftID, rightID)
			}
		}
	}
	return nil
}

func validatePortableHookEvents(events HookEvents, baseRepository string, repositories map[string]PortableRepository, source hookSource) error {
	if err := validateHookEvents(events, baseRepository, source); err != nil {
		return err
	}
	for event, hooks := range events {
		for _, hook := range hooks {
			repository := hook.Repository
			if repository == "" {
				repository = baseRepository
			}
			if _, exists := repositories[repository]; !exists {
				return fmt.Errorf("hook event %q hook %q references unknown repository %q", event, hook.ID, repository)
			}
		}
	}
	return nil
}

func (repository PortableRepository) validate(id string) error {
	kind := pathutil.ChildMount
	if repository.Parent == "" {
		kind = pathutil.TopLevelMount
	}
	if err := ValidatePortableMount(repository.Mount, kind); err != nil {
		return fmt.Errorf("repository %q mount: %w", id, err)
	}
	if err := ValidateRemoteName(repository.Clone.Remote); err != nil {
		return fmt.Errorf("repository %q clone remote: %w", id, err)
	}
	if err := ValidateCloneURL(repository.Clone.URL); err != nil {
		return fmt.Errorf("repository %q clone URL: %w", id, err)
	}
	if err := ValidateRemoteName(repository.Upstream.Remote); err != nil {
		return fmt.Errorf("repository %q upstream remote: %w", id, err)
	}
	if repository.Upstream.Remote != repository.Clone.Remote {
		return fmt.Errorf("repository %q upstream remote must equal clone remote", id)
	}
	if err := ValidateBranchName(repository.DefaultBranch); err != nil {
		return fmt.Errorf("repository %q default branch: %w", id, err)
	}
	if err := ValidateBranchName(repository.Upstream.Branch); err != nil {
		return fmt.Errorf("repository %q upstream branch: %w", id, err)
	}
	if repository.Upstream.Branch != repository.DefaultBranch {
		return fmt.Errorf("repository %q upstream branch must equal default branch", id)
	}
	if err := ValidateMergeRef(repository.Upstream.Merge); err != nil {
		return fmt.Errorf("repository %q upstream merge: %w", id, err)
	}
	return validateInitialCommits(repository.Identity.InitialCommits)
}

// ValidateManifestMetadata validates the local v2/v3 portable-manifest block.
func ValidateManifestMetadata(metadata ManifestMetadata) error {
	if metadata.IsZero() {
		return nil
	}
	if metadata.Path != "project.wtree.yml" {
		return fmt.Errorf("manifest path must be %q", "project.wtree.yml")
	}
	if metadata.Source == "" {
		return fmt.Errorf("manifest source is required when manifest metadata is present")
	}
	if err := ValidateManifestSource(metadata.Source); err != nil {
		return err
	}
	return nil
}

// ValidatePortableID accepts a stable, path-safe project or repository ID.
func ValidatePortableID(value string) error {
	if len(value) == 0 || len(value) > 128 {
		return fmt.Errorf("must be 1 to 128 characters")
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || (index > 0 && (character == '.' || character == '_' || character == '-'))) {
			return fmt.Errorf("must use only letters, digits, '.', '_' or '-' and must begin with a letter or digit")
		}
	}
	return nil
}

// ValidateRemoteName ensures a remote name is not a ref, option, or shell
// fragment. The v1 contract intentionally accepts the portable safe subset.
func ValidateRemoteName(value string) error {
	if err := ValidatePortableID(value); err != nil {
		return fmt.Errorf("invalid remote name: %w", err)
	}
	return nil
}

// ValidateBranchName checks the portable subset of Git branch syntax.
func ValidateBranchName(value string) error {
	if value == "" || value == "@" || len(value) > 255 || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") {
		return fmt.Errorf("invalid branch name")
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) || strings.ContainsRune(`~^:?*[\`, character) {
			return fmt.Errorf("invalid branch name")
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("invalid branch name")
		}
	}
	return nil
}

// ValidatePortableMount validates the manifest's cross-platform literal
// format without changing the more permissive runtime mount behavior.
func ValidatePortableMount(mount string, kind pathutil.MountKind) error {
	if kind == pathutil.TopLevelMount && mount == "." {
		return nil
	}
	if mount == "" {
		return fmt.Errorf("repository mount is required")
	}
	if strings.HasPrefix(mount, "/") || hasWindowsPathVolume(mount) {
		return fmt.Errorf("repository mount %q must be relative", mount)
	}
	if strings.Contains(mount, `\`) || strings.Contains(mount, "//") || strings.HasSuffix(mount, "/") || path.Clean(mount) != mount {
		return fmt.Errorf("repository mount %q must use canonical slash-separated components", mount)
	}
	components := strings.Split(mount, "/")
	for _, component := range components {
		if component == ".." {
			return fmt.Errorf("repository mount %q escapes its parent", mount)
		}
		if component == "" || component == "." {
			return fmt.Errorf("repository mount %q must not contain dot or empty components", mount)
		}
		if strings.EqualFold(component, ".git") {
			return fmt.Errorf("repository mount %q enters forbidden Git administration path", mount)
		}
		if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") || strings.ContainsAny(component, `<>:"|?*`) || strings.IndexFunc(component, unicode.IsControl) >= 0 || reservedPortableDeviceComponent(component) {
			return fmt.Errorf("repository mount %q has a platform-unsafe component", mount)
		}
	}
	return nil
}

// ValidateMergeRef validates a full remote branch ref.
func ValidateMergeRef(value string) error {
	if !strings.HasPrefix(value, "refs/heads/") {
		return fmt.Errorf("merge ref must begin with refs/heads/")
	}
	if err := ValidateBranchName(strings.TrimPrefix(value, "refs/heads/")); err != nil {
		return fmt.Errorf("invalid merge ref")
	}
	return nil
}

// ValidateCloneURL accepts the v1 clone transports without ever interpreting
// their contents as command-line arguments or shell text. Errors deliberately
// do not echo the input, so malformed credential-bearing URLs stay redacted.
func ValidateCloneURL(value string) error {
	if value == "" || len(value) > 4096 || containsControl(value) || strings.HasPrefix(value, "-") {
		return fmt.Errorf("invalid clone URL")
	}
	if hasURIScheme(value, "http") || hasURIScheme(value, "https") {
		return validateHTTPCloneURL(value)
	}
	if hasURIScheme(value, "ssh") {
		return validateSSHURL(value)
	}
	if hasURIScheme(value, "file") {
		if strings.HasPrefix(value[len("file"):], "://") {
			return validateFileURL(value)
		}
		return fmt.Errorf("invalid clone URL")
	}
	if isAbsoluteLocalPath(value) {
		if containsUnsafeShellFragment(value) {
			return fmt.Errorf("invalid clone URL")
		}
		return nil
	}
	if hierarchicalURIScheme(value) != "" {
		return fmt.Errorf("invalid clone URL")
	}
	if isSCPStyleURL(value) {
		return nil
	}
	return fmt.Errorf("invalid clone URL")
}

// ValidateManifestSource accepts only cleaned absolute local paths or
// credential-free HTTP(S) URLs, which are safe to persist in local metadata.
func ValidateManifestSource(value string) error {
	if value == "" || len(value) > 4096 || containsControl(value) {
		return fmt.Errorf("invalid manifest source")
	}
	if hasURIScheme(value, "http") || hasURIScheme(value, "https") {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery || containsControl(parsed.Host) || containsControl(parsed.Path) || containsUnsafeShellFragment(parsed.Path) {
			return fmt.Errorf("invalid manifest source")
		}
		return nil
	}
	if !isAbsoluteLocalPath(value) || filepath.Clean(value) != value {
		return fmt.Errorf("manifest source must be a cleaned absolute path or HTTP(S) URL")
	}
	return nil
}

// hasURIScheme matches a URI scheme case-insensitively. RFC 3986 defines URI
// schemes as case-insensitive, so supported transports must be classified
// before an input can be considered for scp-style parsing.
func hasURIScheme(value, scheme string) bool {
	return len(value) > len(scheme) && strings.EqualFold(value[:len(scheme)], scheme) && value[len(scheme)] == ':'
}

// hierarchicalURIScheme returns the scheme of a syntactically valid
// scheme:// URI. Callers use it to reject unsupported hierarchical transports
// before they can fall through to scp-style parsing.
func hierarchicalURIScheme(value string) string {
	colon := strings.IndexByte(value, ':')
	if colon < 1 || len(value) < colon+3 || value[colon+1:colon+3] != "//" {
		return ""
	}
	for index, character := range value[:colon] {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || (index > 0 && (character >= '0' && character <= '9' || character == '+' || character == '-' || character == '.'))) {
			return ""
		}
	}
	return value[:colon]
}

func validateHTTPCloneURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery || containsControl(parsed.Host) || containsControl(parsed.Path) || containsUnsafeShellFragment(parsed.Path) {
		return fmt.Errorf("invalid clone URL")
	}
	return nil
}

func validateSSHURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	_, hasPassword := "", false
	if parsed != nil && parsed.User != nil {
		_, hasPassword = parsed.User.Password()
	}
	if err != nil || parsed.Host == "" || parsed.Fragment != "" || parsed.RawQuery != "" || containsControl(parsed.Host) || containsControl(parsed.Path) || containsUnsafeShellFragment(parsed.Path) || hasPassword {
		return fmt.Errorf("invalid clone URL")
	}
	return nil
}

func validateFileURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.User != nil || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || containsControl(parsed.Host) || containsControl(parsed.Path) || containsUnsafeShellFragment(parsed.Path) || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid clone URL")
	}
	return nil
}

func isSCPStyleURL(value string) bool {
	colon := strings.LastIndexByte(value, ':')
	if colon <= 0 || colon == len(value)-1 {
		return false
	}
	prefix, remotePath := value[:colon], value[colon+1:]
	if strings.HasPrefix(remotePath, "-") || containsUnsafeShellFragment(remotePath) {
		return false
	}
	if at := strings.IndexByte(prefix, '@'); at >= 0 {
		return strings.Count(prefix, "@") == 1 && at > 0 && at < len(prefix)-1 && safeSSHAtom(prefix[:at]) && safeSSHAtom(prefix[at+1:])
	}
	return safeSSHAtom(prefix)
}

func safeSSHAtom(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune(".-_", character)) {
			return false
		}
	}
	return true
}

func isAbsoluteLocalPath(value string) bool {
	if filepath.IsAbs(value) || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func containsUnsafeShellFragment(value string) bool {
	return strings.ContainsAny(value, ";|$`<>") || strings.Contains(value, "&&")
}

func hasWindowsPathVolume(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':'
}

func reservedPortableDeviceComponent(component string) bool {
	base := strings.ToUpper(strings.Split(component, ".")[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func validateInitialCommits(commits []string) error {
	if len(commits) == 0 {
		return fmt.Errorf("initial commits must be non-empty")
	}
	for index, commit := range commits {
		if !isFullCommitID(commit) {
			return fmt.Errorf("initial commits must be full object IDs")
		}
		if index > 0 && commits[index-1] >= commit {
			return fmt.Errorf("initial commits must be sorted and unique")
		}
	}
	return nil
}

func isFullCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func (manifest PortableManifest) canonical() PortableManifest {
	canonical := manifest
	canonical.Repositories = make(map[string]PortableRepository, len(manifest.Repositories))
	for id, repository := range manifest.Repositories {
		repository.Identity.InitialCommits = append([]string(nil), repository.Identity.InitialCommits...)
		sort.Strings(repository.Identity.InitialCommits)
		canonical.Repositories[id] = repository
	}
	return canonical
}

func portableManifestYAML(manifest PortableManifest) yaml.Node {
	root := yaml.Node{Kind: yaml.MappingNode}
	root.Content = append(root.Content, scalarNode("version"), intNode(manifest.Version))
	project := yaml.Node{Kind: yaml.MappingNode}
	project.Content = append(project.Content, scalarNode("id"), scalarNode(manifest.Project.ID), scalarNode("name"), scalarNode(manifest.Project.Name), scalarNode("base_repository"), scalarNode(manifest.Project.BaseRepository))
	repositories := repositoriesYAML(manifest.Repositories)
	root.Content = append(root.Content, scalarNode("project"), &project, scalarNode("repositories"), &repositories)
	if manifest.Version == PortableManifestVersion3 {
		if len(manifest.Hooks) != 0 {
			hooks := hookEventsYAML(manifest.Hooks, manifest.Project.BaseRepository)
			root.Content = append(root.Content, scalarNode("hooks"), &hooks)
		}
		if len(manifest.SharedHooks) != 0 {
			sharedHooks := hookEventsYAML(manifest.SharedHooks, manifest.Project.BaseRepository)
			root.Content = append(root.Content, scalarNode("shared_hooks"), &sharedHooks)
		}
	}
	return root
}

func projectConfigV3YAML(value ProjectConfig) yaml.Node {
	root := yaml.Node{Kind: yaml.MappingNode}
	root.Content = append(root.Content, scalarNode("version"), intNode(value.Version))
	project := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("id"), scalarNode(value.Project.ID), scalarNode("name"), scalarNode(value.Project.Name), scalarNode("base_repository"), scalarNode(value.Project.BaseRepository)}}
	root.Content = append(root.Content, scalarNode("project"), &project, scalarNode("logical_root"), scalarNode(value.LogicalRoot))
	repositories := localRepositoriesYAML(value.Repositories)
	worktrees := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("root"), scalarNode(value.Worktrees.Root)}}
	discovery := yaml.Node{Kind: yaml.MappingNode}
	if len(value.Discovery.Ignore) != 0 {
		discovery.Content = append(discovery.Content, scalarNode("ignore"), stringsYAML(value.Discovery.Ignore))
	}
	manifest := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("path"), scalarNode(value.Manifest.Path), scalarNode("source"), scalarNode(value.Manifest.Source)}}
	root.Content = append(root.Content, scalarNode("repositories"), &repositories, scalarNode("worktrees"), &worktrees, scalarNode("discovery"), &discovery, scalarNode("manifest"), &manifest)
	if len(value.Hooks) != 0 {
		hooks := hookEventsYAML(value.Hooks, value.Project.BaseRepository)
		root.Content = append(root.Content, scalarNode("hooks"), &hooks)
	}
	return root
}

func localRepositoriesYAML(repositories map[string]Repository) yaml.Node {
	ids := make([]string, 0, len(repositories))
	for id := range repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	node := yaml.Node{Kind: yaml.MappingNode}
	for _, id := range ids {
		repository := repositories[id]
		value := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("source"), scalarNode(repository.Source), scalarNode("parent"), scalarNode(repository.Parent), scalarNode("mount"), scalarNode(repository.DefaultMount), scalarNode("default_branch"), scalarNode(repository.DefaultBranch)}}
		node.Content = append(node.Content, scalarNode(id), &value)
	}
	return node
}

func hookEventsYAML(events HookEvents, baseRepository string) yaml.Node {
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	node := yaml.Node{Kind: yaml.MappingNode}
	for _, name := range names {
		sequence := yaml.Node{Kind: yaml.SequenceNode}
		for _, hook := range events[name] {
			if hook.Repository == "" {
				hook.Repository = baseRepository
			}
			if hook.Timeout == 0 {
				hook.Timeout = HookDefaultTimeout
			}
			value := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("id"), scalarNode(hook.ID)}}
			value.Content = append(value.Content, scalarNode("repository"), scalarNode(hook.Repository))
			value.Content = append(value.Content, scalarNode("command"), stringsYAML(hook.Command))
			value.Content = append(value.Content, scalarNode("timeout"), scalarNode(hook.Timeout.String()))
			sequence.Content = append(sequence.Content, &value)
		}
		node.Content = append(node.Content, scalarNode(name), &sequence)
	}
	return node
}

func stringsYAML(values []string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode}
	for _, value := range values {
		node.Content = append(node.Content, scalarNode(value))
	}
	return node
}

func repositoriesYAML(repositories map[string]PortableRepository) yaml.Node {
	ids := make([]string, 0, len(repositories))
	for id := range repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	node := yaml.Node{Kind: yaml.MappingNode}
	for _, id := range ids {
		repository := repositories[id]
		clone := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("remote"), scalarNode(repository.Clone.Remote), scalarNode("url"), scalarNode(repository.Clone.URL)}}
		upstream := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("branch"), scalarNode(repository.Upstream.Branch), scalarNode("remote"), scalarNode(repository.Upstream.Remote), scalarNode("merge"), scalarNode(repository.Upstream.Merge)}}
		commits := yaml.Node{Kind: yaml.SequenceNode}
		for _, commit := range repository.Identity.InitialCommits {
			commits.Content = append(commits.Content, scalarNode(commit))
		}
		identity := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("initial_commits"), &commits}}
		value := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalarNode("clone"), &clone, scalarNode("upstream"), &upstream, scalarNode("identity"), &identity, scalarNode("parent"), scalarNode(repository.Parent), scalarNode("mount"), scalarNode(repository.Mount), scalarNode("default_branch"), scalarNode(repository.DefaultBranch)}}
		node.Content = append(node.Content, scalarNode(id), &value)
	}
	return node
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)}
}

func portableAncestor(repositories map[string]PortableRepository, ancestor, descendant string) bool {
	for parent := repositories[descendant].Parent; parent != ""; parent = repositories[parent].Parent {
		if parent == ancestor {
			return true
		}
	}
	return false
}

func portablePathsOverlap(left, right string) bool {
	return pathutil.CaseFoldedPathOverlap(left, right)
}
