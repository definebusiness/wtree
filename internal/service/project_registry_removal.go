package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
)

// RegistryRemovalRetained reports the durable artifacts deliberately left in
// place by a registry-only removal. These fields are always present in JSON.
type RegistryRemovalRetained struct {
	ProjectConfig  bool `json:"projectConfig"`
	WorkspaceState bool `json:"workspaceState"`
	RecoveryData   bool `json:"recoveryData"`
	LockFile       bool `json:"lockFile"`
}

// ProjectRegistryRemovalPlan is the shared, read-only plan/result model for
// prune and unregister registry operations.
type ProjectRegistryRemovalPlan struct {
	Operation                string                  `json:"operation"`
	ProjectID                string                  `json:"projectId"`
	Name                     string                  `json:"name"`
	ConfigPath               string                  `json:"configPath"`
	Reasons                  []string                `json:"reasons"`
	Retained                 RegistryRemovalRetained `json:"retained"`
	LocalConfigMayReregister bool                    `json:"localConfigMayReregister"`

	// registered is not serialized. It makes execution reject any exact target
	// entry change, including repository identities that are not plan output.
	registered store.RegistryProject
}

type registryRemovalLocker interface {
	RegistryLock(context.Context, string, time.Duration) (lock.Handle, error)
	ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error)
}

// ProjectRegistryRemovalService owns removal policy; store continues to own
// registry serialization and atomic replacement.
type ProjectRegistryRemovalService struct {
	locker        registryRemovalLocker
	writeRegistry func(string, store.Registry) error
	lockTimeout   time.Duration
}

func NewProjectRegistryRemovalService() *ProjectRegistryRemovalService {
	return NewProjectRegistryRemovalServiceWith(lock.Manager{}, store.WriteRegistry)
}

// NewProjectRegistryRemovalServiceWith is deliberately narrow so tests can
// observe locking and write failure boundaries without changing store policy.
func NewProjectRegistryRemovalServiceWith(locker registryRemovalLocker, writeRegistry func(string, store.Registry) error) *ProjectRegistryRemovalService {
	return &ProjectRegistryRemovalService{locker: locker, writeRegistry: writeRegistry, lockTimeout: time.Second}
}

// PlanPrune validates one exact registry key and returns its complete,
// read-only removal plan. It creates no lock paths and changes no files.
func (s *ProjectRegistryRemovalService) PlanPrune(ctx context.Context, dataDir, projectID string) (ProjectRegistryRemovalPlan, error) {
	if err := validateRegistryRemovalID(projectID); err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	registry, err := readRemovalRegistry(dataDir)
	if err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	return s.planPruneFromRegistry(ctx, dataDir, projectID, registry)
}

// PlanUnregister validates one exact registry key and returns a read-only plan
// for intentional registration removal. Unlike prune, inconsistent entries are
// eligible; unresolved recovery metadata still blocks all removal.
func (s *ProjectRegistryRemovalService) PlanUnregister(ctx context.Context, dataDir, projectID string) (ProjectRegistryRemovalPlan, error) {
	if err := validateRegistryRemovalID(projectID); err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	registry, err := readRemovalRegistry(dataDir)
	if err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	return s.planUnregisterFromRegistry(ctx, dataDir, projectID, registry)
}

// Prune acquires registry then project exclusion, repeats planning from the
// current persisted registry, and atomically removes exactly the selected key.
func (s *ProjectRegistryRemovalService) Prune(ctx context.Context, dataDir string, expected ProjectRegistryRemovalPlan) (ProjectRegistryRemovalPlan, error) {
	return s.remove(ctx, dataDir, expected, "prune", s.planPruneFromRegistry)
}

// Unregister uses the same locked exact-key mutation boundary as Prune. It
// removes only the registry entry; retained local config can register again
// when a later mutating project command reconciles it.
func (s *ProjectRegistryRemovalService) Unregister(ctx context.Context, dataDir string, expected ProjectRegistryRemovalPlan) (ProjectRegistryRemovalPlan, error) {
	return s.remove(ctx, dataDir, expected, "unregister", s.planUnregisterFromRegistry)
}

type registryRemovalPlanner func(context.Context, string, string, store.Registry) (ProjectRegistryRemovalPlan, error)

func (s *ProjectRegistryRemovalService) remove(ctx context.Context, dataDir string, expected ProjectRegistryRemovalPlan, operation string, planner registryRemovalPlanner) (ProjectRegistryRemovalPlan, error) {
	if s == nil || s.locker == nil || s.writeRegistry == nil {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorInternal, errors.New("project registry removal is not configured"))
	}
	if expected.Operation != operation {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorInvalidArguments, fmt.Errorf("expected %s removal plan", operation))
	}
	if err := validateRegistryRemovalID(expected.ProjectID); err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	timeout := s.lockTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	registryLock, err := s.locker.RegistryLock(ctx, dataDir, timeout)
	if err != nil {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorConflict, fmt.Errorf("acquire registry mutation lock: %w", err))
	}
	defer registryLock.Unlock()
	projectLock, err := s.locker.ProjectLock(ctx, dataDir, expected.ProjectID, timeout)
	if err != nil {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorConflict, fmt.Errorf("acquire project mutation lock: %w", err))
	}
	defer projectLock.Unlock()

	registry, err := readRemovalRegistry(dataDir)
	if err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	actual, err := planner(ctx, dataDir, expected.ProjectID, registry)
	if err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	if !reflect.DeepEqual(actual, expected) {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorConflict, fmt.Errorf("project registry entry or %s eligibility changed before removal", operation))
	}
	delete(registry.Projects, expected.ProjectID)
	if err := s.writeRegistry(filepath.Join(dataDir, "registry.json"), registry); err != nil {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorInternal, fmt.Errorf("remove project registry entry: %w", err))
	}
	return actual, nil
}

