package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAggregateFactsAreParentFirstAndDefensive(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "root")
	childPath := filepath.Join(rootPath, "child")
	facts, err := NewAggregateFacts([]AggregateRepositoryFact{
		{ID: "root", Mount: ".", Path: rootPath, Branch: "main", Head: aggregateTestObjectID, Status: AggregateStatusPlanned},
		{ID: "child", ParentID: "root", Mount: "child", Path: childPath, ObservedCommit: aggregateTestObjectID, Status: AggregateStatusCompleted},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := facts.Repositories(); len(got) != 2 || got[0].ID != "root" || got[1].ParentID != "root" {
		t.Fatalf("Repositories() = %#v, want deterministic parent-first copy", got)
	}
	copy := facts.Repositories()
	copy[0].ID = "mutated"
	if facts.Repositories()[0].ID != "root" {
		t.Fatal("Repositories() exposed mutable aggregate state")
	}
	failure := &AggregateFailure{Code: ErrorGit, Message: "safe"}
	owned, err := NewAggregateFacts([]AggregateRepositoryFact{{ID: "root", Mount: ".", Path: rootPath, Status: AggregateStatusFailed, Failure: failure}})
	if err != nil {
		t.Fatal(err)
	}
	failure.Message = "https://user:aggregate-secret-canary@example.invalid"
	returned := owned.Repositories()
	returned[0].Failure.Message = "mutated"
	if got := owned.Repositories()[0].Failure.Message; got != "safe" {
		t.Fatalf("AggregateFacts retained mutable failure pointer %q", got)
	}

	for name, input := range map[string][]AggregateRepositoryFact{
		"duplicate":            {{ID: "root", Mount: ".", Path: rootPath, Status: AggregateStatusPlanned}, {ID: "root", Mount: "other", Path: childPath, Status: AggregateStatusPlanned}},
		"child before parent":  {{ID: "child", ParentID: "root", Mount: "child", Path: childPath, Status: AggregateStatusPlanned}, {ID: "root", Mount: ".", Path: rootPath, Status: AggregateStatusPlanned}},
		"missing path":         {{ID: "root", Mount: ".", Status: AggregateStatusPlanned}},
		"failure missing":      {{ID: "root", Mount: ".", Path: rootPath, Status: AggregateStatusFailed}},
		"failure on completed": {{ID: "root", Mount: ".", Path: rootPath, Status: AggregateStatusCompleted, Failure: &AggregateFailure{Code: ErrorGit, Message: "nope"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAggregateFacts(input); err == nil {
				t.Fatal("NewAggregateFacts() error = nil")
			}
		})
	}
}

func TestAggregateFailureRedactsAndBoundsDiagnostics(t *testing.T) {
	secret := "aggregate-secret-canary"
	failure, err := NewAggregateFailure(ErrorGit, fmt.Errorf("fetch https://user:%s@example.invalid/repo?token=%s %s", secret, secret, strings.Repeat("x", 9000)))
	if err != nil || strings.Contains(failure.Message, secret) || len(failure.Message) > 8195 {
		t.Fatalf("NewAggregateFailure() = %#v, %v", failure, err)
	}
	path := filepath.Join(t.TempDir(), "root")
	if _, err := NewAggregateFacts([]AggregateRepositoryFact{{ID: "root", Mount: ".", Path: path, Status: AggregateStatusFailed, Failure: &AggregateFailure{Code: ErrorGit, Message: "https://user:" + secret + "@example.invalid"}}}); err == nil {
		t.Fatal("NewAggregateFacts() accepted secret-shaped diagnostic")
	}
}

func TestDirectProcessRunsWithoutShellAndBoundsStreams(t *testing.T) {
	if directProcessHelper() {
		return
	}
	directory := t.TempDir()
	request := DirectProcessRequest{
		Program: os.Args[0], Args: []string{"-test.run=^TestDirectProcessHelper$"}, Directory: directory,
		Environment: directProcessEnvironment(t, t.TempDir()),
	}
	result, err := RunDirectProcess(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.StdoutTruncated || result.StderrTruncated || !strings.Contains(result.Stdout, "-test.run=^TestDirectProcessHelper$") || !strings.Contains(result.Stdout, directory) || !strings.Contains(result.Stdout, "HOME=\"\"") || strings.Contains(result.Stdout, "must-not-pass") || result.Stderr != "helper stderr" {
		t.Fatalf("RunDirectProcess() = %#v", result)
	}

	marker := filepath.Join(t.TempDir(), "shell-marker")
	_, err = RunDirectProcess(context.Background(), DirectProcessRequest{Program: "literal;touch " + marker, Directory: directory, Environment: request.Environment})
	if err == nil {
		t.Fatal("RunDirectProcess() started shell-shaped program")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("shell-shaped program created marker: %v", statErr)
	}

	limited, err := RunDirectProcess(context.Background(), DirectProcessRequest{Program: os.Args[0], Args: []string{"-test.run=^TestDirectProcessLargeOutputHelper$"}, Directory: directory, Environment: request.Environment})
	if err != nil || !limited.StdoutTruncated || !limited.StderrTruncated || limited.Stdout != strings.Repeat("o", directProcessRetainedStreamBytes)+directProcessTruncationMarker+strings.Repeat("O", directProcessRetainedStreamBytes) || limited.Stderr != strings.Repeat("e", directProcessRetainedStreamBytes)+directProcessTruncationMarker+strings.Repeat("E", directProcessRetainedStreamBytes) {
		t.Fatalf("bounded RunDirectProcess() = %#v, %v", limited, err)
	}
	failed, err := RunDirectProcess(context.Background(), DirectProcessRequest{Program: os.Args[0], Args: []string{"-test.run=^TestDirectProcessExitHelper$"}, Directory: directory, Environment: request.Environment})
	if err != nil || failed.ExitCode != 7 {
		t.Fatalf("failed RunDirectProcess() = %#v, %v", failed, err)
	}
	redacted, err := RunDirectProcess(context.Background(), DirectProcessRequest{Program: os.Args[0], Args: []string{"-test.run=^TestDirectProcessSecretOutputHelper$"}, Directory: directory, Environment: request.Environment})
	if err != nil || strings.Contains(redacted.Stdout, "process-secret-canary") || !strings.Contains(redacted.Stdout, "REDACTED") {
		t.Fatalf("redacted RunDirectProcess() = %#v, %v", redacted, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunDirectProcess(ctx, request); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("RunDirectProcess(cancelled) error = %v", err)
	}
}

func TestDirectProcessEnvironmentIsExactAndRejectsUnverifiedWTREEKeys(t *testing.T) {
	environment := directProcessEnvironment(t, t.TempDir())
	got, err := sanitizedDirectProcessEnvironment(environment)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PATH=" + os.Getenv("PATH"), "TMPDIR=" + environmentValue(t, environment, "TMPDIR"),
		"HOME=", "XDG_CONFIG_HOME=", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_ATTR_NOSYSTEM=1", "LC_ALL=C", "LANG=C",
		"WTREE_PROJECT_ID=project", "WTREE_WORKSPACE=workspace", "WTREE_REPOSITORY_ID=repository", "WTREE_MOUNT=child", "WTREE_PATH=/workspace/child", "WTREE_BRANCH=main", "WTREE_COMMIT=" + aggregateTestObjectID,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizedDirectProcessEnvironment() = %#v, want %#v", got, want)
	}
	for _, environment := range [][]string{append(environment, "WTREE_UNSAFE=value"), append(environment, "WTREE_PROJECT_ID=duplicate")} {
		if _, err := sanitizedDirectProcessEnvironment(environment); err == nil {
			t.Fatalf("sanitizedDirectProcessEnvironment(%#v) accepted unverified or duplicate WTREE key", environment)
		}
	}
}

func TestDirectProcessCancellationKillsDescendants(t *testing.T) {
	if directProcessNestedHelper() {
		return
	}
	directory := t.TempDir()
	markerDirectory := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	originalTerminate := directProcessTerminate
	terminationCalls := 0
	directProcessTerminate = func(command *exec.Cmd) error {
		terminationCalls++
		if terminationCalls == 1 {
			return errors.New("injected first termination failure")
		}
		return originalTerminate(command)
	}
	defer func() { directProcessTerminate = originalTerminate }()
	started := time.Now()
	_, err := RunDirectProcess(ctx, DirectProcessRequest{Program: os.Args[0], Args: []string{"-test.run=^TestDirectProcessDescendantHelper$"}, Directory: directory, Environment: directProcessEnvironment(t, markerDirectory)})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("RunDirectProcess() cancellation = %v after %s", err, time.Since(started))
	}
	if terminationCalls < 2 {
		t.Fatalf("RunDirectProcess() did not escalate failed termination: %d calls", terminationCalls)
	}
	time.Sleep(500 * time.Millisecond)
	if _, statErr := os.Stat(filepath.Join(markerDirectory, "descendant-marker")); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled descendant continued side effect: %v", statErr)
	}
}

func TestDirectProcessDrainsDelayedAndInheritedTrailingOutput(t *testing.T) {
	if directProcessNestedHelper() {
		return
	}
	request := DirectProcessRequest{
		Program: os.Args[0], Args: []string{"-test.run=^TestDirectProcessTrailingOutputHelper$"},
		Directory: t.TempDir(), Environment: directProcessEnvironment(t, t.TempDir()),
	}
	result, err := RunDirectProcess(context.Background(), request)
	if err != nil || !strings.Contains(result.Stdout, "stdout-before\nstdout-after\n") || result.Stderr != "stderr-before\nstderr-after\n" {
		t.Fatalf("RunDirectProcess(trailing output) = %#v, %v", result, err)
	}

	request.Args = []string{"-test.run=^TestDirectProcessInheritedPipeParentHelper$"}
	result, err = RunDirectProcess(context.Background(), request)
	if err != nil || !strings.Contains(result.Stdout, "parent\n") || !strings.Contains(result.Stdout, "child-stdout\n") || result.Stderr != "child-stderr\n" {
		t.Fatalf("RunDirectProcess(inherited pipes) = %#v, %v", result, err)
	}
}

func TestDirectProcessReportsTwoTerminationFailuresWithoutLeakingSideEffects(t *testing.T) {
	if directProcessNestedHelper() {
		return
	}
	markerDirectory := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	originalTerminate := directProcessTerminate
	terminationCalls := 0
	directProcessTerminate = func(*exec.Cmd) error {
		terminationCalls++
		return errors.New("injected termination failure")
	}
	defer func() { directProcessTerminate = originalTerminate }()
	started := time.Now()
	_, err := RunDirectProcess(ctx, DirectProcessRequest{
		Program: os.Args[0], Args: []string{"-test.run=^TestDirectProcessDescendantHelper$"},
		Directory: t.TempDir(), Environment: directProcessEnvironment(t, markerDirectory),
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "final force termination succeeded") {
		t.Fatalf("RunDirectProcess() cleanup error = %v", err)
	}
	if terminationCalls != 2 || time.Since(started) > 2*time.Second {
		t.Fatalf("termination calls/duration = %d/%s", terminationCalls, time.Since(started))
	}
	time.Sleep(500 * time.Millisecond)
	if _, statErr := os.Stat(filepath.Join(markerDirectory, "descendant-marker")); !os.IsNotExist(statErr) {
		t.Fatalf("force-terminated process tree continued side effect: %v", statErr)
	}
}

func TestDirectProcessHelper(t *testing.T) {
	if !directProcessHelper() {
		t.Skip("helper process only")
	}
}

func TestDirectProcessLargeOutputHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessLargeOutputHelper") {
		t.Skip("helper process only")
	}
	fmt.Fprint(os.Stdout, strings.Repeat("o", directProcessRetainedStreamBytes+3)+strings.Repeat("O", directProcessRetainedStreamBytes))
	fmt.Fprint(os.Stderr, strings.Repeat("e", directProcessRetainedStreamBytes+3)+strings.Repeat("E", directProcessRetainedStreamBytes))
	os.Exit(0)
}

func TestDirectProcessExitHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessExitHelper") {
		t.Skip("helper process only")
	}
	os.Exit(7)
}

func TestDirectProcessSecretOutputHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessSecretOutputHelper") {
		t.Skip("helper process only")
	}
	fmt.Fprint(os.Stdout, "https://user:process-secret-canary@example.invalid/repository?token=process-secret-canary")
}

func TestDirectProcessDescendantHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessDescendantHelper") {
		t.Skip("helper process only")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestDirectProcessDelayedWriteHelper$")
	child.Env = os.Environ()
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		panic(err)
	}
	time.Sleep(5 * time.Second)
}

func TestBoundedProcessStreamRetainsExactBoundarySizes(t *testing.T) {
	for _, size := range []int{directProcessRetainedStreamBytes, directProcessRetainedStreamBytes + 1, directProcessInspectionBytes, directProcessInspectionBytes + 1, directProcessInspectionBytes * 2} {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			stream := &boundedProcessStream{}
			value := strings.Repeat("x", size)
			for _, chunk := range splitDirectProcessBytes([]byte(value), 7, 4099, 3, 8191) {
				if _, err := stream.Write(chunk); err != nil {
					t.Fatal(err)
				}
			}
			if got, wantTruncated := stream.Truncated(), size > directProcessInspectionBytes; got != wantTruncated {
				t.Fatalf("Truncated() = %t, want %t", got, wantTruncated)
			}
			want := value
			if size > directProcessInspectionBytes {
				want = strings.Repeat("x", directProcessRetainedStreamBytes) + directProcessTruncationMarker + strings.Repeat("x", directProcessRetainedStreamBytes)
			}
			if got := stream.String(); got != want {
				t.Fatalf("String() length = %d, want %d", len(got), len(want))
			}
		})
	}
}

