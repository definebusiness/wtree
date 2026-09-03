package fsutil

import (
	"errors"
	"os"
	"path/filepath"
)

// postReplacementError reports a failure after a target has been atomically
// replaced. Callers must retain the new complete generation in that case.
type postReplacementError struct{ Err error }

func (e *postReplacementError) Error() string { return e.Err.Error() }

func (e *postReplacementError) Unwrap() error { return e.Err }

// ReplacementCompleted reports whether err occurred after atomic replacement.
func ReplacementCompleted(err error) bool {
	var post *postReplacementError
	return errors.As(err, &post)
}

// AtomicStepHook is deliberately small so owning packages can retain their
// existing failure-injection seams without each reimplementing the durability
// protocol. A hook is called before the named irreversible step.
type AtomicStepHook func(string) error

type atomicReplaceFunc func(string, string, os.FileInfo) error

func adaptAtomicReplace(replace func(string, string) error) atomicReplaceFunc {
	if replace == nil {
		return atomicReplaceWithInfo
	}
	return func(source, destination string, _ os.FileInfo) error { return replace(source, destination) }
}

// WriteFileAtomicMode durably replaces one regular file while preserving the
// caller-selected mode. The temporary is co-located so rename is atomic.
func WriteFileAtomicMode(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomicModeWithInfo(path, data, mode, nil, atomicReplaceWithInfo, true)
}

// WriteFileAtomicCreateMode durably creates a file using creation permissions
// that remain subject to the process umask.
func WriteFileAtomicCreateMode(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomicModeCreateWithInfo(path, data, mode, nil, atomicReplaceWithInfo, true)
}

// WriteFileAtomicCreateModeWithReplace retains umask-correct creation while
// allowing transaction tests to inject the final replacement boundary.
func WriteFileAtomicCreateModeWithReplace(path string, data []byte, mode os.FileMode, replace func(string, string) error) error {
	return writeFileAtomicModeCreate(path, data, mode, nil, replace)
}

// WriteFileAtomicCreateModeWithHook is the creation variant of
// WriteFileAtomicModeWithHook. The hook may reject a changed target at the
// final replacement boundary while retaining umask-correct creation modes.
func WriteFileAtomicCreateModeWithHook(path string, data []byte, mode os.FileMode, hook AtomicStepHook) error {
	return writeFileAtomicModeCreateWithInfo(path, data, mode, hook, atomicReplaceWithInfo, true)
}

// WriteFileAtomicModeWithHook is WriteFileAtomicMode with test-only step
// observation. The temporary is always created beside the target, flushed and
// closed before replacement, then the containing directory is flushed.
func WriteFileAtomicModeWithHook(path string, data []byte, mode os.FileMode, hook AtomicStepHook) error {
	return writeFileAtomicModeWithInfo(path, data, mode, hook, atomicReplaceWithInfo, true)
}

// WriteFileAtomicModeWithReplace retains the same durability protocol while
// allowing a narrowly injected replacement operation in transaction tests.
func WriteFileAtomicModeWithReplace(path string, data []byte, mode os.FileMode, replace func(string, string) error) error {
	return writeFileAtomicMode(path, data, mode, nil, replace)
}

// writeFileAtomicMode retains the legacy injected replacement seam. Production
// writers use the identity-aware boundary below.
func writeFileAtomicMode(path string, data []byte, mode os.FileMode, hook AtomicStepHook, replace func(string, string) error) error {
	return writeFileAtomicModeWithInfo(path, data, mode, hook, adaptAtomicReplace(replace), false)
}

func writeFileAtomicModeWithInfo(path string, data []byte, mode os.FileMode, hook AtomicStepHook, replace atomicReplaceFunc, platformAuthority bool) error {
	if platformAuthority {
		if handled, err := writeFileAtomicPlatform(path, data, mode, hook); handled {
			return err
		}
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := atomicStep(hook, "create-temp"); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	temporaryInfo, err := temporary.Stat()
	if err != nil {
		temporary.Close()
		return err
	}
	defer func() {
		_ = removeAtomicTemporary(name, temporaryInfo)
	}()
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
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
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := atomicStep(hook, "before-rename"); err != nil {
		return err
	}
	if err := replace(name, path, temporaryInfo); err != nil {
		return err
	}
	if err := atomicStep(hook, "dir-sync"); err != nil {
		return &postReplacementError{Err: err}
	}
	if err := syncDirectory(directory); err != nil {
		return &postReplacementError{Err: err}
	}
	return nil
}

func writeFileAtomicModeCreate(path string, data []byte, mode os.FileMode, hook AtomicStepHook, replace func(string, string) error) error {
	return writeFileAtomicModeCreateWithInfo(path, data, mode, hook, adaptAtomicReplace(replace), false)
}

func writeFileAtomicModeCreateWithInfo(path string, data []byte, mode os.FileMode, hook AtomicStepHook, replace atomicReplaceFunc, platformAuthority bool) error {
	if platformAuthority {
		if handled, err := writeFileAtomicPlatform(path, data, mode, hook); handled {
			return err
		}
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := atomicStep(hook, "create-temp"); err != nil {
		return err
	}
	temporary, err := createTempWithUmask(directory, filepath.Base(path), mode)
	if err != nil {
		return err
	}
	name := temporary.Name()
	temporaryInfo, err := temporary.Stat()
	if err != nil {
		temporary.Close()
		return err
	}
	defer func() {
		_ = removeAtomicTemporary(name, temporaryInfo)
	}()
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
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := atomicStep(hook, "before-rename"); err != nil {
		return err
	}
	if err := replace(name, path, temporaryInfo); err != nil {
		return err
	}
	if err := atomicStep(hook, "dir-sync"); err != nil {
		return &postReplacementError{Err: err}
	}
	if err := syncDirectory(directory); err != nil {
		return &postReplacementError{Err: err}
	}
	return nil
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
