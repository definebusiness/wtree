package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
)

func TestClonePlanForestUsesBaseFirstPathsAndVerificationOwners(t *testing.T) {
	base := t.TempDir()
	urls := map[string]string{
		"z-base": filepath.Join(base, "base.git"),
		"a-top":  filepath.Join(base, "top.git"),
		"shared": filepath.Join(base, "shared.git"),
		"tool":   filepath.Join(base, "tool.git"),
	}
	commits := map[string]string{
		"z-base": "0123456789abcdef0123456789abcdef01234567",
		"a-top":  "1123456789abcdef0123456789abcdef01234567",
		"shared": "2123456789abcdef0123456789abcdef01234567",
		"tool":   "3123456789abcdef0123456789abcdef01234567",
	}
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "forest-project", Name: "forest", BaseRepository: "z-base"}, Repositories: map[string]config.PortableRepository{}}
	for _, value := range []struct{ id, parent, mount string }{{"a-top", "", "web"}, {"tool", "shared", "tools/tool"}, {"z-base", "", "services/base"}, {"shared", "z-base", "packages/shared"}} {
		manifest.Repositories[value.id] = config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: urls[value.id]}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{commits[value.id]}}, Parent: value.parent, Mount: value.mount, DefaultBranch: "main"}
	}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	remote := &clonePlanRemote{commits: map[string]string{}, errors: map[string]error{}}
	for id, url := range urls {
		remote.commits[url+"\x00refs/heads/main"] = commits[id]
	}
	source := writeClonePlanManifest(t, base, data)
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).DryRun(context.Background(), ClonePlanRequest{ManifestSource: source, Destination: filepath.Join(base, "logical-root"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := clonePlanIDs(plan.Repositories), []string{"z-base", "a-top", "shared", "tool"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("forest plan order = %v, want %v", got, want)
	}
	paths := map[string]string{}
	for _, repository := range plan.Repositories {
		paths[repository.ID] = repository.Path
		if repository.Verification.TrackedManifestExact != (repository.ID == "z-base") || repository.Verification.CommittedParentIgnore != (repository.Parent != "") {
			t.Fatalf("verification owner for %s = %#v", repository.ID, repository.Verification)
		}
	}
	if paths["z-base"] != filepath.Join(plan.LogicalRoot, "services", "base") || paths["a-top"] != filepath.Join(plan.LogicalRoot, "web") || paths["shared"] != filepath.Join(paths["z-base"], "packages", "shared") || paths["tool"] != filepath.Join(paths["shared"], "tools", "tool") {
		t.Fatalf("forest paths = %#v", paths)
	}
	if plan.LogicalRoot != plan.Destination.Path || plan.BaseRepository != "z-base" {
		t.Fatalf("forest topology facts = %#v", plan)
	}
	if !hasClonePlanAction(plan.Actions, "write_local_configuration", filepath.Join(paths["z-base"], ".wtree.yml")) || countClonePlanActions(plan.Actions, "verify_tracked_manifest") != 1 || countClonePlanActions(plan.Actions, "verify_base_metadata_ignore") != 1 || countClonePlanActions(plan.Actions, "verify_parent_ignore") != 2 {
		t.Fatalf("forest actions = %#v", plan.Actions)
	}
	encoded, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		LogicalRoot    string `json:"logicalRoot"`
		BaseRepository string `json:"baseRepository"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.LogicalRoot != plan.LogicalRoot || decoded.BaseRepository != "z-base" {
		t.Fatalf("forest plan JSON = %#v, %v", decoded, err)
	}
}

func TestClonePlanGroupingActionsAreParentFirst(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "logical-root")
	repositories := []ClonePlanRepository{{
		ID: "base", Mount: "services/group/base", Path: filepath.Join(destination, "services", "group", "base"),
	}}
	actions := clonePlanActions(repositories, destination, "base")
	var grouping []string
	for _, action := range actions {
		if action.Action == "create_grouping_directory" {
			grouping = append(grouping, action.Path)
		}
	}
	if want := []string{filepath.Join(destination, "services"), filepath.Join(destination, "services", "group")}; !reflect.DeepEqual(grouping, want) {
		t.Fatalf("grouping action order = %v, want %v", grouping, want)
	}
}

func TestClonePlanForestMapOrderHasIdenticalPlanActionsAndJSON(t *testing.T) {
	base := t.TempDir()
	urls := map[string]string{
		"z-base": filepath.Join(base, "base.git"), "a-top": filepath.Join(base, "top.git"),
		"shared": filepath.Join(base, "shared.git"), "tool": filepath.Join(base, "tool.git"),
	}
	commits := map[string]string{
		"z-base": "0123456789abcdef0123456789abcdef01234567", "a-top": "1123456789abcdef0123456789abcdef01234567",
		"shared": "2123456789abcdef0123456789abcdef01234567", "tool": "3123456789abcdef0123456789abcdef01234567",
	}
	templates := map[string]struct{ parent, mount string }{
		"z-base": {mount: "services/base"}, "a-top": {mount: "web"}, "shared": {parent: "z-base", mount: "packages/shared"}, "tool": {parent: "shared", mount: "tools/tool"},
	}
	remote := &clonePlanRemote{commits: map[string]string{}, errors: map[string]error{}}
	for id, url := range urls {
		remote.commits[url+"\x00refs/heads/main"] = commits[id]
	}
	request := ClonePlanRequest{Destination: filepath.Join(base, "logical-root"), CWD: base, DataDir: filepath.Join(base, "data")}
	var first ClonePlan
	var firstJSON []byte
	for index, insertion := range [][]string{{"z-base", "a-top", "shared", "tool"}, {"tool", "shared", "a-top", "z-base"}} {
		manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "forest-project", Name: "forest", BaseRepository: "z-base"}, Repositories: map[string]config.PortableRepository{}}
		for _, id := range insertion {
			value := templates[id]
			manifest.Repositories[id] = config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: urls[id]}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{commits[id]}}, Parent: value.parent, Mount: value.mount, DefaultBranch: "main"}
		}
		data, err := config.MarshalPortableManifest(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 && string(data) != string(first.ManifestBytes()) {
			t.Fatalf("canonical manifest changed with map insertion order")
		}
		request.ManifestSource = writeClonePlanManifest(t, base, data)
		plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).DryRun(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := plan.JSON()
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first, firstJSON = plan, encoded
			continue
		}
		if !reflect.DeepEqual(plan.Repositories, first.Repositories) || !reflect.DeepEqual(plan.Actions, first.Actions) || string(encoded) != string(firstJSON) {
			t.Fatalf("forest plan varied with map insertion order\nfirst=%s\nsecond=%s", firstJSON, encoded)
		}
	}
}

func TestClonePlanForestRetainsObservedRemoteFactsAfterPlanning(t *testing.T) {
	base := t.TempDir()
	urls := map[string]string{"base": filepath.Join(base, "base.git"), "peer": filepath.Join(base, "peer.git")}
	commits := map[string]string{"base": "0123456789abcdef0123456789abcdef01234567", "peer": "1123456789abcdef0123456789abcdef01234567"}
	manifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "forest-project", Name: "forest", BaseRepository: "base"}, Repositories: map[string]config.PortableRepository{
		"base": {Clone: config.CloneSource{Remote: "origin", URL: urls["base"]}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{commits["base"]}}, Mount: "api", DefaultBranch: "main"},
		"peer": {Clone: config.CloneSource{Remote: "origin", URL: urls["peer"]}, Upstream: config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{commits["peer"]}}, Mount: "web", DefaultBranch: "main"},
	}}
	data, err := config.MarshalPortableManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	remote := &clonePlanRemote{commits: map[string]string{}, errors: map[string]error{}}
	for id, url := range urls {
		remote.commits[url+"\x00refs/heads/main"] = commits[id]
	}
	plan, err := NewClonePlannerWith(ClonePlannerDependencies{RemoteFacts: remote}).DryRun(context.Background(), ClonePlanRequest{ManifestSource: writeClonePlanManifest(t, base, data), Destination: filepath.Join(base, "logical-root"), CWD: base, DataDir: filepath.Join(base, "data")})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	remote.commits[urls["base"]+"\x00refs/heads/main"] = "2123456789abcdef0123456789abcdef01234567"
	remote.commits[urls["peer"]+"\x00refs/heads/main"] = "3123456789abcdef0123456789abcdef01234567"
	remote.mu.Unlock()
	if got := map[string]string{plan.Repositories[0].ID: plan.Repositories[0].ObservedCommit, plan.Repositories[1].ID: plan.Repositories[1].ObservedCommit}; !reflect.DeepEqual(got, commits) {
		t.Fatalf("observed remote facts changed after planning: %v", got)
	}
	again, err := plan.JSON()
	if err != nil || string(again) != string(encoded) {
		t.Fatalf("plan JSON changed after remote advanced: %v\n%s\n%s", err, encoded, again)
	}
}

func clonePlanIDs(repositories []ClonePlanRepository) []string {
	ids := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		ids = append(ids, repository.ID)
	}
	return ids
}

func hasClonePlanAction(actions []ClonePlanAction, action, path string) bool {
	for _, candidate := range actions {
		if candidate.Action == action && candidate.Path == path {
			return true
		}
	}
	return false
}

func countClonePlanActions(actions []ClonePlanAction, action string) int {
	count := 0
	for _, candidate := range actions {
		if candidate.Action == action {
			count++
		}
	}
	return count
}
