// Package apperr defines the stable error vocabulary shared by every layer of
// ManjuFlow. Domain and service code always returns one of these codes so the
// HTTP edge can map failures without inspecting driver specific errors.
package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Code is a stable, machine readable failure classification. Values are part of
// the public HTTP contract and must not be renamed without a migration note.
type Code string

const (
	CodeInvalidArgument    Code = "invalid_argument"
	CodeUnauthenticated    Code = "unauthenticated"
	CodePermissionDenied   Code = "permission_denied"
	CodeNotFound           Code = "not_found"
	CodeConflict           Code = "conflict"
	CodeFailedPrecondition Code = "failed_precondition"
	CodeExhausted          Code = "resource_exhausted"
	CodeCanceled           Code = "canceled"
	CodeDeadlineExceeded   Code = "deadline_exceeded"
	CodeInternal           Code = "internal"
)

// Error carries a stable code, an operator readable message, optional detail
// pairs and the wrapped cause. The cause is preserved for errors.Is/errors.As
// so transport layers can still recognise sentinel values such as
// context.Canceled.
type Error struct {
	Code    Code
	Message string
	Details map[string]string

	cause error
}

// New builds an error without a cause.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a stable code to an existing error while keeping the chain.
func Wrap(cause error, code Code, format string, args ...any) *Error {
	if cause == nil {
		return nil
	}
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), cause: cause}
}

// With records an additional detail pair and returns the same error for
// chaining. Details are copied on read so callers cannot mutate shared state.
func (e *Error) With(key, value string) *Error {
	if e.Details == nil {
		e.Details = map[string]string{}
	}
	e.Details[key] = value
	return e
}

func (e *Error) Error() string {
	var sb strings.Builder
	sb.WriteString(string(e.Code))
	sb.WriteString(": ")
	sb.WriteString(e.Message)
	if len(e.Details) > 0 {
		keys := make([]string, 0, len(e.Details))
		for key := range e.Details {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		sb.WriteString(" (")
		for i, key := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(key)
			sb.WriteString("=")
			sb.WriteString(e.Details[key])
		}
		sb.WriteString(")")
	}
	if e.cause != nil {
		sb.WriteString(": ")
		sb.WriteString(e.cause.Error())
	}
	return sb.String()
}

// Unwrap exposes the wrapped cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.cause }

// DetailsCopy returns an isolated copy of the detail pairs.
func (e *Error) DetailsCopy() map[string]string {
	if len(e.Details) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(e.Details))
	for key, value := range e.Details {
		out[key] = value
	}
	return out
}

// As extracts the first *Error in the chain.
func As(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// CodeOf classifies any error. Context failures are mapped explicitly so a
// cancelled request is never reported as an internal fault.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return CodeCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeDeadlineExceeded
	}
	if typed, ok := As(err); ok {
		return typed.Code
	}
	return CodeInternal
}

// IsCode reports whether err classifies as the given code.
func IsCode(err error, code Code) bool { return CodeOf(err) == code }

// Message returns a safe, operator readable message for err.
func Message(err error) string {
	if err == nil {
		return ""
	}
	if typed, ok := As(err); ok {
		return typed.Message
	}
	switch CodeOf(err) {
	case CodeCanceled:
		return "request was cancelled before completion"
	case CodeDeadlineExceeded:
		return "request exceeded its deadline"
	default:
		return "internal error"
	}
}

// HTTPStatus maps a code to its transport status.
func HTTPStatus(code Code) int {
	switch code {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodePermissionDenied:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeFailedPrecondition:
		return http.StatusUnprocessableEntity
	case CodeExhausted:
		return http.StatusTooManyRequests
	case CodeCanceled:
		return 499
	case CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// FromContext converts a cancelled or expired context into a classified error.
// It returns nil while the context is still usable, which lets callers guard
// long running steps with a single line.
func FromContext(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(err, CodeDeadlineExceeded, "operation deadline exceeded")
	}
	return Wrap(err, CodeCanceled, "operation cancelled by caller")
}