func TestBoundedProcessStreamReconstructsOverlappingWindowsByOffsets(t *testing.T) {
	for _, size := range []int{directProcessInspectionBytes + 1, directProcessInspectionBytes + 37, directProcessInspectionBytes + directProcessRetainedStreamBytes, directProcessInspectionBytes * 2} {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			value := bytes.Repeat([]byte("repeat-"), size/7+1)[:size]
			stream := &boundedProcessStream{}
			for _, chunk := range splitDirectProcessBytes(value, 1, 3, 57, 8193) {
				if _, err := stream.Write(chunk); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := stream.snapshot()
			if got := snapshot.reconstruct(); !bytes.Equal(got, value) {
				t.Fatalf("offset reconstruction differed for repeated bytes at %d", size)
			}
			want := string(value[:directProcessRetainedStreamBytes]) + directProcessTruncationMarker + string(value[len(value)-directProcessRetainedStreamBytes:])
			if got := redactDirectProcessOutput(stream); got != want {
				t.Fatalf("rendered reconstruction differs at %d", size)
			}
		})
	}
}

func TestBoundedProcessStreamRedactsCompleteAndChunkSplitCredentials(t *testing.T) {
	for _, value := range []string{
		"https://alice:12345@example.invalid/path",
		"https://alice@example.invalid/path",
		"report?token=visible-secret&view=compact",
		"report?PASSWORD=visible-secret#anchor",
	} {
		stream := &boundedProcessStream{}
		for _, chunk := range splitDirectProcessBytes([]byte(value), 1, 2, 1, 3) {
			_, _ = stream.Write(chunk)
		}
		got := redactDirectProcessOutput(stream)
		if strings.Contains(got, "alice") || strings.Contains(got, "12345") || strings.Contains(got, "visible-secret") || !strings.Contains(got, "REDACTED") {
			t.Fatalf("complete/chunked redaction leaked %q as %q", value, got)
		}
	}
}

