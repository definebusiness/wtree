package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/render"
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

func TestInitAutomaticallyProtectsMissingNestedIgnore(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "root\n", "initial")
	child := testutil.NewPushedGitRepository(t)
	child.CommitFile("readme", "child\n", "initial")
	if err := os.Rename(child.Path, filepath.Join(root.Path, "child")); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.IgnoreUpdates) != 1 || result.IgnoreUpdates[0].RepositoryID != "root" {
		t.Fatalf("ignore updates = %#v", result.IgnoreUpdates)
	}
	contents, err := os.ReadFile(filepath.Join(root.Path, ".gitignore"))
	if err != nil || string(contents) != "/.wtree.yml\n/child/\n" {
		t.Fatalf("automatic ignore output = %q, %v", contents, err)
	}
}

func TestInitAutomaticallyProtectsAndVerifiesThreeLevelGraphBeforePublication(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "root\n", "initial")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("readme", "backend\n", "initial")
	shared := testutil.NewPushedGitRepository(t)
	shared.CommitFile("readme", "shared\n", "initial")
	if err := os.Rename(shared.Path, filepath.Join(backend.Path, "shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backend.Path, filepath.Join(root.Path, "backend")); err != nil {
		t.Fatal(err)
	}

	data := t.TempDir()
	result, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(root.Path, ".gitignore"):            "/.wtree.yml\n/backend/\n",
		filepath.Join(root.Path, "backend", ".gitignore"): "/shared/\n",
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("automatic ignore %s = %q, %v; want %q", path, got, readErr, want)
		}
	}
	if len(result.IgnoreUpdates) != 2 {
		t.Fatalf("ignore updates = %#v, want root and backend updates", result.IgnoreUpdates)
	}
	for _, path := range []string{result.ConfigPath, result.ManifestPath, filepath.Join(data, "registry.json"), filepath.Join(data, "state", result.ProjectID, "default.json")} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("publication target %s missing after verified ignores: %v", path, statErr)
		}
	}
	for _, check := range []struct{ repository, mount string }{{root.Path, "backend/"}, {filepath.Join(root.Path, "backend"), "shared/"}} {
		if output, checkErr := exec.Command("git", "-C", check.repository, "check-ignore", "--no-index", check.mount).CombinedOutput(); checkErr != nil {
			t.Fatalf("Git did not verify protected mount %s from %s: %v\n%s", check.mount, check.repository, checkErr, output)
		}
	}
}

func TestInitRetainsCompletedSourceIgnoreWhenLaterSourceReplacementFails(t *testing.T) {
	root := testutil.NewPushedGitRepository(t)
	root.CommitFile("readme", "root\n", "initial")
	backend := testutil.NewPushedGitRepository(t)
	backend.CommitFile("readme", "backend\n", "initial")
	shared := testutil.NewPushedGitRepository(t)
	shared.CommitFile("readme", "shared\n", "initial")
	if err := os.Rename(shared.Path, filepath.Join(backend.Path, "shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backend.Path, filepath.Join(root.Path, "backend")); err != nil {
		t.Fatal(err)
	}

	writes := 0
	initializer := service.NewInitializerWithIgnoreFileWriter(func(file service.IgnoreFilePlan, _ func() error) (bool, error) {
		writes++
		if writes == 2 {
			return false, errors.New("injected second source replacement failure")
		}
		if err := os.WriteFile(file.Path, file.NewBytes, file.Snapshot.Mode); err != nil {
			return false, err
		}
		return true, nil
	})
	data := t.TempDir()
	_, err := initializer.Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data})
	if err == nil || !strings.Contains(err.Error(), "remaining targets") {
		t.Fatalf("Init() error = %v, want source progress with remaining targets", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(root.Path, ".gitignore")); readErr != nil || string(got) != "/.wtree.yml\n/backend/\n" {
		t.Fatalf("completed source ignore = %q, %v; want retained root update", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(root.Path, "backend", ".gitignore")); !os.IsNotExist(statErr) {
		t.Fatalf("unreplaced later source file exists: %v", statErr)
	}
	for _, path := range []string{filepath.Join(root.Path, ".wtree.yml"), filepath.Join(root.Path, "project.wtree.yml"), filepath.Join(data, "registry.json")} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("wtree-owned target %s published before source completion: %v", path, statErr)
		}
	}
}

