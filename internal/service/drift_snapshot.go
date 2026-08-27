package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
)

// UpdateClassification is the complete, mutually exclusive result of the
// update preflight for one repository. It is deliberately internal: M02 owns
// the public update-plan wire contract.
type UpdateClassification string

const (
	UpdateClassificationUnchanged                UpdateClassification = "unchanged"
	UpdateClassificationFastForwardable          UpdateClassification = "fast-forwardable"
	UpdateClassificationAdded                    UpdateClassification = "added"
	UpdateClassificationRemovedRetained          UpdateClassification = "removed-retained"
	UpdateClassificationDirty                    UpdateClassification = "dirty"
	UpdateClassificationDivergent                UpdateClassification = "divergent"
	UpdateClassificationMissing                  UpdateClassification = "missing"
	UpdateClassificationMountChangeBlocked       UpdateClassification = "mount-change-blocked"
	UpdateClassificationStructurallyInconsistent UpdateClassification = "structurally-inconsistent"
)

// DriftFailure is a stable preflight refusal. Messages are bounded and
// redacted before becoming snapshot data, so future renderers cannot expose a
// manifest source or Git diagnostic accidentally.
type DriftFailure struct {
	RepositoryID string `json:"repositoryId,omitempty"`
	Check        string `json:"check"`
	Message      string `json:"message"`
}

// DriftRepositoryObservation is the complete injected observation for one
// repository. Snapshot construction never reads Git, the filesystem, a
// network remote, or a lock after it receives this value.
type DriftRepositoryObservation struct {
	RepositoryID         string
	Path                 string
	CommonGitDir         string
	Branch               string
	Head                 string
	Clean                bool
	Detached             bool
	TargetAbsent         bool
	TargetOccupied       bool
	IdentityMatches      bool
	IdentityKnown        bool
	IgnoreVerified       bool
	IgnoreKnown          bool
	TrackedManifestExact bool
	TrackedManifestKnown bool
	AdvertisedCommit     string
	AdvertisedKnown      bool
	CanFastForward       bool
	Upstream             gitadapter.Upstream
	UpstreamKnown        bool
	UpstreamDiagnostic   string
}

// DriftCollectionEvidence makes authority completeness explicit. A collector
// must set every field after it has observed the corresponding authority; an
// absent optional record is represented by its known field plus no record,
// never by omitting the authority.
type DriftCollectionEvidence struct {
	CurrentManifestKnown, CandidateManifestKnown                           bool
	ConfigKnown, RegistryKnown, DefaultStateKnown, WorkspaceInventoryKnown bool
	RetainedKnown, OperationInventoryKnown, ObservationInventoryKnown      bool
	Errors                                                                 []DriftFailure
}

func (e DriftCollectionEvidence) Complete() bool {
	return e.CurrentManifestKnown && e.CandidateManifestKnown && e.ConfigKnown && e.RegistryKnown && e.DefaultStateKnown && e.WorkspaceInventoryKnown && e.RetainedKnown && e.OperationInventoryKnown && e.ObservationInventoryKnown
}

// DriftSnapshotCollector is the service-owned collection boundary. Its
// implementation may read injected files/Git facts, but the returned input is
// classified exactly once without another observation.
type DriftSnapshotCollector interface {
	CollectDriftSnapshot(context.Context) (DriftSnapshotInput, error)
}

// DriftSnapshotReaders is the concrete, service-owned injected collection
// seam. High-level generations are observed once; low-level inventory reads
// are repeated only where exact byte or membership revalidation is required.
// It deliberately has no filesystem or Git implementation: command wiring
// supplies those authorities, while this layer guarantees that a partial
// capture cannot reach classification.
type DriftSnapshotReaders struct {
	ReadCurrentManifest   func(context.Context) (DriftManifestGeneration, error)
	ReadCandidateManifest func(context.Context) ([]byte, error)
	ReadLocalConfig       func(context.Context) ([]byte, *config.ProjectConfig, error)
	ReadRegistry          func(context.Context) ([]byte, *store.Registry, error)
	ReadDefaultState      func(context.Context) (PersistedWorkspaceGeneration, error)
	ReadObservations      func(context.Context) (domain.Project, domain.Workspace, []DriftRepositoryObservation, error)
	Inventory             DriftInventoryReader
}

// DriftManifestGeneration binds exact tracked bytes to their authoritative
// local path and the validated source recorded by the local configuration.
type DriftManifestGeneration struct {
	Path   string
	Source string
	Bytes  []byte
}

// DriftDirectoryEntry is the minimal injected directory fact needed for a
// read-only inventory. Service owns membership, ordering and type checks.
type DriftDirectoryEntry struct {
	Name      string
	Regular   bool
	Directory bool
	Symlink   bool
}

// DriftInventoryReader supplies only low-level directory/file reads and the
// existing private record decoders. It never supplies a pre-assembled list.
type DriftInventoryReader struct {
	DataDir              string
	ReadDir              func(context.Context, string) ([]DriftDirectoryEntry, error)
	Lstat                func(context.Context, string) (DriftDirectoryEntry, error)
	ReadFile             func(context.Context, string) ([]byte, error)
	DecodeReconciliation func(string, []byte) ([]RetainedUnmanagedFact, error)
	DecodeOperation      func(string, []byte) (DriftOperationRecord, error)
}

// CollectDriftSnapshot implements the one-pass collection contract using
// injected readers. A nil reader is an explicit missing authority rather than
// a harmless empty inventory; successful empty slices are known-empty.
func (readers DriftSnapshotReaders) CollectDriftSnapshot(ctx context.Context) (DriftSnapshotInput, error) {
	var input DriftSnapshotInput
	var err error
	if readers.ReadCurrentManifest == nil || readers.ReadCandidateManifest == nil || readers.ReadLocalConfig == nil || readers.ReadRegistry == nil || readers.ReadObservations == nil || readers.Inventory.ReadDir == nil || readers.Inventory.Lstat == nil || readers.Inventory.ReadFile == nil || readers.Inventory.DecodeReconciliation == nil || readers.Inventory.DecodeOperation == nil || readers.Inventory.DataDir == "" {
		return input, errors.New("drift snapshot authority reader is required")
	}
	input.DataDir = readers.Inventory.DataDir
	currentManifest, err := readers.ReadCurrentManifest(ctx)
	if err != nil {
		return input, collectionError("current-manifest-collection", err)
	}
	input.CurrentManifest = append([]byte(nil), currentManifest.Bytes...)
	input.CurrentManifestPath = currentManifest.Path
	input.CurrentManifestSource = currentManifest.Source
	input.Collection.CurrentManifestKnown = true
	if input.CandidateManifest, err = readers.ReadCandidateManifest(ctx); err != nil {
		return input, collectionError("candidate-manifest-collection", err)
	}
	input.Collection.CandidateManifestKnown = true
	if input.LocalConfigBytes, input.LocalConfig, err = readers.ReadLocalConfig(ctx); err != nil {
		return input, collectionError("local-config-collection", err)
	}
	input.Collection.ConfigKnown = true
	if input.RegistryBytes, input.Registry, err = readers.ReadRegistry(ctx); err != nil {
		return input, collectionError("registry-collection", err)
	}
	input.Collection.RegistryKnown = true
	if input.Project, input.DefaultWorkspace, input.Observations, err = readers.ReadObservations(ctx); err != nil {
		return input, collectionError("observation-inventory", err)
	}
	input.Collection.ObservationInventoryKnown = true
	if err := config.ValidatePortableID(input.Project.ID); err != nil {
		return input, collectionError("workspace-inventory", errors.New("resolved project ID is not a safe storage name"))
	}
	if input.PersistedWorkspaces, err = readers.Inventory.workspaceInventory(ctx, input.Project.ID); err != nil {
		return input, collectionError("workspace-inventory", err)
	}
	input.Collection.WorkspaceInventoryKnown = true
	defaultPath := WorkspaceStatePath(readers.Inventory.DataDir, input.Project.ID, "default")
	for index := 0; index < len(input.PersistedWorkspaces); index++ {
		generation := input.PersistedWorkspaces[index]
		if generation.Path != defaultPath {
			continue
		}
		if input.DefaultState.Path != "" {
			return input, collectionError("default-state-collection", errors.New("default workspace state is duplicated"))
		}
		input.DefaultState = generation
		input.PersistedWorkspaces = append(input.PersistedWorkspaces[:index], input.PersistedWorkspaces[index+1:]...)
		index--
	}
	if input.DefaultState.Path == "" {
		return input, collectionError("default-state-collection", errors.New("default workspace state is absent from the workspace inventory"))
	}
	if readers.ReadDefaultState != nil {
		compatibility, readErr := readers.ReadDefaultState(ctx)
		if readErr != nil {
			return input, collectionError("default-state-collection", readErr)
		}
		if compatibility.Path != defaultPath || filepath.Clean(compatibility.Path) != compatibility.Path || !bytes.Equal(compatibility.Bytes, input.DefaultState.Bytes) {
			return input, collectionError("default-state-collection", errors.New("default workspace state changed between authoritative captures"))
		}
	}
	input.Collection.DefaultStateKnown = true
	if input.Reconciliation, input.RetainedUnmanaged, err = readers.Inventory.reconciliationInventory(ctx, input.Project.ID); err != nil {
		return input, collectionError("retained-inventory", err)
	}
	input.Collection.RetainedKnown = true
	recovery, err := readers.Inventory.recoveryInventory(ctx, input.Project.ID)
	if err != nil {
		return input, collectionError("recovery-inventory", err)
	}
	updates, err := readers.Inventory.updateInventory(ctx, input.Project.ID)
	if err != nil {
		return input, collectionError("update-inventory", err)
	}
	input.Operations = append(recovery, updates...)
	sort.Slice(input.Operations, func(i, j int) bool {
		if input.Operations[i].Path != input.Operations[j].Path {
			return input.Operations[i].Path < input.Operations[j].Path
		}
		return input.Operations[i].Operation < input.Operations[j].Operation
	})
	input.Collection.OperationInventoryKnown = true
	return cloneDriftSnapshotInput(input), nil
}

