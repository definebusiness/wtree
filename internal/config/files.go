package config

import (
	"fmt"
	"os"

	"github.com/definebusiness/wtree/internal/fsutil"
	"gopkg.in/yaml.v3"
)

var configAtomicStepHook func(string) error

// ReadGlobalFile loads a global configuration file with strict version checks.
func ReadGlobalFile(path string) (GlobalConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return GlobalConfig{}, err
	}
	return LoadGlobal(data)
}

// ReadProjectFile loads a project configuration file with strict version checks.
func ReadProjectFile(path string) (ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectConfig{}, err
	}
	return LoadProject(data)
}

func WriteGlobalFile(path string, value GlobalConfig) error {
	if value.Version == 0 {
		value.Version = GlobalConfigVersion
	}
	if value.Version != GlobalConfigVersion {
		return fmt.Errorf("unsupported global config version %d", value.Version)
	}
	return writeYAML(path, value)
}

func WriteProjectFile(path string, value ProjectConfig) error {
	if value.Version != ProjectConfigVersion && value.Version != ProjectConfigVersion3 {
		return fmt.Errorf("unsupported project config version %d", value.Version)
	}
	if err := value.Validate(); err != nil {
		return err
	}
	data, err := MarshalProject(value)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomicModeWithHook(path, data, 0o600, configAtomicHook)
}

func writeYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomicModeWithHook(path, data, 0o600, configAtomicHook)
}

func configAtomicHook(step string) error {
	if configAtomicStepHook != nil {
		return configAtomicStepHook(step)
	}
	return nil
}
