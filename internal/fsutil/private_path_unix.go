//go:build !windows

package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func privatePathNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

type privatePath struct {
	anchor     string
	chain      []*os.File
	components []string
	directory  *os.File
	leaf       string
}

type unixPrivateLock struct {
	authority *privatePath
	file      *os.File
	info      os.FileInfo
}

func containsPathSeparator(name string) bool {
	return filepath.Base(name) != name || filepath.Clean(name) != name || name == string(filepath.Separator)
}

func openPrivatePath(anchor string, components []string, leaf string, create, protectExisting bool) (*privatePath, error) {
	if !filepath.IsAbs(anchor) || filepath.Clean(anchor) != anchor {
		return nil, errors.New("private path anchor must be a cleaned absolute path")
	}
	fd, err := unix.Open(anchor, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open private path anchor: %w", err)
	}
	current := os.NewFile(uintptr(fd), anchor)
	if current == nil {
		unix.Close(fd)
		return nil, errors.New("adopt private path anchor")
	}
	anchorHandleInfo, handleErr := current.Stat()
	anchorPathInfo, pathErr := os.Lstat(anchor)
	if handleErr != nil || pathErr != nil || !anchorPathInfo.IsDir() || anchorPathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(anchorHandleInfo, anchorPathInfo) {
		current.Close()
		return nil, errors.Join(errors.New("private path anchor identity or type differs"), handleErr, pathErr)
	}
	chain := []*os.File{current}
	for _, component := range components {
		next, openErr := openPrivateDirectoryAt(current, component, create, protectExisting)
		if openErr != nil {
			closePrivateUnixChain(chain)
			return nil, openErr
		}
		chain = append(chain, next)
		current = next
	}
	authority := &privatePath{anchor: anchor, chain: chain, components: append([]string(nil), components...), directory: current, leaf: leaf}
	if err := authority.validateLeaf(false); err != nil {
		if !protectExisting {
			authority.close()
			return nil, err
		}
		if protectErr := authority.protectExistingLeaf(); protectErr != nil {
			authority.close()
			return nil, errors.Join(err, protectErr)
		}
		if verifyErr := authority.validateLeaf(false); verifyErr != nil {
			authority.close()
			return nil, verifyErr
		}
	}
	return authority, nil
}

func closePrivateUnixChain(chain []*os.File) error {
	var result error
	for index := len(chain) - 1; index >= 0; index-- {
		if chain[index] != nil {
			result = errors.Join(result, chain[index].Close())
		}
	}
	return result
}

func openPrivateDirectoryAt(parent *os.File, name string, create, protectExisting bool) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil && create && errors.Is(err, unix.ENOENT) {
		created := false
		if mkdirErr := unix.Mkdirat(int(parent.Fd()), name, 0o700); mkdirErr == nil {
			created = true
		} else if !errors.Is(mkdirErr, unix.EEXIST) {
			return nil, fmt.Errorf("create private directory %q: %w", name, mkdirErr)
		}
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == nil && created {
			err = unix.Fchmod(fd, 0o700)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open private directory %q: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("adopt private directory handle")
	}
	info, err := file.Stat()
	if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() != 0o700 && protectExisting {
		var stat unix.Stat_t
		if statErr := unix.Fstat(fd, &stat); statErr == nil && int(stat.Uid) == os.Geteuid() {
			err = unix.Fchmod(fd, 0o700)
			if err == nil {
				info, err = file.Stat()
			}
		}
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		file.Close()
		return nil, errors.Join(errors.New("unsafe private directory"), err)
	}
	return file, nil
}

func (path *privatePath) protectExistingLeaf() error {
	fd, err := unix.Openat(int(path.directory.Fd()), path.leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() {
		return errors.Join(errors.New("unsafe existing private leaf"), err)
	}
	return unix.Fchmod(fd, 0o600)
}

func (path *privatePath) close() error {
	if path == nil || len(path.chain) == 0 {
		return nil
	}
	err := closePrivateUnixChain(path.chain)
	path.chain = nil
	path.directory = nil
	return err
}

func (path *privatePath) openLeaf(flags int, create bool) (*os.File, error) {
	if path == nil || path.directory == nil {
		return nil, os.ErrInvalid
	}
	if err := path.validateDirectory(); err != nil {
		return nil, errors.Join(errPrivateDirectoryAuthority, err)
	}
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Openat(int(path.directory.Fd()), path.leaf, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path.leaf)
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("adopt private leaf handle")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		file.Close()
		return nil, errors.Join(errors.New("unsafe private leaf"), err)
	}
	return file, nil
}

