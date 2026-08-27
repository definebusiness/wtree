package service

// This file is the read-only M05 bridge between doctor and the shared drift
// snapshot. It intentionally gathers the same immutable evidence used by
// update without loading a replacement manifest source or advertising refs.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
)

// doctorTrackedManifestUnavailable is intentionally diagnostic-only: a local
// portable manifest must never be substituted for the base HEAD generation.
// Doctor can still report the absence and independently inventory durable
// operation evidence without claiming a complete drift classification.
type doctorTrackedManifestUnavailable struct{}

func (doctorTrackedManifestUnavailable) Error() string {
	return "tracked portable manifest is unavailable at the base HEAD"
}

type doctorObservationError struct {
	check string
	cause error
}

func (e doctorObservationError) Error() string {
	return e.check + ": " + boundedRedactedDiagnostic(e.cause.Error())
}

func (e doctorObservationError) Unwrap() error { return e.cause }

func wrapDoctorObservation(check string, err error) error {
	if err == nil {
		return nil
	}
	return doctorObservationError{check: check, cause: err}
}

// collectLocalDriftSnapshot is the single local-only collection seam shared by
// doctor and status. It deliberately reads the manifest tracked at the local
// base HEAD and local state/registry generations; it does not advertise,
// fetch, or otherwise contact a remote.
func collectLocalDriftSnapshot(ctx context.Context, git gitadapter.Git, project domain.Project, dataDir string) (DriftSnapshot, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir {
		return DriftSnapshot{}, fmt.Errorf("doctor data directory must be absolute")
	}
	configBytes, err := os.ReadFile(project.ConfigPath)
	if err != nil {
		return DriftSnapshot{}, fmt.Errorf("read local project configuration: %w", err)
	}
	local, err := config.LoadProject(configBytes)
	if err != nil {
		return DriftSnapshot{}, fmt.Errorf("decode local project configuration: %w", err)
	}
	workspace, err := RequireWorkspace(project, dataDir, "default")
	if err != nil {
		return DriftSnapshot{}, fmt.Errorf("read default workspace: %w", err)
	}
	base, found := doctorProjectRepository(project, project.BaseRepository)
	if !found {
		return DriftSnapshot{}, fmt.Errorf("configured base repository is absent")
	}
	baseIdentity, err := git.CommonGitDir(ctx, base.SourcePath)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return DriftSnapshot{}, ctxErr
	}
	if err != nil {
		return DriftSnapshot{}, wrapDoctorObservation("validate configured base repository identity", err)
	}
	if baseIdentity != base.CommonGitDir {
		return DriftSnapshot{}, fmt.Errorf("validate configured base repository identity")
	}
	baseHead, err := git.Head(ctx, base.SourcePath)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return DriftSnapshot{}, ctxErr
	}
	if err != nil {
		return DriftSnapshot{}, wrapDoctorObservation("read configured base repository HEAD", err)
	}
	manifestPath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	manifestBytes, err := git.TrackedFile(ctx, base.SourcePath, baseHead, local.Manifest.Path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return DriftSnapshot{}, ctxErr
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DriftSnapshot{}, ctxErr
		}
		return DriftSnapshot{}, doctorTrackedManifestUnavailable{}
	}
	if _, err := config.LoadPortableManifest(manifestBytes); err != nil {
		return DriftSnapshot{}, fmt.Errorf("decode tracked portable manifest: %w", err)
	}
	registryPath := filepath.Join(dataDir, "registry.json")
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		return DriftSnapshot{}, fmt.Errorf("read registry: %w", err)
	}
	registry, err := store.DecodeRegistry(registryBytes)
	if err != nil {
		return DriftSnapshot{}, fmt.Errorf("decode registry: %w", err)
	}
	defaultPath := WorkspaceStatePath(dataDir, project.ID, "default")
	defaultBytes, err := os.ReadFile(defaultPath)
	if err != nil {
		return DriftSnapshot{}, fmt.Errorf("read default workspace state: %w", err)
	}
	if _, err := store.DecodeWorkspace(defaultBytes); err != nil {
		return DriftSnapshot{}, fmt.Errorf("decode default workspace state: %w", err)
	}
	observations, failures, err := collectLocalDriftObservations(ctx, git, project, workspace, manifestBytes, local.Manifest.Path)
	if err != nil {
		return DriftSnapshot{}, err
	}
	readers := DriftSnapshotReaders{
		ReadCurrentManifest: func(context.Context) (DriftManifestGeneration, error) {
			return DriftManifestGeneration{Path: manifestPath, Source: local.Manifest.Source, Bytes: append([]byte(nil), manifestBytes...)}, nil
		},
		ReadCandidateManifest: func(context.Context) ([]byte, error) { return append([]byte(nil), manifestBytes...), nil },
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
			return project, workspace, append([]DriftRepositoryObservation(nil), observations...), nil
		},
		Inventory: updateDriftInventory(dataDir),
	}
	input, err := readers.CollectDriftSnapshot(ctx)
	if err != nil {
		return DriftSnapshot{}, err
	}
	input.Collection.Errors = append(input.Collection.Errors, failures...)
	for _, repository := range project.ParentFirst() {
		input.CollectionFailureOrder = append(input.CollectionFailureOrder, repository.ID)
	}
	return buildDriftSnapshot(input, driftSnapshotOptions{requireAdvertisement: false})
}

