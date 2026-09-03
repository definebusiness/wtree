// Command test-runner provides deterministic local inventory, duration-aware
// assignment, and bounded local test execution.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type commandResult struct {
	Output      []byte
	ErrorOutput []byte
	Elapsed     time.Duration
	ExitCode    int
}

type commander interface {
	Run(context.Context, string, ...string) commandResult
}

type systemCommander struct{}

func (systemCommander) Run(ctx context.Context, name string, args ...string) commandResult {
	return runOwnedCommand(ctx, name, args...)
}

func discover(ctx context.Context, commands commander, helperPath string) (Inventory, error) {
	packagesResult := commands.Run(ctx, "go", "list", "./...")
	if packagesResult.ExitCode != 0 {
		return Inventory{}, fmt.Errorf("go list: %s", strings.TrimSpace(string(append(packagesResult.Output, packagesResult.ErrorOutput...))))
	}
	targetResult := commands.Run(ctx, "go", "test", "-list", "^(Test|Example|Fuzz)", "./internal/service")
	if targetResult.ExitCode != 0 {
		return Inventory{}, fmt.Errorf("go test -list: %s", strings.TrimSpace(string(append(targetResult.Output, targetResult.ErrorOutput...))))
	}
	cliResult := commands.Run(ctx, "go", "test", "-list", "^(Test|Example|Fuzz)", "./internal/cli")
	if cliResult.ExitCode != 0 {
		return Inventory{}, fmt.Errorf("go test cli -list: %s", strings.TrimSpace(string(append(cliResult.Output, cliResult.ErrorOutput...))))
	}
	helpers, err := readHelpers(helperPath)
	if err != nil {
		return Inventory{}, err
	}
	inventory, err := buildInventory(lines(packagesResult.Output), lines(targetResult.Output), helpers, 8)
	if err != nil {
		return Inventory{}, err
	}
	inventory.CLITargets, err = ordinaryTargets(lines(cliResult.Output))
	if err != nil {
		return Inventory{}, fmt.Errorf("cli inventory: %w", err)
	}
	return inventory, nil
}

func lines(data []byte) []string {
	return strings.Fields(string(data))
}

func defaultHelperPath() string {
	return filepath.Join("tools", "test-runner", "service-subprocess-helpers.tsv")
}

func cachePath(base, mode string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(base, "wtree-test-runner", timingFormat, runtime.GOOS, runtime.GOARCH, mode, "weights.tsv")
}

