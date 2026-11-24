package sysexec

import (
	"errors"
	"testing"

	"netshare-hub/internal/sys"
)

func TestReservedFoldersAreNeverShared(t *testing.T) {
	s := NewSamba(nil, "/x/managed.conf", "/x/smb.conf")
	s.reserved = func() []string { return []string{"/srv/disks", "/srv/replicas/office"} }
	for _, p := range []string{"/srv/disks", "/srv/disks/a", "/srv", "/srv/replicas"} {
		if err := s.checkPath(sys.ShareSpec{Name: "x", Path: p}); !errors.Is(err, sys.ErrReservedFolder) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := s.checkPath(sys.ShareSpec{Name: "x", Path: "/srv/media"}); err != nil {
		t.Errorf("an unrelated folder: %v", err)
	}
}