func TestBoundedProcessStreamRedactsInspectionCutsAndRealGaps(t *testing.T) {
	credential := "gap-user:987654-secret"
	head := strings.Repeat("h", directProcessInspectionBytes-len(" https://")-len(credential)) + " https://" + credential
	tail := "@example.invalid/path" + strings.Repeat("t", directProcessInspectionBytes-len("@example.invalid/path"))
	stream := &boundedProcessStream{}
	for _, chunk := range splitDirectProcessBytes([]byte(head+strings.Repeat("m", directProcessInspectionBytes+19)+tail), 1, 17, 4097, 5) {
		_, _ = stream.Write(chunk)
	}
	got := redactDirectProcessOutput(stream)
	if strings.Contains(got, credential) {
		t.Fatalf("gap userinfo leaked: prefix=%q suffix=%q", got[:128], got[len(got)-128:])
	}
	snapshot := stream.snapshot()
	if len(snapshot.headSpans) == 0 {
		t.Fatal("scanner did not retain bounded evidence for a credential crossing inspection windows")
	}

	queryHeadSecret, queryTailSecret := "head-token-secret", "tail-token-secret"
	queryPrefix := directProcessRetainedStreamBytes - len("?token=") - 11
	queryHead := strings.Repeat("q", queryPrefix) + "?token=" + queryHeadSecret + strings.Repeat("q", directProcessInspectionBytes-queryPrefix-len("?token=")-len(queryHeadSecret))
	queryTail := queryTailSecret + "&page=2" + strings.Repeat("z", directProcessInspectionBytes-len(queryTailSecret)-len("&page=2"))
	stream = &boundedProcessStream{}
	for _, chunk := range splitDirectProcessBytes([]byte(queryHead+strings.Repeat("g", directProcessInspectionBytes+7)+queryTail), 4093, 1, 13) {
		_, _ = stream.Write(chunk)
	}
	got = redactDirectProcessOutput(stream)
	if strings.Contains(got, queryHeadSecret) || strings.Contains(got, queryTailSecret) || !strings.Contains(got, "?token=[REDACTED]") {
		t.Fatalf("gap query leaked")
	}

	tailOnlySecret := "tail-final-secret"
	tailOnly := strings.Repeat("u", directProcessInspectionBytes-len("?token=")-len(tailOnlySecret)) + "?token=" + tailOnlySecret
	stream = &boundedProcessStream{}
	for _, chunk := range splitDirectProcessBytes([]byte(strings.Repeat("p", directProcessInspectionBytes*2+3)+tailOnly), 1, 61, 4099) {
		_, _ = stream.Write(chunk)
	}
	got = redactDirectProcessOutput(stream)
	if strings.Contains(got, tailOnlySecret) || !strings.Contains(got, "[REDACTED]") {
		t.Fatal("final 32 KiB query cut leaked")
	}
}

func TestBoundedProcessStreamAvoidsArtificialGapAdjacencyAndBoundsMemory(t *testing.T) {
	headPrefix := "https://example.invalid:8443/path?view=compact, punctuation"
	tailSuffix := "@ordinary-text?view=compact"
	head := headPrefix + strings.Repeat("h", directProcessInspectionBytes-len(headPrefix))
	tail := strings.Repeat("t", directProcessInspectionBytes-len(tailSuffix)) + tailSuffix
	stream := &boundedProcessStream{}
	value := head + strings.Repeat("?token=x&", 200000) + tail
	for _, chunk := range splitDirectProcessBytes([]byte(value), 31, 4099, 7) {
		_, _ = stream.Write(chunk)
	}
	got := redactDirectProcessOutput(stream)
	want := head[:directProcessRetainedStreamBytes] + directProcessTruncationMarker + tail[len(tail)-directProcessRetainedStreamBytes:]
	if got != want {
		t.Fatalf("invented redaction across genuine gap")
	}
	snapshot := stream.snapshot()
	if len(snapshot.head) > directProcessInspectionBytes || len(snapshot.tail) > directProcessInspectionBytes || len(snapshot.headSpans) > directProcessInspectionBytes || len(snapshot.tailSpans) > directProcessInspectionBytes {
		t.Fatalf("unbounded retained state: %#v", snapshot)
	}
	stderr := &boundedProcessStream{}
	_, _ = stderr.Write([]byte("stderr?token=stderr-secret"))
	if strings.Contains(redactDirectProcessOutput(stderr), "stderr-secret") || strings.Contains(got, "stderr-secret") {
		t.Fatal("streams did not redact independently")
	}
}

