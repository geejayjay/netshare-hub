package app_test

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"netshare-hub/internal/app"
	"netshare-hub/internal/modules/activity"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys/sambalog"
	"netshare-hub/internal/sys/sysfake"
)

// Step 2.2a: the SMB activity log.

type activityPage struct {
	Items []struct {
		ID                          int64
		User, Share, Op, Path       string
		Path2, ClientIP, ClientName string
		OK                          bool
	}
	Next int64
}

var defaultsWithActivity = map[string]any{"serverName": "", "workgroup": "", "minProtocol": "default",
	"maxProtocol": "default", "encryption": "default", "multichannel": "default", "activityLog": true}

func activityServer(t *testing.T) (*client, *sysfake.Fake, string, string) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	fake := sysfake.New()
	system := fake.System()
	application, err := app.New(t.Context(), cfg, app.Options{System: &system,
		Activity: activity.Options{BatchSize: 200, BatchDelay: 20 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, application)
	admin := newClient(t, server)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken
	// The feeder subscribes to the audit source when the app starts.
	for i := 0; i < 100 && fake.AuditListeners() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	return admin, fake, server, cfg.DataDir
}

func smbEvent(op string, write bool, p string) sambalog.Event {
	return sambalog.Event{Kind: sambalog.KindFile, Time: time.Now(), User: "jay", ClientIP: netip.MustParseAddr("192.168.1.20"),
		ClientName: "DESKTOP-JAY", Share: "media", Op: op, OK: true, Write: write, Path: "/srv/media/" + p}
}

func waitActivity(t *testing.T, c *client, query string, n int) activityPage {
	t.Helper()
	var page activityPage
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page = activityPage{}
		c.do("GET", "/activity"+query, nil, &page)
		if len(page.Items) >= n {
			return page
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("activity%s: %d entries, want %d", query, len(page.Items), n)
	return page
}

func TestActivityLogSetting(t *testing.T) {
	admin, fake, _, _ := activityServer(t)
	jay := createUser(t, admin, "jay")
	admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, nil)
	if strings.Contains(fake.CurrentConfig(), "full_audit") {
		t.Fatal("the activity log is off until an admin turns it on")
	}

	// Without the module, turning it on is refused and nothing changes.
	fake.MissingModules = []string{"full_audit"}
	before := fake.CurrentConfig()
	var p problem
	status, _ := admin.do("PUT", "/settings/samba", defaultsWithActivity, &p)
	want(t, "module missing", status, 422)
	want(t, "module missing code", p.Code, "SAMBA_MODULE_MISSING")
	if !strings.Contains(p.Detail, "samba-vfs-modules") {
		t.Errorf("the message should say what to install: %q", p.Detail)
	}
	want(t, "config untouched", fake.CurrentConfig(), before)

	fake.MissingModules = nil
	var s struct{ ActivityLog bool }
	status, _ = admin.do("PUT", "/settings/samba", defaultsWithActivity, &s)
	want(t, "turn on", status, 200)
	want(t, "activityLog", s.ActivityLog, true)
	sec := section(t, fake, "media")
	for _, line := range []string{"vfs objects = full_audit", "full_audit:prefix = nshaudit|%u|%I|%S|%m", "full_audit:syslog = no"} {
		if !strings.Contains(sec, line) {
			t.Errorf("share config is missing %q:\n%s", line, sec)
		}
	}
	if !strings.Contains(fake.CurrentConfig(), "log level = 0 full_audit:1 auth_json_audit:3") {
		t.Errorf("the audit log level is missing:\n%s", fake.CurrentConfig())
	}
}

func TestActivityFeed(t *testing.T) {
	admin, fake, base, _ := activityServer(t)
	jay := createUser(t, admin, "jay")
	admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, nil)

	fake.EmitAudit(smbEvent("openat", false, "Photos")) // a read: not in the feed
	fake.EmitAudit(smbEvent("mkdirat", false, "Photos/2025"))
	fake.EmitAudit(smbEvent("openat", true, "=cmd.jpg"))
	ren := smbEvent("renameat", false, "Photos/2025")
	ren.Path2 = "/srv/media/Photos/2025-old"
	fake.EmitAudit(ren)
	fake.EmitAudit(sambalog.Event{Kind: sambalog.KindAuth, User: "admin", ClientIP: netip.MustParseAddr("10.0.0.66"),
		AuthStatus: "NT_STATUS_WRONG_PASSWORD", Time: time.Now()})

	page := waitActivity(t, admin, "", 4)
	if len(page.Items) != 4 {
		t.Fatalf("feed: %+v", page.Items)
	}
	r := page.Items[1]
	if r.Op != "rename" || r.Path != "Photos/2025" || r.Path2 != "Photos/2025-old" || r.ClientName != "DESKTOP-JAY" {
		t.Errorf("rename entry: %+v", r)
	}
	if page.Items[0].Op != "login_failed" || page.Items[0].ClientIP != "10.0.0.66" {
		t.Errorf("failed login entry: %+v", page.Items[0])
	}

	// Filters.
	if p := waitActivity(t, admin, "?op=write", 1); len(p.Items) != 1 || p.Items[0].Path != "=cmd.jpg" {
		t.Errorf("op filter: %+v", p.Items)
	}
	if p := waitActivity(t, admin, "?share=media&path=2025-old", 1); len(p.Items) != 1 {
		t.Errorf("path filter: %+v", p.Items)
	}

	// CSV export: a file name cannot become a spreadsheet formula.
	status, h, body := admin.raw("GET", "/activity/export", nil)
	want(t, "export status", status, 200)
	if !strings.HasPrefix(h.Get("Content-Type"), "text/csv") || !strings.Contains(string(body), "'=cmd.jpg") {
		t.Errorf("export:\n%s", body)
	}

	// The activity log is for auditors and admins.
	plain := newClient(t, base)
	plain.login("jay", pw)
	if status, _ := plain.do("GET", "/activity", nil, nil); status != 403 {
		t.Errorf("a plain user read the activity log: %d", status)
	}
}

