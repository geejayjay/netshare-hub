// Package logger builds the structured logger used everywhere.
//
// Every log line written with a request context automatically carries the
// request ID and the acting user, so one ID links the browser error, the
// access log and the internal cause.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyUser
)

// WithRequestID stores the request ID for later log lines.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

// RequestID returns the request ID, or "" outside a request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(keyRequestID).(string)
	return id
}

// WithUser stores the acting username for later log lines.
func WithUser(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, keyUser, username)
}

// Options are the bootstrap logging settings.
type Options struct {
	Level  string // debug | info | warn | error
	Format string // json | text
	Output io.Writer
}

// New builds the logger.
func New(o Options) *slog.Logger {
	if o.Output == nil {
		o.Output = os.Stdout
	}
	hopts := &slog.HandlerOptions{Level: parseLevel(o.Level)}

	var h slog.Handler
	if strings.EqualFold(o.Format, "text") {
		h = slog.NewTextHandler(o.Output, hopts)
	} else {
		h = slog.NewJSONHandler(o.Output, hopts)
	}
	return slog.New(contextHandler{h})
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// contextHandler adds request-scoped attributes to every record.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if user, _ := ctx.Value(keyUser).(string); user != "" {
		r.AddAttrs(slog.String("user", user))
	}
	DefaultRing.push(r)
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
