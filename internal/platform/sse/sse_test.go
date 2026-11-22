package sse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

func TestHubPublishSubscribe(t *testing.T) {
	hub := NewHub()
	ch, unsub := hub.Subscribe()
	defer unsub()

	if hub.ClientCount() != 1 {
		t.Fatalf("expected 1 client, got %d", hub.ClientCount())
	}

	hub.Publish("alert", map[string]string{"title": "hello"})

	select {
	case msg := <-ch:
		s := string(msg)
		if !strings.Contains(s, "event: alert\n") || !strings.Contains(s, `"title":"hello"`) {
			t.Fatalf("unexpected SSE frame: %q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE event")
	}

	unsub()
	if hub.ClientCount() != 0 {
		t.Fatalf("expected 0 clients after unsub, got %d", hub.ClientCount())
	}
}

func TestHubSlowSubscriberDoesNotBlock(t *testing.T) {
	hub := NewHub()
	_, unsub := hub.Subscribe()
	defer unsub()

	for i := 0; i < defaultBufferSize+10; i++ {
		hub.Publish("log", i)
	}
}

func TestServeHTTPRequiresAuth(t *testing.T) {
	hub := NewHub()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	rec := httptest.NewRecorder()
	hub.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestServeHTTPStreamsEvents(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = httpx.WithPrincipal(ctx, &httpx.Principal{
		UserID:   1,
		Username: "admin",
		Role:     rbac.RoleAdmin,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		hub.ServeHTTP(rec, req)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hub.ClientCount() != 1 {
		t.Fatal("client did not subscribe")
	}

	hub.Publish("job", map[string]string{"id": "42"})
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, ": connected\n\n") || !strings.Contains(body, "event: job\n") {
		t.Fatalf("unexpected stream body: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("unexpected content-type: %q", ct)
	}
}