func cacheBase(ctx context.Context, commands commander) string {
	if override := os.Getenv("TEST_RUNNER_CACHE_DIR"); override != "" {
		return override
	}
	result := commands.Run(ctx, "go", "env", "GOCACHE")
	if result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(string(result.Output))
}

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "inventory" && os.Args[1] != "run" && os.Args[1] != "changed" && os.Args[1] != "changed-run" && os.Args[1] != "docs-check") {
		fmt.Fprintln(os.Stderr, "usage: test-runner inventory|run|changed [--mode normal|race] [--shard N] [--helpers PATH] [--base COMMIT]")
		os.Exit(2)
	}
	command := os.Args[1]
	if command == "docs-check" {
		if err := checkDocs("."); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	flags := flag.NewFlagSet(command, flag.ExitOnError)
	mode := flags.String("mode", "normal", "normal or race")
	shard := flags.Int("shard", 0, "one-based logical shard to reproduce")
	workers := flags.Int("workers", 4, "bounded runner-owned process count (1-4)")
	failFast := flags.Bool("fail-fast", false, "non-authoritative: stop admitting queued units after a failure")
	helpers := flags.String("helpers", defaultHelperPath(), "subprocess helper inventory")
	base := flags.String("base", "", "required comparison commit for changed-area selection")
	timeout := flags.Duration("timeout", 30*time.Minute, "per go test command timeout")
	flags.Parse(os.Args[2:])
	if *mode != "normal" && *mode != "race" {
		fmt.Fprintln(os.Stderr, "mode must be normal or race")
		os.Exit(2)
	}
	if *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "timeout must be greater than zero")
		os.Exit(2)
	}
	if *workers < 1 || *workers > 4 {
		fmt.Fprintln(os.Stderr, "workers must be between 1 and 4")
		os.Exit(2)
	}
	if command == "changed" || command == "changed-run" {
		selection, err := selectChanged(context.Background(), systemCommander{}, *base)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		actions, err := hostChangedExecutionPlan(selection)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if command == "changed-run" {
			if err := executeChangedActions(context.Background(), processChangedExecutor{}, actions, timeout.String(), runtime.GOARCH); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
		if selection.Documentation && len(selection.Packages) == 0 {
			fmt.Println("__WTREE_DOCUMENTATION_ONLY__")
			return
		}
		var values []string
		for _, action := range actions {
			switch action.Kind {
			case "harness":
				values = append(values, "__WTREE_HARNESS__")
			case "test":
				values = append(values, action.Package)
			case "cross-compile":
				values = append(values, "__WTREE_PLATFORM_"+action.Platform+"__")
			}
		}
		fmt.Println(strings.Join(values, " "))
		return
	}
	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	inventory, err := discover(runCtx, systemCommander{}, *helpers)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cache, cacheDiagnostic := openTimingCache(cacheBase(runCtx, systemCommander{}), *mode)
	if cacheDiagnostic != "" {
		fmt.Fprintln(os.Stderr, cacheDiagnostic)
	}
	inventory, assignment, err := balancedInventory(inventory, cache, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "service assignment:", err)
		os.Exit(1)
	}
	if command == "run" {
		status := runWithTimeout(runCtx, systemCommander{}, inventory, *mode, *shard, *workers, *failFast, cache, *timeout, assignment)
		if status != 0 {
			os.Exit(status)
		}
		return
	}
	response := struct {
		Mode         string     `json:"mode"`
		TargetCount  int        `json:"targetCount"`
		HelperCount  int        `json:"helperCount"`
		Shards       [][]string `json:"shards"`
		Reproduction []string   `json:"reproduction,omitempty"`
		CachePath    string     `json:"cachePath"`
		CacheState   string     `json:"cacheState"`
	}{Mode: *mode, TargetCount: len(inventory.ServiceTargets), HelperCount: len(inventory.Helpers), Shards: inventory.Shards, CachePath: timingCachePath(cache), CacheState: timingCacheState(cache)}
	if *shard != 0 {
		if *shard < 1 || *shard > len(inventory.Shards) {
			fmt.Fprintln(os.Stderr, "shard must be between 1 and 8")
			os.Exit(2)
		}
		pattern, err := shardPattern(inventory.Shards[*shard-1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		response.Reproduction = reproductionArgs(*mode, *timeout, pattern)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, encoded, "", "  "); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(pretty.String())
}

func reproductionArgs(mode string, timeout time.Duration, pattern string) []string {
	args := append([]string{"go"}, goTestArgs(mode, timeout, false)...)
	return append(args, "-run", pattern, "./internal/service")
}

func goTestArgs(mode string, timeout time.Duration, jsonOutput bool) []string {
	args := []string{"test", "-short=false"}
	if jsonOutput {
		args = append(args, "-json")
	}
	args = append(args, "-count=1", "-parallel=4", "-timeout="+timeout.String())
	if mode == "race" {
		args = append(args, "-race")
	}
	return args
}

func timingCachePath(cache *timingCache) string {
	if cache == nil {
		return ""
	}
	return cache.path
}

func timingCacheState(cache *timingCache) string {
	if cache == nil {
		return "fallback"
	}
	return cache.state
}

func runSerial(ctx context.Context, commands commander, inventory Inventory, mode string, requestedShard int, cache *timingCache) int {
	return runWithTimeout(ctx, commands, inventory, mode, requestedShard, 1, false, cache, 30*time.Minute, "round-robin")
}

func runSerialWithTimeout(ctx context.Context, commands commander, inventory Inventory, mode string, requestedShard int, cache *timingCache, timeout time.Duration) int {
	return runWithTimeout(ctx, commands, inventory, mode, requestedShard, 1, false, cache, timeout, "round-robin")
}

func runWithTimeout(ctx context.Context, commands commander, inventory Inventory, mode string, requestedShard, workers int, failFast bool, cache *timingCache, timeout time.Duration, assignment string) int {
	if requestedShard < 0 || requestedShard > len(inventory.Shards) {
		fmt.Fprintln(os.Stderr, "shard must be between 1 and 8")
		return 2
	}
	if timeout <= 0 {
		fmt.Fprintln(os.Stderr, "timeout must be greater than zero")
		return 2
	}
	if workers < 1 || workers > 4 {
		fmt.Fprintln(os.Stderr, "workers must be between 1 and 4")
		return 2
	}
	start := time.Now()
	shards := inventory.Shards
	if requestedShard == 0 {
		fmt.Printf("service %s assignment=%s logical-shards=%d workers=%d\n", mode, assignment, len(shards), workers)
	}
	nonServiceCapacity := len(inventory.Packages) - 1
	if nonServiceCapacity < 0 {
		nonServiceCapacity = 0
	}
	nonServiceUnits := make([]scheduledUnit, 0, nonServiceCapacity)
	if requestedShard == 0 && len(inventory.Packages) > 0 {
		nonService := make([]string, 0, len(inventory.Packages)-1)
		for _, pkg := range inventory.Packages {
			if pkg != servicePackage {
				nonService = append(nonService, pkg)
			}
		}
		if len(nonService) == 0 {
			fmt.Fprintln(os.Stderr, "complete inventory has no non-service packages")
			return 1
		}
		for _, pkg := range nonService {
			if pkg == cliPackage && len(inventory.CLITargets) > 0 {
				cliWorkers := cliShardWorkers(workers)
				cliShards, shardErr := cliShardTargets(inventory.CLITargets, cliWorkers)
				if shardErr != nil {
					return 1
				}
				for index, targets := range cliShards {
					if len(targets) == 0 {
						continue
					}
					targets = append([]string(nil), targets...)
					pattern, patternErr := shardPattern(targets)
					if patternErr != nil {
						return 1
					}
					shardName, shardArgs := fmt.Sprintf("non-service %s shard %d/%d", pkg, index+1, cliWorkers), append(goTestArgs(mode, timeout, false), "-run", pattern, pkg)
					unit := scheduledUnit{Name: shardName, Run: func(unitCtx context.Context) unitResult {
						return unitResult{Name: shardName, Result: commands.Run(unitCtx, "go", shardArgs...), Targets: targets}
					}}
					nonServiceUnits = append(nonServiceUnits, unit)
				}
				continue
			}
			packageName := pkg
			arguments := append(goTestArgs(mode, timeout, false), packageName)
			unit := scheduledUnit{Name: "non-service " + packageName, Run: func(unitCtx context.Context) unitResult {
				return unitResult{Name: "non-service " + packageName, Result: commands.Run(unitCtx, "go", arguments...), Targets: []string{packageName}}
			}}
			nonServiceUnits = append(nonServiceUnits, unit)
		}
	}
	var weights []TimingWeight
	if cache != nil && cache.state == "loaded" {
		weights = cache.weights
	}
	batchNow := time.Now()
	batches, batchErr := servicePhysicalBatches(shards, weights, workers, batchNow)
	if batchErr != nil {
		fmt.Fprintln(os.Stderr, "service batches:", batchErr)
		return 1
	}
	serviceUnits := make([]scheduledUnit, 0, len(batches))
	for _, batch := range batches {
		if requestedShard != 0 && requestedShard != batch.LogicalShard {
			continue
		}
		pattern, err := shardPattern(batch.Targets)
		if err != nil {
			fmt.Fprintf(os.Stderr, "shard %d: %v\n", batch.LogicalShard, err)
			return 1
		}
		arguments := append(goTestArgs(mode, timeout, true), "-run", pattern, "./internal/service")
		shardIndex, part, partCount := batch.LogicalShard, batch.Part, batch.PartCount
		shardTargets, shardArgs := append([]string(nil), batch.Targets...), append([]string(nil), arguments...)
		unitName := fmt.Sprintf("service shard %d/8", shardIndex)
		if partCount > 1 {
			unitName = fmt.Sprintf("service shard %d/8 part %d/%d", shardIndex, part, partCount)
		}
		serviceUnits = append(serviceUnits, scheduledUnit{Name: unitName, Run: func(unitCtx context.Context) unitResult {
			result := commands.Run(unitCtx, "go", shardArgs...)
			parsed, parseErr := parseEvents(bytes.NewReader(result.Output))
			return unitResult{Name: unitName, Result: result, Targets: shardTargets, Parsed: parsed, ParseErr: parseErr}
		}})
	}
	units := make([]scheduledUnit, 0, len(nonServiceUnits)+len(serviceUnits))
	if workers == 1 || len(nonServiceUnits) <= workers {
		units = append(units, nonServiceUnits...)
		units = append(units, serviceUnits...)
	} else {
		// Start one deterministic non-service wave (including slow package tests),
		// then let available workers overlap service shards before small remaining
		// packages consume every admission slot.
		units = append(units, nonServiceUnits[:workers]...)
		units = append(units, serviceUnits...)
		units = append(units, nonServiceUnits[workers:]...)
	}
	scheduledResult, err := runBounded(ctx, units, workers, timeout, failFast)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	failures, executed := 0, 0
	var observations, partial []TargetResult
	var scheduled []string
	completeTargets := make([]string, 0)
	for _, logicalShard := range shards {
		completeTargets = append(completeTargets, logicalShard...)
	}
	for _, outcome := range scheduledResult.Results {
		if outcome.Name == "" {
			continue
		}
		if strings.HasPrefix(outcome.Name, "non-service ") {
			label := "packages"
			if strings.Contains(outcome.Name, " shard ") {
				label = "targets"
			}
			if outcome.Result.ExitCode != 0 || outcome.Cancelled {
				failures++
				fmt.Printf("non-service %s status=failed %s=%d elapsed=%s\n", mode, label, len(outcome.Targets), outcome.Result.Elapsed.Round(time.Millisecond))
				os.Stdout.Write(outcome.Result.Output)
				os.Stdout.Write(outcome.Result.ErrorOutput)
			} else {
				fmt.Printf("non-service %s status=passed %s=%d elapsed=%s\n", mode, label, len(outcome.Targets), outcome.Result.Elapsed.Round(time.Millisecond))
			}
			continue
		}
		executed += len(outcome.Targets)
		scheduled = append(scheduled, outcome.Targets...)
		failedEvents := 0
		for _, target := range outcome.Parsed {
			if target.Failed {
				failedEvents++
			}
		}
		if outcome.Result.ExitCode != 0 || outcome.ParseErr != nil || outcome.Cancelled || failedEvents != 0 {
			failures++
			partial = append(partial, outcome.Parsed...)
			fmt.Printf("%s %s status=failed targets=%d elapsed=%s event-failures=%d\n", outcome.Name, mode, len(outcome.Targets), outcome.Result.Elapsed.Round(time.Millisecond), failedEvents)
			if outcome.ParseErr != nil {
				fmt.Fprintf(os.Stderr, "%s JSON parse: %v\n", outcome.Name, outcome.ParseErr)
			}
			os.Stdout.Write(outcome.Result.Output)
			os.Stdout.Write(outcome.Result.ErrorOutput)
			continue
		}
		observations = append(observations, outcome.Parsed...)
		fmt.Printf("%s %s status=passed targets=%d elapsed=%s\n", outcome.Name, mode, len(outcome.Targets), outcome.Result.Elapsed.Round(time.Millisecond))
	}
	completeInventory := requestedShard == 0 && validateShards(completeTargets, [][]string{scheduled}) == nil
	if failures == 0 && !scheduledResult.Incomplete && completeInventory && cache != nil {
		if err := writeTimingObservations(cache, observations, scheduled, time.Now()); err != nil {
			// Timing data changes balancing only. A private-cache failure never
			// changes test coverage or the command's process-status semantics.
			cache.state = "fallback"
			fmt.Fprintln(os.Stderr, "timing cache fallback: unwritable")
		}
	} else if len(partial) != 0 && cache != nil {
		if err := writePartialTimingObservations(cache, partial, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "timing partial cache fallback: unwritable")
		}
	}
	status := "passed"
	if failures != 0 || scheduledResult.Incomplete {
		status = "failed"
	}
	if failFast {
		fmt.Printf("service %s fail-fast=incomplete\n", mode)
	}
	if ctx.Err() != nil {
		drained := 0
		for _, outcome := range scheduledResult.Results {
			if outcome.Name != "" {
				drained++
			}
		}
		// runBounded waits for every admitted Run callback; runOwnedCommand in
		// turn waits for its owned process group. There can therefore be no
		// runner-owned survivor after this drain boundary.
		fmt.Fprintf(os.Stderr, "interrupted: owned-units-drained=%d survivors=0\n", drained)
	}
	fmt.Printf("service %s bounded status=%s targets=%d elapsed=%s failed-units=%d workers=%d\n", mode, status, executed, time.Since(start).Round(time.Millisecond), failures, workers)
	if failures != 0 || scheduledResult.Incomplete {
		return 1
	}
	return 0
}
