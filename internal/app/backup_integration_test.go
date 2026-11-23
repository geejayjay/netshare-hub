//go:build integration && linux

package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys/sysexec"
)

// realInstall starts NetShare Hub on the real system with its own data
// folder (its own database): a separate install, as far as the app knows.
func realInstall(t *testing.T) (*client, string, func()) {
	t.Helper()
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
	return admin, cfg.DataDir, func() { server.Close(); application.Close() }
}

// Step 3.6 against the real system: a backup made on one install, whose
// accounts are then gone, restores on a new install: the SMB user signs in
// over SMB with their old password and reaches their files.
func TestRestoreOverRealSamba(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	user := fmt.Sprintf("bk%d", os.Getpid()%10000)
	name := "bk" + fmt.Sprint(os.Getpid()%10000)
	dir := fmt.Sprintf("/srv/shares/backup-%d", os.Getpid())
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Install 1: a user, a share with a file, then a backup.
	src, _, closeSrc := realInstall(t)
	uid := createUser(t, src, user).ID
	var sh shareBody
	if status, body := src.do("POST", "/shares", map[string]any{"name": name, "path": dir, "createDirectory": true,
		"access": []map[string]any{{"userId": uid, "level": "write"}}}, &sh); status != 201 {
		t.Fatalf("create share: %d %v", status, body)
	}
	local := filepath.Join(t.TempDir(), "plan.txt")
	os.WriteFile(local, []byte("the plan"), 0o644)
	if out := smb(t, name, user, "put "+local+" plan.txt"); strings.Contains(out, "NT_STATUS_") {
		t.Fatalf("write before the backup: %s", out)
	}
	var file json.RawMessage
	if status, _ := src.do("POST", "/system/backup", map[string]any{"passphrase": backupPass}, &file); status != 200 {
		t.Fatalf("backup: %d", status)
	}
	// Install 1 goes away: share and account deleted (the files stay).
	src.do("DELETE", "/shares/"+itoa(sh.ID), nil, nil)
	src.do("DELETE", "/users/"+itoa(uid), nil, nil)
	closeSrc()
	if exec.Command("id", user).Run() == nil {
		t.Fatal("the account should be gone before the restore")
	}

	// Install 2: a new database. Restore.
	dst, dataDir, closeDst := realInstall(t)
	t.Cleanup(closeDst) // cleanups run last-first: this one after the one below
	var sum restoreSummary
	if status := restore(t, dst, file, nil, &sum); status != 200 || !sum.Applied {
		t.Fatalf("restore: %d %+v", status, sum)
	}
	t.Cleanup(func() {
		admin := newClient(t, dst.base)
		admin.login("admin", pw)
		var list struct{ Items []shareBody }
		admin.do("GET", "/shares", nil, &list)
		for _, s := range list.Items {
			admin.do("DELETE", "/shares/"+itoa(s.ID), nil, nil)
		}
		var users struct{ Items []userBody }
		admin.do("GET", "/users", nil, &users)
		for _, u := range users.Items {
			if u.Username == user {
				admin.do("DELETE", "/users/"+itoa(u.ID), nil, nil)
			}
		}
	})
	// The copy kept before restoring: only the owner can read it.
	st, err := os.Stat(filepath.Join(dataDir, "backups", sum.RollbackCopy))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("rollback copy: %v %v", st, err)
	}

	// The same SMB password works, and the old file is there and readable.
	var out string
	for i := 0; i < 20; i++ { // the fix-permissions job runs in the background
		out = smb(t, name, user, "get plan.txt "+filepath.Join(t.TempDir(), "back.txt")+"; ls")
		if strings.Contains(out, "plan.txt") && !strings.Contains(out, "NT_STATUS_") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(out, "plan.txt") || strings.Contains(out, "NT_STATUS_") {
		t.Fatalf("SMB after the restore:\n%s", out)
	}
	wrong := exec.Command("smbclient", "//127.0.0.1/"+name, "-U", user+"%wrong-password-1", "-c", "ls")
	if b, _ := wrong.CombinedOutput(); !strings.Contains(string(b), "NT_STATUS_LOGON_FAILURE") {
		t.Errorf("a wrong password got in:\n%s", b)
	}
	// The hash was never on a command line: the import folder is gone.
	if m, _ := filepath.Glob("/tmp/nsh-import-*"); len(m) > 0 {
		t.Errorf("import files left behind: %v", m)
	}
	// The web password works too.
	c := newClient(t, dst.base)
	c.login(user, pw)
}
