package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys/sysfake"
)

// moduleServer starts the app with the given modules and signs in the admin.
func moduleServer(t *testing.T, mods config.Modules) (*client, *sysfake.Fake, string) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	cfg.Modules = mods
	fake := sysfake.New()
	system := fake.System()
	application, err := app.New(t.Context(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, application)
	admin := newClient(t, server)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken
	return admin, fake, cfg.DataDir
}

// A minimal setup: every optional module off. The core still works, and
// the modules that are off leave nothing behind: no routes, no activity
// database, no audit stream, no audit settings in Samba's config.
func TestMinimalSetup(t *testing.T) {
	admin, fake, dataDir := moduleServer(t, config.Modules{})

	var mods map[string]bool
	status, _ := admin.do("GET", "/system/modules", nil, &mods)
	want(t, "modules", status, 200)
	for name, on := range mods {
		if on {
			t.Errorf("module %s is on", name)
		}
	}

	jay := createUser(t, admin, "jay")
	var sh struct{ ID int64 }
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
	want(t, "create share", status, 201)

	for _, path := range []string{"/activity", "/guard/settings", "/guard/bans", "/system/sessions", "/settings/discovery",
		"/files/media/entries", "/shares/nfs/status", "/replication/jobs", "/replication/status", "/block/disks", "/block/status"} {
		status, _ := admin.do("GET", path, nil, nil)
		want(t, "GET "+path+" with its module off", status, 404)
	}
	status, _ = admin.do("POST", "/system/backup", map[string]any{"passphrase": "a long passphrase here"}, nil)
	want(t, "backup with its module off", status, 404)

	if _, err := os.Stat(filepath.Join(dataDir, "activity.db")); !os.IsNotExist(err) {
		t.Errorf("activity.db exists with the activity log off: %v", err)
	}
	if n := fake.AuditListeners(); n != 0 {
		t.Errorf("%d audit listeners with every event module off", n)
	}
	// The stored activity setting cannot switch Samba's audit on.
	admin.do("PUT", "/settings/samba", map[string]any{"activityLog": true, "serverName": "", "workgroup": "", "minProtocol": "default",
		"maxProtocol": "default", "encryption": "default", "multichannel": "default"}, nil)
	if cfg := fake.CurrentConfig(); strings.Contains(cfg, "full_audit") || strings.Contains(cfg, "auth_json_audit") {
		t.Errorf("audit settings in Samba's config with the modules off:\n%s", cfg)
	}

	// NFS cannot be turned on while its module is off.
	var p problem
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"nfs": map[string]any{"enabled": true, "mode": "user",
		"asUserId": jay.ID, "clients": []map[string]any{{"address": "192.168.1.0/24"}}}}, &p)
	want(t, "NFS with its module off", status, 422)
	if fieldCodes(p)["nfs.enabled"] != "module_off" {
		t.Errorf("problem: %+v", p)
	}
}

func TestModuleDependencies(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(file, []byte("sys = \"fake\"\n[modules]\nactivity_log = false\nransomware_guard = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(file); err == nil || !strings.Contains(err.Error(), "activity_log") {
		t.Errorf("guard without the activity log: %v", err)
	}
	// Defaults: everything from before step 4 on, the new protocols off.
	d := config.Default().Modules
	if !d.FileManager || !d.ActivityLog || !d.RansomwareGuard || !d.BruteForce || d.NFS || d.Replication || d.BlockStorage {
		t.Errorf("defaults: %+v", d)
	}
}
