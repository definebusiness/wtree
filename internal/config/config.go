// Package config loads strict, versioned project and global configuration.
package config

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const Version = 1

type Project struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}
type Repository struct {
	Source        string `yaml:"source"`
	Parent        string `yaml:"parent"`
	DefaultMount  string `yaml:"mount"`
	DefaultBranch string `yaml:"default_branch"`
}
type Worktrees struct {
	Root string `yaml:"root"`
}
type Discovery struct {
	Ignore []string `yaml:"ignore,omitempty"`
}
type ProjectConfig struct {
	Version      int                   `yaml:"version"`
	Project      Project               `yaml:"project"`
	Repositories map[string]Repository `yaml:"repositories"`
	Worktrees    Worktrees             `yaml:"worktrees"`
	Discovery    Discovery             `yaml:"discovery,omitempty"`
}
type GlobalConfig struct {
	Version   int       `yaml:"version"`
	Worktrees Worktrees `yaml:"worktrees"`
}

func LoadProject(data []byte) (ProjectConfig, error) {
	var config ProjectConfig
	if err := strictYAML(data, &config); err != nil {
		return ProjectConfig{}, err
	}
	if config.Version != Version {
		return ProjectConfig{}, fmt.Errorf("unsupported project config version %d", config.Version)
	}
	return config, nil
}
func LoadGlobal(data []byte) (GlobalConfig, error) {
	var config GlobalConfig
	if err := strictYAML(data, &config); err != nil {
		return GlobalConfig{}, err
	}
	if config.Version != Version {
		return GlobalConfig{}, fmt.Errorf("unsupported global config version %d", config.Version)
	}
	return config, nil
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
