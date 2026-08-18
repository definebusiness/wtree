package service_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestInitPublishesPortableManifestFromVerifiedUpstreams(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile(".gitignore", "/backend/\n", "ignore backend")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("backend.txt", "backend\n", "backend")
	backend.CommitFile(".gitignore", "/shared/\n", "ignore shared")
	shared := testutil.NewPushedGitRepository(t)
	shared.CommitFile("shared.txt", "shared\n", "shared")
	if err := os.Rename(shared.Path, filepath.Join(backend.Path, "shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backend.Path, filepath.Join(root.Path, "backend")); err != nil {
		t.Fatal(err)
	}
	headBefore, err := exec.Command("git", "-C", root.Path, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}

	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{
		Path: root.Path, DataDir: data, ManifestSource: "https://example.invalid/acme/project.wtree.yml",
		CloneURLOverrides: []string{"backend=file:///tmp/backend.git"},
	})
	if err != nil {
		t.Fatal(err)
	}
	headAfter, err := exec.Command("git", "-C", root.Path, "rev-parse", "HEAD").Output()
	if err != nil || !bytes.Equal(headAfter, headBefore) {
		t.Fatalf("init changed root commit: before=%q after=%q err=%v", headBefore, headAfter, err)
	}
	if output, err := exec.Command("git", "-C", root.Path, "diff", "--cached", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("init staged root changes: %v\n%s", err, output)
	}
	local, err := config.ReadProjectFile(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if local.Manifest.Path != "project.wtree.yml" || local.Manifest.Source != "https://example.invalid/acme/project.wtree.yml" {
		t.Fatalf("local manifest metadata = %#v", local.Manifest)
	}
	if local.Version != config.Version {
		t.Fatalf("local configuration version = %d, want unchanged version %d", local.Version, config.Version)
	}
	portableBytes, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	portable, err := config.LoadPortableManifest(portableBytes)
	if err != nil {
		t.Fatal(err)
	}
	if portable.Version != config.PortableManifestVersion || portable.Project.BaseRepository != "root" {
		t.Fatalf("portable manifest root facts = %#v", portable)
	}
	remarshaled, err := config.MarshalPortableManifest(portable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(remarshaled, portableBytes) {
		t.Fatalf("portable manifest is not byte-stable after re-marshal:\nread: %s\nremarshal: %s", portableBytes, remarshaled)
	}
	if portable.Repositories["root"].Clone.Remote != "origin" || portable.Repositories["root"].Upstream.Merge != "refs/heads/main" || len(portable.Repositories["root"].Identity.InitialCommits) != 1 {
		t.Fatalf("root portable facts = %#v", portable.Repositories["root"])
	}
	if got := portable.Repositories["backend"].Clone.URL; got != "file:///tmp/backend.git" {
		t.Fatalf("backend clone URL = %q", got)
	}
	if backendFacts := portable.Repositories["backend"]; backendFacts.Parent != "root" || backendFacts.Mount != "backend" {
		t.Fatalf("backend portable topology = %#v", backendFacts)
	}
	if sharedFacts := portable.Repositories["shared"]; sharedFacts.Parent != "backend" || sharedFacts.Mount != "shared" {
		t.Fatalf("shared portable topology = %#v", sharedFacts)
	}
	if got, err := os.ReadFile(filepath.Join(root.Path, ".gitignore")); err != nil || string(got) != "/backend/\n/.wtree.yml\n" {
		t.Fatalf("root ignore = %q, %v", got, err)
	}
	state, err := store.ReadWorkspace(filepath.Join(data, "state", result.ProjectID, "default.json"))
	if err != nil || len(state.Repositories) != 3 || state.Repositories["backend"].Branch != "main" || state.Repositories["shared"].Mount != "shared" {
		t.Fatalf("default state = %#v, %v", state, err)
	}
}

func TestInitManifestPreflightRejectsUnpublishedBranchWithoutMutation(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("published.txt", "one\n", "published")
	repository.CommitFile("local.txt", "two\n", "unpublished")
	// CommitFile pushes ordinary fixtures; create this final history locally.
	if err := os.WriteFile(filepath.Join(repository.Path, "unpublished.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository.Run(t, "add", "unpublished.txt")
	repository.Run(t, "commit", "-m", "not pushed")

	data := t.TempDir()
	_, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data})
	if err == nil {
		t.Fatal("Init() error = nil, want unpublished upstream rejection")
	}
	if _, statErr := os.Stat(filepath.Join(repository.Path, ".wtree.yml")); !os.IsNotExist(statErr) {
		t.Fatalf("local config after failed preflight: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(repository.Path, "project.wtree.yml")); !os.IsNotExist(statErr) {
		t.Fatalf("portable manifest after failed preflight: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(data, "registry.json")); !os.IsNotExist(statErr) {
		t.Fatalf("registry after failed preflight: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(data, "state")); !os.IsNotExist(statErr) {
		t.Fatalf("workspace state after failed preflight: %v", statErr)
	}
	if got := err.Error(); !strings.Contains(got, "published upstream") {
		t.Fatalf("preflight error = %q, want repository upstream context", got)
	}
}

func TestInitManifestRequiresCommittedNestedIgnoreUnlessAddIgnore(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "root\n", "initial")
	child := testutil.NewPushedGitRepository(t)
	child.CommitFile("readme", "child\n", "initial")
	if err := os.Rename(child.Path, filepath.Join(root.Path, "child")); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil {
		t.Fatal("plain Init() accepted missing committed child ignore")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("plain init wrote ignore: %v", err)
	}
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, AddIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.IgnoreUpdates) != 1 || result.IgnoreUpdates[0].RepositoryID != "root" {
		t.Fatalf("ignore updates = %#v", result.IgnoreUpdates)
	}
	contents, err := os.ReadFile(filepath.Join(root.Path, ".gitignore"))
	if err != nil || string(contents) != "/.wtree.yml\n/child/\n" {
		t.Fatalf("add-ignore output = %q, %v", contents, err)
	}
}

func TestInitIgnoresExternalGitignoreNamedLikeOwnedFile(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "root\n", "initial")
	child := testutil.NewPushedGitRepository(t)
	child.CommitFile("readme", "child\n", "initial")
	if err := os.Rename(child.Path, filepath.Join(root.Path, "child")); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), ".gitignore")
	if err := os.WriteFile(external, []byte("/child/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root.Run(t, "config", "core.excludesFile", external)
	data := t.TempDir()
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err == nil {
		t.Fatal("plain Init() accepted external .gitignore source")
	}
	if _, err := os.Stat(filepath.Join(root.Path, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("plain init wrote owned ignore: %v", err)
	}
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data, AddIgnore: true}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root.Path, ".gitignore"))
	if err != nil || string(contents) != "/.wtree.yml\n/child/\n" {
		t.Fatalf("owned .gitignore = %q, %v", contents, err)
	}
}

