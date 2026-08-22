package service

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/definebusiness/wtree/internal/store"
)

func TestDoctorFixRepairsEveryForestMountFromItsDeclaredOwner(t *testing.T) {
	parallelM07RealGitTest(t)
	project, data := forestWorkspaceProject(t)
	statePath := WorkspaceStatePath(data, project.ID, "default")
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		t.Fatal(err)
	}
	stalePaths := make(map[string]string, len(project.Repositories))
	for _, repository := range project.ParentFirst() {
		checkout := state.Repositories[repository.ID]
		owner := state.Path
		if repository.ParentID != "" {
			owner = stalePaths[repository.ParentID]
		}
		checkout.Mount = "stale-" + repository.ID
		checkout.ResolvedPath = filepath.Join(owner, checkout.Mount)
		state.Repositories[repository.ID] = checkout
		stalePaths[repository.ID] = checkout.ResolvedPath
	}
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}
	workspace, err := RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	report, err := NewDoctorService().Fix(context.Background(), project, workspace, DoctorFixRequest{DataDir: data})
	if err != nil || !report.Fixed {
		t.Fatalf("forest doctor fix report=%#v err=%v", report, err)
	}
	fixed, err := RequireWorkspace(project, data, "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range project.ParentFirst() {
		checkout := workspaceCheckoutMap(fixed)[repository.ID]
		owner := fixed.RootPath
		if repository.ParentID != "" {
			owner = workspaceCheckoutMap(fixed)[repository.ParentID].ResolvedPath
		}
		mount, err := filepath.Rel(owner, checkout.ResolvedPath)
		if err != nil || filepath.ToSlash(mount) != checkout.Mount {
			t.Fatalf("repository %q fixed checkout=%#v owner=%q relative=%q err=%v", repository.ID, checkout, owner, mount, err)
		}
	}

	state, err = store.ReadWorkspace(WorkspaceStatePath(data, project.ID, "default"))
	if err != nil {
		t.Fatal(err)
	}
	missingRoot := filepath.Join(t.TempDir(), "missing forest")
	paths := map[string]string{}
	for _, repository := range project.ParentFirst() {
		checkout := state.Repositories[repository.ID]
		owner := missingRoot
		if repository.ParentID != "" {
			owner = paths[repository.ParentID]
		}
		checkout.ResolvedPath = filepath.Join(owner, checkout.Mount)
		state.Repositories[repository.ID] = checkout
		paths[repository.ID] = checkout.ResolvedPath
	}
	state.Path = missingRoot
	missingWorkspace, err := workspaceFromState(state)
	if err != nil {
		t.Fatal(err)
	}
	missingReport, err := NewDoctorService().Doctor(context.Background(), project, missingWorkspace, data)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, finding := range missingReport.Findings {
		if finding.Code == "missing-checkout" {
			got = append(got, finding.RepositoryID)
		}
	}
	if want := []string{"api", "web", "alpha", "beta", "gamma"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("doctor repository finding order = %v, want %v", got, want)
	}
}
