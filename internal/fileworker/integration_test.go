//go:build integration && linux

package fileworker_test

// Real file workers on a real system: started by the real system layer as
// real Linux users. Proves the point of step 1.2: files made in the browser
// belong to the user, and the KERNEL enforces what each user may touch.
// Run ONLY in the disposable test container, as root:
//
//	docker compose -f compose.test.yml run --rm test \
//	    go test -tags integration ./internal/fileworker/

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysexec"
)

func ownerOf(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st := info.Sys().(*syscall.Stat_t)
	u, err := user.LookupId(strconv.Itoa(int(st.Uid)))
	if err != nil {
		return strconv.Itoa(int(st.Uid))
	}
	return u.Username
}

// workerProcesses lists "user args" of running file workers.
func workerProcesses(t *testing.T) string {
	t.Helper()
	all, _ := exec.Command("ps", "-eo", "user:32=,pid=,args=").CombinedOutput()
	var lines []string
	for _, l := range strings.Split(string(all), "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), "netshare-hub fileworker") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	return strings.Join(lines, "\n")
}

func TestRealWorkers(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	// A worker is started as "<program> fileworker". A test binary cannot
	// act as one, so build the real program. It goes in a folder every user
	// can enter: the worker runs as the user, not as root.
	binDir, err := os.MkdirTemp("/usr/local/lib", "nsh-itest-")
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(binDir, 0o755)
	t.Cleanup(func() { os.RemoveAll(binDir) })
	bin := filepath.Join(binDir, "netshare-hub")
	if out, err := exec.Command("go", "build", "-o", bin, "netshare-hub/cmd/netshare-hub").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// The web server runs as its own user (agent mode). Another user in the
	// same group stands in for "some other local process".
	const webUser, otherUser = "nsh-wk-web", "nsh-wk-other"
	exec.Command("useradd", "--no-create-home", webUser).Run()
	exec.Command("useradd", "--no-create-home", "--gid", webUser, otherUser).Run()
	t.Cleanup(func() {
		exec.Command("userdel", otherUser).Run()
		exec.Command("userdel", webUser).Run()
	})

	system, err := sysexec.New(sysexec.Options{Protected: []string{"/var/lib/netshare-hub"}, WebUser: webUser, WorkerExe: bin})
	if err != nil {
		t.Fatal(err)
	}
	const pw = "worker-itest-pass-1"
	users := []string{"nsh-wk-jay", "nsh-wk-ana", "nsh-wk-bob"}
	for _, u := range users {
		if err := system.Accounts.CreateUser(ctx, u, pw); err != nil {
			t.Fatalf("CreateUser(%s): %v", u, err)
		}
	}
	jay, ana, bob := users[0], users[1], users[2]

	base := fmt.Sprintf("/srv/shares/wk-%d", os.Getpid())
	media, err := system.Files.CreateShareDir(ctx, base+"-media")
	if err != nil {
		t.Fatal(err)
	}
	private, err := system.Files.CreateShareDir(ctx, base+"-private")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		system.Samba.ApplyShares(ctx, nil)
		for _, u := range users {
			system.Accounts.DeleteUser(ctx, u)
		}
		os.RemoveAll(media)
		os.RemoveAll(private)
	})

	apply := func(shares ...sys.ShareSpec) {
		t.Helper()
		if err := system.Samba.ApplyShares(ctx, shares); err != nil && !errors.Is(err, sys.ErrReloadFailed) {
			t.Fatalf("ApplyShares: %v", err)
		}
	}
	mediaShare := sys.ShareSpec{Name: "wkmedia", Path: media, Readers: []string{jay, ana}, Writers: []string{jay}}
	apply(mediaShare, sys.ShareSpec{Name: "wkprivate", Path: private, Readers: []string{jay}, Writers: []string{jay}})

	c := fileworker.NewClient(system.Workers)

	// --- only the web server's user may talk to a worker ---
	ep, err := system.Workers.Endpoint(ctx, jay, false)
	if err != nil {
		t.Fatal(err)
	}
	curl := func(as string) string {
		out, _ := exec.Command("sudo", "-u", as, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
			"--unix-socket", ep.Address, "http://worker/v1/list?share=wkmedia").CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	if got := curl(webUser); got != "200" {
		t.Errorf("web server user: status %s, want 200", got)
	}
	if got := curl(otherUser); got != "403" {
		t.Errorf("another user in the web server's group: status %s, want 403", got)
	}
	if got := curl(ana); got != "000" {
		t.Errorf("a share user connecting to someone else's worker: status %s, want 000 (cannot connect)", got)
	}
	if got := curl("nobody"); got != "000" {
		t.Errorf("an unrelated user: status %s, want 000 (cannot connect)", got)
	}

	// --- files made in the browser belong to the user ---
	if _, err := c.Mkdir(ctx, jay, "wkmedia", "films"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := c.Write(ctx, jay, "wkmedia", "films/a.txt", strings.NewReader("hello"), false); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := ownerOf(t, filepath.Join(media, "films", "a.txt")); got != jay {
		t.Errorf("file owner = %s, want %s (the user, not root and not the web server)", got, jay)
	}
	if got := ownerOf(t, filepath.Join(media, "films")); got != jay {
		t.Errorf("folder owner = %s, want %s", got, jay)
	}

	// --- the worker really runs as the user ---
	procs := workerProcesses(t)
	if !strings.Contains(procs, jay) {
		t.Errorf("no worker running as %s:\n%s", jay, procs)
	}
	for _, line := range strings.Split(procs, "\n") {
		if strings.HasPrefix(line, "root ") {
			t.Errorf("a file worker runs as root: %s", line)
		}
	}

	// --- read-only and other people's shares ---
	d, err := c.Read(ctx, ana, "wkmedia", "films/a.txt", "")
	if err != nil {
		t.Fatalf("read-only user could not read: %v", err)
	}
	b, _ := io.ReadAll(d.Body)
	d.Body.Close()
	if string(b) != "hello" {
		t.Errorf("read-only user read %q", b)
	}
	if _, err := c.Write(ctx, ana, "wkmedia", "x.txt", strings.NewReader("x"), false); !errors.Is(err, sys.ErrReadOnly) {
		t.Errorf("read-only user wrote: %v", err)
	}
	if _, err := c.List(ctx, ana, "wkprivate", "", "", 0); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("user listed a share they are not on: %v", err)
	}
	if _, err := c.List(ctx, bob, "wkmedia", "", "", 0); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("user without shares got a worker: %v", err)
	}
	if _, err := c.List(ctx, "root", "wkmedia", "", "", 0); !errors.Is(err, sys.ErrNotManaged) {
		t.Errorf("root got a worker: %v", err)
	}

	// --- the KERNEL enforces permissions, not just our code ---
	// A file inside the share that jay is not allowed to read.
	secret := filepath.Join(media, "root-only.txt")
	if err := os.WriteFile(secret, []byte("root secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(ctx, jay, "wkmedia", "root-only.txt", ""); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("worker read a file the user may not read: %v (want fs.ErrPermission)", err)
	}
	// A symlink inside the share pointing at a system file.
	if err := os.Symlink("/etc/shadow", filepath.Join(media, "shadow")); err != nil {
		t.Fatal(err)
	}
	if d, err := c.Read(ctx, jay, "wkmedia", "shadow", ""); err == nil {
		d.Body.Close()
		t.Error("a symlink out of the share was followed")
	}
	if _, err := c.Write(ctx, jay, "wkmedia", "shadow", strings.NewReader("pwned"), true); err == nil {
		// Writing replaces the link itself (it is inside the share), never /etc/shadow.
		if b, _ := os.ReadFile("/etc/shadow"); strings.Contains(string(b), "pwned") {
			t.Fatal("/etc/shadow was written through a symlink")
		}
	}

	// --- background work (jobs) runs in a separate, low-priority worker ---
	bgCtx := sys.WithBackground(ctx)
	if _, err := c.Write(bgCtx, jay, "wkmedia", "films/bg.txt", strings.NewReader("background"), false); err != nil {
		t.Fatalf("background write: %v", err)
	}
	if got := ownerOf(t, filepath.Join(media, "films", "bg.txt")); got != jay {
		t.Errorf("background file owner = %s, want %s", got, jay)
	}
	bgEp, err := system.Workers.Endpoint(ctx, jay, true)
	if err != nil {
		t.Fatal(err)
	}
	fgEp, _ := system.Workers.Endpoint(ctx, jay, false)
	if bgEp.Address == fgEp.Address {
		t.Error("background and normal work must use different workers")
	}
	bgPID, fgPID := "", ""
	psOut, _ := exec.Command("ps", "-eo", "pid=,ni=,user:32=,args=").CombinedOutput()
	for _, line := range strings.Split(string(psOut), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[2] != jay || !strings.HasSuffix(line, "netshare-hub fileworker") {
			continue
		}
		if f[1] == "10" {
			bgPID = f[0]
		} else {
			fgPID = f[0]
		}
	}
	if bgPID == "" || fgPID == "" {
		t.Fatalf("want one normal and one background (nice 10) worker for %s:\n%s", jay, psOut)
	}
	if out, _ := exec.Command("ionice", "-p", bgPID).CombinedOutput(); !strings.Contains(string(out), "idle") {
		t.Errorf("background worker IO class = %q, want idle", strings.TrimSpace(string(out)))
	}
	if out, _ := exec.Command("ionice", "-p", fgPID).CombinedOutput(); strings.Contains(string(out), "idle") {
		t.Errorf("normal worker must not be idle priority: %q", strings.TrimSpace(string(out)))
	}

	// --- an unrelated share change leaves a running worker alone ---
	before := workerProcesses(t)
	apply(mediaShare,
		sys.ShareSpec{Name: "wkprivate", Path: private, Readers: []string{jay}, Writers: []string{jay}, Comment: "changed"})
	if after := workerProcesses(t); !strings.Contains(after, jay) || !strings.Contains(before, jay) {
		t.Errorf("a comment change stopped jay's worker:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// --- access changes take effect at once ---
	apply(sys.ShareSpec{Name: "wkmedia", Path: media, Readers: []string{jay}, Writers: []string{jay}})
	if _, err := c.List(ctx, ana, "wkmedia", "", "", 0); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("user still reaches a share after being removed: %v", err)
	}
	if _, err := c.List(ctx, jay, "wkmedia", "", "", 0); err != nil {
		t.Errorf("jay after the change: %v", err)
	}

	// Disabling the account stops its worker.
	if err := system.Accounts.SetEnabled(ctx, jay, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if procs := workerProcesses(t); strings.Contains(procs, jay) {
		t.Errorf("worker still running after the account was disabled:\n%s", procs)
	}
	// ...and no new one starts, for normal or background work.
	if _, err := c.List(ctx, jay, "wkmedia", "", "", 0); !errors.Is(err, sys.ErrDisabled) {
		t.Errorf("disabled account got a worker: %v", err)
	}
	if _, err := c.List(sys.WithBackground(ctx), jay, "wkmedia", "", "", 0); !errors.Is(err, sys.ErrDisabled) {
		t.Errorf("disabled account got a background worker: %v", err)
	}
	system.Accounts.SetEnabled(ctx, jay, true)
	if _, err := c.List(ctx, jay, "wkmedia", "", "", 0); err != nil {
		t.Fatalf("re-enabled user: %v", err)
	}
	// Deleting the account works even while its worker runs (userdel
	// refuses users with running processes).
	if err := system.Accounts.DeleteUser(ctx, jay); err != nil {
		t.Errorf("DeleteUser with a running worker: %v", err)
	}
}
