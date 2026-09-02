package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/store"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHookRunnerRequiresGenerationVerifier(t *testing.T) {
	result, err := NewHookRunner().Run(nil, HookRunRequest{})
	if err == nil || result.Status != "incomplete" {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestHookProcessStreamsContextualOutput(t *testing.T) {
	if stream := os.Getenv("WTREE_HOOK_PROCESS_HELPER"); stream != "" {
		switch stream {
		case "stdout":
			fmt.Fprint(os.Stdout, "out\n")
		case "stderr":
			fmt.Fprint(os.Stderr, "err\n")
		case "partial":
			fmt.Fprint(os.Stdout, "https://alice:secret@example.test/api")
		case "fail":
			os.Exit(7)
		case "block":
			time.Sleep(10 * time.Second)
		case "stdout-block":
			fmt.Fprint(os.Stdout, "out\n")
			time.Sleep(10 * time.Second)
		case "closed-output-block":
			_ = os.Stdout.Close()
			_ = os.Stderr.Close()
			time.Sleep(10 * time.Second)
		case "closed-output-marker-block":
			_ = os.WriteFile(os.Getenv("WTREE_HOOK_PROCESS_MARKER"), []byte("started"), 0o600)
			_ = os.Stdout.Close()
			_ = os.Stderr.Close()
			time.Sleep(10 * time.Second)
		case "closed-output-descendant":
			child := exec.Command(os.Args[0], "-test.run=TestHookProcessStreamsContextualOutput")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "WTREE_HOOK_PROCESS_HELPER=") {
					child.Env = append(child.Env, entry)
				}
			}
			child.Env = append(child.Env, "WTREE_HOOK_PROCESS_HELPER=descendant-child")
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			_ = os.Stdout.Close()
			_ = os.Stderr.Close()
			time.Sleep(10 * time.Second)
		case "exit-with-output-child":
			child := exec.Command(os.Args[0], "-test.run=TestHookProcessStreamsContextualOutput")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "WTREE_HOOK_PROCESS_HELPER=") {
					child.Env = append(child.Env, entry)
				}
			}
			child.Env = append(child.Env, "WTREE_HOOK_PROCESS_HELPER=inherited-output-child")
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			_ = os.WriteFile(os.Getenv("WTREE_HOOK_PROCESS_START_MARKER"), []byte("started"), 0o600)
		case "inherited-output-child":
			time.Sleep(1500 * time.Millisecond)
			_ = os.WriteFile(os.Getenv("WTREE_HOOK_PROCESS_MARKER"), []byte("escaped"), 0o600)
		case "descendant":
			child := exec.Command(os.Args[0], "-test.run=TestHookProcessStreamsContextualOutput")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "WTREE_HOOK_PROCESS_HELPER=") {
					child.Env = append(child.Env, entry)
				}
			}
			child.Env = append(child.Env, "WTREE_HOOK_PROCESS_HELPER=descendant-child")
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			time.Sleep(10 * time.Second)
		case "descendant-child":
			time.Sleep(300 * time.Millisecond)
			_ = os.WriteFile(os.Getenv("WTREE_HOOK_PROCESS_MARKER"), []byte("escaped"), 0o600)
		}
		return
	}
	var sink hookTestSink
	for _, stream := range []string{"stdout", "stderr"} {
		result, err := newHookProcessAdapter().Run(context.Background(), HookProcessRequest{Program: os.Args[0], Arguments: []string{"-test.run=TestHookProcessStreamsContextualOutput"}, Directory: mustHookTestDirectory(t), Environment: append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER="+stream), Timeout: 5 * time.Second, Event: "post-create", HookID: "setup", Sink: &sink})
		if err != nil || !result.Started || result.ExitCode != 0 {
			t.Fatalf("Run(%s)=%#v %v", stream, result, err)
		}
	}
	if !strings.Contains(sink.String(), "[wtree hook post-create/setup stdout] out\n") || !strings.Contains(sink.String(), "[wtree hook post-create/setup stderr] err\n") {
		t.Fatalf("sink=%q", sink.String())
	}
}

func TestHookProcessClassifiesOutputTimeoutCancellationAndNonZero(t *testing.T) {
	base := HookProcessRequest{Program: os.Args[0], Arguments: []string{"-test.run=TestHookProcessStreamsContextualOutput"}, Directory: mustHookTestDirectory(t), Timeout: 5 * time.Second, Event: "post-create", HookID: "setup"}
	t.Run("partial-redacted", func(t *testing.T) {
		var sink hookTestSink
		request := base
		request.Sink = &sink
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=partial")
		if _, err := newHookProcessAdapter().Run(context.Background(), request); err != nil || !strings.Contains(sink.String(), "[wtree hook post-create/setup stdout] ") || strings.Contains(sink.String(), "secret") {
			t.Fatalf("Run() err=%v sink=%q", err, sink.String())
		}
	})
	t.Run("writer", func(t *testing.T) {
		request := base
		request.Sink = hookFailingSink{}
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=stdout")
		if _, err := newHookProcessAdapter().Run(context.Background(), request); !errors.Is(err, errHookProcessOutputWriter) {
			t.Fatalf("Run() error=%v", err)
		}
	})
	t.Run("writer-cleanup-escalates-within-bound", func(t *testing.T) {
		attempts := installHookWriterCleanupFailure(t)
		request := base
		request.Sink = hookFailingSink{}
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=stdout-block")
		started := time.Now()
		_, err := newHookProcessAdapter().Run(context.Background(), request)
		if !errors.Is(err, errHookProcessOutputWriter) || attempts() != 2 || time.Since(started) > directProcessCleanupTimeout+500*time.Millisecond {
			t.Fatalf("Run() err=%v attempts=%d elapsed=%s", err, attempts(), time.Since(started))
		}
	})
	t.Run("nonzero", func(t *testing.T) {
		request := base
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=fail")
		result, err := newHookProcessAdapter().Run(context.Background(), request)
		if err != nil || result.ExitCode != 7 {
			t.Fatalf("Run()=%#v %v", result, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		request := base
		request.Timeout = 20 * time.Millisecond
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=block")
		if _, err := newHookProcessAdapter().Run(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run() error=%v", err)
		}
	})
	t.Run("timeout-after-output-eof", func(t *testing.T) {
		request := base
		request.Timeout = 50 * time.Millisecond
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=closed-output-block")
		started := time.Now()
		if _, err := newHookProcessAdapter().Run(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
			t.Fatalf("Run() error=%v elapsed=%s", err, time.Since(started))
		}
	})
	t.Run("parent-cancel-after-output-eof", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "started")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request := base
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=closed-output-marker-block", "WTREE_HOOK_PROCESS_MARKER="+marker)
		result := make(chan error, 1)
		go func() { _, err := newHookProcessAdapter().Run(ctx, request); result <- err }()
		for deadline := time.Now().Add(time.Second); ; time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(marker); err == nil {
				break
			} else if time.Now().After(deadline) {
				t.Fatalf("helper did not start: %v", err)
			}
		}
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() error=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Run did not return after parent cancellation")
		}
	})
	t.Run("timeout-after-output-eof-terminates-descendants", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "escaped")
		request := base
		request.Timeout = 100 * time.Millisecond
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=closed-output-descendant", "WTREE_HOOK_PROCESS_MARKER="+marker)
		if _, err := newHookProcessAdapter().Run(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run() error=%v", err)
		}
		time.Sleep(400 * time.Millisecond)
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("descendant survived closed-output timeout")
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})
	t.Run("timeout-after-leader-exit-terminates-inherited-output-child", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "escaped")
		start := filepath.Join(t.TempDir(), "started")
		request := base
		request.Timeout = 50 * time.Millisecond
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=exit-with-output-child", "WTREE_HOOK_PROCESS_MARKER="+marker, "WTREE_HOOK_PROCESS_START_MARKER="+start)
		if _, err := newHookProcessAdapter().Run(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run() error=%v", err)
		}
		time.Sleep(1600 * time.Millisecond)
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("inherited-output child survived timeout after leader exit")
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})
	t.Run("cancel-after-leader-exit-terminates-inherited-output-child", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "escaped")
		start := filepath.Join(t.TempDir(), "started")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request := base
		request.Timeout = 5 * time.Second
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=exit-with-output-child", "WTREE_HOOK_PROCESS_MARKER="+marker, "WTREE_HOOK_PROCESS_START_MARKER="+start)
		result := make(chan error, 1)
		go func() { _, err := newHookProcessAdapter().Run(ctx, request); result <- err }()
		for deadline := time.Now().Add(time.Second); ; time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(start); err == nil {
				break
			} else if time.Now().After(deadline) {
				t.Fatalf("leader did not start child: %v", err)
			}
		}
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() error=%v", err)
			}
		case <-time.After(directProcessCleanupTimeout + 500*time.Millisecond):
			t.Fatal("Run did not return after cancellation")
		}
		time.Sleep(1600 * time.Millisecond)
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("inherited-output child survived cancellation after leader exit")
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})
	t.Run("parent-cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := base
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=block")
		result, err := newHookProcessAdapter().Run(ctx, request)
		if !errors.Is(err, context.Canceled) || result.Started {
			t.Fatalf("Run()=%#v error=%v", result, err)
		}
	})
	t.Run("timeout-terminates-descendants", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "escaped")
		request := base
		request.Timeout = 100 * time.Millisecond
		request.Environment = append(os.Environ(), "WTREE_HOOK_PROCESS_HELPER=descendant", "WTREE_HOOK_PROCESS_MARKER="+marker)
		if _, err := newHookProcessAdapter().Run(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run() error=%v", err)
		}
		time.Sleep(400 * time.Millisecond)
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("descendant survived hook timeout")
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})
}

