package service

// This file owns the M02 update plan: an immutable, renderable decision made
// from the M01 snapshot.  It deliberately contains no executor and has no
// filesystem, Git, lock, or publication dependency.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
)

const UpdatePlanVersion = 1

type UpdatePlanSource struct {
	Kind   ManifestSourceKind `json:"kind"`
	Value  string             `json:"value"`
	SHA256 string             `json:"sha256"`
}

type UpdatePlanGenerations struct {
	CurrentManifestSHA256   string `json:"currentManifestSha256"`
	CandidateManifestSHA256 string `json:"candidateManifestSha256"`
	LocalConfigSHA256       string `json:"localConfigSha256"`
	RegistrySHA256          string `json:"registrySha256"`
	DefaultStateSHA256      string `json:"defaultStateSha256"`
	ReconciliationSHA256    string `json:"reconciliationSha256,omitempty"`
}

type UpdatePlanRepository struct {
	ID             string               `json:"id"`
	ParentID       string               `json:"parentId,omitempty"`
	Mount          string               `json:"mount"`
	Path           string               `json:"path,omitempty"`
	Classification UpdateClassification `json:"classification"`
	Head           string               `json:"head,omitempty"`
	ObservedCommit string               `json:"observedCommit,omitempty"`
}

type UpdatePlanAction struct {
	Sequence     int    `json:"sequence"`
	Action       string `json:"action"`
	RepositoryID string `json:"repositoryId,omitempty"`
}

type UpdatePlanVerification struct {
	SnapshotComplete bool `json:"snapshotComplete"`
	NoRelocation     bool `json:"noRelocation"`
	NoDeletion       bool `json:"noDeletion"`
	NoMutation       bool `json:"noMutation"`
}

// UpdatePlan is a private-byte-bearing plan for a future executor. Candidate
// bytes intentionally have no JSON field and are returned only as a copy.
type UpdatePlan struct {
	Version      int                    `json:"version"`
	Operation    string                 `json:"operation"`
	Source       UpdatePlanSource       `json:"source"`
	Generations  UpdatePlanGenerations  `json:"generations"`
	Verification UpdatePlanVerification `json:"verification"`
	private      *updatePlanPrivate
}

// updatePlanPrivate contains every mutable representation. It is never
// exposed: ordinary UpdatePlan value copies share this immutable owner and all
// accessors return their own copies.
type updatePlanPrivate struct {
	repositories  []UpdatePlanRepository
	actions       []UpdatePlanAction
	publication   []string
	rollback      []string
	candidateData []byte
	factsDigest   string
	baseline      updateExecutionBaseline
}

// updateExecutionBaseline is the executor-only authority captured together
// with the M01 snapshot. It never reaches the public plan JSON. Keep this
// value complete: M03 compares a single fresh locked recapture against it,
// rather than mixing newly observed Git facts with stale planning facts.
type updateExecutionBaseline struct {
	dataDir        string
	project        domain.Project
	workspace      domain.Workspace
	current        DriftManifestGeneration
	defaultState   PersistedWorkspaceGeneration
	localConfig    []byte
	registry       []byte
	reconciliation DriftFileGeneration
	repositories   []DriftRepository
	observations   []DriftRepositoryObservation
	candidate      []byte
}

// UpdatePlanner is deliberately dependency-light: its caller supplies the
// one authoritative M01 capture and uses ManifestSourceLoader for the single
// candidate-source observation before it is captured.
type UpdatePlanner struct{ loader *ManifestSourceLoader }

func NewUpdatePlanner() *UpdatePlanner { return &UpdatePlanner{loader: NewManifestSourceLoader()} }

func NewUpdatePlannerWithLoader(loader *ManifestSourceLoader) *UpdatePlanner {
	if loader == nil {
		loader = NewManifestSourceLoader()
	}
	return &UpdatePlanner{loader: loader}
}

