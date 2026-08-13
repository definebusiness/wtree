package cli_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/marcel/wtree/internal/cli"
	"github.com/marcel/wtree/internal/service"
	"github.com/marcel/wtree/internal/testutil"
)

func TestExitCodeMapsEveryApplicationCategory(t *testing.T) {
	tests := []struct {
		name string
		kind service.ErrorKind
		want int
	}{
		{name: "internal", kind: service.ErrorInternal, want: 1},
		{name: "invalid arguments", kind: service.ErrorInvalidArguments, want: 2},
		{name: "project", kind: service.ErrorProjectNotFound, want: 3},
		{name: "workspace", kind: service.ErrorWorkspaceNotFound, want: 4},
		{name: "validation", kind: service.ErrorValidation, want: 5},
		{name: "git", kind: service.ErrorGit, want: 6},
		{name: "dirty", kind: service.ErrorDirtyWorkspace, want: 7},
		{name: "conflict", kind: service.ErrorConflict, want: 8},
		{name: "recovery", kind: service.ErrorRollbackIncomplete, want: 9},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cli.ExitCode(service.NewError(test.kind, errors.New("cause"))); got != test.want {
				t.Fatalf("ExitCode() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRawCLIErrorHasMatchingJSONCodeAndExitCode(t *testing.T) {
	result := testutil.RunCommand(t, cli.Execute, "doctor", "--dry-run", "--json")
	if result.Err == nil {
		t.Fatal("doctor --dry-run unexpectedly succeeded")
	}
	if got := cli.ExitCode(result.Err); got != 2 {
		t.Fatalf("ExitCode() = %d, want 2", got)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &envelope); err != nil {
		t.Fatalf("decode JSON error: %v; output=%q", err, result.Stdout)
	}
	if envelope.Error.Code != "invalid_arguments" || strings.Contains(result.Stderr, "wtree:") {
		t.Fatalf("JSON result = %#v, want invalid_arguments with no diagnostic", result)
	}
}