type hookFailingSink struct{}

func (hookFailingSink) Write([]byte) (int, error) { return 0, errors.New("sink") }

func TestHookProcessResolveUsesSuppliedPATHAndPATHEXT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool.py")
	if err := os.WriteFile(path, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	fact, err := hookResolveExecutable(context.Background(), HookExecutableRequest{Program: "tool", Directory: dir, Environment: []string{"PATH=" + dir, "PATHEXT=.py"}}, true)
	if err != nil || !fact.Available || fact.Resolved != path {
		t.Fatalf("Resolve()=%#v %v", fact, err)
	}
	if runtime.GOOS == "windows" {
		fact, err = newHookProcessAdapter().Resolve(context.Background(), HookExecutableRequest{Program: "tool", Directory: dir, Environment: []string{"PATH=" + dir, "PATHEXT=.PY"}})
		if err != nil || !fact.Available || fact.Resolved != path {
			t.Fatalf("native Resolve()=%#v %v", fact, err)
		}
	}
}

func TestHookProcessResolvePATHAuthority(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "tool")
	if err := os.WriteFile(path, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if fact, err := hookResolveExecutable(context.Background(), HookExecutableRequest{Program: "tool", Directory: directory, Environment: []string{"PATH=."}}, false); err != nil || !fact.Available || fact.Resolved != path {
		t.Fatalf("relative PATH Resolve()=%#v %v", fact, err)
	}
	if fact, err := hookResolveExecutable(context.Background(), HookExecutableRequest{Program: "tool", Directory: directory, Environment: []string{"PATH="}}, false); err != nil || !fact.Available || fact.Resolved != path {
		t.Fatalf("empty PATH Resolve()=%#v %v", fact, err)
	}
	posixDirectory := t.TempDir()
	py := filepath.Join(posixDirectory, "tool.py")
	if err := os.WriteFile(py, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if fact, err := hookResolveExecutable(context.Background(), HookExecutableRequest{Program: "tool", Directory: posixDirectory, Environment: []string{"PATH=" + posixDirectory, "PATHEXT=.py"}}, false); err != nil || fact.Available {
		t.Fatalf("POSIX PATHEXT Resolve()=%#v %v", fact, err)
	}
	if fact, err := hookResolveExecutable(context.Background(), HookExecutableRequest{Program: "tool", Directory: posixDirectory, Environment: []string{"PATH=" + posixDirectory, "PATHEXT=.PY"}}, true); err != nil || !fact.Available || fact.Resolved != py {
		t.Fatalf("Windows PATHEXT Resolve()=%#v %v", fact, err)
	}
	if runtime.GOOS != "windows" {
		fact, err := newHookProcessAdapter().Resolve(context.Background(), HookExecutableRequest{Program: "tool", Directory: posixDirectory, Environment: []string{"PATH=" + posixDirectory, "PATHEXT=.py"}})
		if err != nil || fact.Available {
			t.Fatalf("native POSIX PATHEXT Resolve()=%#v %v", fact, err)
		}
	}
}

func TestHookProcessResolveWindowsPATHEXTAuthority(t *testing.T) {
	directory := t.TempDir()
	for name := range map[string]struct{}{"tool": {}, "tool.exe": {}, "tool.cmd": {}, "path-tool.exe": {}, "odd.weird": {}} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("x"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name, program string
		environment   []string
		want          string
	}{
		{name: "default absent", program: "tool", environment: []string{"PATH=" + directory}, want: "tool.exe"},
		{name: "default empty", program: "tool", environment: []string{"PATH=" + directory, "PATHEXT="}, want: "tool.exe"},
		{name: "explicit allowed case", program: "TOOL.CMD", environment: []string{"PATH=" + directory, "PATHEXT=.CMD"}, want: "tool.cmd"},
		{name: "explicit unknown exact", program: "odd.weird", environment: []string{"PATH=" + directory, "PATHEXT=.EXE"}, want: "odd.weird"},
		{name: "path extensionless", program: "./path-tool", environment: []string{"PATHEXT=.EXE"}, want: "path-tool.exe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fact, err := hookResolveExecutable(context.Background(), HookExecutableRequest{Program: test.program, Directory: directory, Environment: test.environment}, true)
			if err != nil || !fact.Available || fact.Resolved != filepath.Join(directory, test.want) {
				t.Fatalf("Resolve()=%#v %v", fact, err)
			}
		})
	}
	if fact, err := hookResolveExecutable(context.Background(), HookExecutableRequest{Program: "tool", Directory: directory, Environment: []string{"PATH=" + directory, "PATHEXT=.PY"}}, true); err != nil || fact.Available {
		t.Fatalf("raw extensionless Resolve()=%#v %v", fact, err)
	}
}