// The step's "done when": a heavy copy (many small files) does not slow the
// main database. The activity lands in its own file, in batches, and a main
// database change during the flood is as quick as without it.
func TestHeavyActivityDoesNotSlowTheMainDatabase(t *testing.T) {
	admin, fake, _, dataDir := activityServer(t)
	jay := createUser(t, admin, "jay")
	var sh shareBody
	admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)

	change := func() time.Duration {
		start := time.Now()
		if status, _ := admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"comment": fmt.Sprint(start.UnixNano())}, nil); status != 200 {
			t.Fatalf("share change: %d", status)
		}
		return time.Since(start)
	}
	calm := change()

	const files = 9000 // each file: an open for writing (and Samba's reads, which are dropped)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < files; i++ {
			fake.EmitAudit(smbEvent("openat", true, fmt.Sprintf("Copy/file-%05d.txt", i)))
			fake.EmitAudit(smbEvent("openat", false, "Copy"))
		}
	}()
	busiest := time.Duration(0)
	for i := 0; i < 5; i++ {
		busiest = max(busiest, change())
	}
	<-done
	if limit := calm*5 + 200*time.Millisecond; busiest > limit {
		t.Errorf("a main database change took %v during the flood (%v without it)", busiest, calm)
	}

	// Every write made it to the activity database...
	deadline := time.Now().Add(10 * time.Second)
	var n int
	actDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dataDir, "activity.db"))+"?_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer actDB.Close()
	for time.Now().Before(deadline) {
		actDB.QueryRow("SELECT COUNT(*) FROM activity WHERE op = 'write'").Scan(&n)
		if n >= files {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n != files {
		t.Errorf("%d of %d writes recorded", n, files)
	}
	// ...and none of it is in the main database.
	mainDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dataDir, "netshare-hub.db"))+"?_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	var tables int
	mainDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'activity'").Scan(&tables)
	if tables != 0 {
		t.Error("the activity table is in the main database")
	}
}

// newTestServer serves an app and returns its API base URL.
func newTestServer(t *testing.T, application *app.App) string {
	t.Helper()
	server := httptest.NewServer(application.Handler)
	t.Cleanup(func() {
		server.Close()
		application.Close()
	})
	return server.URL + app.APIPrefix
}
