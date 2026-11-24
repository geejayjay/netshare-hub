//go:build integration && linux

package sysexec

// Real rsync and SSH (step 4.1). Needs rsync, openssh-client and
// openssh-server in the test container (see deploy/test/Dockerfile).

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"netshare-hub/internal/sys"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func replicationSetup(t *testing.T) (*Replication, *Samba, string) {
	t.Helper()
	requireRoot(t)
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync is not installed")
	}
	base := "/srv/repl-itest"
	_ = os.RemoveAll(base)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	src := base + "/src"
	write(t, src+"/a.txt", "a")
	write(t, src+"/sub/b.txt", "b")
	write(t, src+"/skip.tmp", "tmp")
	if err := os.Chown(src+"/a.txt", 1234, 1234); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("setfacl", "-m", "u:4321:r--", src+"/a.txt").CombinedOutput(); err != nil {
		t.Fatalf("setfacl: %s", b)
	}
	tmp := t.TempDir()
	samba := NewSamba(ExecRunner{}, tmp+"/netshare-hub.conf", tmp+"/smb.conf")
	if err := samba.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{{Name: "src", Path: src}}}); err != nil &&
		!errors.Is(err, sys.ErrReloadFailed) {
		t.Fatalf("apply samba: %v", err)
	}
	return NewReplication(ExecRunner{}, samba, tmp+"/replication"), samba, base
}

func TestRealReplicationLocal(t *testing.T) {
	r, _, base := replicationSetup(t)
	dst := base + "/dst"
	run := sys.ReplicationRun{Share: "src", LocalPath: dst, Excludes: []string{"*.tmp"}}

	stats, err := r.Replicate(ctx, run, nil)
	if err != nil {
		t.Fatalf("replicate: %v", err)
	}
	if stats.Transferred != 2 || !exists(dst+"/sub/b.txt") || exists(dst+"/skip.tmp") {
		t.Errorf("first copy: %+v", stats)
	}
	// Owners and ACLs are kept, so the copy can be shared again as it was.
	var st syscall.Stat_t
	if err := syscall.Stat(dst+"/a.txt", &st); err != nil || st.Uid != 1234 {
		t.Errorf("owner of the copy: %d %v", st.Uid, err)
	}
	if acl := out(t, "getfacl", "-n", dst+"/a.txt"); !strings.Contains(acl, "user:4321:r--") {
		t.Errorf("ACL of the copy:\n%s", acl)
	}

	// Deleted at the source: a preview with mirror lists the deletion and
	// changes nothing; keep-deleted keeps it; mirror removes it.
	if err := os.Remove(base + "/src/a.txt"); err != nil {
		t.Fatal(err)
	}
	run.Mirror, run.DryRun = true, true
	stats, err = r.Replicate(ctx, run, nil)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(strings.Join(stats.Changes, "\n"), "*deleting   a.txt") || !exists(dst+"/a.txt") {
		t.Errorf("preview: %+v", stats.Changes)
	}
	run.Mirror, run.DryRun = false, false
	if _, err := r.Replicate(ctx, run, nil); err != nil || !exists(dst+"/a.txt") {
		t.Errorf("keep-deleted removed a file: %v", err)
	}
	run.Mirror = true
	if stats, err = r.Replicate(ctx, run, nil); err != nil || exists(dst+"/a.txt") || stats.Deleted != 1 {
		t.Errorf("mirror kept a deleted file: %+v %v", stats, err)
	}

	// Refused: a destination in, around or as a share; a system folder; an
	// unknown share; an exclude that is an option.
	for name, bad := range map[string]sys.ReplicationRun{
		"inside the share": {Share: "src", LocalPath: base + "/src/copy"},
		"holds the share":  {Share: "src", LocalPath: base},
		"system folder":    {Share: "src", LocalPath: "/etc/copy"},
		"option exclude":   {Share: "src", LocalPath: dst, Excludes: []string{"--rsh=sh"}},
	} {
		if _, err := r.Replicate(ctx, bad, nil); !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.Replicate(ctx, sys.ReplicationRun{Share: "nope", LocalPath: dst}, nil); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("unknown share: %v", err)
	}
	// A symbolic link in the share is copied as a link, never followed.
	if err := os.Symlink("/etc/shadow", base+"/src/shadow"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Replicate(ctx, sys.ReplicationRun{Share: "src", LocalPath: dst}, nil); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(dst + "/shadow"); err != nil || target != "/etc/shadow" {
		t.Errorf("link: %q %v", target, err)
	}
}

func startSSHD(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Skip("openssh-server is not installed")
	}
	if exec.Command("pgrep", "-x", "sshd").Run() == nil {
		return
	}
	_ = os.MkdirAll("/run/sshd", 0o755)
	if b, err := exec.Command("ssh-keygen", "-A").CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -A: %s", b)
	}
	if b, err := exec.Command("/usr/sbin/sshd").CombinedOutput(); err != nil {
		t.Fatalf("sshd: %s", b)
	}
}