func TestHookProcessStreamBoundsFramesAndRejectsReaderErrors(t *testing.T) {
	var sink hookTestSink
	reader := &hookChunkReader{chunks: [][]byte{[]byte("https://alice:"), []byte("secret@example.test/path\nfinal")}}
	if err := streamHookProcessOutput(reader, &hookProcessWriter{sink: &sink}, "[p] "); err != nil {
		t.Fatal(err)
	}
	if got := sink.String(); strings.Contains(got, "secret") || !strings.Contains(got, "[p] https://REDACTED@example.test/path\n[p] final") {
		t.Fatalf("stream=%q", got)
	}
	var large hookTestSink
	if err := streamHookProcessOutput(bytes.NewReader(bytes.Repeat([]byte("x"), directProcessInspectionBytes*2)), &hookProcessWriter{sink: &large}, "[p] "); err != nil || len(large.String()) == 0 {
		t.Fatalf("large err=%v len=%d", err, len(large.String()))
	}
	if err := streamHookProcessOutput(&hookChunkReader{chunks: [][]byte{[]byte("line")}, err: errors.New("read")}, &hookProcessWriter{}, "[p] "); err == nil {
		t.Fatal("accepted reader failure")
	}
}

func TestHookProcessForcedBoundaryNeverLeaksCredentialContinuation(t *testing.T) {
	for _, test := range []string{
		"https://alice:" + strings.Repeat("x", directProcessInspectionBytes) + "secret@example.test/no-newline",
		"prefix?token=" + strings.Repeat("x", directProcessInspectionBytes) + "secret",
		strings.Repeat("x", directProcessInspectionBytes-4) + "?tok" + "en=secret",
		strings.Repeat("x", directProcessInspectionBytes-11) + "https://ali" + "ce:secret@example.test/no-newline",
	} {
		var sink hookTestSink
		if err := streamHookProcessOutput(bytes.NewReader([]byte(test)), &hookProcessWriter{sink: &sink}, "[p] "); err != nil {
			t.Fatal(err)
		}
		if got := sink.String(); strings.Contains(got, "secret") || strings.Contains(got, "alice:") || strings.Contains(got, "token="+strings.Repeat("x", 32)) {
			t.Fatalf("forced boundary leaked secret: %q", got[len(got)-hookTestMin(len(got), 128):])
		}
	}
}

func TestHookProcessForcedBoundaryRedactsNewlineTerminatedContinuations(t *testing.T) {
	for _, test := range []struct{ first, second, secret, safe string }{
		{first: "https://alice:s", second: "ecret@example.test/path\nsafe@ordinary\n", secret: "secret", safe: "[p] safe@ordinary\n"},
		{first: "?token=s", second: "ecret&next=1\nsafe&ordinary\n", secret: "secret", safe: "[p] safe&ordinary\n"},
	} {
		var sink hookTestSink
		reader := &hookBoundaryReader{padding: directProcessInspectionBytes - len(test.first) + 1, first: []byte(test.first), second: []byte(test.second)}
		if err := streamHookProcessOutput(reader, &hookProcessWriter{sink: &sink}, "[p] "); err != nil {
			t.Fatal(err)
		}
		if got := sink.String(); strings.Contains(got, test.secret) || strings.Contains(got, "alice:") {
			t.Fatalf("newline continuation leaked: %q", got)
		}
		if !strings.Contains(sink.String(), test.safe) {
			t.Fatalf("next ordinary line was not framed: %q", sink.String())
		}
	}
}

type hookBoundaryReader struct {
	padding       int
	first, second []byte
	phase         int
}

func (r *hookBoundaryReader) Read(p []byte) (int, error) {
	if r.padding > 0 {
		n := r.padding
		if n > len(p) {
			n = len(p)
		}
		for i := 0; i < n; i++ {
			p[i] = 'x'
		}
		r.padding -= n
		return n, nil
	}
	if r.phase == 0 {
		r.phase++
		return copy(p, r.first), nil
	}
	if r.phase == 1 {
		r.phase++
		return copy(p, r.second), nil
	}
	return 0, io.EOF
}
func hookTestMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type hookChunkReader struct {
	chunks [][]byte
	err    error
	index  int
}

func (r *hookChunkReader) Read(p []byte) (int, error) {
	if r.index < len(r.chunks) {
		n := copy(p, r.chunks[r.index])
		r.index++
		return n, nil
	}
	if r.err != nil {
		e := r.err
		r.err = nil
		return 0, e
	}
	return 0, io.EOF
}

func TestHookRunnerWritesRunningBeforeProcessAndFinalizes(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	var records []store.HookRunRecord
	process := hookTestProcess{resolve: func() {
		if len(records) == 0 || records[0].State != "running" {
			t.Fatal("process started before running record")
		}
	}}
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: process, Clock: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }, Read: func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist }, Write: func(_ string, r store.HookRunRecord) error { records = append(records, r); return nil }, Remove: func(string) error { return nil }})
	result, err := runner.Run(context.Background(), HookRunRequest{DataDir: t.TempDir(), Plan: plan, Revalidate: func(context.Context) (HookGenerationSnapshot, error) {
		return HookGenerationSnapshot{SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state")}, nil
	}})
	if err != nil || result.Status != "completed" || len(result.CompletedIDs) != 1 || len(records) != 2 || records[1].NextIndex != 1 || records[1].State != "finalizing" {
		t.Fatalf("Run=%#v %v records=%#v", result, err, records)
	}
}

