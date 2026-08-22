package service

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/definebusiness/wtree/internal/pathutil"
	"github.com/definebusiness/wtree/internal/store"
)

// RegistrationConflictCandidate is the narrow registry view used to detect a
// second registration for the same project. It deliberately has no store,
// resolver, Git, or configuration dependency so every consumer compares the
// same persisted facts.
type RegistrationConflictCandidate struct {
	ID                   string
	ConfigPath           string
	RepositoryIdentities []string
	LogicalRoot          string
	TopLevelPaths        []string
}

// CanonicalRegistrationConfigPath returns the comparison form for a stored
// project configuration path. Existing targets are resolved through symlinks;
// missing targets retain their cleaned absolute spelling. Paths are never
// case-folded.
func CanonicalRegistrationConfigPath(path string) string {
	if resolved, err := pathutil.CanonicalPotentialPath(path); err == nil {
		return resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// RegistrationConflictIDs returns every existing registration that has the
// same canonical configuration path or one exact persisted repository
// identity. The result is sorted and unique so callers can report ambiguity
// deterministically instead of selecting a registration to reuse or replace.
func RegistrationConflictIDs(configPath string, repositoryIdentities []string, candidates []RegistrationConflictCandidate) []string {
	return RegistrationConflictIDsForTarget(RegistrationConflictCandidate{ConfigPath: configPath, RepositoryIdentities: repositoryIdentities}, candidates)
}

// RegistrationConflictIDsForTarget additionally compares only topology facts
// proven by strict configuration and complete default-state evidence. The
// registry wire shape remains unchanged; stale candidates simply retain the
// established config-path and Git-identity checks.
func RegistrationConflictIDsForTarget(target RegistrationConflictCandidate, candidates []RegistrationConflictCandidate) []string {
	targetPath := CanonicalRegistrationConfigPath(target.ConfigPath)
	targetIdentities := make(map[string]struct{}, len(target.RepositoryIdentities))
	for _, identity := range target.RepositoryIdentities {
		targetIdentities[identity] = struct{}{}
	}
	targetRoot := canonicalRegistrationTopologyPath(target.LogicalRoot)
	targetTopLevels := canonicalRegistrationTopologyPaths(target.TopLevelPaths)
	conflicts := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate.ID == "" {
			continue
		}
		if pathutil.CaseFoldedPathEqual(CanonicalRegistrationConfigPath(candidate.ConfigPath), targetPath) {
			conflicts[candidate.ID] = struct{}{}
			continue
		}
		for _, identity := range candidate.RepositoryIdentities {
			if _, found := targetIdentities[identity]; found {
				conflicts[candidate.ID] = struct{}{}
				break
			}
		}
		if _, found := conflicts[candidate.ID]; found {
			continue
		}
		candidateRoot := canonicalRegistrationTopologyPath(candidate.LogicalRoot)
		if targetRoot != "" && candidateRoot != "" && pathutil.CaseFoldedPathEqual(targetRoot, candidateRoot) {
			conflicts[candidate.ID] = struct{}{}
			continue
		}
		candidateTopLevels := canonicalRegistrationTopologyPaths(candidate.TopLevelPaths)
		for _, targetPath := range targetTopLevels {
			for _, candidatePath := range candidateTopLevels {
				if pathutil.CaseFoldedPathEqual(targetPath, candidatePath) {
					conflicts[candidate.ID] = struct{}{}
					break
				}
			}
			if _, found := conflicts[candidate.ID]; found {
				break
			}
		}
	}
	ids := make([]string, 0, len(conflicts))
	for id := range conflicts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func canonicalRegistrationTopologyPath(path string) string {
	if path == "" {
		return ""
	}
	return CanonicalRegistrationConfigPath(path)
}

func canonicalRegistrationTopologyPaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if canonical := canonicalRegistrationTopologyPath(path); canonical != "" {
			result = append(result, canonical)
		}
	}
	sort.Strings(result)
	return result
}

func registeredConflictCandidates(ctx context.Context, dataDir string, registry store.Registry) []RegistrationConflictCandidate {
	candidates := make([]RegistrationConflictCandidate, 0, len(registry.Projects))
	resolver := NewResolver()
	for id, registered := range registry.Projects {
		identities := make([]string, 0, len(registered.RepositoryIDs))
		for identity := range registered.RepositoryIDs {
			identities = append(identities, identity)
		}
		sort.Strings(identities)
		candidate := RegistrationConflictCandidate{ID: id, ConfigPath: registered.ConfigPath, RepositoryIdentities: identities}
		project, err := resolver.loadProject(ctx, registered.ConfigPath)
		if err != nil || project.ID != id || !sameRepositoryIDs(repositoryIDs(project), registered.RepositoryIDs) {
			candidates = append(candidates, candidate)
			continue
		}
		state, err := store.ReadWorkspace(WorkspaceStatePath(dataDir, id, "default"))
		if err != nil || state.Partial {
			candidates = append(candidates, candidate)
			continue
		}
		workspace, err := workspaceFromState(state)
		if err != nil || workspace.Validate(project) != nil || !pathutil.CaseFoldedPathEqual(canonicalRegistrationTopologyPath(workspace.RootPath), canonicalRegistrationTopologyPath(project.LogicalRoot)) {
			candidates = append(candidates, candidate)
			continue
		}
		candidate.LogicalRoot = workspace.RootPath
		for _, repository := range project.ParentFirst() {
			if repository.ParentID != "" {
				continue
			}
			path, err := workspace.ResolveRepository(repository.ID)
			if err != nil {
				candidate.LogicalRoot = ""
				candidate.TopLevelPaths = nil
				break
			}
			candidate.TopLevelPaths = append(candidate.TopLevelPaths, path)
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}
