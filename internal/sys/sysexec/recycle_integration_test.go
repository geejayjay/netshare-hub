//go:build integration && linux

package sysexec

import (
	"errors"
	"os"
	"testing"
	"time"

	"netshare-hub/internal/sys"
)

// The purge removes only what has been in the bin longer than the share's
// retention, never follows links, and leaves the rest of the share alone.
func TestRealRecyclePurge(t *testing.T) {
	requireRoot(t)
	base := "/srv/purge-itest"
	_ = os.RemoveAll(base)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	write(t, base+"/share/keep.txt", "not in the bin")
	write(t, base+"/share/.recycle/a/old.txt", "old")
	write(t, base+"/outside.txt", "outside")
	if err := os.Symlink(base+"/outside.txt", base+"/share/.recycle/a/link"); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	s := NewSamba(ExecRunner{}, tmp+"/netshare-hub.conf", tmp+"/smb.conf")
	if err := s.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{{Name: "purge", Path: base + "/share", RecycleDays: 7}}}); err != nil &&
		!errors.Is(err, sys.ErrReloadFailed) {
		t.Fatal(err)
	}

	// Nothing is old enough yet.
	if st, err := s.PurgeRecycle(ctx, "purge"); err != nil || st.Removed != 0 {
		t.Fatalf("too early: %+v %v", st, err)
	}
	// Eight days later.
	s.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	st, err := s.PurgeRecycle(ctx, "purge")
	if err != nil {
		t.Fatal(err)
	}
	if st.Removed < 2 || exists(base+"/share/.recycle/a/old.txt") {
		t.Errorf("purge: %+v", st)
	}
	if !exists(base+"/outside.txt") || !exists(base+"/share/keep.txt") {
		t.Error("the purge reached outside the bin")
	}
	if _, err := s.PurgeRecycle(ctx, "nope"); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("unknown share: %v", err)
	}
}