func TestHookRunnerDoesNotPublishObsoleteInitialGeneration(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	var writes, resolves int
	runner := NewHookRunnerWith(HookRunnerDependencies{
		Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { resolves++ }},
		Read:  func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist },
		Write: func(string, store.HookRunRecord) error { writes++; return nil },
	})
	result, err := runner.Run(context.Background(), HookRunRequest{DataDir: t.TempDir(), Plan: plan, Revalidate: func(context.Context) (HookGenerationSnapshot, error) {
		return HookGenerationSnapshot{SourceBytes: []byte("changed"), WorkspaceStateBytes: []byte("state")}, nil
	}})
	if err != nil || result.Failure == nil || result.Failure.Kind != HookFailureGeneration || writes != 0 || resolves != 0 {
		t.Fatalf("Run() = %#v, %v; writes=%d resolves=%d", result, err, writes, resolves)
	}
}

func TestHookRunnerPersistsGenerationFailureBeforeLaunch(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	var records []store.HookRunRecord
	var resolves int
	calls := 0
	runner := NewHookRunnerWith(HookRunnerDependencies{
		Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { resolves++ }},
		Clock: fixedHookClock,
		Read:  func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist },
		Write: func(_ string, record store.HookRunRecord) error { records = append(records, record); return nil },
	})
	result, err := runner.Run(context.Background(), HookRunRequest{DataDir: t.TempDir(), Plan: plan, Revalidate: func(context.Context) (HookGenerationSnapshot, error) {
		calls++
		if calls == 1 {
			return hookTestGeneration(), nil
		}
		return HookGenerationSnapshot{SourceBytes: []byte("changed"), WorkspaceStateBytes: []byte("state")}, nil
	}})
	if err != nil || result.Failure == nil || result.Failure.Kind != HookFailureGeneration || resolves != 0 || len(records) != 2 || records[0].State != "running" || records[1].State != "failed" || records[1].Failure == nil || records[1].Failure.Kind != string(HookFailureGeneration) {
		t.Fatalf("Run() = %#v, %v; records=%#v resolves=%d", result, err, records, resolves)
	}
}

func TestHookRunnerPersistsKnownProcessFailures(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	tests := []struct {
		name    string
		process hookTestProcess
		kind    HookFailureKind
	}{
		{name: "missing", process: hookTestProcess{factSet: true, fact: HookExecutableFact{}}, kind: HookFailureMissing},
		{name: "launch", process: hookTestProcess{resolveErr: errors.New("launch")}, kind: HookFailureLaunch},
		{name: "nonzero", process: hookTestProcess{run: HookProcessResult{Started: true, ExitCode: 8}}, kind: HookFailureNonZero},
		{name: "timeout", process: hookTestProcess{runErr: context.DeadlineExceeded}, kind: HookFailureTimeout},
		{name: "canceled", process: hookTestProcess{runErr: context.Canceled}, kind: HookFailureCanceled},
		{name: "output-writer", process: hookTestProcess{runErr: errHookProcessOutputWriter}, kind: HookFailureOutput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var records []store.HookRunRecord
			runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: test.process, Clock: fixedHookClock, Read: func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist }, Write: func(_ string, record store.HookRunRecord) error { records = append(records, record); return nil }, Remove: func(string) error { t.Fatal("Remove called after failure"); return nil }})
			result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
			if err != nil || result.Failure == nil || result.Failure.Kind != test.kind || len(records) != 2 || records[1].State != "failed" || records[1].Failure == nil || records[1].Failure.Kind != string(test.kind) {
				t.Fatalf("Run() = %#v, %v; records=%#v", result, err, records)
			}
			if test.kind == HookFailureNonZero && (records[1].Failure.ExitCode == nil || *records[1].Failure.ExitCode != 8) {
				t.Fatalf("nonzero failure=%#v", records[1].Failure)
			}
			if test.kind == HookFailureTimeout && !records[1].Failure.Timeout {
				t.Fatalf("timeout failure=%#v", records[1].Failure)
			}
		})
	}
}

func TestHookRunnerResumesFailedAndFinalizingRecords(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	failed := hookTestRecord(plan, "failed", 0)
	failed.Failure = &store.HookRunFailure{Kind: string(HookFailureMissing), HookID: "setup", RepositoryID: "root"}
	var failedWrites []store.HookRunRecord
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{}, Clock: fixedHookClock, Read: func(string) (store.HookRunRecord, error) { return failed, nil }, Write: func(_ string, r store.HookRunRecord) error { failedWrites = append(failedWrites, r); return nil }, Remove: func(string) error { return nil }})
	result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Status != "completed" || len(failedWrites) != 2 || failedWrites[0].State != "running" || failedWrites[1].NextIndex != 1 || failedWrites[1].State != "finalizing" {
		t.Fatalf("failed resume=%#v %v writes=%#v", result, err, failedWrites)
	}

	finalizing := hookTestRecord(plan, "finalizing", 1)
	var resolved, removed int
	runner = NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { resolved++ }}, Read: func(string) (store.HookRunRecord, error) { return finalizing, nil }, Remove: func(string) error { removed++; return nil }})
	result, err = runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Status != "completed" || resolved != 0 || removed != 1 {
		t.Fatalf("finalizing resume=%#v %v resolved=%d removed=%d", result, err, resolved, removed)
	}
}

