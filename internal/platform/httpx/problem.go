package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/logger"
)

// Problem is the one error body the API ever returns (RFC 9457 problem+json).
//
//	{
//	  "type": "about:blank", "title": "Unprocessable Entity", "status": 422,
//	  "code": "VALIDATION_FAILED", "detail": "Some fields need your attention.",
//	  "requestId": "a1b2c3d4", "errors": [{"field": "username", "code": "taken", "message": "..."}]
//	}
type Problem struct {
	Type      string            `json:"type" doc:"Always \"about:blank\"; use \"code\" to identify the error."`
	Title     string            `json:"title" doc:"Short summary of the HTTP status."`
	Status    int               `json:"status" doc:"HTTP status code."`
	Code      errs.Code         `json:"code" doc:"Stable machine-readable error code."`
	Detail    string            `json:"detail,omitempty" doc:"Human-readable explanation, safe to show to the user."`
	RequestID string            `json:"requestId,omitempty" doc:"Quote this ID to find the matching server logs."`
	Errors    []errs.FieldError `json:"errors,omitempty" nullable:"false" doc:"Per-field validation errors."`

	retryAfter int
}

func (p *Problem) Error() string  { return string(p.Code) + ": " + p.Detail }
func (p *Problem) GetStatus() int { return p.Status }

// ContentType makes Huma send application/problem+json.
func (p *Problem) ContentType(string) string { return "application/problem+json" }

// GetHeaders lets Huma add Retry-After for rate-limit errors.
func (p *Problem) GetHeaders() http.Header {
	h := http.Header{}
	if p.retryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(p.retryAfter))
	}
	return h
}

// kindStatus is the only place a Kind becomes an HTTP status.
var kindStatus = map[errs.Kind]int{
	errs.KindInternal:            http.StatusInternalServerError,
	errs.KindInvalid:             http.StatusUnprocessableEntity,
	errs.KindUnauthenticated:     http.StatusUnauthorized,
	errs.KindForbidden:           http.StatusForbidden,
	errs.KindNotFound:            http.StatusNotFound,
	errs.KindConflict:            http.StatusConflict,
	errs.KindRateLimited:         http.StatusTooManyRequests,
	errs.KindUnavailable:         http.StatusServiceUnavailable,
	errs.KindRangeNotSatisfiable: http.StatusRequestedRangeNotSatisfiable,
}

// FromError converts any error into a Problem and logs it. Internal causes
// are logged in full and never sent to the client.
func FromError(ctx context.Context, err error) *Problem {
	if p, ok := err.(*Problem); ok {
		return p
	}
	e := errs.From(err)
	status := kindStatus[e.Kind]

	if status >= 500 {
		slog.ErrorContext(ctx, "request failed", "code", e.Code, "err", e.Cause)
	} else {
		slog.InfoContext(ctx, "request rejected", "code", e.Code, "status", status)
	}

	return &Problem{
		Type:       "about:blank",
		Title:      http.StatusText(status),
		Status:     status,
		Code:       e.Code,
		Detail:     e.Message,
		RequestID:  logger.RequestID(ctx),
		Errors:     e.Fields,
		retryAfter: e.RetryAfterSeconds,
	}
}

// WriteProblem writes a Problem from plain net/http code (middleware, 404s).
func WriteProblem(w http.ResponseWriter, p *Problem) {
	for k, v := range p.GetHeaders() {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// statusCode gives errors raised by the framework itself (bad JSON, wrong
// content type...) a stable code.
var statusCode = map[int]errs.Code{
	http.StatusBadRequest:            errs.CodeBadRequest,
	http.StatusUnauthorized:          errs.CodeUnauthenticated,
	http.StatusForbidden:             errs.CodeForbidden,
	http.StatusNotFound:              errs.CodeNotFound,
	http.StatusMethodNotAllowed:      "METHOD_NOT_ALLOWED",
	http.StatusNotAcceptable:         "NOT_ACCEPTABLE",
	http.StatusRequestTimeout:        "REQUEST_TIMEOUT",
	http.StatusRequestEntityTooLarge: "PAYLOAD_TOO_LARGE",
	http.StatusUnsupportedMediaType:  "UNSUPPORTED_MEDIA_TYPE",
	http.StatusUnprocessableEntity:   errs.CodeValidationFailed,
}

// fromHuma converts an error raised by Huma (mostly request validation) into
// our Problem shape, so clients only ever see one format.
func fromHuma(ctx context.Context, status int, msg string, details []error) *Problem {
	if status == 0 { // Huma asks for an empty example to build the OpenAPI schema.
		return &Problem{}
	}

	p := &Problem{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Code:      statusCode[status],
		RequestID: logger.RequestID(ctx),
	}
	if p.Code == "" {
		p.Code = errs.CodeInternal
	}

	if status >= 500 {
		slog.ErrorContext(ctx, "framework error", "status", status, "msg", msg, "errs", details)
		p.Code = errs.CodeInternal
		p.Detail = "Something went wrong on the server."
		return p
	}

	for _, d := range details {
		if fe, ok := fieldError(d); ok {
			p.Errors = append(p.Errors, fe)
		}
	}
	if len(p.Errors) > 0 {
		p.Code = errs.CodeValidationFailed
		p.Detail = "Some fields need your attention."
	} else {
		p.Detail = sentence(msg)
	}
	slog.InfoContext(ctx, "request rejected", "code", p.Code, "status", status)
	return p
}

// fieldError turns one Huma validation detail into our FieldError.
func fieldError(err error) (errs.FieldError, bool) {
	detailer, ok := err.(huma.ErrorDetailer)
	if !ok {
		return errs.FieldError{}, false
	}
	d := detailer.ErrorDetail()
	field := fieldName(d.Location)
	code, message := splitMessage(d.Message)

	// "required" errors point at the parent object and name the property.
	if name, ok := strings.CutPrefix(message, requiredMarker); ok {
		if field != "" {
			field += "."
		}
		return errs.FieldError{Field: field + name, Code: "required", Message: "This field is required."}, true
	}
	return errs.FieldError{Field: field, Code: code, Message: message}, true
}

// fieldName turns "body.address.city" into "address.city" and "query.page"
// into "page", which is what a form or a caller actually calls the field.
func fieldName(location string) string {
	for _, prefix := range []string{"body", "query", "path", "header", "cookie"} {
		if location == prefix {
			return ""
		}
		if rest, ok := strings.CutPrefix(location, prefix+"."); ok {
			return rest
		}
	}
	return location
}

func sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// installErrors makes Huma build every error through fromHuma.
func installErrors() {
	huma.NewError = func(status int, msg string, details ...error) huma.StatusError {
		return fromHuma(context.Background(), status, msg, details)
	}
	huma.NewErrorWithContext = func(hctx huma.Context, status int, msg string, details ...error) huma.StatusError {
		ctx := context.Background()
		if hctx != nil {
			ctx = hctx.Context()
		}
		return fromHuma(ctx, status, msg, details)
	}
}
