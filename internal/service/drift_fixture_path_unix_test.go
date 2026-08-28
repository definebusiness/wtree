//go:build !windows

package service

// driftFixturePath keeps the deliberately synthetic paths in the injected
// drift-reader tests stable on Unix. Windows uses a native absolute path for
// the same filesystem-facing test input.
func driftFixturePath(path string) string { return path }
