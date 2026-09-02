package fsutil

import (
	"context"
	"errors"
	"os"
)

// PrivatePath retains authority over one application-owned directory suffix
// and leaf beneath a caller-owned anchor. Components are traversed one at a
// time without following links or reparses.
type PrivatePath struct{ platform *privatePath }

// PrivateLock is an immediate advisory lock bound to the validated leaf
// generation. Unlock also releases the retained directory authority.
type PrivateLock interface{ Unlock() error }

var ErrPrivateLockHeld = errors.New("private lock is held")
var ErrPrivateRemovalAmbiguous = errors.New("private removal generation is ambiguous")
var errPrivateDirectoryAuthority = errors.New("private directory authority is invalid")

// ErrPrivateRemovalQuarantined marks an exact private generation that was
// removed from its authoritative name but deliberately retained as bounded
// evidence because the platform cannot delete it by handle.
var ErrPrivateRemovalQuarantined = errors.New("private removal generation is retained in quarantine")

func PrivatePathNotExist(err error) bool { return privatePathNotExist(err) }

// OpenPrivatePath opens the exact absolute anchor and then traverses the owned
// components beneath it. The anchor itself is never created or altered.
func OpenPrivatePath(anchor string, components []string, leaf string, create bool) (*PrivatePath, error) {
	return openPrivatePathWithOptions(anchor, components, leaf, create, false)
}

// OpenPrivatePathForMutation may protect effective-user-owned, plain existing
// components while converging an application mutation path. Read-only callers
// must use OpenPrivatePath.
func OpenPrivatePathForMutation(anchor string, components []string, leaf string) (*PrivatePath, error) {
	return openPrivatePathWithOptions(anchor, components, leaf, true, true)
}

func openPrivatePathWithOptions(anchor string, components []string, leaf string, create, protectExisting bool) (*PrivatePath, error) {
	if !validPrivateName(leaf) {
		return nil, errors.New("invalid private leaf name")
	}
	for _, component := range components {
		if !validPrivateName(component) {
			return nil, errors.New("invalid private directory component")
		}
	}
	platform, err := openPrivatePath(anchor, components, leaf, create, protectExisting)
	if err != nil {
		return nil, err
	}
	return &PrivatePath{platform: platform}, nil
}

func validPrivateName(name string) bool {
	return name != "" && name != "." && name != ".." && !containsPathSeparator(name)
}

func (path *PrivatePath) Close() error {
	if path == nil || path.platform == nil {
		return nil
	}
	err := path.platform.close()
	path.platform = nil
	return err
}

func (path *PrivatePath) ReadFile() ([]byte, error) {
	if path == nil || path.platform == nil {
		return nil, os.ErrInvalid
	}
	return path.platform.readFile()
}

func (path *PrivatePath) WriteFileAtomicModeWithHook(data []byte, mode os.FileMode, hook AtomicStepHook) error {
	if path == nil || path.platform == nil {
		return os.ErrInvalid
	}
	return path.platform.writeFileAtomic(data, mode, hook)
}

func (path *PrivatePath) Remove() error {
	return path.RemoveWithHook(nil)
}

func (path *PrivatePath) RemoveWithHook(hook AtomicStepHook) error {
	if path == nil || path.platform == nil {
		return os.ErrInvalid
	}
	return path.platform.remove(hook)
}

// ReadDirectory validates every entry as a private regular leaf before
// returning its name.
func (path *PrivatePath) ReadDirectory() ([]string, error) {
	if path == nil || path.platform == nil {
		return nil, os.ErrInvalid
	}
	return path.platform.readDirectory()
}

// ReadFileNamed reads a validated leaf beneath the retained directory.
func (path *PrivatePath) ReadFileNamed(name string) ([]byte, error) {
	if path == nil || path.platform == nil || !validPrivateName(name) {
		return nil, os.ErrInvalid
	}
	return path.platform.readFileNamed(name)
}

// SyncDirectory confirms the retained containing-directory publication
// boundary using the strongest operation supported by the platform.
func (path *PrivatePath) SyncDirectory() error {
	if path == nil || path.platform == nil {
		return os.ErrInvalid
	}
	return path.platform.syncDirectory()
}

func (path *PrivatePath) TryLock(ctx context.Context) (PrivateLock, error) {
	if path == nil || path.platform == nil {
		return nil, os.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := path.platform.tryLock()
	if err != nil {
		return nil, err
	}
	// The lock owns the authority from this point onward.
	path.platform = nil
	return lock, nil
}
