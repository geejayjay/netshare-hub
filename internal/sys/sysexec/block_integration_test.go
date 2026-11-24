//go:build integration && linux

package sysexec

// Real iSCSI (LIO) and NVMe over TCP (nvmet) targets, with real initiators
// on the same machine (step 5.1). Needs the test container privileged, the
// kernel modules available, and open-iscsi and nvme-cli installed.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/sys"
)

const (
	allowedIQN = "iqn.2025-01.test.netshare-hub:allowed"
	otherIQN   = "iqn.2025-01.test.netshare-hub:other"
	allowedNQN = "nqn.2014-08.org.nvmexpress:uuid:11111111-2222-3333-4444-555555555555"
	otherNQN   = "nqn.2014-08.org.nvmexpress:uuid:99999999-2222-3333-4444-555555555555"
)

func blockSetup(t *testing.T) (*Block, string) {
	t.Helper()
	requireRoot(t)
	if entries, _ := os.ReadDir("/sys/kernel/config"); len(entries) == 0 {
		_ = exec.Command("mount", "-t", "configfs", "none", "/sys/kernel/config").Run()
	}
	for _, m := range []string{"target_core_mod", "target_core_file", "target_core_iblock", "iscsi_target_mod", "nvmet", "nvmet_tcp",
		"iscsi_tcp", "nvme_tcp"} {
		_ = exec.Command("modprobe", m).Run()
	}
	base := "/srv/blk-itest"
	_ = os.RemoveAll(base)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.MkdirAll(base+"/share", 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	samba := NewSamba(ExecRunner{}, tmp+"/netshare-hub.conf", tmp+"/smb.conf")
	if err := samba.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{{Name: "share", Path: base + "/share"}}}); err != nil &&
		!errors.Is(err, sys.ErrReloadFailed) {
		t.Fatal(err)
	}
	b := NewBlock(samba, tmp+"/block")
	if reason := b.unavailable(); reason != "" {
		t.Skip(reason)
	}
	t.Cleanup(func() { _ = b.ApplyDisks(ctx, nil) })
	return b, base
}

func run2(name string, args ...string) (string, error) {
	b, err := exec.Command(name, args...).CombinedOutput()
	return string(b), err
}

func TestRealISCSI(t *testing.T) {
	b, base := blockSetup(t)
	if !b.iscsiReady() {
		t.Skip("the iSCSI target modules are not loaded")
	}
	// A target an admin made by hand: never touched.
	foreign := filepath.Join(b.root, "target", "iscsi", "iqn.2003-01.org.example:foreign")
	_ = os.Mkdir(filepath.Dir(foreign), 0o755)
	if err := os.Mkdir(foreign, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(foreign) })

	disk := sys.BlockDisk{ID: "d1", Protocol: sys.ProtocolISCSI, ImageFolder: base + "/images", SizeGiB: 1, Client: allowedIQN,
		CHAPUser: "jay", CHAPSecret: "s3cret-pass12", Listen: "127.0.0.1", Port: 3260}
	// Refused before anything is made: an image folder in a share.
	bad := disk
	bad.ImageFolder = base + "/share/images"
	var de *sys.DiskError
	if err := b.ApplyDisks(ctx, []sys.BlockDisk{bad}); !errors.As(err, &de) || !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("image in a share: %v", err)
	}
	if err := b.ApplyDisks(ctx, []sys.BlockDisk{disk}); err != nil {
		t.Fatalf("ApplyDisks: %v", err)
	}
	if info, err := os.Stat(disk.ImagePath()); err != nil || info.Size() != 1<<30 || info.Mode().Perm() != 0o600 {
		t.Fatalf("image: %v %v", info, err)
	}
	tpg := filepath.Join(b.iscsiTarget(disk), "tpgt_1")
	if v := readTrim(filepath.Join(tpg, "attrib", "generate_node_acls")); v != "0" {
		t.Errorf("generate_node_acls = %s", v)
	}

	// A real iSCSI login over TCP, from libiscsi's userspace initiator (the
	// kernel initiator's daemon cannot run in a container). It reads the
	// LUN's capacity, so it gets as far as using the disk.
	if _, err := exec.LookPath("iscsi-readcapacity16"); err != nil {
		t.Skip("libiscsi-bin is not installed")
	}
	target := disk.TargetName()
	capacity := func(initiator, user, pass string) (string, error) {
		url := "iscsi://127.0.0.1:3260/" + target + "/0"
		if user != "" {
			url = "iscsi://" + user + "%" + pass + "@127.0.0.1:3260/" + target + "/0"
		}
		return run2("iscsi-readcapacity16", "-i", initiator, url)
	}
	if out, err := capacity(allowedIQN, "jay", "wrong-pass-123"); err == nil {
		t.Errorf("logged in with the wrong CHAP secret: %s", out)
	}
	if out, err := capacity(allowedIQN, "", ""); err == nil {
		t.Errorf("logged in without CHAP: %s", out)
	}
	out, err := capacity(allowedIQN, "jay", "s3cret-pass12")
	if err != nil {
		t.Fatalf("the named initiator could not log in: %s", out)
	}
	if !strings.Contains(out, "1073741824") && !strings.Contains(out, "2097152") {
		t.Errorf("capacity: %s", out)
	}
	// Another initiator is refused, even with the right secret.
	if out, err := capacity(otherIQN, "jay", "s3cret-pass12"); err == nil {
		t.Errorf("a second initiator logged in: %s", out)
	}

	// Read-only: the client sees a write-protected disk.
	disk.ReadOnly = true
	if err := b.ApplyDisks(ctx, []sys.BlockDisk{disk}); err != nil {
		t.Fatal(err)
	}
	if v := readTrim(filepath.Join(tpg, "acls", allowedIQN, "lun_0", "write_protect")); v != "1" {
		t.Errorf("write_protect = %q", v)
	}

	// Removing the disk removes NetShare Hub's objects only; the image stays.
	if err := b.ApplyDisks(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.iscsiTarget(disk)); err == nil {
		t.Error("the target is still there")
	}
	if _, err := os.Stat(b.backstore(disk)); err == nil {
		t.Error("the backstore is still there")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("a target NetShare Hub did not make was removed")
	}
	if _, err := os.Stat(disk.ImagePath()); err != nil {
		t.Error("the image (the client's data) was deleted")
	}
}

