package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestClonePlanningResultSuccessOwnsValidatedPlanAndStableJSON(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
	destination := filepath.Join(base, "clone")
	before := mustDirectorySnapshot(t, base)
	result, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL)}).PlanningResult(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: destination, CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CloneResultPlanned || result.Plan == nil || result.Failure != nil || result.Source != nil || result.RequestSource != nil || len(result.Repositories) != 2 {
		t.Fatalf("clone result = %#v", result)
	}
	if result.Repositories[0].ID != "root" || result.Repositories[1].ID != "api" || result.Repositories[0].Status != "planned" {
		t.Fatalf("repository outcomes are not deterministic parent-first: %#v", result.Repositories)
	}
	if after := mustDirectorySnapshot(t, base); !reflect.DeepEqual(before, after) {
		t.Fatalf("planning result mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
	}

	first, err := result.JSON()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := result.JSON()
	if !reflect.DeepEqual(first, second) || !json.Valid(first) {
		t.Fatalf("unstable clone result JSON: %s", first)
	}
	jsonText := string(first)
	if strings.Index(jsonText, `"plan"`) > strings.Index(jsonText, `"repositories"`) || strings.Contains(jsonText, `"failure"`) || strings.Contains(jsonText, `"requestSource"`) {
		t.Fatalf("unexpected success JSON schema/order: %s", jsonText)
	}
	var directTopology map[string]json.RawMessage
	var directLogicalRoot, directBaseRepository string
	decodeErr := json.Unmarshal(first, &directTopology)
	if decodeErr == nil {
		decodeErr = errors.Join(json.Unmarshal(directTopology["logicalRoot"], &directLogicalRoot), json.Unmarshal(directTopology["baseRepository"], &directBaseRepository))
	}
	if decodeErr != nil || directLogicalRoot != result.LogicalRoot || directBaseRepository != "root" {
		t.Fatalf("planned result direct topology = %s, decode=%v", first, decodeErr)
	}
	var decoded CloneResult
	if err := json.Unmarshal(first, &decoded); err != nil || decoded.Validate() != nil {
		t.Fatalf("decoded clone result validation: decode=%v validate=%v", err, decoded.Validate())
	}
	mutatedTopology := decoded
	mutatedTopology.LogicalRoot = filepath.Join(base, "other-root")
	if err := mutatedTopology.Validate(); err == nil {
		t.Fatal("tampered direct topology bypassed result validation")
	}
	mutatedTopology = decoded
	mutatedTopology.BaseRepository = "other-base"
	if err := mutatedTopology.Validate(); err == nil {
		t.Fatal("tampered direct base repository bypassed result validation")
	}

	planCopy := result.PlanCopy()
	planCopy.Repositories[0].Verification.InitialCommits[0] = strings.Repeat("f", 40)
	planCopy.Actions[0].RepositoryID = "changed"
	planCopy.Destination.AncestorFacts[0].Path = "changed"
	if reflect.DeepEqual(planCopy, result.Plan) {
		t.Fatal("PlanCopy exposed mutable result storage")
	}
	repositoriesCopy := result.RepositoriesCopy()
	repositoriesCopy[0].Verification.InitialCommits[0] = strings.Repeat("e", 40)
	if reflect.DeepEqual(repositoriesCopy, result.Repositories) {
		t.Fatal("RepositoriesCopy exposed mutable result storage")
	}

	mutated := decoded
	mutated.Repositories = mutated.RepositoriesCopy()
	mutated.Repositories[1].Status = "cloned"
	if err := mutated.Validate(); err == nil {
		t.Fatal("mutated outcome bypassed validation")
	}
	mutated = decoded
	mutated.Plan = mutated.PlanCopy()
	mutated.Plan.Repositories[0].ObservedCommit = strings.Repeat("a", 40)
	if err := mutated.Validate(); err == nil {
		t.Fatal("mutated embedded plan bypassed validation")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("planning result executed clone: %v", err)
	}
	directPlan := result.PlanCopy()
	directResult, err := NewCloneResult(*directPlan)
	if err != nil {
		t.Fatal(err)
	}
	directPlan.Repositories[0].Verification.InitialCommits[0] = strings.Repeat("d", 40)
	directPlan.Actions[0].ChildInitialCommits = append(directPlan.Actions[0].ChildInitialCommits, "changed")
	directPlan.Destination.AncestorFacts[0].Path = "changed"
	if err := directResult.Validate(); err != nil {
		t.Fatalf("constructor retained caller-owned plan storage: %v", err)
	}
}

func TestClonePlanningResultRepresentsEveryRealPrePlanFailureBoundary(t *testing.T) {
	tests := []struct {
		name        string
		wantStage   CloneResultStage
		wantCode    ErrorKind
		prepare     func(*testing.T, string) (*ClonePlanner, ClonePlanRequest)
		wantRequest bool
		wantSource  bool
	}{
		{
			name: "source load", wantStage: CloneResultStageSource, wantCode: ErrorValidation, wantRequest: true,
			prepare: func(_ *testing.T, base string) (*ClonePlanner, ClonePlanRequest) {
				return NewClonePlanner(), ClonePlanRequest{ManifestSource: filepath.Join(base, "missing.yml"), Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")}
			},
		},
		{
			name: "decode", wantStage: CloneResultStageDecode, wantCode: ErrorValidation, wantRequest: true, wantSource: true,
			prepare: func(t *testing.T, base string) (*ClonePlanner, ClonePlanRequest) {
				source := writeClonePlanManifest(t, base, []byte("version: nope\ncredential: https://user:secret@example.invalid\n"))
				return NewClonePlanner(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")}
			},
		},
		{
			name: "destination", wantStage: CloneResultStageDestination, wantCode: ErrorValidation, wantRequest: true, wantSource: true,
			prepare: func(t *testing.T, base string) (*ClonePlanner, ClonePlanRequest) {
				rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
				source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
				return NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL)}), ClonePlanRequest{ManifestSource: source, Destination: base, CWD: base, DataDir: filepath.Join(base, "data")}
			},
		},
		{
			name: "registry", wantStage: CloneResultStageRegistry, wantCode: ErrorConflict, wantRequest: true, wantSource: true,
			prepare: func(t *testing.T, base string) (*ClonePlanner, ClonePlanRequest) {
				rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
				source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
				reader := &staticCloneRegistryFactsReader{err: NewError(ErrorConflict, errors.New("registry changed https://user:registry-secret@example.invalid"))}
				return NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL), RegistryFacts: reader}), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")}
			},
		},
		{
			name: "remote", wantStage: CloneResultStageRemote, wantCode: ErrorGit, wantRequest: true, wantSource: true,
			prepare: func(t *testing.T, base string) (*ClonePlanner, ClonePlanRequest) {
				rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
				source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
				remote := newClonePlanRemote(rootURL, childURL)
				remote.errors[rootURL+"\x00refs/heads/published-main"] = errors.New("remote https://user:remote-secret@example.invalid failed")
				return NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone"), CWD: base, DataDir: filepath.Join(base, "data")}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			planner, request := test.prepare(t, base)
			before := mustDirectorySnapshot(t, base)
			result, err := planner.PlanningResult(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != CloneResultFailed || result.Plan != nil || len(result.Repositories) != 0 || result.Failure == nil || result.Failure.Stage != test.wantStage || result.Failure.Code != test.wantCode {
				t.Fatalf("failure result = %#v", result)
			}
			if (result.RequestSource != nil) != test.wantRequest || (result.Source != nil) != test.wantSource {
				t.Fatalf("provenance request=%#v source=%#v", result.RequestSource, result.Source)
			}
			if strings.Contains(result.Failure.Message, "registry-secret") || strings.Contains(result.Failure.Message, "remote-secret") || len(result.Failure.Message) > 8195 {
				t.Fatalf("unbounded or leaking failure: %#v", result.Failure)
			}
			if after := mustDirectorySnapshot(t, base); !reflect.DeepEqual(before, after) {
				t.Fatalf("failure result mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
			}
			first, err := result.JSON()
			if err != nil {
				t.Fatal(err)
			}
			second, _ := result.JSON()
			if !reflect.DeepEqual(first, second) || strings.Contains(string(first), "registry-secret") || strings.Contains(string(first), "remote-secret") || strings.Contains(string(first), `"plan"`) || strings.Contains(string(first), `"repositories"`) || strings.Contains(string(first), `"logicalRoot"`) || strings.Contains(string(first), `"baseRepository"`) {
				t.Fatalf("unstable, mixed, or leaking failure JSON: %s", first)
			}
			var decoded CloneResult
			if err := json.Unmarshal(first, &decoded); err != nil || decoded.Validate() != nil {
				t.Fatalf("decoded failure validation: decode=%v validate=%v", err, decoded.Validate())
			}
		})
	}
}