func (reader DriftInventoryReader) workspaceInventory(ctx context.Context, projectID string) ([]PersistedWorkspaceGeneration, error) {
	directory := WorkspaceStateDirectory(reader.DataDir, projectID)
	entries, absent, err := reader.beginDirectoryInventory(ctx, directory, false, "workspace-inventory")
	if err != nil {
		return nil, err
	}
	if absent {
		return nil, errors.New("workspace-inventory: authoritative workspace directory is missing")
	}
	result := make([]PersistedWorkspaceGeneration, 0, len(entries))
	for _, entry := range entries {
		if !entry.Regular || entry.Symlink || filepath.Ext(entry.Name) != ".json" {
			return nil, errors.New("workspace-inventory: unexpected directory entry")
		}
		path := filepath.Join(directory, entry.Name)
		data, err := reader.ReadFile(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("workspace-inventory: %w", err)
		}
		verification, err := reader.ReadFile(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("workspace-inventory: re-read generation: %w", err)
		}
		if !bytes.Equal(data, verification) {
			return nil, errors.New("workspace-inventory: workspace generation changed during collection")
		}
		result = append(result, PersistedWorkspaceGeneration{Path: path, Bytes: append([]byte(nil), data...)})
	}
	if err := reader.finishDirectoryInventory(ctx, directory, entries, false, "workspace-inventory"); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader DriftInventoryReader) reconciliationInventory(ctx context.Context, projectID string) (DriftFileGeneration, []RetainedUnmanagedFact, error) {
	path := filepath.Join(reader.DataDir, "projects", projectID, "reconciliation.json")
	first, err := reader.Lstat(ctx, path)
	if errors.Is(err, os.ErrNotExist) {
		if _, secondErr := reader.Lstat(ctx, path); errors.Is(secondErr, os.ErrNotExist) {
			return DriftFileGeneration{}, nil, nil
		} else if secondErr != nil {
			return DriftFileGeneration{}, nil, fmt.Errorf("retained-inventory: revalidate optional reconciliation authority: %w", secondErr)
		}
		return DriftFileGeneration{}, nil, errors.New("retained-inventory: reconciliation authority appeared during collection")
	}
	if err != nil {
		return DriftFileGeneration{}, nil, fmt.Errorf("retained-inventory: inspect reconciliation authority: %w", err)
	}
	if first.Name != filepath.Base(path) || !first.Regular || first.Directory || first.Symlink {
		return DriftFileGeneration{}, nil, errors.New("retained-inventory: reconciliation authority must be a regular non-symlink file")
	}
	data, err := reader.ReadFile(ctx, path)
	if err != nil {
		return DriftFileGeneration{}, nil, fmt.Errorf("retained-inventory: read reconciliation authority: %w", err)
	}
	verification, err := reader.ReadFile(ctx, path)
	if err != nil {
		return DriftFileGeneration{}, nil, fmt.Errorf("retained-inventory: re-read reconciliation authority: %w", err)
	}
	second, err := reader.Lstat(ctx, path)
	if err != nil {
		return DriftFileGeneration{}, nil, fmt.Errorf("retained-inventory: revalidate reconciliation authority: %w", err)
	}
	if second != first || !bytes.Equal(data, verification) {
		return DriftFileGeneration{}, nil, errors.New("retained-inventory: reconciliation authority changed during collection")
	}
	facts, err := reader.DecodeReconciliation(path, append([]byte(nil), data...))
	if err != nil {
		return DriftFileGeneration{}, nil, fmt.Errorf("retained-inventory: decode reconciliation authority: %w", err)
	}
	return DriftFileGeneration{Path: path, Bytes: append([]byte(nil), data...)}, append([]RetainedUnmanagedFact(nil), facts...), nil
}
func (reader DriftInventoryReader) operationInventory(ctx context.Context, projectID string) ([]DriftOperationRecord, error) {
	recovery, err := reader.recoveryInventory(ctx, projectID)
	if err != nil {
		return nil, err
	}
	updates, err := reader.updateInventory(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := append(recovery, updates...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].Operation < result[j].Operation
	})
	return result, nil
}

func (reader DriftInventoryReader) recoveryInventory(ctx context.Context, projectID string) ([]DriftOperationRecord, error) {
	directory := filepath.Join(reader.DataDir, "projects", projectID, "recovery")
	entries, absent, err := reader.beginDirectoryInventory(ctx, directory, true, "recovery-inventory")
	if err != nil || absent {
		return nil, err
	}
	result := make([]DriftOperationRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.Regular || entry.Symlink || filepath.Ext(entry.Name) != ".json" {
			return nil, errors.New("recovery-inventory: unexpected directory entry")
		}
		path := filepath.Join(directory, entry.Name)
		data, err := reader.ReadFile(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("recovery-inventory: %w", err)
		}
		verification, err := reader.ReadFile(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("recovery-inventory: re-read generation: %w", err)
		}
		if !bytes.Equal(data, verification) {
			return nil, errors.New("recovery-inventory: recovery generation changed during collection")
		}
		fact, err := reader.DecodeOperation(path, data)
		if err != nil {
			return nil, fmt.Errorf("recovery-inventory: %w", err)
		}
		fact.Path = path
		if fact.Operation == "" {
			fact.Operation = "recovery"
		}
		if !validDriftCheck(fact.Operation) {
			return nil, errors.New("recovery-inventory: decoded operation is invalid")
		}
		result = append(result, fact)
	}
	if err := reader.finishDirectoryInventory(ctx, directory, entries, true, "recovery-inventory"); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader DriftInventoryReader) updateInventory(ctx context.Context, projectID string) ([]DriftOperationRecord, error) {
	directory := filepath.Join(reader.DataDir, "projects", projectID, "update")
	entries, absent, err := reader.beginDirectoryInventory(ctx, directory, true, "update-inventory")
	if err != nil || absent {
		return nil, err
	}
	result := make([]DriftOperationRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.Directory || entry.Regular || entry.Symlink {
			return nil, errors.New("update-inventory: unexpected directory entry")
		}
		result = append(result, DriftOperationRecord{Path: filepath.Join(directory, entry.Name), Operation: "update"})
	}
	if err := reader.finishDirectoryInventory(ctx, directory, entries, true, "update-inventory"); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader DriftInventoryReader) beginDirectoryInventory(ctx context.Context, directory string, optional bool, check string) ([]DriftDirectoryEntry, bool, error) {
	entries, err := reader.ReadDir(ctx, directory)
	if errors.Is(err, os.ErrNotExist) && optional {
		return nil, true, reader.finishDirectoryInventory(ctx, directory, nil, true, check)
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", check, err)
	}
	entries = sortedDriftEntries(entries)
	if err := validateDriftDirectoryEntries(entries); err != nil {
		return nil, false, fmt.Errorf("%s: %w", check, err)
	}
	return entries, false, nil
}

func (reader DriftInventoryReader) finishDirectoryInventory(ctx context.Context, directory string, first []DriftDirectoryEntry, optional bool, check string) error {
	second, err := reader.ReadDir(ctx, directory)
	if errors.Is(err, os.ErrNotExist) && optional && first == nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: revalidate directory membership: %w", check, err)
	}
	second = sortedDriftEntries(second)
	if err := validateDriftDirectoryEntries(second); err != nil {
		return fmt.Errorf("%s: revalidate directory membership: %w", check, err)
	}
	if !reflect.DeepEqual(first, second) {
		return fmt.Errorf("%s: directory membership changed during collection", check)
	}
	return nil
}

func validateDriftDirectoryEntries(entries []DriftDirectoryEntry) error {
	for index, entry := range entries {
		if !validDriftStorageEntryName(entry.Name) || entry.Regular == entry.Directory || entry.Symlink || index > 0 && entries[index-1].Name == entry.Name {
			return errors.New("unexpected or duplicated directory entry")
		}
	}
	return nil
}

func validDriftStorageEntryName(name string) bool {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
func sortedDriftEntries(entries []DriftDirectoryEntry) []DriftDirectoryEntry {
	copied := append([]DriftDirectoryEntry(nil), entries...)
	sort.Slice(copied, func(i, j int) bool { return copied[i].Name < copied[j].Name })
	return copied
}

// CollectDriftSnapshot collects through an injected collector and then builds
// the immutable snapshot. Collection errors become typed preflight failures.
func CollectDriftSnapshot(ctx context.Context, collector DriftSnapshotCollector) (DriftSnapshot, error) {
	if collector == nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "collection", Message: "drift snapshot collector is required"}, DriftSnapshot{})
	}
	input, err := collector.CollectDriftSnapshot(ctx)
	if err != nil {
		var collection *driftCollectionError
		if errors.As(err, &collection) {
			return typedDriftFailure(collection.failure, DriftSnapshot{})
		}
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "collection", Message: boundedRedactedDiagnostic(err.Error())}, DriftSnapshot{})
	}
	return BuildDriftSnapshot(input)
}

type driftCollectionError struct{ failure DriftFailure }

func (e *driftCollectionError) Error() string { return e.failure.Check + ": " + e.failure.Message }
func collectionError(check string, err error) error {
	return &driftCollectionError{failure: DriftFailure{RepositoryID: "project", Check: check, Message: boundedRedactedDiagnostic(err.Error())}}
}

// DriftPreflightError carries the same stable refusal that is present in the
// snapshot, allowing callers to render one deterministic failure path.
type DriftPreflightError struct {
	Failure  DriftFailure
	Snapshot DriftSnapshot
}

