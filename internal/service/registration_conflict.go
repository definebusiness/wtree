package service

import (
	"path/filepath"
	"sort"
)

// RegistrationConflictCandidate is the narrow registry view used to detect a
// second registration for the same project. It deliberately has no store,
// resolver, Git, or configuration dependency so every consumer compares the
// same persisted facts.
type RegistrationConflictCandidate struct {
	ID                   string
	ConfigPath           string
	RepositoryIdentities []string
}

// CanonicalRegistrationConfigPath returns the comparison form for a stored
// project configuration path. Existing targets are resolved through symlinks;
// missing targets retain their cleaned absolute spelling. Paths are never
// case-folded.
func CanonicalRegistrationConfigPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// RegistrationConflictIDs returns every existing registration that has the
// same canonical configuration path or one exact persisted repository
// identity. The result is sorted and unique so callers can report ambiguity
// deterministically instead of selecting a registration to reuse or replace.
func RegistrationConflictIDs(configPath string, repositoryIdentities []string, candidates []RegistrationConflictCandidate) []string {
	targetPath := CanonicalRegistrationConfigPath(configPath)
	targetIdentities := make(map[string]struct{}, len(repositoryIdentities))
	for _, identity := range repositoryIdentities {
		targetIdentities[identity] = struct{}{}
	}
	conflicts := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate.ID == "" {
			continue
		}
		if CanonicalRegistrationConfigPath(candidate.ConfigPath) == targetPath {
			conflicts[candidate.ID] = struct{}{}
			continue
		}
		for _, identity := range candidate.RepositoryIdentities {
			if _, found := targetIdentities[identity]; found {
				conflicts[candidate.ID] = struct{}{}
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
