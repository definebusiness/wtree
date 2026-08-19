package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type cloneFileSnapshot struct {
	path   string
	exists bool
	data   []byte
	mode   os.FileMode
	info   os.FileInfo
}

func secureCloneFileSnapshot(path string) (cloneFileSnapshot, error) {
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return cloneFileSnapshot{path: path}, nil
	}
	if err != nil {
		return cloneFileSnapshot{}, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return cloneFileSnapshot{}, fmt.Errorf("%q must be a regular non-symlink file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cloneFileSnapshot{}, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return cloneFileSnapshot{}, NewError(ErrorConflict, fmt.Errorf("%q changed while captured", path))
	}
	return cloneFileSnapshot{path: path, exists: true, data: data, mode: before.Mode(), info: before}, nil
}

func sameCloneFileSnapshot(expected, actual cloneFileSnapshot) bool {
	if expected.exists != actual.exists {
		return false
	}
	if !expected.exists {
		return true
	}
	return expected.mode == actual.mode && expected.info != nil && actual.info != nil && os.SameFile(expected.info, actual.info) && bytes.Equal(expected.data, actual.data)
}

func revalidateCloneFileSnapshot(expected cloneFileSnapshot) error {
	actual, err := secureCloneFileSnapshot(expected.path)
	if err != nil {
		return err
	}
	if !sameCloneFileSnapshot(expected, actual) {
		return NewError(ErrorConflict, fmt.Errorf("publication file %q changed", expected.path))
	}
	return nil
}

func cloneSnapshotHasExactBytes(snapshot cloneFileSnapshot, data []byte, mode os.FileMode) bool {
	return snapshot.exists && snapshot.mode.IsRegular() && snapshot.mode&os.ModeSymlink == 0 && requestedFilePermissionsMatch(snapshot.mode, mode) && bytes.Equal(snapshot.data, data)
}

func observableFileWritabilityMatches(actual, requested os.FileMode) bool {
	return (actual.Perm()&0o222 != 0) == (requested.Perm()&0o222 != 0)
}

func revalidateClonePublicationGeneration(registry, state, recovery cloneFileSnapshot) error {
	for _, snapshot := range []cloneFileSnapshot{registry, state, recovery} {
		if err := revalidateCloneFileSnapshot(snapshot); err != nil {
			return err
		}
	}
	return nil
}

type cloneTreeEntry struct {
	path   string
	mode   os.FileMode
	size   int64
	mtime  int64
	digest string
	info   os.FileInfo
}

type cloneTreeInventory struct{ entries []cloneTreeEntry }

func captureCloneTree(root string) (cloneTreeInventory, error) {
	var entries []cloneTreeEntry
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !primeFileIdentity(info) {
			return fmt.Errorf("capture clone tree identity: %q", path)
		}
		item := cloneTreeEntry{path: filepath.ToSlash(relative), mode: info.Mode(), size: info.Size(), mtime: info.ModTime().UnixNano(), info: info}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(data)
			item.digest = hex.EncodeToString(digest[:])
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256([]byte(target))
			item.digest = hex.EncodeToString(digest[:])
		case info.IsDir():
		default:
			return fmt.Errorf("unsupported filesystem object in clone tree: %q", path)
		}
		entries = append(entries, item)
		return nil
	})
	if err != nil {
		return cloneTreeInventory{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return cloneTreeInventory{entries: entries}, nil
}

func revalidateCloneTree(root string, expected cloneTreeInventory) error {
	actual, err := captureCloneTree(root)
	if err != nil {
		return err
	}
	if len(actual.entries) != len(expected.entries) {
		return errors.New("clone destination tree membership changed")
	}
	for index := range expected.entries {
		want := expected.entries[index]
		got := actual.entries[index]
		if want.path != got.path || want.mode != got.mode || want.size != got.size || want.mtime != got.mtime || want.digest != got.digest || want.info == nil || got.info == nil || !os.SameFile(want.info, got.info) {
			return fmt.Errorf("clone destination tree changed at %q", expected.entries[index].path)
		}
	}
	return nil
}

// translateCloneRootAfterRename records the platform-owned transition from the
// staging name to the final name. Unix requires every root attribute to remain
// exact. Windows additionally records the root timestamp produced by the
// rename itself, after first proving exact identity and all other metadata, so
// every later inventory check still detects a root metadata mutation.
func translateCloneRootAfterRename(root string, inventory *cloneTreeInventory) error {
	if inventory == nil || len(inventory.entries) == 0 {
		return errors.New("clone tree inventory is empty")
	}
	rootIndex := -1
	for index := range inventory.entries {
		if inventory.entries[index].path == "." {
			rootIndex = index
			break
		}
	}
	if rootIndex == -1 {
		return errors.New("clone tree inventory has no root")
	}
	want := &inventory.entries[rootIndex]
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !reconcileCloneRootAfterRename(want, info) {
		return errors.New("published clone root identity or metadata changed")
	}
	return nil
}

func primeFileIdentity(info os.FileInfo) bool {
	return info != nil && os.SameFile(info, info)
}

type clonePathIdentity struct {
	path string
	info os.FileInfo
}

func captureClonePathIdentity(path string) (clonePathIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return clonePathIdentity{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return clonePathIdentity{}, fmt.Errorf("%q is not a real directory", path)
	}
	return clonePathIdentity{path: path, info: info}, nil
}

func revalidateClonePathIdentity(expected clonePathIdentity) error {
	actual, err := os.Lstat(expected.path)
	if err != nil || !actual.IsDir() || actual.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected.info, actual) {
		return NewError(ErrorConflict, fmt.Errorf("directory identity changed: %q", expected.path))
	}
	return nil
}
