package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/lock"
)

type synchronizedMutationLocker struct {
	entered chan struct{}
	release chan struct{}
	unlocks atomic.Int32
}

func (locker *synchronizedMutationLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	close(locker.entered)
	<-locker.release
	return synchronizedMutationHandle{unlocks: &locker.unlocks}, nil
}

type synchronizedMutationHandle struct{ unlocks *atomic.Int32 }

func (handle synchronizedMutationHandle) Unlock() error {
	handle.unlocks.Add(1)
	return nil
}

// Each named mutator below uses acquireProjectMutationAuthority at its actual
// service lock boundary. This synchronized contract reproduces the shared
// interleaving once: preflight has completed, the mutator is waiting for the
// project lock, and an update journal appears before that lock is granted.
func TestProjectMutatorsRecheckUpdateJournalAfterAcquiringProjectLock(t *testing.T) {
	for _, mutator := range []string{"reconcile", "create", "remove", "delete", "import", "config-set", "config-unset"} {
		t.Run(mutator, func(t *testing.T) {
			dataDir := t.TempDir()
			locker := &synchronizedMutationLocker{entered: make(chan struct{}), release: make(chan struct{})}
			result := make(chan error, 1)
			go func() {
				handle, err := acquireProjectMutationAuthority(context.Background(), locker, dataDir, "project", time.Second)
				if handle != nil {
					_ = handle.Unlock()
				}
				result <- err
			}()
			<-locker.entered
			journalPath := writeSynchronizedActiveJournal(t, dataDir)
			close(locker.release)
			err := <-result
			var application *Error
			if err == nil || !errors.As(err, &application) || application.Kind != ErrorConflict {
				t.Fatalf("%s accepted journal created while waiting for project lock: %v", mutator, err)
			}
			if locker.unlocks.Load() != 1 {
				t.Fatalf("%s refusal leaked project lock: unlocks=%d", mutator, locker.unlocks.Load())
			}
			if err := os.Remove(journalPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Dir(journalPath)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func writeSynchronizedActiveJournal(t *testing.T, dataDir string) string {
	t.Helper()
	operationID := "update-0123456789abcdef01234567"
	path, err := UpdateJournalPath(dataDir, "project", operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := driftOID('a') + driftOID('a')[:24]
	journal := UpdateJournal{
		Version: UpdateJournalVersion, OperationID: operationID, ProjectID: "project", PlanDigest: digest,
		Generations:   UpdatePlanGenerations{CurrentManifestSHA256: digest, CandidateManifestSHA256: digest, LocalConfigSHA256: digest, RegistrySHA256: digest, DefaultStateSHA256: digest},
		RollbackState: "active",
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
