package fileworker_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysfake"
)

var ctx = context.Background()

// setup gives two users two shares, served from real temporary folders:
//
//	media: jay read+write, ana read-only
//	docs:  jay only
func setup(t *testing.T) (*fileworker.Client, *sysfake.Fake) {
	t.Helper()
	fake := sysfake.New()
	fake.FilesDir = t.TempDir()
	for _, name := range []string{"jay", "ana", "bob"} {
		if err := fake.CreateUser(ctx, name, "password-123"); err != nil {
			t.Fatal(err)
		}
	}
	err := fake.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{
		{Name: "media", Path: "/srv/media", Readers: []string{"jay", "ana"}, Writers: []string{"jay"}},
		{Name: "docs", Path: "/srv/docs", Readers: []string{"jay"}, Writers: []string{"jay"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fake.ApplyConfig(ctx, sys.SambaConfig{}) }) // stops the in-process workers
	return fileworker.NewClient(fake), fake
}

func write(t *testing.T, c *fileworker.Client, account, share, path, content string) {
	t.Helper()
	if _, err := c.Write(ctx, account, share, path, strings.NewReader(content), false); err != nil {
		t.Fatalf("Write(%s): %v", path, err)
	}
}

func read(t *testing.T, c *fileworker.Client, account, share, path, rng string) string {
	t.Helper()
	d, err := c.Read(ctx, account, share, path, rng)
	if err != nil {
		t.Fatalf("Read(%s): %v", path, err)
	}
	defer d.Body.Close()
	b, _ := io.ReadAll(d.Body)
	return string(b)
}

func TestFileOperations(t *testing.T) {
	c, fake := setup(t)

	// Create a folder and a file, then read them back.
	if _, err := c.Mkdir(ctx, "jay", "media", "films"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	write(t, c, "jay", "media", "films/notes.txt", "hello world")
	if got := read(t, c, "jay", "media", "films/notes.txt", ""); got != "hello world" {
		t.Errorf("content = %q", got)
	}
	// The file really is inside the share's folder on disk.
	onDisk, err := os.ReadFile(filepath.Join(fake.ShareDir("media"), "films", "notes.txt"))
	if err != nil || string(onDisk) != "hello world" {
		t.Errorf("file on disk: %q, %v", onDisk, err)
	}

	// A byte range, so downloads can resume and media can seek.
	d, err := c.Read(ctx, "jay", "media", "films/notes.txt", "bytes=6-10")
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(d.Body)
	d.Body.Close()
	if string(part) != "world" || d.Status != http.StatusPartialContent {
		t.Errorf("range read = %q, status %d", part, d.Status)
	}
	if _, err := c.Read(ctx, "jay", "media", "films/notes.txt", "bytes=500-600"); !errors.Is(err, fileworker.ErrInvalidRange) {
		t.Errorf("range past the end: got %v", err)
	}

	// Stat and list.
	entry, err := c.Stat(ctx, "jay", "media", "films/notes.txt")
	if err != nil || entry.Name != "notes.txt" || entry.Size != 11 || entry.IsDir {
		t.Errorf("Stat = %+v, %v", entry, err)
	}
	list, err := c.List(ctx, "jay", "media", "", "", 0)
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Name != "films" || !list.Entries[0].IsDir {
		t.Errorf("List = %+v, %v", list, err)
	}

	// An existing file is only replaced when asked to.
	if _, err := c.Write(ctx, "jay", "media", "films/notes.txt", strings.NewReader("new"), false); !errors.Is(err, fs.ErrExist) {
		t.Errorf("write over existing file: got %v, want fs.ErrExist", err)
	}
	if _, err := c.Write(ctx, "jay", "media", "films/notes.txt", strings.NewReader("replaced"), true); err != nil {
		t.Errorf("overwrite: %v", err)
	}
	if got := read(t, c, "jay", "media", "films/notes.txt", ""); got != "replaced" {
		t.Errorf("after overwrite = %q", got)
	}

	// Rename, with the same "do not replace by accident" rule.
	write(t, c, "jay", "media", "films/other.txt", "other")
	if err := c.Rename(ctx, "jay", "media", "films/notes.txt", "films/other.txt", false); !errors.Is(err, fs.ErrExist) {
		t.Errorf("rename onto existing file: got %v, want fs.ErrExist", err)
	}
	if err := c.Rename(ctx, "jay", "media", "films/notes.txt", "films/renamed.txt", false); err != nil {
		t.Errorf("Rename: %v", err)
	}
	if _, err := c.Stat(ctx, "jay", "media", "films/notes.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old name after rename: got %v", err)
	}

	// Remove: a folder with content needs "recursive".
	if err := c.Remove(ctx, "jay", "media", "films", false); !errors.Is(err, fileworker.ErrNotEmpty) && !errors.Is(err, fs.ErrExist) {
		t.Errorf("remove non-empty folder: got %v, want ErrNotEmpty", err)
	}
	if err := c.Remove(ctx, "jay", "media", "films", true); err != nil {
		t.Errorf("recursive remove: %v", err)
	}
	if _, err := c.Stat(ctx, "jay", "media", "films"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("folder after remove: got %v", err)
	}
	// The share's own folder is never removed.
	if err := c.Remove(ctx, "jay", "media", "", true); !errors.Is(err, fileworker.ErrInvalidPath) {
		t.Errorf("remove share root: got %v, want ErrInvalidPath", err)
	}
}

func TestListPaging(t *testing.T) {
	c, _ := setup(t)
	for _, name := range []string{"e", "a", "d", "b", "c"} {
		write(t, c, "jay", "docs", name+".txt", name)
	}

	var names []string
	after := ""
	for page := 0; page < 10; page++ {
		res, err := c.List(ctx, "jay", "docs", "", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) > 2 {
			t.Fatalf("page has %d entries, limit was 2", len(res.Entries))
		}
		for _, e := range res.Entries {
			names = append(names, e.Name)
		}
		if res.Next == "" {
			break
		}
		after = res.Next
	}
	if got := strings.Join(names, " "); got != "a.txt b.txt c.txt d.txt e.txt" {
		t.Errorf("paged listing = %q", got)
	}
}

func TestReadOnlyAccess(t *testing.T) {
	c, _ := setup(t)
	write(t, c, "jay", "media", "film.txt", "content")

	// Ana may read...
	if got := read(t, c, "ana", "media", "film.txt", ""); got != "content" {
		t.Errorf("read-only user read %q", got)
	}
	// ...but nothing that changes the share.
	changes := map[string]func() error{
		"write": func() error {
			_, err := c.Write(ctx, "ana", "media", "new.txt", strings.NewReader("x"), false)
			return err
		},
		"mkdir":  func() error { _, err := c.Mkdir(ctx, "ana", "media", "folder"); return err },
		"rename": func() error { return c.Rename(ctx, "ana", "media", "film.txt", "mine.txt", false) },
		"remove": func() error { return c.Remove(ctx, "ana", "media", "film.txt", false) },
	}
	for name, change := range changes {
		if err := change(); !errors.Is(err, sys.ErrReadOnly) {
			t.Errorf("%s by a read-only user: got %v, want ErrReadOnly", name, err)
		}
	}
	if got := read(t, c, "jay", "media", "film.txt", ""); got != "content" {
		t.Error("a read-only user changed the file")
	}
}

func TestOnlyOwnShares(t *testing.T) {
	c, _ := setup(t)
	write(t, c, "jay", "docs", "private.txt", "secret")

	// Ana is not on "docs": to her worker the share does not exist.
	if _, err := c.Read(ctx, "ana", "docs", "private.txt", ""); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read from someone else's share: got %v, want fs.ErrNotExist", err)
	}
	if _, err := c.List(ctx, "ana", "docs", "", "", 0); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("list someone else's share: got %v, want fs.ErrNotExist", err)
	}
	// Bob has no shares at all, so he gets no worker.
	if _, err := c.List(ctx, "bob", "media", "", "", 0); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("user without shares: got %v, want sys.ErrNotFound", err)
	}
	// System accounts and unknown accounts never get a worker.
	if _, err := c.List(ctx, "root", "media", "", "", 0); !errors.Is(err, sys.ErrNotManaged) {
		t.Errorf("worker for root: got %v, want sys.ErrNotManaged", err)
	}
	if _, err := c.List(ctx, "ghost", "media", "", "", 0); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("worker for unknown account: got %v, want sys.ErrNotFound", err)
	}
}

