package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const servicePackage = "github.com/definebusiness/wtree/internal/service"
const cliPackage = "github.com/definebusiness/wtree/internal/cli"

// HelperTarget is a subprocess-only test target and the ordinary test that
// exercises it. Helpers must never be scheduled as independent tests.
type HelperTarget struct {
	Name   string
	Parent string
}

type Inventory struct {
	Packages       []string
	ServiceTargets []string
	Helpers        []HelperTarget
	Shards         [][]string
	CLITargets     []string
}

// serviceBatch is one runner-owned command within a canonical logical service
// shard. Logical shard identity remains the reproduction boundary; physical
// batches only improve admission under the shared process cap.
type serviceBatch struct {
	LogicalShard int
	Part         int
	PartCount    int
	Targets      []string
}

// servicePhysicalBatches keeps the serial and two-worker lanes byte-for-byte
// compatible at the command level. Three and four workers split each logical
// shard in two only when both batches can contain a target.
func servicePhysicalBatches(shards [][]string, weights []TimingWeight, workers int, now time.Time) ([]serviceBatch, error) {
	if workers < 1 || workers > 4 {
		return nil, fmt.Errorf("workers must be between 1 and 4")
	}
	batches := make([]serviceBatch, 0, len(shards)*2)
	for logicalIndex, shard := range shards {
		if len(shard) == 0 {
			continue
		}
		targets := append([]string(nil), shard...)
		partCount := 1
		if workers >= 3 && len(targets) > 1 {
			partCount = 2
		}
		if partCount == 1 {
			batches = append(batches, serviceBatch{
				LogicalShard: logicalIndex + 1,
				Part:         1,
				PartCount:    1,
				Targets:      targets,
			})
			continue
		}
		parts, _ := assignBalanced(targets, weights, partCount, now)
		if err := validateShards(targets, parts); err != nil {
			return nil, fmt.Errorf("logical shard %d: %w", logicalIndex+1, err)
		}
		for partIndex, partTargets := range parts {
			if len(partTargets) == 0 {
				continue
			}
			batches = append(batches, serviceBatch{
				LogicalShard: logicalIndex + 1,
				Part:         partIndex + 1,
				PartCount:    partCount,
				Targets:      partTargets,
			})
		}
	}
	return batches, nil
}

func ordinaryTargets(listed []string) ([]string, error) {
	seen := map[string]bool{}
	for _, target := range listed {
		if isTarget(target) {
			if seen[target] {
				return nil, fmt.Errorf("duplicate target: %s", target)
			}
			seen[target] = true
		}
	}
	targets := make([]string, 0, len(seen))
	for target := range seen {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no targets")
	}
	return targets, nil
}

func cliShardTargets(targets []string, workers int) ([][]string, error) {
	if workers < 1 || workers > 4 {
		return nil, fmt.Errorf("workers must be between 1 and 4")
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no CLI targets")
	}
	shards := make([][]string, workers)
	for index, target := range targets {
		shards[index%workers] = append(shards[index%workers], target)
	}
	if err := validateShards(targets, shards); err != nil {
		return nil, err
	}
	return shards, nil
}

func cliShardWorkers(workers int) int {
	if workers < 2 {
		return 1
	}
	return 2
}

func readHelpers(path string) ([]HelperTarget, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open helper inventory: %w", err)
	}
	defer file.Close()

	var helpers []HelperTarget
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		value := strings.TrimSpace(scanner.Text())
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		parts := strings.Split(value, "\t")
		if len(parts) != 2 || !isTarget(parts[0]) || !isTarget(parts[1]) {
			return nil, fmt.Errorf("invalid helper inventory entry at line %d", line)
		}
		if seen[parts[0]] {
			return nil, fmt.Errorf("duplicate helper inventory entry: %s", parts[0])
		}
		seen[parts[0]] = true
		helpers = append(helpers, HelperTarget{Name: parts[0], Parent: parts[1]})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read helper inventory: %w", err)
	}
	if len(helpers) == 0 {
		return nil, fmt.Errorf("helper inventory is empty")
	}
	sort.Slice(helpers, func(i, j int) bool { return helpers[i].Name < helpers[j].Name })
	return helpers, nil
}

