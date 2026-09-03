//go:build windows

package fsutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivatePathReadFinalValidationNotFoundIsNotAbsence(t *testing.T) {
	authority, err := OpenPrivatePath(t.TempDir(), []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if err := authority.WriteFileAtomicModeWithHook([]byte("record"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	original := validatePrivateDirectoryAfterRead
	// Some Windows filesystems refuse to rename an open directory tree even
	// when every retained handle shares deletion. Inject the exact native
	// status produced by that namespace disappearance at the post-read check.
	validatePrivateDirectoryAfterRead = func(*privatePath) error { return windows.STATUS_OBJECT_NAME_NOT_FOUND }
	defer func() { validatePrivateDirectoryAfterRead = original }()
	if _, err := authority.ReadFile(); err == nil || PrivatePathNotExist(err) {
		t.Fatalf("post-read directory disappearance = %v, want non-absence authority error", err)
	}
}

func TestWindowsPrivatePathAcquisitionIdentityNotFoundIsNotAbsence(t *testing.T) {
	anchor := t.TempDir()
	projects, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.Close(); err != nil {
		t.Fatal(err)
	}
	original := reopenPrivateWindowsIdentity
	identityCheck := false
	privateWindowsBeforeIdentityReopen = func(name string) {
		if name == "projects" {
			identityCheck = true
		}
	}
	// The first component open has succeeded; only its identity reopen reports
	// the native status produced when the authoritative name disappears.
	reopenPrivateWindowsIdentity = func(parent windows.Handle, name string, access windows.ACCESS_MASK, share, disposition, options uint32, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
		if identityCheck && name == "projects" {
			return windows.InvalidHandle, windows.STATUS_OBJECT_NAME_NOT_FOUND
		}
		return original(parent, name, access, share, disposition, options, descriptor)
	}
	defer func() {
		privateWindowsBeforeIdentityReopen = nil
		reopenPrivateWindowsIdentity = original
	}()
	if authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil || PrivatePathNotExist(err) || os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		if authority != nil {
			_ = authority.Close()
		}
		t.Fatalf("acquisition identity disappearance = %v, want non-absence authority error", err)
	}
}

func TestWindowsPrivatePathInitialComponentMissAfterDetachedParentIsNotAbsence(t *testing.T) {
	anchor := t.TempDir()
	moved := anchor + "-detached"
	var detachErr error
	privateWindowsBeforeDirectoryOpen = func(name string) {
		if name == "projects" {
			detachErr = os.Rename(anchor, moved)
		}
	}
	defer func() { privateWindowsBeforeDirectoryOpen = nil }()
	if authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); detachErr != nil {
		t.Skipf("directory replacement fixture unavailable: %v", detachErr)
	} else if err == nil || PrivatePathNotExist(err) || os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		if authority != nil {
			_ = authority.Close()
		}
		t.Fatalf("detached parent initial component miss = %v, want non-absence authority error", err)
	} else if !errors.Is(err, errPrivateDirectoryAuthority) {
		t.Fatalf("detached parent initial component miss = %v, want retained authority marker", err)
	}
}

func TestWindowsPrivatePathInitialComponentMissWithInvalidPartialChainIsNotAbsence(t *testing.T) {
	original := validatePrivatePartialDirectory
	validatePrivatePartialDirectory = func(*privatePath) error { return windows.STATUS_OBJECT_NAME_NOT_FOUND }
	defer func() { validatePrivatePartialDirectory = original }()
	if authority, err := OpenPrivatePath(t.TempDir(), []string{"projects"}, "event.json", false); err == nil || PrivatePathNotExist(err) || os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		if authority != nil {
			_ = authority.Close()
		}
		t.Fatalf("invalid partial-chain initial component miss = %v, want non-absence authority error", err)
	} else if !errors.Is(err, errPrivateDirectoryAuthority) {
		t.Fatalf("invalid partial-chain initial component miss = %v, want retained authority marker", err)
	}
}

