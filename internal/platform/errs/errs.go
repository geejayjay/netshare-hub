// Package errs is the single error model for the whole backend.
//
// Services return *errs.E. They never think about HTTP: the httpx package
// turns a Kind into a status code and a problem+json body in one place.
package errs

import (
	"errors"
	"fmt"
)

// Kind says what category of failure happened. httpx maps it to an HTTP status.
type Kind int

const (
	KindInternal        Kind = iota // bug or unexpected failure (500)
	KindInvalid                     // the request was understood but is not valid (422)
	KindUnauthenticated             // not logged in (401)
	KindForbidden                   // logged in, but not allowed (403)
	KindNotFound                    // the thing does not exist (404)
	KindConflict                    // clashes with current state (409)
	KindRateLimited                 // too many attempts (429)
	KindUnavailable                 // a dependency is down (503)
)

// Code is a stable, machine-readable identifier such as "USER_NOT_FOUND".
// The frontend switches on it to pick a translated message.
type Code string

// Generic codes. Modules declare their own specific codes in their errors.go.
const (
	CodeInternal         Code = "INTERNAL"
	CodeValidationFailed Code = "VALIDATION_FAILED"
	CodeBadRequest       Code = "BAD_REQUEST"
	CodeUnauthenticated  Code = "UNAUTHENTICATED"
	CodeForbidden        Code = "FORBIDDEN"
	CodeCSRFInvalid      Code = "CSRF_INVALID"
	CodeNotFound         Code = "NOT_FOUND"
	CodeConflict         Code = "CONFLICT"
	CodeRateLimited      Code = "RATE_LIMITED"
	CodeUnavailable      Code = "UNAVAILABLE"
)

// FieldError points at one invalid input field.
type FieldError struct {
	Field   string `json:"field" doc:"Name of the invalid field, e.g. \"username\"."`
	Code    string `json:"code" doc:"Machine-readable reason, e.g. \"required\" or \"too_short\"."`
	Message string `json:"message" doc:"Human-readable explanation, safe to show next to the field."`
}

// E is the error type every service returns.
type E struct {
	Kind    Kind
	Code    Code
	Message string       // safe to show to the user
	Fields  []FieldError // only for KindInvalid
	Cause   error        // internal detail: logged, never sent to the client
	// RetryAfterSeconds is set for KindRateLimited.
	RetryAfterSeconds int
}

func (e *E) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *E) Unwrap() error { return e.Cause }

// WithCause attaches the underlying error for the logs.
func (e *E) WithCause(cause error) *E {
	e.Cause = cause
	return e
}

func newE(kind Kind, code Code, msg string) *E {
	return &E{Kind: kind, Code: code, Message: msg}
}

func Invalid(code Code, msg string, fields ...FieldError) *E {
	e := newE(KindInvalid, code, msg)
	e.Fields = fields
	return e
}
func Unauthenticated(code Code, msg string) *E { return newE(KindUnauthenticated, code, msg) }
func Forbidden(code Code, msg string) *E       { return newE(KindForbidden, code, msg) }
func NotFound(code Code, msg string) *E        { return newE(KindNotFound, code, msg) }
func Conflict(code Code, msg string) *E        { return newE(KindConflict, code, msg) }
func Unavailable(code Code, msg string) *E     { return newE(KindUnavailable, code, msg) }

func RateLimited(msg string, retryAfterSeconds int) *E {
	e := newE(KindRateLimited, CodeRateLimited, msg)
	e.RetryAfterSeconds = retryAfterSeconds
	return e
}

// Internal wraps an unexpected error. The cause is logged; the client only
// sees a generic message.
func Internal(cause error) *E {
	return newE(KindInternal, CodeInternal, "Something went wrong on the server.").WithCause(cause)
}

// From converts any error into an *E. Unknown errors become Internal.
func From(err error) *E {
	if err == nil {
		return nil
	}
	var e *E
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}

// Is reports whether err is an *E with the given code.
func Is(err error, code Code) bool {
	var e *E
	return errors.As(err, &e) && e.Code == code
}
