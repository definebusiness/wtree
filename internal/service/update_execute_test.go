package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestUpdateExecutJournaledEffectsRunParentFirstAndRemoveCleanJournal(t *testing.T) {
	plan := updateExecutorPlan(t)
	data := t.TempDir()
	var calls []string
	executor := NewUpdateExecutor()
	result, err := executeUpdateForTest(context.Background(), executor, UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-1", Plan: plan}, updateExecutorRecapture(t), []updateEffect{
		{Name: "root", Repository: "root", Execute: func(context.Context) (string, error) { calls = append(calls, "root"); return "head-root", nil }, Rollback: func(context.Context) error { calls = append(calls, "undo-root"); return nil }},
		{Name: "child", Repository: "child", Execute: func(context.Context) (string, error) { calls = append(calls, "child"); return "head-child", nil }, Rollback: func(context.Context) error { calls = append(calls, "undo-child"); return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Completed, []string{"root", "child"}) || !reflect.DeepEqual(calls, []string{"root", "child"}) {
		t.Fatalf("result=%#v calls=%#v", result, calls)
	}
	path, _ := UpdateJournalPath(data, "project", "operation-1")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestUpdateExecutRollbackIsReverseAndJournalIsRetainedOnlyForIncompleteUndo(t *testing.T) {
	plan := updateExecutorPlan(t)
	for _, test := range []struct {
		name        string
		undo        error
		wantJournal bool
	}{{"clean", nil, false}, {"incomplete", os.ErrPermission, true}} {
		t.Run(test.name, func(t *testing.T) {
			data := t.TempDir()
			var calls []string
			_, err := executeUpdateForTest(context.Background(), NewUpdateExecutor(), UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-2", Plan: plan}, updateExecutorRecapture(t), []updateEffect{
				{Name: "root", Execute: func(context.Context) (string, error) { calls = append(calls, "root"); return "r", nil }, Rollback: func(context.Context) error { calls = append(calls, "undo-root"); return test.undo }},
				{Name: "child", Execute: func(context.Context) (string, error) { calls = append(calls, "child"); return "", errors.New("boom") }},
			})
			if test.wantJournal {
				var application *Error
				if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
					t.Fatalf("err=%v", err)
				}
			} else if !HasCleanRollback(err) {
				t.Fatalf("clean rollback error=%v", err)
			}
			if !reflect.DeepEqual(calls, []string{"root", "child", "undo-root"}) {
				t.Fatalf("calls=%#v", calls)
			}
			path, _ := UpdateJournalPath(data, "project", "operation-2")
			_, statErr := os.Lstat(path)
			if test.wantJournal != (statErr == nil) {
				t.Fatalf("journal err=%v", statErr)
			}
		})
	}
}

func TestUpdateExecutRefusesActiveJournalAndCancellationBeforeEffects(t *testing.T) {
	plan := updateExecutorPlan(t)
	data := t.TempDir()
	path, _ := UpdateJournalPath(data, "project", "operation-3")
	journal, err := newUpdateJournal(UpdateExecutionRequest{ProjectID: "project", OperationID: "operation-3", Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewUpdateJournal(NewUpdateExecutor(), path, journal); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = executeUpdateForTest(context.Background(), NewUpdateExecutor(), UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-3", Plan: plan}, updateExecutorRecapture(t), []updateEffect{{Name: "root", Execute: func(context.Context) (string, error) { called = true; return "", nil }}})
	if err == nil || called {
		t.Fatalf("active journal err=%v called=%t", err, called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = executeUpdateForTest(ctx, NewUpdateExecutor(), UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-4", Plan: plan}, updateExecutorRecapture(t), []updateEffect{{Name: "root", Execute: func(context.Context) (string, error) { called = true; return "", nil }}})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled err=%v called=%t", err, called)
	}
}

func TestUpdateExecutJournalTransitionFailpointsRollBackWithoutEffects(t *testing.T) {
	for _, step := range []string{"journal-create-after", "journal-root-started-after"} {
		t.Run(step, func(t *testing.T) {
			data := t.TempDir()
			called := false
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(actual string) error {
				if actual == step {
					return errors.New("injected transition failure")
				}
				return nil
			}})
			_, err := executeUpdateForTest(context.Background(), executor, UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-journal-" + strings.TrimPrefix(step, "journal-")[:6], Plan: updateExecutorPlan(t)}, updateExecutorRecapture(t), []updateEffect{{Name: "root", Execute: func(context.Context) (string, error) { called = true; return "head", nil }}})
			if !HasCleanRollback(err) || called {
				t.Fatalf("err=%v called=%t", err, called)
			}
		})
	}
}

// updateExecutionBoundaryMatrix is the exhaustive inventory of the executor's
// named boundary families. The table-driven tests below exercise the generic
// journal/effect transitions; the production crash/reopen tests exercise the
// configured-ref and added-clone families with real Git fixtures.
var updateExecutionBoundaryMatrix = []struct {
	name       string
	sourceCall string
}{
	{"journal creation", `"journal-create-before"`},
	{"journal creation completion", `"journal-create-after"`},
	{"journal transition writes", `"journal-"+transition+"-before"`},
	{"journal transition writes after", `"journal-"+transition+"-after"`},
	{"journal removal", `"journal-"+transition+"-remove-before"`},
	{"journal removal completion", `"journal-"+transition+"-remove-after"`},
	{"recovery inverse", `"recovery-"+effect.Name+"-before"`},
	{"recovery inverse completion", `"recovery-"+effect.Name+"-after"`},
	{"existing configured-ref observe", `"repository-"+repository.ID+"-observe"`},
	{"existing configured-ref observe completion", `"repository-"+repository.ID+"-observe-after"`},
	{"existing configured-ref fetch", `"repository-"+repository.ID+"-fetch"`},
	{"existing configured-ref fetch completion", `"repository-"+repository.ID+"-fetch-after"`},
	{"existing configured-ref fast-forward", `"repository-"+repository.ID+"-fast-forward"`},
	{"existing configured-ref fast-forward completion", `"repository-"+repository.ID+"-fast-forward-after"`},
	{"added clone", `"repository-"+id+"-clone"`},
	{"added clone completion", `"repository-"+id+"-clone-after"`},
	{"added fetch", `"repository-"+id+"-fetch"`},
	{"added fetch completion", `"repository-"+id+"-fetch-after"`},
	{"added checkout", `"repository-"+id+"-checkout"`},
	{"added checkout completion", `"repository-"+id+"-checkout-after"`},
	{"added identity/tree verification", `"repository-"+id+"-verify"`},
	{"added identity/tree verification completion", `"repository-"+id+"-verify-after"`},
	{"added publish", `"repository-"+id+"-publish"`},
	{"added publish completion", `"repository-"+id+"-publish-after"`},
	{"base manifest postcondition", `"base-manifest-postcondition"`},
	{"terminal recovery summary publish", `"terminal-cleanup-summary-publish-before"`},
	{"terminal recovery summary publish completion", `"terminal-cleanup-summary-publish-after"`},
	{"terminal backup blob unlink", `"terminal-cleanup-backup-"+backup.Kind+"-remove-before"`},
	{"terminal backup blob unlink completion", `"terminal-cleanup-backup-"+backup.Kind+"-remove-after"`},
	{"terminal backup directory removal", `"terminal-cleanup-backup-directory-remove-before"`},
	{"terminal backup directory removal completion", `"terminal-cleanup-backup-directory-remove-after"`},
	{"terminal staging removal", `"terminal-cleanup-staging-remove-before"`},
	{"terminal staging removal completion", `"terminal-cleanup-staging-remove-after"`},
	{"terminal operation removal", `"terminal-cleanup-operation-remove-before"`},
	{"terminal operation removal completion", `"terminal-cleanup-operation-remove-after"`},
	{"terminal recovery summary removal", `"terminal-cleanup-summary-remove-before"`},
	{"terminal recovery summary removal completion", `"terminal-cleanup-summary-remove-after"`},
}

func TestUpdateExecutBoundaryMatrixGenericJournalLifecycle(t *testing.T) {
	for _, test := range []struct {
		name        string
		step        string
		clean       bool
		prepare     int
		execute     int
		cleanup     int
		rollback    int
		wantContext bool
	}{
		{"journal create before", "journal-create-before", false, 0, 0, 0, 0, false},
		{"journal create after", "journal-create-after", true, 0, 0, 0, 0, false},
		{"effect before", "root-before", true, 0, 0, 0, 0, false},
		{"started write before", "journal-root-started-before", true, 0, 0, 0, 0, false},
		{"started write after", "journal-root-started-after", true, 0, 0, 0, 0, false},
		{"prepared write before", "journal-root-prepared-before", true, 1, 0, 1, 0, false},
		{"prepared write after", "journal-root-prepared-after", true, 1, 0, 1, 0, false},
		{"completed write before", "journal-root-completed-before", true, 1, 1, 0, 1, false},
		{"completed write after", "journal-root-completed-after", true, 1, 1, 0, 1, false},
		{"effect after cancellation", "root-after", true, 1, 1, 0, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, data := updateExecutorPlan(t), t.TempDir()
			counts := struct{ prepare, execute, cleanup, rollback int }{}
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(step string) error {
				if step != test.step {
					return nil
				}
				if test.wantContext {
					return context.Canceled
				}
				return errors.New("injected matrix boundary failure")
			}})
			request := UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-matrix-" + strings.ReplaceAll(test.name, " ", "-"), Plan: plan}
			_, err := executeUpdateForTest(context.Background(), executor, request, updateExecutorRecapture(t), []updateEffect{{Name: "root", Prepare: func(context.Context) (string, error) {
				counts.prepare++
				return "prepared", nil
			}, Execute: func(context.Context) (string, error) {
				counts.execute++
				return "completed", nil
			}, Cleanup: func(context.Context) error {
				counts.cleanup++
				return nil
			}, Rollback: func(context.Context) error {
				counts.rollback++
				return nil
			}}})
			if test.clean != HasCleanRollback(err) {
				t.Fatalf("clean rollback=%t err=%v", HasCleanRollback(err), err)
			}
			if test.wantContext && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost precedence: %v", err)
			}
			if got := struct{ prepare, execute, cleanup, rollback int }{counts.prepare, counts.execute, counts.cleanup, counts.rollback}; got != (struct{ prepare, execute, cleanup, rollback int }{test.prepare, test.execute, test.cleanup, test.rollback}) {
				t.Fatalf("effect counts=%#v want prepare=%d execute=%d cleanup=%d rollback=%d", got, test.prepare, test.execute, test.cleanup, test.rollback)
			}
			path, pathErr := UpdateJournalPath(data, request.ProjectID, request.OperationID)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
				t.Fatalf("clean/non-effect boundary retained journal: %v", statErr)
			}
		})
	}
}

func TestUpdateExecutBoundaryMatrixSourceInventoryIsComplete(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate boundary matrix test source")
	}
	production, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "update_execute.go"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, boundary := range updateExecutionBoundaryMatrix {
		if seen[boundary.sourceCall] {
			t.Fatalf("duplicate boundary matrix source call %q", boundary.sourceCall)
		}
		seen[boundary.sourceCall] = true
		if !bytes.Contains(production, []byte(boundary.sourceCall)) {
			t.Fatalf("boundary matrix omits production call for %s: %s", boundary.name, boundary.sourceCall)
		}
	}
}

func TestUpdateExecutBoundaryMatrixProductionEffectFamilies(t *testing.T) {
	plan := updateExecutorPlan(t)
	snapshot := mustUpdateExecutorSnapshot(t)
	for _, test := range []struct {
		name      string
		step      string
		wantCalls int
	}{
		{"observe before", "repository-root-observe", 0},
		{"observe after", "repository-root-observe-after", 1},
		{"fetch before", "repository-root-fetch", 1},
		{"fetch after", "repository-root-fetch-after", 2},
		{"fast-forward before", "repository-root-fast-forward", 2},
		{"fast-forward after", "repository-root-fast-forward-after", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &updateExecutionGit{}
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake, Before: failUpdateBoundary(test.step)})
			effects, err := executor.productionEffects(context.Background(), UpdateExecutionRequest{DataDir: t.TempDir(), ProjectID: "project", OperationID: "operation-existing-matrix", Plan: plan}, snapshot)
			if err != nil || len(effects) != 1 {
				t.Fatalf("production effects=%#v err=%v", effects, err)
			}
			if _, err := effects[0].Execute(context.Background()); err == nil {
				t.Fatalf("accepted injected boundary %q", test.step)
			}
			if len(fake.calls) != test.wantCalls {
				t.Fatalf("calls before %q=%#v want %d", test.step, fake.calls, test.wantCalls)
			}
		})
	}

	for _, test := range []struct{ name, step string }{
		{"clone before", "repository-added-clone"},
		{"clone after", "repository-added-clone-after"},
		{"fetch before", "repository-added-fetch"},
		{"fetch after", "repository-added-fetch-after"},
		{"checkout before", "repository-added-checkout"},
		{"checkout after", "repository-added-checkout-after"},
		{"verification before", "repository-added-verify"},
		{"verification after", "repository-added-verify-after"},
		{"publish before", "repository-added-publish"},
		{"publish after", "repository-added-publish-after"},
	} {
		t.Run("added "+test.name, func(t *testing.T) {
			root, data := t.TempDir(), t.TempDir()
			mount := filepath.Join(root, "added")
			request := UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-added-matrix"}
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: &updateExecutionGit{}, Before: failUpdateBoundary(test.step)})
			effect, err := executor.addedRepositoryEffectWithin(request, "added", driftRepository("root", "added"), DriftRepositoryObservation{RepositoryID: "added", Path: mount, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, root)
			if err != nil {
				t.Fatal(err)
			}
			_, prepareErr := effect.Prepare(context.Background())
			if test.step == "repository-added-publish" || test.step == "repository-added-publish-after" {
				if prepareErr != nil {
					t.Fatalf("prepare before publish boundary: %v", prepareErr)
				}
				_, prepareErr = effect.Execute(context.Background())
			}
			if prepareErr == nil {
				t.Fatalf("accepted injected boundary %q", test.step)
			}
			if err := effect.Cleanup(context.Background()); err != nil {
				t.Fatalf("cleanup after %q: %v", test.step, err)
			}
			if _, err := os.Lstat(mount); !os.IsNotExist(err) {
				t.Fatalf("boundary %q retained published mount: %v", test.step, err)
			}
			stage := filepath.Join(data, "projects", "project", "update", "operation-added-matrix", "staging", "added")
			if _, err := os.Lstat(stage); !os.IsNotExist(err) {
				t.Fatalf("boundary %q retained private staging: %v", test.step, err)
			}
		})
	}
}

