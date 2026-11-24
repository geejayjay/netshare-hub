package agent

import (
	"context"
	"net/http"
	"strings"

	"netshare-hub/internal/sys"
)

// Block storage operations (step 5.1).
const (
	opBlockStatus  = "block.status"
	opBlockDevices = "block.devices"
	opApplyDisks   = "block.apply-disks"
	codeDiskFailed = "disk_failed"
)

type applyDisksRequest struct {
	Disks []sys.BlockDisk `json:"disks"`
}

type devicesResponse struct {
	Devices []sys.DiskDevice `json:"devices"`
}

func registerBlock(s *Server, system sys.System) {
	if system.Block == nil {
		return
	}
	b := system.Block
	register(s, opBlockStatus, func(ctx context.Context, _ struct{}) (any, error) { return b.BlockStatus(ctx) }, nil)
	register(s, opBlockDevices, func(ctx context.Context, _ struct{}) (any, error) {
		d, err := b.Devices(ctx)
		return devicesResponse{Devices: d}, err
	}, nil)
	register(s, opApplyDisks, func(ctx context.Context, r applyDisksRequest) (any, error) {
		if len(r.Disks) > 1000 {
			return nil, sys.ErrInvalid
		}
		return nil, b.ApplyDisks(ctx, r.Disks)
	}, func(r applyDisksRequest) []any {
		// Never the CHAP secret.
		out := make([]string, len(r.Disks))
		for i, d := range r.Disks {
			out[i] = d.ID + "=" + d.Protocol + ":" + d.Client
		}
		return []any{"disks", out}
	})
}

// encodeDiskError keeps which disk failed across the socket.
func encodeDiskError(err *sys.DiskError) (int, errorResponse) {
	status, body := encodeError(err.Err)
	body.Share = err.ID // reused field: the disk's id
	body.Message = body.Code + ": " + body.Message
	body.Code = codeDiskFailed
	if status < 400 {
		status = http.StatusInternalServerError
	}
	return status, body
}

func decodeDiskError(res errorResponse) error {
	code, msg, _ := strings.Cut(res.Message, ": ")
	inner := decodeError(errorResponse{Code: code, Message: msg})
	return &sys.DiskError{ID: res.Share, Err: inner}
}

// --- client ---

func (c *Client) BlockStatus(ctx context.Context) (sys.BlockStatus, error) {
	var res sys.BlockStatus
	err := c.call(ctx, opBlockStatus, struct{}{}, &res)
	return res, err
}

func (c *Client) Devices(ctx context.Context) ([]sys.DiskDevice, error) {
	var res devicesResponse
	err := c.call(ctx, opBlockDevices, struct{}{}, &res)
	return res.Devices, err
}

func (c *Client) ApplyDisks(ctx context.Context, disks []sys.BlockDisk) error {
	if disks == nil {
		disks = []sys.BlockDisk{}
	}
	return c.call(ctx, opApplyDisks, applyDisksRequest{Disks: disks}, nil)
}
