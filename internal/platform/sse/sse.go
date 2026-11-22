// Package sse provides a Server-Sent Events hub so one live connection per
// browser tab can receive job progress, alerts, and live log lines.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"netshare-hub/internal/platform/httpx"
)

const (
	defaultBufferSize = 64
	keepaliveInterval = 25 * time.Second
)

// Hub manages active SSE subscribers and sends events to them.
type Hub struct {
	mu      sync.RWMutex
	nextID  atomic.Uint64
	seq     atomic.Uint64
	clients map[uint64]*client
}

type client struct {
	ch chan []byte
	// who is the signed-in user on this connection; nil for subscribers
	// that are not a browser (they only receive Publish broadcasts).
	who *httpx.Principal
}

// NewHub creates an empty SSE hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[uint64]*client),
	}
}

// Subscribe registers a new client channel and returns an unsubscribe function.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	return h.SubscribeAs(nil)
}

// SubscribeAs registers a client for a signed-in user, so PublishTo can
// decide per user whether an event may be seen.
func (h *Hub) SubscribeAs(who *httpx.Principal) (<-chan []byte, func()) {
	id := h.nextID.Add(1)
	ch := make(chan []byte, defaultBufferSize)

	h.mu.Lock()
	h.clients[id] = &client{ch: ch, who: who}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if existing, ok := h.clients[id]; ok {
			delete(h.clients, id)
			close(existing.ch)
		}
		h.mu.Unlock()
	}
}

// Publish encodes payload as JSON and sends it to ALL connected clients.
// Use it only for events every signed-in user may see; for anything that
// belongs to one user (job progress, file names) use PublishTo.
// Slow clients whose buffers are full drop the event rather than blocking.
func (h *Hub) Publish(eventType string, payload any) {
	h.publish(eventType, payload, nil)
}

// PublishTo sends an event only to signed-in users for whom allow returns
// true. Subscribers without a user never receive it.
func (h *Hub) PublishTo(eventType string, payload any, allow func(*httpx.Principal) bool) {
	h.publish(eventType, payload, allow)
}

func (h *Hub) publish(eventType string, payload any, allow func(*httpx.Principal) bool) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	id := h.seq.Add(1)
	frame := []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", id, eventType, data))

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.clients {
		if allow != nil && (c.who == nil || !allow(c.who)) {
			continue
		}
		select {
		case c.ch <- frame:
		default:
		}
	}
}

// ClientCount returns the number of currently connected subscribers.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ServeHTTP streams events to an authenticated browser tab.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	who := httpx.PrincipalFrom(r.Context())
	if who == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Through the controller, not w.(http.Flusher): middleware wraps the
	// writer (the access log records the status), and the controller finds
	// the real one behind it via Unwrap.
	flusher := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsubscribe := h.SubscribeAs(who)
	defer unsubscribe()

	_, _ = w.Write([]byte(": connected\n\n"))
	if err := flusher.Flush(); err != nil {
		return // this connection cannot stream
	}

	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case frame, open := <-ch:
			if !open {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			_ = flusher.Flush()
		case <-ticker.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			_ = flusher.Flush()
		}
	}
}