func (path *privatePath) validateDirectory() error {
	if path == nil || path.directory == nil || len(path.chain) != len(path.components)+1 || path.chain[len(path.chain)-1] != path.directory {
		return os.ErrInvalid
	}
	anchorFD, err := unix.Open(path.anchor, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(errors.New("private path anchor generation changed"), err)
	}
	anchorCurrent := os.NewFile(uintptr(anchorFD), path.anchor)
	if anchorCurrent == nil {
		unix.Close(anchorFD)
		return errors.New("adopt private path anchor validation handle")
	}
	retainedAnchorInfo, retainedAnchorErr := path.chain[0].Stat()
	currentAnchorInfo, currentAnchorErr := anchorCurrent.Stat()
	anchorCloseErr := anchorCurrent.Close()
	if retainedAnchorErr != nil || currentAnchorErr != nil || anchorCloseErr != nil || !retainedAnchorInfo.IsDir() || retainedAnchorInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(retainedAnchorInfo, currentAnchorInfo) {
		return errors.Join(errors.New("private path anchor generation changed"), retainedAnchorErr, currentAnchorErr, anchorCloseErr)
	}
	for index, component := range path.components {
		retained := path.chain[index+1]
		retainedInfo, statErr := retained.Stat()
		if statErr != nil || !retainedInfo.IsDir() || retainedInfo.Mode()&os.ModeSymlink != 0 || retainedInfo.Mode().Perm() != 0o700 {
			return errors.Join(errors.New("unsafe retained private directory"), statErr)
		}
		current, openErr := openPrivateDirectoryAt(path.chain[index], component, false, false)
		if openErr != nil {
			return errors.Join(errors.New("private directory generation changed"), openErr)
		}
		currentInfo, statErr := current.Stat()
		closeErr := current.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(retainedInfo, currentInfo) {
			return errors.Join(errors.New("private directory generation changed"), statErr, closeErr)
		}
	}
	return nil
}

func (path *privatePath) validateLeaf(required bool) error {
	file, err := path.openLeaf(unix.O_RDONLY, false)
	if err == nil {
		return file.Close()
	}
	if !required && errors.Is(err, unix.ENOENT) && !errors.Is(err, errPrivateDirectoryAuthority) {
		return nil
	}
	return err
}

func (path *privatePath) readFile() ([]byte, error) {
	return path.readFileNamed(path.leaf)
}

