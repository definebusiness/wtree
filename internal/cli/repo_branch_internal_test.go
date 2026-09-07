package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/definebusiness/wtree/internal/service"
)

type repoBranchFailingWriter struct{}

var errRepoBranchOutput = errors.New("injected output failure")

func (repoBranchFailingWriter) Write([]byte) (int, error) {
	return 0, errRepoBranchOutput
}

func TestRenderRepoBranchSuccessPropagatesOutputFailure(t *testing.T) {
	err := renderRepoBranchSuccess(repoBranchFailingWriter{}, service.RepositoryBranchResult{Version: 1, Operation: "repo-branch", Status: "completed", ProjectID: "project", RepositoryID: "tools", PreviousBaseline: "main", Baseline: "next"})
	if !errors.Is(err, errRepoBranchOutput) {
		t.Fatalf("render error = %v", err)
	}
}

func TestRepoBranchJSONOutputFailureDoesNotEmitFallbackDocument(t *testing.T) {
	var stderr bytes.Buffer
	err := Execute([]string{"repo", "branch", "tools", "--json"}, repoBranchFailingWriter{}, &stderr)
	if !errors.Is(err, errRepoBranchOutput) || ExitCode(err) != 1 || stderr.String() != "" {
		t.Fatalf("JSON output failure = %v exit=%d stderr=%q", err, ExitCode(err), stderr.String())
	}
}
