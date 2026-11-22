//go:build integration && linux

package logingest

// Against real Samba: the events the parser makes from Samba's own log
// lines. Run ONLY in the disposable test container, as root:
//
//	go test -p 1 -tags integration ./internal/modules/logingest/

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealSambaLogs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if _, err := os.Stat("/usr/lib/x86_64-linux-gnu/samba/vfs/full_audit.so"); err != nil {
		t.Skip("needs samba-vfs-modules (full_audit)")
	}
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	const user, pw = "nsh-ingest", "ingest-pass-1"
	exec.Command("useradd", "--no-create-home", "--shell", "/usr/sbin/nologin", user).Run()
	cmd := exec.Command("smbpasswd", "-s", "-a", user)
	cmd.Stdin = strings.NewReader(pw + "\n" + pw + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("smbpasswd: %v %s", err, out)
	}
	dir := t.TempDir()
	share := filepath.Join("/srv/shares", "ingest-"+filepath.Base(dir))
	os.MkdirAll(share, 0o777)
	os.Chmod(share, 0o777)
	managed := "/etc/samba/netshare-hub.conf"
	before, _ := os.ReadFile(managed)
	t.Cleanup(func() {
		os.WriteFile(managed, before, 0o644)
		exec.Command("smbcontrol", "all", "reload-config").Run()
		exec.Command("smbcontrol", "all", "debug", "0").Run()
		exec.Command("smbpasswd", "-x", user).Run()
		exec.Command("userdel", user).Run()
		os.RemoveAll(share)
	})
	// The audit settings step 2.2a will render, by hand here.
	// The log level must be in the config: each connection's process reads
	// it again, so setting it at runtime does not last.
	conf := "[global]\n   log level = 0 full_audit:1 auth_json_audit:3\n\n" +
		"[ingest]\n   path = " + share + "\n   valid users = " + user + "\n   read only = no\n" +
		"   vfs objects = full_audit\n   full_audit:prefix = nshaudit|%u|%I|%S|%m\n" +
		"   full_audit:success = connect disconnect openat renameat unlinkat mkdirat\n" +
		"   full_audit:failure = connect openat renameat unlinkat mkdirat\n   full_audit:syslog = false\n"
	if err := os.WriteFile(managed, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	run("smbcontrol", "all", "reload-config")

	// Follow the log files Samba writes for this client: by address before
	// the session knows the machine name, by machine name after.
	host, _ := os.Hostname()
	in := New()
	events := in.Subscribe("test", 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, f := range []string{"/var/log/samba/log.127.0.0.1", "/var/log/samba/log." + host} {
		go in.Tail(ctx, f, 50*time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	local := filepath.Join(dir, "up.txt")
	os.WriteFile(local, []byte("hello"), 0o644)
	exec.Command("smbclient", "//127.0.0.1/ingest", "-U", user+"%"+pw, "-c",
		`mkdir "new dir"; put `+local+` "new dir/up.txt"; rename "new dir/up.txt" "new dir/moved.txt"; del "new dir/moved.txt"`).Run()
	exec.Command("smbclient", "//127.0.0.1/ingest", "-U", user+"%wrong-password", "-c", "ls").Run()

	want := map[string]bool{"connect": false, "mkdirat": false, "openat w": false, "renameat": false,
		"unlinkat": false, "auth ok": false, "auth NT_STATUS_WRONG_PASSWORD": false}
	deadline := time.After(10 * time.Second)
	for {
		missing := 0
		for _, seen := range want {
			if !seen {
				missing++
			}
		}
		if missing == 0 {
			return
		}
		select {
		case ev := <-events:
			if ev.Share != "" && ev.Share != "ingest" {
				continue
			}
			switch {
			case ev.Kind == KindAuth && ev.User == user && ev.OK:
				want["auth ok"] = true
			case ev.Kind == KindAuth && ev.User == user:
				want["auth "+ev.AuthStatus] = true
			case ev.Kind == KindConnect && ev.User == user:
				want["connect"] = true
			case ev.Op == "openat" && ev.Write && ev.OK && filepath.Base(ev.Path) == "up.txt":
				want["openat w"] = true
			case ev.Op == "mkdirat" && ev.OK && filepath.Base(ev.Path) == "new dir":
				want["mkdirat"] = true
			case ev.Op == "renameat" && ev.OK && filepath.Base(ev.Path2) == "moved.txt":
				want["renameat"] = true
			case ev.Op == "unlinkat" && ev.OK && filepath.Base(ev.Path) == "moved.txt":
				want["unlinkat"] = true
			}
		case <-deadline:
			t.Fatalf("events not seen from real Samba: %v (stats %+v)", want, in.Stats())
		}
	}
}
