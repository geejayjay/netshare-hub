package sys

import (
	"context"
	"fmt"
	"net/netip"
	"path"
	"regexp"
)

// Block storage (step 5.1): a disk for ONE computer, over iSCSI (the
// kernel's LIO target) or NVMe over TCP (the kernel's nvmet target). The
// client formats it (NTFS, APFS, ext4...) and uses it like a local disk.
//
// Two computers using one disk at once corrupt it, so every disk names
// exactly one client: an iSCSI initiator name (IQN) or an NVMe host name
// (NQN). The kernel target refuses every other client (iSCSI: no
// generated ACLs, no demo mode; NVMe: allow_any_host off). For iSCSI a
// CHAP secret can be required as well, so a copied initiator name is not
// enough. NVMe over TCP has no such secret here: its host name can be
// copied by anyone who learns it, so NVMe disks belong on a trusted network.
//
// The backing store is either an image file the root side creates (sparse)
// in a folder that is not a share, or a whole disk named by its stable
// /dev/disk/by-id link, which must not be mounted, be swap, hold a mounted
// partition or belong to LVM, RAID or device-mapper.
//
// NetShare Hub only touches the kernel objects it created (their names
// start with its prefixes), so targets an admin set up by hand are left
// alone.
//
// The kernel targets need real root: they cannot run in an unprivileged
// container (Proxmox's default LXC). Run NetShare Hub on the Proxmox host
// or in a VM for block storage.

// Disk protocols.
const (
	ProtocolISCSI  = "iscsi"
	ProtocolNVMeOF = "nvmeof"
)

// Prefixes of the kernel objects NetShare Hub owns.
const (
	IQNPrefix = "iqn.2025-01.io.netshare-hub:"
	NQNPrefix = "nqn.2025-01.io.netshare-hub:"
)

var (
	iqnRe     = regexp.MustCompile(`^iqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9][a-z0-9.-]*(:[A-Za-z0-9.:_-]+)?$`)
	euiRe     = regexp.MustCompile(`^eui\.[0-9A-Fa-f]{16}$`)
	nqnRe     = regexp.MustCompile(`^nqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9][a-z0-9.-]*:[A-Za-z0-9.:_-]+$`)
	chapUser  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	chapPass  = regexp.MustCompile(`^[A-Za-z0-9!#$%&()*+,./:;<=>?@\[\]^_{|}~-]{12,16}$`)
	byIDPath  = regexp.MustCompile(`^/dev/disk/by-id/[A-Za-z0-9._:-]+$`)
	partition = regexp.MustCompile(`-part[0-9]+$`)
)

// ValidIQN reports whether s is an iSCSI name (iqn. or eui. form).
func ValidIQN(s string) bool { return len(s) <= 223 && (iqnRe.MatchString(s) || euiRe.MatchString(s)) }

// ValidNQN reports whether s is an NVMe qualified name.
func ValidNQN(s string) bool { return len(s) <= 223 && nqnRe.MatchString(s) }

// ValidCHAPUser and ValidCHAPSecret check iSCSI CHAP credentials. Windows
// accepts secrets of 12 to 16 characters only.
func ValidCHAPUser(s string) bool   { return chapUser.MatchString(s) }
func ValidCHAPSecret(s string) bool { return chapPass.MatchString(s) }

// ValidDiskDevice reports whether p names a whole disk by its stable id
// (not a partition, not /dev/sdX, which can change between boots).
func ValidDiskDevice(p string) bool {
	return byIDPath.MatchString(p) && !partition.MatchString(p) && path.Clean(p) == p
}

// MaxDiskGiB bounds an image's size (64 TiB).
const MaxDiskGiB = 65536

