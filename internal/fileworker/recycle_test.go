package fileworker_test

import (
	"errors"
	"io/fs"
	"testing"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysfake"
)

// Step 2.7b: on a share with a recycle bin, deleting moves to .recycle.
func TestRemoveGoesToTheRecycleBin(t *testing.T) {
	fake := sysfake.New()
	fake.FilesDir = t.TempDir()
	if err := fake.CreateUser(ctx, "jay", "password-123"); err != nil {
		t.Fatal(err)
	}
	if err := fake.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{
		{Name: "media", Path: "/srv/media", Readers: []string{"jay"}, Writers: []string{"jay"}, RecycleDays: 30},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fake.ApplyConfig(ctx, sys.SambaConfig{}) })
	c := fileworker.NewClient(fake)

	if _, err := c.Mkdir(ctx, "jay", "media", "a"); err != nil {
		t.Fatal(err)
	}
	write(t, c, "jay", "media", "a/x.txt", "one")
	if err := c.Remove(ctx, "jay", "media", "a/x.txt", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := read(t, c, "jay", "media", ".recycle/a/x.txt", ""); got != "one" {
		t.Errorf("in the bin: %q", got)
	}
	// The same name again: a new version.
	write(t, c, "jay", "media", "a/x.txt", "two")
	if err := c.Remove(ctx, "jay", "media", "a/x.txt", false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c, "jay", "media", ".recycle/a/Copy #1 of x.txt", ""); got != "two" {
		t.Errorf("second version: %q", got)
	}
	// A whole folder goes too.
	if err := c.Remove(ctx, "jay", "media", "a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, "jay", "media", ".recycle/a"); err != nil {
		t.Errorf("folder in the bin: %v", err)
	}
	// The bin is not in the share's own listing.
	res, err := c.List(ctx, "jay", "media", ".", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.Name == ".recycle" {
			t.Error("the recycle bin is listed with the files")
		}
	}
	// Deleting inside the bin is for good.
	if err := c.Remove(ctx, "jay", "media", ".recycle/a/x.txt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, "jay", "media", ".recycle/a/x.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("still in the bin: %v", err)
	}
	if _, err := c.Stat(ctx, "jay", "media", ".recycle/.recycle"); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the bin was moved into itself")
	}
}