func TestClonePlanningResultRejectsCredentialInputWithoutPersistingIt(t *testing.T) {
	secret := "raw-source-secret"
	result, err := NewClonePlanner().PlanningResult(context.Background(), ClonePlanRequest{ManifestSource: "https://user:" + secret + "@example.invalid/project.wtree.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestSource != nil || result.Source != nil || result.Failure == nil || strings.Contains(result.Failure.Message, secret) {
		t.Fatalf("credential input leaked into failure result: %#v", result)
	}
	data, err := result.JSON()
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatalf("credential input leaked into JSON: %v %s", err, data)
	}
}

func TestCloneResultFailureDefensiveCopiesAndMutationRejection(t *testing.T) {
	request := &CloneResultRequestSource{Kind: ManifestSourceLocal, Value: filepath.Join(t.TempDir(), "project.wtree.yml")}
	source := &ClonePlanSource{Kind: request.Kind, Value: request.Value, SHA256: strings.Repeat("a", 64)}
	result, err := newCloneFailureResult(request, source, CloneResultStageDecode, NewError(ErrorValidation, errors.New(strings.Repeat("x", 20000))))
	if err != nil {
		t.Fatal(err)
	}
	request.Value = "changed"
	source.SHA256 = "changed"
	if result.RequestSource.Value == "changed" || result.Source.SHA256 == "changed" || len(result.Failure.Message) > 8195 {
		t.Fatalf("failure did not own bounded defensive copies: %#v", result)
	}

	mutations := []func(*CloneResult){
		func(value *CloneResult) { value.Status = "unknown" },
		func(value *CloneResult) { value.Plan = &ClonePlan{} },
		func(value *CloneResult) { value.Failure.Stage = CloneResultStageRemote },
		func(value *CloneResult) { value.Failure.Code = ErrorGit },
		func(value *CloneResult) { value.Source = nil },
		func(value *CloneResult) { value.RequestSource.Value = "https://user:secret@example.invalid/x" },
	}
	for index, mutate := range mutations {
		var copyOfResult CloneResult
		data, _ := json.Marshal(result)
		_ = json.Unmarshal(data, &copyOfResult)
		mutate(&copyOfResult)
		if err := copyOfResult.Validate(); err == nil {
			t.Fatalf("mutation %d bypassed validation: %#v", index, copyOfResult)
		}
	}
}

func TestClonePlanningResultConcurrentReadOnlyGeneration(t *testing.T) {
	base := t.TempDir()
	rootURL, childURL := filepath.Join(base, "root.git"), filepath.Join(base, "api.git")
	source := writeClonePlanManifest(t, base, clonePlanManifest(t, rootURL, childURL))
	planner := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: newClonePlanRemote(rootURL, childURL)})
	before := mustDirectorySnapshot(t, base)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 24)
	for index := 0; index < 24; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			result, err := planner.PlanningResult(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "clone-"+string(rune('a'+index))), CWD: base, DataDir: filepath.Join(base, "data")})
			if err != nil {
				errorsSeen <- err
				return
			}
			errorsSeen <- result.Validate()
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if after := mustDirectorySnapshot(t, base); !reflect.DeepEqual(before, after) {
		t.Fatalf("concurrent planning results mutated filesystem\nbefore=%#v\nafter=%#v", before, after)
	}
}