// BlockDisk is one disk for one client.
type BlockDisk struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	// Exactly one backing store: an image file (ImageFolder/<ID>.img, made
	// SizeGiB big if it does not exist; never shrunk), or a whole disk.
	ImageFolder string `json:"imageFolder,omitempty"`
	SizeGiB     int    `json:"sizeGiB,omitempty"`
	Device      string `json:"device,omitempty"`
	// Client is the one initiator (IQN) or host (NQN) that may connect.
	Client string `json:"client"`
	// CHAP (iSCSI only): the client must prove it knows the secret. The
	// secret is never logged.
	CHAPUser   string `json:"chapUser,omitempty"`
	CHAPSecret string `json:"chapSecret,omitempty"`
	// Listen is the address and port the target listens on.
	Listen   string `json:"listen"`
	Port     int    `json:"port"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// ImagePath is the image file of an image-backed disk.
func (d BlockDisk) ImagePath() string {
	if d.ImageFolder == "" {
		return ""
	}
	return d.ImageFolder + "/" + d.ID + ".img"
}

// TargetName is the disk's iSCSI target IQN or NVMe subsystem NQN.
func (d BlockDisk) TargetName() string {
	if d.Protocol == ProtocolNVMeOF {
		return NQNPrefix + d.ID
	}
	return IQNPrefix + d.ID
}

// Check validates a disk (the folder and device rules are the root side's).
func (d BlockDisk) Check() error {
	if !ValidPeerID(d.ID) {
		return fmt.Errorf("unsafe disk id %q", d.ID)
	}
	switch d.Protocol {
	case ProtocolISCSI:
		if !ValidIQN(d.Client) {
			return fmt.Errorf("disk %q: invalid initiator name", d.ID)
		}
		if (d.CHAPUser == "") != (d.CHAPSecret == "") {
			return fmt.Errorf("disk %q: CHAP needs a user and a secret", d.ID)
		}
		if d.CHAPUser != "" && (!ValidCHAPUser(d.CHAPUser) || !ValidCHAPSecret(d.CHAPSecret)) {
			return fmt.Errorf("disk %q: invalid CHAP credentials", d.ID)
		}
	case ProtocolNVMeOF:
		if !ValidNQN(d.Client) {
			return fmt.Errorf("disk %q: invalid host name", d.ID)
		}
		if d.CHAPUser != "" || d.CHAPSecret != "" {
			return fmt.Errorf("disk %q: CHAP is for iSCSI disks", d.ID)
		}
		if d.ReadOnly {
			return fmt.Errorf("disk %q: read-only is for iSCSI disks", d.ID)
		}
	default:
		return fmt.Errorf("disk %q: unknown protocol %q", d.ID, d.Protocol)
	}
	switch {
	case d.ImageFolder != "" && d.Device != "":
		return fmt.Errorf("disk %q: one backing store", d.ID)
	case d.ImageFolder != "":
		if !ValidSharePath(d.ImageFolder) || !SharablePath(d.ImageFolder) {
			return fmt.Errorf("disk %q: unsafe image folder", d.ID)
		}
		if d.SizeGiB < 1 || d.SizeGiB > MaxDiskGiB {
			return fmt.Errorf("disk %q: invalid size", d.ID)
		}
	case d.Device != "":
		if !ValidDiskDevice(d.Device) {
			return fmt.Errorf("disk %q: name a whole disk by /dev/disk/by-id", d.ID)
		}
	default:
		return fmt.Errorf("disk %q: no backing store", d.ID)
	}
	a, err := netip.ParseAddr(d.Listen)
	if err != nil || a.Zone() != "" || a.String() != d.Listen {
		return fmt.Errorf("disk %q: invalid listen address", d.ID)
	}
	if d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("disk %q: invalid port", d.ID)
	}
	return nil
}

// CheckDisks validates a whole list: unique ids, one disk per backing
// store, and no client on two disks (it would see both as one).
func CheckDisks(disks []BlockDisk) error {
	ids, backing := map[string]bool{}, map[string]string{}
	for _, d := range disks {
		if err := d.Check(); err != nil {
			return err
		}
		if ids[d.ID] {
			return fmt.Errorf("disk %q listed twice", d.ID)
		}
		ids[d.ID] = true
		b := d.Device
		if b == "" {
			b = d.ImagePath()
		}
		if other, ok := backing[b]; ok {
			return fmt.Errorf("disks %q and %q use the same backing store", other, d.ID)
		}
		backing[b] = d.ID
	}
	return nil
}

// DiskDevice is a whole disk on this machine, for the "dedicated disk"
// wizard.
type DiskDevice struct {
	// Path is the stable /dev/disk/by-id link; Kernel the current name.
	Path      string `json:"path"`
	Kernel    string `json:"kernel"`
	SizeBytes int64  `json:"sizeBytes"`
	Model     string `json:"model,omitempty"`
	// InUse says why the disk cannot be given away ("" when it can).
	InUse string `json:"inUse,omitempty"`
	// Partitions is how many partitions it has (they would be lost to the
	// client: it formats the whole disk).
	Partitions int `json:"partitions"`
}

// DiskState is a disk as the kernel serves it now.
type DiskState struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	// Connected: the client has a session now (iSCSI only; NVMe does not
	// report it).
	Connected *bool `json:"connected,omitempty"`
}

// BlockStatus says what this machine can serve.
type BlockStatus struct {
	// ISCSI and NVMeOF: the kernel target is loaded and configurable.
	ISCSI  bool `json:"iscsi"`
	NVMeOF bool `json:"nvmeof"`
	// Reason explains what is missing (modules, an unprivileged
	// container, a read-only configfs).
	Reason string      `json:"reason,omitempty"`
	Disks  []DiskState `json:"disks"`
}

// Block manages the disks NetShare Hub serves.
type Block interface {
	BlockStatus(ctx context.Context) (BlockStatus, error)
	// Devices lists the whole disks on this machine (by-id), with whether
	// each is in use.
	Devices(ctx context.Context) ([]DiskDevice, error)
	// ApplyDisks makes the kernel targets serve exactly these disks. Disks
	// whose settings changed are torn down and set up again (their client
	// is disconnected). If a disk cannot be set up, its half-made objects
	// are removed and the error says which disk; disks set up before it
	// stay. Errors: ErrInvalid, ErrNotFound (target not available),
	// *DiskError.
	ApplyDisks(ctx context.Context, disks []BlockDisk) error
}

// DiskError says which disk could not be set up.
type DiskError struct {
	ID  string
	Err error
}

func (e *DiskError) Error() string { return "disk " + e.ID + ": " + e.Err.Error() }
func (e *DiskError) Unwrap() error { return e.Err }

// SameDisk reports whether two disks need no change in the kernel.
func SameDisk(a, b BlockDisk) bool { return a == b }