func TestWindowsPrivatePathProtectsOwnedDescendantsAndReplacement(t *testing.T) {
	anchor := t.TempDir()
	setPrivateWindowsTestDACL(t, anchor, "D:P(A;OICI;FA;;;WD)", true)
	authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.WriteFileAtomicModeWithHook([]byte("first"), 0o600, nil); err != nil {
		authority.Close()
		t.Fatal(err)
	}
	if err := authority.WriteFileAtomicModeWithHook([]byte("replacement"), 0o600, nil); err != nil {
		authority.Close()
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	user, err := privateWindowsEffectiveUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(anchor, "projects"),
		filepath.Join(anchor, "projects", "project"),
		filepath.Join(anchor, "projects", "project", "hooks"),
		filepath.Join(anchor, "projects", "project", "hooks", "workspace"),
	} {
		handle := openPrivateWindowsTestHandle(t, path, true)
		if err := validatePrivateWindowsHandle(handle, user, true); err != nil {
			windows.CloseHandle(handle)
			t.Fatalf("private directory %q: %v", path, err)
		}
		windows.CloseHandle(handle)
	}
	leaf := filepath.Join(anchor, "projects", "project", "hooks", "workspace", "event.json")
	handle := openPrivateWindowsTestHandle(t, leaf, false)
	defer windows.CloseHandle(handle)
	if err := validatePrivateWindowsHandle(handle, user, false); err != nil {
		t.Fatalf("private replacement leaf: %v", err)
	}
}

func TestWindowsPrivatePathReleasesPublicationAuthorityBeforeDirSync(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	leaf := filepath.Join(anchor, "projects", "event.json")
	completed := errors.New("dir sync observed concurrent record")
	err = authority.WriteFileAtomicModeWithHook([]byte("published record"), 0o600, func(step string) error {
		if step != "dir-sync" {
			return nil
		}
		if err := os.WriteFile(leaf, []byte("concurrent record"), 0o600); err != nil {
			t.Fatalf("concurrent private record write during dir-sync: %v", err)
		}
		return completed
	})
	if err == nil || !ReplacementCompleted(err) || !errors.Is(err, completed) {
		t.Fatalf("error=%v completed=%t", err, ReplacementCompleted(err))
	}
	if err := authority.WriteFileAtomicModeWithHook([]byte("next record"), 0o600, nil); err != nil {
		t.Fatalf("subsequent private transition: %v", err)
	}
	data, err := authority.ReadFile()
	if err != nil || string(data) != "next record" {
		t.Fatalf("next private record=%q error=%v", data, err)
	}
}

func TestWindowsPrivateRecordTransitionsUnderRetainedHookLock(t *testing.T) {
	anchor := t.TempDir()
	components := []string{"projects", "project", "hooks", "workspace"}
	lockPath, err := OpenPrivatePath(anchor, components, "post-create.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := lockPath.TryLock(context.Background())
	if err != nil {
		lockPath.Close()
		t.Fatal(err)
	}
	defer lease.Unlock()
	record, err := OpenPrivatePath(anchor, components, "post-create.json", true)
	if err != nil {
		t.Fatal(err)
	}
	defer record.Close()
	for _, state := range []string{"running", "succeeded", "finalizing"} {
		data := []byte(`{"state":"` + state + `"}` + "\n")
		if err := record.WriteFileAtomicModeWithHook(data, 0o600, nil); err != nil {
			t.Fatalf("write %s record: %v", state, err)
		}
		got, err := record.ReadFile()
		if err != nil || string(got) != string(data) {
			t.Fatalf("read %s record = %q, %v", state, got, err)
		}
	}
}

func TestWindowsPrivatePathRejectsUnsafeDirectoryAndLeafSecurityWithoutMutation(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.WriteFileAtomicModeWithHook([]byte("record"), 0o600, nil); err != nil {
		authority.Close()
		t.Fatal(err)
	}
	authority.Close()
	user, err := privateWindowsEffectiveUser()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(anchor, "projects")
	unsafeDirectory := "O:" + user + "D:P(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;WD)"
	setPrivateWindowsTestDACL(t, directory, unsafeDirectory, true)
	before := privateWindowsTestSecurity(t, directory)
	if opened, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil {
		opened.Close()
		t.Fatal("accepted an extra directory trustee")
	}
	if after := privateWindowsTestSecurity(t, directory); after != before {
		t.Fatalf("read-only validation mutated directory security: before=%q after=%q", before, after)
	}
	setPrivateWindowsTestDACL(t, directory, "O:"+user+"D:P(A;OICI;FA;;;"+user+")", true)
	leaf := filepath.Join(directory, "event.json")
	setPrivateWindowsTestDACL(t, leaf, "O:"+user+"D:P(A;;GR;;;"+user+")", true)
	before = privateWindowsTestSecurity(t, leaf)
	if opened, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil {
		opened.Close()
		t.Fatal("accepted a non-full-control leaf ACE")
	}
	if after := privateWindowsTestSecurity(t, leaf); after != before {
		t.Fatalf("read-only validation mutated leaf security: before=%q after=%q", before, after)
	}
}

func TestWindowsPrivatePathRejectsUnprotectedAndNullDACL(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	authority.Close()
	user, err := privateWindowsEffectiveUser()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(anchor, "projects")
	setPrivateWindowsTestDACL(t, directory, "O:"+user+"D:(A;OICI;FA;;;"+user+")", false)
	if opened, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil {
		opened.Close()
		t.Fatal("accepted an unprotected directory DACL")
	}
	if err := windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
		t.Skipf("setting a null test DACL is unavailable: %v", err)
	}
	if opened, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil {
		opened.Close()
		t.Fatal("accepted a null directory DACL")
	}
}

