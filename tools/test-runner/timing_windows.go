//go:build windows

package main

import "fmt"

const timingPersistenceAvailable = false

// Windows Go exposes writable directories with POSIX-incompatible mode bits.
// More importantly, os.Rename explicitly does not promise atomic replacement
// on non-Unix platforms. Until a native, separately proven replacement layer
// exists, persistence is unavailable and callers retain cache-free semantics.
func privateTimingPath(root, mode string) (string, error) {
	return "", fmt.Errorf("timing cache persistence unavailable on windows")
}

func replaceTimingFile(temporaryName, path, directory string) error {
	return fmt.Errorf("timing cache persistence unavailable on windows")
}
