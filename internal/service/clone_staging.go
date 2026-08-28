package service

import "os"

// cloneStagingLease retains the platform capability that proves the staging
// root and its parent are still the objects created for this transaction.
// Windows backs the lease with handles; Unix relies on the existing identity
// and private-mode checks.
type cloneStagingLease interface {
	prepareChild(string, string, os.FileInfo, os.FileInfo, func(string, os.FileMode) error, func(string) (os.FileInfo, error)) (os.FileInfo, error)
	captureChild(string, os.FileInfo, os.FileInfo, func(string) (os.FileInfo, error)) (os.FileInfo, error)
	releaseChild(string, os.FileInfo, os.FileInfo, func(string) (os.FileInfo, error)) error
	closeAll() error
}
