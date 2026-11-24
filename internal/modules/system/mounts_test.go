package system

import (
	"strings"
	"testing"
)

func TestParseMountInfoAndBestMatch(t *testing.T) {
	sample := strings.Join([]string{
		"25 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw",
		"42 25 0:40 / /srv/shares/media ro,relatime - zfs tank/media ro,xattr",
		"43 25 0:41 / /srv/shares/docs rw,relatime - zfs tank/docs rw,xattr",
	}, "\n")

	mounts := parseMountInfo(strings.NewReader(sample))
	if len(mounts) != 3 {
		t.Fatalf("got %d mounts, want 3", len(mounts))
	}

	m1, ok := bestMountFor("/srv/shares/media/movies", mounts)
	if !ok || m1.MountPoint != "/srv/shares/media" || !m1.ReadOnly || m1.FSType != "zfs" {
		t.Fatalf("unexpected media mount: %+v ok=%v", m1, ok)
	}

	m2, ok := bestMountFor("/srv/local-root-folder", mounts)
	if !ok || m2.MountPoint != "/" || m2.ReadOnly {
		t.Fatalf("unexpected root mount: %+v ok=%v", m2, ok)
	}
}
