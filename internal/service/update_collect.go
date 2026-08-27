package service

// Production collection for the M02 dry-run command. It deliberately has no
// writer, lock, transaction, fetch, or mutation dependency: all observations
// are gathered once, then delegated to the M01 snapshot boundary.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
)

type UpdateSnapshotCollector struct {
	Project          domain.Project
	DefaultWorkspace domain.Workspace
	DataDir          string
	Candidate        LoadedManifestSource
	configBytes      []byte
	local            config.ProjectConfig
	git              *gitadapter.Adapter
	observe          updateObservationFunc
	isAncestor       func(context.Context, string, string, string) (bool, error)
}

// updateObservationFunc is the narrow collection seam for update preflight.
// Production uses productionObservations; tests can inject a complete, ordered
// observation result without replacing any filesystem, lock, or writer path.
type updateObservationFunc func(context.Context, []byte, []byte, string) ([]DriftRepositoryObservation, []DriftFailure, error)

func NewUpdateSnapshotCollector(project domain.Project, workspace domain.Workspace, dataDir string, candidate LoadedManifestSource) *UpdateSnapshotCollector {
	return &UpdateSnapshotCollector{Project: project, DefaultWorkspace: workspace, DataDir: dataDir, Candidate: candidate, git: gitadapter.NewAdapter("git")}
}

// CollectUpdateSnapshot is the command-facing one-capture authority.  The
// local configuration is read and decoded once, then its stored source is
// overridden (only in memory) by --from before the candidate is loaded once.
func CollectUpdateSnapshot(ctx context.Context, project domain.Project, workspace domain.Workspace, dataDir, override string, loader *ManifestSourceLoader) (DriftSnapshot, LoadedManifestSource, error) {
	configBytes, err := os.ReadFile(project.ConfigPath)
	if err != nil {
		return DriftSnapshot{}, LoadedManifestSource{}, fmt.Errorf("read local project configuration: %w", err)
	}
	local, err := config.LoadProject(configBytes)
	if err != nil {
		return DriftSnapshot{}, LoadedManifestSource{}, fmt.Errorf("decode local project configuration: %w", err)
	}
	if loader == nil {
		loader = NewManifestSourceLoader()
	}
	source := local.Manifest.Source
	if override != "" {
		source = override
	}
	// ManifestSourceLoader accepts relative local paths before normalizing them,
	// so validate only explicitly HTTP(S) sources here. This keeps the update
	// command's public error boundary secret-free for userinfo/query/fragment
	// refusals without changing local-source normalization.
	if isHTTPManifestSource(source) && (config.ValidateManifestSource(source) != nil || strings.Contains(source, "#")) {
		return DriftSnapshot{}, LoadedManifestSource{}, NewError(ErrorValidation, errors.New("update manifest source is invalid"))
	}
	candidate, err := loader.Load(ctx, source)
	if err != nil {
		return DriftSnapshot{}, LoadedManifestSource{}, err
	}
	if err := ctx.Err(); err != nil {
		return DriftSnapshot{}, LoadedManifestSource{}, err
	}
	collector := NewUpdateSnapshotCollector(project, workspace, dataDir, candidate)
	collector.configBytes, collector.local = append([]byte(nil), configBytes...), local
	input, err := collector.CollectDriftSnapshot(ctx)
	if err != nil {
		return DriftSnapshot{}, LoadedManifestSource{}, err
	}
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil {
		return snapshot, candidate, err
	}
	return snapshot, candidate, nil
}

