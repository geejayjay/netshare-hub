//go:build integration && linux

package sysexec

// Real NFS (step 4.2). Needs the test container run privileged (the kernel
// NFS server, mounts) with nfs-kernel-server installed; see compose.test.yml.

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"netshare-hub/internal/sys"
)

const nfsUser = "nsh-nfs-itest"

// startNFS starts the kernel NFS server, or skips the test. The tests mount
// with NFSv3: NFSv4 builds its pseudo-root from "/", which in a container
// is an overlay filesystem the kernel cannot export (a real host has no
// such problem). Exports, squashing and client lists are the same for both.
func startNFS(t *testing.T) {
	t.Helper()
	requireRoot(t)
	if _, err := exec.LookPath("exportfs"); err != nil {
		t.Skip("nfs-kernel-server is not installed")
	}
	if _, err := os.Stat("/proc/fs/nfsd/threads"); err != nil {
		_ = os.MkdirAll("/proc/fs/nfsd", 0o755)
		if b, err := exec.Command("mount", "-t", "nfsd", "nfsd", "/proc/fs/nfsd").CombinedOutput(); err != nil {
			t.Skipf("no kernel NFS server here (run the container privileged): %s", b)
		}
	}
	if !(&NFS{proc: "/proc"}).running() {
		_ = exec.Command("rpcbind").Run()
		if b, err := exec.Command("rpc.nfsd", "4").CombinedOutput(); err != nil {
			t.Skipf("rpc.nfsd: %s", b)
		}
	}
	if exec.Command("pgrep", "-x", "rpc.mountd").Run() != nil {
		if b, err := exec.Command("rpc.mountd").CombinedOutput(); err != nil {
			t.Fatalf("rpc.mountd: %s", b)
		}
	}
}

func mountNFS(t *testing.T, path, at string, extra ...string) error {
	t.Helper()
	_ = os.MkdirAll(at, 0o755)
	args := append([]string{"-t", "nfs", "-o", "vers=3,nolock,soft,timeo=20,retrans=1"}, extra...)
	b, err := exec.Command("mount", append(args, "127.0.0.1:"+path, at)...).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(b)))
	}
	t.Cleanup(func() { _ = exec.Command("umount", "-f", "-l", at).Run() })
	return nil
}

