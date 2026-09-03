// Package config loads strict, versioned project and global configuration.
package config

import (
	"bytes"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/pathutil"
	"gopkg.in/yaml.v3"
)

const (
	// ProjectConfigVersion is the local, machine-specific project config wire
	// version. v1 is intentionally not translated: users must reinitialize.
	ProjectConfigVersion = 2
	// GlobalConfigVersion remains independent and unchanged.
	GlobalConfigVersion = 1
)

type Project struct {
	ID             string `yaml:"id" json:"id"`
	Name           string `yaml:"name" json:"name"`
	BaseRepository string `yaml:"base_repository" json:"base_repository"`
}
type Repository struct {
	Source        string `yaml:"source" json:"source"`
	Parent        string `yaml:"parent" json:"parent"`
	DefaultMount  string `yaml:"mount" json:"mount"`
	DefaultBranch string `yaml:"default_branch" json:"defaultBranch"`
}
type Worktrees struct {
	Root string `yaml:"root" json:"root"`
}
type Discovery struct {
	Ignore []string `yaml:"ignore,omitempty" json:"ignore,omitempty"`
}

// ManifestMetadata records the portable manifest associated with a local
// project configuration. Its zero value remains useful while services build a
// prospective v2 or v3 configuration before publishing the manifest source.
type ManifestMetadata struct {
	Path   string `yaml:"path" json:"path"`
	Source string `yaml:"source" json:"source"`
}

func (metadata ManifestMetadata) IsZero() bool {
	return metadata.Path == "" && metadata.Source == ""
}

type ProjectConfig struct {
	Version      int                   `yaml:"version" json:"version"`
	Project      Project               `yaml:"project" json:"project"`
	LogicalRoot  string                `yaml:"logical_root" json:"logical_root"`
	Repositories map[string]Repository `yaml:"repositories" json:"repositories"`
	Worktrees    Worktrees             `yaml:"worktrees" json:"worktrees"`
	Discovery    Discovery             `yaml:"discovery,omitempty" json:"discovery,omitempty"`
	Manifest     ManifestMetadata      `yaml:"manifest,omitempty" json:"manifest,omitempty"`
	Hooks        HookEvents            `yaml:"hooks,omitempty" json:"hooks,omitempty"`
}
type GlobalConfig struct {
	Version   int       `yaml:"version"`
	Worktrees Worktrees `yaml:"worktrees"`
}

func LoadProject(data []byte) (ProjectConfig, error) {
	version, err := localConfigVersion(data)
	if err != nil {
		return ProjectConfig{}, err
	}
	if version == 1 {
		return ProjectConfig{}, fmt.Errorf("local project config version 1 is unsupported; reinitialization is required")
	}
	if version != ProjectConfigVersion && version != ProjectConfigVersion3 {
		return ProjectConfig{}, fmt.Errorf("unsupported local project config version %d", version)
	}
	var value ProjectConfig
	if version == ProjectConfigVersion {
		var v2 projectConfigV2
		err = strictYAML(data, &v2)
		value = ProjectConfig{Version: v2.Version, Project: v2.Project, LogicalRoot: v2.LogicalRoot, Repositories: v2.Repositories, Worktrees: v2.Worktrees, Discovery: v2.Discovery, Manifest: v2.Manifest}
	} else {
		err = strictYAML(data, &value)
	}
	if err != nil {
		return ProjectConfig{}, err
	}
	if version == ProjectConfigVersion3 {
		if err := validateExplicitHookTimeouts(data); err != nil {
			return ProjectConfig{}, err
		}
	}
	if err := requireLocalFields(data); err != nil {
		return ProjectConfig{}, err
	}
	if err := value.Validate(); err != nil {
		return ProjectConfig{}, err
	}
	return value, nil
}

// projectConfigV2 intentionally omits Hooks. Strict decoding through this
// wire type preserves v2's rejection of the otherwise known v3 field.
type projectConfigV2 struct {
	Version      int                   `yaml:"version" json:"version"`
	Project      Project               `yaml:"project" json:"project"`
	LogicalRoot  string                `yaml:"logical_root" json:"logical_root"`
	Repositories map[string]Repository `yaml:"repositories" json:"repositories"`
	Worktrees    Worktrees             `yaml:"worktrees" json:"worktrees"`
	Discovery    Discovery             `yaml:"discovery,omitempty" json:"discovery,omitempty"`
	Manifest     ManifestMetadata      `yaml:"manifest,omitempty" json:"manifest,omitempty"`
}

func localConfigVersion(data []byte) (int, error) {
	var document struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return 0, err
	}
	return document.Version, nil
}

