package sysfake

import (
	"context"
	"fmt"
	"sync"

	"netshare-hub/internal/sys"
)

// Block is the fake sys.Block: it checks disks like the real one (rules,
// one backing store each, not inside a share, devices not in use) and
// keeps the applied list.
type Block struct {
	fake *Fake
	mu   sync.Mutex
	// Status is what BlockStatus reports (Disks is filled in).
	Status sys.BlockStatus
	// DeviceList is the pretend machine's whole disks.
	DeviceList []sys.DiskDevice
	disks      []sys.BlockDisk
	// Applied counts successful ApplyDisks calls.
	Applied int
	Fail    error
}

// Block returns the fake's block storage part.
func (f *Fake) Block() *Block {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.block == nil {
		f.block = &Block{fake: f, Status: sys.BlockStatus{ISCSI: true, NVMeOF: true},
			DeviceList: []sys.DiskDevice{
				{Path: "/dev/disk/by-id/ata-FAKE_SSD_1", Kernel: "sdb", SizeBytes: 500 << 30, Model: "Fake SSD"},
				{Path: "/dev/disk/by-id/ata-FAKE_ROOT", Kernel: "sda", SizeBytes: 64 << 30, Model: "Fake root", InUse: "sda1 is mounted at /", Partitions: 2},
			}}
	}
	return f.block
}

func (b *Block) BlockStatus(context.Context) (sys.BlockStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.Status
	st.Disks = []sys.DiskState{}
	for _, d := range b.disks {
		st.Disks = append(st.Disks, sys.DiskState{ID: d.ID, Protocol: d.Protocol})
	}
	return st, nil
}

func (b *Block) Devices(context.Context) ([]sys.DiskDevice, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]sys.DiskDevice{}, b.DeviceList...), nil
}

func (b *Block) ApplyDisks(_ context.Context, disks []sys.BlockDisk) error {
	if err := sys.CheckDisks(disks); err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	b.fake.mu.Lock()
	shares := append([]sys.ShareSpec(nil), b.fake.shares...)
	b.fake.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, d := range disks {
		if d.Protocol == sys.ProtocolISCSI && !b.Status.ISCSI || d.Protocol == sys.ProtocolNVMeOF && !b.Status.NVMeOF {
			return &sys.DiskError{ID: d.ID, Err: fmt.Errorf("%w: the %s target is not loaded", sys.ErrNotFound, d.Protocol)}
		}
		if d.ImageFolder != "" {
			for _, sh := range shares {
				if sys.Overlaps(d.ImageFolder, sh.Path) {
					return &sys.DiskError{ID: d.ID, Err: fmt.Errorf("%w: the image folder overlaps share %q", sys.ErrInvalid, sh.Name)}
				}
			}
		}
		if d.Device != "" {
			found := false
			for _, dev := range b.DeviceList {
				if dev.Path == d.Device {
					found = true
					if dev.InUse != "" {
						return &sys.DiskError{ID: d.ID, Err: fmt.Errorf("%w: %s", sys.ErrInvalid, dev.InUse)}
					}
				}
			}
			if !found {
				return &sys.DiskError{ID: d.ID, Err: fmt.Errorf("%w: %s: no such disk", sys.ErrInvalid, d.Device)}
			}
		}
	}
	if b.Fail != nil {
		err := b.Fail
		b.Fail = nil
		return err
	}
	b.disks = append([]sys.BlockDisk(nil), disks...)
	b.Applied++
	return nil
}

// Disks returns the applied disks.
func (b *Block) Disks() []sys.BlockDisk {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]sys.BlockDisk(nil), b.disks...)
}
