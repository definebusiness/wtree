//go:build windows

package service

import (
	"os"
	"path/filepath"
	"strings"
)

// driftFixturePath turns synthetic Unix-rooted filesystem fixtures into
// native absolute paths. These tests inject every filesystem read; the helper
// deliberately leaves logical mount and configured-spelling contracts alone.
func driftFixturePath(path string) string {
	if !strings.HasPrefix(path, "/") {
		return path
	}
	return filepath.Join(os.TempDir(), "wtree-drift-fixture", filepath.FromSlash(strings.TrimPrefix(path, "/")))
}
