//go:build !windows

package service

import (
	"fmt"
	"os"
)

type retainedWorkspaceDirectory struct {
	file *os.File
	info os.FileInfo
}

func retainWorkspaceDirectory(path string) (workspaceDirectoryAuthority, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.IsDir() {
		_ = file.Close()
		return nil, fmt.Errorf("not a directory")
	}
	return &retainedWorkspaceDirectory{file: file, info: info}, nil
}

func (authority *retainedWorkspaceDirectory) matches(info os.FileInfo) bool {
	return authority != nil && authority.info != nil && info != nil && os.SameFile(authority.info, info)
}

func (authority *retainedWorkspaceDirectory) close() error {
	if authority == nil || authority.file == nil {
		return nil
	}
	err := authority.file.Close()
	authority.file = nil
	return err
}
