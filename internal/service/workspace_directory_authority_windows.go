//go:build windows

package service

import "os"

// Windows grouping operations use the platform-owned clone staging boundary;
// do not retain a directory handle here because its sharing semantics can
// block the Git child creation this receipt is protecting.
type retainedWorkspaceDirectory struct{ info os.FileInfo }

func retainWorkspaceDirectory(path string) (workspaceDirectoryAuthority, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	return retainedWorkspaceDirectory{info: info}, nil
}

func (authority retainedWorkspaceDirectory) matches(info os.FileInfo) bool {
	return authority.info != nil && info != nil && os.SameFile(authority.info, info)
}

func (retainedWorkspaceDirectory) close() error { return nil }
