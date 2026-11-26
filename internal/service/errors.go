package service

import (
	"errors"
	"fmt"
)

// Error kinds returned by the service layer. Adapters map them to transport codes.
var (
	ErrInvalidArgument    = errors.New("invalid argument")
	ErrNotFound           = errors.New("not found")
	ErrFailedPrecondition = errors.New("failed precondition")
	ErrPermissionDenied   = errors.New("permission denied")
	ErrAborted            = errors.New("aborted")
	ErrUnavailable        = errors.New("unavailable")
	ErrInternal           = errors.New("internal")
)

type codedError struct {
	kind error
	msg  string
}

func (e *codedError) Error() string { return e.msg }
func (e *codedError) Unwrap() error { return e.kind }

func newErr(kind error, format string, args ...any) error {
	return &codedError{kind: kind, msg: fmt.Sprintf(format, args...)}
}

const actionsDisabledMsg = "container actions are disabled: set allow_actions = true under [actions] in asterism.toml (or ASTERISM_ALLOW_ACTIONS=true) and restart Asterism"
