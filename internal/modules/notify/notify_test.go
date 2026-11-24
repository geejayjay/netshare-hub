package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"netshare-hub/internal/db/migrations"
	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/platform/db"
)

func openTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(context.Background(), migrations.FS, filepath.Join(dir, "bak")); err != nil {
		t.Fatal(err)
	}
	return NewService(database)
}

func TestChannelCRUDAndSecretMasking(t *testing.T) {
	ctx := context.Background()
	svc := openTestService(t)

	ch, err := svc.Create(ctx, ChannelInput{
		Name:        "Ops Gotify",
		Kind:        KindGotify,
		MinSeverity: "warning",
		Config:      ChannelConfig{URL: "https://gotify.example.com", Token: "super-secret-token"},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if !ch.HasSecret {
		t.Fatalf("expected hasSecret=true")
	}
	if ch.Config.Token != "" {
		t.Fatalf("token leaked in response: %q", ch.Config.Token)
	}

	// Updating URL without resending token preserves the stored token.
	updated, err := svc.Update(ctx, ch.ID, ChannelPatch{
		Config: &ChannelConfig{URL: "https://gotify2.example.com"},
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if !updated.HasSecret || updated.Config.URL != "https://gotify2.example.com" {
		t.Fatalf("unexpected updated channel: %+v", updated)
	}

	if err := svc.Delete(ctx, ch.ID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	items, err := svc.List(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("expected empty list, got %d (%v)", len(items), err)
	}
}

func TestDispatchAndWebhookSignature(t *testing.T) {
	ctx := context.Background()
	svc := openTestService(t)

	var hits atomic.Int32
	var gotSig string
	var gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotSig = r.Header.Get("X-NetShare-Signature")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["title"].(string); ok {
			gotTitle = v
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, err := svc.Create(ctx, ChannelInput{
		Name:        "Discord Hook",
		Kind:        KindWebhook,
		MinSeverity: "error",
		Config:      ChannelConfig{URL: srv.URL, Secret: "hmac-key"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Test endpoint sends immediately regardless of minSeverity.
	res, err := svc.Test(ctx, ch.ID)
	if err != nil || !res.OK {
		t.Fatalf("expected test ok, got %+v (%v)", res, err)
	}
	if gotSig == "" {
		t.Fatalf("expected HMAC signature header")
	}

	// Warning alert should be filtered out (minSeverity=error).
	svc.dispatch(ctx, alerts.Alert{Severity: alerts.Warning, Title: "Ignored warning"})
	if hits.Load() != 1 {
		t.Fatalf("expected warning to be skipped, hits=%d", hits.Load())
	}

	// Critical alert should be delivered.
	svc.dispatch(ctx, alerts.Alert{Severity: alerts.Critical, Title: "Ransomware detected", Message: "User blocked"})
	if hits.Load() != 2 || gotTitle != "Ransomware detected" {
		t.Fatalf("expected critical delivery, hits=%d title=%q", hits.Load(), gotTitle)
	}
}

func TestNtfyDelivery(t *testing.T) {
	ctx := context.Background()
	svc := openTestService(t)

	var gotAuth, gotPriority string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPriority = r.Header.Get("Priority")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch, err := svc.Create(ctx, ChannelInput{
		Name:   "My ntfy",
		Kind:   KindNtfy,
		Config: ChannelConfig{URL: srv.URL + "/topic", Token: "tk_123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Test(ctx, ch.ID)
	if err != nil || !res.OK {
		t.Fatalf("ntfy test failed: %+v (%v)", res, err)
	}
	if gotAuth != "Bearer tk_123" || gotPriority != "2" {
		t.Fatalf("unexpected headers: auth=%q priority=%q", gotAuth, gotPriority)
	}
}
