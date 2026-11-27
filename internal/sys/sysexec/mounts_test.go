package sysexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"netshare-hub/internal/sys"
)

func TestParseMountInfoAndBestMatch(t *testing.T) {
	sample := strings.Join([]string{
		"25 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw",
		"42 25 0:40 / /srv/shares/media ro,relatime - zfs tank/media ro,xattr",
		"43 25 0:41 / /srv/shares/docs rw,relatime - zfs tank/docs rw,xattr",
		// A writable mount whose superblock is read-only still counts as ro.
		"44 25 0:42 / /srv/shares/backup rw,relatime - zfs tank/backup ro,xattr",
		// Escaped space in the mount point.
		"45 25 0:43 / /mnt/HDD\\040Pool rw,noatime shared:365 master:872 - zfs hdd-pool rw,xattr,noacl",
	}, "\n")

	mounts := parseMountInfo(strings.NewReader(sample))
	if len(mounts) != 5 {
		t.Fatalf("got %d mounts, want 5", len(mounts))
	}

	m1, ok := bestMountFor("/srv/shares/media/movies", mounts)
	if !ok || m1.MountPoint != "/srv/shares/media" || !m1.ReadOnly || m1.FSType != "zfs" {
		t.Fatalf("unexpected media mount: %+v ok=%v", m1, ok)
	}

	m2, ok := bestMountFor("/srv/local-root-folder", mounts)
	if !ok || m2.MountPoint != "/" || m2.ReadOnly {
		t.Fatalf("unexpected root mount: %+v ok=%v", m2, ok)
	}

	if m, _ := bestMountFor("/srv/shares/docs", mounts); m.ReadOnly {
		t.Errorf("docs mount should be writable: %+v", m)
	}
	if m, _ := bestMountFor("/srv/shares/backup/x", mounts); !m.ReadOnly {
		t.Errorf("backup mount (ro superblock) should be read-only: %+v", m)
	}
	if m, _ := bestMountFor("/mnt/HDD Pool/Media", mounts); m.MountPoint != "/mnt/HDD Pool" || m.ReadOnly {
		t.Errorf("escaped mount point: %+v", m)
	}
}

func TestInspectMountsLimit(t *testing.T) {
	f := NewFiles(nil)
	if _, err := f.InspectMounts(context.Background(), make([]string, sys.MaxMountPaths+1)); !errors.Is(err, sys.ErrInvalid) {
		t.Fatalf("oversized request: %v, want ErrInvalid", err)
	}
	report, err := f.InspectMounts(context.Background(), []string{"", "/definitely/not/here/netshare-hub-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Path != "/definitely/not/here/netshare-hub-test" {
		t.Fatalf("checks: %+v", report.Checks)
	}
	if w := report.Checks[0].Warnings; len(w) == 0 || w[len(w)-1] != sys.MountWarnMissing {
		t.Errorf("missing folder warnings: %v", w)
	}
}