// LoadCandidate applies the fixed update source precedence. The override is
// never persisted by this M02 dry-run boundary.
func (planner *UpdatePlanner) LoadCandidate(ctx context.Context, storedSource, override string) (LoadedManifestSource, error) {
	if planner == nil || planner.loader == nil {
		planner = NewUpdatePlanner()
	}
	source := storedSource
	if override != "" {
		source = override
	}
	return planner.loader.Load(ctx, source)
}

func (planner *UpdatePlanner) Plan(ctx context.Context, snapshot DriftSnapshot, storedSource, override string) (UpdatePlan, error) {
	if err := ctx.Err(); err != nil {
		return UpdatePlan{}, err
	}
	loaded, err := planner.LoadCandidate(ctx, storedSource, override)
	if err != nil {
		return UpdatePlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return UpdatePlan{}, err
	}
	return BuildUpdatePlan(snapshot, loaded)
}

// ExecuteUpdateDryRun is the service dry-run boundary. It intentionally does
// not own an executor; its only effect is the returned immutable plan.
func ExecuteUpdateDryRun(ctx context.Context, planner *UpdatePlanner, snapshot DriftSnapshot, storedSource, override string) (UpdatePlan, error) {
	if planner == nil {
		planner = NewUpdatePlanner()
	}
	return planner.Plan(ctx, snapshot, storedSource, override)
}

func (plan UpdatePlan) CandidateManifestBytes() []byte {
	if plan.private == nil {
		return nil
	}
	return append([]byte(nil), plan.private.candidateData...)
}

// Repositories, Actions, Publication, and Rollback return defensive copies.
// They are the only public access to plan-owned collections.
func (plan UpdatePlan) Repositories() []UpdatePlanRepository {
	if plan.private == nil {
		return nil
	}
	return append([]UpdatePlanRepository(nil), plan.private.repositories...)
}

func (plan UpdatePlan) Actions() []UpdatePlanAction {
	if plan.private == nil {
		return nil
	}
	return append([]UpdatePlanAction(nil), plan.private.actions...)
}

func (plan UpdatePlan) Publication() []string {
	if plan.private == nil {
		return nil
	}
	return append([]string(nil), plan.private.publication...)
}

func (plan UpdatePlan) Rollback() []string {
	if plan.private == nil {
		return nil
	}
	return append([]string(nil), plan.private.rollback...)
}

// executionBaseline is intentionally package-private. The M03 executor gets
// only a defensive copy, never an alias into an immutable plan value.
func (plan UpdatePlan) executionBaseline() updateExecutionBaseline {
	if plan.private == nil {
		return updateExecutionBaseline{}
	}
	return cloneUpdateExecutionBaseline(plan.private.baseline)
}

// JSON validates before rendering so a caller cannot accidentally expose an
// incomplete or tampered plan as an authoritative dry-run result.
func (plan UpdatePlan) JSON() ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// MarshalJSON has a dedicated projection so the private candidate bytes and
// internal digest remain absent even when callers render the plan directly.
func (plan UpdatePlan) MarshalJSON() ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version      int                    `json:"version"`
		Operation    string                 `json:"operation"`
		Source       UpdatePlanSource       `json:"source"`
		Generations  UpdatePlanGenerations  `json:"generations"`
		Repositories []UpdatePlanRepository `json:"repositories"`
		Actions      []UpdatePlanAction     `json:"actions"`
		Verification UpdatePlanVerification `json:"verification"`
		Publication  []string               `json:"publication"`
		Rollback     []string               `json:"rollback"`
	}{plan.Version, plan.Operation, plan.Source, plan.Generations, plan.Repositories(), plan.Actions(), plan.Verification, plan.Publication(), plan.Rollback()})
}