func (s *ProjectRegistryRemovalService) planPruneFromRegistry(ctx context.Context, dataDir, projectID string, registry store.Registry) (ProjectRegistryRemovalPlan, error) {
	registered, exists := registry.Projects[projectID]
	if !exists {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorProjectNotFound, fmt.Errorf("project registration %q was not found", projectID))
	}
	report, err := NewProjectInventoryService().Inventory(ctx, dataDir)
	if err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	var entry *ProjectInventoryEntry
	for index := range report.Projects {
		if report.Projects[index].ID == projectID {
			entry = &report.Projects[index]
			break
		}
	}
	if entry == nil {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorConflict, fmt.Errorf("project registration %q changed during planning", projectID))
	}
	if hasInventoryFinding(*entry, "recovery-record") {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorConflict, errors.New("unresolved recovery metadata blocks registry removal"))
	}
	if entry.prunePolicy.ambiguousDuplicatePath {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorValidation, errors.New("project registration has an ambiguous duplicate configuration path and cannot be selected for prune"))
	}
	reasons := pruneReasons(*entry)
	if !entry.Prunable || len(reasons) == 0 {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorValidation, errors.New("project registration is not objectively prunable; use wtree project unregister <id> for intentional removal"))
	}
	return ProjectRegistryRemovalPlan{
		Operation: "prune", ProjectID: projectID, Name: registered.Name, ConfigPath: registered.ConfigPath,
		Reasons:                  reasons,
		Retained:                 retainedRegistryRemovalArtifacts(),
		LocalConfigMayReregister: true,
		registered:               registered,
	}, nil
}

func (s *ProjectRegistryRemovalService) planUnregisterFromRegistry(ctx context.Context, dataDir, projectID string, registry store.Registry) (ProjectRegistryRemovalPlan, error) {
	registered, exists := registry.Projects[projectID]
	if !exists {
		return ProjectRegistryRemovalPlan{}, NewError(ErrorProjectNotFound, fmt.Errorf("project registration %q was not found", projectID))
	}
	report, err := NewProjectInventoryService().Inventory(ctx, dataDir)
	if err != nil {
		return ProjectRegistryRemovalPlan{}, err
	}
	for _, entry := range report.Projects {
		if entry.ID != projectID {
			continue
		}
		if hasInventoryFinding(entry, "recovery-record") {
			return ProjectRegistryRemovalPlan{}, NewError(ErrorConflict, errors.New("unresolved recovery metadata blocks registry removal"))
		}
		return ProjectRegistryRemovalPlan{
			Operation:                "unregister",
			ProjectID:                projectID,
			Name:                     registered.Name,
			ConfigPath:               registered.ConfigPath,
			Reasons:                  []string{"intentional-unregister"},
			Retained:                 retainedRegistryRemovalArtifacts(),
			LocalConfigMayReregister: true,
			registered:               registered,
		}, nil
	}
	return ProjectRegistryRemovalPlan{}, NewError(ErrorConflict, fmt.Errorf("project registration %q changed during planning", projectID))
}

func retainedRegistryRemovalArtifacts() RegistryRemovalRetained {
	return RegistryRemovalRetained{ProjectConfig: true, WorkspaceState: true, RecoveryData: true, LockFile: true}
}

func readRemovalRegistry(dataDir string) (store.Registry, error) {
	registry, err := store.ReadRegistry(filepath.Join(dataDir, "registry.json"))
	if os.IsNotExist(err) {
		return store.Registry{Version: store.Version, Projects: map[string]store.RegistryProject{}}, nil
	}
	if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return store.Registry{}, NewError(ErrorInternal, fmt.Errorf("read project registry: %w", err))
		}
		return store.Registry{}, NewError(ErrorValidation, fmt.Errorf("read project registry: %w", err))
	}
	if registry.Projects == nil {
		registry.Projects = map[string]store.RegistryProject{}
	}
	return registry, nil
}

func validateRegistryRemovalID(projectID string) error {
	if projectID == "" || projectID == "." || projectID == ".." || filepath.IsAbs(projectID) || filepath.Base(projectID) != projectID || filepath.Clean(projectID) != projectID || strings.ContainsAny(projectID, "/\\\x00") {
		return NewError(ErrorInvalidArguments, fmt.Errorf("unsafe project ID %q", projectID))
	}
	return nil
}

func hasInventoryFinding(entry ProjectInventoryEntry, code string) bool {
	for _, finding := range entry.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func pruneReasons(entry ProjectInventoryEntry) []string {
	allowed := map[string]bool{"missing-config": true, "unreadable-config": true, "invalid-config": true, "config-id-mismatch": true}
	reasons := make([]string, 0, 1)
	for _, finding := range entry.Findings {
		if allowed[finding.Code] || (finding.Code == "duplicate-config-path" && entry.prunePolicy.supersededDuplicate) {
			reasons = append(reasons, finding.Code)
		}
	}
	sort.Strings(reasons)
	return reasons
}