func TestRealNFS(t *testing.T) {
	startNFS(t)
	dir := "/srv/nfs-itest"
	_ = os.MkdirAll(dir, 0o755)
	// tmpfs: a real filesystem with ACLs that NFS can export (the
	// container's own overlay filesystem cannot be exported reliably).
	if b, err := exec.Command("mount", "-t", "tmpfs", "tmpfs", dir).CombinedOutput(); err != nil {
		t.Fatalf("mount tmpfs: %s", b)
	}
	t.Cleanup(func() { _ = exec.Command("umount", "-l", dir).Run() })
	share := dir + "/media"
	if err := os.Mkdir(share, 0o755); err != nil {
		t.Fatal(err)
	}

	r := ExecRunner{}
	accounts := NewAccounts(r)
	t.Cleanup(func() { _ = accounts.DeleteUser(ctx, nfsUser) })
	if err := accounts.CreateUser(ctx, nfsUser, "nfs-password-1"); err != nil {
		t.Fatal(err)
	}
	uid, gid, err := (&NFS{runner: r, accounts: accounts}).managedIDs(ctx, nfsUser)
	if err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	samba := NewSamba(r, tmp+"/netshare-hub.conf", tmp+"/smb.conf")
	exportsFile := "/etc/exports.d/netshare-hub-itest.exports"
	nfs := NewNFS(r, accounts, samba, exportsFile)
	samba.afterApply = func() { nfs.Refresh(ctx) }
	accounts.onEnabled = func() { nfs.Refresh(ctx) }
	t.Cleanup(func() {
		_ = os.Remove(exportsFile)
		_ = os.Remove(exportsFile + ".json")
		_ = exec.Command("exportfs", "-ra").Run()
	})

	apply := func(spec sys.ShareSpec) {
		t.Helper()
		if err := samba.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{spec}}); err != nil && !errors.Is(err, sys.ErrReloadFailed) {
			t.Fatalf("apply samba: %v", err)
		}
	}
	writer := sys.ShareSpec{Name: "media", Path: share, Readers: []string{nfsUser}, Writers: []string{nfsUser}}
	apply(writer)

	// Status.
	st, err := nfs.NFSStatus(ctx)
	if err != nil || !st.Installed || !st.Running || !st.Supported {
		t.Fatalf("status: %+v %v", st, err)
	}

	// Export as the user, to this machine only.
	export := sys.NFSExport{Share: "media", AsUser: nfsUser, Clients: []sys.NFSClient{{Address: "127.0.0.1"}}}
	if err := nfs.ApplyExports(ctx, []sys.NFSExport{export}); err != nil {
		t.Fatalf("ApplyExports: %v", err)
	}
	if v := out(t, "exportfs", "-v"); !strings.Contains(v, share) || !strings.Contains(v, "all_squash") ||
		!strings.Contains(v, "anonuid="+itoa(uid)) {
		t.Fatalf("kernel exports:\n%s", v)
	}

	// Root on the client writes as the user: root squashed, all squashed.
	mnt := t.TempDir() + "/m"
	if err := mountNFS(t, share, mnt); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if err := os.WriteFile(mnt+"/hello.txt", []byte("hi"), 0o644); err != nil {
		t.Fatalf("write over NFS: %v", err)
	}
	var info syscall.Stat_t
	if err := syscall.Stat(share+"/hello.txt", &info); err != nil {
		t.Fatal(err)
	}
	if int(info.Uid) != uid || int(info.Gid) != gid {
		t.Errorf("file written over NFS belongs to %d:%d, want the user %d:%d", info.Uid, info.Gid, uid, gid)
	}
	_ = exec.Command("umount", "-f", "-l", mnt).Run()

	// A disabled account is never acted as; enabled again, it is.
	if err := accounts.SetEnabled(ctx, nfsUser, false); err != nil {
		t.Fatal(err)
	}
	if v := out(t, "exportfs", "-v"); strings.Contains(v, share) {
		t.Errorf("exported as a disabled account:\n%s", v)
	}
	if err := accounts.SetEnabled(ctx, nfsUser, true); err != nil {
		t.Fatal(err)
	}
	if v := out(t, "exportfs", "-v"); !strings.Contains(v, share) {
		t.Errorf("export did not come back after enabling:\n%s", v)
	}

	// The user can only read now: the root side makes the export read-only
	// by itself after the Samba change (Refresh), without a new request.
	apply(sys.ShareSpec{Name: "media", Path: share, Readers: []string{nfsUser}})
	if v := out(t, "exportfs", "-v"); !(strings.Contains(v, ",ro,") || strings.Contains(v, "(ro,")) || strings.Contains(v, ",rw,") {
		t.Errorf("not read-only after the user lost write:\n%s", v)
	}
	mnt2 := t.TempDir() + "/m"
	if err := mountNFS(t, share, mnt2); err != nil {
		t.Fatalf("mount read-only export: %v", err)
	}
	if err := os.WriteFile(mnt2+"/nope.txt", []byte("x"), 0o644); err == nil {
		t.Error("wrote to a read-only export")
	}
	if b, err := os.ReadFile(mnt2 + "/hello.txt"); err != nil || string(b) != "hi" {
		t.Errorf("read over NFS: %q %v", b, err)
	}
	_ = exec.Command("umount", "-f", "-l", mnt2).Run()

	// A computer that is not on the list cannot mount.
	apply(writer)
	other := sys.NFSExport{Share: "media", AsUser: nfsUser, Clients: []sys.NFSClient{{Address: "10.255.255.1"}}}
	if err := nfs.ApplyExports(ctx, []sys.NFSExport{other}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := mountNFS(t, share, t.TempDir()+"/m"); err == nil {
		t.Error("mounted from an address that is not on the client list")
	}

	// Refused by the root side: a folder that is not a live share, a system
	// account, a share the user cannot read.
	for name, bad := range map[string]sys.NFSExport{
		"not a share":    {Share: "etc", Clients: []sys.NFSClient{{Address: "127.0.0.1"}}},
		"system account": {Share: "media", AsUser: "root", Clients: []sys.NFSClient{{Address: "127.0.0.1"}}},
		"everyone":       {Share: "media", AsUser: nfsUser, Clients: []sys.NFSClient{{Address: "0.0.0.0/0"}}},
	} {
		if err := nfs.ApplyExports(ctx, []sys.NFSExport{bad}); !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Stop sharing: the export goes with the share, by itself.
	if err := samba.ApplyConfig(ctx, sys.SambaConfig{}); err != nil && !errors.Is(err, sys.ErrReloadFailed) {
		t.Fatal(err)
	}
	if v := out(t, "exportfs", "-v"); strings.Contains(v, share) {
		t.Errorf("export outlived its share:\n%s", v)
	}
	// And an empty list with nothing exported is a no-op.
	if err := nfs.ApplyExports(ctx, nil); err != nil {
		t.Errorf("empty apply: %v", err)
	}
}
