package audit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"netshare-hub/internal/db/migrations"
	"netshare-hub/internal/platform/db"
)

func TestAuditRecordListAndScrub(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(context.Background(), migrations.FS, filepath.Join(dir, "bak")); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sanitized := sanitizeJSON([]byte(`{"username":"alice","password":"super-secret","nested":{"chapSecret":"123456789012","ok":true}}`))
	if strings.Contains(sanitized, "super-secret") || strings.Contains(sanitized, "123456789012") {
		t.Fatalf("secret leaked into audit payload: %s", sanitized)
	}
	if !strings.Contains(sanitized, `"alice"`) || !strings.Contains(sanitized, `"[redacted]"`) {
		t.Fatalf("unexpected sanitized json: %s", sanitized)
	}

	svc := NewService(database)
	if err := svc.Record(context.Background(), AuditEntry{
		ActorID: 1, Actor: "admin", Action: "POST shares", Target: "/shares", After: `{"name":"media"}`, RequestID: "req-123",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	got, err := svc.List(context.Background(), Filter{Actor: "admin", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Target != "/shares" || got.Items[0].RequestID != "req-123" {
		t.Fatalf("unexpected list result: %+v", got)
	}
}