func TestInitManifestRollbackRestoresIgnoreAndPortableTargetsExactly(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile(".gitignore", "# preserved\n", "initial")
	before, err := os.ReadFile(filepath.Join(repository.Path, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	initializer := service.NewInitializerWithPublishers(store.WriteRegistry, store.WriteWorkspace, func(string, string) error { return errors.New("injected local config publication failure") })
	_, err = initializer.Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: t.TempDir()})
	if err == nil {
		t.Fatal("Init() error = nil")
	}
	after, err := os.ReadFile(filepath.Join(repository.Path, ".gitignore"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("ignore after rollback = %q, %v; want %q", after, err, before)
	}
	for _, path := range []string{".wtree.yml", "project.wtree.yml"} {
		if _, statErr := os.Stat(filepath.Join(repository.Path, path)); !os.IsNotExist(statErr) {
			t.Fatalf("%s remained after rollback: %v", path, statErr)
		}
	}
}

func TestInitManifestWriteFailureRestoresEarlierFiles(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile(".gitignore", "# preserved\n", "initial")
	before, err := os.ReadFile(filepath.Join(repository.Path, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	initializer := service.NewInitializerWithFileWriter(func(path string, data []byte, _ os.FileMode) error {
		if filepath.Base(path) == "project.wtree.yml" {
			return errors.New("injected portable manifest publication failure")
		}
		return store.WriteRawAtomic(path, data)
	})
	_, err = initializer.Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: t.TempDir()})
	if err == nil {
		t.Fatal("Init() error = nil")
	}
	after, err := os.ReadFile(filepath.Join(repository.Path, ".gitignore"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("ignore after portable failure = %q, %v; want %q", after, err, before)
	}
	for _, path := range []string{".wtree.yml", "project.wtree.yml"} {
		if _, statErr := os.Stat(filepath.Join(repository.Path, path)); !os.IsNotExist(statErr) {
			t.Fatalf("%s remained after portable failure: %v", path, statErr)
		}
	}
}

func TestInitManifestCancellationAfterStatePublicationRollsBack(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initializer := service.NewInitializerWithWriters(store.WriteRegistry, func(path string, state store.WorkspaceState) error {
		if err := store.WriteWorkspace(path, state); err != nil {
			return err
		}
		cancel()
		return nil
	})
	_, err := initializer.Init(ctx, service.InitRequest{Path: repository.Path, DataDir: t.TempDir()})
	if err == nil {
		t.Fatal("Init() error = nil after cancellation")
	}
	for _, path := range []string{".gitignore", ".wtree.yml", "project.wtree.yml"} {
		if _, statErr := os.Stat(filepath.Join(repository.Path, path)); !os.IsNotExist(statErr) {
			t.Fatalf("%s remained after cancellation rollback: %v", path, statErr)
		}
	}
}

func TestInitManifestCancellationAtEveryPublicationBoundaryRollsBack(t *testing.T) {
	for _, boundary := range []string{"ignore", "local", "portable", "registry", "state"} {
		t.Run(boundary, func(t *testing.T) {
			repository := testutil.NewPushedGitRepository(t)
			repository.CommitFile("readme", "x\n", "initial")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			initializer := service.NewInitializerWithPublicationWriters(
				func(path string, registry store.Registry) error {
					if err := store.WriteRegistry(path, registry); err != nil {
						return err
					}
					if boundary == "registry" {
						cancel()
					}
					return nil
				},
				func(path string, state store.WorkspaceState) error {
					if err := store.WriteWorkspace(path, state); err != nil {
						return err
					}
					if boundary == "state" {
						cancel()
					}
					return nil
				},
				func(path string, data []byte, _ os.FileMode) error {
					if err := store.WriteRawAtomic(path, data); err != nil {
						return err
					}
					base := filepath.Base(path)
					if (boundary == "ignore" && base == ".gitignore") || (boundary == "local" && base == ".wtree.yml") || (boundary == "portable" && base == "project.wtree.yml") {
						cancel()
					}
					return nil
				},
			)
			_, err := initializer.Init(ctx, service.InitRequest{Path: repository.Path, DataDir: t.TempDir()})
			if err == nil {
				t.Fatal("Init() error = nil after cancellation")
			}
			for _, path := range []string{".gitignore", ".wtree.yml", "project.wtree.yml"} {
				if _, statErr := os.Stat(filepath.Join(repository.Path, path)); !os.IsNotExist(statErr) {
					t.Fatalf("%s remained after rollback: %v", path, statErr)
				}
			}
		})
	}
}

func TestInitManifestConcurrentPublicationHasOneWinner(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("readme", "x\n", "initial")
	data := t.TempDir()
	start := make(chan struct{})
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data})
			errors <- err
		}()
	}
	close(start)
	group.Wait()
	close(errors)
	succeeded := 0
	for err := range errors {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful concurrent init count = %d, want 1", succeeded)
	}
	if _, err := os.Stat(filepath.Join(repository.Path, ".wtree.yml")); err != nil {
		t.Fatalf("winning config missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository.Path, "project.wtree.yml")); err != nil {
		t.Fatalf("winning portable manifest missing: %v", err)
	}
	registry, err := store.ReadRegistry(filepath.Join(data, "registry.json"))
	if err != nil || len(registry.Projects) != 1 {
		t.Fatalf("registry after concurrent init = %#v, %v", registry, err)
	}
}