func TestRealReplicationRemote(t *testing.T) {
	r, _, base := replicationSetup(t)
	startSSHD(t)
	t.Cleanup(func() {
		_ = os.RemoveAll(replicaHome)
		_ = exec.Command("userdel", sys.ReplicaAccount).Run()
	})

	key, err := r.PublicKey(ctx)
	if err != nil || !sys.ValidSSHKey(key) {
		t.Fatalf("key: %q %v", key, err)
	}
	if again, _ := r.PublicKey(ctx); again != key {
		t.Error("the key changed between calls")
	}
	if info, err := os.Stat(r.dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("key folder: %v %v", info.Mode(), err)
	}
	hostKey, err := r.HostKey(ctx)
	if err != nil {
		t.Fatal(err)
	}

	in := base + "/in"
	// Refused receiving folders: shell characters, a share, a system folder.
	for name, path := range map[string]string{"shell": base + "/in;id", "space": base + "/in x", "share": base + "/src/in",
		"system": "/etc/in"} {
		if err := r.SetReceivers(ctx, []sys.Receiver{{ID: "r1", Path: path, PublicKey: key}}); !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("receiver %s: %v", name, err)
		}
	}
	if err := r.SetReceivers(ctx, []sys.Receiver{{ID: "r1", Path: in, PublicKey: key}}); err != nil {
		t.Fatalf("SetReceivers: %v", err)
	}
	keys := out(t, "cat", replicaHome+"/.ssh/authorized_keys")
	if !strings.Contains(keys, `restrict,command="/usr/bin/rrsync -wo `+in+`" `+key) {
		t.Errorf("authorized_keys:\n%s", keys)
	}
	var st syscall.Stat_t
	_ = syscall.Stat(replicaHome+"/.ssh/authorized_keys", &st)
	if st.Uid != 0 {
		t.Error("the receiving account owns its own key file")
	}

	if err := r.SetPeers(ctx, []sys.Peer{{ID: "p1", Host: "127.0.0.1", Port: 22, HostKey: hostKey}}); err != nil {
		t.Fatal(err)
	}
	stats, err := r.Replicate(ctx, sys.ReplicationRun{Share: "src", Peer: "p1", Mirror: true}, nil)
	if err != nil {
		t.Fatalf("replicate to peer: %v", err)
	}
	if stats.Transferred < 2 || !exists(in+"/sub/b.txt") {
		t.Errorf("remote copy: %+v", stats)
	}
	_ = syscall.Stat(in+"/sub/b.txt", &st)
	if owner := out(t, "id", "-u", sys.ReplicaAccount); strings.TrimSpace(owner) != itoa(int(st.Uid)) {
		t.Errorf("remote copy belongs to %d, want %s", st.Uid, owner)
	}

	ssh := []string{"-i", r.keyFile(), "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + r.knownHosts("p1"), "-l", sys.ReplicaAccount, "127.0.0.1"}
	// The key cannot open a shell or run anything else.
	if b, err := exec.Command("ssh", append(ssh, "id")...).CombinedOutput(); err == nil || strings.Contains(string(b), "uid=") {
		t.Errorf("the key ran a command: %s", b)
	}
	// It cannot read the copies back.
	back := t.TempDir()
	rsh := "ssh " + strings.Join(ssh[:len(ssh)-1], " ")
	if b, err := exec.Command("rsync", "-r", "-e", rsh, "127.0.0.1:.", back).CombinedOutput(); err == nil || exists(back+"/sub") {
		t.Errorf("read back from a write-only receiver: %s", b)
	}
	// It cannot write outside its folder.
	_ = exec.Command("rsync", "-r", "-e", rsh, base+"/src/", "127.0.0.1:../escaped/").Run()
	if exists(base + "/escaped") {
		t.Error("wrote outside the receiving folder")
	}

	// A peer whose host key does not match the pinned one is never sent
	// anything.
	if err := r.SetPeers(ctx, []sys.Peer{{ID: "p1", Host: "127.0.0.1", Port: 22,
		HostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}); err != nil {
		t.Fatal(err)
	}
	write(t, base+"/src/new.txt", "new")
	var re *sys.ReplicationError
	if _, err := r.Replicate(ctx, sys.ReplicationRun{Share: "src", Peer: "p1"}, nil); !errors.As(err, &re) {
		t.Errorf("wrong host key: %v", err)
	}
	if exists(in + "/new.txt") {
		t.Error("sent to a host with the wrong key")
	}

	// Removing the receiver takes its key away.
	if err := r.SetReceivers(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if keys := out(t, "cat", replicaHome+"/.ssh/authorized_keys"); strings.Contains(keys, "ssh-ed25519") {
		t.Errorf("key kept after the receiver was removed:\n%s", keys)
	}
}
