package service

import (
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/store"
)

func TestDeterministicForestProjectIDIsSensitiveToMembershipTopologyAndMount(t *testing.T) {
	base := forestIdentityRepositories()
	baseline := deterministicInitProjectID("api", base)
	for _, test := range []struct {
		name         string
		repositories map[string]config.PortableRepository
	}{
		{
			name: "sibling-membership",
			repositories: func() map[string]config.PortableRepository {
				result := clonePortableRepositories(base)
				result["worker"] = forestPortableRepository("worker", "", "worker")
				return result
			}(),
		},
		{
			name: "declared-parentage",
			repositories: func() map[string]config.PortableRepository {
				result := clonePortableRepositories(base)
				web := result["web"]
				web.Parent, web.Mount = "api", "web"
				result["web"] = web
				return result
			}(),
		},
		{
			name: "top-level-mount",
			repositories: func() map[string]config.PortableRepository {
				result := clonePortableRepositories(base)
				web := result["web"]
				web.Mount = "products/web"
				result["web"] = web
				return result
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := deterministicInitProjectID("api", test.repositories); got == baseline {
				t.Fatalf("project identity did not change for %s: %q", test.name, got)
			}
		})
	}
}

func TestInitForestRetainedArtifactsUseDeterministicCollisionSuffix(t *testing.T) {
	fixture := newInitForestFixture(t)
	data := t.TempDir()
	first, err := NewInitializer().Init(t.Context(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(data, "state", first.ProjectID, "default.json")
	if err := store.WriteWorkspace(retained, store.WorkspaceState{Version: store.Version, ID: "retained", Name: "retained", Path: fixture.logicalRoot, Repositories: map[string]store.CheckoutState{}}); err != nil {
		t.Fatal(err)
	}
	second, err := NewInitializer().Init(t.Context(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	third, err := NewInitializer().Init(t.Context(), InitRequest{Path: fixture.logicalRoot, DataDir: data, BaseRepository: "api", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.ProjectID == first.ProjectID || second.ProjectID != third.ProjectID {
		t.Fatalf("forest retained-artifact allocation = first %q, second %q, third %q", first.ProjectID, second.ProjectID, third.ProjectID)
	}
	if got, err := store.ReadWorkspace(retained); err != nil || got.ID != "retained" {
		t.Fatalf("retained forest state = %#v, %v", got, err)
	}
}

func forestIdentityRepositories() map[string]config.PortableRepository {
	return map[string]config.PortableRepository{
		"api": forestPortableRepository("api", "", "api"),
		"web": forestPortableRepository("web", "", "web"),
	}
}

func forestPortableRepository(id, parent, mount string) config.PortableRepository {
	return config.PortableRepository{
		Clone:         config.CloneSource{Remote: "origin", URL: "https://example.invalid/" + id + ".git"},
		Upstream:      config.Upstream{Branch: "main", Remote: "origin", Merge: "refs/heads/main"},
		Identity:      config.RepositoryIdentity{InitialCommits: []string{"0123456789abcdef0123456789abcdef01234567"}},
		Parent:        parent,
		Mount:         mount,
		DefaultBranch: "main",
	}
}

func clonePortableRepositories(value map[string]config.PortableRepository) map[string]config.PortableRepository {
	result := make(map[string]config.PortableRepository, len(value))
	for id, repository := range value {
		result[id] = repository
	}
	return result
}