func TestHookRunnerResumeRebuildsUnderLockAndNeverCreatesOnMismatch(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	record := hookTestRecord(plan, "failed", 0)
	record.CompletedHookIDs = []string{}
	record.Failure = &store.HookRunFailure{Kind: string(HookFailureMissing), HookID: "setup", RepositoryID: "root"}
	var runs, writes int
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{runCall: func(HookProcessRequest) { runs++ }}, Read: func(string) (store.HookRunRecord, error) { return record, nil }, Write: func(string, store.HookRunRecord) error { writes++; return nil }, Remove: func(string) error { return nil }})
	result, err := runner.Resume(context.Background(), HookResumeRequest{DataDir: t.TempDir(), ProjectID: "project", WorkspaceID: "default", Event: "post-create", Prepare: func(_ context.Context, received store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
		received.CompletedHookIDs = append(received.CompletedHookIDs, "mutated")
		return plan, func(context.Context) (HookGenerationSnapshot, error) { return hookTestGeneration(), nil }, nil
	}})
	if err != nil || result.Status != "completed" || runs != 1 || writes == 0 {
		t.Fatalf("Resume=%#v err=%v runs=%d writes=%d", result, err, runs, writes)
	}
	if len(record.CompletedHookIDs) != 0 {
		t.Fatal("resume preparer received aliased record")
	}
	for _, test := range []struct {
		name    string
		prepare func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error)
	}{
		{name: "rebuild-error", prepare: func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
			return HookPlan{}, nil, errors.New("stale")
		}},
		{name: "plan-digest", prepare: func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
			return mustHookRunnerPlanWithArguments(t, []string{"first", "second"}, []string{"changed"}), func(context.Context) (HookGenerationSnapshot, error) { return hookTestGeneration(), nil }, nil
		}},
		{name: "ordered-hook-ids", prepare: func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
			return mustHookRunnerPlanEntries(t, "second", "first"), func(context.Context) (HookGenerationSnapshot, error) { return hookTestGeneration(), nil }, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runs, writes = 0, 0
			result, err = runner.Resume(context.Background(), HookResumeRequest{DataDir: t.TempDir(), ProjectID: "project", WorkspaceID: "default", Event: "post-create", Prepare: test.prepare})
			if err != nil || result.Failure == nil || result.Failure.Kind != HookFailureGeneration || runs != 0 || writes != 0 {
				t.Fatalf("mismatched Resume=%#v err=%v runs=%d writes=%d", result, err, runs, writes)
			}
		})
	}
	result, err = runner.Resume(context.Background(), HookResumeRequest{DataDir: t.TempDir(), ProjectID: "project", WorkspaceID: "default", Event: "post-create", Prepare: func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
		return plan, nil, nil
	}})
	if err == nil || result.Failure != nil || runs != 0 || writes != 0 {
		t.Fatalf("nil verifier Resume=%#v err=%v runs=%d writes=%d", result, err, runs, writes)
	}
}

func TestHookRunnerResumeStartsAtDurableNextIndexAndFinalizingCleansOnly(t *testing.T) {
	plan := mustHookRunnerPlanEntries(t, "first", "second")
	record := hookTestRecord(plan, "failed", 1)
	record.Failure = &store.HookRunFailure{Kind: string(HookFailureNonZero), HookID: "second", RepositoryID: "root"}
	var runs []string
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{runCall: func(request HookProcessRequest) { runs = append(runs, request.HookID) }}, Read: func(string) (store.HookRunRecord, error) { return record, nil }, Write: func(string, store.HookRunRecord) error { return nil }, Remove: func(string) error { return nil }})
	result, err := runner.Resume(context.Background(), HookResumeRequest{DataDir: t.TempDir(), ProjectID: "project", WorkspaceID: "default", Event: "post-create", Prepare: func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
		return plan, func(context.Context) (HookGenerationSnapshot, error) { return hookTestGeneration(), nil }, nil
	}})
	if err != nil || result.Status != "completed" || strings.Join(runs, ",") != "second" || strings.Join(result.CompletedIDs, ",") != "first,second" {
		t.Fatalf("resume=%#v err=%v runs=%v", result, err, runs)
	}
	finalizing := hookTestRecord(plan, "finalizing", 2)
	resolved, removed := 0, 0
	runner = NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { resolved++ }}, Read: func(string) (store.HookRunRecord, error) { return finalizing, nil }, Remove: func(string) error { removed++; return nil }})
	result, err = runner.Resume(context.Background(), HookResumeRequest{DataDir: t.TempDir(), ProjectID: "project", WorkspaceID: "default", Event: "post-create", Prepare: func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
		return plan, func(context.Context) (HookGenerationSnapshot, error) { return hookTestGeneration(), nil }, nil
	}})
	if err != nil || result.Status != "completed" || resolved != 0 || removed != 1 {
		t.Fatalf("finalizing resume=%#v err=%v resolved=%d removed=%d", result, err, resolved, removed)
	}
}

func TestHookRunnerResumeRejectsConcurrentGenerationChangeWithoutMutatingRecord(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	data := t.TempDir()
	path, err := store.HookRunRecordPath(data, "project", "default", "post-create")
	if err != nil {
		t.Fatal(err)
	}
	record := hookTestRecord(plan, "failed", 0)
	record.CompletedHookIDs = []string{}
	record.Failure = &store.HookRunFailure{Kind: string(HookFailureMissing), HookID: "setup", RepositoryID: "root"}
	if err := store.WriteHookRunRecord(path, record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	process := &hookProductionResolveRunSpy{}
	result, err := NewHookRunnerWith(HookRunnerDependencies{Process: process}).Resume(context.Background(), HookResumeRequest{DataDir: data, ProjectID: "project", WorkspaceID: "default", Event: "post-create", Prepare: func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
		return plan, func(context.Context) (HookGenerationSnapshot, error) {
			return HookGenerationSnapshot{SourceBytes: []byte("changed"), WorkspaceStateBytes: []byte("state")}, nil
		}, nil
	}})
	if err != nil || result.Failure == nil || result.Failure.Kind != HookFailureGeneration || process.runs != 0 {
		t.Fatalf("Resume=%#v err=%v runs=%d", result, err, process.runs)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("concurrent generation change mutated record: %v\nbefore=%s\nafter=%s", err, before, after)
	}
}

func TestHookRunnerResumePropagatesResolveSentinelsWithoutOutputOrMutation(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	data := t.TempDir()
	path, err := store.HookRunRecordPath(data, "project", "default", "post-create")
	if err != nil {
		t.Fatal(err)
	}
	record := hookTestRecord(plan, "running", 0)
	record.CompletedHookIDs = []string{}
	for _, test := range []struct {
		name string
		want error
	}{
		{name: "canceled", want: context.Canceled},
		{name: "deadline", want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := store.WriteHookRunRecord(path, record); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadHookRunRecord(path); err != nil {
				t.Fatalf("read prepared record: %v", err)
			}
			resolved, runs := 0, 0
			var output bytes.Buffer
			result, resumeErr := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{
				resolve:    func() { resolved++ },
				resolveErr: test.want,
				runCall:    func(HookProcessRequest) { runs++ },
			}}).Resume(context.Background(), HookResumeRequest{
				DataDir: data, ProjectID: "project", WorkspaceID: "default", Event: "post-create", Sink: &output,
				Prepare: func(context.Context, store.HookRunRecord) (HookPlan, HookGenerationVerifier, error) {
					return plan, func(context.Context) (HookGenerationSnapshot, error) { return hookTestGeneration(), nil }, nil
				},
			})
			if !errors.Is(resumeErr, test.want) || result.Failure != nil || resolved != 1 || runs != 0 || output.Len() != 0 {
				var kind HookFailureKind
				if result.Failure != nil {
					kind = result.Failure.Kind
				}
				t.Fatalf("Resume status=%q completed=%v failure=%q err=%v resolved=%d runs=%d output=%q", result.Status, result.CompletedIDs, kind, resumeErr, resolved, runs, output.String())
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("resolve sentinel mutated record: %v\nbefore=%s\nafter=%s", err, before, after)
			}
		})
	}
}