func requireLocalFields(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if len(document.Content) == 0 {
		return fmt.Errorf("local project config is required")
	}
	root := document.Content[0]
	for _, field := range []string{"version", "project", "logical_root", "repositories", "worktrees", "manifest"} {
		if mappingValue(root, field) == nil {
			return fmt.Errorf("local project config field %q is required", field)
		}
	}
	project := mappingValue(root, "project")
	for _, field := range []string{"id", "name", "base_repository"} {
		if mappingValue(project, field) == nil {
			return fmt.Errorf("local project project field %q is required", field)
		}
	}
	manifest := mappingValue(root, "manifest")
	for _, field := range []string{"path", "source"} {
		if mappingValue(manifest, field) == nil {
			return fmt.Errorf("local project manifest field %q is required", field)
		}
	}
	repositories := mappingValue(root, "repositories")
	if repositories.Kind != yaml.MappingNode {
		return fmt.Errorf("local project repositories must be a mapping")
	}
	for index := 0; index+1 < len(repositories.Content); index += 2 {
		id, repository := repositories.Content[index].Value, repositories.Content[index+1]
		for _, field := range []string{"source", "parent", "mount", "default_branch"} {
			if mappingValue(repository, field) == nil {
				return fmt.Errorf("local project repository %q field %q is required", id, field)
			}
		}
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}
func LoadGlobal(data []byte) (GlobalConfig, error) {
	var value GlobalConfig
	if err := strictYAML(data, &value); err != nil {
		return GlobalConfig{}, err
	}
	if value.Version != GlobalConfigVersion {
		return GlobalConfig{}, fmt.Errorf("unsupported global config version %d", value.Version)
	}
	return value, nil
}

// Validate checks all local v2 facts that do not depend on the checkout's
// actual filesystem placement. Placement and canonical inversion are checked
// by the service loader, which has the base configuration path.
func (value ProjectConfig) Validate() error {
	if value.Version != ProjectConfigVersion && value.Version != ProjectConfigVersion3 {
		return fmt.Errorf("unsupported local project config version %d", value.Version)
	}
	if err := ValidatePortableID(value.Project.ID); err != nil {
		return fmt.Errorf("project ID: %w", err)
	}
	if value.Project.Name == "" || containsControl(value.Project.Name) {
		return fmt.Errorf("project name is required and must not contain control characters")
	}
	if err := ValidatePortableID(value.Project.BaseRepository); err != nil {
		return fmt.Errorf("project base repository: %w", err)
	}
	if err := ValidateLogicalRoot(value.LogicalRoot); err != nil {
		return err
	}
	if len(value.Repositories) == 0 {
		return fmt.Errorf("project repositories are required")
	}
	if err := ValidateManifestMetadata(value.Manifest); err != nil {
		return err
	}
	if value.Version == ProjectConfigVersion && len(value.Hooks) != 0 {
		return fmt.Errorf("local project config version %d does not support hooks", ProjectConfigVersion)
	}
	repositories := make([]domain.Repository, 0, len(value.Repositories))
	for id, repository := range value.Repositories {
		if err := ValidatePortableID(id); err != nil {
			return fmt.Errorf("repository ID %q: %w", id, err)
		}
		if err := ValidateLocalSource(repository.Source); err != nil {
			return fmt.Errorf("repository %q source: %w", id, err)
		}
		if repository.Parent != "" {
			if err := ValidatePortableID(repository.Parent); err != nil {
				return fmt.Errorf("repository %q parent: %w", id, err)
			}
		}
		if err := ValidateBranchName(repository.DefaultBranch); err != nil {
			return fmt.Errorf("repository %q default branch: %w", id, err)
		}
		repositories = append(repositories, domain.Repository{ID: id, ParentID: repository.Parent, DefaultMount: repository.DefaultMount, DefaultBranch: repository.DefaultBranch})
	}
	project := domain.Project{Version: domain.CurrentVersion, ID: value.Project.ID, Name: value.Project.Name, BaseRepository: value.Project.BaseRepository, Repositories: repositories}
	if err := project.Validate(); err != nil {
		return err
	}
	if value.Version == ProjectConfigVersion3 {
		if err := validateHookEventsForRepositories(value.Hooks, value.Project.BaseRepository, value.Repositories, hookSourceLocal); err != nil {
			return err
		}
	}
	return nil
}

func validateHookEventsForRepositories(events HookEvents, baseRepository string, repositories map[string]Repository, source hookSource) error {
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

// ValidateLogicalRoot accepts a clean relative path from the base config
// directory back to the logical root. It may contain leading `..` because a
// non-root base checkout necessarily points upward to its logical root.
func ValidateLogicalRoot(value string) error {
	if value == "" {
		return fmt.Errorf("logical root is required")
	}
	if strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || hasWindowsPathVolume(value) {
		return fmt.Errorf("logical root %q must be relative", value)
	}
	if path.Clean(value) != value {
		return fmt.Errorf("logical root %q must be clean and canonical", value)
	}
	return nil
}

// ValidateLocalSource accepts only clean slash-separated paths relative to
// the logical project root. `.` preserves one-root compatibility.
func ValidateLocalSource(value string) error {
	if _, err := pathutil.NormalizeMount(value, pathutil.TopLevelMount); err != nil {
		return err
	}
	return nil
}
func strictYAML(data []byte, value any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("multiple YAML documents are not supported")
	} else if err.Error() != "EOF" {
		return err
	}
	return nil
}

// EffectiveWorktreeRoot resolves CLI > project > global > default and expands a leading ~ using home.
func EffectiveWorktreeRoot(cli, project, global, fallback, home string) (string, error) {
	for _, value := range []string{cli, project, global, fallback} {
		if value != "" {
			return expandHome(value, home)
		}
	}
	return "", fmt.Errorf("worktree root is not configured")
}
func ResolveWorktreeRoot(cli string, project ProjectConfig, global GlobalConfig, fallback, home string) (string, error) {
	return EffectiveWorktreeRoot(cli, project.Worktrees.Root, global.Worktrees.Root, fallback, home)
}
func expandHome(value, home string) (string, error) {
	if value == "~" {
		return home, nil
	}
	if strings.HasPrefix(value, "~/") {
		if home == "" {
			return "", fmt.Errorf("cannot expand home path")
		}
		return filepath.Join(home, value[2:]), nil
	}
	return value, nil
}