// collectDriftSnapshot remains the doctor-local spelling used by its focused
// tests; production callers share collectLocalDriftSnapshot above.
func (d *DoctorService) collectDriftSnapshot(ctx context.Context, project domain.Project, dataDir string) (DriftSnapshot, error) {
	return collectLocalDriftSnapshot(ctx, d.git, project, dataDir)
}

func doctorFallbackFindings(ctx context.Context, dataDir, projectID string) ([]DoctorFinding, error) {
	return doctorFallbackFindingsWithInventory(ctx, updateDriftInventory(dataDir), projectID)
}

func doctorFallbackFindingsWithInventory(ctx context.Context, inventory DriftInventoryReader, projectID string) ([]DoctorFinding, error) {
	_, retained, err := inventory.reconciliationInventory(ctx, projectID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, wrapDoctorObservation("collect retained reconciliation evidence", err)
	}
	operations, err := inventory.operationInventory(ctx, projectID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, wrapDoctorObservation("collect operation evidence", err)
	}
	findings := []DoctorFinding{{Code: "manifest-configuration-mismatch", Severity: "error", Message: "tracked portable manifest is unavailable at the base checkout HEAD"}}
	for _, fact := range retained {
		findings = append(findings, DoctorFinding{Code: "retained-unmanaged-repository", Severity: "warning", RepositoryID: fact.RepositoryID, Message: "repository is intentionally retained outside the current manifest"})
	}
	return append(findings, doctorOperationFindings(operations)...), nil
}

func doctorProjectRepository(project domain.Project, id string) (domain.Repository, bool) {
	for _, repository := range project.Repositories {
		if repository.ID == id {
			return repository, true
		}
	}
	return domain.Repository{}, false
}