func TestHookRunnerMapsLockAndRecordFailuresWithoutLaunching(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	for _, test := range []struct {
		name string
		deps HookRunnerDependencies
	}{
		{name: "lock", deps: HookRunnerDependencies{Locker: hookFailLocker{}, Process: hookTestProcess{resolve: func() { t.Fatal("process launched") }}}},
		{name: "read", deps: HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { t.Fatal("process launched") }}, Read: func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, errors.New("read") }}},
		{name: "initial-write", deps: HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { t.Fatal("process launched") }}, Read: func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist }, Write: func(string, store.HookRunRecord) error { return errors.New("write") }}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewHookRunnerWith(test.deps).Run(context.Background(), hookTestRunRequest(t, plan))
			want := HookFailureRecord
			if test.name == "lock" {
				want = HookFailureLock
			}
			if err != nil || result.Failure == nil || result.Failure.Kind != want {
				t.Fatalf("Run() = %#v, %v", result, err)
			}
		})
	}
}

func TestHookRunnerAdvancesOnlyAfterDurableSuccess(t *testing.T) {
	plan := mustHookRunnerPlanEntries(t, "first", "second")
	var runs []string
	var writes int
	runner := NewHookRunnerWith(HookRunnerDependencies{
		Locker:  hookTestLocker{},
		Process: hookTestProcess{runCall: func(request HookProcessRequest) { runs = append(runs, request.HookID) }},
		Read:    func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist },
		Write: func(string, store.HookRunRecord) error {
			writes++
			if writes == 2 { // the post-first-hook advancement
				return errors.New("record write")
			}
			return nil
		},
	})
	result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Failure == nil || result.Failure.Kind != HookFailureRecord || strings.Join(runs, ",") != "first" {
		t.Fatalf("Run() = %#v, %v; runs=%v", result, err, runs)
	}
}

func TestHookRunnerRejectsMismatchedRecordWithoutMutation(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	record := hookTestRecord(plan, "running", 0)
	record.SourceSHA256 = strings.Repeat("0", 64)
	var writes, resolves int
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { resolves++ }}, Read: func(string) (store.HookRunRecord, error) { return record, nil }, Write: func(string, store.HookRunRecord) error { writes++; return nil }})
	result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Failure == nil || result.Failure.Kind != HookFailureGeneration || writes != 0 || resolves != 0 {
		t.Fatalf("Run() = %#v, %v; writes=%d resolves=%d", result, err, writes, resolves)
	}
}

func TestHookRunnerFinalizingRemovalFailureIsRecordFailure(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	record := hookTestRecord(plan, "finalizing", 1)
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { t.Fatal("process called") }}, Read: func(string) (store.HookRunRecord, error) { return record, nil }, Remove: func(string) error { return store.ErrHookRunRemovalDurabilityUnconfirmed }})
	result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Status != "incomplete" || result.Failure == nil || result.Failure.Kind != HookFailureRecord {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestHookRunnerRestoresFinalizingAfterRemovalUncertainty(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	record := hookTestRecord(plan, "finalizing", 1)
	var writes []store.HookRunRecord
	var resolves, removes int
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { resolves++ }}, Read: func(string) (store.HookRunRecord, error) { return record, nil }, Write: func(_ string, value store.HookRunRecord) error { writes = append(writes, value); return nil }, Remove: func(string) error { removes++; return store.ErrHookRunRemovalDurabilityUnconfirmed }})
	result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Failure == nil || result.Failure.Kind != HookFailureRecord || resolves != 0 || removes != 1 || len(writes) != 1 || writes[0].State != "finalizing" {
		t.Fatalf("Run=%#v %v writes=%#v resolves=%d removes=%d", result, err, writes, resolves, removes)
	}
}

func TestHookRunnerRestoredFinalizingOnlyCleansOnNextRunner(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	record := hookTestRecord(plan, "finalizing", 1)
	var resolves, removes int
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolve: func() { resolves++ }}, Read: func(string) (store.HookRunRecord, error) { return record, nil }, Remove: func(string) error { removes++; return nil }})
	result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Status != "completed" || resolves != 0 || removes != 1 {
		t.Fatalf("Run=%#v %v resolves=%d removes=%d", result, err, resolves, removes)
	}
}

func TestHookRunnerCompletesVerifiedQuarantinedFinalizingWithoutProcess(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	dataDir := t.TempDir()
	path, err := store.HookRunRecordPath(dataDir, "project", "default", "post-create")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHookRunRecord(path, hookTestRecord(plan, "finalizing", 1)); err != nil {
		t.Fatal(err)
	}
	var resolves, runs int
	runner := NewHookRunnerWith(HookRunnerDependencies{
		Locker: hookTestLocker{},
		Process: hookTestProcess{
			resolve: func() { resolves++ },
			runCall: func(HookProcessRequest) { runs++ },
		},
	})
	request := hookTestRunRequest(t, plan)
	request.DataDir = dataDir

	// RED before the R10c-n2 fix: the redundant store sync translated fsutil's
	// verified quarantine marker into an error, so Run restored finalizing.
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.Status != "completed" || result.Failure != nil || resolves != 0 || runs != 0 {
		t.Fatalf("Run()=%#v %v resolves=%d runs=%d", result, err, resolves, runs)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("finalizing record=%v, want absent", err)
	}
}

func TestHookRunnerSerializesConcurrentSameEvent(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	locker := &hookSerialLocker{}
	var mu sync.Mutex
	stored := false
	var runs int
	process := hookTestProcess{runCall: func(HookProcessRequest) { mu.Lock(); runs++; mu.Unlock() }}
	deps := HookRunnerDependencies{
		Locker: locker, Process: process, Clock: fixedHookClock,
		Read: func(string) (store.HookRunRecord, error) {
			mu.Lock()
			defer mu.Unlock()
			if stored {
				return hookTestRecord(plan, "finalizing", 1), nil
			}
			return store.HookRunRecord{}, os.ErrNotExist
		},
		Write:  func(string, store.HookRunRecord) error { mu.Lock(); stored = true; mu.Unlock(); return nil },
		Remove: func(string) error { return nil },
	}
	runner := NewHookRunnerWith(deps)
	request := HookRunRequest{DataDir: t.TempDir(), Plan: plan, Revalidate: func(context.Context) (HookGenerationSnapshot, error) { return hookTestGeneration(), nil }}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := runner.Run(context.Background(), request); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if runs != 1 {
		t.Fatalf("runs=%d", runs)
	}
}