func (e *DriftPreflightError) Error() string { return e.Failure.Check + ": " + e.Failure.Message }
func typedDriftFailure(failure DriftFailure, snapshot DriftSnapshot) (DriftSnapshot, error) {
	failure = normalizeDriftFailure(failure, "preflight")
	snapshot.failures = append(snapshot.failures, failure)
	sortDriftFailures(snapshot.failures)
	return snapshot, &DriftPreflightError{Failure: failure, Snapshot: snapshot}
}

func normalizeDriftFailures(failures []DriftFailure, fallbackCheck string) []DriftFailure {
	normalized := make([]DriftFailure, 0, len(failures))
	for _, failure := range failures {
		normalized = append(normalized, normalizeDriftFailure(failure, fallbackCheck))
	}
	sortDriftFailures(normalized)
	return normalized
}

func normalizeDriftFailure(failure DriftFailure, fallbackCheck string) DriftFailure {
	if failure.RepositoryID == "" || failure.RepositoryID != "project" && !validDriftRepositoryID(failure.RepositoryID) {
		failure.RepositoryID = "project"
	}
	if !validDriftCheck(failure.Check) {
		failure.Check = fallbackCheck
	}
	failure.Message = boundedRedactedDiagnostic(failure.Message)
	return failure
}

func validDriftCheck(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || index > 0 && (character == '.' || character == '_' || character == '-')) {
			return false
		}
	}
	return true
}

// PersistedWorkspaceGeneration retains the exact observed state bytes along
// with the decoded, validated facts. Bytes is copied at both boundaries.
type PersistedWorkspaceGeneration struct {
	Path  string
	Bytes []byte
}

// DriftFileGeneration retains the exact bytes and fixed authoritative path of
// one optional service-owned file for later compare-and-swap revalidation.
type DriftFileGeneration struct {
	Path  string
	Bytes []byte
}

// DriftOperationRecord represents an already discovered update journal or
// recovery record. M01 only reports it; later update execution owns it.
type DriftOperationRecord struct {
	Path       string
	Operation  string
	Diagnostic string
}

// DriftSnapshotInput is a one-shot observation packet. Callers must collect
// all facts first, then call BuildDriftSnapshot once; classification is pure.
type DriftSnapshotInput struct {
	Collection DriftCollectionEvidence
	// CollectionFailureOrder is the collector-captured parent-first repository
	// order used only when rendering multiple collection failures. It never
	// changes classification and is intentionally absent from public plans.
	CollectionFailureOrder []string
	DataDir                string
	Project                domain.Project
	DefaultWorkspace       domain.Workspace
	DefaultState           PersistedWorkspaceGeneration
	CurrentManifest        []byte
	CurrentManifestPath    string
	CurrentManifestSource  string
	CandidateManifest      []byte
	LocalConfig            *config.ProjectConfig
	LocalConfigBytes       []byte
	Registry               *store.Registry
	RegistryBytes          []byte
	RegistryKnown          bool
	RegistryConsistent     bool
	PersistedWorkspaces    []PersistedWorkspaceGeneration
	Reconciliation         DriftFileGeneration
	RetainedUnmanaged      []RetainedUnmanagedFact
	Operations             []DriftOperationRecord
	Observations           []DriftRepositoryObservation
}

// RetainedUnmanagedFact is private-record evidence for repositories removed
// by an earlier update. The strict on-disk reconciliation schema is introduced
// by the update execution milestone, not by this observational snapshot.
type RetainedUnmanagedFact struct {
	RepositoryID string
	Path         string
	CommonGitDir string
}

// DriftRepository is a detached snapshot result. ObservedCommit is evidence
// only; ExecutionCommit intentionally remains empty because moving remote refs
// are never exact execution targets for ordinary update.
type DriftRepository struct {
	ID              string               `json:"id"`
	ParentID        string               `json:"parentId,omitempty"`
	Mount           string               `json:"mount"`
	Path            string               `json:"path,omitempty"`
	Branch          string               `json:"branch,omitempty"`
	Head            string               `json:"head,omitempty"`
	ObservedCommit  string               `json:"observedCommit,omitempty"`
	ExecutionCommit string               `json:"-"`
	Classification  UpdateClassification `json:"classification"`
	Failures        []DriftFailure       `json:"failures,omitempty"`
}

// DriftWorkspace is the immutable account of one persisted state generation.
type DriftWorkspace struct {
	Path    string
	ID      string
	Name    string
	Partial bool
	data    []byte
}

func (workspace DriftWorkspace) Bytes() []byte { return append([]byte(nil), workspace.data...) }

// DriftSnapshot provides the only M01 shared drift and update-classification
// source. Every accessor defensively copies mutable data.
type DriftSnapshot struct {
	dataDir               string
	project               domain.Project
	defaultWorkspace      domain.Workspace
	defaultState          PersistedWorkspaceGeneration
	currentManifest       []byte
	currentManifestPath   string
	currentManifestSource string
	candidateManifest     []byte
	currentDigest         string
	candidateDigest       string
	localConfig           []byte
	registry              []byte
	repositories          []DriftRepository
	workspaces            []DriftWorkspace
	reconciliation        DriftFileGeneration
	retained              []RetainedUnmanagedFact
	operations            []DriftOperationRecord
	observations          []DriftRepositoryObservation
	collection            DriftCollectionEvidence
	failures              []DriftFailure
	differences           []DriftSetDifference
}

// DriftSetDifference is internal provenance for state-only, disk-only, and
// retained-only repository IDs. Later status/doctor work may project it to a
// public wire model without re-observing the project.
type DriftSetDifference struct{ ID, Origin, Check string }

func (snapshot DriftSnapshot) Project() domain.Project { return cloneDriftProject(snapshot.project) }
func (snapshot DriftSnapshot) DefaultWorkspace() domain.Workspace {
	return cloneDriftDomainWorkspace(snapshot.defaultWorkspace)
}

// DefaultState returns the exact immutable default-state generation that was
// correlated with the resolved workspace.
func (snapshot DriftSnapshot) DefaultState() PersistedWorkspaceGeneration {
	value := snapshot.defaultState
	value.Bytes = append([]byte(nil), snapshot.defaultState.Bytes...)
	return value
}
func (snapshot DriftSnapshot) CurrentManifestBytes() []byte {
	return append([]byte(nil), snapshot.currentManifest...)
}
func (snapshot DriftSnapshot) CurrentManifestGeneration() DriftManifestGeneration {
	return DriftManifestGeneration{Path: snapshot.currentManifestPath, Source: snapshot.currentManifestSource, Bytes: append([]byte(nil), snapshot.currentManifest...)}
}
func (snapshot DriftSnapshot) CandidateManifestBytes() []byte {
	return append([]byte(nil), snapshot.candidateManifest...)
}
func (snapshot DriftSnapshot) CurrentManifestSHA256() string   { return snapshot.currentDigest }
func (snapshot DriftSnapshot) CandidateManifestSHA256() string { return snapshot.candidateDigest }
func (snapshot DriftSnapshot) LocalConfigBytes() []byte {
	return append([]byte(nil), snapshot.localConfig...)
}
func (snapshot DriftSnapshot) RegistryBytes() []byte {
	return append([]byte(nil), snapshot.registry...)
}
func (snapshot DriftSnapshot) Repositories() []DriftRepository {
	return cloneDriftRepositories(snapshot.repositories)
}
func (snapshot DriftSnapshot) PersistedWorkspaces() []DriftWorkspace {
	return cloneDriftWorkspaces(snapshot.workspaces)
}
func (snapshot DriftSnapshot) ReconciliationGeneration() DriftFileGeneration {
	return DriftFileGeneration{Path: snapshot.reconciliation.Path, Bytes: append([]byte(nil), snapshot.reconciliation.Bytes...)}
}
func (snapshot DriftSnapshot) RetainedUnmanaged() []RetainedUnmanagedFact {
	return append([]RetainedUnmanagedFact(nil), snapshot.retained...)
}
func (snapshot DriftSnapshot) Operations() []DriftOperationRecord {
	return append([]DriftOperationRecord(nil), snapshot.operations...)
}
func (snapshot DriftSnapshot) Observations() []DriftRepositoryObservation {
	return append([]DriftRepositoryObservation(nil), snapshot.observations...)
}
func (snapshot DriftSnapshot) CollectionEvidence() DriftCollectionEvidence {
	value := snapshot.collection
	value.Errors = append([]DriftFailure(nil), snapshot.collection.Errors...)
	return value
}
func (snapshot DriftSnapshot) Failures() []DriftFailure {
	return append([]DriftFailure(nil), snapshot.failures...)
}
func (snapshot DriftSnapshot) SetDifferences() []DriftSetDifference {
	return append([]DriftSetDifference(nil), snapshot.differences...)
}
func (snapshot DriftSnapshot) MayUpdate() bool { return len(snapshot.failures) == 0 }
func (snapshot DriftSnapshot) HasNonDefaultCompleteWorkspace() bool {
	for _, workspace := range snapshot.workspaces {
		if workspace.ID != "default" && !workspace.Partial {
			return true
		}
	}
	return false
}
func (snapshot DriftSnapshot) HasImportedPartialWorkspace() bool {
	for _, workspace := range snapshot.workspaces {
		if workspace.ID != "default" && workspace.Partial {
			return true
		}
	}
	return false
}
func (snapshot DriftSnapshot) HasFailure(repositoryID, check string) bool {
	for _, failure := range snapshot.failures {
		if failure.RepositoryID == repositoryID && failure.Check == check {
			return true
		}
	}
	return false
}