func TestInitApplyFailureOrCancellationDetectsConcurrentCompletedSourceIgnoreChange(t *testing.T) {
	for _, outcome := range []string{"failure", "cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			root := testutil.NewPushedGitRepository(t)
			root.CommitFile("readme", "root\n", "initial")
			backend := testutil.NewPushedGitRepository(t)
			backend.CommitFile("readme", "backend\n", "initial")
			shared := testutil.NewPushedGitRepository(t)
			shared.CommitFile("readme", "shared\n", "initial")
			if err := os.Rename(shared.Path, filepath.Join(backend.Path, "shared")); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backend.Path, filepath.Join(root.Path, "backend")); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rootIgnore := filepath.Join(root.Path, ".gitignore")
			attacker := []byte("concurrent user generation\n")
			initializer := service.NewInitializerWithIgnoreFileWriter(func(file service.IgnoreFilePlan, beforeReplace func() error) (bool, error) {
				if file.ParentRepositoryID == "backend" && outcome == "failure" {
					if err := os.WriteFile(rootIgnore, attacker, 0o600); err != nil {
						return false, err
					}
					return false, errors.New("injected later source replacement failure")
				}
				if err := beforeReplace(); err != nil {
					return false, err
				}
				if err := os.WriteFile(file.Path, file.NewBytes, file.Snapshot.Mode); err != nil {
					return false, err
				}
				if file.ParentRepositoryID == "root" && outcome == "cancellation" {
					if err := os.WriteFile(rootIgnore, attacker, 0o600); err != nil {
						return true, err
					}
					cancel()
				}
				return true, nil
			})

			_, err := initializer.Init(ctx, service.InitRequest{Path: root.Path, DataDir: t.TempDir()})
			var application *service.Error
			if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
				t.Fatalf("Init() error = %v, want rollback incomplete", err)
			}
			if strings.Count(err.Error(), "source ignore progress:") != 1 {
				t.Fatalf("Init() diagnostic = %q, want exactly one source progress wrapper", err)
			}
			if !strings.Contains(err.Error(), "retained source ignore generation changed") {
				t.Fatalf("Init() error = %q, want retained-generation failure", err)
			}
			if got, readErr := os.ReadFile(rootIgnore); readErr != nil || string(got) != string(attacker) {
				t.Fatalf("concurrent root ignore = %q, %v; want preserved attacker generation", got, readErr)
			}
			for _, path := range []string{filepath.Join(backend.Path, ".gitignore"), filepath.Join(root.Path, ".wtree.yml"), filepath.Join(root.Path, "project.wtree.yml")} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("unexpected later target %s after failed source application: %v", path, statErr)
				}
			}
		})
	}
}

func TestInitLaterFailuresRetainExactSourceIgnoreProgressDiagnostics(t *testing.T) {
	for _, failure := range []string{"local", "portable", "registry", "workspace", "registry-lock"} {
		t.Run(failure, func(t *testing.T) {
			repository := testutil.NewPushedGitRepository(t)
			repository.CommitFile("readme", "root\n", "initial")
			data := t.TempDir()
			var initializer *service.Initializer
			if failure == "registry-lock" {
				held, err := (lock.Manager{}).RegistryLock(context.Background(), data, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Unlock()
				initializer = service.NewInitializer()
			} else {
				initializer = service.NewInitializerWithPublicationWriters(
					func(path string, registry store.Registry) error {
						if failure == "registry" {
							return errors.New("injected registry failure")
						}
						return store.WriteRegistry(path, registry)
					},
					func(path string, state store.WorkspaceState) error {
						if failure == "workspace" {
							return errors.New("injected workspace failure")
						}
						return store.WriteWorkspace(path, state)
					},
					func(path string, contents []byte, _ os.FileMode) error {
						if (failure == "local" && filepath.Base(path) == ".wtree.yml") || (failure == "portable" && filepath.Base(path) == "project.wtree.yml") {
							return errors.New("injected file publication failure")
						}
						return store.WriteRawAtomic(path, contents)
					},
				)
			}

			_, err := initializer.Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: data})
			if err == nil {
				t.Fatal("Init() error = nil")
			}
			assertRetainedInitIgnoreProgress(t, err, filepath.Join(repository.Path, ".gitignore"))
			if got, readErr := os.ReadFile(filepath.Join(repository.Path, ".gitignore")); readErr != nil || string(got) != "/.wtree.yml\n" {
				t.Fatalf("source ignore after later failure = %q, %v", got, readErr)
			}
		})
	}
}

