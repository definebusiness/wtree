package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicModePreservesModeAndPreReplacementFailures(t *testing.T) {
	for _, step := range []string{"create-temp", "write", "sync", "close", "before-rename"} {
		t.Run(step, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := WriteFileAtomicModeWithHook(path, []byte("new"), 0o600, func(got string) error {
				if got == step {
					return errors.New("injected")
				}
				return nil
			}); err == nil {
				t.Fatal("write succeeded")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "old" {
				t.Fatalf("replacement changed old data: %q, %v", data, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "target")
	if err := WriteFileAtomicMode(path, []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	assertAtomicRequestedMode(t, info.Mode(), 0o640)
}

func TestWriteFailureHookRunsBeforeWritingTemporary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	err := WriteFileAtomicModeWithHook(path, []byte("new"), 0o600, func(step string) error {
		if step != "write" {
			return nil
		}
		entries, readErr := os.ReadDir(filepath.Dir(path))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if entry.Name() == filepath.Base(path) {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), entry.Name()))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(data) != 0 {
				t.Fatalf("temporary data before write = %q, want empty", data)
			}
			return errors.New("injected write failure")
		}
		t.Fatal("write hook was not called")
		return nil
	})
	if err == nil {
		t.Fatal("write succeeded")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("target after injected write failure = %q, %v; want old generation", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("entries after injected write failure = %v; want target only", entries)
	}
}

func TestWriteFileAtomicModeCleansTemporaryAndLeavesCompleteGenerationOnLateFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	err := WriteFileAtomicModeWithHook(path, []byte("new"), 0o600, func(step string) error {
		if step == "dir-sync" {
			return errors.New("injected directory sync failure")
		}
		return nil
	})
	if err == nil {
		t.Fatal("write succeeded")
	}
	if !ReplacementCompleted(err) {
		t.Fatalf("ReplacementCompleted(%v) = false, want true", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("late failure generation = %q, %v; want complete new generation", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) {
			t.Fatalf("temporary file was retained: %s", entry.Name())
		}
	}
}

func TestAtomicFailureMatrixLeavesCompleteTargetAndCleansTemporary(t *testing.T) {
	for _, existing := range []bool{true, false} {
		for _, step := range []string{"create-temp", "write", "sync", "close", "before-rename", "replace", "dir-sync"} {
			t.Run(map[bool]string{true: "existing", false: "missing"}[existing]+"/"+step, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "target")
				if existing {
					if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
						t.Fatal(err)
					}
				}
				hook := func(got string) error {
					if got == step {
						return errors.New("injected " + step + " failure")
					}
					return nil
				}
				replace := os.Rename
				if step == "replace" {
					replace = func(string, string) error { return errors.New("injected replacement failure") }
				}
				var err error
				if existing {
					err = writeFileAtomicMode(path, []byte("new"), 0o600, hook, replace)
				} else {
					err = writeFileAtomicModeCreate(path, []byte("new"), 0o644, hook, replace)
				}
				if err == nil {
					t.Fatal("write succeeded")
				}
				if got, want := ReplacementCompleted(err), step == "dir-sync"; got != want {
					t.Fatalf("ReplacementCompleted(%v) = %t, want %t", err, got, want)
				}
				data, readErr := os.ReadFile(path)
				if step == "dir-sync" {
					if readErr != nil || string(data) != "new" {
						t.Fatalf("late failure target = %q, %v; want complete new", data, readErr)
					}
					info, statErr := os.Stat(path)
					if statErr != nil {
						t.Fatal(statErr)
					}
					if existing {
						assertAtomicRequestedMode(t, info.Mode(), 0o600)
					}
					if !existing && info.Mode().Perm()&0o111 != 0 {
						t.Fatalf("created mode = %o, want non-executable", info.Mode().Perm())
					}
				} else if existing {
					if readErr != nil || string(data) != "old" {
						t.Fatalf("pre-replacement target = %q, %v; want complete old", data, readErr)
					}
				} else if !os.IsNotExist(readErr) {
					t.Fatalf("missing target created after %s failure: %q, %v", step, data, readErr)
				}
				entries, readDirErr := os.ReadDir(filepath.Dir(path))
				if readDirErr != nil {
					t.Fatal(readDirErr)
				}
				for _, entry := range entries {
					if entry.Name() != filepath.Base(path) {
						t.Fatalf("temporary file was retained after %s failure: %s", step, entry.Name())
					}
				}
			})
		}
	}
}

func TestRenameNoReplaceMovesDirectoryWithoutClobbering(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(source, target); err != nil {
		t.Fatalf("RenameNoReplace absent target: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("renamed target: %v", err)
	}
	second := filepath.Join(root, "second")
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(second, target); err == nil {
		t.Fatal("RenameNoReplace existing target succeeded")
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("source was lost after rejected rename: %v", err)
	}
}

func TestWriteFileAtomicCreateModeNoReplacePreservesFinalBoundaryCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	err := WriteFileAtomicCreateModeNoReplaceWithOwnedTempHook(path, []byte("owned\n"), 0o600, nil, func(temporary string, info os.FileInfo) error {
		current, err := os.Lstat(temporary)
		if err != nil || !os.SameFile(info, current) {
			t.Fatalf("owned temporary was not retained at final callback: %v", err)
		}
		return os.WriteFile(path, []byte("foreign\n"), 0o600)
	})
	if err == nil {
		t.Fatal("absent-only creation succeeded after target creation")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "foreign\n" {
		t.Fatalf("final-boundary target = %q, %v; want preserved foreign generation", data, readErr)
	}
}