func collectLocalDriftObservations(ctx context.Context, git gitadapter.Git, project domain.Project, workspace domain.Workspace, manifestBytes []byte, manifestPath string) ([]DriftRepositoryObservation, []DriftFailure, error) {
	manifest, err := config.LoadPortableManifest(manifestBytes)
	if err != nil {
		return nil, nil, err
	}
	observations := make([]DriftRepositoryObservation, 0, len(project.Repositories))
	failures := make([]DriftFailure, 0)
	heads := make(map[string]string, len(project.Repositories))
	for _, repository := range project.ParentFirst() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		path, pathErr := workspace.ResolveRepository(repository.ID)
		if pathErr != nil {
			observations = append(observations, DriftRepositoryObservation{RepositoryID: repository.ID, TargetAbsent: true})
			failures = append(failures, updateObservationFailure(repository.ID, "workspace-path", pathErr))
			continue
		}
		observation := DriftRepositoryObservation{RepositoryID: repository.ID, Path: path}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			observation.TargetAbsent = true
			observations = append(observations, observation)
			continue
		} else if err != nil {
			failures = append(failures, updateObservationFailure(repository.ID, "target-stat", err))
			observations = append(observations, observation)
			continue
		}
		common, commonErr := git.CommonGitDir(ctx, path)
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		top, topErr := git.TopLevel(ctx, path)
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if commonErr == nil && topErr == nil {
			observation.CommonGitDir, observation.IdentityKnown = common, true
			observation.IdentityMatches = sameCheckoutPath(top, path) && common == repository.CommonGitDir
		} else {
			if commonErr != nil {
				failures = append(failures, updateObservationFailure(repository.ID, "identity-observation", commonErr))
			}
			if topErr != nil {
				failures = append(failures, updateObservationFailure(repository.ID, "path-observation", topErr))
			}
		}
		if clean, cleanErr := git.IsClean(ctx, path); cleanErr == nil {
			observation.Clean = clean
		} else {
			failures = append(failures, updateObservationFailure(repository.ID, "cleanliness-observation", cleanErr))
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		branch, detached, branchErr := git.CurrentBranch(ctx, path)
		if branchErr == nil {
			observation.Branch, observation.Detached = branch, detached
		} else {
			failures = append(failures, updateObservationFailure(repository.ID, "branch-observation", branchErr))
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if head, headErr := git.Head(ctx, path); headErr == nil {
			observation.Head = head
			heads[repository.ID] = head
		} else {
			failures = append(failures, updateObservationFailure(repository.ID, "head-observation", headErr))
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !observation.Detached {
			if upstream, upstreamErr := git.Upstream(ctx, path); upstreamErr == nil {
				observation.UpstreamKnown, observation.Upstream = true, upstream
			} else {
				failures = append(failures, updateObservationFailure(repository.ID, "upstream-observation", upstreamErr))
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if repository.ID == project.BaseRepository {
			tracked, trackedErr := git.TrackedFile(ctx, path, observation.Head, manifestPath)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, ctxErr
			}
			if trackedErr == nil {
				observation.TrackedManifestKnown, observation.TrackedManifestExact = true, bytes.Equal(tracked, manifestBytes)
			} else {
				failures = append(failures, updateObservationFailure(repository.ID, "tracked-manifest-observation", trackedErr))
			}
		}
		if declared, ok := manifest.Repositories[repository.ID]; ok && declared.Parent != "" {
			parentPath, parentErr := workspace.ResolveRepository(declared.Parent)
			parentHead := heads[declared.Parent]
			if parentErr == nil && parentHead != "" {
				var ignored bool
				var ignoreErr error
				if inspector, ok := git.(gitadapter.CommittedIgnoreInspector); ok {
					ignored, ignoreErr = inspector.InspectCommittedIgnore(ctx, parentPath, parentHead, declared.Mount)
				} else {
					// Compatibility for narrow injected test boundaries. Production
					// Adapter always supplies the observational inspector above.
					ignored, ignoreErr = git.IsIgnoredAt(ctx, parentPath, parentHead, declared.Mount)
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, nil, ctxErr
				}
				if ignoreErr == nil {
					observation.IgnoreKnown, observation.IgnoreVerified = true, ignored
				} else {
					failures = append(failures, updateObservationFailure(repository.ID, "parent-ignore-observation", ignoreErr))
				}
			} else {
				if parentErr == nil {
					parentErr = fmt.Errorf("parent HEAD was not observed")
				}
				failures = append(failures, updateObservationFailure(repository.ID, "parent-workspace-path", parentErr))
			}
		}
		if declared, ok := manifest.Repositories[repository.ID]; ok {
			contains, containsErr := git.ContainsCommits(ctx, path, declared.Identity.InitialCommits)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, ctxErr
			}
			if containsErr == nil {
				observation.IdentityKnown = true
				observation.IdentityMatches = observation.IdentityMatches && contains
			} else {
				failures = append(failures, updateObservationFailure(repository.ID, "identity-history-observation", containsErr))
			}
		}
		observations = append(observations, observation)
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	return observations, failures, nil
}

// doctorDriftFindings is a pure projection: it does not read, reclassify, or
// mutate snapshot evidence. The public doctor wire remains additive.
func doctorDriftFindings(snapshot DriftSnapshot) []DoctorFinding {
	manifest, manifestErr := config.LoadPortableManifest(snapshot.CurrentManifestBytes())
	observations := map[string]DriftRepositoryObservation{}
	for _, observation := range snapshot.Observations() {
		observations[observation.RepositoryID] = observation
	}
	findings := make([]DoctorFinding, 0)
	for _, failure := range snapshot.Failures() {
		code := doctorDriftFindingCode(failure, manifest, manifestErr, observations)
		if code == "" {
			continue
		}
		findings = append(findings, DoctorFinding{Code: code, Severity: "error", RepositoryID: doctorFindingRepositoryID(failure.RepositoryID), Message: doctorDriftFindingMessage(code)})
	}
	for _, retained := range snapshot.RetainedUnmanaged() {
		findings = append(findings, DoctorFinding{Code: "retained-unmanaged-repository", Severity: "warning", RepositoryID: retained.RepositoryID, Message: "repository is intentionally retained outside the current manifest"})
	}
	findings = append(findings, doctorOperationFindings(snapshot.Operations())...)
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].RepositoryID != findings[j].RepositoryID {
			return findings[i].RepositoryID < findings[j].RepositoryID
		}
		return findings[i].Code < findings[j].Code
	})
	return uniqueDoctorFindings(findings)
}

