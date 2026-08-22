// Package store provides versioned JSON state files with atomic replacement.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/definebusiness/wtree/internal/fsutil"
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
	return WriteWorkspaceCAS(path, value, nil)
}
func WriteWorkspaceCAS(path string, value WorkspaceState, compare func() error) error {
	data, err := WorkspaceBytes(value)
	if err != nil {
		return err
	}
	return writeBytesCAS(path, data, compare)
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

// WriteRawCAS restores exact authoritative bytes only while compare still
// accepts the public target at the final atomic replacement boundary.
func WriteRawCAS(path string, data []byte, compare func() error) error {
	return writeBytesCAS(path, data, compare)
}
func ReadWorkspace(path string) (WorkspaceState, error) {
	var value WorkspaceState
	err := read(path, &value)
	if err == nil && value.Version != Version {
		err = fmt.Errorf("unsupported workspace version %d", value.Version)
	}
	return value, err
}

// DecodeWorkspace strictly decodes one already captured workspace generation.
func DecodeWorkspace(data []byte) (WorkspaceState, error) {
	var value WorkspaceState
	if err := decode(data, &value); err != nil {
		return WorkspaceState{}, err
	}
	if value.Version != Version {
		return WorkspaceState{}, fmt.Errorf("unsupported workspace version %d", value.Version)
	}
	return value, nil
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
	return WriteRegistryCAS(path, value, nil)
}
func WriteRegistryCAS(path string, value Registry, compare func() error) error {
	data, err := RegistryBytes(value)
	if err != nil {
		return err
	}
	return writeBytesCAS(path, data, compare)
}

// RegistryBytes returns the exact v1 JSON representation used by
// WriteRegistry. Transaction coordinators use it to distinguish their own
// attempted publication from a concurrent writer before restoring anything.
func RegistryBytes(value Registry) ([]byte, error) {
	if value.Version == 0 {
		value.Version = Version
	}
	if value.Version != Version {
		return nil, fmt.Errorf("unsupported registry version %d", value.Version)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
func ReadRegistry(path string) (Registry, error) {
	var value Registry
	err := read(path, &value)
	if err == nil && value.Version != Version {
		err = fmt.Errorf("unsupported registry version %d", value.Version)
	}
	return value, err
}

// DecodeRegistry strictly decodes one already captured registry generation.
// Read-only planners use it to keep the parsed facts and their byte digest
// from the same filesystem observation during concurrent changes.
func DecodeRegistry(data []byte) (Registry, error) {
	var value Registry
	if err := decode(data, &value); err != nil {
		return Registry{}, err
	}
	if value.Version != Version {
		return Registry{}, fmt.Errorf("unsupported registry version %d", value.Version)
	}
	return value, nil
}
func WriteRecovery(path string, value RecoveryRecord) error {
	return WriteRecoveryCAS(path, value, nil)
}
func WriteRecoveryCAS(path string, value RecoveryRecord, compare func() error) error {
	data, err := RecoveryBytes(value)
	if err != nil {
		return err
	}
	return writeBytesCAS(path, data, compare)
}

// RecoveryBytes returns the exact v1 JSON representation used by WriteRecovery.
func RecoveryBytes(value RecoveryRecord) ([]byte, error) {
	if value.Version == 0 {
		value.Version = Version
	}
	if value.Version != Version {
		return nil, fmt.Errorf("unsupported recovery version %d", value.Version)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
func ReadRecovery(path string) (RecoveryRecord, error) {
	var value RecoveryRecord
	err := read(path, &value)
	if err == nil && value.Version != Version {
		err = fmt.Errorf("unsupported recovery version %d", value.Version)
	}
	return value, err
}

// DecodeRecovery strictly decodes one already captured recovery generation.
func DecodeRecovery(data []byte) (RecoveryRecord, error) {
	var value RecoveryRecord
	if err := decode(data, &value); err != nil {
		return RecoveryRecord{}, err
	}
	if value.Version != Version {
		return RecoveryRecord{}, fmt.Errorf("unsupported recovery version %d", value.Version)
	}
	return value, nil
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
	return fsutil.WriteFileAtomicModeWithHook(path, data, 0o600, atomicHook)
}
func writeBytesCAS(path string, data []byte, compare func() error) error {
	return fsutil.WriteFileAtomicModeWithHook(path, data, 0o600, func(step string) error {
		if err := atomicHook(step); err != nil {
			return err
		}
		if step == "before-rename" && compare != nil {
			return compare()
		}
		return nil
	})
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
	return decode(data, value)
}

func decode(data []byte, value any) error {
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