// BuildDriftSnapshot validates supplied generations, correlates them with the
// candidate manifest, and returns a deeply immutable classification. It does
// not mutate any supplied file, state, ref, lock, or recovery path.
func BuildDriftSnapshot(input DriftSnapshotInput) (DriftSnapshot, error) {
	return buildDriftSnapshot(input, driftSnapshotOptions{requireAdvertisement: true})
}

// driftSnapshotOptions is deliberately private. Only a command-owned
// collector can request local-only classification; arbitrary callers of the
// shared public snapshot API cannot weaken update's remote-ref contract.
type driftSnapshotOptions struct{ requireAdvertisement bool }

func buildDriftSnapshot(input DriftSnapshotInput, options driftSnapshotOptions) (DriftSnapshot, error) {
	// An omitted authority is never equivalent to an observed empty authority.
	// Every caller, including tests, must supply the complete one-shot capture.
	if !input.Collection.Complete() {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "collection-completeness", Message: "authoritative drift collection is incomplete"}, DriftSnapshot{})
	}
	normalizedCollectionErrors := normalizeDriftFailuresInRepositoryOrder(input.Collection.Errors, "collection", input.CollectionFailureOrder)
	input.Collection.Errors = append([]DriftFailure(nil), normalizedCollectionErrors...)
	if len(normalizedCollectionErrors) != 0 {
		snapshot := DriftSnapshot{collection: input.Collection, failures: append([]DriftFailure(nil), normalizedCollectionErrors...)}
		return snapshot, &DriftPreflightError{Failure: normalizedCollectionErrors[0], Snapshot: snapshot}
	}
	if len(input.CurrentManifest) == 0 || len(input.CandidateManifest) == 0 {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "manifest-generation", Message: "authoritative manifest bytes are missing"}, DriftSnapshot{})
	}
	if len(input.LocalConfigBytes) == 0 || input.LocalConfig == nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "local-config", Message: "authoritative local configuration bytes are missing"}, DriftSnapshot{})
	}
	if len(input.RegistryBytes) == 0 || input.Registry == nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "registry-generation", Message: "authoritative registry bytes are missing"}, DriftSnapshot{})
	}
	if input.DataDir == "" || !filepath.IsAbs(input.DataDir) || filepath.Clean(input.DataDir) != input.DataDir {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "collection", Message: "authoritative data directory is missing or non-canonical"}, DriftSnapshot{})
	}
	if input.Project.ConfigPath == "" || !filepath.IsAbs(input.Project.ConfigPath) || filepath.Clean(input.Project.ConfigPath) != input.Project.ConfigPath {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "local-config", Message: "resolved project configuration path is missing or non-canonical"}, DriftSnapshot{})
	}
	if err := input.Project.Validate(); err != nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "project", Message: err.Error()}, DriftSnapshot{})
	}
	if err := input.DefaultWorkspace.Validate(input.Project); err != nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "default-workspace", Message: err.Error()}, DriftSnapshot{})
	}
	if input.DefaultState.Path != WorkspaceStatePath(input.DataDir, input.Project.ID, "default") || len(input.DefaultState.Bytes) == 0 {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "default-state", Message: "authoritative default workspace generation has the wrong path or no bytes"}, DriftSnapshot{})
	}
	if err := validateDriftAuthorityPaths(input); err != nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "collection", Message: err.Error()}, DriftSnapshot{})
	}
	current, err := config.LoadPortableManifest(input.CurrentManifest)
	if err != nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "current-manifest", Message: err.Error()}, DriftSnapshot{})
	}
	candidate, err := config.LoadPortableManifest(input.CandidateManifest)
	if err != nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "candidate-manifest", Message: err.Error()}, DriftSnapshot{})
	}
	snapshot := DriftSnapshot{dataDir: input.DataDir, project: cloneDriftProject(input.Project), defaultWorkspace: cloneDriftDomainWorkspace(input.DefaultWorkspace), defaultState: PersistedWorkspaceGeneration{Path: input.DefaultState.Path, Bytes: append([]byte(nil), input.DefaultState.Bytes...)}, currentManifest: append([]byte(nil), input.CurrentManifest...), currentManifestPath: input.CurrentManifestPath, currentManifestSource: input.CurrentManifestSource, candidateManifest: append([]byte(nil), input.CandidateManifest...), localConfig: append([]byte(nil), input.LocalConfigBytes...), registry: append([]byte(nil), input.RegistryBytes...), reconciliation: DriftFileGeneration{Path: input.Reconciliation.Path, Bytes: append([]byte(nil), input.Reconciliation.Bytes...)}, retained: append([]RetainedUnmanagedFact(nil), input.RetainedUnmanaged...), operations: cloneDriftOperations(input.Operations), observations: append([]DriftRepositoryObservation(nil), input.Observations...), collection: input.Collection}
	snapshot.collection.Errors = append([]DriftFailure(nil), input.Collection.Errors...)
	sort.Slice(snapshot.retained, func(left, right int) bool {
		if snapshot.retained[left].RepositoryID != snapshot.retained[right].RepositoryID {
			return snapshot.retained[left].RepositoryID < snapshot.retained[right].RepositoryID
		}
		return snapshot.retained[left].Path < snapshot.retained[right].Path
	})
	sort.Slice(snapshot.operations, func(left, right int) bool {
		if snapshot.operations[left].Path != snapshot.operations[right].Path {
			return snapshot.operations[left].Path < snapshot.operations[right].Path
		}
		return snapshot.operations[left].Operation < snapshot.operations[right].Operation
	})
	snapshot.currentDigest = digestDriftBytes(snapshot.currentManifest)
	snapshot.candidateDigest = digestDriftBytes(snapshot.candidateManifest)
	localConfigChecked := false
	if len(input.LocalConfigBytes) != 0 {
		decoded, decodeErr := config.LoadProject(input.LocalConfigBytes)
		if decodeErr != nil {
			addDriftFailure(&snapshot, "project", "local-config", boundedRedactedDiagnostic(decodeErr.Error()))
		} else if input.LocalConfig != nil && !reflect.DeepEqual(decoded, *input.LocalConfig) {
			addDriftFailure(&snapshot, "project", "local-config", "local configuration bytes and parsed generation disagree")
		} else {
			addLocalConfigFailures(&snapshot, decoded, input.Project, input.CurrentManifestPath, input.CurrentManifestSource)
			localConfigChecked = true
		}
	}
	if input.LocalConfig != nil && !localConfigChecked {
		addLocalConfigFailures(&snapshot, *input.LocalConfig, input.Project, input.CurrentManifestPath, input.CurrentManifestSource)
	}
	registryChecked := false
	if len(input.RegistryBytes) != 0 {
		decoded, decodeErr := store.DecodeRegistry(input.RegistryBytes)
		if decodeErr != nil {
			addDriftFailure(&snapshot, "project", "registry-generation", boundedRedactedDiagnostic(decodeErr.Error()))
		} else if input.Registry != nil && !reflect.DeepEqual(decoded, *input.Registry) {
			addDriftFailure(&snapshot, "project", "registry-generation", "registry bytes and parsed generation disagree")
		} else {
			addRegistryFailures(&snapshot, decoded, input.Project)
			registryChecked = true
		}
	}
	if input.RegistryKnown && !input.RegistryConsistent {
		addDriftFailure(&snapshot, "project", "registry-generation", "registry generation is inconsistent with the current project")
	}
	if input.Registry != nil && !registryChecked {
		addRegistryFailures(&snapshot, *input.Registry, input.Project)
	}
	addManifestProjectFailures(&snapshot, current, input.Project, "current")
	if candidate.Project.ID != current.Project.ID {
		addDriftFailure(&snapshot, "project", "project-id", "candidate manifest changes the project ID")
	}
	if candidate.Project.BaseRepository != current.Project.BaseRepository {
		addDriftFailure(&snapshot, "project", "base-repository", "candidate manifest changes the base repository")
	}
	if input.DefaultWorkspace.ID != "default" || input.DefaultWorkspace.Name != "default" || input.DefaultWorkspace.Partial {
		addDriftFailure(&snapshot, "project", "default-workspace", "update requires the complete default workspace")
	}
	snapshot.workspaces = decodeDriftWorkspaces(&snapshot, input.PersistedWorkspaces, input.Project)
	for _, operation := range snapshot.operations {
		addDriftFailure(&snapshot, "project", "unresolved-operation", fmt.Sprintf("unresolved %s record", boundedRedactedDiagnostic(operation.Operation)))
	}

	observations, err := indexDriftObservations(input.Observations)
	if err != nil {
		return typedDriftFailure(DriftFailure{RepositoryID: "project", Check: "observation-inventory", Message: err.Error()}, snapshot)
	}
	if err := correlateDefaultDriftState(input.DefaultState, input.DefaultWorkspace, input.Project, observations); err != nil {
		addDriftFailure(&snapshot, "project", "default-state", err.Error())
	}
	currentByID := current.Repositories
	candidateByID := candidate.Repositories
	for _, repository := range candidateManifestParentFirst(candidate) {
		candidateRepository := candidateByID[repository]
		currentRepository, existed := currentByID[repository]
		observation := observations[repository]
		fact := DriftRepository{ID: repository, ParentID: candidateRepository.Parent, Mount: candidateRepository.Mount, Classification: UpdateClassificationAdded}
		if existed {
			fact = classifyExistingDriftRepository(repository, currentRepository, candidateRepository, observation, input.Project, true, options.requireAdvertisement)
		} else {
			snapshot.differences = append(snapshot.differences, DriftSetDifference{ID: repository, Origin: "candidate", Check: "candidate-only"})
			fact.Path, fact.ObservedCommit = observation.Path, observation.AdvertisedCommit
			if !observation.TargetAbsent || observation.TargetOccupied {
				addRepositoryFailure(&fact, "occupied-target", "candidate repository target is already occupied")
			}
			if candidateRepository.Parent != "" && !driftIgnoreObserved(observation) {
				addRepositoryFailure(&fact, "parent-ignore", "candidate repository parent ignore coverage was not observed")
			} else if observation.IgnoreKnown && !observation.IgnoreVerified {
				addRepositoryFailure(&fact, "parent-ignore", "candidate repository lacks committed parent ignore coverage")
			}
			if !driftAdvertisedObserved(observation) || !validDriftObjectID(observation.AdvertisedCommit) {
				addRepositoryFailure(&fact, "advertised-ref", "candidate selected remote ref is unavailable")
			}
		}
		snapshot.repositories = append(snapshot.repositories, fact)
		appendRepositoryFailures(&snapshot, fact)
	}
	for _, repository := range currentManifestParentFirstRemoved(current, candidate) {
		currentRepository := currentByID[repository]
		observation := observations[repository]
		fact := DriftRepository{ID: repository, ParentID: currentRepository.Parent, Mount: currentRepository.Mount, Path: observation.Path, Branch: observation.Branch, Head: observation.Head, ObservedCommit: observation.AdvertisedCommit, Classification: UpdateClassificationRemovedRetained}
		snapshot.differences = append(snapshot.differences, DriftSetDifference{ID: repository, Origin: "current", Check: "current-only"})
		validateRemovedRetainedDriftRepository(&snapshot, &fact, currentRepository, observation, input.Project, input.RetainedUnmanaged)
		snapshot.repositories = append(snapshot.repositories, fact)
		appendRepositoryFailures(&snapshot, fact)
	}
	appendUnexpectedDriftObservations(&snapshot, observations, currentByID, candidateByID, input.RetainedUnmanaged)
	validateRetainedDriftFacts(&snapshot, input.RetainedUnmanaged, observations, currentByID, candidateByID)
	stateGenerations := append([]PersistedWorkspaceGeneration{input.DefaultState}, input.PersistedWorkspaces...)
	appendStateOnlyDriftFacts(&snapshot, stateGenerations, currentByID, candidateByID)
	sort.Slice(snapshot.retained, func(left, right int) bool {
		if snapshot.retained[left].RepositoryID != snapshot.retained[right].RepositoryID {
			return snapshot.retained[left].RepositoryID < snapshot.retained[right].RepositoryID
		}
		return snapshot.retained[left].Path < snapshot.retained[right].Path
	})
	sortDriftRepositories(snapshot.repositories, current, candidate)
	sort.Slice(snapshot.differences, func(left, right int) bool {
		if snapshot.differences[left].ID != snapshot.differences[right].ID {
			return snapshot.differences[left].ID < snapshot.differences[right].ID
		}
		if snapshot.differences[left].Origin != snapshot.differences[right].Origin {
			return snapshot.differences[left].Origin < snapshot.differences[right].Origin
		}
		return snapshot.differences[left].Check < snapshot.differences[right].Check
	})
	if hasDriftRepositorySetChange(currentByID, candidateByID) && hasNonDefaultDriftWorkspace(snapshot.workspaces) {
		addDriftFailure(&snapshot, "project", "non-default-workspace-repository-set-change", "candidate repository-set changes are unsafe while a non-default workspace exists")
	}
	sortDriftFailures(snapshot.failures)
	return snapshot, nil
}

