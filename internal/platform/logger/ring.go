package logger

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LogEntry is one structured log line captured in memory for the live log viewer.
type LogEntry struct {
	Seq       int64             `json:"seq"`
	At        time.Time         `json:"at"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	RequestID string            `json:"requestId,omitempty"`
	User      string            `json:"user,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// Ring is a bounded, thread-safe in-memory buffer of recent log entries.
type Ring struct {
	mu       sync.RWMutex
	buf      []LogEntry
	cap      int
	head     int
	count    int
	seq      atomic.Int64
	listener func(LogEntry)
}

// DefaultRing captures every log record emitted through logger.New.
var DefaultRing = NewRing(1000)

// NewRing creates a ring buffer holding up to capacity log entries.
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 256
	}
	return &Ring{buf: make([]LogEntry, capacity), cap: capacity}
}

// SetListener registers an optional live callback (used to fan out over SSE).
func (r *Ring) SetListener(fn func(LogEntry)) {
	r.mu.Lock()
	r.listener = fn
	r.mu.Unlock()
}

func (r *Ring) push(rec slog.Record) {
	e := LogEntry{
		Seq:     r.seq.Add(1),
		At:      rec.Time,
		Level:   strings.ToLower(rec.Level.String()),
		Message: rec.Message,
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	rec.Attrs(func(a slog.Attr) bool {
		val := fmt.Sprint(a.Value.Resolve().Any())
		switch a.Key {
		case "request_id":
			e.RequestID = val
		case "user":
			e.User = val
		default:
			if e.Attrs == nil {
				e.Attrs = map[string]string{}
			}
			e.Attrs[a.Key] = val
		}
		return true
	})

	r.mu.Lock()
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
	fn := r.listener
	r.mu.Unlock()

	if fn != nil {
		fn(e)
	}
}

// Filter restricts which buffered entries Query returns.
type Filter struct {
	Level     string
	RequestID string
	Query     string
	Limit     int
}

// Query returns matching entries in newest-first order.
func (r *Ring) Query(f Filter) []LogEntry {
	limit := f.Limit
	if limit <= 0 || limit > r.cap {
		limit = 200
	}
	level := strings.ToLower(strings.TrimSpace(f.Level))
	reqID := strings.TrimSpace(f.RequestID)
	q := strings.ToLower(strings.TrimSpace(f.Query))

	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]LogEntry, 0, min(limit, r.count))
	for i := 0; i < r.count && len(out) < limit; i++ {
		idx := (r.head - 1 - i + r.cap) % r.cap
		e := r.buf[idx]
		if level != "" && e.Level != level {
			continue
		}
		if reqID != "" && !strings.Contains(e.RequestID, reqID) {
			continue
		}
		if q != "" && !matchesQuery(e, q) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func matchesQuery(e LogEntry, q string) bool {
	if strings.Contains(strings.ToLower(e.Message), q) ||
		strings.Contains(strings.ToLower(e.User), q) ||
		strings.Contains(strings.ToLower(e.RequestID), q) {
		return true
	}
	for k, v := range e.Attrs {
		if strings.Contains(strings.ToLower(k), q) || strings.Contains(strings.ToLower(v), q) {
			return true
		}
	}
	return false
}
