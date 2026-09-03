package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildInventoryExcludesOnlyListedHelpersAndAssignsExactlyOnce(t *testing.T) {
	packages := []string{"example/other", servicePackage}
	listed := []string{"TestParent", "TestHelper", "TestOrdinary", "ExampleExample", "FuzzFuzz"}
	inventory, err := buildInventory(packages, listed, []HelperTarget{{Name: "TestHelper", Parent: "TestParent"}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ExampleExample", "FuzzFuzz", "TestOrdinary", "TestParent"}
	if !reflect.DeepEqual(inventory.ServiceTargets, want) {
		t.Fatalf("targets = %v, want %v", inventory.ServiceTargets, want)
	}
	if err := validateShards(inventory.ServiceTargets, inventory.Shards); err != nil {
		t.Fatal(err)
	}
	pattern, err := shardPattern(inventory.Shards[0])
	if err != nil || pattern != "^(ExampleExample)$" {
		t.Fatalf("shard reproduction = %q, %v", pattern, err)
	}
}

func TestBuildInventoryFailsClosed(t *testing.T) {
	basePackages := []string{"example/other", servicePackage}
	helper := []HelperTarget{{Name: "TestHelper", Parent: "TestParent"}}
	cases := []struct {
		name     string
		packages []string
		listed   []string
		helpers  []HelperTarget
	}{
		{"duplicate package", []string{servicePackage, servicePackage}, []string{"TestParent", "TestHelper"}, helper},
		{"missing service", []string{"example/other"}, []string{"TestParent", "TestHelper"}, helper},
		{"empty target", basePackages, nil, helper},
		{"duplicate target", basePackages, []string{"TestParent", "TestHelper", "TestParent"}, helper},
		{"missing helper", basePackages, []string{"TestParent", "TestOrdinary"}, helper},
		{"missing parent", basePackages, []string{"TestHelper", "TestOrdinary"}, helper},
		{"helper parent helper", basePackages, []string{"TestParent", "TestHelper", "TestChild"}, []HelperTarget{{Name: "TestHelper", Parent: "TestParent"}, {Name: "TestChild", Parent: "TestHelper"}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildInventory(test.packages, test.listed, test.helpers, 8); err == nil {
				t.Fatal("buildInventory unexpectedly succeeded")
			}
		})
	}
	if err := validateShards([]string{"TestA"}, [][]string{{"TestUnknown"}}); err == nil {
		t.Fatal("unknown assignment unexpectedly succeeded")
	}
	if err := validateShards([]string{"TestA"}, [][]string{}); err == nil {
		t.Fatal("incomplete assignment unexpectedly succeeded")
	}
}

func TestOrdinaryTargetsAreDeterministicAndFailClosed(t *testing.T) {
	targets, err := ordinaryTargets([]string{"TestB", "ExampleA", "noise", "TestA"})
	if err != nil || !reflect.DeepEqual(targets, []string{"ExampleA", "TestA", "TestB"}) {
		t.Fatalf("targets = %#v, %v", targets, err)
	}
	if _, err := ordinaryTargets([]string{"TestA", "TestA"}); err == nil {
		t.Fatal("duplicate CLI target accepted")
	}
	if _, err := ordinaryTargets([]string{"noise"}); err == nil {
		t.Fatal("empty CLI target inventory accepted")
	}
}

func TestCLIShardTargetsAreDeterministicAndExactOnce(t *testing.T) {
	targets := []string{"TestA", "TestB", "TestC", "TestD", "TestE"}
	for _, workers := range []int{1, 2, 4} {
		shards, err := cliShardTargets(targets, workers)
		if err != nil || len(shards) != workers || validateShards(targets, shards) != nil {
			t.Fatalf("workers=%d shards=%#v err=%v", workers, shards, err)
		}
		for _, shard := range shards {
			if len(shard) == 0 {
				t.Fatalf("workers=%d has empty shard", workers)
			}
		}
	}
	if _, err := cliShardTargets(targets, 5); err == nil {
		t.Fatal("unbounded CLI workers accepted")
	}
}

func TestCLIShardWorkersReservesCapacityForService(t *testing.T) {
	for workers, want := range map[int]int{1: 1, 2: 2, 3: 2, 4: 2} {
		if got := cliShardWorkers(workers); got != want {
			t.Fatalf("workers=%d got=%d want=%d", workers, got, want)
		}
	}
}

func TestRunWithTimeoutCLICommandPlanPreservesCanonicalCoverage(t *testing.T) {
	for _, workers := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("workers-%d", workers), func(t *testing.T) {
			inventory := Inventory{
				Packages:   []string{cliPackage, servicePackage},
				CLITargets: []string{"TestA", "TestB", "TestC", "TestD"},
				Shards:     make([][]string, 8),
			}
			inventory.Shards[0] = []string{"TestServiceA", "TestServiceB"}
			commands := &successfulRecordingCommander{}
			captureStdout(t, func() {
				if status := runWithTimeout(context.Background(), commands, inventory, "normal", 0, workers, false, nil, time.Minute, "round-robin"); status != 0 {
					t.Fatalf("status=%d", status)
				}
			})
			var cliPatterns []string
			serviceCalls := 0
			for _, call := range commands.calls {
				switch call[len(call)-1] {
				case cliPackage:
					for index, arg := range call {
						if arg == "-run" {
							cliPatterns = append(cliPatterns, call[index+1])
						}
					}
				case "./internal/service":
					serviceCalls++
				}
			}
			if workers >= 2 {
				sort.Strings(cliPatterns)
				wantServiceCalls := 1
				if workers >= 3 {
					wantServiceCalls = 2
				}
				if !reflect.DeepEqual(cliPatterns, []string{"^(TestA|TestC)$", "^(TestB|TestD)$"}) || serviceCalls != wantServiceCalls {
					t.Fatalf("workers=%d cli=%#v service=%d", workers, cliPatterns, serviceCalls)
				}
				return
			}
			if !reflect.DeepEqual(cliPatterns, []string{"^(TestA|TestB|TestC|TestD)$"}) {
				t.Fatalf("workers=%d cli=%#v", workers, cliPatterns)
			}
			wantServiceCalls := 1
			if workers >= 3 {
				wantServiceCalls = 2
			}
			if serviceCalls != wantServiceCalls {
				t.Fatalf("workers=%d service calls=%d want=%d", workers, serviceCalls, wantServiceCalls)
			}
		})
	}
}

