package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

type doctorCountingLocker struct{ unlocks atomic.Int32 }

func (locker *doctorCountingLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	return doctorCountingHandle{unlocks: &locker.unlocks}, nil
}

type doctorCountingHandle struct{ unlocks *atomic.Int32 }

func (handle doctorCountingHandle) Unlock() error {
	handle.unlocks.Add(1)
	return nil
}

type doctorSynchronizedLocker struct {
	entered chan struct{}
	release chan struct{}
	unlocks atomic.Int32
}

func (locker *doctorSynchronizedLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	close(locker.entered)
	<-locker.release
	return doctorCountingHandle{unlocks: &locker.unlocks}, nil
}

func TestDoctorFixRefusesActiveUpdateJournalBeforeRepair(t *testing.T) {
	project, root, workspace, data, statePath, target := doctorPruneAuthorityFixture(t)
	locker := &doctorCountingLocker{}
	doctor := service.NewDoctorServiceWith(gitadapter.NewAdapter("git"), locker, store.WriteWorkspace)
	journalPath := writeDoctorActiveJournal(t, data, project.ID)
	before := doctorAuthoritySnapshot(t, root.Path, data, statePath, target)

	_, err := doctor.Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data})
	assertDoctorJournalConflict(t, err)
	assertDoctorAuthoritySnapshot(t, root.Path, data, statePath, target, before)
	if locker.unlocks.Load() != 1 {
		t.Fatalf("journal refusal leaked project lock: unlocks=%d", locker.unlocks.Load())
	}
	if err := os.Remove(journalPath); err != nil {
		t.Fatal(err)
	}
}

func trackDoctorManifest(t *testing.T, root testutil.GitRepository) {
	t.Helper()
	root.CommitFile(".gitignore", "/backend/\n", "commit portable parent ignore")
	root.Run(t, "add", "--", ".wtree.yml", "project.wtree.yml")
	root.Run(t, "commit", "-m", "track doctor manifest authority")
}

func TestDoctorFixRechecksJournalCreatedWhileWaitingForLock(t *testing.T) {
	project, root, workspace, data, statePath, target := doctorPruneAuthorityFixture(t)
	locker := &doctorSynchronizedLocker{entered: make(chan struct{}), release: make(chan struct{})}
	doctor := service.NewDoctorServiceWith(gitadapter.NewAdapter("git"), locker, store.WriteWorkspace)
	result := make(chan error, 1)
	go func() {
		_, err := doctor.Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data})
		result <- err
	}()
	<-locker.entered
	journalPath := writeDoctorActiveJournal(t, data, project.ID)
	before := doctorAuthoritySnapshot(t, root.Path, data, statePath, target)
	close(locker.release)
	assertDoctorJournalConflict(t, <-result)
	assertDoctorAuthoritySnapshot(t, root.Path, data, statePath, target, before)
	if locker.unlocks.Load() != 1 {
		t.Fatalf("journal refusal leaked project lock: unlocks=%d", locker.unlocks.Load())
	}
	if err := os.Remove(journalPath); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorReadOnlyAndDryRunRemainAvailableWithActiveUpdateJournal(t *testing.T) {
	project, root, workspace, data, statePath, target := doctorPruneAuthorityFixture(t)
	doctor := service.NewDoctorService()
	journalPath := writeDoctorActiveJournal(t, data, project.ID)
	before := doctorAuthoritySnapshot(t, root.Path, data, statePath, target)

	report, err := doctor.Doctor(context.Background(), project, workspace, data)
	if err != nil || !doctorHasRepair(report, "prune-worktree-metadata") {
		t.Fatalf("read-only doctor report=%#v err=%v", report, err)
	}
	report, err = doctor.Fix(context.Background(), project, workspace, service.DoctorFixRequest{DataDir: data, DryRun: true})
	if err != nil || !report.DryRun || !doctorHasRepair(report, "prune-worktree-metadata") {
		t.Fatalf("dry-run doctor report=%#v err=%v", report, err)
	}
	assertDoctorAuthoritySnapshot(t, root.Path, data, statePath, target, before)
	if err := os.Remove(journalPath); err != nil {
		t.Fatal(err)
	}
}

func doctorPruneAuthorityFixture(t *testing.T) (project domain.Project, root testutil.GitRepository, workspace domain.Workspace, data, statePath, target string) {
	t.Helper()
	project, root, _, data = createFixture(t)
	trackDoctorManifest(t, root)
	target = filepath.Join(t.TempDir(), "gone")
	root.Run(t, "branch", "feature/authority")
	root.Run(t, "worktree", "add", target, "feature/authority")
	head, err := gitadapter.NewAdapter("git").Head(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	statePath = service.WorkspaceStatePath(data, project.ID, "authority")
	state := store.WorkspaceState{
		ID: "authority", Name: "authority", Path: target, Partial: true, MissingRepositoryIDs: []string{"backend"},
		Repositories: map[string]store.CheckoutState{"root": {Branch: "feature/authority", Head: head, Mount: ".", ResolvedPath: target}},
	}
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}
	workspace, err = service.RequireWorkspace(project, data, "authority")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	report, err := service.NewDoctorService().Doctor(context.Background(), project, workspace, data)
	if err != nil || !doctorHasRepair(report, "prune-worktree-metadata") {
		t.Fatalf("repairable doctor report=%#v err=%v", report, err)
	}
	return project, root, workspace, data, statePath, target
}

func writeDoctorActiveJournal(t *testing.T, dataDir, projectID string) string {
	t.Helper()
	operationID := "update-0123456789abcdef01234567"
	path, err := service.UpdateJournalPath(dataDir, projectID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	journal := service.UpdateJournal{
		Version: service.UpdateJournalVersion, OperationID: operationID, ProjectID: projectID, PlanDigest: digest,
		Generations:   service.UpdatePlanGenerations{CurrentManifestSHA256: digest, CandidateManifestSHA256: digest, LocalConfigSHA256: digest, RegistrySHA256: digest, DefaultStateSHA256: digest},
		RollbackState: "active",
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type doctorAuthorityState struct {
	state    []byte
	registry []byte
	worktree map[string][]byte
}

func doctorAuthoritySnapshot(t *testing.T, source, data, statePath, target string) doctorAuthorityState {
	t.Helper()
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile(filepath.Join(data, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	common, err := gitadapter.NewAdapter("git").CommonGitDir(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("repairable worktree %q state before fix: %v", target, err)
	}
	return doctorAuthorityState{state: state, registry: registry, worktree: doctorDirectoryBytes(t, filepath.Join(common, "worktrees"))}
}

func assertDoctorAuthoritySnapshot(t *testing.T, source, data, statePath, target string, want doctorAuthorityState) {
	t.Helper()
	got := doctorAuthoritySnapshot(t, source, data, statePath, target)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("doctor journal refusal mutated Git metadata, workspace state, registry, or filesystem")
	}
}

func doctorDirectoryBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	values := map[string][]byte{}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return values
	} else if err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		values[relative] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func assertDoctorJournalConflict(t *testing.T, err error) {
	t.Helper()
	var application *service.Error
	if err == nil || !errors.As(err, &application) || application.Kind != service.ErrorConflict {
		t.Fatalf("doctor accepted active update journal: %v", err)
	}
}
