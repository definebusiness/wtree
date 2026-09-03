//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package main

import "fmt"

const timingPersistenceAvailable = false

// Persistence is disabled on platforms outside the tested POSIX and Windows
// contracts. Inventory and test execution remain complete without timing data.
func privateTimingPath(root, mode string) (string, error) {
	return "", fmt.Errorf("timing cache persistence unavailable on this platform")
}

func replaceTimingFile(temporaryName, path, directory string) error {
	return fmt.Errorf("timing cache persistence unavailable on this platform")
}