func TestRealNVMeOF(t *testing.T) {
	b, base := blockSetup(t)
	if !b.nvmeReady() {
		t.Skip("the NVMe target modules are not loaded")
	}
	if _, err := exec.LookPath("nvme"); err != nil {
		t.Skip("nvme-cli is not installed")
	}
	disk := sys.BlockDisk{ID: "d2", Protocol: sys.ProtocolNVMeOF, ImageFolder: base + "/images", SizeGiB: 1, Client: allowedNQN,
		Listen: "127.0.0.1", Port: 4420}
	if err := b.ApplyDisks(ctx, []sys.BlockDisk{disk}); err != nil {
		t.Fatalf("ApplyDisks: %v", err)
	}
	nqn := disk.TargetName()
	if v := readTrim(b.nvmet("subsystems", nqn, "attr_allow_any_host")); v != "0" {
		t.Errorf("allow_any_host = %s", v)
	}
	t.Cleanup(func() { _, _ = run2("nvme", "disconnect", "-n", nqn) })

	// Another host is refused.
	if out, err := run2("nvme", "connect", "-t", "tcp", "-a", "127.0.0.1", "-s", "4420", "-n", nqn, "--hostnqn", otherNQN); err == nil &&
		!strings.Contains(out, "failed") {
		t.Errorf("a second host connected: %s", out)
		_, _ = run2("nvme", "disconnect", "-n", nqn)
	}
	// The named host gets the disk.
	if out, err := run2("nvme", "connect", "-t", "tcp", "-a", "127.0.0.1", "-s", "4420", "-n", nqn, "--hostnqn", allowedNQN); err != nil {
		t.Fatalf("connect: %s", out)
	}
	found := false
	for i := 0; i < 50 && !found; i++ {
		ctrls, _ := filepath.Glob("/sys/class/nvme/nvme*")
		for _, c := range ctrls {
			found = found || readTrim(c+"/subsysnqn") == nqn
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatal("no NVMe controller for the subsystem")
	}
	if out, err := run2("nvme", "disconnect", "-n", nqn); err != nil {
		t.Errorf("disconnect: %s", out)
	}

	// Two disks share one port; removing one keeps the port for the other.
	disk2 := disk
	disk2.ID, disk2.Client = "d3", otherNQN
	if err := b.ApplyDisks(ctx, []sys.BlockDisk{disk, disk2}); err != nil {
		t.Fatal(err)
	}
	ports, _ := os.ReadDir(b.nvmet("ports"))
	ours := 0
	for _, p := range ports {
		if strings.HasPrefix(p.Name(), "77") {
			ours++
		}
	}
	if ours != 1 {
		t.Errorf("%d NetShare Hub ports for one address", ours)
	}
	if err := b.ApplyDisks(ctx, []sys.BlockDisk{disk2}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.nvmet("subsystems", nqn)); err == nil {
		t.Error("removed disk's subsystem is still there")
	}
	if _, err := os.Stat(b.nvmet("hosts", allowedNQN)); err == nil {
		t.Error("an unused host entry is still there")
	}
	if err := b.ApplyDisks(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.nvmet("ports", "7700")); err == nil {
		t.Error("an unused port is still there")
	}
}
