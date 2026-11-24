//go:build linux

package sysexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"netshare-hub/internal/sys"
)

// Block storage through the kernel's configfs: LIO for iSCSI
// (/sys/kernel/config/target) and nvmet for NVMe over TCP
// (/sys/kernel/config/nvmet). No tool is run: every change is a mkdir, a
// symlink or a write of a fixed attribute, with values that passed
// sys.BlockDisk.Check and the folder and device checks here.

// DefaultBlockDir keeps the record of the disks applied (it holds CHAP
// secrets: root only).
const DefaultBlockDir = "/var/lib/netshare-hub-agent/block"

const (
	fileioHBA = "fileio_77"
	iblockHBA = "iblock_77"
	// nvmet ports are numbers; NetShare Hub uses this range.
	firstPort = 7700
	lastPort  = 7799
)

// Block is the real sys.Block.
type Block struct {
	root      string // configfs
	proc      string
	sysfs     string
	dev       string // /dev/disk/by-id
	dir       string
	samba     *Samba
	protected []string
	mu        sync.Mutex
}

func NewBlock(samba *Samba, dir string, protected ...string) *Block {
	return &Block{root: "/sys/kernel/config", proc: "/proc", sysfs: "/sys", dev: "/dev/disk/by-id", dir: dir, samba: samba,
		protected: protected}
}

// --- status ---

func (b *Block) moduleLoaded(name string) bool {
	_, err := os.Stat(filepath.Join(b.sysfs, "module", name))
	return err == nil
}

// unavailable says why no target can be configured, or "".
func (b *Block) unavailable() string {
	if unprivilegedContainer(b.proc) {
		return "This machine is an unprivileged container: the kernel's iSCSI and NVMe targets cannot run in it. " +
			"Run NetShare Hub on the Proxmox host or in a VM for block storage."
	}
	if _, err := os.Stat(b.root); err != nil {
		return "configfs is not mounted (mount -t configfs none /sys/kernel/config)."
	}
	if err := syscall.Access(b.root, 2 /* W_OK */); errors.Is(err, syscall.EROFS) {
		return "configfs is read-only for the agent. With systemd, allow it in a drop-in for netshare-hub-agent.service: " +
			"[Service] ReadWritePaths=/sys/kernel/config"
	}
	return ""
}

// iscsiReady: LIO is loaded with its iSCSI fabric and both backstores.
// The fabric's configfs folder (target/iscsi) only appears once someone
// creates it, so it is not a test.
func (b *Block) iscsiReady() bool {
	_, err := os.Stat(filepath.Join(b.root, "target", "core"))
	return err == nil && b.moduleLoaded("iscsi_target_mod") && b.moduleLoaded("target_core_file") && b.moduleLoaded("target_core_iblock")
}

func (b *Block) nvmeReady() bool {
	_, err := os.Stat(filepath.Join(b.root, "nvmet"))
	return err == nil && b.moduleLoaded("nvmet_tcp")
}

func (b *Block) BlockStatus(context.Context) (sys.BlockStatus, error) {
	st := sys.BlockStatus{Disks: []sys.DiskState{}}
	if reason := b.unavailable(); reason != "" {
		st.Reason = reason
		return st, nil
	}
	st.ISCSI, st.NVMeOF = b.iscsiReady(), b.nvmeReady()
	var missing []string
	if !st.ISCSI {
		missing = append(missing, "target_core_mod target_core_file target_core_iblock iscsi_target_mod")
	}
	if !st.NVMeOF {
		missing = append(missing, "nvmet nvmet_tcp")
	}
	if len(missing) > 0 {
		st.Reason = "Kernel modules not loaded: " + strings.Join(missing, " ") + ". Load them with modprobe and list them in " +
			"/etc/modules-load.d/netshare-hub.conf so they load at boot."
	}
	b.mu.Lock()
	applied, _ := b.load()
	b.mu.Unlock()
	for _, d := range applied {
		s := sys.DiskState{ID: d.ID, Protocol: d.Protocol}
		if d.Protocol == sys.ProtocolISCSI {
			info, err := os.ReadFile(filepath.Join(b.iscsiTarget(d), "tpgt_1", "acls", d.Client, "info"))
			if err == nil {
				c := !strings.Contains(string(info), "No active")
				s.Connected = &c
			}
		}
		st.Disks = append(st.Disks, s)
	}
	return st, nil
}

