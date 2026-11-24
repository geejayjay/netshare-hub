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
	"testing"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys/sysexec"
)

// Step 2.7b over real SMB: what a Windows user deletes lands in the
// share's recycle bin, and comes back from the web.
func TestRecycleBinOverRealSMB(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if _, err := exec.LookPath("smbclient"); err != nil {
		t.Skip("needs smbclient")
	}
	// Workers run "<program> fileworker" as the user: build the real program
	// into a folder every user can enter.
	binDir, err := os.MkdirTemp("/usr/local/lib", "nsh-recycle-itest-")
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(binDir, 0o755)
	t.Cleanup(func() { os.RemoveAll(binDir) })
	exe := filepath.Join(binDir, "netshare-hub")
	if out, err := exec.Command("go", "build", "-o", exe, "netshare-hub/cmd/netshare-hub").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	system, err := sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs(), WorkerExe: exe})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	t.Cleanup(func() { server.Close(); application.Close() })
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	user := fmt.Sprintf("rb%d", os.Getpid()%10000)
	u := createUser(t, admin, user)
	t.Cleanup(func() { admin.do("DELETE", "/users/"+itoa(u.ID), nil, nil) })
	dir := fmt.Sprintf("/srv/shares/recycle-%d", os.Getpid())
	var sh struct{ ID int64 }
	if status, _ := admin.do("POST", "/shares", map[string]any{"name": "rbin", "path": dir, "createDirectory": true, "recycleDays": 7,
		"access": []map[string]any{{"userId": u.ID, "level": "write"}}}, &sh); status != 201 {
		t.Fatalf("create share: %d", status)
	}
	t.Cleanup(func() { admin.do("DELETE", "/shares/"+itoa(sh.ID), nil, nil); os.RemoveAll(dir) })

	local := filepath.Join(t.TempDir(), "report.txt")
	os.WriteFile(local, []byte("quarterly"), 0o644)
	if out := smb(t, "rbin", user, "mkdir work; put "+local+` work\report.txt; del work\report.txt`); strings.Contains(out, "NT_STATUS_") {
		t.Fatalf("smbclient: %s", out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".recycle", "work", "report.txt")); err != nil || string(b) != "quarterly" {
		t.Fatalf("not in the bin after an SMB delete: %q %v", b, err)
	}
	// Hidden over SMB (but there).
	if out := smb(t, "rbin", user, "ls"); strings.Contains(out, ".recycle") && !strings.Contains(out, " H ") && !strings.Contains(out, "DH") {
		t.Errorf("the bin is listed as a normal folder:\n%s", out)
	}

	// Back from the web, as the user.
	uc := newClient(t, server.URL+app.APIPrefix)
	uc.login(user, pw)
	var bin listBody
	uc.do("GET", "/files/rbin/recycle?path=work", nil, &bin)
	if bin.names() != "report.txt" {
		t.Errorf("bin: %s", bin.names())
	}
	if status, _ := uc.do("POST", "/files/rbin/recycle/restore", map[string]any{"path": "work/report.txt"}, nil); status != 200 {
		t.Fatalf("restore: %d", status)
	}
	if out := smb(t, "rbin", user, `get work\report.txt `+filepath.Join(t.TempDir(), "back.txt")); !strings.Contains(out, "getting file") {
		t.Errorf("restored file over SMB: %s", out)
	}
}
