package testutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestRunCommandCapturesStreamsAndError(t *testing.T) {
	wantErr := errors.New("command failed")
	result := RunCommand(t, func(_ []string, stdout, stderr io.Writer) error {
		fmt.Fprint(stdout, "standard output")
		fmt.Fprint(stderr, "standard error")
		return wantErr
	})

	if result.Stdout != "standard output" {
		t.Errorf("stdout = %q", result.Stdout)
	}
	if result.Stderr != "standard error" {
		t.Errorf("stderr = %q", result.Stderr)
	}
	if !errors.Is(result.Err, wantErr) {
		t.Errorf("error = %v, want %v", result.Err, wantErr)
	}
}

func TestSetenvRestoresEnvironmentAfterSubtest(t *testing.T) {
	const key = "WTREE_TESTUTIL_ISOLATED_ENV"
	t.Setenv(key, "outside")

	t.Run("isolated", func(t *testing.T) {
		Setenv(t, key, "inside")
		if got := os.Getenv(key); got != "inside" {
			t.Errorf("environment value = %q, want inside", got)
		}
	})

	if got := os.Getenv(key); got != "outside" {
		t.Errorf("environment value after subtest = %q, want outside", got)
	}
}