func failUpdateBoundary(wanted string) func(string) error {
	return func(step string) error {
		if step == wanted {
			return errors.New("injected production boundary failure")
		}
		return nil
	}
}

func TestUpdateExecutBoundaryMatrixRecoveryInverseRetainsStrictEvidence(t *testing.T) {
	for _, test := range []struct {
		name      string
		step      string
		wantCalls int
	}{
		{"before inverse", "recovery-repository-root-fast-forward-before", 0},
		{"after inverse", "recovery-repository-root-fast-forward-after", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			original := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
			candidate := append(append([]byte(nil), original...), []byte("# candidate\n")...)
			target := filepath.Join(root, "project.wtree.yml")
			if err := os.WriteFile(target, original, 0o640); err != nil {
				t.Fatal(err)
			}
			plan := updateRecoveryPlan(t, target, original, candidate)
			request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-recovery-matrix", Plan: plan}
			sources, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "tracked-manifest", path: target}}, map[string][]byte{"tracked-manifest": original})
			if err != nil {
				t.Fatalf("prepare recovery backup: %v", err)
			}
			if err := writeUpdateBackups(request, sources); err != nil {
				t.Fatalf("write recovery backup: %v", err)
			}
			journal, err := newUpdateJournal(request)
			if err != nil {
				t.Fatal(err)
			}
			journal.Backups = backupMetadata(sources)
			journal.Progress = []UpdateJournalEffect{{Sequence: 1, Name: "repository-root-fast-forward", Repository: "root", Receipt: updateRecoveryFastForwardReceipt(t, request, "root", driftOID('2')), State: "completed"}}
			path, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeNewUpdateJournal(NewUpdateExecutor(), path, journal); err != nil {
				t.Fatalf("write recovery journal: %v", err)
			}
			fake := &updateExecutionGit{}
			err = NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake, Before: failUpdateBoundary(test.step)}).Recover(context.Background(), request)
			var application *Error
			if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete || len(fake.calls) != test.wantCalls {
				t.Fatalf("recovery boundary error=%v calls=%#v", err, fake.calls)
			}
			retained, readErr := os.ReadFile(path)
			if readErr != nil || bytes.Contains(retained, original) || bytes.Contains(retained, candidate) {
				t.Fatalf("recovery boundary did not retain redacted strict evidence=%q err=%v", retained, readErr)
			}
			if _, err := decodeStrictUpdateJournal(retained); err != nil {
				t.Fatalf("recovery boundary retained invalid journal: %v", err)
			}
		})
	}
}

