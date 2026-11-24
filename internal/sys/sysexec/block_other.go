//go:build !linux

package sysexec

import (
	"context"
	"errors"

	"netshare-hub/internal/sys"
)

// DefaultBlockDir: see block_linux.go.
const DefaultBlockDir = "/var/lib/netshare-hub-agent/block"

// Block needs Linux's kernel targets.
type Block struct{}

func NewBlock(*Samba, string, ...string) *Block { return &Block{} }

func (*Block) BlockStatus(context.Context) (sys.BlockStatus, error) {
	return sys.BlockStatus{Reason: "Block storage needs Linux.", Disks: []sys.DiskState{}}, nil
}

func (*Block) ReservedFolders() []string { return nil }

func (*Block) Devices(context.Context) ([]sys.DiskDevice, error) { return []sys.DiskDevice{}, nil }

func (*Block) ApplyDisks(_ context.Context, disks []sys.BlockDisk) error {
	if len(disks) == 0 {
		return nil
	}
	return errors.Join(sys.ErrNotFound, errors.New("block storage needs Linux"))
}
