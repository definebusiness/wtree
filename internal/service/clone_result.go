package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/definebusiness/wtree/internal/config"
)

const CloneResultVersion = 1

type CloneResultStatus string

const (
	CloneResultPlanned CloneResultStatus = "planned"
	CloneResultFailed  CloneResultStatus = "failed"
)

type CloneResultStage string

const (
	CloneResultStageSource      CloneResultStage = "source_load"
	CloneResultStageDecode      CloneResultStage = "decode"
	CloneResultStageDestination CloneResultStage = "destination"
	CloneResultStageRegistry    CloneResultStage = "registry"
	CloneResultStageRemote      CloneResultStage = "remote"
	CloneResultStageInternal    CloneResultStage = "internal"
)

// CloneResultRequestSource is present only after the manifest source has been
// normalized and proven credential-free. It deliberately contains no raw CLI
// input or destination decision.
type CloneResultRequestSource struct {
	Kind  ManifestSourceKind `json:"kind"`
	Value string             `json:"value"`
}

type CloneRepositoryOutcome struct {
	ID               string            `json:"id"`
	Parent           string            `json:"parent"`
	Mount            string            `json:"mount"`
	Path             string            `json:"path"`
	CloneRemote      string            `json:"cloneRemote"`
	CloneURL         string            `json:"cloneUrl"`
	LocalBranch      string            `json:"localBranch"`
	RemoteRef        string            `json:"remoteRef"`
	AdvertisedCommit string            `json:"advertisedCommit"`
	Status           string            `json:"status"`
	Verification     CloneVerification `json:"verification"`
}

type CloneResultFailure struct {
	Stage   CloneResultStage `json:"stage"`
	Code    ErrorKind        `json:"code"`
	Message string           `json:"message"`
}

// CloneResult is the stable v1 read-only planning outcome. A planned result
// owns a complete validated plan. A failed result owns only credential-free
// provenance known before the failing boundary and never fabricates a plan.
type CloneResult struct {
	Version       int                       `json:"version"`
	Operation     string                    `json:"operation"`
	Status        CloneResultStatus         `json:"status"`
	DryRun        bool                      `json:"dryRun"`
	RequestSource *CloneResultRequestSource `json:"requestSource,omitempty"`
	Source        *ClonePlanSource          `json:"source,omitempty"`
	Plan          *ClonePlan                `json:"plan,omitempty"`
	Repositories  []CloneRepositoryOutcome  `json:"repositories,omitempty"`
	Failure       *CloneResultFailure       `json:"failure,omitempty"`
}

func NewCloneResult(plan ClonePlan) (CloneResult, error) {
	if err := plan.Validate(); err != nil {
		return CloneResult{}, err
	}
	ownedPlan := clonePlanCopy(plan)
	result := CloneResult{
		Version: CloneResultVersion, Operation: "clone", Status: CloneResultPlanned, DryRun: true,
		Plan: &ownedPlan, Repositories: cloneResultOutcomes(ownedPlan),
	}
	if err := result.Validate(); err != nil {
		return CloneResult{}, err
	}
	return result, nil
}

func newCloneFailureResult(requestSource *CloneResultRequestSource, source *ClonePlanSource, stage CloneResultStage, cause error) (CloneResult, error) {
	if cause == nil {
		return CloneResult{}, errors.New("clone failure result requires a cause")
	}
	failure := &CloneResultFailure{Stage: stage, Code: cloneResultErrorKind(cause), Message: boundedRedactedDiagnostic(cause.Error())}
	result := CloneResult{
		Version: CloneResultVersion, Operation: "clone", Status: CloneResultFailed, DryRun: true,
		RequestSource: cloneResultRequestSourceCopy(requestSource), Source: cloneResultSourceCopy(source), Failure: failure,
	}
	if err := result.Validate(); err != nil {
		return CloneResult{}, err
	}
	return result, nil
}

func cloneResultErrorKind(cause error) ErrorKind {
	var applicationError *Error
	if errors.As(cause, &applicationError) && validCloneResultErrorKind(applicationError.Kind) {
		return applicationError.Kind
	}
	return ErrorInternal
}

func cloneResultRequestSourceCopy(source *CloneResultRequestSource) *CloneResultRequestSource {
	if source == nil {
		return nil
	}
	copyOfSource := *source
	return &copyOfSource
}

func cloneResultSourceCopy(source *ClonePlanSource) *ClonePlanSource {
	if source == nil {
		return nil
	}
	copyOfSource := *source
	return &copyOfSource
}

func clonePlanCopy(plan ClonePlan) ClonePlan {
	copyOfPlan := plan
	copyOfPlan.Destination.AncestorFacts = append([]ClonePathFact(nil), plan.Destination.AncestorFacts...)
	copyOfPlan.Repositories = append([]ClonePlanRepository(nil), plan.Repositories...)
	for index := range copyOfPlan.Repositories {
		copyOfPlan.Repositories[index].Verification.InitialCommits = append([]string(nil), plan.Repositories[index].Verification.InitialCommits...)
	}
	copyOfPlan.Actions = append([]ClonePlanAction(nil), plan.Actions...)
	for index := range copyOfPlan.Actions {
		copyOfPlan.Actions[index].ChildInitialCommits = append([]string(nil), plan.Actions[index].ChildInitialCommits...)
	}
	copyOfPlan.manifestData = append([]byte(nil), plan.manifestData...)
	return copyOfPlan
}