func TestBoundedProcessStreamPreservesOrdinaryVisibleURLsAndQueries(t *testing.T) {
	value := "https://example.invalid:8443/path@ordinary-text?view=compact&sort=name, punctuation!"
	stream := &boundedProcessStream{}
	for _, chunk := range splitDirectProcessBytes([]byte(value), 2, 1, 7) {
		_, _ = stream.Write(chunk)
	}
	if got := redactDirectProcessOutput(stream); got != value {
		t.Fatalf("ordinary visible URL/query changed: %q", got)
	}
}

func TestBoundedProcessStreamStreamingUserinfoMatchesVisibleRedaction(t *testing.T) {
	for _, credential := range []string{"alice?visible-secret", "alice#visible-secret"} {
		t.Run(credential[:6], func(t *testing.T) {
			complete := "https://" + credential + "@example.invalid/path"
			completeStream := &boundedProcessStream{}
			for _, chunk := range splitDirectProcessBytes([]byte(complete), 1, 3, 2, 7) {
				_, _ = completeStream.Write(chunk)
			}
			if got, want := redactDirectProcessOutput(completeStream), redactDirectProcessVisibleOutput(complete); got != want {
				t.Fatalf("reconstructable redaction = %q, want %q", got, want)
			}

			for _, location := range []string{"head", "tail", "gap"} {
				t.Run(location, func(t *testing.T) {
					stream := &boundedProcessStream{}
					var value string
					switch location {
					case "head":
						value = complete + strings.Repeat("h", directProcessInspectionBytes-len(complete)) + strings.Repeat("m", directProcessInspectionBytes+1) + strings.Repeat("t", directProcessInspectionBytes)
					case "tail":
						value = strings.Repeat("h", directProcessInspectionBytes) + strings.Repeat("m", directProcessInspectionBytes+1) + strings.Repeat("t", directProcessInspectionBytes-len(complete)) + complete
					case "gap":
						prefix := "https://" + credential
						tail := "@example.invalid/path"
						value = prefix + strings.Repeat("h", directProcessInspectionBytes-len(prefix)) + strings.Repeat("m", directProcessInspectionBytes+1) + strings.Repeat("t", directProcessInspectionBytes-len(tail)) + tail
					}
					for _, chunk := range splitDirectProcessBytes([]byte(value), 1, 4093, 17, 3) {
						_, _ = stream.Write(chunk)
					}
					got := redactDirectProcessOutput(stream)
					if strings.Contains(got, "alice") || strings.Contains(got, "visible-secret") || !strings.Contains(got, "REDACTED") {
						t.Fatalf("streaming %s redaction leaked userinfo: %q", location, got[:min(len(got), 256)])
					}
				})
			}
		})
	}
}

func TestBoundedProcessStreamStreamingAuthorityKeepsQueriesAndFragmentsCorrect(t *testing.T) {
	for _, ordinary := range []string{
		"https://example.invalid?view=compact",
		"https://example.invalid#details, punctuation!",
	} {
		stream := &boundedProcessStream{}
		value := ordinary + strings.Repeat("h", directProcessInspectionBytes-len(ordinary)) + strings.Repeat("m", directProcessInspectionBytes+1) + strings.Repeat("t", directProcessInspectionBytes)
		for _, chunk := range splitDirectProcessBytes([]byte(value), 3, 1, 29) {
			_, _ = stream.Write(chunk)
		}
		if got := redactDirectProcessOutput(stream); !strings.HasPrefix(got, ordinary) {
			t.Fatalf("ordinary authority punctuation changed: %q", got[:min(len(got), 128)])
		}
	}

	stream := &boundedProcessStream{}
	secret := "tail-query-secret"
	query := "https://example.invalid?token=" + secret
	value := strings.Repeat("h", directProcessInspectionBytes) + strings.Repeat("m", directProcessInspectionBytes+1) + strings.Repeat("t", directProcessInspectionBytes-len(query)) + query
	for _, chunk := range splitDirectProcessBytes([]byte(value), 1, 17, 4097) {
		_, _ = stream.Write(chunk)
	}
	if got := redactDirectProcessOutput(stream); strings.Contains(got, secret) || !strings.Contains(got, "?token=[REDACTED]") {
		t.Fatalf("streaming sensitive query leaked or changed: %q", got[len(got)-min(len(got), 256):])
	}
}