func TestServicePhysicalBatchesPreserveLogicalIdentityAndCoverage(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	shards := [][]string{{"TestD", "TestB", "TestA", "TestC"}, {"TestE"}}
	weights := []TimingWeight{
		{Target: "TestA", Elapsed: 9 * time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestB", Elapsed: 7 * time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestC", Elapsed: 3 * time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestD", Elapsed: time.Second, Samples: 1, ObservedAt: now},
	}
	for _, workers := range []int{1, 2} {
		batches, err := servicePhysicalBatches(shards, weights, workers, now)
		if err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}
		if len(batches) != 2 || batches[0].PartCount != 1 || batches[1].PartCount != 1 || !reflect.DeepEqual(batches[0].Targets, shards[0]) || !reflect.DeepEqual(batches[1].Targets, shards[1]) {
			t.Fatalf("workers=%d batches=%#v", workers, batches)
		}
	}
	for _, workers := range []int{3, 4} {
		batches, err := servicePhysicalBatches(shards, weights, workers, now)
		if err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}
		want := []serviceBatch{
			{LogicalShard: 1, Part: 1, PartCount: 2, Targets: []string{"TestA", "TestD"}},
			{LogicalShard: 1, Part: 2, PartCount: 2, Targets: []string{"TestB", "TestC"}},
			{LogicalShard: 2, Part: 1, PartCount: 1, Targets: []string{"TestE"}},
		}
		if !reflect.DeepEqual(batches, want) {
			t.Fatalf("workers=%d batches=%#v want=%#v", workers, batches, want)
		}
		all := []string{}
		for _, batch := range batches {
			if len(batch.Targets) == 0 {
				t.Fatalf("workers=%d has empty batch: %#v", workers, batch)
			}
			all = append(all, batch.Targets...)
		}
		if err := validateShards([]string{"TestA", "TestB", "TestC", "TestD", "TestE"}, [][]string{all}); err != nil {
			t.Fatalf("workers=%d exact-once coverage: %v", workers, err)
		}
	}
	cold, err := servicePhysicalBatches(shards[:1], nil, 4, now)
	if err != nil || !reflect.DeepEqual(cold, []serviceBatch{{LogicalShard: 1, Part: 1, PartCount: 2, Targets: []string{"TestA", "TestC"}}, {LogicalShard: 1, Part: 2, PartCount: 2, Targets: []string{"TestB", "TestD"}}}) {
		t.Fatalf("cold batches=%#v err=%v", cold, err)
	}
}