func validateDriftAuthorityPaths(input DriftSnapshotInput) error {
	stateDirectory := WorkspaceStateDirectory(input.DataDir, input.Project.ID)
	seen := map[string]bool{input.DefaultState.Path: true}
	for _, generation := range input.PersistedWorkspaces {
		if generation.Path == "" || filepath.Clean(generation.Path) != generation.Path || filepath.Dir(generation.Path) != stateDirectory || filepath.Ext(generation.Path) != ".json" || !validDriftStorageEntryName(filepath.Base(generation.Path)) || len(generation.Bytes) == 0 || seen[generation.Path] {
			return errors.New("persisted workspace generation has an invalid or duplicate authoritative path")
		}
		seen[generation.Path] = true
	}
	reconciliationPath := filepath.Join(input.DataDir, "projects", input.Project.ID, "reconciliation.json")
	if input.Reconciliation.Path == "" {
		if len(input.Reconciliation.Bytes) != 0 || len(input.RetainedUnmanaged) != 0 {
			return errors.New("retained evidence exists without the reconciliation authority")
		}
	} else if input.Reconciliation.Path != reconciliationPath || len(input.Reconciliation.Bytes) == 0 {
		return errors.New("reconciliation generation has the wrong authoritative path or no bytes")
	}
	recoveryDirectory := filepath.Join(input.DataDir, "projects", input.Project.ID, "recovery")
	updateDirectory := filepath.Join(input.DataDir, "projects", input.Project.ID, "update")
	operationPaths := map[string]bool{}
	for _, operation := range input.Operations {
		name := filepath.Base(operation.Path)
		parent := filepath.Dir(operation.Path)
		validRecovery := parent == recoveryDirectory && filepath.Ext(name) == ".json"
		validUpdate := parent == updateDirectory && filepath.Ext(name) == ""
		if operation.Path == "" || filepath.Clean(operation.Path) != operation.Path || !validDriftStorageEntryName(name) || !validRecovery && !validUpdate || operationPaths[operation.Path] {
			return errors.New("operation inventory contains an invalid or duplicate authoritative path")
		}
		operationPaths[operation.Path] = true
	}
	return nil
}

func correlateDefaultDriftState(generation PersistedWorkspaceGeneration, workspace domain.Workspace, project domain.Project, observations map[string]DriftRepositoryObservation) error {
	if generation.Path == "" || len(generation.Bytes) == 0 {
		return errors.New("authoritative default workspace state is missing")
	}
	state, err := store.DecodeWorkspace(generation.Bytes)
	if err != nil {
		return fmt.Errorf("decode authoritative default workspace state: %s", boundedRedactedDiagnostic(err.Error()))
	}
	if state.ID != "default" || state.Name != "default" {
		return errors.New("authoritative default workspace state is not default")
	}
	decoded, err := workspaceFromState(state)
	if err != nil {
		return fmt.Errorf("decode default workspace state: %s", boundedRedactedDiagnostic(err.Error()))
	}
	if err := decoded.Validate(project); err != nil {
		return fmt.Errorf("validate default workspace state: %s", boundedRedactedDiagnostic(err.Error()))
	}
	if !reflect.DeepEqual(decoded, workspace) {
		return errors.New("resolved default workspace does not match authoritative state generation")
	}
	for _, checkout := range workspace.Checkouts {
		observation, ok := observations[checkout.RepositoryID]
		if !ok {
			return fmt.Errorf("default workspace checkout %q was not observed", checkout.RepositoryID)
		}
		if filepath.Clean(observation.Path) != checkout.ResolvedPath {
			return fmt.Errorf("default workspace checkout %q path does not match observed checkout", checkout.RepositoryID)
		}
		if observation.Detached != checkout.Detached || observation.Branch != checkout.Branch || observation.Head != checkout.Head {
			return fmt.Errorf("default workspace checkout %q branch or HEAD does not match observed checkout", checkout.RepositoryID)
		}
		for _, repository := range project.Repositories {
			if repository.ID == checkout.RepositoryID && (checkout.Mount != repository.DefaultMount || checkout.ResolvedPath != repository.SourcePath) {
				return fmt.Errorf("default workspace checkout %q mount or path does not match project", checkout.RepositoryID)
			}
		}
	}
	if workspace.Recovery != nil {
		return errors.New("default workspace has unresolved recovery evidence")
	}
	return nil
}

func validateRemovedRetainedDriftRepository(snapshot *DriftSnapshot, fact *DriftRepository, current config.PortableRepository, observation DriftRepositoryObservation, project domain.Project, retained []RetainedUnmanagedFact) {
	var expected domain.Repository
	for _, repository := range project.Repositories {
		if repository.ID == fact.ID {
			expected = repository
			break
		}
	}
	if expected.ID == "" || expected.ParentID != current.Parent || expected.DefaultMount != current.Mount || expected.DefaultBranch != current.DefaultBranch {
		addRepositoryFailure(fact, "removed-retained-contract", "removed repository does not match the current project contract")
		return
	}
	if observation.TargetAbsent || observation.Path == "" {
		addRepositoryFailure(fact, "removed-retained", "removed repository checkout is missing and cannot be retained")
		return
	}
	if filepath.Clean(observation.Path) != expected.SourcePath || observation.CommonGitDir == "" || observation.CommonGitDir != expected.CommonGitDir || !observation.IdentityKnown || !observation.IdentityMatches {
		addRepositoryFailure(fact, "removed-retained", "removed repository identity or canonical path does not match the current project")
		return
	}
	if !observation.Clean || observation.Detached || observation.Branch != current.DefaultBranch || !validDriftObjectID(observation.Head) {
		addRepositoryFailure(fact, "removed-retained", "removed repository is not a clean attached expected-branch checkout")
		return
	}
	if !observation.UpstreamKnown || observation.Upstream.LocalBranch != current.DefaultBranch || observation.Upstream.Remote != current.Upstream.Remote || observation.Upstream.Merge != current.Upstream.Merge || observation.Upstream.FetchURL != current.Clone.URL {
		addRepositoryFailure(fact, "upstream", "removed repository upstream contract does not match the manifest")
		return
	}
	prospective := RetainedUnmanagedFact{RepositoryID: fact.ID, Path: observation.Path, CommonGitDir: observation.CommonGitDir}
	for _, existing := range retained {
		if existing.RepositoryID != fact.ID {
			continue
		}
		if existing != prospective {
			addRepositoryFailure(fact, "retained-unmanaged", "removed repository retained evidence does not match observed checkout")
		}
		return
	}
	// A removed repository becomes a complete prospective retained fact even
	// when it had no earlier retention record. This remains in-memory only.
	snapshot.retained = append(snapshot.retained, prospective)
}

