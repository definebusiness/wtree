//go:build !windows

package service

import (
	"os"
	"time"

	"github.com/definebusiness/wtree/internal/fsutil"
)

func mutateClonePostIdentity(path, mutation string) error {
	switch mutation {
	case "byte-identical-inode":
		return fsutil.WriteFileAtomicMode(path, []byte("owned\n"), 0o600)
	case "mtime-only":
		future := time.Now().Add(time.Hour)
		return os.Chtimes(path, future, future)
	case "mode-only":
		return os.Chmod(path, 0o640)
	case "exact-config-inode":
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return fsutil.WriteFileAtomicMode(path, data, 0o600)
	default:
		return os.ErrInvalid
	}
}
