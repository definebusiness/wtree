//go:build !windows

package service

import "os"

func openHookGenerationFile(path string) (*os.File, error) {
	return os.Open(path)
}