func TestUpdateExecutJournalRejectsTraversalSecretsAndSymlinks(t *testing.T) {
	plan := updateExecutorPlan(t)
	if _, err := UpdateJournalPath(t.TempDir(), "../project", "operation-1"); err == nil {
		t.Fatal("accepted traversal")
	}
	journal, err := newUpdateJournal(UpdateExecutionRequest{ProjectID: "project", OperationID: "operation-1", Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	journal.Failure = "https://user:secret@example.test"
	if err := journal.Validate(); err == nil {
		t.Fatal("accepted secret")
	}
	data := t.TempDir()
	path, _ := UpdateJournalPath(data, "project", "operation-1")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, path); err != nil {
		t.Fatal(err)
	}
	journal, _ = newUpdateJournal(UpdateExecutionRequest{ProjectID: "project", OperationID: "operation-1", Plan: plan})
	if err := writeNewUpdateJournal(NewUpdateExecutor(), path, journal); err == nil {
		t.Fatal("wrote through journal symlink")
	}
}

func TestUpdateExecutJournalRejectsUnknownJSONAndDuplicateEffectReceipts(t *testing.T) {
	journal, err := newUpdateJournal(UpdateExecutionRequest{ProjectID: "project", OperationID: "operation-strict", Plan: updateExecutorPlan(t)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"unknown":true}`)...)
	if _, err := decodeStrictUpdateJournal(data); err == nil {
		t.Fatal("accepted unknown journal field")
	}
	journal.Progress = []UpdateJournalEffect{{Sequence: 1, Name: "effect", State: "started"}, {Sequence: 2, Name: "effect", State: "started"}}
	if err := journal.Validate(); err == nil {
		t.Fatal("accepted duplicate effect receipt")
	}
	journal.Progress = []UpdateJournalEffect{{Sequence: 1, Name: "effect", State: "rolled-back"}}
	if err := journal.Validate(); err == nil {
		t.Fatal("accepted rollback state before rollback transition")
	}
	journal.RollbackState, journal.Failure = "incomplete", "failed"
	if err := journal.Validate(); err == nil {
		t.Fatal("accepted incomplete journal without an unreverted effect")
	}
	journal.RollbackState, journal.Failure = "active", ""
	journal.Progress = nil
	journal.Backups = []UpdateJournalBackup{{Kind: "tracked-manifest", File: "tracked-manifest.bin", Existed: true, Mode: 0o640, Length: 1, SHA256: strings.Repeat("a", 64)}, {Kind: "tracked-manifest", File: "tracked-manifest.bin", Existed: true, Mode: 0o640, Length: 1, SHA256: strings.Repeat("a", 64)}}
	if err := journal.Validate(); err == nil {
		t.Fatal("accepted duplicate opaque backup metadata")
	}
	journal.Backups = []UpdateJournalBackup{{Kind: "tracked-manifest", File: "../escape", Existed: true, Mode: 0o640, Length: 1, SHA256: strings.Repeat("a", 64)}}
	if err := journal.Validate(); err == nil {
		t.Fatal("accepted traversing opaque backup metadata")
	}
	journal.Backups = nil
	journal.Progress = []UpdateJournalEffect{{Sequence: 1, Name: "repository-child-add", Repository: "child", Receipt: "receipt", State: "prepared"}, {Sequence: 2, Name: "next", State: "started"}}
	if err := journal.Validate(); err == nil {
		t.Fatal("accepted non-final prepared addition receipt")
	}
}

func TestUpdateExecutLockedRecaptureRejectsFixedDriftButPermitsSelectedTipMovement(t *testing.T) {
	plan := updateExecutorPlan(t)
	data := t.TempDir()
	for _, test := range []struct {
		name   string
		mutate func(*DriftSnapshot)
	}{
		{"tip-moves", func(snapshot *DriftSnapshot) { snapshot.observations[0].AdvertisedCommit = driftOID('2') }},
		{"head-changes", func(snapshot *DriftSnapshot) { snapshot.observations[0].Head = driftOID('2') }},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			_, err := executeUpdateForTest(context.Background(), NewUpdateExecutor(), UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-" + test.name, Plan: plan}, func(ctx context.Context, plan UpdatePlan) (DriftSnapshot, error) {
				snapshot, err := updateExecutorRecapture(t)(ctx, plan)
				test.mutate(&snapshot)
				return snapshot, err
			}, []updateEffect{{Name: "root", Execute: func(context.Context) (string, error) { called = true; return "head", nil }, Rollback: func(context.Context) error { return nil }}})
			if test.name == "tip-moves" {
				if err != nil || !called {
					t.Fatalf("tip movement err=%v called=%t", err, called)
				}
			} else if err == nil || called {
				t.Fatalf("fixed drift err=%v called=%t", err, called)
			}
		})
	}
}

func TestUpdateExecutPrivateBaselineDefensiveCopy(t *testing.T) {
	plan := updateExecutorPlan(t)
	copy := plan.executionBaseline()
	copy.candidate[0] ^= 1
	copy.observations[0].Head = driftOID('9')
	copy.project.Repositories[0].ID = "changed"
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan changed through baseline accessor: %v", err)
	}
	if bytes.Equal(copy.candidate, plan.CandidateManifestBytes()) || copy.observations[0].Head == plan.executionBaseline().observations[0].Head || copy.project.Repositories[0].ID == plan.executionBaseline().project.Repositories[0].ID {
		t.Fatal("private baseline accessor aliases immutable plan")
	}
}

func TestUpdateExecutProductionFastForwardUsesOnlyConfiguredRefAndOwnedInverse(t *testing.T) {
	plan := updateExecutorPlan(t)
	fake := &updateExecutionGit{}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake})
	effects, err := executor.productionEffects(context.Background(), UpdateExecutionRequest{DataDir: t.TempDir(), ProjectID: "project", OperationID: "operation-ff", Plan: plan}, mustUpdateExecutorSnapshot(t))
	if err != nil || len(effects) != 1 {
		t.Fatalf("effects=%#v err=%v", effects, err)
	}
	if receipt, err := effects[0].Execute(context.Background()); err != nil {
		t.Fatalf("execute receipt=%q err=%v", receipt, err)
	} else if decoded, err := decodeUpdateFastForwardReceipt(receipt); err != nil || decoded.NewCommit != driftOID('2') {
		t.Fatalf("execute receipt=%q decoded=%#v err=%v", receipt, decoded, err)
	}
	if err := effects[0].Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.calls, []string{"observe:origin:refs/heads/main", "fetch:origin:refs/heads/main", "ff:main:" + driftOID('0') + ":" + driftOID('2'), "restore:main:" + driftOID('2'), "restore-fetch:origin:refs/heads/main:" + driftOID('2')}) {
		t.Fatalf("calls=%#v", fake.calls)
	}
}

func TestUpdateExecutProductionDerivesActionsAndBindsCandidateToActualBaseHead(t *testing.T) {
	plan := updateExecutorPlan(t)
	data := t.TempDir()
	fake := &updateExecutionGit{tracked: plan.CandidateManifestBytes()}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake})
	result, err := executor.executeForTest(context.Background(), UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-production", Plan: plan}, updateExecutionTestSeams{recapture: updateExecutorRecapture(t)})
	if err != nil || !reflect.DeepEqual(result.Completed, []string{"repository-root-fast-forward"}) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(fake.calls, []string{"observe:origin:refs/heads/main", "fetch:origin:refs/heads/main", "ff:main:" + driftOID('0') + ":" + driftOID('2')}) {
		t.Fatalf("production calls=%#v", fake.calls)
	}
	journalPath, _ := UpdateJournalPath(data, "project", "operation-production")
	journalBytes, readErr := os.ReadFile(journalPath)
	journal, journalErr := decodeStrictUpdateJournal(journalBytes)
	if readErr != nil || journalErr != nil || journal.RollbackState != "active" || len(journal.Progress) != 1 || journal.Progress[0].State != "completed" {
		t.Fatalf("successful M03 retained invalid handoff journal=%#v read=%v decode=%v", journal, readErr, journalErr)
	}

	fake = &updateExecutionGit{tracked: []byte("different")}
	executor = NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake})
	rollbackData := t.TempDir()
	_, err = executor.executeForTest(context.Background(), UpdateExecutionRequest{DataDir: rollbackData, ProjectID: "project", OperationID: "operation-wrong-base", Plan: plan}, updateExecutionTestSeams{recapture: updateExecutorRecapture(t)})
	if !HasCleanRollback(err) {
		t.Fatalf("candidate mismatch did not cleanly roll back: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rollbackData, "projects", "project", "update", "operation-wrong-base")); !os.IsNotExist(err) {
		t.Fatalf("clean rollback retained an unresolved operation authority: %v", err)
	}
}

func TestUpdateExecutAddedRepositoryStagesVerifiesPublishesAndOnlyRemovesOwnedReceipt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "checkout", "nested")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	portable := driftRepository("root", "nested")
	fake := &updateExecutionGit{}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake})
	effect, err := executor.addedRepositoryEffect(UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-added"}, "child", portable, DriftRepositoryObservation{Path: target, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := effect.Execute(context.Background())
	prepared, receiptErr := decodeUpdateAddedReceipt(receipt)
	if err != nil || receiptErr != nil || prepared.Head != driftOID('2') {
		t.Fatalf("receipt=%q decoded=%#v err=%v receiptErr=%v", receipt, prepared, err, receiptErr)
	}
	if info, err := os.Lstat(target); err != nil || !info.IsDir() {
		t.Fatalf("published target err=%v info=%v", err, info)
	}
	if err := effect.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("owned target remains: %v", err)
	}
	if !reflect.DeepEqual(fake.calls, []string{"clone:origin", "fetch:origin:refs/heads/main", "checkout:main"}) {
		t.Fatalf("calls=%#v", fake.calls)
	}
}

func TestUpdateExecutAddedRepositoryRejectsUnsafeMountAndPreservesConcurrentTree(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, "nested", "child")
	if err := validateUpdateMountAuthority(workspace, filepath.Join(root, "elsewhere")); err == nil {
		t.Fatal("accepted a target outside the workspace authority")
	}
	if err := os.Symlink(root, filepath.Join(workspace, "nested")); err != nil {
		t.Fatal(err)
	}
	if err := ensureAbsentUpdateMountWithin(workspace, target); err == nil {
		t.Fatal("accepted symlink mount ancestor")
	}
	if err := os.Remove(filepath.Join(workspace, "nested")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := &updateExecutionGit{}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake})
	effect, err := executor.addedRepositoryEffectWithin(UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-owned"}, "child", driftRepository("root", "nested/child"), DriftRepositoryObservation{Path: target, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := effect.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "concurrent"), []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := effect.Rollback(context.Background()); err == nil {
		t.Fatal("removed a concurrently modified published tree")
	}
	if data, err := os.ReadFile(filepath.Join(target, "concurrent")); err != nil || string(data) != "do not remove" {
		t.Fatalf("concurrent data lost: %q %v", data, err)
	}
}

func TestUpdateExecutAddedRepositoryFailedEffectCleansOnlyOwnedStaging(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	target := filepath.Join(workspace, "child")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	fake := &updateExecutionGit{}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fake, Before: func(step string) error {
		if step == "repository-child-fetch" {
			return context.Canceled
		}
		return nil
	}})
	request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-stage-cleanup"}
	effect, err := executor.addedRepositoryEffectWithin(request, "child", driftRepository("", "child"), DriftRepositoryObservation{Path: target, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := effect.Execute(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error=%v", err)
	}
	if err := effect.Cleanup(context.WithoutCancel(context.Background())); err != nil {
		t.Fatalf("cleanup=%v", err)
	}
	stage := filepath.Join(request.DataDir, "projects", request.ProjectID, "update", request.OperationID, "staging", "child")
	if _, err := os.Lstat(stage); !os.IsNotExist(err) {
		t.Fatalf("owned staging remains: %v", err)
	}
}

func TestUpdateExecutNestedAddedRepositoriesPublishAndRollbackParentFirst(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-nested"}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: &updateExecutionGit{}})
	parentTarget := filepath.Join(workspace, "parent")
	parent, err := executor.addedRepositoryEffectWithin(request, "parent", driftRepository("", "parent"), DriftRepositoryObservation{Path: parentTarget, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	childTarget := filepath.Join(parentTarget, "child")
	child, err := executor.addedRepositoryEffectWithin(request, "child", driftRepository("parent", "child"), DriftRepositoryObservation{Path: childTarget, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Execute(context.Background()); err != nil {
		t.Fatalf("publish parent: %v", err)
	}
	if _, err := child.Execute(context.Background()); err != nil {
		t.Fatalf("publish child: %v", err)
	}
	if err := child.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback child: %v", err)
	}
	if err := parent.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback parent: %v", err)
	}
	if _, err := os.Lstat(parentTarget); !os.IsNotExist(err) {
		t.Fatalf("owned nested parent remains: %v", err)
	}
}

func TestUpdateExecutPreparedAddedReceiptRecoversStageOrPublishedMount(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "stage", true: "mount"}[published], func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			target := filepath.Join(workspace, "space café")
			if err := os.MkdirAll(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-prepared"}
			git := &updateExecutionGit{}
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: git})
			effect, err := executor.addedRepositoryEffectWithin(request, "child", driftRepository("", "space café"), DriftRepositoryObservation{RepositoryID: "child", Path: target, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := effect.Prepare(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if published {
				if got, err := effect.Execute(context.Background()); err != nil || got != receipt {
					t.Fatalf("publish receipt=%q err=%v", got, err)
				}
			}
			baseline := updateExecutionBaseline{workspace: domain.Workspace{RootPath: workspace}, observations: []DriftRepositoryObservation{{RepositoryID: "child", Path: target, TargetAbsent: true}}}
			if err := executor.recoverPreparedAddition(context.Background(), request, baseline, UpdateJournalEffect{Sequence: 1, Name: "repository-child-add", Repository: "child", Receipt: receipt, State: "prepared"}); err != nil {
				t.Fatalf("recover prepared addition: %v", err)
			}
			stage := filepath.Join(request.DataDir, "projects", request.ProjectID, "update", request.OperationID, "staging", "child")
			if _, err := os.Lstat(stage); !os.IsNotExist(err) {
				t.Fatalf("prepared stage remains: %v", err)
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("prepared mount remains: %v", err)
			}
		})
	}
}

func TestUpdateExecutPreparedAddedRecoveryPreservesConcurrentMount(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	target := filepath.Join(workspace, "child")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-prepared-race"}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: &updateExecutionGit{}})
	effect, err := executor.addedRepositoryEffectWithin(request, "child", driftRepository("", "child"), DriftRepositoryObservation{RepositoryID: "child", Path: target, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := effect.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := effect.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "concurrent"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline := updateExecutionBaseline{workspace: domain.Workspace{RootPath: workspace}, observations: []DriftRepositoryObservation{{RepositoryID: "child", Path: target, TargetAbsent: true}}}
	err = executor.recoverPreparedAddition(context.Background(), request, baseline, UpdateJournalEffect{Sequence: 1, Name: "repository-child-add", Repository: "child", Receipt: receipt, State: "prepared"})
	if err == nil {
		t.Fatal("recovery removed a concurrent mount")
	}
	if data, err := os.ReadFile(filepath.Join(target, "concurrent")); err != nil || string(data) != "keep" {
		t.Fatalf("concurrent mount was not preserved: %q %v", data, err)
	}
}

func TestUpdateExecutAddedBoundaryFailuresCleanOnlyOwnedEffects(t *testing.T) {
	for _, boundary := range []string{"clone-after", "fetch-after", "checkout-after", "verify-after", "publish-after"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			target := filepath.Join(workspace, "child")
			if err := os.MkdirAll(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-" + strings.TrimSuffix(boundary, "-after")}
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: &updateExecutionGit{}, Before: func(step string) error {
				if step == "repository-child-"+boundary {
					return errors.New("injected boundary failure")
				}
				return nil
			}})
			effect, err := executor.addedRepositoryEffectWithin(request, "child", driftRepository("", "child"), DriftRepositoryObservation{RepositoryID: "child", Path: target, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := effect.Execute(context.Background()); err == nil {
				t.Fatal("effect completed through injected boundary")
			}
			if err := effect.Cleanup(context.Background()); err != nil {
				t.Fatalf("owned cleanup: %v", err)
			}
			stage := filepath.Join(request.DataDir, "projects", request.ProjectID, "update", request.OperationID, "staging", "child")
			if _, err := os.Lstat(stage); !os.IsNotExist(err) {
				t.Fatalf("owned stage remains: %v", err)
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("owned mount remains: %v", err)
			}
		})
	}
}

func TestUpdateExecutActualGitNestedAddedRepositoriesPublishAndRollback(t *testing.T) {
	ctx := context.Background()
	parentSource := testutil.NewPushedGitRepository(t)
	parentSource.CommitFile("parent.txt", "parent\n", "parent")
	parentSource.CommitFile(".gitignore", "/child/\n", "ignore nested child")
	childSource := testutil.NewPushedGitRepository(t)
	childSource.CommitFile("child.txt", "child\n", "child")
	adapter := gitadapter.NewAdapter("git")
	parentUpstream, err := adapter.Upstream(ctx, parentSource.Path)
	if err != nil {
		t.Fatal(err)
	}
	childUpstream, err := adapter.Upstream(ctx, childSource.Path)
	if err != nil {
		t.Fatal(err)
	}
	parentHead, err := adapter.Head(ctx, parentSource.Path)
	if err != nil {
		t.Fatal(err)
	}
	childHead, err := adapter.Head(ctx, childSource.Path)
	if err != nil {
		t.Fatal(err)
	}
	portable := func(remote string, head string, mount string) config.PortableRepository {
		return config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: remote}, Upstream: config.Upstream{Remote: "origin", Branch: "main", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{head}}, Mount: mount, DefaultBranch: "main"}
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-real-git"}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: adapter})
	parentPath := filepath.Join(workspace, "parent")
	parent, err := executor.addedRepositoryEffectWithin(request, "parent", portable(parentUpstream.FetchURL, parentHead, "parent"), DriftRepositoryObservation{RepositoryID: "parent", Path: parentPath, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(parentPath, "child")
	child, err := executor.addedRepositoryEffectWithin(request, "child", portable(childUpstream.FetchURL, childHead, "child"), DriftRepositoryObservation{RepositoryID: "child", Path: childPath, TargetAbsent: true, IgnoreKnown: true, IgnoreVerified: true}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Execute(ctx); err != nil {
		stage := filepath.Join(request.DataDir, "projects", request.ProjectID, "update", request.OperationID, "staging", "parent")
		top, topErr := adapter.TopLevel(ctx, stage)
		t.Fatalf("publish parent: %v (stage top=%q topErr=%v)", err, top, topErr)
	}
	if _, err := child.Execute(ctx); err != nil {
		t.Fatalf("publish child: %v", err)
	}
	for _, check := range []struct{ path, want string }{{parentPath, parentHead}, {childPath, childHead}} {
		got, err := adapter.Head(ctx, check.path)
		if err != nil || got != check.want {
			t.Fatalf("actual nested HEAD at %q = %q, %v", check.path, got, err)
		}
		clean, err := adapter.IsClean(ctx, check.path)
		if err != nil || !clean {
			t.Fatalf("actual nested clone cleanliness at %q = %t, %v", check.path, clean, err)
		}
	}
	if err := child.Rollback(ctx); err != nil {
		t.Fatalf("reverse child rollback: %v", err)
	}
	if err := parent.Rollback(ctx); err != nil {
		t.Fatalf("reverse parent rollback: %v", err)
	}
	if _, err := os.Lstat(parentPath); !os.IsNotExist(err) {
		t.Fatalf("actual owned nested mount remains: %v", err)
	}
}

func TestUpdateExecutActualGitConfiguredRefFastForwardUsesExecutionTipAndOwnedInverse(t *testing.T) {
	ctx := context.Background()
	source := testutil.NewPushedGitRepository(t)
	source.CommitFile("project.wtree.yml", "version: 1\n", "initial manifest")
	adapter := gitadapter.NewAdapter("git")
	upstream, err := adapter.Upstream(ctx, source.Path)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "workspace")
	if err := adapter.Clone(ctx, upstream.FetchURL, checkout, "origin"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FetchTrackingBranch(ctx, checkout, "origin", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	oldHead, err := adapter.CheckoutTrackingBranch(ctx, checkout, "main", "origin", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	source.CommitFile("planned.txt", "one\n", "planned tip")
	plannedTip, err := adapter.Head(ctx, source.Path)
	if err != nil {
		t.Fatal(err)
	}
	common, err := adapter.CommonGitDir(ctx, checkout)
	if err != nil {
		t.Fatal(err)
	}
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": {Clone: config.CloneSource{Remote: "origin", URL: upstream.FetchURL}, Upstream: config.Upstream{Remote: "origin", Branch: "main", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{oldHead}}, Mount: ".", DefaultBranch: "main"}})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: common, SourcePath: checkout}})
	workspace := driftWorkspace(project)
	workspace.RootPath = checkout
	workspace.Checkouts[0].ResolvedPath, workspace.Checkouts[0].Head = checkout, oldHead
	snapshot, err := driftBuild(t, DriftSnapshotInput{DataDir: t.TempDir(), Project: project, DefaultWorkspace: workspace, CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{{RepositoryID: "root", Path: checkout, CommonGitDir: common, Branch: "main", Head: oldHead, Clean: true, AdvertisedCommit: plannedTip, CanFastForward: true, TrackedManifestExact: true, UpstreamKnown: true, Upstream: gitadapter.Upstream{LocalBranch: "main", Remote: "origin", Merge: "refs/heads/main", FetchURL: upstream.FetchURL}}}})
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("actual FF snapshot err=%v failures=%#v", err, snapshot.Failures())
	}
	plan, err := BuildUpdatePlan(snapshot, LoadedManifestSource{Kind: ManifestSourceLocal, Source: filepath.Join(t.TempDir(), "candidate.wtree.yml"), data: manifest})
	if err != nil {
		t.Fatal(err)
	}
	source.CommitFile("execution.txt", "two\n", "execution tip")
	executionTip, err := adapter.Head(ctx, source.Path)
	if err != nil {
		t.Fatal(err)
	}
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: adapter})
	effects, err := executor.productionEffects(ctx, UpdateExecutionRequest{DataDir: t.TempDir(), ProjectID: project.ID, OperationID: "operation-real-ff", Plan: plan}, snapshot)
	if err != nil || len(effects) != 1 {
		t.Fatalf("actual FF effects=%#v err=%v", effects, err)
	}
	if receipt, err := effects[0].Execute(ctx); err != nil {
		t.Fatalf("actual execution tip receipt=%q err=%v", receipt, err)
	} else if decoded, err := decodeUpdateFastForwardReceipt(receipt); err != nil || decoded.NewCommit != executionTip {
		t.Fatalf("actual execution tip receipt=%q decoded=%#v err=%v", receipt, decoded, err)
	}
	if got, err := adapter.Head(ctx, checkout); err != nil || got != executionTip || got == plannedTip {
		t.Fatalf("actual fast-forward HEAD=%q planned=%q execution=%q err=%v", got, plannedTip, executionTip, err)
	}
	if err := effects[0].Rollback(ctx); err != nil {
		t.Fatalf("actual fast-forward inverse: %v", err)
	}
	if got, err := adapter.Head(ctx, checkout); err != nil || got != oldHead {
		t.Fatalf("actual fast-forward rollback HEAD=%q old=%q err=%v", got, oldHead, err)
	}
}

// TestUpdateExecutProductionCrashReopen exercises the public executor rather
// than a journal or effect seam.  Each subtest creates an entirely new local
// project whose base checkout, nested checkouts, registry, state, and local
// configuration are collected again by the production recapture path.  The
// panic is an intentional process-crash simulation: it happens after the
// named durable boundary, so normal in-process rollback must not run.
func TestUpdateExecutProductionCrashReopen(t *testing.T) {
	for _, test := range []struct {
		name   string
		step   string
		nested bool
	}{
		{"one-existing-fast-forward", "journal-repository-root-fast-forward-completed-after", false},
		{"existing-fast-forward-before-completed", "repository-root-fast-forward-after", false},
		{"nested-existing-fast-forwards", "journal-repository-child-fast-forward-completed-after", true},
		{"prepared-add-before-rename", "repository-added-publish", false},
		{"published-add-before-completed", "repository-added-publish-after", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdateExecutionCrashFixture(t, test.nested)
			panicValue := "crash at " + test.step
			crashed := false
			var boundaries []string
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						if recovered != panicValue {
							t.Fatalf("unexpected crash %#v", recovered)
						}
						crashed = true
					}
				}()
				executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(step string) error {
					boundaries = append(boundaries, step)
					if step == test.step {
						panic(panicValue)
					}
					return nil
				}})
				_, err := executor.Execute(context.Background(), fixture.request)
				if err != nil {
					t.Fatalf("Execute() returned before simulated crash: %v", err)
				}
			}()
			if !crashed {
				t.Fatalf("executor did not reach the requested durable crash boundary %q; boundaries=%#v", test.step, boundaries)
			}
			journalPath, err := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			journalBytes, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatalf("crash did not retain journal: %v", err)
			}
			if _, err := decodeStrictUpdateJournal(journalBytes); err != nil {
				t.Fatalf("crash journal is not strict: %v", err)
			}

			if err := NewUpdateExecutor().Recover(context.Background(), fixture.request); err != nil {
				t.Fatalf("fresh Recover() = %v", err)
			}
			fixture.assertRestored(t)
			if _, err := os.Lstat(journalPath); !os.IsNotExist(err) {
				t.Fatalf("complete recovery retained journal: %v", err)
			}
			backupDirectory, err := updateBackupDirectory(fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(backupDirectory); !os.IsNotExist(err) {
				t.Fatalf("complete recovery retained private backup authority: %v", err)
			}
			if err := NewUpdateExecutor().Recover(context.Background(), fixture.request); err != nil {
				t.Fatalf("idempotent absent recovery = %v", err)
			}
		})
	}
}

func TestUpdateExecutRecoveryRetainsMalformedFastForwardReceiptWithoutMutatingGit(t *testing.T) {
	for _, test := range []struct {
		name    string
		receipt string
	}{
		{"raw-object-id", driftOID('2')},
		{"malformed-strict-bytes", "not-a-strict-fast-forward-receipt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdateExecutionCrashFixture(t, false)
			crashUpdateExecutorAt(t, fixture.request, "journal-repository-root-fast-forward-completed-after")

			journalPath, err := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			journalBytes, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			journal, err := decodeStrictUpdateJournal(journalBytes)
			if err != nil || len(journal.Progress) != 1 || journal.Progress[0].Name != "repository-root-fast-forward" || journal.Progress[0].State != "completed" {
				t.Fatalf("crash journal=%#v err=%v", journal, err)
			}
			journal.Progress[0].Receipt = test.receipt
			if err := writeUpdateJournal(NewUpdateExecutor(), journalPath, journal); err != nil {
				t.Fatalf("write malformed receipt journal: %v", err)
			}

			beforeHead, err := fixture.git.Head(context.Background(), fixture.paths["root"])
			if err != nil {
				t.Fatal(err)
			}
			beforeTracking, err := fixture.git.ResolveRef(context.Background(), fixture.paths["root"], "refs/remotes/origin/main")
			if err != nil {
				t.Fatal(err)
			}
			err = NewUpdateExecutor().Recover(context.Background(), fixture.request)
			var application *Error
			if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
				t.Fatalf("Recover() = %v, want rollback_incomplete", err)
			}
			if afterHead, headErr := fixture.git.Head(context.Background(), fixture.paths["root"]); headErr != nil || afterHead != beforeHead {
				t.Fatalf("malformed receipt changed local HEAD=%q want=%q err=%v", afterHead, beforeHead, headErr)
			}
			if afterTracking, refErr := fixture.git.ResolveRef(context.Background(), fixture.paths["root"], "refs/remotes/origin/main"); refErr != nil || afterTracking != beforeTracking {
				t.Fatalf("malformed receipt changed tracking ref=%q want=%q err=%v", afterTracking, beforeTracking, refErr)
			}
			retained, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatalf("malformed receipt did not retain journal: %v", err)
			}
			if _, err := decodeStrictUpdateJournal(retained); err != nil {
				t.Fatalf("retained malformed receipt evidence is not strict: %v", err)
			}
		})
	}
}

func TestUpdateExecutProductionCrashRecoveryRetainsConcurrentOrTamperedEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, fixture updateExecutionCrashFixture)
		verify func(t *testing.T, fixture updateExecutionCrashFixture)
	}{
		{"backup-blob-tamper", func(t *testing.T, fixture updateExecutionCrashFixture) {
			directory, err := updateBackupDirectory(fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "tracked-manifest.bin"), []byte("tampered opaque backup"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, fixture updateExecutionCrashFixture) {
			directory, _ := updateBackupDirectory(fixture.request)
			bytes, err := os.ReadFile(filepath.Join(directory, "tracked-manifest.bin"))
			if err != nil || string(bytes) != "tampered opaque backup" {
				t.Fatalf("tampered private evidence was overwritten or removed: %q %v", bytes, err)
			}
		}},
		{"tracked-manifest-concurrent", func(t *testing.T, fixture updateExecutionCrashFixture) {
			path := filepath.Join(fixture.paths["root"], "project.wtree.yml")
			if err := os.WriteFile(path, []byte("concurrent generation\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, fixture updateExecutionCrashFixture) {
			bytes, err := os.ReadFile(filepath.Join(fixture.paths["root"], "project.wtree.yml"))
			if err != nil || string(bytes) != "concurrent generation\n" {
				t.Fatalf("concurrent manifest was overwritten: %q %v", bytes, err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdateExecutionCrashFixture(t, false)
			crashUpdateExecutorAt(t, fixture.request, "journal-repository-root-fast-forward-completed-after")
			test.mutate(t, fixture)
			err := NewUpdateExecutor().Recover(context.Background(), fixture.request)
			var application *Error
			if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
				t.Fatalf("Recover() = %v, want retained rollback evidence", err)
			}
			journalPath, _ := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
			journal, readErr := os.ReadFile(journalPath)
			if readErr != nil || bytes.Contains(journal, []byte("tampered opaque backup")) || bytes.Contains(journal, []byte("concurrent generation")) {
				t.Fatalf("incomplete journal is absent or leaked mutable evidence: %q %v", journal, readErr)
			}
			if _, err := decodeStrictUpdateJournal(journal); err != nil {
				t.Fatalf("incomplete journal is not strict: %v", err)
			}
			test.verify(t, fixture)
		})
	}
}

func TestUpdateExecutProductionRollbackAndRecoveryLeaveSecondPreflightUsable(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(t *testing.T, fixture updateExecutionCrashFixture)
	}{
		{"clean-rollback", func(t *testing.T, fixture updateExecutionCrashFixture) {
			_, err := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: failUpdateBoundary("base-manifest-postcondition")}).Execute(context.Background(), fixture.request)
			if !HasCleanRollback(err) {
				t.Fatalf("clean rollback Execute: %v", err)
			}
		}},
		{"clean-recovery", func(t *testing.T, fixture updateExecutionCrashFixture) {
			crashUpdateExecutorAt(t, fixture.request, "journal-repository-root-fast-forward-completed-after")
			if err := NewUpdateExecutor().Recover(context.Background(), fixture.request); err != nil {
				t.Fatalf("clean Recover: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdateExecutionCrashFixture(t, false)
			test.run(t, fixture)
			assertUpdateSecondPreflight(t, fixture.request)
		})
	}
}

func TestUpdateExecutProductionAddedSuccessRetainsStrictM04HandoffAuthority(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	if _, err := NewUpdateExecutor().Execute(context.Background(), fixture.request); err != nil {
		t.Fatalf("clean added Execute: %v", err)
	}
	journalPath, err := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("successful M03 lost strict handoff journal: %v", err)
	}
	journal, err := decodeStrictUpdateJournal(journalBytes)
	if err != nil || journal.RollbackState != "active" || len(journal.Progress) == 0 || journal.Progress[len(journal.Progress)-1].State != "completed" {
		t.Fatalf("successful M03 handoff journal=%#v err=%v", journal, err)
	}
	if err := validateOwnedUpdateBackups(fixture.request, journal.Backups); err != nil {
		t.Fatalf("successful M03 lost exact private backup authority: %v", err)
	}
	recoveryPath, _, err := updateTerminalRecoveryRecord(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(recoveryPath); !os.IsNotExist(err) {
		t.Fatalf("successful M03 created false terminal recovery summary: %v", err)
	}
	baseline := fixture.request.Plan.executionBaseline()
	candidate := LoadedManifestSource{Kind: fixture.request.Plan.Source.Kind, Source: fixture.request.Plan.Source.Value, data: fixture.request.Plan.CandidateManifestBytes()}
	input, err := NewUpdateSnapshotCollector(baseline.project, baseline.workspace, fixture.request.DataDir, candidate).CollectDriftSnapshot(context.Background())
	if err != nil {
		t.Fatalf("successful M03 operation inventory capture: %v", err)
	}
	if len(input.Operations) != 1 || input.Operations[0].Operation != "update" || input.Operations[0].Path != filepath.Dir(journalPath) {
		t.Fatalf("successful M03 update operation inventory=%#v", input.Operations)
	}
	_, err = BuildDriftSnapshot(input)
	if err == nil || !strings.Contains(err.Error(), "workspace does not contain repository") {
		t.Fatalf("unpublished M03 addition collector mismatch err=%v", err)
	}
}

func TestUpdateExecutProductionConfiguredFetchFailuresRestoreExactTrackingGeneration(t *testing.T) {
	for _, test := range []struct {
		name              string
		boundary          string
		boundaryErr       error
		moveAfterObserve  bool
		absentTracking    bool
		moveBeforeCleanup bool
	}{
		{name: "fetch-after", boundary: "repository-root-fetch-after", boundaryErr: errors.New("after fetch")},
		{name: "fetch-after-cancellation", boundary: "repository-root-fetch-after", boundaryErr: context.Canceled},
		{name: "response-mismatch", boundary: "repository-root-fetch", moveAfterObserve: true},
		{name: "created-tracking-ref-is-deleted", boundary: "repository-root-fetch-after", boundaryErr: errors.New("after fetch"), absentTracking: true},
		{name: "concurrent-tracking-movement-is-preserved", boundary: "repository-root-fetch-after", boundaryErr: errors.New("after fetch"), moveBeforeCleanup: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdateExecutionCrashFixture(t, false)
			prior := fixture.old["root"]
			if test.absentTracking {
				prior = ""
			}
			if err := fixture.git.RestoreConfiguredRef(context.Background(), fixture.paths["root"], gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", PreviousRemoteCommit: prior, ActualRemoteCommit: fixture.request.Plan.executionBaseline().observations[0].AdvertisedCommit}); err != nil {
				t.Fatal(err)
			}
			beforeTracking, err := fixture.git.ResolveRef(context.Background(), fixture.paths["root"], "refs/remotes/origin/main")
			if test.absentTracking {
				if err == nil {
					t.Fatalf("prepare absent tracking generation=%q", beforeTracking)
				}
			} else if err != nil || beforeTracking != prior {
				t.Fatalf("prepare prior tracking generation=%q err=%v", beforeTracking, err)
			}
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(step string) error {
				if step != test.boundary {
					return nil
				}
				if test.moveAfterObserve {
					fixture.rootSource.CommitFile("response-mismatch.txt", "moved\n", "move after observe")
					return nil
				}
				return test.boundaryErr
			}})
			effects, err := executor.productionEffects(context.Background(), fixture.request, fixture.snapshot)
			if err != nil || len(effects) == 0 || effects[0].Name != "repository-root-fast-forward" {
				t.Fatalf("production configured effect=%#v err=%v", effects, err)
			}
			_, err = effects[0].Prepare(context.Background())
			if err == nil || (test.boundaryErr == context.Canceled && !errors.Is(err, context.Canceled)) {
				t.Fatalf("configured fetch prepare error=%v", err)
			}
			moved := ""
			if test.moveBeforeCleanup {
				fixture.rootSource.CommitFile("concurrent-tracking.txt", "moved\n", "concurrent tracking movement")
				movedReceipt, fetchErr := fixture.git.FetchConfiguredRef(context.Background(), fixture.paths["root"], "origin", "refs/heads/main")
				if fetchErr != nil {
					t.Fatal(fetchErr)
				}
				moved = movedReceipt.ActualRemoteCommit
			}
			cleanupErr := effects[0].Cleanup(context.Background())
			if test.moveBeforeCleanup {
				if cleanupErr == nil {
					t.Fatal("configured fetch cleanup erased a concurrent tracking generation")
				}
			} else if cleanupErr != nil {
				t.Fatalf("configured fetch cleanup: %v", cleanupErr)
			}
			got, resolveErr := fixture.git.ResolveRef(context.Background(), fixture.paths["root"], "refs/remotes/origin/main")
			if test.absentTracking {
				if resolveErr == nil {
					t.Fatalf("cleanup retained operation-created tracking ref=%q", got)
				}
			} else if want := map[bool]string{true: moved, false: beforeTracking}[test.moveBeforeCleanup]; resolveErr != nil || got != want {
				t.Fatalf("tracking ref after cleanup=%q err=%v want=%q", got, resolveErr, want)
			}
			if got, headErr := fixture.git.Head(context.Background(), fixture.paths["root"]); headErr != nil || got != fixture.old["root"] {
				t.Fatalf("local branch after fetch cleanup=%q err=%v want=%q", got, headErr, fixture.old["root"])
			}
		})
	}
}

func TestUpdateExecutProductionConsumesConfiguredReceiptReturnedWithError(t *testing.T) {
	for _, test := range []struct {
		name       string
		cancel     bool
		createdRef bool
		concurrent bool
	}{
		{name: "mutation-return-error-restores-prior"},
		{name: "mutation-return-cancellation-restores-prior", cancel: true},
		{name: "mutation-return-error-deletes-created-ref", createdRef: true},
		{name: "mutation-return-error-preserves-concurrent-ref", concurrent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdateExecutionCrashFixture(t, false)
			prior := fixture.old["root"]
			if test.createdRef {
				prior = ""
			}
			if err := fixture.git.RestoreConfiguredRef(context.Background(), fixture.paths["root"], gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", PreviousRemoteCommit: prior, ActualRemoteCommit: fixture.request.Plan.executionBaseline().observations[0].AdvertisedCommit}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			injected := errors.New("fetch process failed after mutation")
			wrapped := &updateExecutionFetchReturnErrorGit{Git: fixture.git, returnErr: injected}
			if test.cancel {
				wrapped.after = cancel
				wrapped.returnErr = nil
			}
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: wrapped})
			effects, err := executor.productionEffects(context.Background(), fixture.request, fixture.snapshot)
			if err != nil || len(effects) == 0 {
				t.Fatalf("production configured effect=%#v err=%v", effects, err)
			}
			_, prepareErr := effects[0].Prepare(ctx)
			if test.cancel {
				if !errors.Is(prepareErr, context.Canceled) {
					t.Fatalf("prepare error=%v, want cancellation", prepareErr)
				}
			} else if !errors.Is(prepareErr, injected) {
				t.Fatalf("prepare error=%v, want injected process error", prepareErr)
			}
			moved := ""
			if test.concurrent {
				fixture.rootSource.CommitFile("concurrent-after-error", "concurrent\n", "concurrent tracking generation")
				receipt, err := fixture.git.FetchConfiguredRef(context.Background(), fixture.paths["root"], "origin", "refs/heads/main")
				if err != nil {
					t.Fatal(err)
				}
				moved = receipt.ActualRemoteCommit
			}
			cleanupErr := effects[0].Cleanup(context.Background())
			if test.concurrent {
				if cleanupErr == nil {
					t.Fatal("cleanup erased concurrent post-error tracking generation")
				}
			} else if cleanupErr != nil {
				t.Fatalf("cleanup partial configured fetch: %v", cleanupErr)
			}
			got, resolveErr := fixture.git.ResolveRef(context.Background(), fixture.paths["root"], "refs/remotes/origin/main")
			switch {
			case test.createdRef:
				if resolveErr == nil {
					t.Fatalf("cleanup retained operation-created tracking ref=%q", got)
				}
			case test.concurrent:
				if resolveErr != nil || got != moved {
					t.Fatalf("concurrent tracking ref=%q err=%v want=%q", got, resolveErr, moved)
				}
			default:
				if resolveErr != nil || got != prior {
					t.Fatalf("restored tracking ref=%q err=%v want=%q", got, resolveErr, prior)
				}
			}
			if got, err := fixture.git.Head(context.Background(), fixture.paths["root"]); err != nil || got != fixture.old["root"] {
				t.Fatalf("local branch after partial-fetch cleanup=%q err=%v", got, err)
			}
		})
	}
}

func TestUpdateExecutProductionRetainsIncompleteEvidenceWhenFetchMutationCannotBeProven(t *testing.T) {
	fixture := newUpdateExecutionCrashFixture(t, false)
	injected := errors.New("fetch process failed and local recapture was unavailable")
	wrapped := &updateExecutionFetchReturnErrorGit{Git: fixture.git, returnErr: injected, discardReceipt: true}
	_, err := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: wrapped}).Execute(context.Background(), fixture.request)
	var application *Error
	if err == nil || !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete || HasCleanRollback(err) {
		t.Fatalf("unproven fetch mutation error=%v, want rollback_incomplete", err)
	}
	journalPath, pathErr := UpdateJournalPath(fixture.request.DataDir, fixture.request.ProjectID, fixture.request.OperationID)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	journalBytes, readErr := os.ReadFile(journalPath)
	journal, decodeErr := decodeStrictUpdateJournal(journalBytes)
	if readErr != nil || decodeErr != nil || journal.RollbackState != "incomplete" || len(journal.Progress) != 1 || journal.Progress[0].State != "unreverted" || journal.Progress[0].Receipt != "" {
		t.Fatalf("unproven fetch recovery evidence=%#v read=%v decode=%v", journal, readErr, decodeErr)
	}
	if got, headErr := fixture.git.Head(context.Background(), fixture.paths["root"]); headErr != nil || got != fixture.old["root"] {
		t.Fatalf("unproven fetch moved local branch=%q err=%v", got, headErr)
	}
	if recoverErr := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: fixture.git}).Recover(context.Background(), fixture.request); recoverErr == nil {
		t.Fatal("fresh recovery falsely classified the unproven tracking mutation as clean")
	}
}

func assertUpdateSecondPreflight(t *testing.T, request UpdateExecutionRequest) {
	t.Helper()
	baseline := request.Plan.executionBaseline()
	snapshot, candidate, err := CollectUpdateSnapshot(context.Background(), baseline.project, baseline.workspace, request.DataDir, request.Plan.Source.Value, NewManifestSourceLoader())
	if err != nil {
		t.Fatalf("second production snapshot: %v", err)
	}
	if _, err := BuildUpdatePlan(snapshot, candidate); err != nil {
		t.Fatalf("second production plan: %v", err)
	}
}

func crashUpdateExecutorAt(t *testing.T, request UpdateExecutionRequest, wanted string) {
	t.Helper()
	crashed := false
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered != wanted {
					t.Fatalf("unexpected crash %#v", recovered)
				}
				crashed = true
			}
		}()
		_, err := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(step string) error {
			if step == wanted {
				panic(wanted)
			}
			return nil
		}}).Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("Execute() before simulated crash: %v", err)
		}
	}()
	if !crashed {
		t.Fatalf("executor did not reach crash boundary %q", wanted)
	}
}

type updateExecutionCrashFixture struct {
	request    UpdateExecutionRequest
	git        *gitadapter.Adapter
	rootSource testutil.PushedGitRepository
	snapshot   DriftSnapshot
	paths      map[string]string
	old        map[string]string
	current    []byte
}

func newUpdateExecutionCrashFixture(t *testing.T, nested bool) updateExecutionCrashFixture {
	t.Helper()
	ctx := context.Background()
	adapter := gitadapter.NewAdapter("git")
	rootSource := testutil.NewPushedGitRepository(t)
	addedSource := testutil.NewPushedGitRepository(t)
	rootSource.CommitFile(".gitignore", "/parent/\n/removed/\n/added/\n", "ignore nested checkouts")
	addedSource.CommitFile("added.txt", "initial added\n", "initial added")
	var parentSource, childSource testutil.PushedGitRepository
	if nested {
		parentSource = testutil.NewPushedGitRepository(t)
		childSource = testutil.NewPushedGitRepository(t)
		parentSource.CommitFile(".gitignore", "/child/\n/added/\n", "ignore nested checkouts")
		childSource.CommitFile("child.txt", "initial child\n", "initial child")
	}

	upstream := func(source testutil.PushedGitRepository) gitadapter.Upstream {
		value, err := adapter.Upstream(ctx, source.Path)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	head := func(path string) string {
		value, err := adapter.Head(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	rootIdentity, addedIdentity := head(rootSource.Path), head(addedSource.Path)
	rootUpstream, addedUpstream := upstream(rootSource), upstream(addedSource)
	var parentIdentity, childIdentity string
	var parentUpstream, childUpstream gitadapter.Upstream
	if nested {
		parentIdentity, childIdentity = head(parentSource.Path), head(childSource.Path)
		parentUpstream, childUpstream = upstream(parentSource), upstream(childSource)
	}
	portable := func(value gitadapter.Upstream, identity, parent, mount string) config.PortableRepository {
		return config.PortableRepository{Clone: config.CloneSource{Remote: "origin", URL: value.FetchURL}, Upstream: config.Upstream{Remote: "origin", Branch: "main", Merge: "refs/heads/main"}, Identity: config.RepositoryIdentity{InitialCommits: []string{identity}}, Parent: parent, Mount: mount, DefaultBranch: "main"}
	}
	root := t.TempDir()
	candidatePath := filepath.Join(t.TempDir(), "candidate space ñ.wtree.yml")
	localRepositories := map[string]config.Repository{"root": {Source: ".", DefaultMount: ".", DefaultBranch: "main"}}
	if nested {
		localRepositories["parent"] = config.Repository{Source: "parent", Parent: "root", DefaultMount: "parent", DefaultBranch: "main"}
		localRepositories["child"] = config.Repository{Source: "parent/child", Parent: "parent", DefaultMount: "child", DefaultBranch: "main"}
		localRepositories["removed"] = config.Repository{Source: "removed", Parent: "root", DefaultMount: "removed", DefaultBranch: "main"}
	}
	local := config.ProjectConfig{Version: config.ProjectConfigVersion, Project: config.Project{ID: "project", Name: "crash fixture", BaseRepository: "root"}, LogicalRoot: ".", Worktrees: config.Worktrees{Root: filepath.Join(t.TempDir(), "worktrees")}, Manifest: config.ManifestMetadata{Path: "project.wtree.yml", Source: candidatePath}, Repositories: localRepositories}
	localBytes, err := config.MarshalProject(local)
	if err != nil {
		t.Fatal(err)
	}
	rootSource.CommitFile(".wtree.yml", string(localBytes), "tracked local configuration")
	currentRepositories := map[string]config.PortableRepository{"root": portable(rootUpstream, rootIdentity, "", ".")}
	if nested {
		currentRepositories["parent"] = portable(parentUpstream, parentIdentity, "root", "parent")
		currentRepositories["child"] = portable(childUpstream, childIdentity, "parent", "child")
		currentRepositories["removed"] = portable(addedUpstream, addedIdentity, "root", "removed")
	}
	currentManifest := config.PortableManifest{Version: config.PortableManifestVersion, Project: config.PortableProject{ID: "project", Name: "crash fixture", BaseRepository: "root"}, Repositories: currentRepositories}
	currentBytes, err := config.MarshalPortableManifest(currentManifest)
	if err != nil {
		t.Fatal(err)
	}
	rootSource.CommitFile("project.wtree.yml", string(currentBytes), "current portable manifest")

	paths := map[string]string{"root": root, "added": filepath.Join(root, "added")}
	if nested {
		paths["parent"] = filepath.Join(root, "parent")
		paths["child"] = filepath.Join(root, "parent", "child")
		paths["removed"] = filepath.Join(root, "removed")
	}
	clone := func(remote, path string) {
		if err := adapter.Clone(ctx, remote, path, "origin"); err != nil {
			t.Fatal(err)
		}
		if err := adapter.FetchTrackingBranch(ctx, path, "origin", "refs/heads/main"); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.CheckoutTrackingBranch(ctx, path, "main", "origin", "refs/heads/main"); err != nil {
			t.Fatal(err)
		}
	}
	clone(rootUpstream.FetchURL, paths["root"])
	old := map[string]string{"root": head(paths["root"])}
	if nested {
		clone(parentUpstream.FetchURL, paths["parent"])
		clone(childUpstream.FetchURL, paths["child"])
		clone(addedUpstream.FetchURL, paths["removed"])
		old["parent"], old["child"], old["removed"] = head(paths["parent"]), head(paths["child"]), head(paths["removed"])
	}

	candidateManifest := currentManifest
	candidateManifest.Repositories = make(map[string]config.PortableRepository, len(currentManifest.Repositories))
	for id, repository := range currentManifest.Repositories {
		candidateManifest.Repositories[id] = repository
	}
	if nested {
		delete(candidateManifest.Repositories, "removed")
	} else {
		candidateManifest.Repositories["added"] = portable(addedUpstream, addedIdentity, "root", "added")
	}
	candidateBytes, err := config.MarshalPortableManifest(candidateManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, candidateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".wtree.yml")
	common := map[string]string{}
	ids := []string{"root"}
	if nested {
		ids = append(ids, "parent", "child", "removed")
	}
	for _, id := range ids {
		value, err := adapter.CommonGitDir(ctx, paths[id])
		if err != nil {
			t.Fatal(err)
		}
		common[id] = value
	}
	projectRepositories := []domain.Repository{{ID: "root", CommonGitDir: common["root"], SourcePath: paths["root"], DefaultMount: ".", DefaultBranch: "main"}}
	workspaceCheckouts := []domain.Checkout{{RepositoryID: "root", Branch: "main", Head: old["root"], Mount: ".", ResolvedPath: paths["root"]}}
	if nested {
		projectRepositories = append(projectRepositories, domain.Repository{ID: "parent", CommonGitDir: common["parent"], SourcePath: paths["parent"], ParentID: "root", DefaultMount: "parent", DefaultBranch: "main"}, domain.Repository{ID: "child", CommonGitDir: common["child"], SourcePath: paths["child"], ParentID: "parent", DefaultMount: "child", DefaultBranch: "main"}, domain.Repository{ID: "removed", CommonGitDir: common["removed"], SourcePath: paths["removed"], ParentID: "root", DefaultMount: "removed", DefaultBranch: "main"})
		workspaceCheckouts = []domain.Checkout{
			// workspaceFromState sorts repository IDs, so keep the in-memory
			// authoritative workspace in that same canonical order.
			{RepositoryID: "child", Branch: "main", Head: old["child"], Mount: "child", ResolvedPath: paths["child"]}, {RepositoryID: "parent", Branch: "main", Head: old["parent"], Mount: "parent", ResolvedPath: paths["parent"]}, {RepositoryID: "removed", Branch: "main", Head: old["removed"], Mount: "removed", ResolvedPath: paths["removed"]}, {RepositoryID: "root", Branch: "main", Head: old["root"], Mount: ".", ResolvedPath: paths["root"]},
		}
	}
	project := domain.Project{Version: domain.CurrentVersion, ID: "project", Name: "crash fixture", ConfigPath: configPath, BaseRepository: "root", LogicalRoot: root, Repositories: projectRepositories}
	workspace := domain.Workspace{Version: domain.CurrentVersion, ID: "default", Name: "default", RootPath: root, Checkouts: workspaceCheckouts}
	dataDir := filepath.Join(t.TempDir(), "data with ñ")
	if err := store.WriteRegistry(filepath.Join(dataDir, "registry.json"), driftRegistry(project)); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteWorkspace(WorkspaceStatePath(dataDir, project.ID, "default"), driftWorkspaceState(workspace)); err != nil {
		t.Fatal(err)
	}

	// These are the selected execution tips. The collector observes them, and
	// Execute observes/fetches them again while holding the project lock.
	if !nested {
		rootSource.CommitFile("project.wtree.yml", string(candidateBytes), "candidate portable manifest")
	}
	if nested {
		childSource.CommitFile("child.txt", "updated child\n", "update child")
	}
	fetchIDs := []string{"root"}
	if nested {
		fetchIDs = []string{"child"}
	}
	for _, id := range fetchIDs {
		if err := adapter.FetchTrackingBranch(ctx, paths[id], "origin", "refs/heads/main"); err != nil {
			t.Fatal(err)
		}
	}
	collector := NewUpdateSnapshotCollector(project, workspace, dataDir, LoadedManifestSource{Kind: ManifestSourceLocal, Source: candidatePath, data: candidateBytes})
	input, err := collector.CollectDriftSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildDriftSnapshot(input)
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("production snapshot err=%v failures=%#v", err, snapshot.Failures())
	}
	plan, err := BuildUpdatePlan(snapshot, LoadedManifestSource{Kind: ManifestSourceLocal, Source: candidatePath, data: candidateBytes})
	if err != nil {
		t.Fatal(err)
	}
	return updateExecutionCrashFixture{request: UpdateExecutionRequest{DataDir: dataDir, ProjectID: project.ID, OperationID: "operation-crash-reopen", Plan: plan}, git: adapter, rootSource: rootSource, snapshot: snapshot, paths: paths, old: old, current: currentBytes}
}

func (fixture updateExecutionCrashFixture) assertRestored(t *testing.T) {
	t.Helper()
	for _, id := range []string{"root", "parent", "child", "removed"} {
		if fixture.old[id] == "" {
			continue
		}
		got, err := fixture.git.Head(context.Background(), fixture.paths[id])
		if err != nil || got != fixture.old[id] {
			t.Fatalf("%s HEAD after recovery=%q want=%q err=%v", id, got, fixture.old[id], err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(fixture.paths["root"], "project.wtree.yml"))
	if err != nil || !bytes.Equal(manifest, fixture.current) {
		t.Fatalf("tracked manifest after recovery=%q err=%v", manifest, err)
	}
	if info, err := os.Stat(filepath.Join(fixture.paths["root"], "project.wtree.yml")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("tracked manifest mode after recovery=%v err=%v", info, err)
	}
	if sha256String(manifest) != sha256String(fixture.current) {
		t.Fatal("tracked manifest SHA-256 changed during crash recovery")
	}
	if _, err := os.Lstat(fixture.paths["added"]); !os.IsNotExist(err) {
		t.Fatalf("operation-owned added checkout remains: %v", err)
	}
	stage := filepath.Join(fixture.request.DataDir, "projects", fixture.request.ProjectID, "update", fixture.request.OperationID, "staging", "added")
	if _, err := os.Lstat(stage); !os.IsNotExist(err) {
		t.Fatalf("operation-owned staging checkout remains: %v", err)
	}
}

func TestUpdateExecutJournalRemovalUsesExactOwnedBytes(t *testing.T) {
	plan := updateExecutorPlan(t)
	data := t.TempDir()
	removed := false
	executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Remove: func(path string, compare func() error) error {
		if err := os.WriteFile(path, []byte("concurrent replacement"), 0o600); err != nil {
			return err
		}
		if err := compare(); err == nil {
			t.Fatal("journal removal compare accepted replacement")
		}
		return errors.New("refuse replacement")
	}})
	request := UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-cas", Plan: plan}
	_, err := executeUpdateForTest(context.Background(), executor, request, updateExecutorRecapture(t), []updateEffect{{Name: "root", Execute: func(context.Context) (string, error) { return "head", nil }, Rollback: func(context.Context) error { removed = true; return nil }}})
	if err == nil || removed {
		t.Fatalf("err=%v rollback=%t", err, removed)
	}
	path, _ := UpdateJournalPath(data, "project", "operation-cas")
	if bytes, readErr := os.ReadFile(path); readErr != nil || string(bytes) != "concurrent replacement" {
		t.Fatalf("concurrent journal replacement lost: %q %v", bytes, readErr)
	}
	recoveryPath, _, recoveryErr := updateTerminalRecoveryRecord(request)
	if recoveryErr != nil {
		t.Fatal(recoveryErr)
	}
	if recovery, readErr := store.ReadRecovery(recoveryPath); readErr != nil || recovery.Operation != "update" || recovery.FailedStep != "terminal-cleanup" {
		t.Fatalf("terminal recovery=%#v err=%v", recovery, readErr)
	}
}

func TestUpdateExecutTerminalCleanupRetainsSummaryAndResumesWithoutRepositoryInverse(t *testing.T) {
	plan := updateExecutorPlan(t)
	data := t.TempDir()
	request := UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-terminal-resume", Plan: plan}
	path, err := UpdateJournalPath(data, request.ProjectID, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := newUpdateJournal(request)
	if err != nil {
		t.Fatal(err)
	}
	journal.Progress = []UpdateJournalEffect{{Sequence: 1, Name: "root", Repository: "root", Receipt: driftOID('2'), State: "completed"}}
	journal, err = terminalCleanupJournal(journal, "success")
	if err != nil || writeNewUpdateJournal(NewUpdateExecutor(), path, journal) != nil {
		t.Fatalf("prepare cleaning journal err=%v", err)
	}
	child := filepath.Join(filepath.Dir(path), "concurrent-child")
	blocked := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(step string) error {
		if step == "terminal-cleanup-operation-remove-before" {
			return os.WriteFile(child, []byte("concurrent"), 0o600)
		}
		return nil
	}})
	if err := blocked.terminalCleanup(context.Background(), request, path, journal, "success"); err == nil {
		t.Fatal("terminal cleanup unexpectedly removed a concurrent child")
	}
	retained, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("cleanup failure lost reconstructed journal: %v", readErr)
	}
	decoded, decodeErr := decodeStrictUpdateJournal(retained)
	if decodeErr != nil || decoded.RollbackState != "incomplete" || decoded.TerminalOutcome != "success" || len(decoded.Progress) != 2 || decoded.Progress[0].State != "completed" || decoded.Progress[1].Name != "terminal-cleanup" || decoded.Progress[1].State != "unreverted" {
		t.Fatalf("retained terminal cleanup journal=%#v err=%v", decoded, decodeErr)
	}
	recoveryPath, _, recoveryErr := updateTerminalRecoveryRecord(request)
	if recoveryErr != nil {
		t.Fatal(recoveryErr)
	}
	if recovery, err := store.ReadRecovery(recoveryPath); err != nil || recovery.Operation != "update" || !reflect.DeepEqual(recovery.UnrevertedSteps, []string{"terminal-cleanup"}) {
		t.Fatalf("retained terminal summary=%#v err=%v", recovery, err)
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}
	if err := NewUpdateExecutor().Recover(context.Background(), request); err != nil {
		t.Fatalf("resume cleanup-only recovery: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("resumed cleanup retained operation directory: %v", err)
	}
	if _, err := os.Lstat(recoveryPath); !os.IsNotExist(err) {
		t.Fatalf("resumed cleanup retained owned recovery summary: %v", err)
	}
}

func TestUpdateExecutTerminalCleanupToleratesMissingOpaqueBackupAtEveryPosition(t *testing.T) {
	for _, missing := range []int{0, 1, 2} {
		t.Run([]string{"first", "middle", "last"}[missing], func(t *testing.T) {
			plan := updateExecutorPlan(t)
			root, data := t.TempDir(), t.TempDir()
			request := UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-missing-" + []string{"first", "middle", "last"}[missing], Plan: plan}
			baseline := plan.executionBaseline()
			sources := []updateBackupSource{
				{kind: "default-state", path: filepath.Join(root, "default.json")},
				{kind: "local-config", path: filepath.Join(root, "project.json")},
				{kind: "registry", path: filepath.Join(root, "registry.json")},
			}
			expected := map[string][]byte{"default-state": baseline.defaultState.Bytes, "local-config": baseline.localConfig, "registry": baseline.registry}
			for _, source := range sources {
				if err := os.WriteFile(source.path, expected[source.kind], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := prepareUpdateBackupSources(sources, expected)
			if err != nil {
				t.Fatalf("prepare opaque backups: %v", err)
			}
			if err := writeUpdateBackups(request, prepared); err != nil {
				t.Fatalf("write opaque backups: %v", err)
			}
			journal, err := newUpdateJournal(request)
			if err != nil {
				t.Fatal(err)
			}
			journal.Backups = backupMetadata(prepared)
			journal, err = terminalCleanupJournal(journal, "success")
			if err != nil {
				t.Fatal(err)
			}
			path, err := UpdateJournalPath(data, request.ProjectID, request.OperationID)
			if err != nil || writeNewUpdateJournal(NewUpdateExecutor(), path, journal) != nil {
				t.Fatalf("write cleaning journal path=%q err=%v", path, err)
			}
			directory, err := updateBackupDirectory(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(directory, journal.Backups[missing].File)); err != nil {
				t.Fatal(err)
			}
			if err := NewUpdateExecutor().terminalCleanup(context.Background(), request, path, journal, "success"); err != nil {
				t.Fatalf("resume missing opaque backup cleanup: %v", err)
			}
			if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("cleaned operation remains: %v", err)
			}
		})
	}
}

func TestUpdateExecutSummaryOnlyRecoveryRequiresExactSummaryAndAbsentOperation(t *testing.T) {
	for _, test := range []struct {
		name          string
		summary       string
		operation     bool
		wantSuccess   bool
		wantOperation bool
	}{
		{name: "absent-summary-absent-operation", wantSuccess: true},
		{name: "absent-summary-present-operation", operation: true, wantOperation: true},
		{name: "changed-summary-absent-operation", summary: "changed"},
		{name: "changed-summary-present-operation", summary: "changed", operation: true, wantOperation: true},
		{name: "owned-summary-absent-operation", summary: "owned", wantSuccess: true},
		{name: "owned-summary-present-empty-operation", summary: "owned", operation: true, wantSuccess: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := UpdateExecutionRequest{DataDir: t.TempDir(), ProjectID: "project", OperationID: "operation-summary-authority", Plan: updateExecutorPlan(t)}
			journalPath, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if test.operation {
				if err := ensureUpdateJournalParent(filepath.Dir(journalPath)); err != nil {
					t.Fatal(err)
				}
			}
			recoveryPath, recovery, err := updateTerminalRecoveryRecord(request)
			if err != nil {
				t.Fatal(err)
			}
			switch test.summary {
			case "owned":
				if err := store.WriteRecovery(recoveryPath, recovery); err != nil {
					t.Fatal(err)
				}
			case "changed":
				if err := os.MkdirAll(filepath.Dir(recoveryPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(recoveryPath, []byte("{\"version\":1,\"operation\":\"concurrent\"}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			err = NewUpdateExecutor().Recover(context.Background(), request)
			if test.wantSuccess {
				if err != nil {
					t.Fatalf("summary-only recovery: %v", err)
				}
			} else {
				var application *Error
				if err == nil || !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
					t.Fatalf("summary-only recovery error=%v, want rollback_incomplete", err)
				}
			}
			_, operationErr := os.Lstat(filepath.Dir(journalPath))
			if test.wantOperation != (operationErr == nil) {
				t.Fatalf("operation authority err=%v want retained=%t", operationErr, test.wantOperation)
			}
			if test.summary == "changed" {
				got, readErr := os.ReadFile(recoveryPath)
				if readErr != nil || string(got) != "{\"version\":1,\"operation\":\"concurrent\"}\n" {
					t.Fatalf("changed recovery summary=%q err=%v", got, readErr)
				}
			} else if test.wantSuccess {
				if _, statErr := os.Lstat(recoveryPath); !os.IsNotExist(statErr) {
					t.Fatalf("successful recovery retained summary: %v", statErr)
				}
			}
		})
	}
}

func TestUpdateExecutTerminalCleanupBehaviorAtEveryOpaqueBackupUnlinkBoundary(t *testing.T) {
	for _, side := range []string{"before", "after"} {
		for _, kind := range []string{"default-state", "local-config", "registry", "tracked-manifest"} {
			t.Run(kind+"-"+side, func(t *testing.T) {
				plan, sourceRoot := updateExecutorPlan(t), t.TempDir()
				request := UpdateExecutionRequest{DataDir: t.TempDir(), ProjectID: "project", OperationID: "operation-blob-" + kind + "-" + side, Plan: plan}
				baseline := plan.executionBaseline()
				sources := []updateBackupSource{
					{kind: "default-state", path: filepath.Join(sourceRoot, "default.json")},
					{kind: "local-config", path: filepath.Join(sourceRoot, "project.json")},
					{kind: "registry", path: filepath.Join(sourceRoot, "registry.json")},
					{kind: "tracked-manifest", path: filepath.Join(sourceRoot, "project.wtree.yml")},
				}
				expected := map[string][]byte{"default-state": baseline.defaultState.Bytes, "local-config": baseline.localConfig, "registry": baseline.registry, "tracked-manifest": baseline.current.Bytes}
				for _, source := range sources {
					if err := os.WriteFile(source.path, expected[source.kind], 0o600); err != nil {
						t.Fatal(err)
					}
				}
				prepared, err := prepareUpdateBackupSources(sources, expected)
				if err != nil || writeUpdateBackups(request, prepared) != nil {
					t.Fatalf("prepare opaque backups: %v", err)
				}
				journal, err := newUpdateJournal(request)
				if err != nil {
					t.Fatal(err)
				}
				journal.Backups = backupMetadata(prepared)
				journal, err = terminalCleanupJournal(journal, "rolled-back")
				if err != nil {
					t.Fatal(err)
				}
				path, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeNewUpdateJournal(NewUpdateExecutor(), path, journal); err != nil {
					t.Fatalf("write cleanup journal: %v", err)
				}
				step := "terminal-cleanup-backup-" + kind + "-remove-" + side
				crashing := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(actual string) error {
					if actual == step {
						return errors.New("simulated terminal-cleanup crash")
					}
					return nil
				}})
				if err := crashing.terminalCleanup(context.Background(), request, path, journal, "rolled-back"); err == nil {
					t.Fatal("terminal cleanup boundary unexpectedly succeeded")
				}
				backupDirectory, err := updateBackupDirectory(request)
				if err != nil {
					t.Fatal(err)
				}
				_, blobErr := os.Lstat(filepath.Join(backupDirectory, kind+".bin"))
				if (side == "before") != (blobErr == nil) {
					t.Fatalf("blob state after %s boundary err=%v", side, blobErr)
				}
				retained, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("boundary lost strict journal: %v", err)
				}
				decoded, err := decodeStrictUpdateJournal(retained)
				if err != nil || decoded.TerminalOutcome != "rolled-back" || decoded.RollbackState != "incomplete" {
					t.Fatalf("retained cleanup journal=%#v err=%v", decoded, err)
				}
				if err := NewUpdateExecutor().Recover(context.Background(), request); err != nil {
					t.Fatalf("fresh recovery after %s: %v", step, err)
				}
				if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("idempotent cleanup retained operation authority: %v", err)
				}
				if err := NewUpdateExecutor().Recover(context.Background(), request); err != nil {
					t.Fatalf("idempotent second recovery: %v", err)
				}
			})
		}
	}
}

func TestUpdateExecutTerminalCleanupBoundariesRetainExactEvidence(t *testing.T) {
	steps := []string{
		"terminal-cleanup-summary-publish-before",
		"terminal-cleanup-summary-publish-after",
		"terminal-cleanup-backup-directory-remove-before",
		"terminal-cleanup-backup-directory-remove-after",
		"terminal-cleanup-staging-remove-before",
		"terminal-cleanup-staging-remove-after",
		"journal-terminal-cleanup-remove-before",
		"journal-terminal-cleanup-remove-after",
		"terminal-cleanup-operation-remove-before",
		"terminal-cleanup-operation-remove-after",
		"terminal-cleanup-summary-remove-before",
		"terminal-cleanup-summary-remove-after",
	}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			plan, data := updateExecutorPlan(t), t.TempDir()
			request := UpdateExecutionRequest{DataDir: data, ProjectID: "project", OperationID: "operation-boundary-" + strings.ReplaceAll(strings.TrimPrefix(step, "terminal-cleanup-"), "_", "-"), Plan: plan}
			executor := NewUpdateExecutorWith(UpdateExecutorDependencies{Before: func(actual string) error {
				if actual == "journal-terminal-cleanup-start-after" {
					operation, pathErr := UpdateJournalPath(data, request.ProjectID, request.OperationID)
					if pathErr != nil {
						return pathErr
					}
					if err := os.MkdirAll(filepath.Join(filepath.Dir(operation), "backups"), 0o700); err != nil {
						return err
					}
					return os.MkdirAll(filepath.Join(filepath.Dir(operation), "staging"), 0o700)
				}
				if actual == step {
					return errors.New("terminal cleanup boundary")
				}
				return nil
			}})
			_, err := executeUpdateForTest(context.Background(), executor, request, updateExecutorRecapture(t), []updateEffect{{Name: "root", Execute: func(context.Context) (string, error) { return "head", nil }, Rollback: func(context.Context) error { return nil }}})
			if step == "terminal-cleanup-summary-remove-after" {
				if err != nil {
					t.Fatalf("after owned summary removal is a committed cleanup, err=%v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("terminal cleanup boundary unexpectedly succeeded")
			}
			path, pathErr := UpdateJournalPath(data, request.ProjectID, request.OperationID)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			recoveryPath, _, recoveryErr := updateTerminalRecoveryRecord(request)
			if recoveryErr != nil {
				t.Fatal(recoveryErr)
			}
			journalBytes, journalErr := os.ReadFile(path)
			_, summaryErr := store.ReadRecovery(recoveryPath)
			if journalErr != nil && summaryErr != nil {
				t.Fatalf("boundary left no strict journal or recovery summary: journal=%v summary=%v", journalErr, summaryErr)
			}
			if journalErr == nil {
				if _, err := decodeStrictUpdateJournal(journalBytes); err != nil {
					t.Fatalf("boundary retained non-strict journal: %v", err)
				}
			}
		})
	}
}

func TestUpdateExecutPreparesPrivateRetainedFactsWithoutTouchingCheckout(t *testing.T) {
	current := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", "."), "old": driftRepository("root", "old")})
	candidate := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "old", ParentID: "root", DefaultMount: "old", DefaultBranch: "main", CommonGitDir: "/git/old", SourcePath: "/tree/old"}, {ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: current, CandidateManifest: candidate, Observations: []DriftRepositoryObservation{updateExecutorObservation(), {RepositoryID: "old", Path: "/tree/old", CommonGitDir: "/git/old", Branch: "main", Head: driftOID('0'), Clean: true, IgnoreKnown: true, IgnoreVerified: true, UpstreamKnown: true, Upstream: gitadapter.Upstream{LocalBranch: "main", Remote: "origin", Merge: "refs/heads/main", FetchURL: "https://example.test/project.git"}}}})
	if err != nil || !snapshot.MayUpdate() {
		t.Fatalf("snapshot=%#v err=%v", snapshot.Failures(), err)
	}
	facts, err := updateRetainedFacts(snapshot, sha256String(candidate))
	if err != nil || !reflect.DeepEqual(facts, []UpdateRetainedFact{{RepositoryID: "old", Path: driftFixturePath("/tree/old"), CommonGitDir: driftFixturePath("/git/old"), CandidateSHA256: sha256String(candidate)}}) {
		t.Fatalf("facts=%#v err=%v", facts, err)
	}
}

func TestUpdateExecutOpaqueBackupsAreExactPrivateAndTamperEvident(t *testing.T) {
	root := t.TempDir()
	request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-backup"}
	manifest := append(driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")}), []byte("# caf\u00e9\n")...)
	sourcePath := filepath.Join(root, "tree", "project.wtree.yml")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, manifest, 0o640); err != nil {
		t.Fatal(err)
	}
	sources, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "tracked-manifest", path: sourcePath}}, map[string][]byte{"tracked-manifest": manifest})
	if err != nil {
		t.Fatal(err)
	}
	metadata := backupMetadata(sources)
	if len(metadata) != 1 || metadata[0].SHA256 != sha256String(manifest) {
		t.Fatalf("metadata=%#v", metadata)
	}
	if sha256String([]byte("abc")) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("SHA-256 implementation is not the standard digest")
	}
	if err := writeUpdateBackups(request, sources); err != nil {
		t.Fatal(err)
	}
	directory, err := updateBackupDirectory(request)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(directory, "tracked-manifest.bin")
	got, err := os.ReadFile(backupPath)
	if err != nil || !bytes.Equal(got, manifest) {
		t.Fatalf("backup exact bytes=%q err=%v", got, err)
	}
	if info, err := os.Stat(backupPath); err != nil || !requestedFilePermissionsMatch(info.Mode(), 0o600) {
		t.Fatalf("backup protection=%v err=%v", info, err)
	}
	if info, err := os.Stat(directory); err != nil || validatePrivateUpdateDirectory(directory, info) != nil {
		t.Fatalf("backup directory protection=%v err=%v", info, err)
	}
	journal := UpdateJournal{Version: UpdateJournalVersion, OperationID: request.OperationID, ProjectID: request.ProjectID, PlanDigest: strings.Repeat("a", 64), Generations: UpdatePlanGenerations{CurrentManifestSHA256: strings.Repeat("a", 64), CandidateManifestSHA256: strings.Repeat("b", 64), LocalConfigSHA256: strings.Repeat("c", 64), RegistrySHA256: strings.Repeat("d", 64), DefaultStateSHA256: strings.Repeat("e", 64)}, Backups: metadata, RollbackState: "active", Progress: []UpdateJournalEffect{}}
	encoded, err := json.Marshal(journal)
	if err != nil || bytes.Contains(encoded, manifest) || bytes.Contains(encoded, []byte("caf\u00e9")) {
		t.Fatalf("journal leaked backup bytes=%q err=%v", encoded, err)
	}
	if err := os.WriteFile(backupPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateUpdateBackupBlob(backupPath, metadata[0]); err == nil {
		t.Fatal("accepted a tampered opaque backup")
	}
	if err := os.WriteFile(backupPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedUpdateBackups(request, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("clean backup directory remains: %v", err)
	}
}

func TestUpdateExecutOpaqueBackupRefusesSecretsAndRepresentsAbsence(t *testing.T) {
	root := t.TempDir()
	unsafe := filepath.Join(root, "unsafe.yml")
	if err := os.WriteFile(unsafe, []byte("https://user:secret@example.test/path?token=secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "tracked-manifest", path: unsafe}}, map[string][]byte{"tracked-manifest": []byte("https://user:secret@example.test/path?token=secret")}); err == nil {
		t.Fatal("prepared a secret-shaped source for persistence")
	}
	missing := filepath.Join(root, "reconciliation.json")
	sources, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "reconciliation", path: missing}}, map[string][]byte{"reconciliation": nil})
	if err != nil {
		t.Fatal(err)
	}
	metadata := backupMetadata(sources)
	if metadata[0].Existed || metadata[0].Mode != 0 || metadata[0].Length != 0 || metadata[0].SHA256 != "" {
		t.Fatalf("absent backup metadata=%#v", metadata[0])
	}
}

func TestUpdateExecutOpaqueBackupScansCompleteLargeGenerations(t *testing.T) {
	root := t.TempDir()
	valid := append(driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")}), []byte(strings.Repeat("# credential-free padding\n", 600))...)
	path := filepath.Join(root, "project.wtree.yml")
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "tracked-manifest", path: path}}, map[string][]byte{"tracked-manifest": valid}); err != nil {
		t.Fatalf("credential-free >8KiB manifest was rejected: %v", err)
	}
	secret := append([]byte(strings.Repeat("x", 8191)), []byte("https://user:boundary-secret@example.test/repository")...)
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "tracked-manifest", path: path}}, map[string][]byte{"tracked-manifest": secret}); err == nil || strings.Contains(err.Error(), "boundary-secret") {
		t.Fatalf("boundary credential was persisted or leaked: %v", err)
	}
}

func TestUpdateExecutRetainedAuthorityRejectsCompleteSecretShapes(t *testing.T) {
	for _, path := range []string{"/work/project?token=secret/old", "/work/https://user:secret@example.test/old"} {
		if safeRetainedUpdateAuthorityPath(path) {
			t.Fatalf("accepted secret-shaped retained authority %q", path)
		}
	}
	if !safeRetainedUpdateAuthorityPath("/work/space ü/old repository/.git") {
		t.Fatal("rejected safe Unicode/spaced retained authority")
	}
}

func TestUpdateExecutOpaqueBackupSupportsStrictM04GenerationsWithoutPublishing(t *testing.T) {
	root := t.TempDir()
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: filepath.Join(root, ".git"), SourcePath: root}})
	project.ConfigPath, project.LogicalRoot = filepath.Join(root, ".wtree.yml"), root
	local, err := config.MarshalProject(driftLocalConfig(project))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := store.RegistryBytes(driftRegistry(project))
	if err != nil {
		t.Fatal(err)
	}
	workspace := driftWorkspace(project)
	state, err := store.WorkspaceBytes(driftWorkspaceState(workspace))
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"local-config": filepath.Join(root, "local.yml"), "default-state": filepath.Join(root, "default.json"), "registry": filepath.Join(root, "registry.json")}
	bytesByKind := map[string][]byte{"local-config": local, "default-state": state, "registry": registry}
	for kind, path := range paths {
		if err := os.WriteFile(path, bytesByKind[kind], 0o640); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "default-state", path: paths["default-state"]}, {kind: "local-config", path: paths["local-config"]}, {kind: "registry", path: paths["registry"]}}, bytesByKind)
	if err != nil {
		t.Fatal(err)
	}
	request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-m04-contract"}
	if err := writeUpdateBackups(request, sources); err != nil {
		t.Fatal(err)
	}
	metadata := backupMetadata(sources)
	for _, backup := range metadata {
		path, err := updateBackupDirectory(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(path, backup.File))
		if err != nil || !bytes.Equal(data, bytesByKind[backup.Kind]) {
			t.Fatalf("%s backup=%q err=%v", backup.Kind, data, err)
		}
	}
	if err := removeOwnedUpdateBackups(request, metadata); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateExecutRecoveryReopensStrictJournalAndRestoresExactTrackedManifest(t *testing.T) {
	root := t.TempDir()
	original := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	candidate := append(append([]byte(nil), original...), []byte("# selected execution generation\n")...)
	target := filepath.Join(root, "project.wtree.yml")
	if err := os.WriteFile(target, original, 0o640); err != nil {
		t.Fatal(err)
	}
	plan := updateRecoveryPlan(t, target, original, candidate)
	request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-recover", Plan: plan}
	sources, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "tracked-manifest", path: target}}, map[string][]byte{"tracked-manifest": original})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUpdateBackups(request, sources); err != nil {
		t.Fatal(err)
	}
	journal, err := newUpdateJournal(request)
	if err != nil {
		t.Fatal(err)
	}
	journal.Backups = backupMetadata(sources)
	journal.Progress = []UpdateJournalEffect{{Sequence: 1, Name: "repository-root-fast-forward", Repository: "root", Receipt: updateRecoveryFastForwardReceipt(t, request, "root", driftOID('2')), State: "completed"}}
	path, err := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
	if err != nil || writeNewUpdateJournal(NewUpdateExecutor(), path, journal) != nil {
		t.Fatalf("write recovery journal path=%q err=%v", path, err)
	}
	if err := os.WriteFile(target, candidate, 0o640); err != nil {
		t.Fatal(err)
	}
	git := &updateExecutionGit{}
	if err := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: git}).Recover(context.Background(), request); err != nil {
		t.Fatalf("Recover() = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("exact tracked-manifest restore=%q err=%v", got, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("recovered journal remains: %v", err)
	}
	backupDirectory, err := updateBackupDirectory(request)
	if err != nil || !os.IsNotExist(lstatError(backupDirectory)) {
		t.Fatalf("recovered backup directory remains path=%q err=%v", backupDirectory, err)
	}
	wantRecoveryInverse := []string{"restore:main:" + driftOID('2'), "restore-fetch:origin:refs/heads/main:" + driftOID('2')}
	if !reflect.DeepEqual(git.calls, wantRecoveryInverse) {
		t.Fatalf("recovery Git inverse=%#v", git.calls)
	}
	if err := NewUpdateExecutorWith(UpdateExecutorDependencies{Git: git}).Recover(context.Background(), request); err != nil {
		t.Fatalf("repeat clean recovery = %v", err)
	}
	if !reflect.DeepEqual(git.calls, wantRecoveryInverse) {
		t.Fatalf("repeat recovery ran an inverse=%#v", git.calls)
	}
}

func TestUpdateExecutRecoveryRetainsTamperedOrConcurrentEvidence(t *testing.T) {
	root := t.TempDir()
	original := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	candidate := append(append([]byte(nil), original...), []byte("# selected execution generation\n")...)
	target := filepath.Join(root, "project.wtree.yml")
	if err := os.WriteFile(target, original, 0o640); err != nil {
		t.Fatal(err)
	}
	plan := updateRecoveryPlan(t, target, original, candidate)
	request := UpdateExecutionRequest{DataDir: filepath.Join(root, "data"), ProjectID: "project", OperationID: "operation-retain", Plan: plan}
	sources, err := prepareUpdateBackupSources([]updateBackupSource{{kind: "tracked-manifest", path: target}}, map[string][]byte{"tracked-manifest": original})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUpdateBackups(request, sources); err != nil {
		t.Fatal(err)
	}
	journal, err := newUpdateJournal(request)
	if err != nil {
		t.Fatal(err)
	}
	journal.Backups = backupMetadata(sources)
	journal.Progress = []UpdateJournalEffect{{Sequence: 1, Name: "repository-root-fast-forward", Repository: "root", Receipt: updateRecoveryFastForwardReceipt(t, request, "root", driftOID('2')), State: "completed"}}
	path, _ := UpdateJournalPath(request.DataDir, request.ProjectID, request.OperationID)
	if err := writeNewUpdateJournal(NewUpdateExecutor(), path, journal); err != nil {
		t.Fatal(err)
	}
	backupDirectory, _ := updateBackupDirectory(request)
	if err := os.WriteFile(filepath.Join(backupDirectory, "tracked-manifest.bin"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = NewUpdateExecutorWith(UpdateExecutorDependencies{Git: &updateExecutionGit{}}).Recover(context.Background(), request)
	var application *Error
	if !errors.As(err, &application) || application.Kind != ErrorRollbackIncomplete {
		t.Fatalf("tampered recovery error=%v", err)
	}
	retained, readErr := os.ReadFile(path)
	if readErr != nil || bytes.Contains(retained, original) || bytes.Contains(retained, []byte("tampered")) {
		t.Fatalf("recovery retained evidence=%q err=%v", retained, readErr)
	}
	if _, err := decodeStrictUpdateJournal(retained); err != nil {
		t.Fatalf("retained journal is not strict: %v", err)
	}
}

func updateRecoveryPlan(t *testing.T, target string, original, candidate []byte) UpdatePlan {
	t.Helper()
	plan := updateExecutorPlan(t)
	plan.Source.SHA256 = sha256String(candidate)
	plan.Generations.CurrentManifestSHA256 = sha256String(original)
	plan.Generations.CandidateManifestSHA256 = sha256String(candidate)
	plan.private.candidateData = append([]byte(nil), candidate...)
	plan.private.baseline.current.Path = target
	plan.private.baseline.current.Bytes = append([]byte(nil), original...)
	plan.private.baseline.candidate = append([]byte(nil), candidate...)
	plan.private.factsDigest = updatePlanFactsDigest(plan)
	if err := plan.Validate(); err != nil {
		t.Fatalf("recovery plan: %v", err)
	}
	return plan
}

func updateRecoveryFastForwardReceipt(t *testing.T, request UpdateExecutionRequest, repositoryID, newCommit string) string {
	t.Helper()
	for _, observation := range request.Plan.executionBaseline().observations {
		if observation.RepositoryID != repositoryID {
			continue
		}
		receipt, err := encodeUpdateFastForwardReceipt(updateFastForwardReceipt{
			Version:            UpdateJournalVersion,
			OperationID:        request.OperationID,
			ProjectID:          request.ProjectID,
			RepositoryID:       repositoryID,
			Branch:             observation.Branch,
			OldCommit:          observation.Head,
			NewCommit:          newCommit,
			Remote:             observation.Upstream.Remote,
			RemoteRef:          observation.Upstream.Merge,
			ActualRemoteCommit: newCommit,
		})
		if err != nil {
			t.Fatalf("encode recovery fast-forward receipt: %v", err)
		}
		return receipt
	}
	t.Fatalf("locked recovery observation %q not found", repositoryID)
	return ""
}

func lstatError(path string) error {
	_, err := os.Lstat(path)
	return err
}

func updateExecutorPlan(t *testing.T) UpdatePlan {
	t.Helper()
	manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
	project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
	snapshot, err := driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{updateExecutorObservation()}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildUpdatePlan(snapshot, LoadedManifestSource{Kind: ManifestSourceLocal, Source: driftFixturePath("/candidate/project.wtree.yml"), data: manifest})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func updateExecutorRecapture(t *testing.T) func(context.Context, UpdatePlan) (DriftSnapshot, error) {
	t.Helper()
	return func(context.Context, UpdatePlan) (DriftSnapshot, error) {
		manifest := driftManifest(t, map[string]config.PortableRepository{"root": driftRepository("", ".")})
		project := driftProject([]domain.Repository{{ID: "root", DefaultMount: ".", DefaultBranch: "main", CommonGitDir: "/git/root", SourcePath: "/tree"}})
		return driftBuild(t, DriftSnapshotInput{Project: project, DefaultWorkspace: driftWorkspace(project), CurrentManifest: manifest, CandidateManifest: manifest, Observations: []DriftRepositoryObservation{updateExecutorObservation()}})
	}
}

func updateExecutorObservation() DriftRepositoryObservation {
	return DriftRepositoryObservation{RepositoryID: "root", Path: "/tree", CommonGitDir: "/git/root", Branch: "main", Head: driftOID('0'), Clean: true, AdvertisedCommit: driftOID('1'), CanFastForward: true, TrackedManifestExact: true, UpstreamKnown: true, Upstream: gitadapter.Upstream{LocalBranch: "main", Remote: "origin", Merge: "refs/heads/main", FetchURL: "https://example.test/project.git"}}
}
func mustUpdateExecutorSnapshot(t *testing.T) DriftSnapshot {
	t.Helper()
	snapshot, err := updateExecutorRecapture(t)(context.Background(), UpdatePlan{})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func executeUpdateForTest(ctx context.Context, executor *UpdateExecutor, request UpdateExecutionRequest, recapture func(context.Context, UpdatePlan) (DriftSnapshot, error), effects []updateEffect) (UpdateExecutionResult, error) {
	return executor.executeForTest(ctx, request, updateExecutionTestSeams{recapture: recapture, effects: effects})
}

type updateExecutionGit struct {
	gitadapter.Git
	calls   []string
	tracked []byte
}

type updateExecutionFetchReturnErrorGit struct {
	gitadapter.Git
	after          func()
	returnErr      error
	discardReceipt bool
}

func (git *updateExecutionFetchReturnErrorGit) FetchConfiguredRef(ctx context.Context, repository, remote, remoteRef string) (gitadapter.ConfiguredRefFetch, error) {
	receipt, err := git.Git.FetchConfiguredRef(ctx, repository, remote, remoteRef)
	if err != nil {
		return receipt, err
	}
	if git.after != nil {
		git.after()
	}
	if git.returnErr != nil {
		if git.discardReceipt {
			receipt = gitadapter.ConfiguredRefFetch{}
		}
		return receipt, git.returnErr
	}
	return receipt, ctx.Err()
}

func (git *updateExecutionGit) ObserveConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefObservation, error) {
	git.calls = append(git.calls, "observe:origin:refs/heads/main")
	return gitadapter.ConfiguredRefObservation{Remote: "origin", RemoteRef: "refs/heads/main", Commit: driftOID('2')}, nil
}
func (git *updateExecutionGit) FetchConfiguredRef(context.Context, string, string, string) (gitadapter.ConfiguredRefFetch, error) {
	git.calls = append(git.calls, "fetch:origin:refs/heads/main")
	return gitadapter.ConfiguredRefFetch{Remote: "origin", RemoteRef: "refs/heads/main", ActualRemoteCommit: driftOID('2')}, nil
}
func (git *updateExecutionGit) FastForward(_ context.Context, _ string, branch, old, next string) (gitadapter.FastForwardReceipt, error) {
	git.calls = append(git.calls, "ff:"+branch+":"+old+":"+next)
	return gitadapter.FastForwardReceipt{Branch: branch, OldCommit: old, NewCommit: next}, nil
}
func (git *updateExecutionGit) RestoreFastForward(_ context.Context, _ string, receipt gitadapter.FastForwardReceipt) error {
	git.calls = append(git.calls, "restore:"+receipt.Branch+":"+receipt.NewCommit)
	return nil
}
func (git *updateExecutionGit) RestoreConfiguredRef(_ context.Context, _ string, receipt gitadapter.ConfiguredRefFetch) error {
	git.calls = append(git.calls, "restore-fetch:"+receipt.Remote+":"+receipt.RemoteRef+":"+receipt.ActualRemoteCommit)
	return nil
}

func (git *updateExecutionGit) Clone(_ context.Context, _ string, destination, remote string) error {
	git.calls = append(git.calls, "clone:"+remote)
	return os.MkdirAll(destination, 0o700)
}
func (git *updateExecutionGit) FetchTrackingBranch(_ context.Context, _ string, remote, merge string) error {
	git.calls = append(git.calls, "fetch:"+remote+":"+merge)
	return nil
}
func (git *updateExecutionGit) CheckoutTrackingBranch(_ context.Context, _ string, branch, _, _ string) (string, error) {
	git.calls = append(git.calls, "checkout:"+branch)
	return driftOID('2'), nil
}
func (git *updateExecutionGit) TopLevel(_ context.Context, path string) (string, error) {
	return path, nil
}
func (git *updateExecutionGit) CurrentBranch(context.Context, string) (string, bool, error) {
	return "main", false, nil
}
func (git *updateExecutionGit) Head(context.Context, string) (string, error) {
	return driftOID('2'), nil
}
func (git *updateExecutionGit) Upstream(context.Context, string) (gitadapter.Upstream, error) {
	return gitadapter.Upstream{LocalBranch: "main", Remote: "origin", Merge: "refs/heads/main", FetchURL: "https://example.test/project.git"}, nil
}
func (git *updateExecutionGit) ContainsCommits(context.Context, string, []string) (bool, error) {
	return true, nil
}
func (git *updateExecutionGit) IsClean(context.Context, string) (bool, error) { return true, nil }
func (git *updateExecutionGit) HasSubmodules(context.Context, string) (bool, error) {
	return false, nil
}
func (git *updateExecutionGit) CommonGitDir(_ context.Context, path string) (string, error) {
	return filepath.Join(path, ".git"), nil
}
func (git *updateExecutionGit) TrackedFile(context.Context, string, string, string) ([]byte, error) {
	return append([]byte(nil), git.tracked...), nil
}
