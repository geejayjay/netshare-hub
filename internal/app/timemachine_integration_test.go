//go:build integration && linux

package app_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys/sysexec"
)

// Step 3.5a against real Samba: a Time Machine share with Apple's
// extensions loads (a share naming a module Samba cannot load refuses every
// connection), Samba reads its settings as written, files and their
// alternate data streams can be written, and the activity log still sees
// real file names with full_audit below the Apple modules.
func TestTimeMachineOverRealSamba(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if m, _ := filepath.Glob("/usr/lib/*/samba/vfs/fruit.so"); len(m) == 0 {
		t.Skip("needs samba-vfs-modules")
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	system, err := sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs()})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	user := fmt.Sprintf("tm%d", os.Getpid()%10000)
	uid := createUser(t, admin, user).ID
	dir := fmt.Sprintf("/srv/shares/tm-%d", os.Getpid())
	name := "tm" + fmt.Sprint(os.Getpid()%10000)
	var sh shareBody
	if status, body := admin.do("POST", "/shares", map[string]any{"name": name, "path": dir, "createDirectory": true,
		"preset": "time-machine", "timeMachine": true, "timeMachineMaxGiB": 100,
		"access": []map[string]any{{"userId": uid, "level": "write"}}}, &sh); status != 201 {
		t.Fatalf("create share: %d %v", status, body)
	}
	settings := withMac(true)
	settings["activityLog"] = true
	t.Cleanup(func() {
		// The share first: Mac support cannot go off while it exists.
		admin.do("DELETE", "/shares/"+itoa(sh.ID), nil, nil)
		settings["activityLog"], settings["macSupport"] = false, false
		admin.do("PUT", "/settings/samba", settings, nil)
		admin.do("DELETE", "/users/"+itoa(uid), nil, nil)
		server.Close()
		application.Close()
		os.RemoveAll(dir)
	})
	if status, body := admin.do("PUT", "/settings/samba", settings, nil); status != 200 {
		t.Fatalf("activity log on: %d %v", status, body)
	}

	// Samba reads the settings as written.
	param := func(section, p string) string {
		out, err := exec.Command("testparm", "-s", "--section-name", section, "--parameter-name", p, sysexec.DefaultMainConfig).Output()
		if err != nil {
			t.Fatalf("testparm %s %s: %v", section, p, err)
		}
		return strings.TrimSpace(string(out))
	}
	for _, c := range []struct{ section, param, want string }{
		{name, "vfs objects", "catia fruit streams_xattr full_audit"},
		{name, "fruit:time machine", "yes"},
		{name, "fruit:time machine max size", "100G"},
		{"global", "fruit:metadata", "stream"},
		{"global", "fruit:nfs_aces", "no"},
	} {
		if got := param(c.section, c.param); got != c.want {
			t.Errorf("%s / %s = %q, want %q", c.section, c.param, got, c.want)
		}
	}

	// The modules load: a file and an alternate data stream (what Macs keep
	// their metadata in) are written over SMB.
	local := filepath.Join(t.TempDir(), "backup.txt")
	os.WriteFile(local, []byte("tm"), 0o644)
	out := smb(t, name, user, `put `+local+` backup.txt; put `+local+` "backup.txt:com.apple.test"; ls`)
	if !strings.Contains(out, "backup.txt") || strings.Contains(out, "NT_STATUS_") {
		t.Fatalf("SMB:\n%s", out)
	}
	attrs := make([]byte, 4096)
	n, err := syscall.Listxattr(filepath.Join(dir, "backup.txt"), attrs)
	if err != nil || !strings.Contains(string(attrs[:n]), "com.apple.test") {
		t.Errorf("the stream is not in the file's extended attributes (streams_xattr): %q %v", attrs[:n], err)
	}

	// The activity log names the real file (not a stream), with
	// full_audit below the Apple modules.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var page struct{ Items []struct{ Share, Op, Path string } }
		admin.do("GET", "/activity?user="+user+"&limit=200", nil, &page)
		for _, e := range page.Items {
			if e.Share == name && e.Op == "write" && e.Path == "backup.txt" {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("the write of backup.txt is not in the activity log")
}
