//go:build windows

package service

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

func mutateClonePostIdentity(path, mutation string) error {
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	switch mutation {
	case "byte-identical-inode", "exact-config-inode":
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		replacement := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"-replacement")
		if err := os.WriteFile(replacement, data, 0o600); err != nil {
			return err
		}
		defer os.Remove(replacement)
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := os.Rename(replacement, path); err != nil {
			return err
		}
		after, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if os.SameFile(before, after) {
			return errors.New("Windows replacement retained the prior file identity")
		}
		return nil
	case "mtime-only":
		future := time.Now().Add(time.Hour).Round(0)
		if err := os.Chtimes(path, future, future); err != nil {
			return err
		}
		after, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if before.ModTime().Equal(after.ModTime()) {
			return errors.New("Windows timestamp mutation was not observable")
		}
		return nil
	case "mode-only":
		if err := os.Chmod(path, 0o400); err != nil {
			return err
		}
		after, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if before.Mode().Perm()&0o222 == after.Mode().Perm()&0o222 {
			return errors.New("Windows writability mutation was not observable")
		}
		return nil
	default:
		return os.ErrInvalid
	}
}
