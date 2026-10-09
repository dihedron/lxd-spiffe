// Package exit defines the exit codes of lxd-probe (spec: Configuration →
// Command line → Exit codes) and an error type that carries one.
package exit

import (
	"errors"
	"strconv"
)

// Exit codes of lxd-probe.
const (
	// Success: observations recorded; they may contain unexpected outcomes.
	Success = 0
	// Internal is any error not listed below.
	Internal = 1
	// Usage is a command line error.
	Usage = 2
	// Connection is a connection or TLS failure, including a pin mismatch.
	Connection = 3
	// Auth is an authentication or authorization failure.
	Auth = 4
	// Refused is a refusal by the safety rules (PRN-01, PRN-02).
	Refused = 5
	// Sanitize is a failed sanitization check.
	Sanitize = 6
	// Timeout is an expired deadline.
	Timeout = 7
	// Strict is --strict with at least one unexpected outcome.
	Strict = 8
)

// Error is an error that carries the exit code of the process.
type Error struct {
	// Code is the exit code.
	Code int
	// Err is the cause; it may be nil.
	Err error
}

// New returns an error carrying the given exit code.
func New(code int, err error) *Error {
	return &Error{Code: code, Err: err}
}

// Error returns the message of the cause.
func (e *Error) Error() string {
	if e.Err == nil {
		return "exit code " + strconv.Itoa(e.Code)
	}
	return e.Err.Error()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error {
	return e.Err
}

// Code returns the exit code for err: 0 for nil, the code of the first
// *Error in its chain, Internal otherwise.
func Code(err error) int {
	if err == nil {
		return Success
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Internal
}