func classifyExistingDriftRepository(id string, current, candidate config.PortableRepository, observation DriftRepositoryObservation, project domain.Project, requireUpstream bool, advertisement ...bool) DriftRepository {
	requireAdvertisement := true
	if len(advertisement) != 0 {
		requireAdvertisement = advertisement[0]
	}
	fact := DriftRepository{ID: id, ParentID: candidate.Parent, Mount: candidate.Mount, Path: observation.Path, Branch: observation.Branch, Head: observation.Head, ObservedCommit: observation.AdvertisedCommit, Classification: UpdateClassificationUnchanged}
	if current.Mount != candidate.Mount {
		fact.Classification = UpdateClassificationMountChangeBlocked
		addRepositoryFailure(&fact, "mount-change", "candidate changes an existing repository mount")
		return fact
	}
	if current.Parent != candidate.Parent || current.DefaultBranch != candidate.DefaultBranch || !reflect.DeepEqual(current.Clone, candidate.Clone) || !reflect.DeepEqual(current.Upstream, candidate.Upstream) || !reflect.DeepEqual(current.Identity, candidate.Identity) {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "repository-contract", "candidate changes an existing repository contract")
		return fact
	}
	var expected domain.Repository
	for _, repository := range project.Repositories {
		if repository.ID == id {
			expected = repository
			break
		}
	}
	if expected.ID == "" || expected.ParentID != current.Parent || expected.DefaultMount != current.Mount || expected.DefaultBranch != current.DefaultBranch {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "configuration-contract", "current manifest and local project configuration disagree")
		return fact
	}
	if observation.Path == "" || observation.TargetAbsent {
		fact.Classification = UpdateClassificationMissing
		addRepositoryFailure(&fact, "checkout", "repository checkout is missing")
		return fact
	}
	if !observation.IdentityKnown || !observation.IdentityMatches || observation.CommonGitDir == "" || observation.CommonGitDir != expected.CommonGitDir {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "identity", "repository Git identity does not match the project")
		return fact
	}
	if filepath.Clean(observation.Path) != expected.SourcePath {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "path", "repository checkout path does not match the current project")
		return fact
	}
	if !observation.Clean {
		fact.Classification = UpdateClassificationDirty
		addRepositoryFailure(&fact, "cleanliness", "repository checkout is dirty")
		return fact
	}
	if observation.Detached || observation.Branch != current.DefaultBranch {
		fact.Classification = UpdateClassificationDivergent
		addRepositoryFailure(&fact, "branch", "repository checkout is detached or on an unexpected branch")
		return fact
	}
	if requireUpstream && !observation.UpstreamKnown {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "upstream", "repository upstream was not observed")
		return fact
	}
	if requireUpstream && (observation.Upstream.LocalBranch != current.DefaultBranch || observation.Upstream.Remote != current.Upstream.Remote || observation.Upstream.Merge != current.Upstream.Merge || observation.Upstream.FetchURL != current.Clone.URL) {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "upstream", "repository upstream contract does not match the manifest")
		return fact
	}
	if !validDriftObjectID(observation.Head) {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "head", "repository checkout has an invalid HEAD observation")
		return fact
	}
	if id == project.BaseRepository && !driftTrackedManifestObserved(observation) {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "tracked-manifest", "base checkout tracked manifest was not observed")
		return fact
	}
	if id == project.BaseRepository && !observation.TrackedManifestExact {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "tracked-manifest", "base checkout tracked manifest does not match the current generation")
		return fact
	}
	if current.Parent != "" && !driftIgnoreObserved(observation) {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "parent-ignore", "repository parent ignore coverage was not observed")
		return fact
	}
	if current.Parent != "" && !observation.IgnoreVerified {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "parent-ignore", "repository lacks committed parent ignore coverage")
		return fact
	}
	if requireAdvertisement && (!driftAdvertisedObserved(observation) || !validDriftObjectID(observation.AdvertisedCommit)) {
		fact.Classification = UpdateClassificationStructurallyInconsistent
		addRepositoryFailure(&fact, "advertised-ref", "selected remote ref is unavailable")
		return fact
	}
	if requireAdvertisement && observation.AdvertisedCommit != "" && observation.AdvertisedCommit != observation.Head {
		if observation.CanFastForward {
			fact.Classification = UpdateClassificationFastForwardable
		} else {
			fact.Classification = UpdateClassificationDivergent
			addRepositoryFailure(&fact, "ancestry", "selected remote ref is not a verified fast-forward")
		}
	}
	return fact
}

func driftAdvertisedObserved(observation DriftRepositoryObservation) bool {
	return observation.AdvertisedKnown || observation.AdvertisedCommit != ""
}
func driftTrackedManifestObserved(observation DriftRepositoryObservation) bool {
	return observation.TrackedManifestKnown || observation.TrackedManifestExact
}
func driftIgnoreObserved(observation DriftRepositoryObservation) bool {
	return observation.IgnoreKnown || observation.IgnoreVerified
}

func decodeDriftWorkspaces(snapshot *DriftSnapshot, generations []PersistedWorkspaceGeneration, project domain.Project) []DriftWorkspace {
	workspaces := make([]DriftWorkspace, 0, len(generations))
	seen := make(map[string]bool, len(generations))
	for _, generation := range generations {
		copied := append([]byte(nil), generation.Bytes...)
		state, err := store.DecodeWorkspace(copied)
		if err != nil {
			addDriftFailure(snapshot, "project", "workspace-state", fmt.Sprintf("decode persisted workspace state %q: %s", generation.Path, boundedRedactedDiagnostic(err.Error())))
			workspaces = append(workspaces, DriftWorkspace{Path: generation.Path, data: copied})
			continue
		}
		workspace, err := workspaceFromState(state)
		if err == nil {
			err = workspace.Validate(project)
		}
		if err != nil {
			addDriftFailure(snapshot, "project", "workspace-state", fmt.Sprintf("validate persisted workspace state %q: %s", generation.Path, boundedRedactedDiagnostic(err.Error())))
		}
		if state.ID == "" || !validDriftRepositoryID(state.ID) {
			addDriftFailure(snapshot, "project", "workspace-state", "persisted workspace state ID is invalid")
		} else if seen[state.ID] {
			addDriftFailure(snapshot, "project", "workspace-state", "duplicate persisted workspace state ID")
		}
		seen[state.ID] = true
		workspaces = append(workspaces, DriftWorkspace{Path: generation.Path, ID: state.ID, Name: state.Name, Partial: state.Partial, data: copied})
	}
	sort.Slice(workspaces, func(left, right int) bool {
		if workspaces[left].ID != workspaces[right].ID {
			return workspaces[left].ID < workspaces[right].ID
		}
		return workspaces[left].Path < workspaces[right].Path
	})
	return workspaces
}

