package render_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
)

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestJSONErrorEnvelopeIsStableAndStructural(t *testing.T) {
	var output bytes.Buffer
	err := render.JSONError(&output, service.NewError(service.ErrorValidation, errors.New("invalid value")))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Success || envelope.Error.Code != "validation" || envelope.Error.Message == "" {
		t.Fatalf("JSONError() = %s", output.String())
	}
}

func TestJSONErrorIncludesCleanRollbackOutcome(t *testing.T) {
	var output bytes.Buffer
	if err := render.JSONError(&output, service.NewError(service.ErrorGit, service.NewCleanRollbackError(errors.New("add worktree failed")))); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code     string `json:"code"`
			Rollback struct {
				Complete bool `json:"complete"`
			} `json:"rollback"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Success || envelope.Error.Code != "git" || !envelope.Error.Rollback.Complete {
		t.Fatalf("JSONError() = %s", output.String())
	}
}

func TestJSONErrorIncludesOnlyTypedWorkspaceSelectionDetails(t *testing.T) {
	var output bytes.Buffer
	selection := &service.WorkspaceSelectionError{Query: `feature/"quoted"`, Mode: service.WorkspaceSelectionSubstring, Candidates: []service.WorkspaceSelectionCandidate{{Name: "feature/alpha", ID: "alpha"}, {Name: "feature/beta", ID: "beta"}}}
	if err := render.JSONError(&output, service.NewError(service.ErrorConflict, selection)); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Selection *struct {
				Query      string `json:"query"`
				Mode       string `json:"mode"`
				Candidates []struct {
					Name string `json:"name"`
					ID   string `json:"id"`
				} `json:"candidates"`
			} `json:"selection"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "conflict" || envelope.Error.Selection == nil || envelope.Error.Selection.Query != `feature/"quoted"` || envelope.Error.Selection.Mode != "substring" || len(envelope.Error.Selection.Candidates) != 2 || envelope.Error.Selection.Candidates[0].Name != "feature/alpha" || envelope.Error.Selection.Candidates[1].ID != "beta" {
		t.Fatalf("selection envelope = %s", output.String())
	}

	output.Reset()
	if err := render.JSONError(&output, service.NewError(service.ErrorValidation, errors.New("ordinary failure"))); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), `"selection"`) {
		t.Fatalf("ordinary error gained selection detail: %s", output.String())
	}
}

func TestRenderPropagatesWriterFailures(t *testing.T) {
	want := errors.New("broken pipe")
	if err := render.JSON(failingWriter{err: want}, map[string]string{"key": "value"}); !errors.Is(err, want) {
		t.Fatalf("JSON() error = %v, want %v", err, want)
	}
	if err := render.Line(failingWriter{err: want}, "value"); !errors.Is(err, want) {
		t.Fatalf("Line() error = %v, want %v", err, want)
	}
	if err := render.Table(failingWriter{err: want}, [][]string{{"value"}}); !errors.Is(err, want) {
		t.Fatalf("Table() error = %v, want %v", err, want)
	}
}

func TestTableAlignsColumnsToTheirWidestValues(t *testing.T) {
	var output bytes.Buffer
	rows := [][]string{
		{"REPOSITORY", "BRANCH", "STATUS"},
		{"root", "main", "modified"},
		{"backend", "feature/customer-search", "clean"},
	}
	if err := render.Table(&output, rows); err != nil {
		t.Fatal(err)
	}
	want := "REPOSITORY  BRANCH                   STATUS\n" +
		"root        main                     modified\n" +
		"backend     feature/customer-search  clean\n"
	if output.String() != want {
		t.Fatalf("Table() = %q, want %q", output.String(), want)
	}
}