func TestBoundedProcessStreamCanonicalizesNestedUserinfoAndQuerySpans(t *testing.T) {
	credentials := []string{
		"https://alice?token=secret#visible@example.invalid/path",
		"https://alice&token=secret#visible@example.invalid/path",
		"https://alice?token=secret'visible@example.invalid/path",
		"https://alice?token=secret\"visible@example.invalid/path",
	}
	for _, credential := range credentials {
		for _, location := range []string{"complete", "head", "tail", "gap"} {
			t.Run(fmt.Sprintf("%s-%s", location, credential[len("https://alice"):len("https://alice")+1]), func(t *testing.T) {
				stream := &boundedProcessStream{}
				var value string
				switch location {
				case "complete":
					value = credential
				case "head":
					value = credential + strings.Repeat("h", directProcessInspectionBytes-len(credential)) + strings.Repeat("m", directProcessInspectionBytes+3) + strings.Repeat("t", directProcessInspectionBytes)
				case "tail":
					value = strings.Repeat("h", directProcessInspectionBytes) + strings.Repeat("m", directProcessInspectionBytes+3) + strings.Repeat("t", directProcessInspectionBytes-len(credential)) + credential
				case "gap":
					at := strings.LastIndex(credential, "@")
					prefix, suffix := credential[:at], credential[at:]
					value = strings.Repeat("h", directProcessInspectionBytes-len(prefix)) + prefix + strings.Repeat("m", directProcessInspectionBytes+3) + suffix + strings.Repeat("t", directProcessInspectionBytes-len(suffix))
				}
				for _, chunk := range splitDirectProcessBytes([]byte(value), 1, 2, 4093, 7, 31) {
					_, _ = stream.Write(chunk)
				}
				got := redactDirectProcessOutput(stream)
				for _, leaked := range []string{"alice", "token=", "secret", "visible"} {
					if strings.Contains(got, leaked) {
						t.Fatalf("nested span leaked %q in %s output: %q", leaked, location, got[:min(len(got), 256)])
					}
				}
				if location != "gap" && !strings.Contains(got, "REDACTED") {
					t.Fatalf("nested span was not redacted in %s output", location)
				}
			})
		}
	}

	stream := &boundedProcessStream{}
	independent := "https://alice?token=secret#visible"
	for _, chunk := range splitDirectProcessBytes([]byte(independent), 1, 3, 2) {
		_, _ = stream.Write(chunk)
	}
	if got, want := redactDirectProcessOutput(stream), redactDirectProcessVisibleOutput(independent); got != want || !strings.Contains(got, "alice") || strings.Contains(got, "secret") {
		t.Fatalf("independent query redaction = %q, want %q", got, want)
	}

	stream = &boundedProcessStream{}
	streamingIndependent := strings.Repeat("h", directProcessInspectionBytes*2+3) + independent
	for _, chunk := range splitDirectProcessBytes([]byte(streamingIndependent), 4093, 1, 17) {
		_, _ = stream.Write(chunk)
	}
	first := redactDirectProcessOutput(stream)
	stream.mu.Lock()
	firstSpanCount, firstSpanCapacity := len(stream.scanner.tailSpans), cap(stream.scanner.tailSpans)
	stream.mu.Unlock()
	second := redactDirectProcessOutput(stream)
	stream.mu.Lock()
	secondSpanCount, secondSpanCapacity := len(stream.scanner.tailSpans), cap(stream.scanner.tailSpans)
	stream.mu.Unlock()
	if first != second || strings.Contains(first, "secret") || !strings.Contains(first, "alice?token=[REDACTED]#visible") {
		t.Fatalf("streaming independent query redaction was unstable: %q", first[len(first)-min(len(first), 256):])
	}
	if secondSpanCount != firstSpanCount || secondSpanCapacity != firstSpanCapacity || secondSpanCapacity > directProcessInspectionBytes {
		t.Fatalf("repeated render grew span state from %d/%d to %d/%d", firstSpanCount, firstSpanCapacity, secondSpanCount, secondSpanCapacity)
	}
}

func TestBoundedProcessStreamAuthorityGrammarMatchesVisibleRegex(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty first at", value: "https://@ordinary@host/path"},
		{name: "space", value: "https://alice ordinary@example.invalid/path"},
		{name: "tab", value: "https://alice\tordinary@example.invalid/path"},
		{name: "line feed", value: "https://alice\nordinary@example.invalid/path"},
		{name: "form feed", value: "https://alice\fordinary@example.invalid/path"},
		{name: "carriage return", value: "https://alice\rordinary@example.invalid/path"},
		{name: "vertical tab is not regexp space", value: "https://alice\vordinary@example.invalid/path"},
	}
	for _, test := range tests {
		for _, location := range []string{"complete", "head", "tail", "gap"} {
			t.Run(test.name+"-"+location, func(t *testing.T) {
				stream := &boundedProcessStream{}
				var value string
				switch location {
				case "complete":
					value = test.value
				case "head":
					value = test.value + strings.Repeat("h", directProcessInspectionBytes-len(test.value)) + strings.Repeat("m", directProcessInspectionBytes+5) + strings.Repeat("t", directProcessInspectionBytes)
				case "tail":
					value = strings.Repeat("h", directProcessInspectionBytes) + strings.Repeat("m", directProcessInspectionBytes+5) + strings.Repeat("t", directProcessInspectionBytes-len(test.value)) + test.value
				case "gap":
					cut := strings.Index(test.value, "ordinary")
					prefix, suffix := test.value[:cut], test.value[cut:]
					value = strings.Repeat("h", directProcessInspectionBytes-len(prefix)) + prefix + strings.Repeat("m", directProcessInspectionBytes+5) + suffix + strings.Repeat("t", directProcessInspectionBytes-len(suffix))
				}
				for _, chunk := range splitDirectProcessBytes([]byte(value), 1, 4091, 2, 17, 3) {
					_, _ = stream.Write(chunk)
				}
				got := redactDirectProcessOutput(stream)
				if location == "complete" {
					if want := redactDirectProcessVisibleOutput(test.value); got != want {
						t.Fatalf("grammar parity = %q, want %q", got, want)
					}
					return
				}
				want, _ := cropRedactedDirectProcessOutput(redactDirectProcessVisibleOutput(value))
				if test.name == "vertical tab is not regexp space" {
					if strings.Contains(got, "alice") || strings.Contains(got, "ordinary") || location != "gap" && !strings.Contains(got, "REDACTED") {
						t.Fatalf("vertical-tab userinfo grammar was not redacted: %q", got[:min(len(got), 128)])
					}
					return
				}
				if got != want {
					t.Fatalf("streaming grammar parity differs: got prefix %q, want prefix %q", got[:min(len(got), 128)], want[:min(len(want), 128)])
				}
			})
		}
	}
}

