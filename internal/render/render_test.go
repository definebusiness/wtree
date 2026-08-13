package render_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/marcel/wtree/internal/render"
	"github.com/marcel/wtree/internal/service"
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