func isHTTPManifestSource(source string) bool {
	lower := strings.ToLower(source)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func (collector *UpdateSnapshotCollector) CollectDriftSnapshot(ctx context.Context) (DriftSnapshotInput, error) {
	if collector == nil || collector.git == nil {
		return DriftSnapshotInput{}, errors.New("update snapshot collector is not configured")
	}
	if err := ctx.Err(); err != nil {
		return DriftSnapshotInput{}, err
	}
	if !filepath.IsAbs(collector.DataDir) {
		return DriftSnapshotInput{}, errors.New("update data directory must be absolute")
	}
	configBytes, local := append([]byte(nil), collector.configBytes...), collector.local
	if len(configBytes) == 0 {
		var err error
		configBytes, err = os.ReadFile(collector.Project.ConfigPath)
		if err != nil {
			return DriftSnapshotInput{}, fmt.Errorf("read local project configuration: %w", err)
		}
		local, err = config.LoadProject(configBytes)
		if err != nil {
			return DriftSnapshotInput{}, fmt.Errorf("decode local project configuration: %w", err)
		}
	}
	basePath, err := collector.DefaultWorkspace.ResolveRepository(collector.Project.BaseRepository)
	if err != nil {
		return DriftSnapshotInput{}, fmt.Errorf("resolve base repository: %w", err)
	}
	baseHead, err := collector.git.Head(ctx, basePath)
	if err != nil {
		return DriftSnapshotInput{}, fmt.Errorf("observe base HEAD: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return DriftSnapshotInput{}, err
	}
	currentBytes, err := collector.git.TrackedFile(ctx, basePath, baseHead, local.Manifest.Path)
	if err != nil {
		return DriftSnapshotInput{}, fmt.Errorf("read tracked portable manifest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return DriftSnapshotInput{}, err
	}
	registryPath := filepath.Join(collector.DataDir, "registry.json")
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		return DriftSnapshotInput{}, fmt.Errorf("read registry: %w", err)
	}
	registry, err := store.DecodeRegistry(registryBytes)
	if err != nil {
		return DriftSnapshotInput{}, fmt.Errorf("decode registry: %w", err)
	}
	defaultPath := WorkspaceStatePath(collector.DataDir, collector.Project.ID, "default")
	defaultBytes, err := os.ReadFile(defaultPath)
	if err != nil {
		return DriftSnapshotInput{}, fmt.Errorf("read default workspace state: %w", err)
	}
	if _, err := store.DecodeWorkspace(defaultBytes); err != nil {
		return DriftSnapshotInput{}, fmt.Errorf("decode default workspace state: %w", err)
	}
	candidateBytes := collector.Candidate.Bytes()
	if _, err := config.LoadPortableManifest(candidateBytes); err != nil {
		return DriftSnapshotInput{}, fmt.Errorf("decode candidate portable manifest: %w", err)
	}
	observations, observationFailures, err := collector.collectObservations(ctx, currentBytes, candidateBytes, local.Manifest.Path)
	if err != nil {
		return DriftSnapshotInput{}, err
	}
	readers := DriftSnapshotReaders{
		ReadCurrentManifest: func(context.Context) (DriftManifestGeneration, error) {
			return DriftManifestGeneration{Path: filepath.Join(filepath.Dir(collector.Project.ConfigPath), local.Manifest.Path), Source: local.Manifest.Source, Bytes: append([]byte(nil), currentBytes...)}, nil
		},
		ReadCandidateManifest: func(context.Context) ([]byte, error) { return append([]byte(nil), candidateBytes...), nil },
		ReadLocalConfig: func(context.Context) ([]byte, *config.ProjectConfig, error) {
			value := local
			return append([]byte(nil), configBytes...), &value, nil
		},
		ReadRegistry: func(context.Context) ([]byte, *store.Registry, error) {
			value := registry
			return append([]byte(nil), registryBytes...), &value, nil
		},
		ReadDefaultState: func(context.Context) (PersistedWorkspaceGeneration, error) {
			return PersistedWorkspaceGeneration{Path: defaultPath, Bytes: append([]byte(nil), defaultBytes...)}, nil
		},
		ReadObservations: func(context.Context) (domain.Project, domain.Workspace, []DriftRepositoryObservation, error) {
			return collector.Project, collector.DefaultWorkspace, append([]DriftRepositoryObservation(nil), observations...), nil
		},
		Inventory: updateDriftInventory(collector.DataDir),
	}
	input, err := readers.CollectDriftSnapshot(ctx)
	if err != nil {
		return DriftSnapshotInput{}, err
	}
	input.Collection.Errors = append(input.Collection.Errors, observationFailures...)
	input.CollectionFailureOrder = updateObservationRepositoryOrder(currentBytes, candidateBytes)
	return input, nil
}

func updateDriftInventory(dataDir string) DriftInventoryReader {
	return DriftInventoryReader{
		DataDir: dataDir,
		ReadDir: func(_ context.Context, path string) ([]DriftDirectoryEntry, error) {
			entries, err := os.ReadDir(path)
			if err != nil {
				return nil, err
			}
			result := make([]DriftDirectoryEntry, 0, len(entries))
			for _, entry := range entries {
				info, infoErr := entry.Info()
				if infoErr != nil {
					return nil, infoErr
				}
				mode := info.Mode()
				result = append(result, DriftDirectoryEntry{Name: entry.Name(), Regular: mode.IsRegular(), Directory: mode.IsDir(), Symlink: mode&os.ModeSymlink != 0})
			}
			return result, nil
		},
		Lstat: func(_ context.Context, path string) (DriftDirectoryEntry, error) {
			info, err := os.Lstat(path)
			if err != nil {
				return DriftDirectoryEntry{}, err
			}
			mode := info.Mode()
			return DriftDirectoryEntry{Name: filepath.Base(path), Regular: mode.IsRegular(), Directory: mode.IsDir(), Symlink: mode&os.ModeSymlink != 0}, nil
		},
		ReadFile: func(_ context.Context, path string) ([]byte, error) { return os.ReadFile(path) },
		DecodeReconciliation: func(_ string, data []byte) ([]RetainedUnmanagedFact, error) {
			return DecodeUpdateReconciliation(data)
		},
		DecodeOperation: func(_ string, data []byte) (DriftOperationRecord, error) {
			record, err := store.DecodeRecovery(data)
			if err != nil {
				return DriftOperationRecord{}, err
			}
			return DriftOperationRecord{Operation: record.Operation}, nil
		},
	}
}

func (collector *UpdateSnapshotCollector) collectObservations(ctx context.Context, currentBytes, candidateBytes []byte, manifestPath string) ([]DriftRepositoryObservation, []DriftFailure, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if collector.observe != nil {
		observations, failures, err := collector.observe(ctx, currentBytes, candidateBytes, manifestPath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return append([]DriftRepositoryObservation(nil), observations...), normalizeDriftFailures(failures, "observation"), err
	}
	return collector.productionObservations(ctx, currentBytes, candidateBytes, manifestPath)
}

func (collector *UpdateSnapshotCollector) productionObservations(ctx context.Context, currentBytes, candidateBytes []byte, manifestPath string) ([]DriftRepositoryObservation, []DriftFailure, error) {
	current, err := config.LoadPortableManifest(currentBytes)
	if err != nil {
		return nil, nil, err
	}
	candidate, err := config.LoadPortableManifest(candidateBytes)
	if err != nil {
		return nil, nil, err
	}
	paths, err := cloneDomainProject(candidate).EffectivePaths(collector.DefaultWorkspace.RootPath, nil)
	if err != nil {
		return nil, nil, err
	}
	observations := make([]DriftRepositoryObservation, 0, len(current.Repositories)+len(candidate.Repositories))
	failures := make([]DriftFailure, 0)
	ordered := updateObservationRepositoryOrder(currentBytes, candidateBytes)
	for _, id := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if repository, exists := current.Repositories[id]; exists {
			path, pathErr := collector.DefaultWorkspace.ResolveRepository(id)
			if pathErr != nil {
				observations = append(observations, DriftRepositoryObservation{RepositoryID: id, TargetAbsent: true})
				failures = append(failures, updateObservationFailure(id, "workspace-path", pathErr))
				continue
			}
			observation, observationFailures, observationErr := collector.observeExisting(ctx, id, path, repository, currentBytes, manifestPath)
			if observationErr != nil {
				return nil, nil, observationErr
			}
			observations = append(observations, observation)
			failures = append(failures, observationFailures...)
			continue
		}
		candidateRepository := candidate.Repositories[id]
		observation := DriftRepositoryObservation{RepositoryID: id, Path: paths[id], IgnoreKnown: candidateRepository.Parent == ""}
		if info, statErr := os.Lstat(paths[id]); statErr == nil {
			observation.TargetOccupied = true
			observation.TargetAbsent = false
			_ = info
		} else if os.IsNotExist(statErr) {
			observation.TargetAbsent = true
		} else {
			observation.TargetOccupied = true
			failures = append(failures, updateObservationFailure(id, "target-stat", statErr))
		}
		if candidateRepository.Parent != "" {
			parentPath, parentErr := collector.DefaultWorkspace.ResolveRepository(candidateRepository.Parent)
			if parentErr == nil {
				evidence, ignoreErr := collector.git.InspectWorkingTreeIgnore(ctx, parentPath, candidateRepository.Mount)
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, nil, ctxErr
				}
				if ignoreErr == nil {
					observation.IgnoreKnown, observation.IgnoreVerified = true, evidence.Qualifies(parentPath)
				} else {
					failures = append(failures, updateObservationFailure(id, "parent-ignore-observation", ignoreErr))
				}
			} else {
				failures = append(failures, updateObservationFailure(id, "parent-workspace-path", parentErr))
			}
		}
		commit, remoteErr := collector.git.AdvertisedCommit(ctx, candidateRepository.Clone.URL, candidateRepository.Upstream.Merge)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		if remoteErr == nil {
			observation.AdvertisedKnown, observation.AdvertisedCommit = true, commit
		} else {
			observation.AdvertisedKnown = true
			failures = append(failures, updateObservationFailure(id, "advertised-ref-observation", remoteErr))
		}
		observations = append(observations, observation)
	}
	return observations, normalizeDriftFailures(failures, "observation"), nil
}

func updateObservationRepositoryOrder(currentBytes, candidateBytes []byte) []string {
	current, currentErr := config.LoadPortableManifest(currentBytes)
	candidate, candidateErr := config.LoadPortableManifest(candidateBytes)
	if currentErr != nil || candidateErr != nil {
		return nil
	}
	ordered := candidateManifestParentFirst(candidate)
	ordered = append(ordered, currentManifestParentFirstRemoved(current, candidate)...)
	return append([]string(nil), ordered...)
}

func updateObservationFailure(id, check string, err error) DriftFailure {
	return DriftFailure{RepositoryID: id, Check: check, Message: boundedRedactedDiagnostic(err.Error())}
}

func (collector *UpdateSnapshotCollector) observeExisting(ctx context.Context, id, path string, repository config.PortableRepository, currentBytes []byte, manifestPath string) (DriftRepositoryObservation, []DriftFailure, error) {
	observation := DriftRepositoryObservation{RepositoryID: id, Path: path}
	failures := make([]DriftFailure, 0)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		observation.TargetAbsent = true
		return observation, failures, nil
	} else if err != nil {
		failures = append(failures, updateObservationFailure(id, "target-stat", err))
		return observation, failures, nil
	}
	common, commonErr := collector.git.CommonGitDir(ctx, path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return DriftRepositoryObservation{}, nil, ctxErr
	}
	top, topErr := collector.git.TopLevel(ctx, path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return DriftRepositoryObservation{}, nil, ctxErr
	}
	if commonErr == nil && topErr == nil {
		observation.CommonGitDir, observation.IdentityKnown, observation.IdentityMatches = common, true, sameCheckoutPath(top, path)
	} else {
		if commonErr != nil {
			failures = append(failures, updateObservationFailure(id, "identity-observation", commonErr))
		}
		if topErr != nil {
			failures = append(failures, updateObservationFailure(id, "path-observation", topErr))
		}
	}
	for _, expected := range collector.Project.Repositories {
		if expected.ID == id {
			observation.IdentityMatches = observation.IdentityMatches && common == expected.CommonGitDir
			break
		}
	}
	if clean, cleanErr := collector.git.IsClean(ctx, path); cleanErr == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		observation.Clean = clean
	} else {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		failures = append(failures, updateObservationFailure(id, "cleanliness-observation", cleanErr))
	}
	if branch, detached, branchErr := collector.git.CurrentBranch(ctx, path); branchErr == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		observation.Branch, observation.Detached = branch, detached
	} else {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		failures = append(failures, updateObservationFailure(id, "branch-observation", branchErr))
	}
	if head, headErr := collector.git.Head(ctx, path); headErr == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		observation.Head = head
	} else {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		failures = append(failures, updateObservationFailure(id, "head-observation", headErr))
	}
	if upstream, upstreamErr := collector.git.Upstream(ctx, path); upstreamErr == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		observation.UpstreamKnown, observation.Upstream = true, upstream
	} else {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		failures = append(failures, updateObservationFailure(id, "upstream-observation", upstreamErr))
	}
	if id == collector.Project.BaseRepository && observation.Head != "" {
		if tracked, trackedErr := collector.git.TrackedFile(ctx, path, observation.Head, manifestPath); trackedErr == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return DriftRepositoryObservation{}, nil, ctxErr
			}
			observation.TrackedManifestKnown, observation.TrackedManifestExact = true, bytes.Equal(tracked, currentBytes)
		} else {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return DriftRepositoryObservation{}, nil, ctxErr
			}
			failures = append(failures, updateObservationFailure(id, "tracked-manifest-observation", trackedErr))
		}
	}
	if repository.Parent != "" {
		parentPath, parentErr := collector.DefaultWorkspace.ResolveRepository(repository.Parent)
		if parentErr == nil {
			evidence, ignoreErr := collector.git.InspectWorkingTreeIgnore(ctx, parentPath, repository.Mount)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return DriftRepositoryObservation{}, nil, ctxErr
			}
			if ignoreErr == nil {
				observation.IgnoreKnown, observation.IgnoreVerified = true, evidence.Qualifies(parentPath)
			} else {
				failures = append(failures, updateObservationFailure(id, "parent-ignore-observation", ignoreErr))
			}
		} else {
			failures = append(failures, updateObservationFailure(id, "parent-workspace-path", parentErr))
		}
	}
	if contains, containsErr := collector.git.ContainsCommits(ctx, path, repository.Identity.InitialCommits); containsErr == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		observation.IdentityKnown = true
		observation.IdentityMatches = observation.IdentityMatches && contains
	} else {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		failures = append(failures, updateObservationFailure(id, "identity-history-observation", containsErr))
	}
	if advertised, advertiseErr := collector.git.ObserveConfiguredRef(ctx, path, repository.Upstream.Remote, repository.Upstream.Merge); advertiseErr == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		observation.AdvertisedKnown, observation.AdvertisedCommit = true, advertised.Commit
		if observation.Head != "" && advertised.Commit != observation.Head {
			can, ancestorErr := collector.observeAncestry(ctx, path, observation.Head, advertised.Commit)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return DriftRepositoryObservation{}, nil, ctxErr
			}
			if ancestorErr == nil {
				observation.CanFastForward = can
			} else {
				failures = append(failures, updateObservationFailure(id, "ancestry-observation", ancestorErr))
			}
		}
	} else {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftRepositoryObservation{}, nil, ctxErr
		}
		failures = append(failures, updateObservationFailure(id, "advertised-ref-observation", advertiseErr))
	}
	return observation, normalizeDriftFailures(failures, "observation"), nil
}

func (collector *UpdateSnapshotCollector) observeAncestry(ctx context.Context, path, ancestor, descendant string) (bool, error) {
	if collector.isAncestor != nil {
		return collector.isAncestor(ctx, path, ancestor, descendant)
	}
	return collector.git.IsAncestor(ctx, path, ancestor, descendant)
}
