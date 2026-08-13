// Package render owns the human and JSON presentation boundary.
package render

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/marcel/wtree/internal/service"
)

// JSON writes exactly one JSON value followed by a newline.
func JSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

// Line writes one human-readable line.
func Line(writer io.Writer, value string) error {
	_, err := io.WriteString(writer, value+"\n")
	return err
}

// Table writes rows with padding derived from the widest value in each
// column. Callers provide cells rather than terminal-dependent tab stops.
func Table(writer io.Writer, rows [][]string) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	for _, row := range rows {
		if _, err := io.WriteString(table, strings.Join(row, "\t")+"\n"); err != nil {
			return err
		}
	}
	return table.Flush()
}

// ErrorEnvelope is the stable JSON representation of an operation failure.
type ErrorEnvelope struct {
	Success bool         `json:"success"`
	Error   ErrorDetails `json:"error"`
}

type ErrorDetails struct {
	Code     string           `json:"code"`
	Message  string           `json:"message"`
	Rollback *RollbackDetails `json:"rollback,omitempty"`
}

type RollbackDetails struct {
	Complete bool `json:"complete"`
}

// JSONError renders a failure without writing diagnostic text to stderr.
func JSONError(writer io.Writer, err error) error {
	details := ErrorDetails{Code: ErrorCode(err), Message: err.Error()}
	if service.HasCleanRollback(err) {
		details.Rollback = &RollbackDetails{Complete: true}
	}
	return JSON(writer, ErrorEnvelope{Success: false, Error: details})
}

func ErrorCode(err error) string {
	var application *service.Error
	if errors.As(err, &application) {
		return string(application.Kind)
	}
	return string(service.ErrorInternal)
}
