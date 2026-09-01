package service

import (
	"errors"
	"fmt"
)

// ErrorKind is the stable application error taxonomy exposed by the CLI.
type ErrorKind string

const (
	ErrorInternal           ErrorKind = "internal"
	ErrorInvalidArguments   ErrorKind = "invalid_arguments"
	ErrorProjectNotFound    ErrorKind = "project_not_found"
	ErrorWorkspaceNotFound  ErrorKind = "workspace_not_found"
	ErrorValidation         ErrorKind = "validation"
	ErrorGit                ErrorKind = "git"
	ErrorDirtyWorkspace     ErrorKind = "dirty_workspace"
	ErrorConflict           ErrorKind = "conflict"
	ErrorRollbackIncomplete ErrorKind = "rollback_incomplete"
	ErrorSetupIncomplete    ErrorKind = "setup_incomplete"
)

type SetupIncompleteDetails struct {
	Operation        string          `json:"operation"`
	CoreStatus       string          `json:"coreStatus"`
	SetupStatus      string          `json:"setupStatus"`
	Event            string          `json:"event"`
	HookID           string          `json:"hookId,omitempty"`
	Repository       string          `json:"repository,omitempty"`
	FailureKind      HookFailureKind `json:"failureKind"`
	ExitCode         *int            `json:"exitCode,omitempty"`
	Timeout          bool            `json:"timeout"`
	CompletedHookIDs []string        `json:"completedHookIds"`
	RetryCommand     string          `json:"retryCommand"`
}

type SetupIncompleteError struct {
	Details SetupIncompleteDetails
	Cause   error
}

func (e *SetupIncompleteError) Error() string {
	if e == nil {
		return "setup incomplete"
	}
	operation := e.Details.Operation
	if operation == "" {
		operation = "create"
	}
	return fmt.Sprintf("%s setup incomplete for %s; retry %s", operation, e.Details.Event, e.Details.RetryCommand)
}

// Unwrap preserves a cancellation or deadline cause while callers retain the
// stable setup-incomplete envelope and its committed-core retry details.
func (e *SetupIncompleteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func SetupIncompleteFrom(err error) (SetupIncompleteDetails, bool) {
	var setup *SetupIncompleteError
	if !errors.As(err, &setup) || setup == nil {
		return SetupIncompleteDetails{}, false
	}
	details := setup.Details
	details.CompletedHookIDs = append([]string{}, details.CompletedHookIDs...)
	return details, true
}

// Error wraps an operational cause in a stable application category.
type Error struct {
	Kind  ErrorKind
	Cause error
}

func NewError(kind ErrorKind, cause error) *Error {
	return &Error{Kind: kind, Cause: cause}
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

// CleanRollbackError marks a failed operation whose completed effects were
// fully undone. Renderers can expose this result without parsing text.
type CleanRollbackError struct{ Cause error }

func NewCleanRollbackError(cause error) *CleanRollbackError { return &CleanRollbackError{Cause: cause} }

func (e *CleanRollbackError) Error() string {
	if e.Cause == nil {
		return "rollback complete"
	}
	return e.Cause.Error()
}

func (e *CleanRollbackError) Unwrap() error { return e.Cause }

// HasCleanRollback reports whether a failure is known to have rolled every
// completed reversible effect back successfully.
func HasCleanRollback(err error) bool {
	var rollback *CleanRollbackError
	return errors.As(err, &rollback)
}