// BuildUpdatePlan converts exactly one immutable snapshot and loaded source
// into a deterministic plan. It never re-observes a project.
func BuildUpdatePlan(snapshot DriftSnapshot, source LoadedManifestSource) (UpdatePlan, error) {
	if !snapshot.MayUpdate() {
		return UpdatePlan{}, NewError(ErrorValidation, errors.New("update preflight has refusal findings"))
	}
	if !validUpdatePlanSource(source.Kind, source.Source) {
		return UpdatePlan{}, NewError(ErrorValidation, errors.New("update plan source is incomplete"))
	}
	candidate := source.Bytes()
	if len(candidate) == 0 || !reflect.DeepEqual(candidate, snapshot.CandidateManifestBytes()) {
		return UpdatePlan{}, NewError(ErrorConflict, errors.New("candidate source bytes do not match the captured snapshot"))
	}
	repositories := snapshot.Repositories()
	planRepositories := make([]UpdatePlanRepository, 0, len(repositories))
	for _, repository := range repositories {
		planRepositories = append(planRepositories, UpdatePlanRepository{ID: repository.ID, ParentID: repository.ParentID, Mount: repository.Mount, Path: repository.Path, Classification: repository.Classification, Head: repository.Head, ObservedCommit: repository.ObservedCommit})
	}
	actions := make([]UpdatePlanAction, 0, len(planRepositories))
	for index, repository := range planRepositories {
		action := "verify"
		switch repository.Classification {
		case UpdateClassificationFastForwardable:
			action = "fast-forward"
		case UpdateClassificationAdded:
			action = "add"
		case UpdateClassificationRemovedRetained:
			action = "retain-unmanaged"
		case UpdateClassificationUnchanged:
			action = "unchanged"
		}
		actions = append(actions, UpdatePlanAction{Sequence: index + 1, Action: action, RepositoryID: repository.ID})
	}
	current := snapshot.CurrentManifestGeneration()
	plan := UpdatePlan{
		Version: UpdatePlanVersion, Operation: "update",
		Source:       UpdatePlanSource{Kind: source.Kind, Value: source.Source, SHA256: sha256String(candidate)},
		Generations:  UpdatePlanGenerations{CurrentManifestSHA256: snapshot.CurrentManifestSHA256(), CandidateManifestSHA256: snapshot.CandidateManifestSHA256(), LocalConfigSHA256: sha256String(snapshot.LocalConfigBytes()), RegistrySHA256: sha256String(snapshot.RegistryBytes()), DefaultStateSHA256: sha256String(snapshot.DefaultState().Bytes), ReconciliationSHA256: optionalSHA256(snapshot.ReconciliationGeneration().Bytes)},
		Verification: UpdatePlanVerification{SnapshotComplete: snapshot.CollectionEvidence().Complete(), NoRelocation: true, NoDeletion: true, NoMutation: true},
		private: &updatePlanPrivate{
			repositories:  append([]UpdatePlanRepository(nil), planRepositories...),
			actions:       append([]UpdatePlanAction(nil), actions...),
			publication:   []string{"local-config", "default-workspace", "registry", "reconciliation"},
			rollback:      []string{"repository-effects", "local-config", "default-workspace", "registry", "reconciliation"},
			candidateData: append([]byte(nil), candidate...),
			baseline:      newUpdateExecutionBaseline(snapshot),
		},
	}
	plan.private.factsDigest = updatePlanFactsDigest(plan)
	if current.Source == "" {
		return UpdatePlan{}, NewError(ErrorValidation, errors.New("captured current manifest source is missing"))
	}
	if err := plan.Validate(); err != nil {
		return UpdatePlan{}, NewError(ErrorValidation, err)
	}
	return plan, nil
}

func validUpdatePlanSource(kind ManifestSourceKind, source string) bool {
	if source == "" || config.ValidateManifestSource(source) != nil {
		return false
	}
	isHTTP := strings.HasPrefix(strings.ToLower(source), "http://") || strings.HasPrefix(strings.ToLower(source), "https://")
	return (kind == ManifestSourceHTTP && isHTTP) || (kind == ManifestSourceLocal && !isHTTP)
}

