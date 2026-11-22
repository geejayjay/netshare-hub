//go:build integration && linux

package agent_test

// End-to-end test of the privilege split against the real system: a real
// agent on a real unix socket, real Linux accounts, real Samba, and a real
// SMB client. It changes system accounts and the Samba config, so run it
// ONLY in the disposable test container, as root:
//
//	docker compose -f compose.test.yml run --rm test \
//	    go test -tags integration ./internal/agent/

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/agent"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysexec"
)

const (
	webUser   = "nsh-itest-web"   // the account the web server would run as
	otherUser = "nsh-itest-other" // same group, different user: must be refused
	rwUser    = "nsh-itest-rw"
	roUser    = "nsh-itest-ro"
	outsider  = "nsh-itest-out" // has a Samba account but no access to the share
	password  = "integration-pass-1"
)

func sh(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// curlAs calls the agent as another Linux user and returns the HTTP status
// ("000" when the connection itself was refused).
func curlAs(t *testing.T, user, socket, op string) string {
	t.Helper()
	out, _ := sh(t, "sudo", "-u", user, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		"--unix-socket", socket, "-X", "POST", "-d", "{}", "http://agent/v1/"+op)
	return strings.TrimSpace(out)
}

func smb(t *testing.T, share, user, pass, command string) string {
	t.Helper()
	out, _ := sh(t, "smbclient", "//127.0.0.1/"+share, "-U", user+"%"+pass, "-c", command)
	return out
}

func TestAgentEndToEnd(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}

	// --- the accounts involved on the "server process" side ---
	sh(t, "useradd", "--no-create-home", webUser)
	sh(t, "useradd", "--no-create-home", "--gid", webUser, otherUser)
	t.Cleanup(func() {
		sh(t, "userdel", otherUser)
		sh(t, "userdel", webUser)
	})

	// --- start the real agent ---
	runDir := fmt.Sprintf("/run/nsh-itest-%d", os.Getpid())
	socket := runDir + "/agent.sock"
	t.Cleanup(func() { os.RemoveAll(runDir) })

	system, err := sysexec.New(sysexec.Options{Protected: []string{"/var/lib/netshare-hub"}, WebUser: webUser})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Serve(ctx, socket, webUser, system, "itest") }()
	t.Cleanup(func() {
		stop()
		<-done
	})
	for i := 0; ; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if i > 50 {
			t.Fatal("agent did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// --- who may talk to the agent ---
	if info, _ := os.Stat(socket); info.Mode().Perm() != 0o660 {
		t.Errorf("socket mode = %o, want 660", info.Mode().Perm())
	}
	if got := curlAs(t, webUser, socket, "ping"); got != "200" {
		t.Errorf("the web server's user must be accepted, got status %s", got)
	}
	// Same group (so the file permissions let it connect), but not the
	// allowed user: the kernel-reported identity must stop it.
	if got := curlAs(t, otherUser, socket, "ping"); got != "403" {
		t.Errorf("another user in the same group must be refused with 403, got %s", got)
	}
	if got := curlAs(t, otherUser, socket, "accounts.delete"); got != "403" {
		t.Errorf("refused caller reached an operation, got %s", got)
	}
	// Any other local user cannot even open the socket.
	if got := curlAs(t, "nobody", socket, "ping"); got != "000" {
		t.Errorf("an unrelated user must not be able to connect, got status %s", got)
	}

	// --- the real flow, through the client the web server uses ---
	client := agent.Dial(socket)
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	remote := client.System()

	for _, u := range []string{rwUser, roUser, outsider} {
		if err := remote.Accounts.CreateUser(ctx, u, password); err != nil {
			t.Fatalf("CreateUser(%s): %v", u, err)
		}
	}
	t.Cleanup(func() {
		remote.Samba.ApplyShares(context.Background(), nil)
		for _, u := range []string{rwUser, roUser, outsider} {
			remote.Accounts.DeleteUser(context.Background(), u)
		}
	})

	shareDir := fmt.Sprintf("/srv/shares/itest-%d", os.Getpid())
	real, err := remote.Files.CreateShareDir(ctx, shareDir)
	if err != nil || real != shareDir {
		t.Fatalf("CreateShareDir = %q, %v", real, err)
	}
	t.Cleanup(func() { os.RemoveAll(shareDir) })

	err = remote.Samba.ApplyShares(ctx, []sys.ShareSpec{{
		Name: "itest", Path: shareDir, Comment: "integration test",
		Readers: []string{rwUser, roUser}, Writers: []string{rwUser},
	}})
	if err != nil {
		t.Fatalf("ApplyShares: %v", err)
	}

	// --- prove it over real SMB ---
	if out := smb(t, "itest", rwUser, password, "put /etc/hostname hello.txt; ls"); !strings.Contains(out, "hello.txt") {
		t.Fatalf("read+write user could not write:\n%s", out)
	}
	if out := smb(t, "itest", roUser, password, "ls"); !strings.Contains(out, "hello.txt") {
		t.Errorf("read-only user could not list:\n%s", out)
	}
	if out := smb(t, "itest", roUser, password, "put /etc/hostname denied.txt"); !strings.Contains(out, "NT_STATUS_ACCESS_DENIED") {
		t.Errorf("read-only user was able to write:\n%s", out)
	}
	if _, err := os.Stat(shareDir + "/denied.txt"); err == nil {
		t.Error("read-only user's file exists on disk")
	}
	if out := smb(t, "itest", outsider, password, "ls"); !strings.Contains(out, "NT_STATUS_ACCESS_DENIED") {
		t.Errorf("a user without access could open the share:\n%s", out)
	}
	if out := smb(t, "itest", rwUser, "wrong-password", "ls"); !strings.Contains(out, "NT_STATUS_LOGON_FAILURE") {
		t.Errorf("a wrong password was accepted:\n%s", out)
	}

	// A disabled account cannot log in; a changed password takes effect.
	if err := remote.Accounts.SetEnabled(ctx, rwUser, false); err != nil {
		t.Fatal(err)
	}
	if out := smb(t, "itest", rwUser, password, "ls"); strings.Contains(out, "hello.txt") {
		t.Errorf("a disabled account could still log in:\n%s", out)
	}
	remote.Accounts.SetEnabled(ctx, rwUser, true)
	if err := remote.Accounts.SetPassword(ctx, rwUser, "changed-pass-2"); err != nil {
		t.Fatal(err)
	}
	if out := smb(t, "itest", rwUser, "changed-pass-2", "ls"); !strings.Contains(out, "hello.txt") {
		t.Errorf("new password does not work:\n%s", out)
	}

	// --- the agent refuses dangerous requests on its own ---
	before, _ := os.ReadFile(sysexec.DefaultManagedConfig)
	refused := map[string]sys.ShareSpec{
		"system folder": {Name: "bad", Path: "/etc/samba", Readers: []string{rwUser}},
		"data folder":   {Name: "bad", Path: "/var/lib/netshare-hub"},
		"injection":     {Name: "bad", Path: shareDir, Comment: "x\n[global]\n   guest ok = yes"},
	}
	// A folder that looks harmless but is a symbolic link into /etc.
	link := shareDir + "-link"
	os.Symlink("/etc", link)
	t.Cleanup(func() { os.Remove(link) })
	refused["symlink into a system folder"] = sys.ShareSpec{Name: "bad", Path: link, Readers: []string{rwUser}}

	for name, spec := range refused {
		if err := remote.Samba.ApplyShares(ctx, []sys.ShareSpec{spec}); !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if _, err := remote.Files.CreateShareDir(ctx, link+"/inside-etc"); !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("creating a folder through a link into /etc: got %v, want ErrInvalid", err)
	}
	if _, err := os.Stat("/etc/inside-etc"); err == nil {
		os.Remove("/etc/inside-etc")
		t.Error("a folder was created inside /etc")
	}
	if after, _ := os.ReadFile(sysexec.DefaultManagedConfig); string(after) != string(before) {
		t.Error("a refused request changed the Samba config")
	}
	if err := remote.Accounts.SetPassword(ctx, "root", "whatever-12345"); !errors.Is(err, sys.ErrNotManaged) {
		t.Errorf("SetPassword(root) through the agent = %v, want ErrNotManaged", err)
	}

	// --- removing the share and the users really removes them ---
	if err := remote.Samba.ApplyShares(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if out := smb(t, "itest", rwUser, "changed-pass-2", "ls"); strings.Contains(out, "hello.txt") {
		t.Errorf("share is still reachable after it was removed:\n%s", out)
	}
	if err := remote.Accounts.DeleteUser(ctx, rwUser); err != nil {
		t.Fatal(err)
	}
	if out, _ := sh(t, "getent", "passwd", rwUser); out != "" {
		t.Error("linux account still exists after DeleteUser")
	}
}
