// Package testutil provides hermetic helpers shared by wtree tests.
package testutil

import (
	"bytes"
	"io"
	"testing"
)

// Command is the stream-oriented command boundary used by CLI tests.
type Command func(args []string, stdout, stderr io.Writer) error

// Result captures a command invocation without writing to the test process.
type Result struct {
	Stdout string
	Stderr string
	Err    error
}

// RunCommand executes command with captured output.
func RunCommand(t testing.TB, command Command, args ...string) Result {
	t.Helper()

	var stdout, stderr bytes.Buffer
	err := command(args, &stdout, &stderr)
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
}

// Setenv sets an environment variable for the lifetime of a test and restores
// its original value automatically when the test finishes.
func Setenv(t testing.TB, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}
