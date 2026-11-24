package app_test

import (
	"strings"
	"testing"

	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys"
)

// Step 5.1: block storage.

func blockModules() config.Modules {
	m := config.Default().Modules
	m.BlockStorage = true
	return m
}

const winIQN = "iqn.1991-05.com.microsoft:desktop-jay"

func TestBlockDiskImage(t *testing.T) {
	admin, fake, _ := moduleServer(t, blockModules())
	var d struct {
		ID       int64
		Protocol string
		Target   string
		Port     int
		Listen   string
		CHAP     bool
		Client   string
	}
	status, raw := admin.do("POST", "/block/disks", map[string]any{"name": "games", "protocol": "iscsi", "imageFolder": "/srv/disks",
		"sizeGiB": 200, "client": winIQN, "chapUser": "jay", "chapSecret": "s3cret-pass12", "confirmSingleClient": true}, &d)
	want(t, "create", status, 201)
	_ = raw
	if d.Protocol != "iscsi" || d.Port != 3260 || d.Listen != "0.0.0.0" || !d.CHAP || !strings.HasPrefix(d.Target, sys.IQNPrefix) {
		t.Errorf("disk: %+v", d)
	}
	applied := fake.Block().Disks()
	if len(applied) != 1 || applied[0].Client != winIQN || applied[0].CHAPSecret != "s3cret-pass12" || applied[0].ImagePath() != "/srv/disks/d1.img" {
		t.Fatalf("applied: %+v", applied)
	}

	// The secret is never sent back.
	var list map[string]any
	admin.do("GET", "/block/disks/"+itoa(d.ID), nil, &list)
	for k, v := range list {
		if s, ok := v.(string); ok && strings.Contains(s, "s3cret") {
			t.Errorf("the CHAP secret is in %q", k)
		}
	}

	// Grow, never shrink.
	var p problem
	status, _ = admin.do("PATCH", "/block/disks/"+itoa(d.ID), map[string]any{"sizeGiB": 100}, &p)
	want(t, "shrink", status, 422)
	want(t, "field", fieldCodes(p)["sizeGiB"], "no_shrink")
	status, _ = admin.do("PATCH", "/block/disks/"+itoa(d.ID), map[string]any{"sizeGiB": 300, "readOnly": true}, nil)
	want(t, "grow", status, 200)
	if a := fake.Block().Disks(); a[0].SizeGiB != 300 || !a[0].ReadOnly {
		t.Errorf("after grow: %+v", a)
	}
	// CHAP off.
	status, _ = admin.do("PATCH", "/block/disks/"+itoa(d.ID), map[string]any{"chapUser": "", "chapSecret": ""}, nil)
	want(t, "chap off", status, 200)
	if a := fake.Block().Disks(); a[0].CHAPUser != "" || a[0].CHAPSecret != "" {
		t.Errorf("CHAP still on: %+v", a)
	}

	status, _ = admin.do("DELETE", "/block/disks/"+itoa(d.ID), nil, nil)
	want(t, "delete", status, 204)
	if len(fake.Block().Disks()) != 0 {
		t.Error("still served after delete")
	}
}

