package fsutil

import (
	"errors"
	"fmt"
	"os"
)

type directoryHandle interface {
	Stat() (os.FileInfo, error)
	Close() error
}

func openDirectoryHandle(path string) (directoryHandle, error) {
	return os.Open(path)
}

func openAndCloseDirectory(path string, open func(string) (directoryHandle, error)) error {
	directory, err := open(path)
	if err != nil {
		return err
	}
	info, statErr := directory.Stat()
	if statErr != nil {
		return errors.Join(statErr, directory.Close())
	}
	if !info.IsDir() {
		return errors.Join(fmt.Errorf("%q is not a directory", path), directory.Close())
	}
	return directory.Close()
}