func (path *privatePath) readFileNamed(name string) ([]byte, error) {
	copy := *path
	copy.leaf = name
	file, err := copy.openLeaf(unix.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	validationErr := path.validateDirectory()
	if readErr != nil || closeErr != nil || validationErr != nil {
		return nil, errors.Join(readErr, closeErr, validationErr)
	}
	return data, nil
}

func (path *privatePath) readDirectory() ([]string, error) {
	if err := path.validateDirectory(); err != nil {
		return nil, err
	}
	fd, err := unix.Dup(int(path.directory.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		unix.Close(fd)
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "private-directory")
	if directory == nil {
		unix.Close(fd)
		return nil, errors.New("adopt private directory enumeration handle")
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validPrivateName(entry.Name()) {
			return nil, errors.New("unsafe private directory entry name")
		}
		copy := *path
		copy.leaf = entry.Name()
		file, err := copy.openLeaf(unix.O_RDONLY, false)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	if err := path.validateDirectory(); err != nil {
		return nil, err
	}
	return names, nil
}

func (path *privatePath) writeFileAtomic(data []byte, mode os.FileMode, hook AtomicStepHook) error {
	if mode.Perm() != 0o600 {
		return errors.New("private leaf mode must be 0600")
	}
	if err := path.validateLeaf(false); err != nil {
		return err
	}
	if err := atomicStep(hook, "create-temp"); err != nil {
		return err
	}
	temporary, name, err := path.createTemporary()
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(path.directory.Fd()), name, 0)
	if err := atomicStep(hook, "write"); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := atomicStep(hook, "sync"); err != nil {
		temporary.Close()
		return err
	}
	if err := Sync(temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := atomicStep(hook, "close"); err != nil {
		temporary.Close()
		return err
	}
	retainedFD, err := unix.Dup(int(temporary.Fd()))
	if err != nil {
		temporary.Close()
		return err
	}
	unix.CloseOnExec(retainedFD)
	retained := os.NewFile(uintptr(retainedFD), name)
	if retained == nil {
		unix.Close(retainedFD)
		temporary.Close()
		return errors.New("retain private temporary generation")
	}
	defer retained.Close()
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := atomicStep(hook, "before-rename"); err != nil {
		return err
	}
	if err := path.validateDirectory(); err != nil {
		return err
	}
	if err := unix.Renameat(int(path.directory.Fd()), name, int(path.directory.Fd()), path.leaf); err != nil {
		return err
	}
	resulting, err := path.openLeaf(unix.O_RDONLY, false)
	if err != nil {
		return &postReplacementError{Err: err}
	}
	retainedInfo, retainedErr := retained.Stat()
	resultingInfo, resultingErr := resulting.Stat()
	resultingCloseErr := resulting.Close()
	if retainedErr != nil || resultingErr != nil || resultingCloseErr != nil || !os.SameFile(retainedInfo, resultingInfo) {
		return &postReplacementError{Err: errors.Join(errors.New("private replacement leaf identity differs from retained generation"), retainedErr, resultingErr, resultingCloseErr)}
	}
	if err := atomicStep(hook, "dir-sync"); err != nil {
		return &postReplacementError{Err: err}
	}
	if err := Sync(path.directory); err != nil {
		return &postReplacementError{Err: err}
	}
	if err := path.validateDirectory(); err != nil {
		return &postReplacementError{Err: err}
	}
	return nil
}

func (path *privatePath) createTemporary() (*os.File, string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name := fmt.Sprintf(".%s-%d", path.leaf, attempt)
		fd, err := unix.Openat(int(path.directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if err := unix.Fchmod(fd, 0o600); err != nil {
			unix.Close(fd)
			unix.Unlinkat(int(path.directory.Fd()), name, 0)
			return nil, "", err
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			unix.Close(fd)
			return nil, "", errors.New("adopt private temporary handle")
		}
		return file, name, nil
	}
	return nil, "", os.ErrExist
}

func (path *privatePath) remove(hook AtomicStepHook) error {
	file, err := path.openLeaf(unix.O_RDONLY, false)
	if errors.Is(err, unix.ENOENT) && !errors.Is(err, errPrivateDirectoryAuthority) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := path.openLeaf(unix.O_RDONLY, false)
	if err != nil {
		return err
	}
	currentInfo, err := current.Stat()
	currentCloseErr := current.Close()
	if err != nil || currentCloseErr != nil || !os.SameFile(info, currentInfo) {
		return errors.Join(errors.New("private leaf generation changed before removal"), err, currentCloseErr)
	}
	if err := atomicStep(hook, "before-quarantine"); err != nil {
		return err
	}
	quarantine, err := path.quarantineLeaf()
	if err != nil {
		return err
	}
	if err := atomicStep(hook, "after-quarantine"); err != nil {
		return path.restoreQuarantine(quarantine, err)
	}
	quarantinedPath := *path
	quarantinedPath.leaf = quarantine
	quarantined, openErr := quarantinedPath.openLeaf(unix.O_RDONLY, false)
	if openErr != nil {
		return path.restoreQuarantine(quarantine, openErr)
	}
	defer quarantined.Close()
	quarantinedInfo, statErr := quarantined.Stat()
	if statErr != nil || !os.SameFile(info, quarantinedInfo) {
		return path.restoreQuarantine(quarantine, statErr)
	}
	concurrent, currentErr := path.openLeaf(unix.O_RDONLY, false)
	if currentErr == nil {
		concurrent.Close()
		return errors.Join(ErrPrivateRemovalAmbiguous, path.syncDirectory())
	}
	if !errors.Is(currentErr, unix.ENOENT) {
		return errors.Join(ErrPrivateRemovalAmbiguous, currentErr, path.syncDirectory())
	}
	if err := atomicStep(hook, "before-delete"); err != nil {
		return errors.Join(ErrPrivateRemovalAmbiguous, err, path.syncDirectory())
	}
	currentQuarantine, currentErr := quarantinedPath.openLeaf(unix.O_RDONLY, false)
	if currentErr != nil {
		return errors.Join(ErrPrivateRemovalAmbiguous, currentErr, path.syncDirectory())
	}
	currentQuarantineInfo, currentStatErr := currentQuarantine.Stat()
	currentCloseErr = currentQuarantine.Close()
	if currentStatErr != nil || currentCloseErr != nil || !os.SameFile(quarantinedInfo, currentQuarantineInfo) {
		return errors.Join(ErrPrivateRemovalAmbiguous, errors.New("private quarantine generation changed before deletion"), currentStatErr, currentCloseErr, path.syncDirectory())
	}
	// Unix offers no identity-conditional or descriptor-bound unlink. Keep the
	// verified generation quarantined instead of reopening a name-based race
	// after its identity has been established. The caller can safely observe
	// that the authoritative leaf name is absent, but cannot claim that removal
	// of this exact generation was both complete and durable.
	if err := path.syncDirectory(); err != nil {
		return errors.Join(ErrPrivateRemovalAmbiguous, err)
	}
	return errors.Join(ErrPrivateRemovalAmbiguous, ErrPrivateRemovalQuarantined)
}

func (path *privatePath) quarantineLeaf() (string, error) {
	// One fixed no-replace evidence name bounds conservative Unix cleanup to a
	// single retained generation for each workspace/event.
	name := "." + path.leaf + ".remove-1-1"
	if err := privateRenameNoReplace(int(path.directory.Fd()), path.leaf, name); err != nil {
		return "", errors.Join(ErrPrivateRemovalAmbiguous, err)
	}
	return name, nil
}

func (path *privatePath) restoreQuarantine(quarantine string, cause error) error {
	restoreErr := privateRenameNoReplace(int(path.directory.Fd()), quarantine, path.leaf)
	syncErr := path.syncDirectory()
	return errors.Join(ErrPrivateRemovalAmbiguous, cause, restoreErr, syncErr)
}

func (path *privatePath) syncDirectory() error {
	if err := path.validateDirectory(); err != nil {
		return err
	}
	if err := Sync(path.directory); err != nil {
		return err
	}
	return path.validateDirectory()
}

func (path *privatePath) tryLock() (*unixPrivateLock, error) {
	// The retained directory is the stable half of the composite lock. A leaf
	// name can be renamed on Unix while its descriptor is open; locking the
	// directory prevents a replacement leaf from becoming a concurrent lease.
	if err := unix.Flock(int(path.directory.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrPrivateLockHeld
		}
		return nil, err
	}
	file, err := path.openLeaf(unix.O_RDONLY, false)
	if errors.Is(err, unix.ENOENT) && !errors.Is(err, errPrivateDirectoryAuthority) {
		fd, createErr := unix.Openat(int(path.directory.Fd()), path.leaf, unix.O_RDONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if createErr == nil {
			createErr = unix.Fchmod(fd, 0o600)
		}
		if createErr == nil {
			file = os.NewFile(uintptr(fd), path.leaf)
			if file == nil {
				unix.Close(fd)
				_ = unix.Unlinkat(int(path.directory.Fd()), path.leaf, 0)
				createErr = errors.New("adopt private lock leaf handle")
			}
		} else if fd >= 0 {
			unix.Close(fd)
			_ = unix.Unlinkat(int(path.directory.Fd()), path.leaf, 0)
		}
		if errors.Is(createErr, unix.EEXIST) {
			file, err = path.openLeaf(unix.O_RDONLY, false)
		} else {
			err = createErr
		}
	}
	if err != nil {
		_ = unix.Flock(int(path.directory.Fd()), unix.LOCK_UN)
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		_ = unix.Flock(int(path.directory.Fd()), unix.LOCK_UN)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrPrivateLockHeld
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
		_ = unix.Flock(int(path.directory.Fd()), unix.LOCK_UN)
		return nil, err
	}
	if err := path.validateDirectory(); err != nil {
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
		_ = unix.Flock(int(path.directory.Fd()), unix.LOCK_UN)
		return nil, err
	}
	return &unixPrivateLock{authority: path, file: file, info: info}, nil
}

func (lock *unixPrivateLock) Unlock() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	var result error
	current, err := lock.authority.openLeaf(unix.O_RDONLY, false)
	if err != nil {
		result = errors.Join(result, err)
	} else {
		currentInfo, statErr := current.Stat()
		result = errors.Join(result, current.Close())
		if statErr != nil || !os.SameFile(lock.info, currentInfo) {
			result = errors.Join(result, errors.New("private lock leaf generation changed during lease"), statErr)
		}
	}
	result = errors.Join(result, unix.Flock(int(lock.file.Fd()), unix.LOCK_UN), lock.file.Close(), unix.Flock(int(lock.authority.directory.Fd()), unix.LOCK_UN), lock.authority.close())
	lock.file = nil
	return result
}
