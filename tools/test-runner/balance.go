package main

import (
	"sort"
	"time"
)

const timingWeightMaxAge = 30 * 24 * time.Hour

// assignBalanced is deterministic for a target set, clock value, and valid
// cache. A cold, corrupt, or wholly stale cache intentionally falls back to
// sorted round-robin so cache trouble can never affect inventory coverage.
func assignBalanced(targets []string, weights []TimingWeight, shardCount int, now time.Time) ([][]string, string) {
	shards := make([][]string, shardCount)
	if shardCount < 1 {
		return shards, "fallback"
	}
	valid := make(map[string]time.Duration, len(weights))
	current := make(map[string]bool, len(targets))
	for _, target := range targets {
		current[target] = true
	}
	values := make([]time.Duration, 0, len(weights))
	cutoff := now.Add(-timingWeightMaxAge)
	for _, weight := range weights {
		if !current[weight.Target] || !safeTimingTarget(weight.Target) || weight.Elapsed < 0 || weight.Samples < 1 || weight.ObservedAt.Before(cutoff) || weight.ObservedAt.After(now.Add(time.Minute)) {
			continue
		}
		valid[weight.Target] = weight.Elapsed
		values = append(values, weight.Elapsed)
	}
	ordered := append([]string(nil), targets...)
	sort.Strings(ordered)
	if len(valid) == 0 {
		for index, target := range ordered {
			shards[index%shardCount] = append(shards[index%shardCount], target)
		}
		return shards, "round-robin"
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	median := values[len(values)/2]
	type weightedTarget struct {
		name   string
		weight time.Duration
	}
	weighted := make([]weightedTarget, 0, len(ordered))
	for _, target := range ordered {
		weight, ok := valid[target]
		if !ok {
			weight = median
		}
		weighted = append(weighted, weightedTarget{target, weight})
	}
	sort.Slice(weighted, func(i, j int) bool {
		if weighted[i].weight != weighted[j].weight {
			return weighted[i].weight > weighted[j].weight
		}
		return weighted[i].name < weighted[j].name
	})
	loads := make([]time.Duration, shardCount)
	for _, target := range weighted {
		least := 0
		for index := 1; index < shardCount; index++ {
			if loads[index] < loads[least] {
				least = index
			}
		}
		shards[least] = append(shards[least], target.name)
		loads[least] += target.weight
	}
	for index := range shards {
		sort.Strings(shards[index])
	}
	return shards, "lpt"
}

func balancedInventory(inventory Inventory, cache *timingCache, now time.Time) (Inventory, string, error) {
	if len(inventory.ServiceTargets) == 0 {
		return inventory, "round-robin", nil
	}
	var weights []TimingWeight
	if cache != nil && cache.state == "loaded" {
		weights = cache.weights
	}
	shards, strategy := assignBalanced(inventory.ServiceTargets, weights, len(inventory.Shards), now)
	if err := validateShards(inventory.ServiceTargets, shards); err != nil {
		return Inventory{}, "", err
	}
	inventory.Shards = shards
	return inventory, strategy, nil
}
