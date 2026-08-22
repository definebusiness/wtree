package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/testutil"
)

func TestDoctorFixClassifiesPostReplacementWorkspacePublication(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "restores-owned-generation"
		if concurrent {
			name = "preserves-concurrent-generation-with-recovery"
		}
		t.Run(name, func(t *testing.T) {
			project, data := forestWorkspaceProject(t)
			statePath := WorkspaceStatePath(data, project.ID, "default")
			workspace, original := staleDoctorWorkspace(t, project, statePath)
			concurrentState := doctorWorkspaceState(workspace)
			concurrentState.Partial = true
			concurrentState.MissingRepositoryIDs = []string{"concurrent"}
			concurrentBytes, err := store.WorkspaceBytes(concurrentState)
			if err != nil {
				t.Fatal(err)
			}

			doctor := NewDoctorService()
			doctor.writeWorkspaceCAS = func(path string, state store.WorkspaceState, compare func() error) error {
				data, err := store.WorkspaceBytes(state)
				if err != nil {
					return err
				}
				return fsutil.WriteFileAtomicModeWithHook(path, data, 0o600, func(step string) error {
					if step == "before-rename" && compare != nil {
						return compare()
					}
					if step == "dir-sync" {
						if concurrent {
							if err := os.WriteFile(path, concurrentBytes, 0o600); err != nil {
								return err
							}
						}
						return errors.New("injected post-replacement workspace failure")
					}
					return nil
				})
			}
			report, err := doctor.Fix(context.Background(), project, workspace, DoctorFixRequest{DataDir: data})
			if report.Fixed || err == nil {
				t.Fatalf("Doctor Fix report=%#v error=%v", report, err)
			}
			got, readErr := os.ReadFile(statePath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", workspace.ID+".json")
			if concurrent {
				if HasCleanRollback(err) || !hasServiceErrorKind(err, ErrorRollbackIncomplete) || !bytes.Equal(got, concurrentBytes) {
					t.Fatalf("concurrent Doctor Fix error=%v state=%q", err, got)
				}
				recovery, recoveryErr := store.ReadRecovery(recoveryPath)
				if recoveryErr != nil || recovery.Operation != "doctor-fix" || recovery.FailedStep != "commit-state" {
					t.Fatalf("Doctor recovery=%#v error=%v", recovery, recoveryErr)
				}
			} else {
				if !HasCleanRollback(err) || !bytes.Equal(got, original) {
					t.Fatalf("owned Doctor Fix error=%v state=%q want=%q", err, got, original)
				}
				if _, statErr := os.Lstat(recoveryPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("clean Doctor rollback wrote recovery: %v", statErr)
				}
			}
		})
	}
}

func TestResolverReconcileClassifiesPostReplacementRegistryPublication(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "restores-owned-generation"
		if concurrent {
			name = "preserves-concurrent-generation-with-recovery"
		}
		t.Run(name, func(t *testing.T) {
			source := testutil.NewPushedGitRepository(t)
			source.CommitFile("readme", "root\n", "initial")
			data := t.TempDir()
			initialized, err := NewInitializer().Init(context.Background(), InitRequest{Path: source.Path, DataDir: data})
			if err != nil {
				t.Fatal(err)
			}
			registryPath := filepath.Join(data, "registry.json")
			original, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(t.TempDir(), "moved")
			if err := os.Rename(source.Path, moved); err != nil {
				t.Fatal(err)
			}
			resolver := NewResolver()
			project, err := resolver.loadProject(context.Background(), filepath.Join(moved, ".wtree.yml"))
			if err != nil {
				t.Fatal(err)
			}
			concurrentRegistry, err := store.ReadRegistry(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			concurrentRegistry.Projects["concurrent"] = store.RegistryProject{Name: "concurrent", ConfigPath: "/concurrent", RepositoryIDs: map[string]string{"identity": "root"}}
			concurrentBytes, err := store.RegistryBytes(concurrentRegistry)
			if err != nil {
				t.Fatal(err)
			}
			resolver.writeRegistryCAS = func(path string, registry store.Registry, compare func() error) error {
				published, err := store.RegistryBytes(registry)
				if err != nil {
					return err
				}
				return fsutil.WriteFileAtomicModeWithHook(path, published, 0o600, func(step string) error {
					if step == "before-rename" && compare != nil {
						return compare()
					}
					if step == "dir-sync" {
						if concurrent {
							if err := os.WriteFile(path, concurrentBytes, 0o600); err != nil {
								return err
							}
						}
						return errors.New("injected post-replacement registry failure")
					}
					return nil
				})
			}
			err = resolver.ReconcileProject(context.Background(), data, project)
			if err == nil {
				t.Fatal("ReconcileProject succeeded after post-replacement failure")
			}
			got, readErr := os.ReadFile(registryPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			recoveryPath := filepath.Join(data, "projects", initialized.ProjectID, "recovery", "reconcile-project.json")
			if concurrent {
				if HasCleanRollback(err) || !hasServiceErrorKind(err, ErrorRollbackIncomplete) || !bytes.Equal(got, concurrentBytes) {
					t.Fatalf("concurrent reconciliation error=%v registry=%q", err, got)
				}
				recovery, recoveryErr := store.ReadRecovery(recoveryPath)
				if recoveryErr != nil || recovery.Operation != "reconcile-project" || recovery.FailedStep != "commit-registry" {
					t.Fatalf("reconciliation recovery=%#v error=%v", recovery, recoveryErr)
				}
			} else {
				if !HasCleanRollback(err) || !bytes.Equal(got, original) {
					t.Fatalf("owned reconciliation error=%v registry=%q want=%q", err, got, original)
				}
				if _, statErr := os.Lstat(recoveryPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("clean reconciliation wrote recovery: %v", statErr)
				}
			}
		})
	}
}

func staleDoctorWorkspace(t *testing.T, project domain.Project, statePath string) (domain.Workspace, []byte) {
	t.Helper()
	state, err := store.ReadWorkspace(statePath)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]string, len(project.Repositories))
	for _, repository := range project.ParentFirst() {
		checkout := state.Repositories[repository.ID]
		owner := state.Path
		if repository.ParentID != "" {
			owner = paths[repository.ParentID]
		}
		checkout.Mount = "stale-" + repository.ID
		checkout.ResolvedPath = filepath.Join(owner, checkout.Mount)
		state.Repositories[repository.ID] = checkout
		paths[repository.ID] = checkout.ResolvedPath
	}
	if err := store.WriteWorkspace(statePath, state); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceFromState(state)
	if err != nil {
		t.Fatal(err)
	}
	return workspace, original
}

func hasServiceErrorKind(err error, kind ErrorKind) bool {
	var application *Error
	return errors.As(err, &application) && application.Kind == kind
}