func TestBlockSafeguards(t *testing.T) {
	admin, fake, _ := moduleServer(t, blockModules())
	jay := createUser(t, admin, "jay")
	admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media", "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, nil)

	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": "d", "protocol": "iscsi", "imageFolder": "/srv/disks", "sizeGiB": 10, "client": winIQN,
			"confirmSingleClient": true}
		for k, v := range extra {
			if v == nil {
				delete(b, k)
			} else {
				b[k] = v
			}
		}
		return b
	}
	cases := []struct {
		name, field, code string
		body              map[string]any
	}{
		{"not confirmed", "confirmSingleClient", "required", base(map[string]any{"confirmSingleClient": false})},
		{"image in a share", "imageFolder", "overlaps_share", base(map[string]any{"imageFolder": "/srv/media/disks"})},
		{"image folder holds a share", "imageFolder", "overlaps_share", base(map[string]any{"imageFolder": "/srv"})},
		{"system folder", "imageFolder", "invalid_format", base(map[string]any{"imageFolder": "/var/disks"})},
		{"comma in folder", "imageFolder", "invalid_format", base(map[string]any{"imageFolder": "/srv/a,fd_dev_name=/dev/sda"})},
		{"bad initiator", "client", "invalid_format", base(map[string]any{"client": "desktop-jay"})},
		{"short CHAP secret", "chapSecret", "invalid_format", base(map[string]any{"chapUser": "jay", "chapSecret": "short"})},
		{"disk in use", "device", "in_use", base(map[string]any{"imageFolder": nil, "sizeGiB": nil, "device": "/dev/disk/by-id/ata-FAKE_ROOT",
			"confirmDevice": "/dev/disk/by-id/ata-FAKE_ROOT"})},
		{"partition", "device", "invalid_format", base(map[string]any{"imageFolder": nil, "sizeGiB": nil, "device": "/dev/disk/by-id/ata-FAKE_SSD_1-part1",
			"confirmDevice": "/dev/disk/by-id/ata-FAKE_SSD_1-part1"})},
		{"kernel name", "device", "invalid_format", base(map[string]any{"imageFolder": nil, "sizeGiB": nil, "device": "/dev/sdb", "confirmDevice": "/dev/sdb"})},
		{"device not confirmed", "confirmDevice", "mismatch", base(map[string]any{"imageFolder": nil, "sizeGiB": nil, "device": "/dev/disk/by-id/ata-FAKE_SSD_1"})},
		{"both backings", "device", "one_backing", base(map[string]any{"device": "/dev/disk/by-id/ata-FAKE_SSD_1", "confirmDevice": "/dev/disk/by-id/ata-FAKE_SSD_1"})},
		{"nvme with chap", "chapUser", "iscsi_only", base(map[string]any{"protocol": "nvmeof", "client": "nqn.2014-08.org.nvmexpress:uuid:1b4e28ba-2fa1-11d2-883f-0016d3cca427",
			"chapUser": "jay", "chapSecret": "s3cret-pass12"})},
		{"nvme with an iqn", "client", "invalid_format", base(map[string]any{"protocol": "nvmeof"})},
	}
	for _, c := range cases {
		var p problem
		status, _ := admin.do("POST", "/block/disks", c.body, &p)
		if status != 422 || fieldCodes(p)[c.field] != c.code {
			t.Errorf("%s: %d %+v", c.name, status, p.Errors)
		}
	}
	if len(fake.Block().Disks()) != 0 {
		t.Fatal("a refused disk was served")
	}

	// A whole disk, confirmed, over NVMe/TCP.
	var d struct {
		Port   int
		Target string
	}
	status, _ := admin.do("POST", "/block/disks", map[string]any{"name": "lab", "protocol": "nvmeof", "device": "/dev/disk/by-id/ata-FAKE_SSD_1",
		"confirmDevice": "/dev/disk/by-id/ata-FAKE_SSD_1", "client": "nqn.2014-08.org.nvmexpress:uuid:1b4e28ba-2fa1-11d2-883f-0016d3cca427",
		"confirmSingleClient": true}, &d)
	want(t, "whole disk", status, 201)
	if d.Port != 4420 || !strings.HasPrefix(d.Target, sys.NQNPrefix) {
		t.Errorf("disk: %+v", d)
	}
	// The same disk twice is refused (by the system layer).
	var p problem
	status, _ = admin.do("POST", "/block/disks", map[string]any{"name": "lab2", "protocol": "iscsi", "device": "/dev/disk/by-id/ata-FAKE_SSD_1",
		"confirmDevice": "/dev/disk/by-id/ata-FAKE_SSD_1", "client": winIQN, "confirmSingleClient": true}, &p)
	want(t, "same disk twice", status, 422)
	want(t, "code", p.Code, "BLOCK_DISK_REFUSED")
	if len(fake.Block().Disks()) != 1 {
		t.Error("the refused disk changed what is served")
	}

	// A machine where the targets cannot run.
	fake.Block().Status = sys.BlockStatus{Reason: "This machine is an unprivileged container."}
	status, _ = admin.do("POST", "/block/disks", base(map[string]any{"name": "x"}), &p)
	want(t, "no target", status, 409)
	want(t, "code", p.Code, "BLOCK_UNAVAILABLE")
	var st struct {
		ISCSI bool
		Hint  string
	}
	admin.do("GET", "/block/status", nil, &st)
	if st.ISCSI || !strings.Contains(st.Hint, "unprivileged") {
		t.Errorf("status: %+v", st)
	}
}

// A share can never expose disk images or a receiving folder, even when
// it is created after them.
func TestReservedFoldersCannotBeShared(t *testing.T) {
	mods := blockModules()
	mods.Replication = true
	admin, _, _ := moduleServer(t, mods)
	jay := createUser(t, admin, "jay")
	status, _ := admin.do("POST", "/block/disks", map[string]any{"name": "games", "protocol": "iscsi", "imageFolder": "/srv/disks",
		"sizeGiB": 10, "client": winIQN, "confirmSingleClient": true}, nil)
	want(t, "disk", status, 201)
	for _, path := range []string{"/srv/disks", "/srv/disks/inside", "/srv"} {
		var p problem
		status, _ := admin.do("POST", "/shares", map[string]any{"name": "x", "path": path, "createDirectory": true,
			"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &p)
		if status != 422 || p.Code != "SHARE_FOLDER_RESERVED" {
			t.Errorf("share %s: %d %s", path, status, p.Code)
		}
	}
}