func cloneResultOutcomes(plan ClonePlan) []CloneRepositoryOutcome {
	outcomes := make([]CloneRepositoryOutcome, 0, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		verification := repository.Verification
		verification.InitialCommits = append([]string(nil), verification.InitialCommits...)
		outcomes = append(outcomes, CloneRepositoryOutcome{
			ID: repository.ID, Parent: repository.Parent, Mount: repository.Mount, Path: repository.Path,
			CloneRemote: repository.CloneRemote, CloneURL: repository.CloneURL, LocalBranch: repository.LocalBranch,
			RemoteRef: repository.RemoteRef, AdvertisedCommit: repository.AdvertisedCommit, Status: "planned", Verification: verification,
		})
	}
	return outcomes
}

func (result CloneResult) PlanCopy() *ClonePlan {
	if result.Plan == nil {
		return nil
	}
	copyOfPlan := clonePlanCopy(*result.Plan)
	return &copyOfPlan
}

func (result CloneResult) RepositoriesCopy() []CloneRepositoryOutcome {
	copyOfRepositories := append([]CloneRepositoryOutcome(nil), result.Repositories...)
	for index := range copyOfRepositories {
		copyOfRepositories[index].Verification.InitialCommits = append([]string(nil), result.Repositories[index].Verification.InitialCommits...)
	}
	return copyOfRepositories
}

func (result CloneResult) Validate() error {
	if result.Version != CloneResultVersion || result.Operation != "clone" || !result.DryRun {
		return errors.New("invalid clone result version, operation, or M03 dry-run state")
	}
	switch result.Status {
	case CloneResultPlanned:
		if result.RequestSource != nil || result.Source != nil || result.Plan == nil || result.Failure != nil || len(result.Repositories) == 0 {
			return errors.New("planned clone result has mixed or missing status fields")
		}
		if err := result.Plan.Validate(); err != nil {
			return fmt.Errorf("invalid clone result plan: %w", err)
		}
		if !reflect.DeepEqual(result.Repositories, cloneResultOutcomes(*result.Plan)) {
			return errors.New("clone result outcomes do not match its immutable plan")
		}
	case CloneResultFailed:
		if result.Plan != nil || len(result.Repositories) != 0 || result.Failure == nil {
			return errors.New("failed clone result has fabricated planning facts")
		}
		if !validCloneResultStage(result.Failure.Stage) || !validCloneResultErrorKind(result.Failure.Code) || !validCloneResultStageCode(result.Failure.Stage, result.Failure.Code) || result.Failure.Message == "" || boundedRedactedDiagnostic(result.Failure.Message) != result.Failure.Message {
			return errors.New("invalid clone result failure")
		}
		if result.RequestSource != nil {
			if err := validateCloneResultRequestSource(*result.RequestSource); err != nil {
				return err
			}
		}
		if result.Source != nil {
			if result.RequestSource == nil || result.Source.Kind != result.RequestSource.Kind || result.Source.Value != result.RequestSource.Value || !validSHA256(result.Source.SHA256) {
				return errors.New("invalid clone result loaded-source provenance")
			}
		}
		if result.Failure.Stage == CloneResultStageSource && result.Source != nil {
			return errors.New("source-stage clone failure contains unavailable loaded-source provenance")
		}
		if result.Failure.Stage != CloneResultStageSource && result.Source == nil {
			return errors.New("post-load clone failure lacks source provenance")
		}
	case "":
		return errors.New("clone result status is required")
	default:
		return errors.New("invalid clone result status")
	}
	return nil
}

func validateCloneResultRequestSource(source CloneResultRequestSource) error {
	if source.Value == "" || config.ValidateManifestSource(source.Value) != nil {
		return errors.New("invalid clone result normalized request source")
	}
	isHTTP := strings.HasPrefix(strings.ToLower(source.Value), "http://") || strings.HasPrefix(strings.ToLower(source.Value), "https://")
	if source.Kind != ManifestSourceLocal && source.Kind != ManifestSourceHTTP || (source.Kind == ManifestSourceHTTP) != isHTTP {
		return errors.New("invalid clone result normalized request source kind")
	}
	return nil
}

func validCloneResultStage(stage CloneResultStage) bool {
	switch stage {
	case CloneResultStageSource, CloneResultStageDecode, CloneResultStageDestination, CloneResultStageRegistry, CloneResultStageRemote, CloneResultStageInternal:
		return true
	default:
		return false
	}
}

func validCloneResultStageCode(stage CloneResultStage, code ErrorKind) bool {
	switch stage {
	case CloneResultStageSource:
		return code == ErrorValidation || code == ErrorConflict
	case CloneResultStageDecode, CloneResultStageDestination:
		return code == ErrorValidation
	case CloneResultStageRegistry:
		return code == ErrorValidation || code == ErrorConflict
	case CloneResultStageRemote:
		return code == ErrorGit
	case CloneResultStageInternal:
		return code == ErrorInternal
	default:
		return false
	}
}

func validCloneResultErrorKind(kind ErrorKind) bool {
	switch kind {
	case ErrorInternal, ErrorInvalidArguments, ErrorProjectNotFound, ErrorWorkspaceNotFound, ErrorValidation, ErrorGit, ErrorDirtyWorkspace, ErrorConflict, ErrorRollbackIncomplete:
		return true
	default:
		return false
	}
}

func (result CloneResult) JSON() ([]byte, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func cloneResultSourceFromLoaded(loaded LoadedManifestSource) ClonePlanSource {
	digest := sha256.Sum256(loaded.Bytes())
	return ClonePlanSource{Kind: loaded.Kind, Value: loaded.Source, SHA256: hex.EncodeToString(digest[:])}
}
