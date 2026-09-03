package main

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunBoundedCapsAndDrainsAuthoritativeUnits(t *testing.T) {
	const total = 9
	started := make(chan struct{}, total)
	release := make(chan struct{})
	var lock sync.Mutex
	active, maximum, completed := 0, 0, 0
	units := make([]scheduledUnit, total)
	for index := range units {
		index := index
		units[index] = scheduledUnit{Name: "unit", Run: func(context.Context) unitResult {
			lock.Lock()
			active++
			if active > maximum {
				maximum = active
			}
			lock.Unlock()
			started <- struct{}{}
			<-release
			lock.Lock()
			active--
			completed++
			lock.Unlock()
			result := commandResult{}
			if index == 3 {
				result.ExitCode = 1
			}
			return unitResult{Result: result}
		}}
	}
	done := make(chan schedulerResult, 1)
	go func() {
		result, err := runBounded(context.Background(), units, 3, time.Second, false)
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	for count := 0; count < 3; count++ {
		<-started
	}
	lock.Lock()
	gotMaximum := maximum
	lock.Unlock()
	if gotMaximum != 3 {
		t.Fatalf("active commands = %d, want cap 3", gotMaximum)
	}
	for count := 0; count < total; count++ {
		release <- struct{}{}
	}
	result := <-done
	lock.Lock()
	gotCompleted := completed
	lock.Unlock()
	if gotCompleted != total || result.Incomplete || len(result.Results) != total {
		t.Fatalf("authoritative drain = completed %d incomplete %v results %d", gotCompleted, result.Incomplete, len(result.Results))
	}
	if result.Results[3].Result.ExitCode != 1 {
		t.Fatalf("input-order result lost: %#v", result.Results[3])
	}
}

func TestRunBoundedFailFastIsExplicitlyIncomplete(t *testing.T) {
	started := 0
	units := []scheduledUnit{
		{Name: "failure", Run: func(context.Context) unitResult { started++; return unitResult{Result: commandResult{ExitCode: 1}} }},
		{Name: "queued", Run: func(context.Context) unitResult { started++; return unitResult{} }},
		{Name: "queued", Run: func(context.Context) unitResult { started++; return unitResult{} }},
	}
	result, err := runBounded(context.Background(), units, 1, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Incomplete || started != 1 || result.Results[1].Name != "" {
		t.Fatalf("fail-fast = %#v started=%d", result, started)
	}
}

func TestRunBoundedEnforcesPerUnitTimeout(t *testing.T) {
	result, err := runBounded(context.Background(), []scheduledUnit{{Name: "timeout", Run: func(ctx context.Context) unitResult { <-ctx.Done(); return unitResult{} }}}, 1, 20*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Results[0].Cancelled {
		t.Fatalf("timed unit was not marked cancelled: %#v", result.Results[0])
	}
}

func TestRunBoundedDoesNotStartQueuedUnitsAfterParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	started := make([]bool, 2)
	units := []scheduledUnit{
		{Name: "started", Run: func(context.Context) unitResult {
			started[0] = true
			close(firstStarted)
			<-releaseFirst
			return unitResult{}
		}},
		{Name: "queued", Run: func(context.Context) unitResult { started[1] = true; return unitResult{} }},
	}
	done := make(chan schedulerResult, 1)
	go func() {
		result, err := runBounded(parent, units, 1, time.Second, false)
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	<-firstStarted
	cancel()
	close(releaseFirst)
	result := <-done
	if started[1] || !result.Incomplete {
		t.Fatalf("queued unit started after cancellation: started=%v result=%#v", started, result)
	}
}

func TestRunnerCancellationDoesNotTouchUnrelatedChild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	pidFile := t.TempDir() + "/owned-child.pid"
	parent, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan schedulerResult, 1)
	go func() {
		result, err := runBounded(parent, []scheduledUnit{{Name: "owned", Run: func(ctx context.Context) unitResult {
			close(started)
			return unitResult{Result: systemCommander{}.Run(ctx, "sh", "-c", "sleep 30 & echo $! > \""+pidFile+"\"; wait")}
		}}}, 1, time.Minute, false)
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	<-started
	var childPID string
	deadline := time.Now().Add(time.Second)
	for childPID == "" && time.Now().Before(deadline) {
		data, _ := os.ReadFile(pidFile)
		childPID = strings.TrimSpace(string(data))
		time.Sleep(time.Millisecond)
	}
	if childPID == "" {
		t.Fatal("owned child PID was not recorded")
	}
	unrelated := exec.Command("sh", "-c", "sleep 0.05")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case result := <-done:
		if !result.Results[0].Cancelled {
			t.Fatalf("owned child was not cancelled: %#v", result.Results[0])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owned child did not terminate within bound")
	}
	if err := unrelated.Wait(); err != nil {
		t.Fatalf("unrelated child was disturbed: %v", err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		state, _ := exec.Command("ps", "-o", "state=", "-p", childPID).Output()
		state = []byte(strings.TrimSpace(string(state)))
		if len(state) == 0 || state[0] == 'Z' {
			break
		} // a reaping zombie is not a survivor
		if time.Now().After(deadline) {
			t.Fatal("owned descendant survived runner cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAssignBalancedLPTAndFallbackAreDeterministic(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	targets := []string{"TestA", "TestB", "TestC", "TestD", "TestUnknown"}
	weights := []TimingWeight{
		{Target: "TestA", Elapsed: 9 * time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestB", Elapsed: 7 * time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestC", Elapsed: 3 * time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestD", Elapsed: time.Second, Samples: 1, ObservedAt: now.Add(-31 * 24 * time.Hour)},
	}
	first, state := assignBalanced(targets, weights, 3, now)
	if state != "lpt" || validateShards(targets, first) != nil {
		t.Fatalf("LPT assignment = %#v state=%s", first, state)
	}
	want := [][]string{{"TestA"}, {"TestB", "TestUnknown"}, {"TestC", "TestD"}}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("skew/unknown/stale LPT = %#v, want %#v", first, want)
	}
	second, _ := assignBalanced(targets, weights, 3, now)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("assignment is not stable: %#v != %#v", first, second)
	}
	cold, state := assignBalanced(targets, nil, 3, now)
	if state != "round-robin" || !reflect.DeepEqual(cold, [][]string{{"TestA", "TestD"}, {"TestB", "TestUnknown"}, {"TestC"}}) {
		t.Fatalf("cold fallback = %#v state=%s", cold, state)
	}
	equal, state := assignBalanced([]string{"TestA", "TestB", "TestC", "TestD"}, []TimingWeight{
		{Target: "TestA", Elapsed: time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestB", Elapsed: time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestC", Elapsed: time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestD", Elapsed: time.Second, Samples: 1, ObservedAt: now},
	}, 3, now)
	if state != "lpt" || !reflect.DeepEqual(equal, [][]string{{"TestA", "TestD"}, {"TestB"}, {"TestC"}}) {
		t.Fatalf("equal-load tie = %#v state=%s", equal, state)
	}
}

func TestAssignBalancedExcludesRemovedCacheTargetsFromMedian(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	shards, state := assignBalanced([]string{"TestA", "TestB", "TestC"}, []TimingWeight{
		{Target: "TestB", Elapsed: time.Second, Samples: 1, ObservedAt: now},
		{Target: "TestRemoved", Elapsed: 100 * time.Second, Samples: 1, ObservedAt: now},
	}, 2, now)
	if state != "lpt" || !reflect.DeepEqual(shards, [][]string{{"TestA", "TestC"}, {"TestB"}}) {
		t.Fatalf("removed cache target changed current assignment: %#v state=%s", shards, state)
	}
}
