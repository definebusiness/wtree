package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunMapsInvalidArgumentsToExitCodeTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if got := run([]string{"unknown-command"}, &stdout, &stderr); got != 2 {
		t.Errorf("run() = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr = %q, want unknown-command diagnostic", stderr.String())
	}
}

func TestRunRejectsTrailingArgumentsAfterVersionOrHelp(t *testing.T) {
	for _, args := range [][]string{
		{"--version", "x"},
		{"-v", "x"},
		{"--help", "x"},
		{"-h", "x"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			if got := run(args, &stdout, &stderr); got != 2 {
				t.Errorf("run() = %d, want 2", got)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "does not accept arguments") {
				t.Errorf("stderr = %q, want invalid-argument diagnostic", stderr.String())
			}
		})
	}
}

func TestRunJSONOperationalFailureDoesNotWriteHumanStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if got := run([]string{"init", t.TempDir(), "--data-dir", t.TempDir(), "--json"}, &stdout, &stderr); got != 1 {
		t.Errorf("run() = %d, want 1", got)
	}
	if !strings.Contains(stdout.String(), `"success":false`) || strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("stdout = %q, want one JSON error object", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunClassifiesLegacyRootErrorForJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"--version", "--json"}, &stdout, &stderr); got != 2 {
		t.Fatalf("run() = %d, want 2", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode JSON error: %v; stdout=%q", err, stdout.String())
	}
	if envelope.Error.Code != "invalid_arguments" {
		t.Fatalf("JSON code = %q, want invalid_arguments", envelope.Error.Code)
	}
}
