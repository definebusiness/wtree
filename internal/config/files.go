package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marcel/wtree/internal/fsutil"
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
		value.Version = Version
	}
	if value.Version != Version {
		return fmt.Errorf("unsupported global config version %d", value.Version)
	}
	return writeYAML(path, value)
}

func WriteProjectFile(path string, value ProjectConfig) error {
	if value.Version != Version {
		return fmt.Errorf("unsupported project config version %d", value.Version)
	}
	return writeYAML(path, value)
}

func writeYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := fsutil.Sync(temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := configAtomicHook("before-rename"); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	if err := configAtomicHook("dir-sync"); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return fsutil.Sync(directoryHandle)
}

func configAtomicHook(step string) error {
	if configAtomicStepHook != nil {
		return configAtomicStepHook(step)
	}
	return nil
}