func TestBoundedProcessStreamUsesPostRedactionTruncation(t *testing.T) {
	base := "https://alice:" + strings.Repeat("s", 48) + "@example.invalid/path"
	redactedBase := redactDirectProcessVisibleOutput(base)
	for _, target := range []int{directProcessInspectionBytes - 1, directProcessInspectionBytes, directProcessInspectionBytes + 1} {
		t.Run(fmt.Sprintf("redacted-%d", target), func(t *testing.T) {
			value := base + strings.Repeat("x", target-len(redactedBase))
			if len(value) <= directProcessInspectionBytes || len(value) > directProcessInspectionBytes*2 {
				t.Fatalf("test input length = %d, need a reconstructable raw truncation", len(value))
			}
			stream := &boundedProcessStream{}
			for _, chunk := range splitDirectProcessBytes([]byte(value), 1, 19, 3, 4093) {
				_, _ = stream.Write(chunk)
			}
			got, truncated := stream.Render()
			want := redactDirectProcessVisibleOutput(value)
			if target > directProcessInspectionBytes {
				want = want[:directProcessRetainedStreamBytes] + directProcessTruncationMarker + want[len(want)-directProcessRetainedStreamBytes:]
			}
			if got != want || truncated != (target > directProcessInspectionBytes) {
				t.Fatalf("Render() = (%d bytes, %t), want (%d bytes, %t)", len(got), truncated, len(want), target > directProcessInspectionBytes)
			}
			if strings.Contains(got, "alice") || strings.Contains(got, strings.Repeat("s", 8)) {
				t.Fatalf("redacted result leaked credential: %q", got[:min(len(got), 128)])
			}
			if count := strings.Count(got, directProcessTruncationMarker); count != map[bool]int{false: 0, true: 1}[truncated] {
				t.Fatalf("marker count = %d, truncated = %t", count, truncated)
			}
		})
	}
}

func TestBoundedProcessStreamRecognizesLongSchemesWithoutRetention(t *testing.T) {
	for _, suffix := range []string{"9", "+", "-", "."} {
		t.Run("suffix-"+suffix, func(t *testing.T) {
			scheme := "a" + strings.Repeat("b", directProcessInspectionBytes+257) + suffix
			for _, userinfo := range []string{"alice", "alice:123456"} {
				stream := &boundedProcessStream{}
				value := scheme + "://" + userinfo + "@example.invalid/path"
				for _, chunk := range splitDirectProcessBytes([]byte(value), 1, 4097, 3, 71) {
					_, _ = stream.Write(chunk)
				}
				got := redactDirectProcessOutput(stream)
				if strings.Contains(got, userinfo) || !strings.Contains(got, "REDACTED") {
					t.Fatalf("long scheme userinfo leaked: %q", got[len(got)-min(len(got), 256):])
				}
			}
		})
	}

	stream := &boundedProcessStream{}
	nonURL := "a" + strings.Repeat("b", directProcessInspectionBytes+257) + "_://alice:123456@example.invalid/path"
	for _, chunk := range splitDirectProcessBytes([]byte(nonURL), 7, 1, 4099) {
		_, _ = stream.Write(chunk)
	}
	if got := redactDirectProcessOutput(stream); !strings.Contains(got, "alice:123456@example.invalid/path") {
		t.Fatalf("ordinary non-URL was redacted: %q", got[len(got)-min(len(got), 256):])
	}
}