// --- devices ---

func (b *Block) Devices(context.Context) ([]sys.DiskDevice, error) {
	entries, err := os.ReadDir(b.dev)
	if errors.Is(err, fs.ErrNotExist) {
		return []sys.DiskDevice{}, nil
	}
	if err != nil {
		return nil, err
	}
	byKernel := map[string]sys.DiskDevice{}
	for _, e := range entries {
		p := b.dev + "/" + e.Name()
		if !sys.ValidDiskDevice(p) {
			continue
		}
		kernel, err := b.kernelName(p)
		if err != nil || b.isPartition(kernel) {
			continue
		}
		d := sys.DiskDevice{Path: p, Kernel: kernel, SizeBytes: b.size(kernel), Model: b.model(kernel),
			Partitions: len(b.partitions(kernel)), InUse: b.inUse(kernel)}
		// One entry per disk; prefer a readable id over a wwn- one.
		if old, ok := byKernel[kernel]; !ok || (strings.HasPrefix(filepath.Base(old.Path), "wwn-") &&
			!strings.HasPrefix(e.Name(), "wwn-")) {
			byKernel[kernel] = d
		}
	}
	out := make([]sys.DiskDevice, 0, len(byKernel))
	for _, d := range byKernel {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (b *Block) kernelName(byID string) (string, error) {
	real, err := filepath.EvalSymlinks(byID)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(real, "/dev/") || strings.Contains(real[5:], "/") {
		return "", fmt.Errorf("%s is not a disk", byID)
	}
	return real[5:], nil
}

func (b *Block) classDir(kernel string) string {
	return filepath.Join(b.sysfs, "class", "block", kernel)
}

func (b *Block) isPartition(kernel string) bool {
	_, err := os.Stat(filepath.Join(b.classDir(kernel), "partition"))
	return err == nil
}

func (b *Block) partitions(kernel string) []string {
	entries, _ := os.ReadDir(b.classDir(kernel))
	var out []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(b.classDir(kernel), e.Name(), "partition")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

func readTrim(p string) string {
	raw, _ := os.ReadFile(p)
	return strings.TrimSpace(string(raw))
}

func (b *Block) size(kernel string) int64 {
	n, _ := strconv.ParseInt(readTrim(filepath.Join(b.classDir(kernel), "size")), 10, 64)
	return n * 512
}

func (b *Block) model(kernel string) string {
	return readTrim(filepath.Join(b.classDir(kernel), "device", "model"))
}

// inUse says why a disk cannot be given to a client, or "".
func (b *Block) inUse(kernel string) string {
	names := append([]string{kernel}, b.partitions(kernel)...)
	devs := map[string]string{}
	for _, n := range names {
		if d := readTrim(filepath.Join(b.classDir(n), "dev")); d != "" {
			devs[d] = n
		}
		if holders, _ := os.ReadDir(filepath.Join(b.classDir(n), "holders")); len(holders) > 0 {
			return n + " is used by " + holders[0].Name() + " (LVM, RAID or device-mapper)"
		}
	}
	if raw, err := os.ReadFile(filepath.Join(b.proc, "self", "mountinfo")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if len(f) > 4 {
				if n, ok := devs[f[2]]; ok {
					return n + " is mounted at " + f[4]
				}
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(b.proc, "swaps")); err == nil {
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			if f := strings.Fields(line); len(f) > 0 {
				for _, n := range names {
					if f[0] == "/dev/"+n {
						return n + " is used as swap"
					}
				}
			}
		}
	}
	return ""
}

// --- applying ---

func (b *Block) stateFile() string { return filepath.Join(b.dir, "disks.json") }

// ReservedFolders are the image folders: no share may expose an image a
// client is writing to.
func (b *Block) ReservedFolders() []string {
	disks, _ := b.load()
	var out []string
	for _, d := range disks {
		if d.ImageFolder != "" {
			out = append(out, d.ImageFolder)
		}
	}
	return out
}

func (b *Block) load() ([]sys.BlockDisk, error) {
	raw, err := os.ReadFile(b.stateFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var disks []sys.BlockDisk
	return disks, json.Unmarshal(raw, &disks)
}

func (b *Block) save(disks []sys.BlockDisk) error {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	if disks == nil {
		disks = []sys.BlockDisk{}
	}
	raw, _ := json.Marshal(disks) // plain data: cannot fail
	return writeAtomicMode(b.stateFile(), string(raw), 0o600)
}

// strictPath: values written into LIO's control file are split on commas
// and "=", so an image path may only hold plainly safe characters.
var strictPath = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)

// checkBacking applies the folder and device rules on the root side.
func (b *Block) checkBacking(d sys.BlockDisk) error {
	if d.ImageFolder != "" {
		if !strictPath.MatchString(d.ImageFolder) || strings.Contains(d.ImageFolder, "/..") {
			return fmt.Errorf("%w: use only letters, digits, . _ - and / in the image folder", sys.ErrInvalid)
		}
		paths := []string{d.ImageFolder}
		if real, err := filepath.EvalSymlinks(d.ImageFolder); err == nil {
			paths = append(paths, filepath.ToSlash(real))
		}
		shares, err := b.samba.applied()
		if err != nil {
			return err
		}
		for _, p := range paths {
			if !pathAllowed(p, append(b.protected, b.dir)) {
				return fmt.Errorf("%w: %s is a protected or system folder", sys.ErrInvalid, d.ImageFolder)
			}
			for _, sh := range shares {
				if sys.Overlaps(p, sh.Path) {
					return fmt.Errorf("%w: the image folder overlaps share %q (a disk image must not be reachable as files)", sys.ErrInvalid, sh.Name)
				}
			}
		}
		return nil
	}
	kernel, err := b.kernelName(d.Device)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", sys.ErrInvalid, d.Device, err)
	}
	if b.isPartition(kernel) {
		return fmt.Errorf("%w: %s is a partition; give a whole disk", sys.ErrInvalid, d.Device)
	}
	if why := b.inUse(kernel); why != "" {
		return fmt.Errorf("%w: %s", sys.ErrInvalid, why)
	}
	return nil
}

func (b *Block) ApplyDisks(ctx context.Context, disks []sys.BlockDisk) error {
	if err := sys.CheckDisks(disks); err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	// Two by-id links can name the same disk: compare the disks themselves.
	kernels := map[string]string{}
	for _, d := range disks {
		if d.Device == "" {
			continue
		}
		k, err := b.kernelName(d.Device)
		if err != nil {
			return &sys.DiskError{ID: d.ID, Err: fmt.Errorf("%w: %s: %v", sys.ErrInvalid, d.Device, err)}
		}
		if other, ok := kernels[k]; ok {
			return fmt.Errorf("%w: disks %q and %q are the same disk (%s)", sys.ErrInvalid, other, d.ID, k)
		}
		kernels[k] = d.ID
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(disks) > 0 {
		if reason := b.unavailable(); reason != "" {
			return fmt.Errorf("%w: %s", sys.ErrNotFound, reason)
		}
	}
	previous, err := b.load()
	if err != nil {
		return fmt.Errorf("read the applied disks: %w", err)
	}
	prev := map[string]sys.BlockDisk{}
	for _, d := range previous {
		prev[d.ID] = d
	}
	want := map[string]bool{}
	for _, d := range disks {
		want[d.ID] = true
		p, ok := prev[d.ID]
		if ok && sys.SameDisk(p, d) && b.exists(d) {
			continue
		}
		// A disk already served with the same backing store passes the
		// "in use" check by being ours: tear it down first.
		if ok {
			b.teardown(ctx, p)
		}
		if d.Device != "" || d.ImageFolder != "" {
			if err := b.checkBacking(d); err != nil {
				_ = b.save(b.keep(previous, d.ID, want, disks))
				return &sys.DiskError{ID: d.ID, Err: err}
			}
		}
		if d.Protocol == sys.ProtocolISCSI && !b.iscsiReady() || d.Protocol == sys.ProtocolNVMeOF && !b.nvmeReady() {
			_ = b.save(b.keep(previous, d.ID, want, disks))
			return &sys.DiskError{ID: d.ID, Err: fmt.Errorf("%w: the %s target is not loaded", sys.ErrNotFound, d.Protocol)}
		}
		if err := b.setup(ctx, d); err != nil {
			b.teardown(ctx, d)
			_ = b.save(b.keep(previous, d.ID, want, disks))
			return &sys.DiskError{ID: d.ID, Err: err}
		}
		prev[d.ID] = d
	}
	for id, p := range prev {
		if !want[id] {
			b.teardown(ctx, p)
		}
	}
	b.cleanPorts()
	return b.save(disks)
}

// keep is the record after a failed apply: the disks set up so far (the
// new list up to the failed one, plus the previous ones still served).
func (b *Block) keep(previous []sys.BlockDisk, failed string, want map[string]bool, disks []sys.BlockDisk) []sys.BlockDisk {
	var out []sys.BlockDisk
	for _, d := range disks {
		if d.ID == failed {
			break
		}
		out = append(out, d)
	}
	seen := map[string]bool{}
	for _, d := range out {
		seen[d.ID] = true
	}
	for _, p := range previous {
		if !seen[p.ID] && p.ID != failed && b.exists(p) {
			out = append(out, p)
		}
	}
	return out
}

// exists reports whether a disk's kernel objects are there (after a reboot
// configfs is empty, whatever the record says).
func (b *Block) exists(d sys.BlockDisk) bool {
	var p string
	if d.Protocol == sys.ProtocolNVMeOF {
		p = filepath.Join(b.root, "nvmet", "subsystems", d.TargetName(), "namespaces", "1")
	} else {
		p = filepath.Join(b.iscsiTarget(d), "tpgt_1", "acls", d.Client)
	}
	_, err := os.Stat(p)
	return err == nil
}

func (b *Block) iscsiTarget(d sys.BlockDisk) string {
	return filepath.Join(b.root, "target", "iscsi", d.TargetName())
}

func (b *Block) backstore(d sys.BlockDisk) string {
	hba := fileioHBA
	if d.Device != "" {
		hba = iblockHBA
	}
	return filepath.Join(b.root, "target", "core", hba, "nsh-"+d.ID)
}

func set(path, value string) error {
	if err := os.WriteFile(path, []byte(value), 0); err != nil {
		return fmt.Errorf("set %s: %w", filepath.Base(path), err)
	}
	return nil
}

func mkdir(path string) error {
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

func link(target, at string) error {
	if err := os.Symlink(target, at); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("link %s: %w", at, err)
	}
	return nil
}

// image creates or grows (never shrinks) a disk's sparse image file.
func image(d sys.BlockDisk) (string, int64, error) {
	p := d.ImagePath()
	size := int64(d.SizeGiB) << 30
	if err := os.MkdirAll(d.ImageFolder, 0o700); err != nil {
		return "", 0, err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("open image %s: %w", p, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%w: %s is not a regular file", sys.ErrInvalid, p)
	}
	if info.Size() > size {
		size = info.Size() // never shrink: the client's data is there
	} else if err := f.Truncate(size); err != nil {
		return "", 0, fmt.Errorf("size image %s: %w", p, err)
	}
	return p, size, nil
}

func (b *Block) setup(ctx context.Context, d sys.BlockDisk) error {
	var backing string
	var size int64
	if d.ImageFolder != "" {
		var err error
		if backing, size, err = image(d); err != nil {
			return err
		}
	} else {
		kernel, err := b.kernelName(d.Device)
		if err != nil {
			return err
		}
		backing = "/dev/" + kernel
	}
	slog.InfoContext(ctx, "block disk: setting up", "disk", d.ID, "protocol", d.Protocol, "backing", backing, "client", d.Client,
		"listen", d.Listen, "port", d.Port, "chap", d.CHAPUser != "", "read_only", d.ReadOnly)
	if d.Protocol == sys.ProtocolNVMeOF {
		return b.setupNVMe(d, backing)
	}
	return b.setupISCSI(d, backing, size)
}

func (b *Block) setupISCSI(d sys.BlockDisk, backing string, size int64) error {
	so := b.backstore(d)
	if err := mkdir(filepath.Dir(so)); err != nil {
		return err
	}
	if err := mkdir(so); err != nil {
		return err
	}
	control := "udev_path=" + backing
	if d.ImageFolder != "" {
		control = "fd_dev_name=" + backing + ",fd_dev_size=" + strconv.FormatInt(size, 10)
	}
	if err := set(filepath.Join(so, "control"), control); err != nil {
		return err
	}
	if err := set(filepath.Join(so, "enable"), "1"); err != nil {
		return err
	}

	t := b.iscsiTarget(d)
	tpg := filepath.Join(t, "tpgt_1")
	lun := filepath.Join(tpg, "lun", "lun_0")
	for _, dir := range []string{filepath.Dir(t), t, tpg, lun} {
		if err := mkdir(dir); err != nil {
			return err
		}
	}
	if err := link(so, filepath.Join(lun, "nsh")); err != nil {
		return err
	}
	// Only the named initiator: no generated ACLs, no demo mode.
	for k, v := range map[string]string{"generate_node_acls": "0", "demo_mode_write_protect": "1", "cache_dynamic_acls": "0",
		"authentication": map[bool]string{true: "1", false: "0"}[d.CHAPUser != ""]} {
		if err := set(filepath.Join(tpg, "attrib", k), v); err != nil {
			return err
		}
	}
	addr := d.Listen
	if a := netip.MustParseAddr(d.Listen); a.Is6() {
		addr = "[" + d.Listen + "]"
	}
	if err := mkdir(filepath.Join(tpg, "np", addr+":"+strconv.Itoa(d.Port))); err != nil {
		return err
	}
	acl := filepath.Join(tpg, "acls", d.Client)
	if err := mkdir(acl); err != nil {
		return err
	}
	if err := mkdir(filepath.Join(acl, "lun_0")); err != nil {
		return err
	}
	if err := link(lun, filepath.Join(acl, "lun_0", "nsh")); err != nil {
		return err
	}
	if err := set(filepath.Join(acl, "lun_0", "write_protect"), map[bool]string{true: "1", false: "0"}[d.ReadOnly]); err != nil {
		return err
	}
	if d.CHAPUser != "" {
		if err := set(filepath.Join(acl, "auth", "userid"), d.CHAPUser); err != nil {
			return err
		}
		if err := set(filepath.Join(acl, "auth", "password"), d.CHAPSecret); err != nil {
			return err
		}
	}
	return set(filepath.Join(tpg, "enable"), "1")
}

func (b *Block) nvmet(parts ...string) string {
	return filepath.Join(append([]string{b.root, "nvmet"}, parts...)...)
}

func (b *Block) setupNVMe(d sys.BlockDisk, backing string) error {
	sub := b.nvmet("subsystems", d.TargetName())
	if err := mkdir(sub); err != nil {
		return err
	}
	if err := set(filepath.Join(sub, "attr_allow_any_host"), "0"); err != nil {
		return err
	}
	ns := filepath.Join(sub, "namespaces", "1")
	if err := mkdir(ns); err != nil {
		return err
	}
	if err := set(filepath.Join(ns, "device_path"), backing); err != nil {
		return err
	}
	if err := set(filepath.Join(ns, "enable"), "1"); err != nil {
		return err
	}
	host := b.nvmet("hosts", d.Client)
	if err := mkdir(host); err != nil {
		return err
	}
	if err := link(host, filepath.Join(sub, "allowed_hosts", d.Client)); err != nil {
		return err
	}
	port, err := b.port(d)
	if err != nil {
		return err
	}
	return link(sub, filepath.Join(port, "subsystems", d.TargetName()))
}

// port finds or creates NetShare Hub's nvmet port for an address.
func (b *Block) port(d sys.BlockDisk) (string, error) {
	fam := "ipv4"
	if netip.MustParseAddr(d.Listen).Is6() {
		fam = "ipv6"
	}
	free := ""
	for id := firstPort; id <= lastPort; id++ {
		p := b.nvmet("ports", strconv.Itoa(id))
		if _, err := os.Stat(p); err != nil {
			if free == "" {
				free = p
			}
			continue
		}
		if readTrim(filepath.Join(p, "addr_traddr")) == d.Listen && readTrim(filepath.Join(p, "addr_trsvcid")) == strconv.Itoa(d.Port) &&
			readTrim(filepath.Join(p, "addr_trtype")) == "tcp" {
			return p, nil
		}
	}
	if free == "" {
		return "", errors.New("no free NVMe port id")
	}
	if err := mkdir(free); err != nil {
		return "", err
	}
	for _, kv := range [][2]string{{"addr_trtype", "tcp"}, {"addr_adrfam", fam}, {"addr_traddr", d.Listen},
		{"addr_trsvcid", strconv.Itoa(d.Port)}} {
		if err := set(filepath.Join(free, kv[0]), kv[1]); err != nil {
			_ = os.Remove(free)
			return "", err
		}
	}
	return free, nil
}

// cleanPorts removes NetShare Hub's nvmet ports nothing uses any more.
func (b *Block) cleanPorts() {
	for id := firstPort; id <= lastPort; id++ {
		p := b.nvmet("ports", strconv.Itoa(id))
		if entries, err := os.ReadDir(filepath.Join(p, "subsystems")); err == nil && len(entries) == 0 {
			_ = os.Remove(p)
		}
	}
}

// teardown removes a disk's kernel objects (whatever exists of them), in
// the order the kernel needs. The client is disconnected.
func (b *Block) teardown(ctx context.Context, d sys.BlockDisk) {
	rm := func(p string) { _ = os.Remove(p) } // links and directories; missing is fine
	if d.Protocol == sys.ProtocolNVMeOF {
		sub := b.nvmet("subsystems", d.TargetName())
		for id := firstPort; id <= lastPort; id++ {
			rm(b.nvmet("ports", strconv.Itoa(id), "subsystems", d.TargetName()))
		}
		rm(filepath.Join(sub, "allowed_hosts", d.Client))
		_ = set(filepath.Join(sub, "namespaces", "1", "enable"), "0")
		rm(filepath.Join(sub, "namespaces", "1"))
		rm(sub)
		// The host entry goes when no subsystem allows it any more.
		if b.hostUnused(d.Client) {
			rm(b.nvmet("hosts", d.Client))
		}
		b.cleanPorts()
	} else {
		t := b.iscsiTarget(d)
		tpg := filepath.Join(t, "tpgt_1")
		_ = set(filepath.Join(tpg, "enable"), "0")
		if acls, err := os.ReadDir(filepath.Join(tpg, "acls")); err == nil {
			for _, a := range acls {
				rm(filepath.Join(tpg, "acls", a.Name(), "lun_0", "nsh"))
				rm(filepath.Join(tpg, "acls", a.Name(), "lun_0"))
				rm(filepath.Join(tpg, "acls", a.Name()))
			}
		}
		rm(filepath.Join(tpg, "lun", "lun_0", "nsh"))
		rm(filepath.Join(tpg, "lun", "lun_0"))
		if nps, err := os.ReadDir(filepath.Join(tpg, "np")); err == nil {
			for _, np := range nps {
				rm(filepath.Join(tpg, "np", np.Name()))
			}
		}
		rm(tpg)
		rm(t)
		rm(b.backstore(d))
	}
	slog.InfoContext(ctx, "block disk: removed", "disk", d.ID, "protocol", d.Protocol)
}

func (b *Block) hostUnused(nqn string) bool {
	subs, _ := os.ReadDir(b.nvmet("subsystems"))
	for _, s := range subs {
		if _, err := os.Lstat(b.nvmet("subsystems", s.Name(), "allowed_hosts", nqn)); err == nil {
			return false
		}
	}
	return true
}
