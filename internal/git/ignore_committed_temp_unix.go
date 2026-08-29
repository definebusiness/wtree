//go:build !windows

package git

import (
	"errors"
	"fmt"
	"os"
)

func createPrivateCommittedIgnoreTemp(pattern string) (*committedIgnoreTemp, error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, err
	}
	path := file.Name()
	cleanup := func() error { return os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("protect committed ignore exclude: %w", err), file.Close(), cleanup())
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("validate committed ignore exclude permissions: %w", err), file.Close(), cleanup())
	}
	if info.Mode().Perm() != 0o600 {
		return nil, errors.Join(fmt.Errorf("validate committed ignore exclude permissions: got %03o, want 600", info.Mode().Perm()), file.Close(), cleanup())
	}
	return &committedIgnoreTemp{file: file, cleanup: cleanup}, nil
}