func addManifestProjectFailures(snapshot *DriftSnapshot, manifest config.PortableManifest, project domain.Project, generation string) {
	if manifest.Project.ID != project.ID {
		addDriftFailure(snapshot, "project", generation+"-manifest-project", generation+" manifest project ID does not match local configuration")
	}
	if manifest.Project.BaseRepository != project.BaseRepository {
		addDriftFailure(snapshot, "project", generation+"-manifest-base", generation+" manifest base repository does not match local configuration")
	}
	if len(manifest.Repositories) != len(project.Repositories) {
		addDriftFailure(snapshot, "project", generation+"-manifest-repository-set", generation+" manifest repository set does not match local configuration")
	}
	for id, repository := range manifest.Repositories {
		var configured domain.Repository
		for _, candidate := range project.Repositories {
			if candidate.ID == id {
				configured = candidate
				break
			}
		}
		if configured.ID == "" || configured.ParentID != repository.Parent || configured.DefaultMount != repository.Mount || configured.DefaultBranch != repository.DefaultBranch {
			addDriftFailure(snapshot, id, generation+"-manifest-configuration", generation+" manifest repository does not match local configuration")
		}
	}
}
func addLocalConfigFailures(snapshot *DriftSnapshot, local config.ProjectConfig, project domain.Project, manifestPath, manifestSource string) {
	if err := local.Validate(); err != nil {
		addDriftFailure(snapshot, "project", "local-config", boundedRedactedDiagnostic(err.Error()))
		return
	}
	if local.Project.ID != project.ID || local.Project.BaseRepository != project.BaseRepository || local.Project.Name != project.Name {
		addDriftFailure(snapshot, "project", "local-config", "local configuration does not match resolved project")
	}
	configDirectory := filepath.Dir(project.ConfigPath)
	logicalRoot := filepath.Clean(filepath.Join(configDirectory, filepath.FromSlash(local.LogicalRoot)))
	if project.LogicalRoot == "" || !filepath.IsAbs(project.LogicalRoot) || filepath.Clean(project.LogicalRoot) != project.LogicalRoot || logicalRoot != project.LogicalRoot {
		addDriftFailure(snapshot, "project", "local-config", "local configuration logical root does not match resolved project")
	}
	if local.Manifest.Path != "project.wtree.yml" || local.Manifest.Source == "" {
		addDriftFailure(snapshot, "project", "local-config", "local configuration manifest path or source is not authoritative")
	}
	expectedManifestPath := filepath.Join(configDirectory, local.Manifest.Path)
	if manifestPath == "" || !filepath.IsAbs(manifestPath) || filepath.Clean(manifestPath) != manifestPath || manifestPath != expectedManifestPath || manifestSource != local.Manifest.Source {
		addDriftFailure(snapshot, "project", "local-config", "captured manifest path or source does not match local configuration")
	}
	if len(local.Repositories) != len(project.Repositories) {
		addDriftFailure(snapshot, "project", "local-config", "local configuration repository set does not match resolved project")
	}
	for id, repository := range local.Repositories {
		var configured domain.Repository
		for _, candidate := range project.Repositories {
			if candidate.ID == id {
				configured = candidate
				break
			}
		}
		if configured.ID == "" || configured.ParentID != repository.Parent || configured.DefaultMount != repository.DefaultMount || configured.DefaultBranch != repository.DefaultBranch {
			addDriftFailure(snapshot, id, "local-config", "local configuration repository does not match resolved project")
			continue
		}
		expectedSource := filepath.Clean(filepath.Join(logicalRoot, filepath.FromSlash(repository.Source)))
		if !filepath.IsAbs(configured.SourcePath) || filepath.Clean(configured.SourcePath) != configured.SourcePath || expectedSource != configured.SourcePath {
			addDriftFailure(snapshot, id, "local-config", "local configuration repository source does not match resolved path")
		}
		if configured.CommonGitDir == "" || !filepath.IsAbs(configured.CommonGitDir) || filepath.Clean(configured.CommonGitDir) != configured.CommonGitDir {
			addDriftFailure(snapshot, id, "local-config", "resolved repository Git identity is missing or non-canonical")
		}
		if id == project.BaseRepository && configured.SourcePath != configDirectory {
			addDriftFailure(snapshot, id, "local-config", "base repository source does not own the local configuration")
		}
	}
}
func addRegistryFailures(snapshot *DriftSnapshot, registry store.Registry, project domain.Project) {
	if registry.Version != store.Version {
		addDriftFailure(snapshot, "project", "registry-generation", "registry version is unsupported")
		return
	}
	registered, exists := registry.Projects[project.ID]
	if !exists || registered.Name != project.Name || !sameRepositoryIDs(registered.RepositoryIDs, repositoryIDs(project)) {
		addDriftFailure(snapshot, "project", "registry-generation", "registry project identity does not match the current project")
	} else if registered.ConfigPath == "" || !filepath.IsAbs(registered.ConfigPath) || filepath.Clean(registered.ConfigPath) != registered.ConfigPath || registered.ConfigPath != project.ConfigPath {
		addDriftFailure(snapshot, "project", "registry-generation", "registry configuration path does not match the current project")
	}
}
func indexDriftObservations(observations []DriftRepositoryObservation) (map[string]DriftRepositoryObservation, error) {
	indexed := make(map[string]DriftRepositoryObservation, len(observations))
	for _, observation := range observations {
		if !validDriftRepositoryID(observation.RepositoryID) {
			return nil, errors.New("drift observation repository ID is invalid")
		}
		if _, exists := indexed[observation.RepositoryID]; exists {
			return nil, errors.New("duplicate drift observation repository ID")
		}
		indexed[observation.RepositoryID] = observation
	}
	return indexed, nil
}
func candidateManifestParentFirst(manifest config.PortableManifest) []string {
	return driftManifestOrder(manifest, nil)
}
func currentManifestParentFirstRemoved(current, candidate config.PortableManifest) []string {
	keep := make(map[string]bool, len(candidate.Repositories))
	for id := range candidate.Repositories {
		keep[id] = true
	}
	return driftManifestOrder(current, func(id string) bool { return !keep[id] })
}
func driftManifestOrder(manifest config.PortableManifest, include func(string) bool) []string {
	depth := make(map[string]int, len(manifest.Repositories))
	var calculate func(string) int
	calculate = func(id string) int {
		if value, ok := depth[id]; ok {
			return value
		}
		repository := manifest.Repositories[id]
		if repository.Parent == "" {
			depth[id] = 0
		} else {
			depth[id] = calculate(repository.Parent) + 1
		}
		return depth[id]
	}
	ids := make([]string, 0, len(manifest.Repositories))
	for id := range manifest.Repositories {
		if include == nil || include(id) {
			ids = append(ids, id)
		}
		calculate(id)
	}
	sort.Slice(ids, func(left, right int) bool {
		if depth[ids[left]] != depth[ids[right]] {
			return depth[ids[left]] < depth[ids[right]]
		}
		return ids[left] < ids[right]
	})
	return ids
}
func hasDriftRepositorySetChange(current, candidate map[string]config.PortableRepository) bool {
	if len(current) != len(candidate) {
		return true
	}
	for id := range current {
		if _, exists := candidate[id]; !exists {
			return true
		}
	}
	return false
}
func hasNonDefaultDriftWorkspace(workspaces []DriftWorkspace) bool {
	for _, workspace := range workspaces {
		if workspace.ID != "default" || workspace.Partial {
			return true
		}
	}
	return false
}
func appendUnexpectedDriftObservations(snapshot *DriftSnapshot, observations map[string]DriftRepositoryObservation, current, candidate map[string]config.PortableRepository, retained []RetainedUnmanagedFact) {
	retainedIDs := make(map[string]bool, len(retained))
	for _, fact := range retained {
		if validRetainedUnmanagedFact(fact) {
			retainedIDs[fact.RepositoryID] = true
		}
	}
	for id, observation := range observations {
		if _, exists := current[id]; exists {
			continue
		}
		if _, exists := candidate[id]; exists {
			continue
		}
		if retainedIDs[id] {
			continue
		}
		fact := DriftRepository{ID: id, Path: observation.Path, Branch: observation.Branch, Head: observation.Head, ObservedCommit: observation.AdvertisedCommit, Classification: UpdateClassificationStructurallyInconsistent}
		addRepositoryFailure(&fact, "disk-only", "unexpected repository observation is not declared by either manifest")
		snapshot.repositories = append(snapshot.repositories, fact)
		snapshot.differences = append(snapshot.differences, DriftSetDifference{ID: id, Origin: "disk", Check: "disk-only"})
		appendRepositoryFailures(snapshot, fact)
	}
}
func validateRetainedDriftFacts(snapshot *DriftSnapshot, retained []RetainedUnmanagedFact, observations map[string]DriftRepositoryObservation, current, candidate map[string]config.PortableRepository) {
	retained = append([]RetainedUnmanagedFact(nil), retained...)
	sort.Slice(retained, func(left, right int) bool {
		if retained[left].RepositoryID != retained[right].RepositoryID {
			return retained[left].RepositoryID < retained[right].RepositoryID
		}
		if retained[left].Path != retained[right].Path {
			return retained[left].Path < retained[right].Path
		}
		return retained[left].CommonGitDir < retained[right].CommonGitDir
	})
	seen := map[string]bool{}
	for _, fact := range retained {
		if !validRetainedUnmanagedFact(fact) {
			addDriftFailure(snapshot, "project", "retained-unmanaged", "retained unmanaged evidence is malformed")
			continue
		}
		if seen[fact.RepositoryID] {
			addDriftFailure(snapshot, fact.RepositoryID, "retained-unmanaged", "retained unmanaged evidence is duplicated")
			continue
		}
		seen[fact.RepositoryID] = true
		observation, observed := observations[fact.RepositoryID]
		_, wasCurrent := current[fact.RepositoryID]
		_, stillCandidate := candidate[fact.RepositoryID]
		if !wasCurrent && !stillCandidate {
			if !hasDriftRepository(snapshot.repositories, fact.RepositoryID) {
				snapshot.repositories = append(snapshot.repositories, DriftRepository{ID: fact.RepositoryID, Path: fact.Path, Classification: UpdateClassificationRemovedRetained})
			}
			appendUniqueDriftDifference(snapshot, DriftSetDifference{ID: fact.RepositoryID, Origin: "retained", Check: "retained-only"})
			index := driftRepositoryIndex(snapshot.repositories, fact.RepositoryID)
			if observed {
				snapshot.repositories[index].Path = observation.Path
				snapshot.repositories[index].Branch = observation.Branch
				snapshot.repositories[index].Head = observation.Head
				snapshot.repositories[index].ObservedCommit = observation.AdvertisedCommit
			}
			if !observed || observation.TargetAbsent {
				addSnapshotRepositoryFailure(snapshot, index, "retained-unmanaged", "prior retained repository checkout is missing")
			} else if filepath.Clean(observation.Path) != fact.Path {
				addSnapshotRepositoryFailure(snapshot, index, "path", "prior retained repository path does not match retained evidence")
			} else if observation.CommonGitDir != fact.CommonGitDir || !observation.IdentityKnown || !observation.IdentityMatches {
				addSnapshotRepositoryFailure(snapshot, index, "identity", "prior retained repository Git identity does not match retained evidence")
			}
			continue
		}
		if stillCandidate {
			if index := driftRepositoryIndex(snapshot.repositories, fact.RepositoryID); index >= 0 {
				addSnapshotRepositoryFailure(snapshot, index, "retained-unmanaged", "retained unmanaged repository conflicts with a candidate repository")
			} else {
				addDriftFailure(snapshot, fact.RepositoryID, "retained-unmanaged", "retained unmanaged repository conflicts with a candidate repository")
			}
			continue
		}
		if !observed || observation.TargetAbsent || filepath.Clean(observation.Path) != fact.Path || observation.CommonGitDir != fact.CommonGitDir || !observation.IdentityKnown || !observation.IdentityMatches {
			if index := driftRepositoryIndex(snapshot.repositories, fact.RepositoryID); index >= 0 {
				addSnapshotRepositoryFailure(snapshot, index, "retained-unmanaged", "removed repository retention evidence does not match the observed checkout")
			} else {
				addDriftFailure(snapshot, fact.RepositoryID, "retained-unmanaged", "removed repository retention evidence does not match the observed checkout")
			}
		}
	}
}

func validRetainedUnmanagedFact(fact RetainedUnmanagedFact) bool {
	return validDriftRepositoryID(fact.RepositoryID) && fact.Path != "" && fact.CommonGitDir != "" && filepath.IsAbs(fact.Path) && filepath.Clean(fact.Path) == fact.Path && filepath.IsAbs(fact.CommonGitDir) && filepath.Clean(fact.CommonGitDir) == fact.CommonGitDir
}

