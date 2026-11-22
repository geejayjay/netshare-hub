package app_test

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/sys/sysfake"
)

// Step 1.11a: the file manager API, through the fake system (its workers
// serve real temporary folders, so listing, copying and zipping really run).

// raw sends a request and returns everything, for downloads.
func (c *client) raw(method, path string, header map[string]string) (int, http.Header, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, body
}

type listBody struct {
	Entries []struct {
		Name  string
		Size  int64
		IsDir bool
	}
	Next string
}

func (l listBody) names() string {
	var out []string
	for _, e := range l.Entries {
		out = append(out, e.Name)
	}
	return strings.Join(out, ",")
}

type fileEnv struct {
	admin, jay *client
	fake       *sysfake.Fake
	base       string
	media      string // jay: read and write
	docs       string // jay: read only
}

func newFileEnv(t *testing.T) fileEnv {
	t.Helper()
	env, _ := newFileEnvWith(t, app.Options{})
	return env
}

func newFileEnvWith(t *testing.T, opts app.Options) (fileEnv, *app.App) {
	t.Helper()
	admin, fake, base, application := newSystemServerWith(t, opts)
	jay := createUser(t, admin, "jay")
	for _, sh := range []struct{ name, level string }{{"media", "write"}, {"docs", "read"}} {
		status, _ := admin.do("POST", "/shares", map[string]any{
			"name": sh.name, "path": "/srv/" + sh.name,
			"access": []map[string]any{{"userId": jay.ID, "level": sh.level}},
		}, nil)
		if status != 201 {
			t.Fatalf("create share %s: %d", sh.name, status)
		}
	}
	env := fileEnv{admin: admin, fake: fake, base: base, media: fake.ShareDir("media"), docs: fake.ShareDir("docs")}
	os.MkdirAll(filepath.Join(env.media, "Films"), 0o755)
	os.WriteFile(filepath.Join(env.media, "Films", "holiday.mp4"), []byte("0123456789"), 0o644)
	os.WriteFile(filepath.Join(env.media, "notes.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(env.media, "page.html"), []byte("<script>alert(1)</script>"), 0o644)
	os.WriteFile(filepath.Join(env.docs, "manual.pdf"), []byte("%PDF-1.7"), 0o644)
	env.jay = newClient(t, base)
	env.jay.login("jay", pw)
	return env, application
}

// waitJob waits for a job to end and returns its final state.
func waitJob(t *testing.T, c *client, id string) (status, errorCode string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var j struct{ Status, ErrorCode string }
		c.do("GET", "/jobs/"+id, nil, &j)
		if j.Status == "succeeded" || j.Status == "failed" || j.Status == "cancelled" {
			return j.Status, j.ErrorCode
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return "", ""
}

type resultBody struct {
	Done int
	Job  *struct{ ID, Kind string }
}

func TestFileBrowsing(t *testing.T) {
	env := newFileEnv(t)
	jay := env.jay

	var l listBody
	status, _ := jay.do("GET", "/files/media/entries", nil, &l)
	want(t, "list status", status, 200)
	want(t, "top folder", l.names(), "Films,notes.txt,page.html")

	// Paging with a cursor.
	l = listBody{}
	jay.do("GET", "/files/media/entries?limit=2", nil, &l)
	want(t, "page 1", l.names(), "Films,notes.txt")
	next := l.Next
	l = listBody{}
	jay.do("GET", "/files/media/entries?limit=2&after="+url.QueryEscape(next), nil, &l)
	want(t, "page 2", l.names(), "page.html")

	// Folders, rename, stat.
	status, _ = jay.do("POST", "/files/media/folders", map[string]any{"path": "Films/2025"}, nil)
	want(t, "mkdir", status, 201)
	var e struct {
		Name  string
		IsDir bool
	}
	status, _ = jay.do("POST", "/files/media/rename", map[string]any{"path": "Films/2025", "name": "Summer 2025"}, &e)
	want(t, "rename", status, 200)
	want(t, "renamed", e.Name, "Summer 2025")
	var p problem
	status, _ = jay.do("POST", "/files/media/rename", map[string]any{"path": "notes.txt", "name": "../escape.txt"}, &p)
	want(t, "rename with a folder in the name", status, 422)
	want(t, "rename field", fieldCodes(p)["name"], "invalid_format")
	p = problem{}
	status, _ = jay.do("POST", "/files/media/rename", map[string]any{"path": "notes.txt", "name": "page.html"}, &p)
	want(t, "rename onto an existing name", status, 409)
	want(t, "rename conflict code", p.Code, "FILE_EXISTS")

	// A read-only share refuses changes; the user can still read it.
	p = problem{}
	status, _ = jay.do("POST", "/files/docs/folders", map[string]any{"path": "new"}, &p)
	want(t, "mkdir on read-only share", status, 403)
	want(t, "read-only code", p.Code, "SHARE_READ_ONLY")
	l = listBody{}
	status, _ = jay.do("GET", "/files/docs/entries", nil, &l)
	want(t, "list read-only share", status, 200)

	// A share the user is not on looks like it does not exist.
	p = problem{}
	status, _ = jay.do("GET", "/files/secret/entries", nil, &p)
	want(t, "unknown share", status, 404)
	want(t, "unknown share code", p.Code, "FILE_NOT_FOUND")

	// A web-only account (the setup admin) has no worker.
	p = problem{}
	status, _ = env.admin.do("GET", "/files/media/entries", nil, &p)
	want(t, "web-only admin", status, 403)
	want(t, "web-only code", p.Code, "FILES_NO_SMB_ACCESS")
}

// The step's "done when": no path, link or share name gets out of a share.
func TestFilePathTraversalAndSymlinks(t *testing.T) {
	env := newFileEnv(t)
	jay := env.jay

	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside the share"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(env.media, "escape-file"))
	os.Symlink(outside, filepath.Join(env.media, "escape-dir"))

	for _, bad := range []string{
		"../docs/manual.pdf", "/etc/passwd", "Films/../../docs", "Films/../notes.txt", "./notes.txt",
		`..\docs\manual.pdf`, "Films//holiday.mp4", "notes.txt/", "notes.txt\x00.jpg", "..",
	} {
		q := url.QueryEscape(bad)
		status, _, body := jay.raw("GET", "/files/media/download?path="+q, nil)
		if status != 422 || bytes.Contains(body, []byte("outside the share")) {
			t.Errorf("download %q: status %d, body %q; want 422", bad, status, body)
		}
		if status, _ := jay.do("GET", "/files/media/entries?path="+q, nil, nil); status != 422 {
			t.Errorf("list %q: status %d, want 422", bad, status)
		}
	}
	for _, share := range []string{"..", "media%2F..", "%2e%2e"} {
		if status, _, _ := jay.raw("GET", "/files/"+share+"/entries", nil); status < 400 {
			t.Errorf("share %q: status %d, want an error", share, status)
		}
	}

	// Links that lead out of the share are never followed.
	for _, link := range []string{"escape-file", "escape-dir/secret.txt"} {
		status, _, body := jay.raw("GET", "/files/media/download?path="+url.QueryEscape(link), nil)
		if status == 200 || bytes.Contains(body, []byte("outside the share")) {
			t.Errorf("download through link %q: status %d, body %q", link, status, body)
		}
	}
	if status, _ := jay.do("GET", "/files/media/entries?path=escape-dir", nil, nil); status == 200 {
		t.Error("listed a folder outside the share through a link")
	}
	// Copying through a link does not copy what it points to.
	var r resultBody
	jay.do("POST", "/files/copy", map[string]any{
		"items":       []map[string]any{{"share": "media", "path": "escape-dir"}, {"share": "media", "path": "escape-file"}},
		"destination": map[string]any{"share": "media", "path": "Films"},
	}, &r)
	if r.Job != nil {
		waitJob(t, jay, r.Job.ID)
	}
	if _, err := os.Stat(filepath.Join(env.media, "Films", "secret.txt")); err == nil {
		t.Error("a file outside the share was copied in through a link")
	}
	if _, err := os.Stat(filepath.Join(env.media, "Films", "escape-file")); err == nil {
		t.Error("a link was copied")
	}

	// Moving or copying a folder into itself is refused (it would never end).
	var p problem
	status, _ := jay.do("POST", "/files/copy", map[string]any{
		"items":       []map[string]any{{"share": "media", "path": "Films"}},
		"destination": map[string]any{"share": "media", "path": "Films/inner"},
	}, &p)
	want(t, "copy into itself", status, 422)
	want(t, "copy into itself field", fieldCodes(p)["items[0].path"], "into_itself")

	// The share's own folder cannot be deleted or moved.
	p = problem{}
	status, _ = jay.do("POST", "/files/delete", map[string]any{"items": []map[string]any{{"share": "media", "path": ""}}}, &p)
	want(t, "delete top folder", status, 422)
	want(t, "delete top folder field", fieldCodes(p)["items[0].path"], "top_folder")
}

func TestFileDownloads(t *testing.T) {
	env := newFileEnv(t)
	jay := env.jay

	status, h, body := jay.raw("GET", "/files/media/download?path=Films/holiday.mp4", nil)
	want(t, "download", status, 200)
	want(t, "body", string(body), "0123456789")
	want(t, "attachment by default", h.Get("Content-Disposition"), `attachment; filename=holiday.mp4`)
	want(t, "type", h.Get("Content-Type"), "application/octet-stream")
	if !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("file responses need the safety headers: %v", h)
	}

	// Range: downloads resume and video seeks.
	status, h, body = jay.raw("GET", "/files/media/download?path=Films/holiday.mp4", map[string]string{"Range": "bytes=2-4"})
	want(t, "range status", status, 206)
	want(t, "range body", string(body), "234")
	want(t, "content-range", h.Get("Content-Range"), "bytes 2-4/10")
	status, _, _ = jay.raw("GET", "/files/media/download?path=Films/holiday.mp4", map[string]string{"Range": "bytes=50-60"})
	want(t, "range outside the file", status, 416)
	status, h, body = jay.raw("GET", "/files/media/download?path=Films/holiday.mp4", map[string]string{"Range": "bytes=0-1,8-9"})
	want(t, "several ranges", status, 206)
	if !strings.HasPrefix(h.Get("Content-Type"), "multipart/byteranges; boundary=") || !bytes.Contains(body, []byte("89")) {
		t.Errorf("several ranges: %s\n%s", h.Get("Content-Type"), body)
	}

	// Inline only for safe types; HTML is always a download.
	_, h, _ = jay.raw("GET", "/files/media/download?path=notes.txt&inline=true", nil)
	want(t, "inline text type", h.Get("Content-Type"), "text/plain; charset=utf-8")
	want(t, "inline text disposition", h.Get("Content-Disposition"), "inline; filename=notes.txt")
	_, h, _ = jay.raw("GET", "/files/media/download?path=page.html&inline=true", nil)
	want(t, "html never inline", h.Get("Content-Disposition"), "attachment; filename=page.html")
	want(t, "html type", h.Get("Content-Type"), "application/octet-stream")

	// A folder is not a download.
	var p problem
	status, _ = jay.do("GET", "/files/media/download?path=Films", nil, &p)
	want(t, "download folder", status, 409)

	// --- zip: one link, once, for this user only ---
	var z struct{ URL string }
	status, _ = jay.do("POST", "/files/zip", map[string]any{"items": []map[string]any{
		{"share": "media", "path": "Films"}, {"share": "media", "path": "notes.txt"}, {"share": "docs", "path": "manual.pdf"},
	}}, &z)
	want(t, "prepare zip", status, 200)
	path := strings.TrimPrefix(z.URL, "/api/v1")

	other := newClient(t, env.base)
	createUser(t, env.admin, "ana")
	other.login("ana", pw)
	if status, _, _ := other.raw("GET", path, nil); status != 404 {
		t.Errorf("another user used the zip link: status %d", status)
	}

	status, h, body = jay.raw("GET", path, nil)
	want(t, "zip download", status, 200)
	want(t, "zip type", h.Get("Content-Type"), "application/zip")
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want(t, "zip entries", strings.Join(names, ","), "Films/,Films/holiday.mp4,manual.pdf,notes.txt")
	if status, _, _ := jay.raw("GET", path, nil); status != 404 {
		t.Errorf("a zip link worked twice: status %d", status)
	}
}

func TestFileCopyMoveDelete(t *testing.T) {
	env := newFileEnv(t)
	jay := env.jay
	item := func(share, p string) map[string]any { return map[string]any{"share": share, "path": p} }
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	// Copy is a job; "rename" (the default) keeps both.
	var r resultBody
	status, _ := jay.do("POST", "/files/copy", map[string]any{
		"items": []map[string]any{item("media", "notes.txt")}, "destination": item("media", ""),
	}, &r)
	want(t, "copy status", status, 202)
	if r.Job == nil || r.Job.Kind != "files.copy" {
		t.Fatalf("copy should be a job: %+v", r)
	}
	st, _ := waitJob(t, jay, r.Job.ID)
	want(t, "copy job", st, "succeeded")
	if b, _ := os.ReadFile(filepath.Join(env.media, "notes (2).txt")); string(b) != "hello" {
		t.Errorf("keep both: notes (2).txt = %q", b)
	}

	// A folder, copied with everything in it.
	r = resultBody{}
	jay.do("POST", "/files/copy", map[string]any{
		"items": []map[string]any{item("media", "Films")}, "destination": item("media", "notes-folder-does-not-exist"),
	}, nil)
	os.Mkdir(filepath.Join(env.media, "Backup"), 0o755)
	jay.do("POST", "/files/copy", map[string]any{
		"items": []map[string]any{item("media", "Films")}, "destination": item("media", "Backup"),
	}, &r)
	waitJob(t, jay, r.Job.ID)
	if b, _ := os.ReadFile(filepath.Join(env.media, "Backup", "Films", "holiday.mp4")); string(b) != "0123456789" {
		t.Errorf("folder copy: %q", b)
	}

	// "skip" leaves what is there; "replace" overwrites it.
	os.WriteFile(filepath.Join(env.media, "Backup", "notes.txt"), []byte("old"), 0o644)
	for _, c := range []struct{ conflict, want string }{{"skip", "old"}, {"replace", "hello"}} {
		r = resultBody{}
		jay.do("POST", "/files/copy", map[string]any{
			"items": []map[string]any{item("media", "notes.txt")}, "destination": item("media", "Backup"), "conflict": c.conflict,
		}, &r)
		waitJob(t, jay, r.Job.ID)
		if b, _ := os.ReadFile(filepath.Join(env.media, "Backup", "notes.txt")); string(b) != c.want {
			t.Errorf("conflict %s: %q, want %q", c.conflict, b, c.want)
		}
	}

	// Copying into a read-only share fails: the job says so.
	r = resultBody{}
	jay.do("POST", "/files/copy", map[string]any{
		"items": []map[string]any{item("media", "notes.txt")}, "destination": item("docs", ""),
	}, &r)
	st, code := waitJob(t, jay, r.Job.ID)
	if st != "failed" || code != "FILES_PARTLY_DONE" {
		t.Errorf("copy into a read-only share: %s %s", st, code)
	}

	// Move within a share is done at once.
	r = resultBody{}
	status, _ = jay.do("POST", "/files/move", map[string]any{
		"items": []map[string]any{item("media", "notes (2).txt")}, "destination": item("media", "Films"),
	}, &r)
	want(t, "move status", status, 200)
	want(t, "moved", r.Done, 1)
	if !exists(filepath.Join(env.media, "Films", "notes (2).txt")) || exists(filepath.Join(env.media, "notes (2).txt")) {
		t.Error("move within a share did not move")
	}

	// Delete: a file at once, a folder as a job.
	r = resultBody{}
	status, _ = jay.do("POST", "/files/delete", map[string]any{"items": []map[string]any{item("media", "Films/notes (2).txt")}}, &r)
	want(t, "delete file status", status, 200)
	want(t, "deleted", r.Done, 1)
	r = resultBody{}
	status, _ = jay.do("POST", "/files/delete", map[string]any{"items": []map[string]any{item("media", "Backup")}}, &r)
	want(t, "delete folder status", status, 202)
	st, _ = waitJob(t, jay, r.Job.ID)
	want(t, "delete job", st, "succeeded")
	if exists(filepath.Join(env.media, "Backup")) {
		t.Error("folder was not deleted")
	}

	// A user who cannot write somewhere cannot delete there either.
	var p problem
	status, _ = jay.do("POST", "/files/delete", map[string]any{"items": []map[string]any{item("docs", "manual.pdf")}}, &p)
	want(t, "delete on read-only share", status, 403)
}

func TestFileNetworkRules(t *testing.T) {
	env := newFileEnv(t)
	var sh struct{ ID int64 }
	var list struct {
		Items []struct {
			ID   int64
			Name string
		}
	}
	env.admin.do("GET", "/shares", nil, &list)
	for _, s := range list.Items {
		if s.Name == "media" {
			sh.ID = s.ID
		}
	}
	// The test client connects from 127.0.0.1.
	env.admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{
		"networkRules": []map[string]any{{"action": "allow", "address": "10.99.0.0/24"}},
	}, nil)
	var p problem
	status, _ := env.jay.do("GET", "/files/media/entries", nil, &p)
	want(t, "blocked by network rules", status, 403)
	want(t, "blocked code", p.Code, "FILES_NETWORK_BLOCKED")
	if status, _ := env.jay.do("GET", "/files/docs/entries", nil, nil); status != 200 {
		t.Errorf("another share is not affected: %d", status)
	}

	env.admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{
		"networkRules": []map[string]any{{"action": "allow", "address": "127.0.0.1"}},
	}, nil)
	if status, _ := env.jay.do("GET", "/files/media/entries", nil, nil); status != 200 {
		t.Errorf("allowed address: %d", status)
	}
}