func doctorOperationFindings(operations []DriftOperationRecord) []DoctorFinding {
	findings := make([]DoctorFinding, 0, len(operations))
	for _, operation := range operations {
		code, message := "update-recovery-record", "an incomplete operation recovery record requires manual action"
		if strings.Contains(filepath.ToSlash(operation.Path), "/update/") {
			code, message = "update-in-progress", "an update operation is still in progress or incomplete"
		}
		findings = append(findings, DoctorFinding{Code: code, Severity: "error", Message: message, path: operation.Path})
	}
	return findings
}

func doctorDriftFindingCode(failure DriftFailure, manifest config.PortableManifest, manifestErr error, observations map[string]DriftRepositoryObservation) string {
	switch failure.Check {
	case "checkout":
		return "manifest-repository-missing"
	case "state-only", "disk-only":
		return "manifest-repository-unmanaged"
	case "identity":
		return "source-identity-mismatch"
	case "path":
		return "mount-mismatch"
	case "branch":
		return "branch-mismatch"
	case "parent-ignore":
		return "parent-ignore-missing"
	case "retained-unmanaged":
		return "retained-unmanaged-repository"
	case "upstream":
		if manifestErr == nil {
			if repository, ok := manifest.Repositories[failure.RepositoryID]; ok && observations[failure.RepositoryID].Upstream.FetchURL != "" && observations[failure.RepositoryID].Upstream.FetchURL != repository.Clone.URL {
				return "repository-url-mismatch"
			}
		}
		return "repository-upstream-mismatch"
	case "configuration-contract", "current-manifest-configuration", "current-manifest-repository-set", "current-manifest-project", "current-manifest-base", "tracked-manifest", "local-config", "registry-generation":
		return "manifest-configuration-mismatch"
	}
	return ""
}

func doctorFindingRepositoryID(value string) string {
	if value == "project" {
		return ""
	}
	return value
}

func doctorDriftFindingMessage(code string) string {
	return map[string]string{
		"manifest-repository-missing":     "manifest-declared repository checkout is missing",
		"manifest-repository-unmanaged":   "workspace state or disk contains a repository absent from the manifest",
		"manifest-configuration-mismatch": "portable manifest and local project configuration disagree",
		"repository-url-mismatch":         "repository configured fetch URL does not match the manifest",
		"repository-upstream-mismatch":    "repository configured upstream does not match the manifest",
		"parent-ignore-missing":           "repository mount lacks committed immediate-parent ignore coverage",
		"retained-unmanaged-repository":   "repository is retained outside the current manifest",
		"update-in-progress":              "an update operation is still in progress or incomplete",
		"update-recovery-record":          "an incomplete operation recovery record requires manual action",
		"source-identity-mismatch":        "configured source checkout is unavailable or has a different Git identity",
		"mount-mismatch":                  "Git identity matches at a different persisted path or mount",
		"branch-mismatch":                 "actual Git branch or detached state differs from workspace state",
	}[code]
}

func uniqueDoctorFindings(findings []DoctorFinding) []DoctorFinding {
	result := make([]DoctorFinding, 0, len(findings))
	for _, finding := range findings {
		if len(result) != 0 && result[len(result)-1].Code == finding.Code && result[len(result)-1].RepositoryID == finding.RepositoryID {
			continue
		}
		result = append(result, finding)
	}
	return result
}