func TestHookRunnerRejectsOverlappingRunAfterFinalizingRemoval(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	dataDir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var runs int
	process := hookTestProcess{
		runCall: func(HookProcessRequest) {
			mu.Lock()
			runs++
			first := runs == 1
			mu.Unlock()
			if first {
				close(started)
				<-release
			}
		},
	}
	locker := &hookObservedImmediateLocker{attempted: make(chan struct{})}
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: locker, Process: process})
	request := hookTestRunRequest(t, plan)
	request.DataDir = dataDir
	first := make(chan HookRunResult, 1)
	go func() {
		result, err := runner.Run(context.Background(), request)
		if err != nil {
			t.Errorf("first Run() error=%v", err)
		}
		first <- result
	}()
	<-started
	secondResult := make(chan HookRunResult, 1)
	go func() {
		result, err := runner.Run(context.Background(), request)
		if err != nil {
			t.Errorf("second Run() error=%v", err)
		}
		secondResult <- result
	}()
	<-locker.attempted
	close(release)
	if result := <-first; result.Status != "completed" {
		t.Fatalf("first Run()=%#v", result)
	}
	second := <-secondResult
	mu.Lock()
	gotRuns := runs
	mu.Unlock()
	if second.Failure == nil || second.Failure.Kind != HookFailureLock || gotRuns != 1 {
		t.Fatalf("overlap Run()=%#v runs=%d", second, gotRuns)
	}
	path, err := store.HookRunRecordPath(dataDir, "project", "default", "post-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("finalizing record=%v, want absent", err)
	}
}

type hookTestLocker struct{}

func (hookTestLocker) HookRunLock(context.Context, string, string, string, string, time.Duration) (lock.Handle, error) {
	return hookNoopLock{}, nil
}

type hookFailLocker struct{}

func (hookFailLocker) HookRunLock(context.Context, string, string, string, string, time.Duration) (lock.Handle, error) {
	return nil, errors.New("held")
}

type hookSerialLocker struct{ mu sync.Mutex }

func (l *hookSerialLocker) HookRunLock(context.Context, string, string, string, string, time.Duration) (lock.Handle, error) {
	l.mu.Lock()
	return hookUnlock{unlock: l.mu.Unlock}, nil
}

type hookUnlock struct{ unlock func() }

func (h hookUnlock) Unlock() error { h.unlock(); return nil }

// hookObservedImmediateLocker retains the production file-lock path while
// proving the overlapping runner attempted acquisition before the owner
// released its finalizing record.
type hookObservedImmediateLocker struct {
	mu        sync.Mutex
	calls     int
	attempted chan struct{}
}

func (l *hookObservedImmediateLocker) HookRunLock(ctx context.Context, dataDir, projectID, workspaceID, event string, timeout time.Duration) (lock.Handle, error) {
	l.mu.Lock()
	l.calls++
	if l.calls == 2 {
		close(l.attempted)
	}
	l.mu.Unlock()
	return (lock.Manager{}).HookRunLock(ctx, dataDir, projectID, workspaceID, event, timeout)
}

type hookTestProcess struct {
	resolve        func()
	resolveRequest func(HookExecutableRequest)
	runCall        func(HookProcessRequest)
	factSet        bool
	fact           HookExecutableFact
	resolveErr     error
	run            HookProcessResult
	runErr         error
}

func (p hookTestProcess) Resolve(_ context.Context, request HookExecutableRequest) (HookExecutableFact, error) {
	if p.resolve != nil {
		p.resolve()
	}
	if p.resolveRequest != nil {
		p.resolveRequest(request)
	}
	if p.resolveErr != nil {
		return HookExecutableFact{}, p.resolveErr
	}
	if p.factSet {
		return p.fact, nil
	}
	return HookExecutableFact{Resolved: filepath.Join(request.Directory, request.Program), Available: true}, nil
}

func TestHookRunnerRequiresPlannedExecutableAuthority(t *testing.T) {
	plan := mustHookRunnerPlan(t)
	var runs int
	var records []store.HookRunRecord
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{factSet: true, fact: HookExecutableFact{Resolved: hookPlanTestPath("changed", "h"), Available: true}, runCall: func(HookProcessRequest) { runs++ }}, Clock: fixedHookClock, Read: func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist }, Write: func(_ string, r store.HookRunRecord) error { records = append(records, r); return nil }})
	result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Failure == nil || result.Failure.Kind != HookFailureGeneration || runs != 0 || len(records) != 2 || records[1].State != "failed" {
		t.Fatalf("Run=%#v %v runs=%d records=%#v", result, err, runs, records)
	}
}

func TestHookRunnerLocalRelativeExecutableUsesSourceAuthority(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	executable := filepath.Join(source, "hooks", "setup")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: "project", ProjectName: "Project", BaseRepository: "root", WorkspaceID: "default", WorkspaceName: "Default", SourceLogicalRoot: source, TargetLogicalRoot: target, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: []hookPlanInputEntry{{ID: "setup", Repository: "root", SourceRepository: source, TargetRepository: target, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: "hooks/setup", ResolvedExecutable: canonical, Availability: "available", Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	var resolved HookExecutableRequest
	var run HookProcessRequest
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: hookTestProcess{resolveRequest: func(request HookExecutableRequest) { resolved = request }, factSet: true, fact: HookExecutableFact{Resolved: canonical, Available: true}, runCall: func(request HookProcessRequest) { run = request }}, Read: func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist }, Write: func(string, store.HookRunRecord) error { return nil }, Remove: func(string) error { return nil }})
	result, err := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if err != nil || result.Status != "completed" || resolved.Directory != source || run.Program != canonical || run.Directory != target {
		t.Fatalf("Run=%#v %v resolve=%#v run=%#v", result, err, resolved, run)
	}
}