func assertRetainedInitIgnoreProgress(t *testing.T, err error, ignorePath string) {
	t.Helper()
	canonicalIgnorePath, canonicalErr := filepath.EvalSymlinks(ignorePath)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	for _, want := range []string{
		"source ignore progress: changed files [{root " + canonicalIgnorePath + " [/.wtree.yml]}]",
		"remaining targets []",
		"retry will not duplicate completed rules",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Init() diagnostic = %q, missing %q", err, want)
		}
	}
	var output bytes.Buffer
	if renderErr := render.JSONError(&output, err); renderErr != nil {
		t.Fatal(renderErr)
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal(output.Bytes(), &envelope); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if envelope.Error.Message != err.Error() {
		t.Fatalf("JSON error message = %q, want exact human diagnostic %q", envelope.Error.Message, err)
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
	if _, err := service.NewInitializer().Init(context.Background(), service.InitRequest{Path: root.Path, DataDir: data}); err != nil {
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
	initializer := service.NewInitializerWithPublishers(store.WriteRegistry, store.WriteWorkspace, func(string, string) error { return errors.New("injected local config publication failure") })
	_, err := initializer.Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: t.TempDir()})
	if err == nil {
		t.Fatal("Init() error = nil")
	}
	after, err := os.ReadFile(filepath.Join(repository.Path, ".gitignore"))
	if err != nil || string(after) != "# preserved\n/.wtree.yml\n" {
		t.Fatalf("ignore after rollback = %q, %v; want retained source update", after, err)
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
	initializer := service.NewInitializerWithFileWriter(func(path string, data []byte, _ os.FileMode) error {
		if filepath.Base(path) == "project.wtree.yml" {
			return errors.New("injected portable manifest publication failure")
		}
		return store.WriteRawAtomic(path, data)
	})
	_, err := initializer.Init(context.Background(), service.InitRequest{Path: repository.Path, DataDir: t.TempDir()})
	if err == nil {
		t.Fatal("Init() error = nil")
	}
	after, err := os.ReadFile(filepath.Join(repository.Path, ".gitignore"))
	if err != nil || string(after) != "# preserved\n/.wtree.yml\n" {
		t.Fatalf("ignore after portable failure = %q, %v; want retained source update", after, err)
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
	for _, path := range []string{".wtree.yml", "project.wtree.yml"} {
		if _, statErr := os.Stat(filepath.Join(repository.Path, path)); !os.IsNotExist(statErr) {
			t.Fatalf("%s remained after cancellation rollback: %v", path, statErr)
		}
	}
	if got, readErr := os.ReadFile(filepath.Join(repository.Path, ".gitignore")); readErr != nil || string(got) != "/.wtree.yml\n" {
		t.Fatalf("source ignore was not retained after cancellation: %q, %v", got, readErr)
	}
}

func TestInitManifestCancellationAtEveryPublicationBoundaryRollsBack(t *testing.T) {
	for _, boundary := range []string{"local", "portable", "registry", "state"} {
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
					if (boundary == "local" && base == ".wtree.yml") || (boundary == "portable" && base == "project.wtree.yml") {
						cancel()
					}
					return nil
				},
			)
			_, err := initializer.Init(ctx, service.InitRequest{Path: repository.Path, DataDir: t.TempDir()})
			if err == nil {
				t.Fatal("Init() error = nil after cancellation")
			}
			for _, path := range []string{".wtree.yml", "project.wtree.yml"} {
				if _, statErr := os.Stat(filepath.Join(repository.Path, path)); !os.IsNotExist(statErr) {
					t.Fatalf("%s remained after rollback: %v", path, statErr)
				}
			}
			if got, readErr := os.ReadFile(filepath.Join(repository.Path, ".gitignore")); readErr != nil || string(got) != "/.wtree.yml\n" {
				t.Fatalf("source ignore was not retained after cancellation: %q, %v", got, readErr)
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
