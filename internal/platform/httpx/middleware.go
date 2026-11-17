package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/logger"
)

const headerRequestID = "X-Request-ID"

// RequestID gives every request an ID, returns it in a header and stores it
// in the context so every log line and error body carries it.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerRequestID)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(headerRequestID, id)
		next.ServeHTTP(w, r.WithContext(logger.WithRequestID(r.Context(), id)))
	})
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// validRequestID accepts a caller-supplied ID only if it is short and plain,
// so it cannot be used to inject content into the logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// RealIP stores the caller's IP in the context.
// Trusted-proxy handling (X-Forwarded-For) will be added with the settings module.
func RealIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyClientIP, ip)))
	})
}

// Recover turns a panic into a logged 500 problem and keeps the server alive.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil || rec == http.ErrAbortHandler {
				return
			}
			slog.ErrorContext(r.Context(), "panic recovered",
				"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			WriteProblem(w, FromError(r.Context(), errs.Internal(fmt.Errorf("panic: %v", rec))))
		}()
		next.ServeHTTP(w, r)
	})
}

// AccessLog writes one line per request.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		}
		slog.Log(r.Context(), level, "http request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "ip", ClientIP(r.Context()))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the real writer (flush, deadlines).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// SecurityHeaders sets headers that are safe for every response. The strict
// CSP works because the whole UI is bundled: nothing loads from another origin.
func SecurityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; " +
		"style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; " +
		"form-action 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// NoStore stops browsers and proxies from caching API responses.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// NotFound and MethodNotAllowed keep unknown API routes in the problem format.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, FromError(r.Context(), errs.NotFound(errs.CodeNotFound, "This API route does not exist.")))
}

func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, fromHuma(r.Context(), http.StatusMethodNotAllowed, "method not allowed", nil))
}
