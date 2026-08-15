package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/store"
)

// ProjectInventoryReport is the read-only global project registry view.
type ProjectInventoryReport struct {
	Projects []ProjectInventoryEntry `json:"projects"`
}

// ProjectInventoryEntry is one registered project and its observed health.
type ProjectInventoryEntry struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name"`
	ConfigPath  string                    `json:"configPath"`
	Status      string                    `json:"status"`
	Prunable    bool                      `json:"prunable"`
	Findings    []ProjectInventoryFinding `json:"findings"`
	prunePolicy inventoryPrunePolicy
}

// inventoryPrunePolicy carries authorization facts separately from the
// human-facing finding text. It is intentionally runtime-only.
type inventoryPrunePolicy struct {
	staleEvidence          bool
	supersededDuplicate    bool
	ambiguousDuplicatePath bool
	recovery               bool
}

// ProjectInventoryFinding is a stable diagnostic for one registered project.
type ProjectInventoryFinding struct {
	Code              string   `json:"code"`
	Severity          string   `json:"severity"`
	Message           string   `json:"message"`
	RelatedProjectIDs []string `json:"relatedProjectIds"`
}

// ProjectInventoryService inspects persisted registry state without resolving
// projects, invoking Git, locking, reconciling, or writing files.
type ProjectInventoryService struct{}

func NewProjectInventoryService() *ProjectInventoryService { return &ProjectInventoryService{} }

func (s *ProjectInventoryService) Inventory(_ context.Context, dataDir string) (ProjectInventoryReport, error) {
	registry, err := store.ReadRegistry(filepath.Join(dataDir, "registry.json"))
	if os.IsNotExist(err) {
		return ProjectInventoryReport{Projects: []ProjectInventoryEntry{}}, nil
	}
	if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return ProjectInventoryReport{}, NewError(ErrorInternal, fmt.Errorf("read project registry: %w", err))
		}
		return ProjectInventoryReport{}, NewError(ErrorValidation, fmt.Errorf("read project registry: %w", err))
	}

	ids := make([]string, 0, len(registry.Projects))
	for id := range registry.Projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	report := ProjectInventoryReport{Projects: make([]ProjectInventoryEntry, 0, len(ids))}
	observed := make(map[string]inventoryObservation, len(ids))
	paths := map[string][]string{}
	identities := map[string][]string{}
	for _, id := range ids {
		registered := registry.Projects[id]
		entry := ProjectInventoryEntry{ID: id, Name: registered.Name, ConfigPath: registered.ConfigPath, Findings: []ProjectInventoryFinding{}}
		observation := inspectInventoryEntry(dataDir, id, registered, &entry)
		observed[id] = observation
		paths[CanonicalRegistrationConfigPath(registered.ConfigPath)] = append(paths[CanonicalRegistrationConfigPath(registered.ConfigPath)], id)
		for identity := range registered.RepositoryIDs {
			identities[identity] = append(identities[identity], id)
		}
		report.Projects = append(report.Projects, entry)
	}
	byID := make(map[string]*ProjectInventoryEntry, len(report.Projects))
	for i := range report.Projects {
		byID[report.Projects[i].ID] = &report.Projects[i]
	}
	for _, group := range paths {
		diagnoseDuplicatePath(group, observed, byID)
	}
	for identity, group := range identities {
		if len(group) > 1 {
			diagnoseDuplicateIdentity(identity, group, byID)
		}
	}
	for i := range report.Projects {
		finalizeInventoryEntry(&report.Projects[i])
	}
	return report, nil
}

type inventoryObservation struct {
	declaredID string
	readable   bool
}