func driftRepositoryIndex(repositories []DriftRepository, id string) int {
	for index := range repositories {
		if repositories[index].ID == id {
			return index
		}
	}
	return -1
}

func appendUniqueDriftDifference(snapshot *DriftSnapshot, difference DriftSetDifference) {
	for _, existing := range snapshot.differences {
		if existing == difference {
			return
		}
	}
	snapshot.differences = append(snapshot.differences, difference)
}

func addSnapshotRepositoryFailure(snapshot *DriftSnapshot, index int, check, message string) {
	failure := DriftFailure{RepositoryID: snapshot.repositories[index].ID, Check: check, Message: boundedRedactedDiagnostic(message)}
	snapshot.repositories[index].Failures = append(snapshot.repositories[index].Failures, failure)
	snapshot.failures = append(snapshot.failures, failure)
}
func hasDriftRepository(repositories []DriftRepository, id string) bool {
	for _, repository := range repositories {
		if repository.ID == id {
			return true
		}
	}
	return false
}
func appendStateOnlyDriftFacts(snapshot *DriftSnapshot, generations []PersistedWorkspaceGeneration, current, candidate map[string]config.PortableRepository) {
	seen := map[string]bool{}
	for _, generation := range generations {
		state, err := store.DecodeWorkspace(generation.Bytes)
		if err != nil {
			continue
		}
		for id := range state.Repositories {
			if !validDriftRepositoryID(id) {
				addDriftFailure(snapshot, "project", "workspace-state", "persisted workspace references an invalid repository ID")
				continue
			}
			if _, ok := current[id]; ok {
				continue
			}
			if _, ok := candidate[id]; ok {
				continue
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			if hasDriftRepository(snapshot.repositories, id) {
				snapshot.differences = append(snapshot.differences, DriftSetDifference{ID: id, Origin: "state", Check: "state-only"})
				continue
			}
			fact := DriftRepository{ID: id, Classification: UpdateClassificationStructurallyInconsistent}
			addRepositoryFailure(&fact, "state-only", "persisted workspace references an undeclared repository")
			snapshot.repositories = append(snapshot.repositories, fact)
			snapshot.differences = append(snapshot.differences, DriftSetDifference{ID: id, Origin: "state", Check: "state-only"})
			appendRepositoryFailures(snapshot, fact)
		}
	}
}
func sortDriftRepositories(repositories []DriftRepository, current, candidate config.PortableManifest) {
	parents := map[string]string{}
	for id, repository := range current.Repositories {
		parents[id] = repository.Parent
	}
	for id, repository := range candidate.Repositories {
		parents[id] = repository.Parent
	}
	depth := map[string]int{}
	var calculate func(string) int
	calculate = func(id string) int {
		if value, ok := depth[id]; ok {
			return value
		}
		parent, ok := parents[id]
		if !ok || parent == "" {
			depth[id] = 0
			return 0
		}
		depth[id] = calculate(parent) + 1
		return depth[id]
	}
	sort.Slice(repositories, func(left, right int) bool {
		dl, dr := calculate(repositories[left].ID), calculate(repositories[right].ID)
		if dl != dr {
			return dl < dr
		}
		return repositories[left].ID < repositories[right].ID
	})
}
func addRepositoryFailure(repository *DriftRepository, check, message string) {
	repository.Failures = append(repository.Failures, DriftFailure{RepositoryID: repository.ID, Check: check, Message: boundedRedactedDiagnostic(message)})
}
func appendRepositoryFailures(snapshot *DriftSnapshot, repository DriftRepository) {
	snapshot.failures = append(snapshot.failures, repository.Failures...)
}
func addDriftFailure(snapshot *DriftSnapshot, repositoryID, check, message string) {
	snapshot.failures = append(snapshot.failures, DriftFailure{RepositoryID: repositoryID, Check: check, Message: boundedRedactedDiagnostic(message)})
}
func sortDriftFailures(failures []DriftFailure) {
	sort.SliceStable(failures, func(left, right int) bool {
		if failures[left].RepositoryID != failures[right].RepositoryID {
			return failures[left].RepositoryID < failures[right].RepositoryID
		}
		if failures[left].Check != failures[right].Check {
			return failures[left].Check < failures[right].Check
		}
		return failures[left].Message < failures[right].Message
	})
}

func normalizeDriftFailuresInRepositoryOrder(failures []DriftFailure, fallbackCheck string, repositoryOrder []string) []DriftFailure {
	normalized := normalizeDriftFailures(failures, fallbackCheck)
	rank := make(map[string]int, len(repositoryOrder))
	for index, id := range repositoryOrder {
		if _, exists := rank[id]; !exists {
			rank[id] = index
		}
	}
	if len(rank) == 0 {
		return normalized
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		leftRank, leftKnown := rank[normalized[left].RepositoryID]
		rightRank, rightKnown := rank[normalized[right].RepositoryID]
		if leftKnown && rightKnown && leftRank != rightRank {
			return leftRank < rightRank
		}
		if leftKnown != rightKnown {
			return leftKnown
		}
		if normalized[left].RepositoryID != normalized[right].RepositoryID {
			return normalized[left].RepositoryID < normalized[right].RepositoryID
		}
		if normalized[left].Check != normalized[right].Check {
			return normalized[left].Check < normalized[right].Check
		}
		return normalized[left].Message < normalized[right].Message
	})
	return normalized
}
func digestDriftBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
func validDriftObjectID(value string) bool     { return aggregateObjectID(value) }
func validDriftRepositoryID(value string) bool { return config.ValidatePortableID(value) == nil }
func cloneDriftProject(value domain.Project) domain.Project {
	copied := value
	copied.DiscoveryIgnores = append([]string(nil), value.DiscoveryIgnores...)
	copied.Repositories = append([]domain.Repository(nil), value.Repositories...)
	return copied
}
func cloneDriftDomainWorkspace(value domain.Workspace) domain.Workspace {
	copied := value
	copied.MissingRepositoryIDs = append([]string(nil), value.MissingRepositoryIDs...)
	copied.Checkouts = append([]domain.Checkout(nil), value.Checkouts...)
	if value.Recovery != nil {
		recovery := *value.Recovery
		recovery.CompletedSteps = append([]string(nil), value.Recovery.CompletedSteps...)
		copied.Recovery = &recovery
	}
	return copied
}
func cloneDriftRepositories(value []DriftRepository) []DriftRepository {
	copied := append([]DriftRepository(nil), value...)
	for index := range copied {
		copied[index].Failures = append([]DriftFailure(nil), value[index].Failures...)
	}
	return copied
}
func cloneDriftWorkspaces(value []DriftWorkspace) []DriftWorkspace {
	copied := append([]DriftWorkspace(nil), value...)
	for index := range copied {
		copied[index].data = append([]byte(nil), value[index].data...)
	}
	return copied
}
func cloneDriftOperations(value []DriftOperationRecord) []DriftOperationRecord {
	copied := append([]DriftOperationRecord(nil), value...)
	for index := range copied {
		copied[index].Diagnostic = boundedRedactedDiagnostic(copied[index].Diagnostic)
	}
	return copied
}

func cloneDriftSnapshotInput(value DriftSnapshotInput) DriftSnapshotInput {
	copied := value
	copied.Project = cloneDriftProject(value.Project)
	copied.DefaultWorkspace = cloneDriftDomainWorkspace(value.DefaultWorkspace)
	copied.CurrentManifest = append([]byte(nil), value.CurrentManifest...)
	copied.CandidateManifest = append([]byte(nil), value.CandidateManifest...)
	copied.LocalConfigBytes = append([]byte(nil), value.LocalConfigBytes...)
	copied.RegistryBytes = append([]byte(nil), value.RegistryBytes...)
	copied.DefaultState.Bytes = append([]byte(nil), value.DefaultState.Bytes...)
	copied.Reconciliation.Bytes = append([]byte(nil), value.Reconciliation.Bytes...)
	copied.PersistedWorkspaces = append([]PersistedWorkspaceGeneration(nil), value.PersistedWorkspaces...)
	for index := range copied.PersistedWorkspaces {
		copied.PersistedWorkspaces[index].Bytes = append([]byte(nil), value.PersistedWorkspaces[index].Bytes...)
	}
	copied.RetainedUnmanaged = append([]RetainedUnmanagedFact(nil), value.RetainedUnmanaged...)
	copied.Operations = cloneDriftOperations(value.Operations)
	copied.Observations = append([]DriftRepositoryObservation(nil), value.Observations...)
	copied.Collection.Errors = append([]DriftFailure(nil), value.Collection.Errors...)
	copied.CollectionFailureOrder = append([]string(nil), value.CollectionFailureOrder...)
	if value.LocalConfig != nil {
		local := *value.LocalConfig
		local.Repositories = make(map[string]config.Repository, len(value.LocalConfig.Repositories))
		for id, repository := range value.LocalConfig.Repositories {
			local.Repositories[id] = repository
		}
		local.Discovery.Ignore = append([]string(nil), value.LocalConfig.Discovery.Ignore...)
		copied.LocalConfig = &local
	}
	if value.Registry != nil {
		registry := *value.Registry
		registry.Projects = make(map[string]store.RegistryProject, len(value.Registry.Projects))
		for id, project := range value.Registry.Projects {
			project.RepositoryIDs = map[string]string{}
			for identity, repositoryID := range value.Registry.Projects[id].RepositoryIDs {
				project.RepositoryIDs[identity] = repositoryID
			}
			registry.Projects[id] = project
		}
		copied.Registry = &registry
	}
	return copied
}
