package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	configuration "github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/lock"
)

const ConfigKeyWorktreesRoot = "worktrees.root"

type ConfigScope string

const (
	ConfigScopeGlobal  ConfigScope = "global"
	ConfigScopeProject ConfigScope = "project"
)

type ConfigRequest struct {
	Scope               ConfigScope
	Key                 string
	Value               string
	GlobalConfigPath    string
	ProjectConfigPath   string
	DefaultWorktreeRoot string
	Home                string
	// UpdateDataDir and ProjectID bind project-scoped mutations to the same
	// update-journal authority used by every other project mutator.
	UpdateDataDir string
	ProjectID     string
}

type ConfigValue struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// ConfigService owns scoped, locked configuration reads and updates.
type ConfigService struct{ locker ProjectLocker }

func NewConfigService() *ConfigService { return &ConfigService{locker: lock.Manager{}} }

func (s *ConfigService) Get(_ context.Context, request ConfigRequest) (ConfigValue, error) {
	if err := validateConfigRequest(request, false); err != nil {
		return ConfigValue{}, err
	}
	global, err := loadGlobalConfig(request.GlobalConfigPath)
	if err != nil {
		return ConfigValue{}, err
	}
	var project configuration.ProjectConfig
	if request.Scope == ConfigScopeProject {
		project, err = loadProjectConfig(request.ProjectConfigPath)
		if err != nil {
			return ConfigValue{}, err
		}
	}
	return effectiveConfigValue(request, project, global)
}

func (s *ConfigService) Set(ctx context.Context, request ConfigRequest) (ConfigValue, error) {
	if err := validateConfigRequest(request, true); err != nil {
		return ConfigValue{}, err
	}
	if err := validateConfigMutationAuthority(request); err != nil {
		return ConfigValue{}, err
	}
	if request.Scope == ConfigScopeProject {
		handle, err := acquireProjectMutationAuthority(ctx, s.locker, request.UpdateDataDir, request.ProjectID, time.Second)
		if err != nil {
			return ConfigValue{}, err
		}
		defer handle.Unlock()
		var result ConfigValue
		err = withConfigLock(ctx, request.ProjectConfigPath, func() error {
			project, err := loadProjectConfig(request.ProjectConfigPath)
			if err != nil {
				return err
			}
			global, err := loadGlobalConfig(request.GlobalConfigPath)
			if err != nil {
				return err
			}
			project.Worktrees.Root = request.Value
			result, err = effectiveConfigValue(request, project, global)
			if err != nil {
				return err
			}
			if err := configuration.WriteProjectFile(request.ProjectConfigPath, project); err != nil {
				return fmt.Errorf("write project configuration: %w", err)
			}
			return nil
		})
		return result, err
	}
	var result ConfigValue
	err := withConfigLock(ctx, request.GlobalConfigPath, func() error {
		global, err := loadGlobalConfig(request.GlobalConfigPath)
		if err != nil {
			return err
		}
		global.Worktrees.Root = request.Value
		if err := configuration.WriteGlobalFile(request.GlobalConfigPath, global); err != nil {
			return fmt.Errorf("write global configuration: %w", err)
		}
		result, err = effectiveConfigValue(request, configuration.ProjectConfig{}, global)
		return err
	})
	return result, err
}

func (s *ConfigService) Unset(ctx context.Context, request ConfigRequest) (ConfigValue, error) {
	if err := validateConfigRequest(request, false); err != nil {
		return ConfigValue{}, err
	}
	if err := validateConfigMutationAuthority(request); err != nil {
		return ConfigValue{}, err
	}
	request.Value = ""
	if request.Scope == ConfigScopeProject {
		handle, err := acquireProjectMutationAuthority(ctx, s.locker, request.UpdateDataDir, request.ProjectID, time.Second)
		if err != nil {
			return ConfigValue{}, err
		}
		defer handle.Unlock()
		var result ConfigValue
		err = withConfigLock(ctx, request.ProjectConfigPath, func() error {
			project, err := loadProjectConfig(request.ProjectConfigPath)
			if err != nil {
				return err
			}
			global, err := loadGlobalConfig(request.GlobalConfigPath)
			if err != nil {
				return err
			}
			project.Worktrees.Root = ""
			result, err = effectiveConfigValue(request, project, global)
			if err != nil {
				return err
			}
			if err := configuration.WriteProjectFile(request.ProjectConfigPath, project); err != nil {
				return fmt.Errorf("write project configuration: %w", err)
			}
			return nil
		})
		return result, err
	}
	var result ConfigValue
	err := withConfigLock(ctx, request.GlobalConfigPath, func() error {
		global, err := loadGlobalConfig(request.GlobalConfigPath)
		if err != nil {
			return err
		}
		global.Worktrees.Root = ""
		if err := configuration.WriteGlobalFile(request.GlobalConfigPath, global); err != nil {
			return fmt.Errorf("write global configuration: %w", err)
		}
		result, err = effectiveConfigValue(request, configuration.ProjectConfig{}, global)
		return err
	})
	return result, err
}

