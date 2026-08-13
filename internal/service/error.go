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
)

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