func TestParseEventsKeepsStructuredResultsWithoutInferringStatusFromText(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"run","Test":"TestSuccess"}`,
		`{"Action":"output","Test":"TestSuccess","Output":"PASS text is not an exit status\n"}`,
		`{"Action":"pass","Test":"TestSuccess","Elapsed":0.1}`,
		`{"Action":"run","Test":"TestFailure"}`,
		`{"Action":"output","Test":"TestFailure","Output":"panic: test timed out after 1s\nWARNING: DATA RACE\ncompile failure\n"}`,
		`{"Action":"fail","Test":"TestFailure","Elapsed":0.2}`,
		`{"Action":"run","Test":"TestExample"}`,
		`{"Action":"pass","Test":"TestExample","Elapsed":0}`,
		`{"Action":"run","Test":"FuzzThing"}`,
		`{"Action":"pass","Test":"FuzzThing","Elapsed":0.3}`,
		`{"Action":"run","Test":"TestParent/subtest"}`,
		`{"Action":"fail","Test":"TestParent/subtest","Elapsed":0.4}`,
	}, "\n")
	results, err := parseEvents(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 || results[2].Target != "TestFailure" || !results[2].Failed || !strings.Contains(results[2].Output, "DATA RACE") {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Target != "FuzzThing" || results[4].Target != "TestSuccess" || results[4].Failed {
		t.Fatalf("results sorted/status = %#v", results)
	}
	if _, err := parseEvents(strings.NewReader("not JSON")); err == nil {
		t.Fatal("invalid JSON unexpectedly succeeded")
	}
}

func TestTimingFormatIsNonSensitive(t *testing.T) {
	weights := []TimingWeight{{Target: "TestUseful", Elapsed: 12 * time.Millisecond, Samples: 2, ObservedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)}}
	encoded, err := formatTiming(weights)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("/")) || bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("timing cache contains sensitive shape: %q", encoded)
	}
	decoded, err := parseTiming(encoded)
	if err != nil || !reflect.DeepEqual(decoded, weights) {
		t.Fatalf("round trip = %#v, %v", decoded, err)
	}
	for _, bad := range [][]byte{[]byte("unknown\n"), []byte(timingFormat + "\nTestA\t-1\t1\t2026-09-02T00:00:00Z\n"), []byte(timingFormat + "\nTestToken=secret\t1\t1\t2026-09-02T00:00:00Z\n")} {
		if _, err := parseTiming(bad); err == nil {
			t.Fatalf("bad timing cache %q unexpectedly succeeded", bad)
		}
	}
	if _, err := formatTiming([]TimingWeight{{Target: "TestToken=secret", Elapsed: time.Second, Samples: 1, ObservedAt: time.Now()}}); err == nil {
		t.Fatal("secret-shaped target unexpectedly formatted")
	}
}

func TestTimingAtomicPersistenceContract(t *testing.T) {
	weights := []TimingWeight{{Target: "TestUseful", Elapsed: 12 * time.Millisecond, Samples: 2, ObservedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)}}
	directory := t.TempDir()
	path := filepath.Join(directory, "weights.tsv")
	err := writeTimingAtomic(path, weights)
	if !timingPersistenceAvailable {
		if err == nil {
			t.Fatal("timing persistence unexpectedly succeeded")
		}
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("unavailable persistence created destination %q: %v", path, statErr)
		}
		entries, readErr := os.ReadDir(directory)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("unavailable persistence left artifacts in %q: entries=%v err=%v", directory, entries, readErr)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("timing cache permissions = %v, %v", info, err)
	}
	if err := writeTimingAtomic(filepath.Join(directory, "missing", "weights.tsv"), weights); err == nil {
		t.Fatal("missing cache directory unexpectedly succeeded")
	}
}

type fakeCommander struct{ results []commandResult }

func (fake *fakeCommander) Run(_ context.Context, _ string, _ ...string) commandResult {
	result := fake.results[0]
	fake.results = fake.results[1:]
	return result
}

func TestDiscoverAndCacheFallback(t *testing.T) {
	directory := t.TempDir()
	helperPath := filepath.Join(directory, "helpers.tsv")
	if err := os.WriteFile(helperPath, []byte("TestHelper\tTestParent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := &fakeCommander{results: []commandResult{{Output: []byte("example/other\n" + servicePackage + "\n")}, {Output: []byte("TestParent\nTestHelper\nTestOrdinary\n")}, {Output: []byte("TestCLI\n")}}}
	inventory, err := discover(context.Background(), commands, helperPath)
	if err != nil || !reflect.DeepEqual(inventory.ServiceTargets, []string{"TestOrdinary", "TestParent"}) {
		t.Fatalf("discover = %#v, %v", inventory, err)
	}
	if got := cachePath("/cache", "race"); !strings.Contains(got, filepath.Join(timingFormat, runtime.GOOS, runtime.GOARCH, "race")) {
		t.Fatalf("cache path = %q", got)
	}
	if got := cachePath("", "normal"); got != "" {
		t.Fatalf("missing cache root path = %q, want empty", got)
	}
	if _, err := parseTiming(nil); err == nil {
		t.Fatal("missing cache must require fallback")
	}
}

func TestDiscoverStopsBeforeSchedulerWhenCancelled(t *testing.T) {
	commands := &blockingCommander{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := discover(ctx, commands, "unused"); done <- err }()
	<-commands.started
	cancel()
	select {
	case err := <-done:
		if err == nil || commands.calls != 1 {
			t.Fatalf("discover cancellation = %v calls=%d", err, commands.calls)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery did not return after cancellation")
	}
}

func TestCacheBaseStopsWhenCancelled(t *testing.T) {
	// The runner's own full-suite invocation supplies this override. Clear it
	// here so the test exercises the cancellable go env command, not the
	// intentional override fast path.
	t.Setenv("TEST_RUNNER_CACHE_DIR", "")
	commands := &blockingCommander{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- cacheBase(ctx, commands) }()
	<-commands.started
	cancel()
	select {
	case got := <-done:
		if got != "" || commands.calls != 1 {
			t.Fatalf("cache base = %q calls=%d", got, commands.calls)
		}
	case <-time.After(time.Second):
		t.Fatal("cache base did not return after cancellation")
	}
}

type blockingCommander struct {
	started chan struct{}
	calls   int
}

func (blocking *blockingCommander) Run(ctx context.Context, _ string, _ ...string) commandResult {
	blocking.calls++
	close(blocking.started)
	<-ctx.Done()
	return commandResult{ExitCode: 1, ErrorOutput: []byte(ctx.Err().Error())}
}

func TestSystemCommanderUsesExitStatus(t *testing.T) {
	result := systemCommander{}.Run(context.Background(), "go", "version", "-not-a-valid-go-version-flag")
	if result.ExitCode == 0 || !strings.Contains(string(result.ErrorOutput), "flag") {
		t.Fatalf("result = %#v", result)
	}
	result = systemCommander{}.Run(context.Background(), "go", "version")
	if result.ExitCode != 0 || !strings.Contains(string(result.Output), "go version") {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunSerialReportsTargetsAndPreservesFailureOutput(t *testing.T) {
	inventory := Inventory{Shards: make([][]string, 8)}
	inventory.Shards[0] = []string{"TestA"}
	inventory.Shards[1] = []string{"TestB"}
	commands := &fakeCommander{results: []commandResult{
		{Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestA\",\"Elapsed\":0.1}\n"), Elapsed: time.Millisecond},
		{Output: []byte("panic: test timed out after 1s\n"), Elapsed: time.Millisecond, ExitCode: 1},
	}}
	stdout := captureStdout(t, func() {
		if status := runSerial(context.Background(), commands, inventory, "normal", 0, nil); status != 1 {
			t.Fatalf("status = %d, want 1", status)
		}
	})
	if !strings.Contains(stdout, "status=passed targets=1") || !strings.Contains(stdout, "panic: test timed out") || !strings.Contains(stdout, "status=failed targets=2") {
		t.Fatalf("output = %q", stdout)
	}
}

func TestRunSerialFullIncludesEveryNonServicePackageBeforeServiceShards(t *testing.T) {
	inventory := Inventory{
		Packages: []string{"example/other", servicePackage},
		Shards:   make([][]string, 8),
	}
	inventory.Shards[0] = []string{"TestA"}
	commands := &fakeCommander{results: []commandResult{
		{Output: []byte("ok example/other\n"), Elapsed: time.Millisecond},
		{Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestA\",\"Elapsed\":0.1}\n"), Elapsed: time.Millisecond},
	}}
	if status := runSerial(context.Background(), commands, inventory, "normal", 0, nil); status != 0 {
		t.Fatalf("status = %d", status)
	}
	if len(commands.results) != 0 {
		t.Fatalf("runner did not execute the complete package plus service inventory: %#v", commands.results)
	}
}

func TestRunSerialSchedulesEachNonServicePackageAsOwnedUnit(t *testing.T) {
	inventory := Inventory{
		Packages: []string{"example/first", "example/second", servicePackage},
		Shards:   make([][]string, 8),
	}
	inventory.Shards[0] = []string{"TestA"}
	commands := &recordingCommander{results: []commandResult{
		{Output: []byte("ok example/first\n")},
		{Output: []byte("ok example/second\n")},
		{Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestA\",\"Elapsed\":0.1}\n")},
	}}
	if status := runSerial(context.Background(), commands, inventory, "normal", 0, nil); status != 0 {
		t.Fatalf("status = %d", status)
	}
	if len(commands.calls) != 3 {
		t.Fatalf("commands = %#v", commands.calls)
	}
	for index, want := range []string{"example/first", "example/second"} {
		if got := strings.Join(commands.calls[index], " "); !strings.HasSuffix(got, " "+want) {
			t.Fatalf("non-service command %d = %q", index, got)
		}
	}
}

func TestRunSerialUsesRequestedTimeoutForNormalAndRaceCommands(t *testing.T) {
	inventory := Inventory{Shards: make([][]string, 8)}
	inventory.Shards[0] = []string{"TestA"}
	commands := &recordingCommander{results: []commandResult{{Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestA\",\"Elapsed\":0.1}\n")}, {Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestA\",\"Elapsed\":0.1}\n")}}}
	if status := runSerialWithTimeout(context.Background(), commands, inventory, "normal", 1, nil, 17*time.Minute); status != 0 {
		t.Fatalf("normal status = %d", status)
	}
	if status := runSerialWithTimeout(context.Background(), commands, inventory, "race", 1, nil, 45*time.Minute); status != 0 {
		t.Fatalf("race status = %d", status)
	}
	if got := strings.Join(commands.calls[0], " "); !strings.Contains(got, "-timeout=17m0s") || !strings.Contains(got, "-short=false") || strings.Contains(got, "-race") {
		t.Fatalf("normal command = %q", got)
	}
	if got := strings.Join(commands.calls[1], " "); !strings.Contains(got, "-timeout=45m0s") || !strings.Contains(got, "-short=false") || !strings.Contains(got, "-race") {
		t.Fatalf("race command = %q", got)
	}
	if status := runSerialWithTimeout(context.Background(), commands, inventory, "normal", 1, nil, 0); status != 2 {
		t.Fatalf("invalid timeout status = %d", status)
	}
}

func TestRunWithTimeoutCarriesParallelLimitForNormalAndRaceCommands(t *testing.T) {
	for _, mode := range []string{"normal", "race"} {
		t.Run(mode, func(t *testing.T) {
			inventory := Inventory{
				Packages:   []string{"example/ordinary", cliPackage, servicePackage},
				CLITargets: []string{"TestCLI"},
				Shards:     make([][]string, 8),
			}
			inventory.Shards[0] = []string{"TestService"}
			commands := &recordingCommander{results: []commandResult{{}, {}, {Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestService\"}\n")}}}
			if status := runWithTimeout(context.Background(), commands, inventory, mode, 0, 1, false, nil, time.Minute, "round-robin"); status != 0 {
				t.Fatalf("status=%d", status)
			}
			if len(commands.calls) != 3 {
				t.Fatalf("calls=%#v", commands.calls)
			}
			for _, call := range commands.calls {
				if joined := strings.Join(call, " "); !strings.Contains(joined, "-parallel=4") {
					t.Fatalf("command lacks -parallel=4: %q", joined)
				}
			}
		})
	}
}

func TestRunWithTimeoutShardsCLIIntoAnchoredCommands(t *testing.T) {
	inventory := Inventory{Packages: []string{cliPackage, servicePackage}, CLITargets: []string{"TestA", "TestB", "TestC", "TestD"}, Shards: make([][]string, 8)}
	inventory.Shards[0] = []string{"TestService"}
	commands := &recordingCommander{results: []commandResult{{}, {}, {Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestService\"}\n")}}}
	if status := runWithTimeout(context.Background(), commands, inventory, "race", 0, 2, false, nil, time.Minute, "round-robin"); status != 0 {
		t.Fatalf("status=%d", status)
	}
	if len(commands.calls) != 3 {
		t.Fatalf("calls=%#v", commands.calls)
	}
	cliCalls := 0
	for _, call := range commands.calls {
		joined := strings.Join(call, " ")
		if !strings.HasSuffix(joined, " "+cliPackage) {
			continue
		}
		cliCalls++
		if !strings.Contains(joined, "-race") || !strings.Contains(joined, "-short=false") || !strings.Contains(joined, "-count=1") || !strings.Contains(joined, "-run ^(") || !strings.HasSuffix(joined, " "+cliPackage) {
			t.Fatalf("CLI argv=%q", joined)
		}
	}
	if cliCalls != 2 {
		t.Fatalf("CLI calls=%d", cliCalls)
	}
}

func TestRunWithTimeoutSplitsServiceCommandsWithoutChangingLogicalShardIdentity(t *testing.T) {
	inventory := Inventory{
		Packages:   []string{"example/ordinary", cliPackage, servicePackage},
		CLITargets: []string{"TestCLI1", "TestCLI2"},
		Shards:     make([][]string, 8),
	}
	inventory.Shards[0] = []string{"TestA", "TestB", "TestC", "TestD"}
	commands := &successfulRecordingCommander{}
	output := captureStdout(t, func() {
		if status := runWithTimeout(context.Background(), commands, inventory, "race", 0, 3, false, nil, time.Minute, "round-robin"); status != 0 {
			t.Fatalf("status=%d", status)
		}
	})
	if len(commands.calls) != 5 {
		t.Fatalf("calls=%#v", commands.calls)
	}
	var serviceCalls [][]string
	for _, call := range commands.calls {
		if call[len(call)-1] == "./internal/service" {
			serviceCalls = append(serviceCalls, call)
		}
	}
	if len(serviceCalls) != 2 {
		t.Fatalf("service calls=%#v", serviceCalls)
	}
	patterns := make([]string, 0, len(serviceCalls))
	for _, call := range serviceCalls {
		joined := strings.Join(call, " ")
		if !strings.Contains(joined, "-race") || !strings.Contains(joined, "-short=false") || !strings.Contains(joined, "-count=1") || !strings.Contains(joined, "-parallel=4") || !strings.Contains(joined, "-timeout=1m0s") {
			t.Fatalf("service argv=%q", joined)
		}
		for index, arg := range call {
			if arg == "-run" {
				patterns = append(patterns, call[index+1])
			}
		}
	}
	sort.Strings(patterns)
	if !reflect.DeepEqual(patterns, []string{"^(TestA|TestC)$", "^(TestB|TestD)$"}) {
		t.Fatalf("physical patterns=%#v", patterns)
	}
	if !strings.Contains(output, "service shard 1/8 part 1/2 race status=passed targets=2") || !strings.Contains(output, "service shard 1/8 part 2/2 race status=passed targets=2") || !strings.Contains(output, "bounded status=passed targets=4") {
		t.Fatalf("output=%q", output)
	}

	commands = &successfulRecordingCommander{}
	if status := runWithTimeout(context.Background(), commands, inventory, "normal", 1, 4, false, nil, time.Minute, "round-robin"); status != 0 {
		t.Fatalf("requested logical shard status=%d", status)
	}
	if len(commands.calls) != 2 {
		t.Fatalf("requested logical shard calls=%#v", commands.calls)
	}
	for _, call := range commands.calls {
		if call[len(call)-1] != "./internal/service" {
			t.Fatalf("requested logical shard admitted non-service call=%#v", call)
		}
	}
}

func TestRunWithTimeoutPhysicalServiceFailureDrainsAndPreservesRawOutput(t *testing.T) {
	inventory := Inventory{Shards: make([][]string, 8)}
	inventory.Shards[0] = []string{"TestA", "TestB", "TestC", "TestD"}
	inventory.Shards[1] = []string{"TestE", "TestF"}
	inventory.Shards[2] = []string{"TestG", "TestH"}
	root := t.TempDir()
	cache, diagnostic := openTimingCache(root, "normal")
	if timingPersistenceAvailable && diagnostic != "" {
		t.Fatal(diagnostic)
	}
	commands := &physicalFailureCommander{}
	output := captureStdout(t, func() {
		if status := runWithTimeout(context.Background(), commands, inventory, "normal", 0, 3, false, cache, time.Minute, "round-robin"); status != 1 {
			t.Fatalf("status=%d", status)
		}
	})
	if len(commands.calls) != 6 || !strings.Contains(output, "service shard 1/8 part 1/2 normal status=failed targets=2") || !strings.Contains(output, "physical shard raw stdout") || !strings.Contains(output, "physical shard raw stderr") || !strings.Contains(output, "service shard 3/8 part 2/2 normal status=passed targets=1") || !strings.Contains(output, "bounded status=failed targets=8") || !strings.Contains(output, "failed-units=1") {
		t.Fatalf("calls=%d output=%q", len(commands.calls), output)
	}
	if cache != nil && cache.state == "written" {
		t.Fatalf("failed physical batch wrote complete cache: %#v", cache.weights)
	}
}

func TestRunWithTimeoutPhysicalBatchesWriteOnlyCompleteServiceObservations(t *testing.T) {
	if !timingPersistenceAvailable {
		t.Skip("timing persistence is intentionally unavailable on this platform")
	}
	inventory := Inventory{
		Packages:   []string{cliPackage, servicePackage},
		CLITargets: []string{"TestCLIOnly", "TestShared"},
		Shards:     make([][]string, 8),
	}
	inventory.Shards[0] = []string{"TestA", "TestB", "TestC", "TestShared"}
	root := t.TempDir()
	cache, diagnostic := openTimingCache(root, "normal")
	if diagnostic != "" {
		t.Fatal(diagnostic)
	}
	commands := &serviceEventCommander{serviceTargets: inventory.Shards[0]}
	if status := runWithTimeout(context.Background(), commands, inventory, "normal", 0, 3, false, cache, time.Minute, "round-robin"); status != 0 {
		t.Fatalf("status=%d", status)
	}
	if cache.state != "written" || len(cache.weights) != len(inventory.Shards[0]) {
		t.Fatalf("complete service cache=%#v", cache)
	}
	got := make([]string, 0, len(cache.weights))
	for _, weight := range cache.weights {
		got = append(got, weight.Target)
	}
	sort.Strings(got)
	want := append([]string(nil), inventory.Shards[0]...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("service-only cache targets=%#v want=%#v", got, want)
	}
	if !commands.cliRan || commands.serviceCalls != 2 {
		t.Fatalf("command classification cli=%v service calls=%d", commands.cliRan, commands.serviceCalls)
	}
}

func TestRunWithTimeoutReproductionDoesNotReplaceCompleteTimingCache(t *testing.T) {
	if !timingPersistenceAvailable {
		t.Skip("timing persistence is intentionally unavailable on this platform")
	}
	cache, diagnostic := openTimingCache(t.TempDir(), "normal")
	if diagnostic != "" {
		t.Fatal(diagnostic)
	}
	now := time.Now().UTC()
	if err := writeTimingObservations(cache, []TargetResult{{Target: "TestA", Elapsed: time.Second}, {Target: "TestB", Elapsed: 2 * time.Second}}, []string{"TestA", "TestB"}, now); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	inventory := Inventory{Shards: make([][]string, 8)}
	inventory.Shards[0] = []string{"TestA"}
	inventory.Shards[1] = []string{"TestB"}
	commands := &recordingCommander{results: []commandResult{{Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestA\",\"Elapsed\":0.1}\n")}}}
	if status := runWithTimeout(context.Background(), commands, inventory, "normal", 1, 4, false, cache, time.Minute, "lpt"); status != 0 {
		t.Fatalf("status=%d", status)
	}
	after, err := os.ReadFile(cache.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("reproduction changed complete cache err=%v before=%q after=%q", err, before, after)
	}
}

func TestRunWithTimeoutPhysicalBatchesShareTheMixedGlobalCap(t *testing.T) {
	inventory := Inventory{
		Packages:   []string{"example/ordinary", cliPackage, servicePackage},
		CLITargets: []string{"TestCLI1", "TestCLI2"},
		Shards:     make([][]string, 8),
	}
	inventory.Shards[0] = []string{"TestA", "TestB", "TestC", "TestD"}
	commands := newBlockingMixedCommander(4)
	done := make(chan int, 1)
	go func() {
		done <- runWithTimeout(context.Background(), commands, inventory, "normal", 0, 3, false, nil, time.Minute, "round-robin")
	}()
	for count := 0; count < 3; count++ {
		select {
		case <-commands.started:
		case <-time.After(time.Second):
			t.Fatal("mixed scheduler did not fill its cap")
		}
	}
	if maximum := commands.maximumActive(); maximum != 3 {
		t.Fatalf("mixed active cap=%d want 3", maximum)
	}
	close(commands.release)
	select {
	case status := <-done:
		if status != 0 || commands.callCount() != 5 || commands.maximumActive() > 3 {
			t.Fatalf("status=%d calls=%d maximum=%d", status, commands.callCount(), commands.maximumActive())
		}
	case <-time.After(time.Second):
		t.Fatal("mixed scheduler did not drain")
	}
}

func TestRunWithTimeoutCLIFailureDrainsAndPreservesOutput(t *testing.T) {
	inventory := Inventory{Packages: []string{cliPackage, servicePackage}, CLITargets: []string{"TestA", "TestB"}, Shards: make([][]string, 8)}
	inventory.Shards[0] = []string{"TestService"}
	commands := &recordingCommander{results: []commandResult{{Output: []byte("cli raw output\n"), ErrorOutput: []byte("cli raw error\n"), ExitCode: 1}, {}, {Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestService\"}\n")}}}
	output := captureStdout(t, func() {
		if status := runWithTimeout(context.Background(), commands, inventory, "normal", 0, 2, false, nil, time.Minute, "round-robin"); status != 1 {
			t.Fatalf("status=%d", status)
		}
	})
	if len(commands.calls) != 3 || !strings.Contains(output, "targets=1") || !strings.Contains(output, "cli raw output") || !strings.Contains(output, "cli raw error") || !strings.Contains(output, "service shard 1/8 normal status=passed") {
		t.Fatalf("calls=%d output=%q", len(commands.calls), output)
	}
}

func TestRunWithTimeoutReportsOwnedDrainAfterCancellation(t *testing.T) {
	inventory := Inventory{Shards: make([][]string, 8)}
	inventory.Shards[0] = []string{"TestA"}
	commands := &recordingCommander{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stderr := captureStderr(t, func() {
		if status := runWithTimeout(ctx, commands, inventory, "normal", 1, 1, false, nil, time.Minute, "round-robin"); status != 1 {
			t.Fatalf("status = %d, want 1", status)
		}
	})
	if len(commands.calls) != 0 {
		t.Fatalf("cancelled scheduler started commands: %#v", commands.calls)
	}
	if !strings.Contains(stderr, "interrupted: owned-units-drained=0 survivors=0") {
		t.Fatalf("interruption report = %q", stderr)
	}
}

func TestReproductionArgsOverrideAmbientShortMode(t *testing.T) {
	if got := strings.Join(reproductionArgs("normal", 30*time.Minute, "^TestA$"), " "); got != "go test -short=false -count=1 -parallel=4 -timeout=30m0s -run ^TestA$ ./internal/service" {
		t.Fatal(got)
	}
	if got := strings.Join(reproductionArgs("race", 45*time.Minute, "^TestA$"), " "); !strings.Contains(got, "-short=false -count=1 -parallel=4 -timeout=45m0s -race") {
		t.Fatal(got)
	}
}

type recordingCommander struct {
	mu      sync.Mutex
	results []commandResult
	calls   [][]string
}

type successfulRecordingCommander struct {
	mu    sync.Mutex
	calls [][]string
}

func (recording *successfulRecordingCommander) Run(_ context.Context, name string, args ...string) commandResult {
	recording.mu.Lock()
	defer recording.mu.Unlock()
	recording.calls = append(recording.calls, append([]string{name}, args...))
	return commandResult{}
}

type physicalFailureCommander struct {
	mu    sync.Mutex
	calls [][]string
}

type serviceEventCommander struct {
	mu             sync.Mutex
	serviceTargets []string
	serviceCalls   int
	cliRan         bool
}

type blockingMixedCommander struct {
	mu      sync.Mutex
	active  int
	maximum int
	calls   int
	started chan struct{}
	release chan struct{}
}

func newBlockingMixedCommander(total int) *blockingMixedCommander {
	return &blockingMixedCommander{started: make(chan struct{}, total), release: make(chan struct{})}
}

func (commands *blockingMixedCommander) Run(_ context.Context, _ string, _ ...string) commandResult {
	commands.mu.Lock()
	commands.active++
	commands.calls++
	if commands.active > commands.maximum {
		commands.maximum = commands.active
	}
	commands.mu.Unlock()
	commands.started <- struct{}{}
	<-commands.release
	commands.mu.Lock()
	commands.active--
	commands.mu.Unlock()
	return commandResult{}
}

func (commands *blockingMixedCommander) maximumActive() int {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	return commands.maximum
}

func (commands *blockingMixedCommander) callCount() int {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	return commands.calls
}

func (commands *serviceEventCommander) Run(_ context.Context, _ string, args ...string) commandResult {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	if args[len(args)-1] == cliPackage {
		commands.cliRan = true
		return commandResult{}
	}
	if args[len(args)-1] != "./internal/service" {
		return commandResult{}
	}
	commands.serviceCalls++
	joined := strings.Join(args, " ")
	var output strings.Builder
	for _, target := range commands.serviceTargets {
		if strings.Contains(joined, target) {
			fmt.Fprintf(&output, "{\"Action\":\"pass\",\"Test\":\"%s\",\"Elapsed\":0.1}\n", target)
		}
	}
	return commandResult{Output: []byte(output.String())}
}

func (commands *physicalFailureCommander) Run(_ context.Context, name string, args ...string) commandResult {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	call := append([]string{name}, args...)
	commands.calls = append(commands.calls, call)
	if strings.Contains(strings.Join(call, " "), "TestA") {
		return commandResult{Output: []byte("physical shard raw stdout\n"), ErrorOutput: []byte("physical shard raw stderr\n"), ExitCode: 1}
	}
	return commandResult{}
}

func (recording *recordingCommander) Run(_ context.Context, name string, args ...string) commandResult {
	recording.mu.Lock()
	defer recording.mu.Unlock()
	recording.calls = append(recording.calls, append([]string{name}, args...))
	result := recording.results[0]
	recording.results = recording.results[1:]
	return result
}

func TestTimingCacheLifecycleFallsBackWithoutChangingInventory(t *testing.T) {
	root := t.TempDir()
	cache, diagnostic := openTimingCache(root, "normal")
	if !timingPersistenceAvailable {
		if cache != nil || diagnostic != "timing cache fallback: unavailable" {
			t.Fatalf("unavailable cache = %#v, diagnostic=%q", cache, diagnostic)
		}
		assertTimingCachePathAbsent(t, root, "normal")
		return
	}
	if diagnostic != "" || cache == nil || cache.state != "cold" {
		t.Fatalf("cold cache = %#v, diagnostic=%q", cache, diagnostic)
	}
	if !strings.HasPrefix(cache.path, root+string(os.PathSeparator)) {
		t.Fatalf("cache path %q is outside root %q", cache.path, root)
	}
	directory := filepath.Dir(cache.path)
	for directory != root {
		info, err := os.Stat(directory)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("private cache directory %q = %v, %v", directory, info, err)
		}
		directory = filepath.Dir(directory)
	}
	observations := []TargetResult{{Target: "TestUseful", Elapsed: 12 * time.Millisecond}}
	if err := writeTimingObservations(cache, observations, []string{"TestUseful"}, time.Date(2026, 9, 2, 1, 2, 3, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if cache.state != "written" {
		t.Fatalf("cache state = %q", cache.state)
	}
	info, err := os.Stat(cache.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file permissions = %v, %v", info, err)
	}
	loaded, diagnostic := openTimingCache(root, "normal")
	if diagnostic != "" || loaded.state != "loaded" || len(loaded.weights) != 1 || loaded.weights[0].Target != "TestUseful" {
		t.Fatalf("loaded cache = %#v, diagnostic=%q", loaded, diagnostic)
	}

	before, err := buildInventory([]string{"example/other", servicePackage}, []string{"TestParent", "TestHelper", "TestUseful"}, []HelperTarget{{Name: "TestHelper", Parent: "TestParent"}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range [][]byte{[]byte("unknown\n"), []byte(timingFormat + "\nTestToken=secret\t1\t1\t2026-09-02T00:00:00Z\n")} {
		if err := os.WriteFile(cache.path, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		fallback, diagnostic := openTimingCache(root, "normal")
		if fallback == nil || fallback.state != "fallback" || diagnostic != "timing cache fallback: invalid" {
			t.Fatalf("corrupt cache = %#v, diagnostic=%q", fallback, diagnostic)
		}
		if strings.Contains(strings.ToLower(diagnostic), "secret") || strings.Contains(diagnostic, string(corrupt)) {
			t.Fatalf("cache diagnostic exposed input: %q", diagnostic)
		}
		after, err := buildInventory([]string{"example/other", servicePackage}, []string{"TestParent", "TestHelper", "TestUseful"}, []HelperTarget{{Name: "TestHelper", Parent: "TestParent"}}, 8)
		if err != nil || !reflect.DeepEqual(before.Shards, after.Shards) {
			t.Fatalf("cache fallback changed inventory: before=%#v after=%#v err=%v", before.Shards, after.Shards, err)
		}
	}
}

func TestTimingCacheRejectsUnsafeRootsAndWriteErrors(t *testing.T) {
	if cache, diagnostic := openTimingCache("relative-cache", "normal"); cache != nil || diagnostic != "timing cache fallback: unavailable" {
		t.Fatalf("relative root = %#v, diagnostic=%q", cache, diagnostic)
	}
	root := t.TempDir()
	cache, diagnostic := openTimingCache(root, "race")
	if !timingPersistenceAvailable {
		if cache != nil || diagnostic != "timing cache fallback: unavailable" {
			t.Fatalf("unavailable cache = %#v, diagnostic=%q", cache, diagnostic)
		}
		assertTimingCachePathAbsent(t, root, "race")
		path := filepath.Join(root, "weights.tsv")
		if err := writeTimingAtomic(path, []TimingWeight{{Target: "TestUseful", Elapsed: time.Millisecond, Samples: 1, ObservedAt: time.Now()}}); err == nil {
			t.Fatal("unavailable persistence unexpectedly succeeded")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("unavailable persistence created destination %q: %v", path, err)
		}
		return
	}
	if diagnostic != "" {
		t.Fatal(diagnostic)
	}
	if err := os.Mkdir(cache.path, 0o700); err != nil {
		t.Fatal(err)
	}
	if unsafe, diagnostic := openTimingCache(root, "race"); unsafe != nil || diagnostic != "timing cache fallback: unavailable" {
		t.Fatalf("unsafe file target = %#v, diagnostic=%q", unsafe, diagnostic)
	}
	broken := &timingCache{path: filepath.Join(root, "missing", "weights.tsv"), state: "cold"}
	if err := writeTimingObservations(broken, []TargetResult{{Target: "TestUseful", Elapsed: time.Millisecond}}, []string{"TestUseful"}, time.Now()); err == nil {
		t.Fatal("write to missing cache directory unexpectedly succeeded")
	}
}

func TestRunSerialWritesCacheWithoutChangingCoverage(t *testing.T) {
	root := t.TempDir()
	cache, diagnostic := openTimingCache(root, "normal")
	inventory := Inventory{Shards: make([][]string, 8)}
	inventory.Shards[0] = []string{"TestA"}
	commands := &fakeCommander{results: []commandResult{{Output: []byte("{\"Action\":\"pass\",\"Test\":\"TestA\",\"Elapsed\":0.1}\n"), Elapsed: time.Millisecond}}}
	if !timingPersistenceAvailable {
		if cache != nil || diagnostic != "timing cache fallback: unavailable" {
			t.Fatalf("unavailable cache = %#v, diagnostic=%q", cache, diagnostic)
		}
		if status := runSerial(context.Background(), commands, inventory, "normal", 0, cache); status != 0 {
			t.Fatalf("cache-free status = %d", status)
		}
		if len(commands.results) != 0 {
			t.Fatalf("cache-free run left %d command results", len(commands.results))
		}
		assertTimingCachePathAbsent(t, root, "normal")
		return
	}
	if diagnostic != "" {
		t.Fatal(diagnostic)
	}
	if status := runSerial(context.Background(), commands, inventory, "normal", 0, cache); status != 0 {
		t.Fatalf("status = %d", status)
	}
	if cache.state != "written" || len(cache.weights) != 1 || cache.weights[0].Target != "TestA" {
		t.Fatalf("cache = %#v", cache)
	}
}

func TestPartialTimingObservationsNeverReplaceCompleteWeights(t *testing.T) {
	if !timingPersistenceAvailable {
		t.Skip("timing persistence is intentionally unavailable on this platform")
	}
	root := t.TempDir()
	cache, diagnostic := openTimingCache(root, "normal")
	if diagnostic != "" {
		t.Fatal(diagnostic)
	}
	complete := []TargetResult{{Target: "TestUseful", Elapsed: 12 * time.Millisecond}}
	if err := writeTimingObservations(cache, complete, []string{"TestUseful"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePartialTimingObservations(cache, []TargetResult{{Target: "TestUseful", Elapsed: time.Millisecond, Failed: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(cache.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("partial sample changed complete weight: %v", err)
	}
	partial, err := os.ReadFile(cache.path + ".partial")
	if err != nil || !bytes.Contains(partial, []byte("TestUseful")) {
		t.Fatalf("partial observation missing: %v %q", err, partial)
	}
}

func TestCompleteTimingWriteDropsRemovedTargets(t *testing.T) {
	if !timingPersistenceAvailable {
		t.Skip("timing persistence is intentionally unavailable on this platform")
	}
	root := t.TempDir()
	cache, diagnostic := openTimingCache(root, "normal")
	if diagnostic != "" {
		t.Fatal(diagnostic)
	}
	now := time.Now().UTC()
	cache.weights = []TimingWeight{{Target: "TestUseful", Elapsed: time.Second, Samples: 1, ObservedAt: now}, {Target: "TestRemoved", Elapsed: time.Second, Samples: 1, ObservedAt: now}}
	if err := writeTimingObservations(cache, []TargetResult{{Target: "TestUseful", Elapsed: 2 * time.Second}}, []string{"TestUseful"}, now); err != nil {
		t.Fatal(err)
	}
	if len(cache.weights) != 1 || cache.weights[0].Target != "TestUseful" {
		t.Fatalf("removed target retained: %#v", cache.weights)
	}
}

func assertTimingCachePathAbsent(t *testing.T, root, mode string) {
	t.Helper()
	path := cachePath(root, mode)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("timing cache destination %q exists or cannot be checked: %v", path, err)
	}
	ownedRoot := filepath.Join(root, "wtree-test-runner")
	if _, err := os.Lstat(ownedRoot); !os.IsNotExist(err) {
		t.Fatalf("timing cache root %q exists or cannot be checked: %v", ownedRoot, err)
	}
}

func captureStdout(t *testing.T, invoke func()) string {
	t.Helper()
	return captureOutput(t, &os.Stdout, invoke)
}

func captureStderr(t *testing.T, invoke func()) string {
	t.Helper()
	return captureOutput(t, &os.Stderr, invoke)
}

func captureOutput(t *testing.T, destination **os.File, invoke func()) string {
	t.Helper()
	old := *destination
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	*destination = write
	done := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(read)
		done <- data
	}()
	invoke()
	write.Close()
	*destination = old
	data := <-done
	read.Close()
	return string(data)
}
