package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var errHookProcessOutputWriter = errors.New("hook process output writer")
var hookProcessWait = (*exec.Cmd).Wait

type HookProcessRequest struct {
	Program       string
	Arguments     []string
	Directory     string
	Environment   []string
	Timeout       time.Duration
	Event, HookID string
	Sink          io.Writer
}
type HookProcessResult struct {
	ExitCode int
	Started  bool
}
type HookExecutableRequest struct {
	Program     string
	Directory   string
	Environment []string
}
type HookExecutableFact struct {
	Resolved  string
	Available bool
}
type HookProcessAdapter interface {
	Resolve(context.Context, HookExecutableRequest) (HookExecutableFact, error)
	Run(context.Context, HookProcessRequest) (HookProcessResult, error)
}
type hookProcessAdapter struct{}

func newHookProcessAdapter() HookProcessAdapter { return hookProcessAdapter{} }
func (hookProcessAdapter) Resolve(ctx context.Context, r HookExecutableRequest) (HookExecutableFact, error) {
	return hookResolveExecutable(ctx, r, runtime.GOOS == "windows")
}

// hookResolveExecutable is an inspect-only counterpart to the hook launch
// lookup. Its platform argument keeps PATH/PATHEXT rules testable without
// changing the host process environment.
func hookResolveExecutable(ctx context.Context, r HookExecutableRequest, windows bool) (HookExecutableFact, error) {
	if err := ctx.Err(); err != nil {
		return HookExecutableFact{}, err
	}
	directory, err := filepath.Abs(r.Directory)
	if err != nil {
		return HookExecutableFact{}, err
	}
	if strings.ContainsAny(r.Program, "/\\") || filepath.IsAbs(r.Program) {
		p := r.Program
		if windows {
			p = strings.ReplaceAll(p, `\`, string(filepath.Separator))
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(directory, p)
		}
		p = filepath.Clean(p)
		if windows {
			resolved, info, resolveErr := hookProcessWindowsExecutableFile(p, r.Environment)
			return HookExecutableFact{Resolved: resolved, Available: executableHookFileForPlatform(info, resolveErr, true)}, nil
		}
		info, err := os.Stat(p)
		return HookExecutableFact{Resolved: p, Available: executableHookFileForPlatform(info, err, windows)}, nil
	}
	path := ""
	extensions := []string{""}
	for _, e := range r.Environment {
		name, value, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if name == "PATH" || windows && strings.EqualFold(name, "PATH") {
			path = value
		}
		if windows && strings.EqualFold(name, "PATHEXT") {
			// PATHEXT is read below through hookWindowsPATHEXT so absent and
			// empty values use the same default as the management resolver.
		}
	}
	if windows {
		extensions = hookWindowsPATHEXT(r.Environment)
	}
	separator := string(os.PathListSeparator)
	if windows {
		separator = ";"
	}
	for _, d := range strings.Split(path, separator) {
		if d == "" {
			d = directory
		} else if !filepath.IsAbs(d) {
			d = filepath.Join(directory, d)
		}
		if windows {
			resolved, info, resolveErr := hookProcessWindowsExecutableFile(filepath.Join(d, r.Program), r.Environment)
			if executableHookFileForPlatform(info, resolveErr, true) {
				absolute, absErr := filepath.Abs(resolved)
				if absErr != nil {
					return HookExecutableFact{}, absErr
				}
				return HookExecutableFact{Resolved: absolute, Available: true}, nil
			}
			continue
		}
		for _, extension := range extensions {
			p := filepath.Join(d, r.Program+extension)
			resolved, info, err := hookProcessExecutableFile(p, windows)
			if executableHookFileForPlatform(info, err, windows) {
				absolute, absErr := filepath.Abs(resolved)
				if absErr != nil {
					return HookExecutableFact{}, absErr
				}
				return HookExecutableFact{Resolved: absolute, Available: true}, nil
			}
		}
	}
	return HookExecutableFact{Available: false}, nil
}

func hookProcessExecutableFile(path string, windows bool) (string, os.FileInfo, error) {
	if !windows {
		info, err := os.Stat(path)
		return path, info, err
	}
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		return "", nil, readErr
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), filepath.Base(path)) {
			resolved := filepath.Join(filepath.Dir(path), entry.Name())
			info, statErr := os.Stat(resolved)
			return resolved, info, statErr
		}
	}
	return path, nil, os.ErrNotExist
}

// hookProcessWindowsExecutableFile follows os/exec's findExecutable rule:
// a suffixed exact regular file wins, while an extensionless or missing path
// is searched only with effective PATHEXT suffixes.
func hookProcessWindowsExecutableFile(path string, environment []string) (string, os.FileInfo, error) {
	candidates := make([]string, 0, len(hookWindowsPATHEXT(environment))+1)
	if hookProcessHasExtension(path) {
		candidates = append(candidates, path)
	}
	for _, extension := range hookWindowsPATHEXT(environment) {
		candidates = append(candidates, path+extension)
	}
	for _, candidate := range candidates {
		resolved, info, err := hookProcessExecutableFile(candidate, true)
		if err == nil && info.Mode().IsRegular() {
			return resolved, info, nil
		}
	}
	return path, nil, os.ErrNotExist
}

func hookProcessHasExtension(path string) bool {
	dot := strings.LastIndex(path, ".")
	return dot >= 0 && strings.LastIndexAny(path, `:/\\`) < dot
}

func executableHookFileForPlatform(info os.FileInfo, err error, windows bool) bool {
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return windows || info.Mode().Perm()&0o111 != 0
}
func (hookProcessAdapter) Run(ctx context.Context, r HookProcessRequest) (HookProcessResult, error) {
	if r.Timeout <= 0 {
		return HookProcessResult{}, errors.New("invalid hook timeout")
	}
	if err := ctx.Err(); err != nil {
		return HookProcessResult{}, err
	}
	timed, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	c := exec.Command(r.Program, r.Arguments...)
	c.Dir = r.Directory
	c.Env = append([]string(nil), r.Environment...)
	configureHookProcess(c)
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return HookProcessResult{}, err
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return HookProcessResult{}, err
	}
	c.Stdout, c.Stderr = stdoutWriter, stderrWriter
	if err := c.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return HookProcessResult{}, err
	}
	tree, err := beginHookProcessTree(c)
	if err != nil {
		return cleanupFailedHookProcessStart(c, tree, err, stdout, stdoutWriter, stderr, stderrWriter)
	}
	defer tree.Close()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	result := HookProcessResult{Started: true}
	var sink hookProcessWriter
	if r.Sink != nil {
		sink.sink = r.Sink
	}
	output := make(chan error, 2)
	go func() {
		output <- streamHookProcessOutput(stdout, &sink, "[wtree hook "+r.Event+"/"+r.HookID+" stdout] ")
	}()
	go func() {
		output <- streamHookProcessOutput(stderr, &sink, "[wtree hook "+r.Event+"/"+r.HookID+" stderr] ")
	}()
	processDone := make(chan error, 1)
	go func() { processDone <- hookProcessWait(c) }()
	var outputErr error
	finished := 0
	processFinished := false
	var processErr error
	for !processFinished || finished < 2 {
		select {
		case err := <-output:
			finished++
			if err != nil && outputErr == nil {
				outputErr = err
				_ = tree.Terminate()
				return finishHookProcessCleanup(processDone, processFinished, output, finished, stdout, stderr, result, errHookProcessOutputWriter)
			}
		case processErr = <-processDone:
			processFinished = true
			processDone = nil
			if c.ProcessState != nil {
				result.ExitCode = c.ProcessState.ExitCode()
			}
		case <-timed.Done():
			// The direct leader can already have exited while descendants still
			// retain caller-owned output writers. The hook tree remains the
			// authority until both process and streams have completed.
			_ = tree.Terminate()
			return finishHookProcessCleanup(processDone, processFinished, output, finished, stdout, stderr, result, timed.Err())
		}
	}
	if outputErr != nil {
		return result, errHookProcessOutputWriter
	}
	if timed.Err() != nil {
		return result, timed.Err()
	}
	var exit *exec.ExitError
	if errors.As(processErr, &exit) {
		return result, nil
	}
	return result, processErr
}

func cleanupFailedHookProcessStart(command *exec.Cmd, tree hookProcessTree, ownershipErr error, pipes ...io.Closer) (HookProcessResult, error) {
	terminateErr := tree.Terminate()
	var pipeErr error
	for _, pipe := range pipes {
		pipeErr = errors.Join(pipeErr, pipe.Close())
	}
	reapErr := hookProcessWait(command)
	var exitErr *exec.ExitError
	if errors.As(reapErr, &exitErr) {
		reapErr = nil
	}
	closeErr := tree.Close()
	setupErr := errors.Join(terminateErr, pipeErr, reapErr, closeErr)
	if setupErr != nil {
		setupErr = fmt.Errorf("initialize hook process ownership: %w", setupErr)
	}
	if setupErr != nil {
		return HookProcessResult{Started: true}, errors.Join(ownershipErr, setupErr)
	}
	return HookProcessResult{Started: true}, ownershipErr
}

func finishHookProcessCleanup(processDone <-chan error, processFinished bool, output <-chan error, finished int, stdout, stderr io.Closer, result HookProcessResult, outcome error) (HookProcessResult, error) {
	timer := time.NewTimer(directProcessCleanupTimeout)
	defer timer.Stop()
	for !processFinished || finished < 2 {
		select {
		case <-output:
			finished++
		case <-processDone:
			// Wait may return before inherited writers release caller-owned pipes;
			// continue draining until the cleanup bound.
			processFinished = true
			processDone = nil
		case <-timer.C:
			_ = stdout.Close()
			_ = stderr.Close()
			return result, outcome
		}
	}
	return result, outcome
}

type hookProcessWriter struct {
	mu   sync.Mutex
	sink io.Writer
}

func streamHookProcessOutput(reader io.Reader, sink *hookProcessWriter, prefix string) error {
	defer func() {
		if closer, ok := reader.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	buffer := make([]byte, 4096)
	pending := make([]byte, 0, 4096)
	redaction := hookForcedRedactionNone
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			pending = append(pending, buffer[:n]...)
			for {
				index := bytes.IndexByte(pending, '\n')
				if index < 0 {
					break
				}
				value := pending[:index+1]
				if redaction != hookForcedRedactionNone {
					var state hookForcedRedaction
					value, _, state = redactForcedHookChunk(value, redaction)
					_ = state
					// A newline ends a URL authority/query value. Never carry a
					// forced credential state into an unrelated next logical line.
					redaction = hookForcedRedactionNone
				}
				if writeErr := sink.write(prefix, value); writeErr != nil {
					return writeErr
				}
				pending = append(pending[:0], pending[index+1:]...)
			}
			for len(pending) > directProcessInspectionBytes {
				value, next, state := redactForcedHookChunk(pending[:directProcessInspectionBytes], redaction)
				redaction = state
				if writeErr := sink.write(prefix, value); writeErr != nil {
					return writeErr
				}
				pending = append(pending[:0], pending[next:]...)
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(pending) > 0 {
				value := pending
				if redaction != hookForcedRedactionNone {
					value, _, _ = redactForcedHookChunk(pending, redaction)
				}
				return sink.write(prefix, value)
			}
			return nil
		}
		return err
	}
}

type hookForcedRedaction int

const (
	hookForcedRedactionNone hookForcedRedaction = iota
	hookForcedRedactionURL
	hookForcedRedactionQuery
)

// redactForcedHookChunk never emits a credential-shaped suffix that might be
// incomplete solely because the bounded live-stream buffer was forced to
// flush. It intentionally sacrifices that uncertain suffix rather than
// retaining an unbounded line or leaking a continuation on the next flush.
func redactForcedHookChunk(value []byte, state hookForcedRedaction) ([]byte, int, hookForcedRedaction) {
	text := string(value)
	if state != hookForcedRedactionNone {
		terminator := "@"
		if state == hookForcedRedactionQuery {
			terminator = "&"
		}
		if index := strings.Index(text, terminator); index >= 0 {
			return []byte(terminator + text[index+1:]), len(value), hookForcedRedactionNone
		}
		return []byte("[REDACTED]"), len(value), state
	}
	url := strings.LastIndex(text, "://")
	if url >= 0 {
		if strings.Contains(text[url+3:], "@") {
			return []byte(redactDirectProcessVisibleOutput(text)), len(value), hookForcedRedactionNone
		}
		return append([]byte(text[:url+3]), []byte("[REDACTED]")...), len(value), hookForcedRedactionURL
	}
	lower := strings.ToLower(text)
	for _, name := range []string{"token=", "access_token=", "password=", "passwd=", "secret=", "api_key=", "apikey=", "auth=", "authorization="} {
		if index := strings.LastIndex(lower, name); index >= 0 && (index == 0 || text[index-1] == '?' || text[index-1] == '&') {
			if strings.Contains(text[index+len(name):], "&") {
				return []byte(redactDirectProcessVisibleOutput(text)), len(value), hookForcedRedactionNone
			}
			return append([]byte(text[:index+len(name)]), []byte("[REDACTED]")...), len(value), hookForcedRedactionQuery
		}
	}
	// Keep a short, bounded suffix so a sensitive query *name* split exactly
	// across a forced boundary is recognized on the following incremental read.
	const lookbehind = 32
	if len(value) > lookbehind {
		return value[:len(value)-lookbehind], len(value) - lookbehind, hookForcedRedactionNone
	}
	return value, len(value), hookForcedRedactionNone
}
func (s *hookProcessWriter) write(prefix string, value []byte) error {
	if s.sink == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := io.WriteString(s.sink, prefix+redactDirectProcessVisibleOutput(string(value)))
	if err != nil {
		return errHookProcessOutputWriter
	}
	return nil
}