func TestWindowsPrivatePathRejectsDirectoryReparse(t *testing.T) {
	anchor := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(anchor, "projects")); err != nil {
		t.Skipf("directory symlinks/junctions unavailable: %v", err)
	}
	if _, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true); err == nil {
		t.Fatal("accepted a directory reparse point")
	}
}

func TestWindowsPrivatePathRejectsTypeSwaps(t *testing.T) {
	anchor := t.TempDir()
	if err := os.WriteFile(filepath.Join(anchor, "projects"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true); err == nil {
		t.Fatal("accepted a file in an owned directory position")
	}
	leafAnchor := t.TempDir()
	authority, err := OpenPrivatePath(leafAnchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	authority.Close()
	if err := os.Mkdir(filepath.Join(leafAnchor, "projects", "event.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivatePath(leafAnchor, []string{"projects"}, "event.json", false); err == nil {
		t.Fatal("accepted a directory in a private leaf position")
	}
}

func TestWindowsPrivatePathRejectsLeafReparse(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	authority.Close()
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(anchor, "projects", "event.json")); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	if _, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil {
		t.Fatal("accepted a leaf reparse point")
	}
}

func TestWindowsPrivatePathRejectsLeafIdentityReplacementAtValidationBoundary(t *testing.T) {
	anchor := t.TempDir()
	components := []string{"projects"}
	for _, leaf := range []string{"event.json", "replacement.json"} {
		authority, err := OpenPrivatePath(anchor, components, leaf, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := authority.WriteFileAtomicModeWithHook([]byte(leaf), 0o600, nil); err != nil {
			authority.Close()
			t.Fatal(err)
		}
		authority.Close()
	}
	directory := filepath.Join(anchor, "projects")
	privateWindowsBeforeIdentityReopen = func(name string) {
		if name != "event.json" {
			return
		}
		privateWindowsBeforeIdentityReopen = nil
		if err := os.Rename(filepath.Join(directory, name), filepath.Join(directory, "old.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(directory, "replacement.json"), filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { privateWindowsBeforeIdentityReopen = nil }()
	if authority, err := OpenPrivatePath(anchor, components, "event.json", false); err == nil {
		authority.Close()
		t.Fatal("accepted a leaf replaced between retained-handle validation and name reopening")
	}
}

func TestWindowsPrivatePathRejectsDirectoryIdentityReplacementAtValidationBoundary(t *testing.T) {
	anchor := t.TempDir()
	for _, component := range []string{"projects", "replacement"} {
		authority, err := OpenPrivatePath(anchor, []string{component}, "event.json", true)
		if err != nil {
			t.Fatal(err)
		}
		authority.Close()
	}
	privateWindowsBeforeIdentityReopen = func(name string) {
		if name != "projects" {
			return
		}
		privateWindowsBeforeIdentityReopen = nil
		if err := os.Rename(filepath.Join(anchor, name), filepath.Join(anchor, "old")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(anchor, "replacement"), filepath.Join(anchor, name)); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { privateWindowsBeforeIdentityReopen = nil }()
	if authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", false); err == nil {
		authority.Close()
		t.Fatal("accepted a directory replaced between retained-handle validation and name reopening")
	}
}

func TestWindowsPrivateDirectoryEnumerationRejectsRetainedGenerationReplacement(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	replacement, err := OpenPrivatePath(anchor, []string{"replacement"}, "event.json", true)
	if err != nil {
		t.Fatal(err)
	}
	replacement.Close()
	renamePrivateWindowsTestDirectory(t, filepath.Join(anchor, "projects"), filepath.Join(anchor, "old"))
	if err := os.Rename(filepath.Join(anchor, "replacement"), filepath.Join(anchor, "projects")); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ReadDirectory(); err == nil {
		t.Fatal("enumeration accepted a replaced private directory generation")
	}
}

func TestWindowsPrivateSecurityDescriptorRejectsOwnerDACLAndACEVariants(t *testing.T) {
	user, err := privateWindowsEffectiveUser()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		sddl string
	}{
		{name: "wrong-owner", sddl: "O:WDD:P(A;;FA;;;" + user + ")"},
		{name: "absent-dacl", sddl: "O:" + user},
		{name: "null-dacl", sddl: "O:" + user + "D:NO_ACCESS_CONTROL"},
		{name: "wrong-trustee", sddl: "O:" + user + "D:P(A;;FA;;;WD)"},
		{name: "extra-ace", sddl: "O:" + user + "D:P(A;;FA;;;" + user + ")(A;;FA;;;WD)"},
		{name: "non-full-control", sddl: "O:" + user + "D:P(A;;GR;;;" + user + ")"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePrivateWindowsSecurityDescriptor(descriptor, user, false); err == nil {
				t.Fatal("accepted unsafe security descriptor")
			}
		})
	}
	t.Run("defaulted-dacl", func(t *testing.T) {
		descriptor, err := windows.SecurityDescriptorFromString("O:" + user + "D:P(A;;FA;;;" + user + ")")
		if err != nil {
			t.Fatal(err)
		}
		if err := descriptor.SetControl(windows.SE_DACL_DEFAULTED, windows.SE_DACL_DEFAULTED); err != nil {
			// SecurityDescriptorFromString produces a self-relative descriptor.
			// Some supported Windows versions reject changing this control bit on
			// such an in-memory descriptor, before it can reach the validator.
			// That is a fixture capability limitation, not an accepted DACL.
			if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
				t.Skipf("cannot construct defaulted-DACL fixture: %v", err)
			}
			t.Fatal(err)
		}
		if err := validatePrivateWindowsSecurityDescriptor(descriptor, user, false); err == nil {
			t.Fatal("accepted a defaulted DACL")
		}
	})
}

func TestWindowsPrivateLockPreventsLeafReplacementDuringLease(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects"}, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := authority.TryLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(anchor, "projects", "event.lock")
	if err := os.Rename(path, path+".replacement"); err == nil {
		lock.Unlock()
		t.Fatal("active private lock lease allowed leaf replacement")
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPrivatePathRejectsIntermediateAncestorReplacementAcrossOperations(t *testing.T) {
	operations := []struct {
		name string
		run  func(*PrivatePath) error
	}{
		{"read", func(path *PrivatePath) error { _, err := path.ReadFile(); return err }},
		{"write", func(path *PrivatePath) error { return path.WriteFileAtomicModeWithHook([]byte("detached"), 0o600, nil) }},
		{"remove", func(path *PrivatePath) error { return path.Remove() }},
		{"enumerate", func(path *PrivatePath) error { _, err := path.ReadDirectory(); return err }},
		{"sync", func(path *PrivatePath) error { return path.SyncDirectory() }},
		{"try-lock", func(path *PrivatePath) error {
			lock, err := path.TryLock(context.Background())
			if lock != nil {
				_ = lock.Unlock()
			}
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			anchor := t.TempDir()
			components := []string{"projects", "project", "hooks", "workspace"}
			leaf := "event.json"
			if operation.name == "try-lock" {
				leaf = "event.lock"
			}
			authority, err := OpenPrivatePath(anchor, components, leaf, true)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			if operation.name != "try-lock" {
				if err := authority.WriteFileAtomicModeWithHook([]byte("old"), 0o600, nil); err != nil {
					t.Fatal(err)
				}
			}
			renamePrivateWindowsTestDirectory(t, filepath.Join(anchor, "projects"), filepath.Join(anchor, "old-projects"))
			fresh, err := OpenPrivatePath(anchor, components, leaf, true)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if operation.name != "try-lock" {
				if err := fresh.WriteFileAtomicModeWithHook([]byte("replacement"), 0o600, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := operation.run(authority); err == nil {
				t.Fatalf("%s accepted authority detached by an intermediate ancestor replacement", operation.name)
			}
			if operation.name != "try-lock" {
				data, err := fresh.ReadFile()
				if err != nil || string(data) != "replacement" {
					t.Fatalf("fresh generation=%q %v", data, err)
				}
			}
		})
	}
}

func TestWindowsPrivateLockPreventsIntermediateAncestorReplacementDuringLease(t *testing.T) {
	anchor := t.TempDir()
	components := []string{"projects", "project", "hooks", "workspace"}
	authority, err := OpenPrivatePath(anchor, components, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := authority.TryLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(anchor, "projects"), filepath.Join(anchor, "old-projects")); err == nil {
		_ = lock.Unlock()
		t.Fatal("active Windows lock lease allowed intermediate ancestor replacement")
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPrivatePathClosesCompleteAuthorityChainExactlyOnce(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	handles := append([]windows.Handle(nil), authority.platform.chain...)
	if len(handles) != 5 {
		t.Fatalf("retained handles=%d, want anchor plus four components", len(handles))
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("second Close()=%v", err)
	}
	for _, handle := range handles {
		if _, err := privateWindowsIdentity(handle); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			t.Fatalf("retained handle %v remains open: %v", handle, err)
		}
	}
}

func TestWindowsPrivateLockClosesTransferredAuthorityChainExactlyOnce(t *testing.T) {
	anchor := t.TempDir()
	authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, "event.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	handles := append([]windows.Handle(nil), authority.platform.chain...)
	lock, err := authority.TryLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("wrapper Close after transfer=%v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("second Unlock()=%v", err)
	}
	for _, handle := range handles {
		if _, err := privateWindowsIdentity(handle); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			t.Fatalf("transferred handle %v remains open: %v", handle, err)
		}
	}
}

func TestWindowsPrivatePathMissingIntermediateAncestorIsNotLeafAbsence(t *testing.T) {
	operations := []struct {
		name string
		run  func(*PrivatePath) error
	}{
		{"read", func(path *PrivatePath) error { _, err := path.ReadFile(); return err }},
		{"write", func(path *PrivatePath) error { return path.WriteFileAtomicModeWithHook([]byte("detached"), 0o600, nil) }},
		{"remove", func(path *PrivatePath) error { return path.Remove() }},
		{"enumerate", func(path *PrivatePath) error { _, err := path.ReadDirectory(); return err }},
		{"sync", func(path *PrivatePath) error { return path.SyncDirectory() }},
		{"try-lock", func(path *PrivatePath) error {
			lock, err := path.TryLock(context.Background())
			if lock != nil {
				_ = lock.Unlock()
			}
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			anchor := t.TempDir()
			leaf := "event.json"
			if operation.name == "try-lock" {
				leaf = "event.lock"
			}
			authority, err := OpenPrivatePath(anchor, []string{"projects", "project", "hooks", "workspace"}, leaf, true)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			if operation.name != "try-lock" {
				if err := authority.WriteFileAtomicModeWithHook([]byte("old"), 0o600, nil); err != nil {
					t.Fatal(err)
				}
			}
			renamePrivateWindowsTestDirectory(t, filepath.Join(anchor, "projects"), filepath.Join(anchor, "old-projects"))
			if err := operation.run(authority); err == nil {
				t.Fatalf("%s treated a missing intermediate ancestor as an absent leaf", operation.name)
			}
		})
	}
}

func renamePrivateWindowsTestDirectory(t *testing.T, oldPath, newPath string) {
	t.Helper()
	if err := os.Rename(oldPath, newPath); err != nil {
		// The retained handles intentionally allow deletion. Some Windows
		// filesystems nevertheless reject directory rename while descendants
		// are open, so they cannot construct this adversarial namespace swap.
		// The handle-level identity-race test covers the same authority check.
		if os.IsPermission(err) {
			t.Skipf("directory replacement fixture unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func setPrivateWindowsTestDACL(t *testing.T, path, sddl string, protected bool) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	protection := uint32(windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if protected {
		protection = windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	information := windows.SECURITY_INFORMATION(uint32(windows.DACL_SECURITY_INFORMATION) | protection)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func openPrivateWindowsTestHandle(t *testing.T, path string, directory bool) windows.Handle {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func privateWindowsTestSecurity(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.String()
}