func sha256String(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func optionalSHA256(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return sha256String(data)
}

func validUpdateSHA256(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil && len(value) == sha256.Size*2 && value == strings.ToLower(value)
}

func (plan UpdatePlan) Validate() error {
	if plan.Version != UpdatePlanVersion || plan.Operation != "update" {
		return errors.New("invalid update plan version or operation")
	}
	if !validUpdatePlanSource(plan.Source.Kind, plan.Source.Value) || !validUpdateSHA256(plan.Source.SHA256) {
		return errors.New("invalid update plan source")
	}
	for _, digest := range []string{plan.Generations.CurrentManifestSHA256, plan.Generations.CandidateManifestSHA256, plan.Generations.LocalConfigSHA256, plan.Generations.RegistrySHA256, plan.Generations.DefaultStateSHA256} {
		if !validUpdateSHA256(digest) {
			return errors.New("incomplete update plan generations")
		}
	}
	if plan.Generations.ReconciliationSHA256 != "" && !validUpdateSHA256(plan.Generations.ReconciliationSHA256) {
		return errors.New("invalid update reconciliation generation")
	}
	if plan.Source.SHA256 != plan.Generations.CandidateManifestSHA256 {
		return errors.New("update source and candidate generation digest disagree")
	}
	if !plan.Verification.SnapshotComplete || !plan.Verification.NoRelocation || !plan.Verification.NoDeletion || !plan.Verification.NoMutation {
		return errors.New("incomplete update verification contract")
	}
	if plan.private == nil {
		return errors.New("missing private update plan data")
	}
	if !reflect.DeepEqual(plan.private.publication, []string{"local-config", "default-workspace", "registry", "reconciliation"}) || !reflect.DeepEqual(plan.private.rollback, []string{"repository-effects", "local-config", "default-workspace", "registry", "reconciliation"}) {
		return errors.New("invalid update publication or rollback ownership")
	}
	if len(plan.private.repositories) == 0 || len(plan.private.actions) != len(plan.private.repositories) {
		return errors.New("incomplete update plan repositories or actions")
	}
	seen := map[string]bool{}
	for index, repository := range plan.private.repositories {
		if repository.ID == "" || seen[repository.ID] || (repository.ParentID != "" && !seen[repository.ParentID]) || repository.Classification == "" {
			return fmt.Errorf("invalid parent-first update repository %q", repository.ID)
		}
		seen[repository.ID] = true
		want := "verify"
		switch repository.Classification {
		case UpdateClassificationFastForwardable:
			want = "fast-forward"
		case UpdateClassificationAdded:
			want = "add"
		case UpdateClassificationRemovedRetained:
			want = "retain-unmanaged"
		case UpdateClassificationUnchanged:
			want = "unchanged"
		}
		if plan.private.actions[index] != (UpdatePlanAction{Sequence: index + 1, Action: want, RepositoryID: repository.ID}) {
			return errors.New("invalid update action ordering or facts")
		}
	}
	if len(plan.private.candidateData) == 0 || sha256String(plan.private.candidateData) != plan.Source.SHA256 || sha256String(plan.private.candidateData) != plan.Generations.CandidateManifestSHA256 {
		return errors.New("candidate bytes do not match update plan digest")
	}
	if err := validateUpdateExecutionBaseline(plan.private.baseline, plan); err != nil {
		return err
	}
	if plan.private.factsDigest == "" || plan.private.factsDigest != updatePlanFactsDigest(plan) {
		return errors.New("update plan facts do not match their captured generation")
	}
	return nil
}

func updatePlanFactsDigest(plan UpdatePlan) string {
	value := struct {
		Source        UpdatePlanSource
		Generations   UpdatePlanGenerations
		Repositories  []UpdatePlanRepository
		Actions       []UpdatePlanAction
		Verification  UpdatePlanVerification
		Publication   []string
		Rollback      []string
		CandidateData []byte
		Baseline      updateExecutionBaseline
	}{plan.Source, plan.Generations, plan.Repositories(), plan.Actions(), plan.Verification, plan.Publication(), plan.Rollback(), plan.CandidateManifestBytes(), plan.executionBaseline()}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return sha256String(data)
}

func newUpdateExecutionBaseline(snapshot DriftSnapshot) updateExecutionBaseline {
	return updateExecutionBaseline{dataDir: snapshot.dataDir, project: snapshot.Project(), workspace: snapshot.DefaultWorkspace(), current: snapshot.CurrentManifestGeneration(), defaultState: snapshot.DefaultState(), localConfig: snapshot.LocalConfigBytes(), registry: snapshot.RegistryBytes(), reconciliation: snapshot.ReconciliationGeneration(), repositories: snapshot.Repositories(), observations: snapshot.Observations(), candidate: snapshot.CandidateManifestBytes()}
}

func cloneUpdateExecutionBaseline(value updateExecutionBaseline) updateExecutionBaseline {
	copied := value
	copied.project = cloneDriftProject(value.project)
	copied.workspace = cloneDriftDomainWorkspace(value.workspace)
	copied.current.Bytes = append([]byte(nil), value.current.Bytes...)
	copied.defaultState.Bytes = append([]byte(nil), value.defaultState.Bytes...)
	copied.localConfig = append([]byte(nil), value.localConfig...)
	copied.registry = append([]byte(nil), value.registry...)
	copied.reconciliation.Bytes = append([]byte(nil), value.reconciliation.Bytes...)
	copied.repositories = cloneDriftRepositories(value.repositories)
	copied.observations = append([]DriftRepositoryObservation(nil), value.observations...)
	copied.candidate = append([]byte(nil), value.candidate...)
	return copied
}

func validateUpdateExecutionBaseline(value updateExecutionBaseline, plan UpdatePlan) error {
	if value.dataDir == "" || !filepath.IsAbs(value.dataDir) || filepath.Clean(value.dataDir) != value.dataDir || value.project.ID == "" || value.project.ID != plan.executionBaseline().project.ID || len(value.candidate) == 0 || !bytes.Equal(value.candidate, plan.CandidateManifestBytes()) {
		return errors.New("invalid private update execution baseline")
	}
	if value.current.Path == "" || len(value.current.Bytes) == 0 || len(value.localConfig) == 0 || len(value.registry) == 0 || len(value.defaultState.Bytes) == 0 || len(value.repositories) != len(plan.Repositories()) || len(value.observations) == 0 {
		return errors.New("incomplete private update execution baseline")
	}
	if sha256String(value.current.Bytes) != plan.Generations.CurrentManifestSHA256 || sha256String(value.localConfig) != plan.Generations.LocalConfigSHA256 || sha256String(value.registry) != plan.Generations.RegistrySHA256 || sha256String(value.defaultState.Bytes) != plan.Generations.DefaultStateSHA256 || optionalSHA256(value.reconciliation.Bytes) != plan.Generations.ReconciliationSHA256 {
		return errors.New("private update execution baseline generations disagree")
	}
	for index, repository := range value.repositories {
		public := plan.Repositories()[index]
		if repository.ID != public.ID || repository.ParentID != public.ParentID || repository.Mount != public.Mount || repository.Path != public.Path || repository.Classification != public.Classification || repository.Head != public.Head || repository.ObservedCommit != public.ObservedCommit {
			return errors.New("private update execution baseline repository facts disagree")
		}
	}
	return nil
}

func updatePlanRepositoryIDs(plan UpdatePlan) []string {
	repositories := plan.Repositories()
	ids := make([]string, len(repositories))
	for i := range repositories {
		ids[i] = repositories[i].ID
	}
	return ids
}

// StableUpdatePlanRepositories is a small rendering helper that prevents
// callers from depending on the private candidate byte slice.
func StableUpdatePlanRepositories(plan UpdatePlan) []UpdatePlanRepository {
	return plan.Repositories()
}
