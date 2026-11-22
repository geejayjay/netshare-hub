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

// Hub manages active SSE subscribers and broadcasts events to them.
type Hub struct {
	mu      sync.RWMutex
	nextID  atomic.Uint64
	seq     atomic.Uint64
	clients map[uint64]chan []byte
}

// NewHub creates an empty SSE hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[uint64]chan []byte),
	}
}

// Subscribe registers a new client channel and returns an unsubscribe function.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	id := h.nextID.Add(1)
	ch := make(chan []byte, defaultBufferSize)

	h.mu.Lock()
	h.clients[id] = ch
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if existing, ok := h.clients[id]; ok {
			delete(h.clients, id)
			close(existing)
		}
		h.mu.Unlock()
	}
}

// Publish encodes payload as JSON and sends it to all connected clients.
// Slow clients whose buffers are full drop the event rather than blocking.
func (h *Hub) Publish(eventType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	id := h.seq.Add(1)
	frame := []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", id, eventType, data))

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.clients {
		select {
		case ch <- frame:
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
	if httpx.PrincipalFrom(r.Context()) == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsubscribe := h.Subscribe()
	defer unsubscribe()

	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

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
			flusher.Flush()
		case <-ticker.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
