package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const timingFormat = "wtree-test-runner-timing-v1"

type TimingWeight struct {
	Target     string
	Elapsed    time.Duration
	Samples    int
	ObservedAt time.Time
}

// timingCache is deliberately private to the runner. Its path is constructed
// only after every runner-owned directory below the caller's cache root has
// been created and checked for symlinks and permissive modes.
type timingCache struct {
	path    string
	weights []TimingWeight
	state   string
}

func openTimingCache(root, mode string) (*timingCache, string) {
	path, err := privateTimingPath(root, mode)
	if err != nil {
		return nil, "timing cache fallback: unavailable"
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &timingCache{path: path, state: "cold"}, ""
	}
	if err != nil {
		return &timingCache{path: path, state: "fallback"}, "timing cache fallback: unreadable"
	}
	weights, err := parseTiming(data)
	if err != nil {
		return &timingCache{path: path, state: "fallback"}, "timing cache fallback: invalid"
	}
	return &timingCache{path: path, weights: weights, state: "loaded"}, ""
}

func parseTiming(data []byte) ([]TimingWeight, error) {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 0 || lines[0] != timingFormat {
		return nil, fmt.Errorf("unsupported timing cache format")
	}
	weights := make([]TimingWeight, 0, len(lines)-1)
	seen := make(map[string]bool)
	for lineNumber, line := range lines[1:] {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || !safeTimingTarget(fields[0]) || seen[fields[0]] {
			return nil, fmt.Errorf("invalid timing cache entry at line %d", lineNumber+2)
		}
		nanoseconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || nanoseconds < 0 {
			return nil, fmt.Errorf("invalid timing duration at line %d", lineNumber+2)
		}
		samples, err := strconv.Atoi(fields[2])
		if err != nil || samples < 1 {
			return nil, fmt.Errorf("invalid timing samples at line %d", lineNumber+2)
		}
		observed, err := time.Parse(time.RFC3339Nano, fields[3])
		if err != nil {
			return nil, fmt.Errorf("invalid timing observation at line %d", lineNumber+2)
		}
		seen[fields[0]] = true
		weights = append(weights, TimingWeight{fields[0], time.Duration(nanoseconds), samples, observed})
	}
	return weights, nil
}

func safeTimingTarget(target string) bool {
	// A target name can legitimately describe a secret-handling behavior. Only
	// reject value-shaped or path-shaped input that could carry actual data.
	return isTarget(target) && !strings.ContainsAny(target, "\t\n\r=:/\\")
}

func formatTiming(weights []TimingWeight) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString(timingFormat + "\n")
	for _, weight := range weights {
		if !safeTimingTarget(weight.Target) || weight.Elapsed < 0 || weight.Samples < 1 || weight.ObservedAt.IsZero() {
			return nil, fmt.Errorf("unsafe timing weight for %q", weight.Target)
		}
		fmt.Fprintf(&buffer, "%s\t%d\t%d\t%s\n", weight.Target, weight.Elapsed.Nanoseconds(), weight.Samples, weight.ObservedAt.UTC().Format(time.RFC3339Nano))
	}
	return buffer.Bytes(), nil
}

func writeTimingAtomic(path string, weights []TimingWeight) error {
	data, err := formatTiming(weights)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("stat timing cache directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe timing cache directory")
	}
	temporary, err := os.CreateTemp(directory, ".timing-*")
	if err != nil {
		return fmt.Errorf("create timing cache temporary: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("restrict timing cache temporary: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write timing cache temporary: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync timing cache temporary: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close timing cache temporary: %w", err)
	}
	return replaceTimingFile(temporaryName, path, directory)
}

func writeTimingObservations(cache *timingCache, results []TargetResult, scheduled []string, observedAt time.Time) error {
	if cache == nil {
		return nil
	}
	scheduledSet := make(map[string]bool, len(scheduled))
	for _, target := range scheduled {
		scheduledSet[target] = true
	}
	byTarget := make(map[string]TimingWeight, len(cache.weights)+len(results))
	for _, weight := range cache.weights {
		if scheduledSet[weight.Target] {
			byTarget[weight.Target] = weight
		}
	}
	for _, result := range results {
		if result.Failed || !scheduledSet[result.Target] {
			continue
		}
		weight := byTarget[result.Target]
		weight.Target = result.Target
		weight.Elapsed = result.Elapsed
		weight.Samples++
		weight.ObservedAt = observedAt.UTC()
		byTarget[result.Target] = weight
	}
	weights := make([]TimingWeight, 0, len(byTarget))
	for _, weight := range byTarget {
		weights = append(weights, weight)
	}
	sort.Slice(weights, func(i, j int) bool { return weights[i].Target < weights[j].Target })
	if err := writeTimingAtomic(cache.path, weights); err != nil {
		return err
	}
	cache.weights = weights
	cache.state = "written"
	return nil
}

// writePartialTimingObservations records failed/cancelled unit measurements in
// a separate runner-owned sidecar. Complete weights remain authoritative for
// balancing, so a partial sample can never displace a last known good value.
func writePartialTimingObservations(cache *timingCache, results []TargetResult, observedAt time.Time) error {
	if cache == nil || cache.path == "" {
		return nil
	}
	weights := make([]TimingWeight, 0, len(results))
	for _, result := range results {
		if !safeTimingTarget(result.Target) || result.Elapsed < 0 {
			continue
		}
		weights = append(weights, TimingWeight{Target: result.Target, Elapsed: result.Elapsed, Samples: 1, ObservedAt: observedAt.UTC()})
	}
	if len(weights) == 0 {
		return nil
	}
	sort.Slice(weights, func(i, j int) bool { return weights[i].Target < weights[j].Target })
	return writeTimingAtomic(cache.path+".partial", weights)
}
