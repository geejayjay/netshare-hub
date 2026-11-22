//go:build integration && linux

package sysexec

// These tests run the real system tools. They create and delete a Linux
// account and rewrite the Samba config, so run them ONLY in a disposable
// container or VM with Samba installed, as root:
//
//	go test -tags integration ./internal/sys/sysexec/
//
// They are skipped in the normal test run (no "integration" tag).

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"netshare-hub/internal/sys"
)

const testUser = "nsh-itest"

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
}

func out(t *testing.T, name string, args ...string) string {
	t.Helper()
	b, _ := exec.Command(name, args...).CombinedOutput()
	return string(b)
}

func TestRealAccountLifecycle(t *testing.T) {
	requireRoot(t)
	a := NewAccounts(ExecRunner{})
	t.Cleanup(func() { _ = a.DeleteUser(ctx, testUser) })

	if err := a.CreateUser(ctx, testUser, "first-password-1"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	passwd := out(t, "getent", "passwd", testUser)
	if !strings.Contains(passwd, "/usr/sbin/nologin") {
		t.Errorf("account should have no login shell: %s", passwd)
	}
	if !strings.Contains(out(t, "id", "-Gn", testUser), sys.ManagedGroup) {
		t.Error("account should be in the managed group")
	}
	if !strings.Contains(out(t, "pdbedit", "-L"), testUser+":") {
		t.Error("samba account was not created")
	}
	// The Linux password must stay locked: the account is for Samba only.
	if shadow := out(t, "getent", "shadow", testUser); !strings.Contains(shadow, testUser+":!") {
		t.Errorf("linux password should be locked: %s", shadow)
	}

	if err := a.SetPassword(ctx, testUser, "second-password-2"); err != nil {
		t.Errorf("SetPassword: %v", err)
	}
	if err := a.SetEnabled(ctx, testUser, false); err != nil {
		t.Errorf("SetEnabled(false): %v", err)
	}
	if flags := out(t, "pdbedit", "-L", "-v", testUser); !strings.Contains(flags, "D") {
		t.Errorf("samba account should be disabled:\n%s", flags)
	}

	// System accounts are off limits.
	if err := a.SetPassword(ctx, "root", "whatever-12345"); !errors.Is(err, sys.ErrNotManaged) {
		t.Errorf("SetPassword(root) = %v, want ErrNotManaged", err)
	}
	if err := a.CreateUser(ctx, "daemon", "whatever-12345"); !errors.Is(err, sys.ErrNameTaken) {
		t.Errorf("CreateUser(daemon) = %v, want ErrNameTaken", err)
	}

	if err := a.DeleteUser(ctx, testUser); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if out(t, "getent", "passwd", testUser) != "" {
		t.Error("linux account still exists")
	}
	if strings.Contains(out(t, "pdbedit", "-L"), testUser+":") {
		t.Error("samba account still exists")
	}
}

func TestRealSambaConfig(t *testing.T) {
	requireRoot(t)
	dir := t.TempDir()
	shareDir := dir + "/data"
	if err := os.Mkdir(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	main := dir + "/smb.conf"
	if err := os.WriteFile(main, []byte("[global]\n   workgroup = TEST\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reloading needs a running smbd; only the validation and file handling
	// are under test here, so a failed reload is accepted.
	s := NewSamba(ExecRunner{}, dir+"/netshare-hub.conf", main)

	good := "[itest]\n   path = " + shareDir + "\n   read only = yes\n   valid users = " + testUser + "\n"
	if err := s.applyConfig(ctx, good); err != nil && !errors.Is(err, sys.ErrReloadFailed) {
		t.Fatalf("valid config rejected: %v", err)
	}
	// The real testparm must see our share through the include line.
	if dump := out(t, "testparm", "-s", main); !strings.Contains(dump, "[itest]") {
		t.Errorf("share is not visible through smb.conf:\n%s", dump)
	}

	// A typo must be caught even though testparm exits 0 for it.
	var cfgErr *sys.ConfigError
	if err := s.applyConfig(ctx, "[itest]\n   raed only = yes\n"); !errors.As(err, &cfgErr) {
		t.Fatalf("typo was not rejected: %v", err)
	}
	if now, _ := os.ReadFile(dir + "/netshare-hub.conf"); string(now) != good {
		t.Error("a rejected config replaced the good one")
	}
}
