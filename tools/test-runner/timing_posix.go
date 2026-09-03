//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const timingPersistenceAvailable = true

// privateTimingPath is the POSIX persistence contract: each runner-owned
// component is a real private directory, and the final target is a private
// regular file. The caller-owned cache root itself need not be private.
func privateTimingPath(root, mode string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || (mode != "normal" && mode != "race") {
		return "", fmt.Errorf("invalid timing cache root or mode")
	}
	rootInfo, err := os.Lstat(root)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", fmt.Errorf("create timing cache root: %w", err)
		}
		rootInfo, err = os.Lstat(root)
	}
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("unsafe timing cache root")
	}
	directory := root
	for _, element := range []string{"wtree-test-runner", timingFormat, runtime.GOOS, runtime.GOARCH, mode} {
		directory = filepath.Join(directory, element)
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			if err := os.Mkdir(directory, 0o700); err != nil {
				return "", fmt.Errorf("create private timing cache directory: %w", err)
			}
			info, err = os.Lstat(directory)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("unsafe private timing cache directory")
		}
	}
	path := filepath.Join(directory, "weights.tsv")
	info, err := os.Lstat(path)
	if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0) {
		return "", fmt.Errorf("unsafe timing cache file")
	}
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect timing cache file: %w", err)
	}
	return path, nil
}

// os.Rename is atomic for same-directory replacement on Unix. Both directory
// syncs make the replacement durable across a completed return on filesystems
// that honor fsync. This contract is intentionally not compiled for Windows.
func replaceTimingFile(temporaryName, path, directory string) error {
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync timing cache directory before replacement: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace timing cache: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict timing cache file: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync timing cache directory after replacement: %w", err)
	}
	return nil
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