func inspectInventoryEntry(dataDir, id string, registered store.RegistryProject, entry *ProjectInventoryEntry) inventoryObservation {
	observation := inventoryObservation{}
	data, err := os.ReadFile(registered.ConfigPath)
	if os.IsNotExist(err) {
		addStaleInventoryFinding(entry, "missing-config", "registered project configuration is missing", nil)
	} else if err != nil {
		addStaleInventoryFinding(entry, "unreadable-config", "registered project configuration cannot be read", nil)
	} else {
		configuration, configErr := config.LoadProject(data)
		if configErr != nil {
			addStaleInventoryFinding(entry, "invalid-config", "registered project configuration is invalid", nil)
		} else {
			observation.readable, observation.declaredID = true, configuration.Project.ID
			if configuration.Project.ID != id {
				addStaleInventoryFinding(entry, "config-id-mismatch", "registered project ID does not match project configuration", []string{configuration.Project.ID})
			}
		}
	}
	state, stateErr := store.ReadWorkspace(WorkspaceStatePath(dataDir, id, "default"))
	if os.IsNotExist(stateErr) {
		addInventoryFinding(entry, "missing-default-state", "warning", "default workspace state is missing", nil)
	} else if stateErr != nil || state.ID != "default" || state.Name != "default" {
		addInventoryFinding(entry, "invalid-default-state", "warning", "default workspace state is invalid", nil)
	}
	recovery, recoveryErr := hasProjectRecovery(dataDir, id)
	if recoveryErr != nil {
		entry.prunePolicy.recovery = true
		addInventoryFinding(entry, "recovery-record", "error", "recovery metadata cannot be inspected", nil)
	} else if recovery {
		entry.prunePolicy.recovery = true
		addInventoryFinding(entry, "recovery-record", "error", "unresolved recovery metadata requires manual action", nil)
	}
	return observation
}

func diagnoseDuplicatePath(group []string, observed map[string]inventoryObservation, entries map[string]*ProjectInventoryEntry) {
	if len(group) < 2 {
		return
	}
	sort.Strings(group)
	keepers := []string{}
	for _, id := range group {
		if observation := observed[id]; observation.readable && observation.declaredID == id {
			keepers = append(keepers, id)
		}
	}
	if len(keepers) == 1 {
		keeper := keepers[0]
		for _, id := range group {
			if id == keeper {
				continue
			}
			entries[id].prunePolicy.supersededDuplicate = true
			addInventoryFinding(entries[id], "duplicate-config-path", "warning", "registered configuration path is superseded by another project registration", []string{keeper})
		}
		addInventoryFinding(entries[keeper], "duplicate-config-path", "warning", "registered configuration path is also registered by another project", groupWithout(group, keeper))
		return
	}
	for _, id := range group {
		entries[id].prunePolicy.ambiguousDuplicatePath = true
		addInventoryFinding(entries[id], "duplicate-config-path", "warning", "registered configuration path is ambiguously shared by multiple projects", groupWithout(group, id))
	}
}

func diagnoseDuplicateIdentity(identity string, group []string, entries map[string]*ProjectInventoryEntry) {
	sort.Strings(group)
	for _, id := range group {
		addInventoryFinding(entries[id], "duplicate-repository-identity", "warning", "repository identity is registered by multiple projects: "+identity, groupWithout(group, id))
	}
}

func finalizeInventoryEntry(entry *ProjectInventoryEntry) {
	entry.Status = "healthy"
	for _, finding := range entry.Findings {
		if finding.Severity == "error" {
			entry.Status = "error"
			break
		}
		if finding.Severity == "warning" {
			entry.Status = "warning"
		}
	}
	for i := range entry.Findings {
		sort.Strings(entry.Findings[i].RelatedProjectIDs)
		if entry.Findings[i].RelatedProjectIDs == nil {
			entry.Findings[i].RelatedProjectIDs = []string{}
		}
	}
	sort.Slice(entry.Findings, func(i, j int) bool {
		left, right := entry.Findings[i], entry.Findings[j]
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.Severity != right.Severity {
			return left.Severity < right.Severity
		}
		if left.Message != right.Message {
			return left.Message < right.Message
		}
		return strings.Join(left.RelatedProjectIDs, "\x00") < strings.Join(right.RelatedProjectIDs, "\x00")
	})
	entry.Prunable = !entry.prunePolicy.recovery && !entry.prunePolicy.ambiguousDuplicatePath && (entry.prunePolicy.staleEvidence || entry.prunePolicy.supersededDuplicate)
}

func addInventoryFinding(entry *ProjectInventoryEntry, code, severity, message string, related []string) {
	entry.Findings = append(entry.Findings, ProjectInventoryFinding{Code: code, Severity: severity, Message: message, RelatedProjectIDs: append([]string{}, related...)})
}

func addStaleInventoryFinding(entry *ProjectInventoryEntry, code, message string, related []string) {
	entry.prunePolicy.staleEvidence = true
	addInventoryFinding(entry, code, "error", message, related)
}
func groupWithout(group []string, id string) []string {
	result := make([]string, 0, len(group)-1)
	for _, candidate := range group {
		if candidate != id {
			result = append(result, candidate)
		}
	}
	return result
}
func hasProjectRecovery(dataDir, id string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "projects", id, "recovery"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return true, nil
		}
	}
	return false, nil
}
