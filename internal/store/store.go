// Package store provides versioned JSON state files with atomic replacement.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/marcel/wtree/internal/fsutil"
)

const Version = 1

var atomicStepHook func(string) error

type Registry struct {
	Version  int                        `json:"version"`
	Projects map[string]RegistryProject `json:"projects"`
}
type RegistryProject struct {
	Name          string            `json:"name"`
	ConfigPath    string            `json:"configPath"`
	RepositoryIDs map[string]string `json:"repositoryIds"`
}
type WorkspaceState struct {
	Version              int                      `json:"version"`
	ID                   string                   `json:"id"`
	Name                 string                   `json:"name"`
	Path                 string                   `json:"path"`
	Partial              bool                     `json:"partial,omitempty"`
	MissingRepositoryIDs []string                 `json:"missingRepositoryIds,omitempty"`
	Repositories         map[string]CheckoutState `json:"repositories"`
}
type CheckoutState struct {
	Branch       string `json:"branch"`
	Mount        string `json:"mount"`
	ResolvedPath string `json:"resolvedPath"`
	Head         string `json:"head,omitempty"`
	Detached     bool   `json:"detached,omitempty"`
}
type RecoveryRecord struct {
	Version          int               `json:"version"`
	ProjectID        string            `json:"projectId"`
	WorkspaceID      string            `json:"workspaceId"`
	Operation        string            `json:"operation"`
	FailedStep       string            `json:"failedStep"`
	CompletedSteps   []string          `json:"completedSteps"`
	UnrevertedSteps  []string          `json:"unrevertedSteps,omitempty"`
	RollbackFailures []RollbackFailure `json:"rollbackFailures,omitempty"`
}
type RollbackFailure struct {
	Step  string `json:"step"`
	Error string `json:"error"`
}

func WriteWorkspace(path string, value WorkspaceState) error {
	data, err := WorkspaceBytes(value)
	if err != nil {
		return err
	}
	return writeBytes(path, data)
}

// WorkspaceBytes returns the exact v1 JSON representation used by
// WriteWorkspace. Transactions use it to identify state published by their
// own failed commit attempt without deleting unrelated state.
func WorkspaceBytes(value WorkspaceState) ([]byte, error) {
	if value.Version == 0 {
		value.Version = Version
	}
	if value.Version != Version {
		return nil, fmt.Errorf("unsupported workspace version %d", value.Version)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// WriteRawAtomic replaces path atomically with exact data. It is intentionally
// narrow: callers restoring a previously read authoritative file retain every
// byte rather than reserializing it.
func WriteRawAtomic(path string, data []byte) error { return writeBytes(path, data) }
func ReadWorkspace(path string) (WorkspaceState, error) {
	var value WorkspaceState
	err := read(path, &value)
	if err == nil && value.Version != Version {
		err = fmt.Errorf("unsupported workspace version %d", value.Version)
	}
	return value, err
}

// MigrateWorkspace is the explicit migration boundary. Version one currently needs no rewrite.
func MigrateWorkspace(value WorkspaceState) (WorkspaceState, error) {
	if value.Version != Version {
		return WorkspaceState{}, fmt.Errorf("unsupported workspace version %d", value.Version)
	}
	return value, nil
}
func MigrateRegistry(value Registry) (Registry, error) {
	if value.Version != Version {
		return Registry{}, fmt.Errorf("unsupported registry version %d", value.Version)
	}
	return value, nil
}
func MigrateRecovery(value RecoveryRecord) (RecoveryRecord, error) {
	if value.Version != Version {
		return RecoveryRecord{}, fmt.Errorf("unsupported recovery version %d", value.Version)
	}
	return value, nil
}
func WriteRegistry(path string, value Registry) error {
	if value.Version == 0 {
		value.Version = Version
	}
	if value.Version != Version {
		return fmt.Errorf("unsupported registry version %d", value.Version)
	}
	return write(path, value)
}
func ReadRegistry(path string) (Registry, error) {
	var value Registry
	err := read(path, &value)
	if err == nil && value.Version != Version {
		err = fmt.Errorf("unsupported registry version %d", value.Version)
	}
	return value, err
}
func WriteRecovery(path string, value RecoveryRecord) error {
	if value.Version == 0 {
		value.Version = Version
	}
	if value.Version != Version {
		return fmt.Errorf("unsupported recovery version %d", value.Version)
	}
	return write(path, value)
}
func ReadRecovery(path string) (RecoveryRecord, error) {
	var value RecoveryRecord
	err := read(path, &value)
	if err == nil && value.Version != Version {
		err = fmt.Errorf("unsupported recovery version %d", value.Version)
	}
	return value, err
}

func write(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeBytes(path, data)
}

func writeBytes(path string, data []byte) error {
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
	if err := atomicHook("write"); err != nil {
		temporary.Close()
		return err
	}
	if err := fsutil.Sync(temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := atomicHook("sync"); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := atomicHook("close"); err != nil {
		return err
	}
	if err := atomicHook("before-rename"); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	if err := atomicHook("dir-sync"); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return fsutil.Sync(directoryHandle)
}
func atomicHook(step string) error {
	if atomicStepHook != nil {
		return atomicStepHook(step)
	}
	return nil
}
func read(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("trailing JSON value")
	} else if err != io.EOF {
		return err
	}
	return nil
}
