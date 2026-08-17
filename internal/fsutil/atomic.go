package fsutil

import (
	"os"
	"path/filepath"
)

// AtomicStepHook is deliberately small so owning packages can retain their
// existing failure-injection seams without each reimplementing the durability
// protocol. A hook is called before the named irreversible step.
type AtomicStepHook func(string) error

// WriteFileAtomicMode durably replaces one regular file while preserving the
// caller-selected mode. The temporary is co-located so rename is atomic.
func WriteFileAtomicMode(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomicMode(path, data, mode, nil, os.Rename)
}

// WriteFileAtomicCreateMode durably creates a file using creation permissions
// that remain subject to the process umask.
func WriteFileAtomicCreateMode(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomicModeCreate(path, data, mode, nil, os.Rename)
}

// WriteFileAtomicCreateModeWithReplace retains umask-correct creation while
// allowing transaction tests to inject the final replacement boundary.
func WriteFileAtomicCreateModeWithReplace(path string, data []byte, mode os.FileMode, replace func(string, string) error) error {
	if replace == nil {
		replace = os.Rename
	}
	return writeFileAtomicModeCreate(path, data, mode, nil, replace)
}

// WriteFileAtomicCreateModeWithHook is the creation variant of
// WriteFileAtomicModeWithHook. The hook may reject a changed target at the
// final replacement boundary while retaining umask-correct creation modes.
func WriteFileAtomicCreateModeWithHook(path string, data []byte, mode os.FileMode, hook AtomicStepHook) error {
	return writeFileAtomicModeCreate(path, data, mode, hook, os.Rename)
}

// WriteFileAtomicModeWithHook is WriteFileAtomicMode with test-only step
// observation. The temporary is always created beside the target, flushed and
// closed before replacement, then the containing directory is flushed.
func WriteFileAtomicModeWithHook(path string, data []byte, mode os.FileMode, hook AtomicStepHook) error {
	return writeFileAtomicMode(path, data, mode, hook, os.Rename)
}

// WriteFileAtomicModeWithReplace retains the same durability protocol while
// allowing a narrowly injected replacement operation in transaction tests.
func WriteFileAtomicModeWithReplace(path string, data []byte, mode os.FileMode, replace func(string, string) error) error {
	if replace == nil {
		replace = os.Rename
	}
	return writeFileAtomicMode(path, data, mode, nil, replace)
}

func writeFileAtomicMode(path string, data []byte, mode os.FileMode, hook AtomicStepHook, replace func(string, string) error) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := atomicStep(hook, "write"); err != nil {
		temporary.Close()
		return err
	}
	if err := Sync(temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := atomicStep(hook, "sync"); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := atomicStep(hook, "close"); err != nil {
		return err
	}
	if err := atomicStep(hook, "before-rename"); err != nil {
		return err
	}
	if err := replace(name, path); err != nil {
		return err
	}
	if err := atomicStep(hook, "dir-sync"); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return Sync(directoryFile)
}

func writeFileAtomicModeCreate(path string, data []byte, mode os.FileMode, hook AtomicStepHook, replace func(string, string) error) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := createTempWithUmask(directory, filepath.Base(path), mode)
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := atomicStep(hook, "write"); err != nil {
		temporary.Close()
		return err
	}
	if err := Sync(temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := atomicStep(hook, "sync"); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := atomicStep(hook, "close"); err != nil {
		return err
	}
	if err := atomicStep(hook, "before-rename"); err != nil {
		return err
	}
	if err := replace(name, path); err != nil {
		return err
	}
	if err := atomicStep(hook, "dir-sync"); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return Sync(directoryFile)
}

func createTempWithUmask(directory, base string, mode os.FileMode) (*os.File, error) {
	for attempt := 0; attempt != 10; attempt++ {
		candidate, err := os.CreateTemp(directory, "."+base+"-*")
		if err != nil {
			return nil, err
		}
		name := candidate.Name()
		if err := candidate.Close(); err != nil {
			_ = os.Remove(name)
			return nil, err
		}
		if err := os.Remove(name); err != nil {
			return nil, err
		}
		candidate, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err == nil {
			return candidate, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, os.ErrExist
}

func atomicStep(hook AtomicStepHook, step string) error {
	if hook == nil {
		return nil
	}
	return hook(step)
}