func TestHookRunnerRejectsRetargetedLocalSourceRelativeSymlinkBeforeRun(t *testing.T) {
	source, target, outside := t.TempDir(), t.TempDir(), t.TempDir()
	inside := filepath.Join(source, "hooks", "inside")
	link := filepath.Join(source, "hooks", "setup")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	plan, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: "project", ProjectName: "Project", BaseRepository: "root", WorkspaceID: "default", WorkspaceName: "Default", SourceLogicalRoot: source, TargetLogicalRoot: target, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: []hookPlanInputEntry{{ID: "setup", Repository: "root", SourceRepository: source, TargetRepository: target, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: filepath.Join("hooks", "setup"), ResolvedExecutable: inside, Availability: "available", Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	outsideProgram := filepath.Join(outside, "outside")
	if err := os.WriteFile(outsideProgram, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideProgram, link); err != nil {
		t.Fatal(err)
	}
	process := &hookProductionResolveRunSpy{}
	var records []store.HookRunRecord
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: process, Clock: fixedHookClock, Read: func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist }, Write: func(_ string, record store.HookRunRecord) error { records = append(records, record); return nil }})
	result, runErr := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if runErr != nil || result.Failure == nil || result.Failure.Kind != HookFailureGeneration || process.runs != 0 || len(records) != 2 || records[1].State != "failed" {
		t.Fatalf("retargeted symlink Run=%#v err=%v runs=%d records=%#v", result, runErr, process.runs, records)
	}
}

func TestHookRunnerRejectsRetargetedPortableRelativeSymlinkBeforeRun(t *testing.T) {
	source, target, outside := t.TempDir(), t.TempDir(), t.TempDir()
	target = source
	inside, link := filepath.Join(source, "hooks", "inside"), filepath.Join(source, "hooks", "setup")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	plan, err := newHookPlan(hookPlanInput{Operation: "clone", Source: "portable", Event: "post-clone", Policy: "requires-run-hooks", ProjectID: "project", ProjectName: "Project", BaseRepository: "root", WorkspaceID: "default", WorkspaceName: "Default", SourceLogicalRoot: source, TargetLogicalRoot: target, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: []hookPlanInputEntry{{ID: "setup", Repository: "root", SourceRepository: source, TargetRepository: target, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: "hooks/setup", ResolvedExecutable: inside, Availability: "available", Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	outsideProgram := filepath.Join(outside, "outside")
	if err := os.WriteFile(outsideProgram, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideProgram, link); err != nil {
		t.Fatal(err)
	}
	process := &hookProductionResolveRunSpy{}
	runner := NewHookRunnerWith(HookRunnerDependencies{Locker: hookTestLocker{}, Process: process, Read: func(string) (store.HookRunRecord, error) { return store.HookRunRecord{}, os.ErrNotExist }, Write: func(string, store.HookRunRecord) error { return nil }})
	result, runErr := runner.Run(context.Background(), hookTestRunRequest(t, plan))
	if runErr != nil || result.Failure == nil || result.Failure.Kind != HookFailureGeneration || process.runs != 0 {
		t.Fatalf("Run=%#v err=%v runs=%d", result, runErr, process.runs)
	}
}

type hookProductionResolveRunSpy struct{ runs int }

func (p *hookProductionResolveRunSpy) Resolve(ctx context.Context, request HookExecutableRequest) (HookExecutableFact, error) {
	return newHookProcessAdapter().Resolve(ctx, request)
}

func (p *hookProductionResolveRunSpy) Run(context.Context, HookProcessRequest) (HookProcessResult, error) {
	p.runs++
	return HookProcessResult{Started: true}, nil
}
func (p hookTestProcess) Run(_ context.Context, request HookProcessRequest) (HookProcessResult, error) {
	if p.runCall != nil {
		p.runCall(request)
	}
	if p.run != (HookProcessResult{}) || p.runErr != nil {
		return p.run, p.runErr
	}
	return HookProcessResult{Started: true}, nil
}
func mustHookRunnerPlan(t *testing.T) HookPlan {
	return mustHookRunnerPlanEntries(t, "setup")
}

func mustHookRunnerPlanEntries(t *testing.T, ids ...string) HookPlan {
	return mustHookRunnerPlanWithArguments(t, ids, nil)
}

func mustHookRunnerPlanWithArguments(t *testing.T, ids, arguments []string) HookPlan {
	t.Helper()
	source, target := hookPlanTestPath("source"), hookPlanTestPath("target")
	entries := make([]hookPlanInputEntry, len(ids))
	for i, id := range ids {
		entries[i] = hookPlanInputEntry{ID: id, Repository: "root", SourceRepository: source, TargetRepository: target, Branch: "main", Head: strings.Repeat("a", 40), ConfiguredExecutable: "h", ResolvedExecutable: filepath.Join(target, "h"), Availability: "available", Arguments: append([]string(nil), arguments...), Timeout: time.Second}
	}
	p, err := newHookPlan(hookPlanInput{Operation: "create", Source: "local", Event: "post-create", Policy: "automatic", ProjectID: "project", ProjectName: "Project", BaseRepository: "root", WorkspaceID: "default", WorkspaceName: "Default", SourceLogicalRoot: source, TargetLogicalRoot: target, SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state"), Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fixedHookClock() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func hookTestGeneration() HookGenerationSnapshot {
	return HookGenerationSnapshot{SourceBytes: []byte("source"), WorkspaceStateBytes: []byte("state")}
}

func hookTestRunRequest(t *testing.T, plan HookPlan) HookRunRequest {
	t.Helper()
	return HookRunRequest{DataDir: t.TempDir(), Plan: plan, Revalidate: func(context.Context) (HookGenerationSnapshot, error) { return hookTestGeneration(), nil }}
}

func hookTestRecord(plan HookPlan, state string, next int) store.HookRunRecord {
	a := plan.authority
	record := store.HookRunRecord{Version: store.HookRunRecordVersion, ProjectID: a.projectID, WorkspaceID: a.workspaceID, WorkspaceName: a.workspaceName, Operation: plan.Operation, Event: a.event, Source: a.source, SourceSHA256: plan.SourceSHA256(), PlanSHA256: plan.Digest(), WorkspaceStateSHA256: plan.WorkspaceStateSHA256(), HookIDs: hookPlanIDs(a.entries), CompletedHookIDs: append([]string(nil), hookPlanIDs(a.entries)[:next]...), NextIndex: next, State: state, CreatedAt: fixedHookClock(), UpdatedAt: fixedHookClock()}
	return record
}

type hookTestSink struct {
	mu sync.Mutex
	bytes.Buffer
}

func (s *hookTestSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Buffer.Write(p)
}
func (s *hookTestSink) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.Buffer.String() }
func mustHookTestDirectory(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return d
}
