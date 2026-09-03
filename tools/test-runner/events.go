package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type goEvent struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"`
	Output  string    `json:"Output"`
}

type TargetResult struct {
	Target  string
	Elapsed time.Duration
	Failed  bool
	Output  string
}

// parseEvents extracts Go's structured test events. Command success remains a
// process exit-status decision; text such as "PASS" is never authoritative.
func parseEvents(reader io.Reader) ([]TargetResult, error) {
	results := make(map[string]*TargetResult)
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event goEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode go JSON event: %w", err)
		}
		if event.Test == "" {
			continue
		}
		result := results[event.Test]
		if result == nil {
			result = &TargetResult{Target: event.Test}
			results[event.Test] = result
		}
		if event.Output != "" {
			result.Output += event.Output
		}
		switch event.Action {
		case "pass", "fail":
			result.Elapsed = time.Duration(event.Elapsed * float64(time.Second))
			result.Failed = event.Action == "fail"
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read go JSON events: %w", err)
	}
	values := make([]TargetResult, 0, len(results))
	for _, result := range results {
		values = append(values, *result)
	}
	// Names are stable and make summaries independent of Go's output ordering.
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if strings.Compare(values[right].Target, values[left].Target) < 0 {
				values[left], values[right] = values[right], values[left]
			}
		}
	}
	return values, nil
}