// Nothing a caller sends may reach outside the share's folder.
func TestConfinement(t *testing.T) {
	c, fake := setup(t)
	outside := filepath.Join(fake.FilesDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"../outside.txt", "..", "a/../../outside.txt", "/etc/passwd", "a//b", "./x", "a/./b", `..\outside.txt`, "x\x00y"} {
		if _, err := c.Read(ctx, "jay", "media", p, ""); !errors.Is(err, fileworker.ErrInvalidPath) {
			t.Errorf("Read(%q): got %v, want ErrInvalidPath", p, err)
		}
		if _, err := c.Write(ctx, "jay", "media", p, strings.NewReader("x"), true); !errors.Is(err, fileworker.ErrInvalidPath) {
			t.Errorf("Write(%q): got %v, want ErrInvalidPath", p, err)
		}
		if err := c.Remove(ctx, "jay", "media", p, true); !errors.Is(err, fileworker.ErrInvalidPath) {
			t.Errorf("Remove(%q): got %v, want ErrInvalidPath", p, err)
		}
		if err := c.Rename(ctx, "jay", "media", "x", p, true); !errors.Is(err, fileworker.ErrInvalidPath) {
			t.Errorf("Rename to %q: got %v, want ErrInvalidPath", p, err)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "not yours" {
		t.Error("a file outside the share was changed")
	}

	// A symbolic link inside the share that points outside must not be followed.
	link := filepath.Join(fake.ShareDir("media"), "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlinks here (%v); the symlink case runs in the Linux container", err)
	}
	if _, err := c.Read(ctx, "jay", "media", "escape", ""); err == nil {
		t.Error("a symlink pointing out of the share was followed")
	}
	if _, err := c.Write(ctx, "jay", "media", "escape", strings.NewReader("overwritten"), true); err == nil {
		if b, _ := os.ReadFile(outside); string(b) != "not yours" {
			t.Error("writing through a symlink changed a file outside the share")
		}
	}
}

// failingReader breaks off in the middle, like an upload whose connection drops.
type failingReader struct{ sent bool }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.sent {
		return 0, errors.New("connection lost")
	}
	f.sent = true
	return copy(p, "partial data"), nil
}

