package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// scheduledUnit is deliberately small: the scheduler owns admission and
// cancellation while the caller owns command construction and result parsing.
// This keeps every runner-owned child behind the same bounded admission gate.
type scheduledUnit struct {
	Name string
	Run  func(context.Context) unitResult
}

type unitResult struct {
	Name      string
	Result    commandResult
	Targets   []string
	Parsed    []TargetResult
	ParseErr  error
	Cancelled bool
}

type schedulerResult struct {
	Results    []unitResult // always in input order, never completion order
	Incomplete bool
}

// runBounded starts at most workers units at once. Authoritative callers pass
// failFast=false, which makes this a drain-all scheduler even after failures.
// Fail-fast is intentionally non-authoritative: queued work is not started and
// the returned result is marked incomplete.
func runBounded(parent context.Context, units []scheduledUnit, workers int, timeout time.Duration, failFast bool) (schedulerResult, error) {
	if workers < 1 || workers > 4 {
		return schedulerResult{}, fmt.Errorf("workers must be between 1 and 4")
	}
	if timeout <= 0 {
		return schedulerResult{}, fmt.Errorf("timeout must be greater than zero")
	}
	results := make([]unitResult, len(units))
	completed := make([]bool, len(units))
	var lock sync.Mutex
	next := 0
	failed := false
	// A fail-fast invocation is deliberately never eligible as complete
	// inventory evidence, even if the first scheduling wave happened to drain.
	incomplete := failFast

	worker := func() {
		for {
			lock.Lock()
			if parent.Err() != nil {
				incomplete = true
				lock.Unlock()
				return
			}
			if next == len(units) || (failFast && failed) {
				if failFast && next < len(units) {
					incomplete = true
				}
				lock.Unlock()
				return
			}
			index := next
			next++
			lock.Unlock()

			unitCtx, cancel := context.WithTimeout(parent, timeout)
			result := units[index].Run(unitCtx)
			if result.Name == "" {
				result.Name = units[index].Name
			}
			result.Cancelled = unitCtx.Err() != nil
			cancel()

			lock.Lock()
			results[index] = result
			completed[index] = true
			if result.Result.ExitCode != 0 || result.ParseErr != nil || result.Cancelled {
				failed = true
			}
			lock.Unlock()
		}
	}
	var group sync.WaitGroup
	for index := 0; index < workers && index < len(units); index++ {
		group.Add(1)
		go func() { defer group.Done(); worker() }()
	}
	group.Wait()
	for index := range results {
		if !completed[index] {
			incomplete = true
		}
	}
	return schedulerResult{Results: results, Incomplete: incomplete}, nil
}