func isTarget(value string) bool {
	return strings.HasPrefix(value, "Test") || strings.HasPrefix(value, "Example") || strings.HasPrefix(value, "Fuzz")
}

func buildInventory(packages, listed []string, helpers []HelperTarget, shardCount int) (Inventory, error) {
	if shardCount != 8 {
		return Inventory{}, fmt.Errorf("M00 requires exactly eight logical service shards; got %d", shardCount)
	}
	packageSeen := make(map[string]bool)
	serviceCount := 0
	for _, pkg := range packages {
		if pkg == "" {
			return Inventory{}, fmt.Errorf("empty package inventory entry")
		}
		if packageSeen[pkg] {
			return Inventory{}, fmt.Errorf("duplicate package inventory entry: %s", pkg)
		}
		packageSeen[pkg] = true
		if pkg == servicePackage {
			serviceCount++
		}
	}
	if serviceCount != 1 {
		return Inventory{}, fmt.Errorf("service package must appear exactly once; found %d", serviceCount)
	}
	if len(packages) == 1 {
		return Inventory{}, fmt.Errorf("no non-service packages found")
	}

	listedSeen := make(map[string]bool)
	for _, target := range listed {
		if !isTarget(target) {
			continue
		}
		if listedSeen[target] {
			return Inventory{}, fmt.Errorf("duplicate service target: %s", target)
		}
		listedSeen[target] = true
	}
	if len(listedSeen) == 0 {
		return Inventory{}, fmt.Errorf("no service tests, examples, or fuzz targets found")
	}
	helperSet := make(map[string]bool)
	for _, helper := range helpers {
		if !listedSeen[helper.Name] {
			return Inventory{}, fmt.Errorf("helper target missing from service inventory: %s", helper.Name)
		}
		if !listedSeen[helper.Parent] {
			return Inventory{}, fmt.Errorf("helper parent missing from service inventory: %s", helper.Parent)
		}
		if helperSet[helper.Parent] {
			return Inventory{}, fmt.Errorf("helper parent is not an ordinary target: %s", helper.Parent)
		}
		helperSet[helper.Name] = true
	}
	targets := make([]string, 0, len(listedSeen))
	for target := range listedSeen {
		if !helperSet[target] {
			targets = append(targets, target)
		}
	}
	sort.Strings(targets)
	if len(targets) == 0 {
		return Inventory{}, fmt.Errorf("all service targets were excluded as helpers")
	}
	shards := make([][]string, shardCount)
	for index, target := range targets {
		shards[index%shardCount] = append(shards[index%shardCount], target)
	}
	if err := validateShards(targets, shards); err != nil {
		return Inventory{}, err
	}
	return Inventory{Packages: append([]string(nil), packages...), ServiceTargets: targets, Helpers: helpers, Shards: shards}, nil
}

func validateShards(targets []string, shards [][]string) error {
	expected := make(map[string]bool, len(targets))
	for _, target := range targets {
		if expected[target] {
			return fmt.Errorf("duplicate service target: %s", target)
		}
		expected[target] = true
	}
	seen := make(map[string]bool, len(targets))
	for _, shard := range shards {
		for _, target := range shard {
			if !expected[target] {
				return fmt.Errorf("service assignment contains unknown target: %s", target)
			}
			if seen[target] {
				return fmt.Errorf("duplicate service assignment: %s", target)
			}
			seen[target] = true
		}
	}
	for _, target := range targets {
		if !seen[target] {
			return fmt.Errorf("service assignment missing target: %s", target)
		}
	}
	return nil
}

func shardPattern(targets []string) (string, error) {
	if len(targets) == 0 {
		return "", fmt.Errorf("cannot reproduce an empty shard")
	}
	return "^(" + strings.Join(targets, "|") + ")$", nil
}