func TestInterruptedUploadLeavesNothing(t *testing.T) {
	c, fake := setup(t)
	if _, err := c.Write(ctx, "jay", "media", "big.bin", &failingReader{}, false); err == nil {
		t.Fatal("an interrupted upload must fail")
	}
	// Give the worker a moment to clean up after the aborted request.
	time.Sleep(200 * time.Millisecond)
	left, _ := os.ReadDir(fake.ShareDir("media"))
	for _, e := range left {
		t.Errorf("left behind after an interrupted upload: %s", e.Name())
	}
}

// A worker must not keep serving a share its user was removed from.
func TestAccessChangesTakeEffect(t *testing.T) {
	c, fake := setup(t)
	write(t, c, "jay", "media", "film.txt", "content")
	read(t, c, "ana", "media", "film.txt", "") // ana's worker is now running

	// Ana is taken off the share.
	fake.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{
		{Name: "media", Path: "/srv/media", Readers: []string{"jay"}, Writers: []string{"jay"}},
	}})
	if _, err := c.Read(ctx, "ana", "media", "film.txt", ""); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("read after losing access: got %v, want sys.ErrNotFound", err)
	}
	// Jay still works (his access did not change).
	if got := read(t, c, "jay", "media", "film.txt", ""); got != "content" {
		t.Errorf("jay after the change read %q", got)
	}

	// A deleted account has no worker.
	fake.DeleteUser(ctx, "jay")
	if _, err := c.Read(ctx, "jay", "media", "film.txt", ""); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("read by a deleted account: got %v, want sys.ErrNotFound", err)
	}
}

func TestWorkerRefusesUnauthorizedCallers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := fileworker.Config{Roots: []sys.WorkerRoot{{Share: "media", Path: t.TempDir(), Writable: true}}, IdleSeconds: 60}
	go fileworker.Serve(listener, cfg, func(*http.Request) bool { return false })
	t.Cleanup(func() { listener.Close() })

	res, err := http.Get("http://" + listener.Addr().String() + "/v1/list?share=media")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.StatusCode)
	}
}

func TestWorkerExitsWhenIdle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- fileworker.Serve(listener, fileworker.Config{IdleSeconds: 1}, func(*http.Request) bool { return true })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("idle exit returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not exit after being idle")
	}
}

func TestDisabledAccountGetsNoWorker(t *testing.T) {
	c, fake := setup(t)
	write(t, c, "jay", "media", "a.txt", "x")
	fake.SetEnabled(ctx, "jay", false)
	if _, err := c.List(ctx, "jay", "media", "", "", 0); !errors.Is(err, sys.ErrDisabled) {
		t.Errorf("disabled account: got %v, want sys.ErrDisabled", err)
	}
	if _, err := c.List(sys.WithBackground(ctx), "jay", "media", "", "", 0); !errors.Is(err, sys.ErrDisabled) {
		t.Errorf("disabled account, background: got %v, want sys.ErrDisabled", err)
	}
}