func (s *ConfigService) List(ctx context.Context, request ConfigRequest) ([]ConfigValue, error) {
	value, err := s.Get(ctx, request)
	if err != nil {
		return nil, err
	}
	return []ConfigValue{value}, nil
}

func validateConfigRequest(request ConfigRequest, requireValue bool) error {
	if request.Scope == "" {
		request.Scope = ConfigScopeGlobal
	}
	if request.Scope != ConfigScopeGlobal && request.Scope != ConfigScopeProject {
		return NewError(ErrorValidation, fmt.Errorf("unsupported configuration scope %q", request.Scope))
	}
	if request.Key != ConfigKeyWorktreesRoot {
		return NewError(ErrorValidation, fmt.Errorf("unsupported configuration key %q", request.Key))
	}
	if strings.TrimSpace(request.GlobalConfigPath) == "" {
		return NewError(ErrorInternal, errors.New("global configuration path is required"))
	}
	if request.Scope == ConfigScopeProject && strings.TrimSpace(request.ProjectConfigPath) == "" {
		return NewError(ErrorProjectNotFound, errors.New("project scope requires a discovered or explicit project"))
	}
	if requireValue && strings.TrimSpace(request.Value) == "" {
		return NewError(ErrorValidation, errors.New("configuration value must not be empty; use unset instead"))
	}
	return nil
}

func validateConfigMutationAuthority(request ConfigRequest) error {
	if request.Scope == ConfigScopeProject && (strings.TrimSpace(request.UpdateDataDir) == "" || !safeUpdateJournalID(request.ProjectID)) {
		return NewError(ErrorValidation, errors.New("project scope requires update journal authority"))
	}
	return nil
}

func loadGlobalConfig(path string) (configuration.GlobalConfig, error) {
	global, err := configuration.ReadGlobalFile(path)
	if os.IsNotExist(err) {
		return configuration.GlobalConfig{Version: configuration.GlobalConfigVersion}, nil
	}
	if err != nil {
		return configuration.GlobalConfig{}, NewError(ErrorValidation, fmt.Errorf("read global configuration: %w", err))
	}
	return global, nil
}

func loadProjectConfig(path string) (configuration.ProjectConfig, error) {
	project, err := configuration.ReadProjectFile(path)
	if os.IsNotExist(err) {
		return configuration.ProjectConfig{}, NewError(ErrorProjectNotFound, fmt.Errorf("project configuration %q is unavailable", path))
	}
	if err != nil {
		return configuration.ProjectConfig{}, NewError(ErrorValidation, fmt.Errorf("read project configuration: %w", err))
	}
	return project, nil
}

func effectiveConfigValue(request ConfigRequest, project configuration.ProjectConfig, global configuration.GlobalConfig) (ConfigValue, error) {
	value, err := configuration.ResolveWorktreeRoot("", project, global, request.DefaultWorktreeRoot, request.Home)
	if err != nil {
		return ConfigValue{}, NewError(ErrorValidation, err)
	}
	source := "default"
	if global.Worktrees.Root != "" {
		source = "global"
	}
	if request.Scope == ConfigScopeProject && project.Worktrees.Root != "" {
		source = "project"
	}
	return ConfigValue{Key: request.Key, Value: value, Source: source}, nil
}

func withConfigLock(ctx context.Context, path string, operation func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	handle, err := (lock.Manager{}).Acquire(ctx, path+".lock", time.Second)
	if err != nil {
		return fmt.Errorf("lock configuration: %w", err)
	}
	defer handle.Unlock()
	return operation()
}
