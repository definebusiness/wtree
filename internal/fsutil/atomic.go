package fsutil

import (
	"bytes"
	"errors"
	"io"
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

// AtomicWriteOutcome describes generations which remain actionable after an
// atomic write returns.  A completed replacement alone is not sufficient to
// establish a clean transaction: conditional exchange can retain a foreign or
// displaced generation at an auxiliary pathname.
type AtomicWriteOutcome struct {
	ReplacementCompleted bool
	AuxiliaryPaths       []string
}

type atomicAuxiliaryError struct {
	Paths []string
	Err   error
}

func (e *atomicAuxiliaryError) Error() string { return e.Err.Error() }
func (e *atomicAuxiliaryError) Unwrap() error { return e.Err }
func (e *atomicAuxiliaryError) AuxiliaryPaths() []string {
	return append([]string(nil), e.Paths...)
}

// AuxiliaryOutcomeError lets an atomic replacement adapter retain every
// actionable generation when it cannot finish cleanup.  It is also the
// portable outcome contract for callers which provide an equivalent writer.
type AuxiliaryOutcomeError struct {
	Paths []string
	Err   error
}

func (e *AuxiliaryOutcomeError) Error() string { return e.Err.Error() }
func (e *AuxiliaryOutcomeError) Unwrap() error { return e.Err }
func (e *AuxiliaryOutcomeError) AuxiliaryPaths() []string {
	return append([]string(nil), e.Paths...)
}

// AtomicOutcome extracts every durable auxiliary pathname reported by the
// platform conditional-replacement implementation.
func AtomicOutcome(err error) AtomicWriteOutcome {
	outcome := AtomicWriteOutcome{ReplacementCompleted: ReplacementCompleted(err)}
	var auxiliary interface{ AuxiliaryPaths() []string }
	if errors.As(err, &auxiliary) {
		outcome.AuxiliaryPaths = append([]string(nil), auxiliary.AuxiliaryPaths()...)
	} else {
		var internal *atomicAuxiliaryError
		if errors.As(err, &internal) {
			outcome.AuxiliaryPaths = append([]string(nil), internal.Paths...)
		}
	}
	if len(outcome.AuxiliaryPaths) != 0 {
		outcome.ReplacementCompleted = true
	}
	return outcome
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

// WriteFileAtomicModeExpected replaces an existing regular destination only
// when the atomically displaced generation still has the expected filesystem
// identity and exact bytes. Platforms without an atomic exchange primitive
// reject the operation rather than falling back to an overwrite race.
func WriteFileAtomicModeExpected(path string, data []byte, mode os.FileMode, expected os.FileInfo, expectedData []byte) error {
	if expected == nil {
		return errors.New("expected destination identity is required")
	}
	// Windows resolves os.FileInfo identity lazily from its original pathname.
	// Bind it before the conditional exchange can move that pathname.
	if !os.SameFile(expected, expected) {
		return errors.New("expected destination identity is unavailable")
	}
	data = append([]byte(nil), data...)
	expectedData = append([]byte(nil), expectedData...)
	return writeFileAtomicModeWithInfo(path, data, mode, nil, func(source, destination string, temporary os.FileInfo) error {
		return replaceExpectedAtomic(source, destination, temporary, expected, data, expectedData)
	}, false)
}

// validateExpectedAtomicGeneration binds identity and exact bytes after the
// conditional exchange has moved a generation to a private pathname. The
// repeated observations fail closed if that displaced file changes while it
// is being inspected.
func validateExpectedAtomicGeneration(path string, expected os.FileInfo, expectedData []byte) (os.FileInfo, error) {
	first, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !first.Mode().IsRegular() || !os.SameFile(first, first) || !os.SameFile(expected, first) || first.Mode() != expected.Mode() {
		return nil, errors.New("displaced generation identity or mode differs from expected")
	}
	firstData, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	middle, err := os.Lstat(path)
	if err != nil || !os.SameFile(middle, middle) || !os.SameFile(first, middle) || middle.Mode() != first.Mode() {
		return nil, errors.Join(errors.New("displaced generation changed during inspection"), err)
	}
	secondData, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	last, err := os.Lstat(path)
	if err != nil || !os.SameFile(last, last) || !os.SameFile(first, last) || last.Mode() != first.Mode() {
		return nil, errors.Join(errors.New("displaced generation changed during inspection"), err)
	}
	if !bytes.Equal(firstData, expectedData) || !bytes.Equal(secondData, expectedData) {
		return nil, errors.New("displaced generation content differs from expected")
	}
	return last, nil
}

func validateExpectedAtomicFile(file *os.File, expected os.FileInfo, expectedData []byte) (os.FileInfo, error) {
	if file == nil || expected == nil {
		return nil, errors.New("expected file generation is unavailable")
	}
	first, err := file.Stat()
	if err != nil || !first.Mode().IsRegular() || !os.SameFile(first, first) || !os.SameFile(expected, first) || first.Mode() != expected.Mode() {
		return nil, errors.Join(errors.New("file generation identity or mode differs from expected"), err)
	}
	read := func() ([]byte, error) {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return io.ReadAll(file)
	}
	firstData, err := read()
	if err != nil {
		return nil, err
	}
	middle, err := file.Stat()
	if err != nil || !os.SameFile(first, middle) || middle.Mode() != first.Mode() {
		return nil, errors.Join(errors.New("file generation changed during inspection"), err)
	}
	secondData, err := read()
	if err != nil {
		return nil, err
	}
	last, err := file.Stat()
	if err != nil || !os.SameFile(first, last) || last.Mode() != first.Mode() {
		return nil, errors.Join(errors.New("file generation changed during inspection"), err)
	}
	if !bytes.Equal(firstData, expectedData) || !bytes.Equal(secondData, expectedData) {
		return nil, errors.New("file generation content differs from expected")
	}
	return last, nil
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

// WriteFileAtomicCreateModeNoReplaceWithHook creates path only if it remains
// absent at the final publication boundary. Unlike the historical create
// writer, it never falls back to an unconditional replacement: unsupported
// platforms and filesystems fail closed.
func WriteFileAtomicCreateModeNoReplaceWithHook(path string, data []byte, mode os.FileMode, hook AtomicStepHook) error {
	return WriteFileAtomicCreateModeNoReplaceWithOwnedTempHook(path, data, mode, hook, nil)
}

// WriteFileAtomicCreateModeNoReplaceWithOwnedTempHook additionally exposes the
// exact co-located temporary generation at the final boundary. Callers may
// distinguish that one known untracked path from foreign working-tree dirt.
func WriteFileAtomicCreateModeNoReplaceWithOwnedTempHook(path string, data []byte, mode os.FileMode, hook AtomicStepHook, final func(string, os.FileInfo) error) error {
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
	info, err := temporary.Stat()
	if err != nil {
		temporary.Close()
		return err
	}
	defer func() { _ = removeAtomicTemporary(name, info) }()
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
	current, err := os.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return errors.Join(errors.New("atomic temporary identity changed before no-replace publication"), err)
	}
	if final != nil {
		if err := final(name, info); err != nil {
			return err
		}
	}
	if err := RenameNoReplace(name, path); err != nil {
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

func writeFileAtomicModeWithInfo(path string, data []byte, mode os.FileMode, hook AtomicStepHook, replace atomicReplaceFunc, platformAuthority bool) (err error) {
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
		// A failed conditional exchange can leave the intervening destination
		// generation at name. It is a recovery artifact, not writer-owned
		// temporary data, and must never be removed by this generic cleanup.
		if !preserveAtomicTemporary(err) {
			_ = removeAtomicTemporary(name, temporaryInfo)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	// Refresh the writer receipt after chmod so a restored writer generation
	// can be validated against the mode that will actually be published.
	temporaryInfo, err = temporary.Stat()
	if err != nil {
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