func TestBoundedProcessStreamLargeWriteUsesFixedRetentionCapacity(t *testing.T) {
	value := append([]byte("prefix-"), bytes.Repeat([]byte("x"), directProcessInspectionBytes*32)...)
	value = append(value, []byte("-suffix")...)
	stream := &boundedProcessStream{}
	if _, err := stream.Write(value); err != nil {
		t.Fatal(err)
	}
	stream.mu.Lock()
	headLength, headCapacity := len(stream.head), cap(stream.head)
	tailLength, tailCapacity := len(stream.tail), cap(stream.tail)
	stream.mu.Unlock()
	if headLength != directProcessInspectionBytes || tailLength != directProcessInspectionBytes || headCapacity > directProcessInspectionBytes || tailCapacity > directProcessInspectionBytes {
		t.Fatalf("retention bounds = head %d/%d tail %d/%d", headLength, headCapacity, tailLength, tailCapacity)
	}
	got, truncated := stream.Render()
	want := string(value[:directProcessRetainedStreamBytes]) + directProcessTruncationMarker + string(value[len(value)-directProcessRetainedStreamBytes:])
	if got != want || !truncated {
		t.Fatalf("large write output mismatch: %d bytes, truncated %t", len(got), truncated)
	}

	spans := &boundedProcessStream{}
	if _, err := spans.Write([]byte(strings.Repeat("?token=large-write-secret&", directProcessInspectionBytes))); err != nil {
		t.Fatal(err)
	}
	spans.mu.Lock()
	queryCapacity := cap(spans.scanner.queryKey)
	headSpanCapacity := cap(spans.scanner.headSpans)
	tailSpanCapacity := cap(spans.scanner.tailSpans)
	spans.mu.Unlock()
	if queryCapacity > 64 || headSpanCapacity > directProcessInspectionBytes || tailSpanCapacity > directProcessInspectionBytes {
		t.Fatalf("scanner retention capacities = query %d head %d tail %d", queryCapacity, headSpanCapacity, tailSpanCapacity)
	}
	if got := redactDirectProcessOutput(spans); strings.Contains(got, "large-write-secret") {
		t.Fatal("large single write leaked a sensitive query value")
	}
}

func TestDirectProcessTrailingOutputHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessTrailingOutputHelper") {
		t.Skip("helper process only")
	}
	fmt.Fprintln(os.Stdout, "stdout-before")
	fmt.Fprintln(os.Stderr, "stderr-before")
	time.Sleep(150 * time.Millisecond)
	fmt.Fprintln(os.Stdout, "stdout-after")
	fmt.Fprintln(os.Stderr, "stderr-after")
}

func TestDirectProcessInheritedPipeParentHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessInheritedPipeParentHelper") {
		t.Skip("helper process only")
	}
	fmt.Fprintln(os.Stdout, "parent")
	child := exec.Command(os.Args[0], "-test.run=^TestDirectProcessInheritedPipeChildHelper$")
	child.Env = os.Environ()
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		panic(err)
	}
}

func TestDirectProcessInheritedPipeChildHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessInheritedPipeChildHelper") {
		t.Skip("helper process only")
	}
	time.Sleep(150 * time.Millisecond)
	fmt.Fprintln(os.Stdout, "child-stdout")
	fmt.Fprintln(os.Stderr, "child-stderr")
}

func TestDirectProcessDelayedWriteHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessDelayedWriteHelper") {
		t.Skip("helper process only")
	}
	time.Sleep(250 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "descendant-marker"), []byte("unexpected\n"), 0o600); err != nil {
		panic(err)
	}
}

func directProcessHelper() bool {
	if !strings.Contains(strings.Join(os.Args, " "), "TestDirectProcessHelper") {
		return false
	}
	directory, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(os.Stdout, "argv=%s cwd=%s HOME=%q SECRET=%q\n", strings.Join(os.Args[1:], "|"), directory, os.Getenv("HOME"), os.Getenv("SECRET"))
	fmt.Fprint(os.Stderr, "helper stderr")
	return true
}

func directProcessNestedHelper() bool {
	arguments := strings.Join(os.Args, " ")
	for _, helper := range []string{
		"TestDirectProcessDescendantHelper", "TestDirectProcessDelayedWriteHelper",
		"TestDirectProcessTrailingOutputHelper", "TestDirectProcessInheritedPipeParentHelper", "TestDirectProcessInheritedPipeChildHelper",
	} {
		if strings.Contains(arguments, helper) {
			return true
		}
	}
	return false
}

const aggregateTestObjectID = "0123456789012345678901234567890123456789"

func directProcessEnvironment(t *testing.T, temporaryDirectory string) []string {
	t.Helper()
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=hostile", "SECRET=must-not-pass", "TMPDIR=" + temporaryDirectory,
		"WTREE_PROJECT_ID=project", "WTREE_WORKSPACE=workspace", "WTREE_REPOSITORY_ID=repository", "WTREE_MOUNT=child", "WTREE_PATH=/workspace/child", "WTREE_BRANCH=main", "WTREE_COMMIT=" + aggregateTestObjectID,
	}
}

func environmentValue(t *testing.T, environment []string, key string) string {
	t.Helper()
	for _, item := range environment {
		if value, found := strings.CutPrefix(item, key+"="); found {
			return value
		}
	}
	t.Fatalf("missing environment value %q", key)
	return ""
}

func splitDirectProcessBytes(value []byte, sizes ...int) [][]byte {
	chunks := make([][]byte, 0, len(value))
	for offset, index := 0, 0; offset < len(value); index++ {
		size := sizes[index%len(sizes)]
		end := offset + size
		if end > len(value) {
			end = len(value)
		}
		chunks = append(chunks, value[offset:end])
		offset = end
	}
	return chunks
}
