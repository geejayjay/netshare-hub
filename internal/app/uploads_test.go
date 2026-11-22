package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/app"
)

// Step 1.13a: resumable (tus) uploads, through the fake system (its workers
// write real files in temporary folders).

type tusResponse struct {
	status int
	header http.Header
	body   string
}

func (c *client) tus(method, path string, header map[string]string, body []byte) tusResponse {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, c.base+path, reader)
	req.Header.Set("Tus-Resumable", "1.0.0")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range header {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return tusResponse{status: res.StatusCode, header: res.Header, body: string(raw)}
}

func metadata(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, kv[i]+" "+base64.StdEncoding.EncodeToString([]byte(kv[i+1])))
	}
	return strings.Join(parts, ",")
}

// startUpload creates an upload and returns its path ("/uploads/<id>").
func startUpload(t *testing.T, c *client, size int, kv ...string) string {
	t.Helper()
	res := c.tus("POST", "/uploads", map[string]string{"Upload-Length": itoa(int64(size)), "Upload-Metadata": metadata(kv...)}, nil)
	if res.status != 201 {
		t.Fatalf("create upload: %d %s", res.status, res.body)
	}
	return strings.TrimPrefix(res.header.Get("Location"), "/api/v1")
}

func send(c *client, upload string, offset int, data []byte) tusResponse {
	return c.tus("PATCH", upload, map[string]string{
		"Upload-Offset": itoa(int64(offset)), "Content-Type": "application/offset+octet-stream",
	}, data)
}

func TestResumableUpload(t *testing.T) {
	env := newFileEnv(t)
	jay := env.jay

	data := []byte("The quick brown fox jumps over the lazy dog")
	up := startUpload(t, jay, len(data), "share", "media", "path", "Films", "filename", "fox.txt")

	// The first part arrives, then the connection "breaks".
	res := send(jay, up, 0, data[:10])
	want(t, "first part", res.status, 204)
	want(t, "offset after first part", res.header.Get("Upload-Offset"), "10")

	// Where to resume: asked again, as a client does after a refresh.
	res = jay.tus("HEAD", up, nil, nil)
	want(t, "head", res.status, 200)
	want(t, "head offset", res.header.Get("Upload-Offset"), "10")
	want(t, "head length", res.header.Get("Upload-Length"), itoa(int64(len(data))))

	// The data in progress is hidden: not in the listing, not under its name.
	var l listBody
	jay.do("GET", "/files/media/entries?path=Films", nil, &l)
	if strings.Contains(l.names(), "nsh-upload") || strings.Contains(l.names(), "fox.txt") {
		t.Errorf("an unfinished upload is visible: %s", l.names())
	}

	// Data for the wrong position is refused.
	res = send(jay, up, 5, data[5:])
	want(t, "wrong offset", res.status, 409)

	// The rest; the file appears under its name.
	res = send(jay, up, 10, data[10:])
	want(t, "last part", res.status, 204)
	want(t, "final offset", res.header.Get("Upload-Offset"), itoa(int64(len(data))))
	want(t, "where it landed", res.header.Get("NetShare-File-Path"), "Films%2Ffox.txt")
	if got, _ := os.ReadFile(filepath.Join(env.media, "Films", "fox.txt")); !bytes.Equal(got, data) {
		t.Errorf("uploaded file = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Join(env.media, "Films"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nsh-upload-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
	// A finished upload is gone.
	want(t, "head after finish", jay.tus("HEAD", up, nil, nil).status, 404)

	// Same name again: both kept by default; "replace" overwrites.
	up = startUpload(t, jay, 3, "share", "media", "path", "Films", "filename", "fox.txt")
	res = send(jay, up, 0, []byte("two"))
	want(t, "keep both", res.header.Get("NetShare-File-Path"), "Films%2Ffox%20%282%29.txt")
	up = startUpload(t, jay, 5, "share", "media", "path", "Films", "filename", "fox.txt", "conflict", "replace")
	send(jay, up, 0, []byte("three"))
	if got, _ := os.ReadFile(filepath.Join(env.media, "Films", "fox.txt")); string(got) != "three" {
		t.Errorf("replace: %q", got)
	}

	// An empty file is complete at once.
	res = jay.tus("POST", "/uploads", map[string]string{"Upload-Length": "0",
		"Upload-Metadata": metadata("share", "media", "path", "", "filename", "empty.txt")}, nil)
	want(t, "empty file", res.status, 201)
	want(t, "empty file path", res.header.Get("NetShare-File-Path"), "empty.txt")
	if info, err := os.Stat(filepath.Join(env.media, "empty.txt")); err != nil || info.Size() != 0 {
		t.Errorf("empty file: %v", err)
	}

	// More data than announced is never taken.
	up = startUpload(t, jay, 4, "share", "media", "path", "", "filename", "four.txt")
	res = send(jay, up, 0, []byte("12345678"))
	want(t, "too much data, offset", res.header.Get("Upload-Offset"), "4")
	if got, _ := os.ReadFile(filepath.Join(env.media, "four.txt")); string(got) != "1234" {
		t.Errorf("more than announced was written: %q", got)
	}

	// Cancel: the data is thrown away.
	up = startUpload(t, jay, 100, "share", "media", "path", "", "filename", "cancelled.txt")
	send(jay, up, 0, []byte("partial"))
	want(t, "cancel", jay.tus("DELETE", up, nil, nil).status, 204)
	want(t, "head after cancel", jay.tus("HEAD", up, nil, nil).status, 404)
	entries, _ = os.ReadDir(env.media)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nsh-upload-") {
			t.Errorf("cancelled upload left data: %s", e.Name())
		}
	}
}

// The step's "done when": a crafted upload cannot land outside the share,
// or anywhere the user may not write.
func TestUploadRefusesCraftedPaths(t *testing.T) {
	env := newFileEnv(t)
	jay := env.jay

	for _, kv := range [][]string{
		{"share", "media", "path", "../docs", "filename", "x.txt"},
		{"share", "media", "path", "/etc", "filename", "x.txt"},
		{"share", "media", "path", "Films/../..", "filename", "x.txt"},
		{"share", "media", "path", "", "filename", "../docs/x.txt"},
		{"share", "media", "path", "", "filename", "Films/x.txt"},
		{"share", "media", "path", "", "filename", `..\x.txt`},
		{"share", "media", "path", "", "filename", ".."},
		{"share", "media", "path", "", "filename", ""},
		{"share", "media", "path", "", "filename", ".nsh-upload-0123456789abcdef0123456789abcdef"},
		{"share", "../media", "path", "", "filename", "x.txt"},
		{"share", "media", "path", "", "filename", "x.txt", "conflict", "skip"},
	} {
		res := jay.tus("POST", "/uploads", map[string]string{"Upload-Length": "3", "Upload-Metadata": metadata(kv...)}, nil)
		if res.status != 422 {
			t.Errorf("%v: status %d, want 422", kv, res.status)
		}
	}

	// A link inside the share that points out of it is not a way out.
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(env.media, "escape"))
	res := jay.tus("POST", "/uploads", map[string]string{"Upload-Length": "3",
		"Upload-Metadata": metadata("share", "media", "path", "escape", "filename", "x.txt")}, nil)
	if res.status == 201 {
		up := strings.TrimPrefix(res.header.Get("Location"), "/api/v1")
		send(jay, up, 0, []byte("bad"))
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("an upload landed outside the share through a link: %v", entries)
	}

	// Forged upload IDs find nothing.
	for _, id := range []string{"..%2F..%2Fetc%2Fpasswd", "0123", strings.Repeat("g", 32)} {
		if status := jay.tus("HEAD", "/uploads/"+id, nil, nil).status; status != 404 {
			t.Errorf("HEAD /uploads/%s: %d, want 404", id, status)
		}
	}

	// A read-only share takes no uploads.
	res = jay.tus("POST", "/uploads", map[string]string{"Upload-Length": "3",
		"Upload-Metadata": metadata("share", "docs", "path", "", "filename", "x.txt")}, nil)
	want(t, "upload to a read-only share", res.status, 403)
}

func TestUploadSecurity(t *testing.T) {
	env := newFileEnv(t)
	jay := env.jay
	up := startUpload(t, jay, 10, "share", "media", "path", "", "filename", "mine.txt")

	// Someone else's upload does not exist for you.
	ana := newClient(t, env.base)
	createUser(t, env.admin, "ana")
	ana.login("ana", pw)
	for _, m := range []string{"HEAD", "DELETE"} {
		if status := ana.tus(m, up, nil, nil).status; status != 404 {
			t.Errorf("ana %s jay's upload: %d, want 404", m, status)
		}
	}
	if status := send(ana, up, 0, []byte("0123456789")).status; status != 404 {
		t.Errorf("ana PATCH jay's upload: %d, want 404", status)
	}

	// Changes need the CSRF token, like every other change.
	csrf := jay.csrf
	jay.csrf = ""
	if status := send(jay, up, 0, []byte("0123456789")).status; status != 403 {
		t.Errorf("PATCH without CSRF token: %d, want 403", status)
	}
	jay.csrf = csrf
	// And a login.
	anon := newClient(t, env.base)
	if status := anon.tus("POST", "/uploads", map[string]string{"Upload-Length": "1"}, nil).status; status != 401 {
		t.Errorf("anonymous upload: %d, want 401", status)
	}
	// Only tus 1.0.0.
	if status := jay.tus("HEAD", up, map[string]string{"Tus-Resumable": "0.2.2"}, nil).status; status != 412 {
		t.Errorf("old tus version: %d, want 412", status)
	}
	// Data must be sent as tus data.
	res := jay.tus("PATCH", up, map[string]string{"Upload-Offset": "0", "Content-Type": "text/plain"}, []byte("x"))
	want(t, "wrong content type", res.status, 415)
}

func TestAbandonedUploadsAreCleanedUp(t *testing.T) {
	env, application := newFileEnvWith(t, app.Options{UploadExpiry: 50 * time.Millisecond})
	jay := env.jay
	up := startUpload(t, jay, 100, "share", "media", "path", "", "filename", "abandoned.txt")
	send(jay, up, 0, []byte("some data"))

	time.Sleep(1100 * time.Millisecond) // timestamps are in seconds
	application.SweepUploads(context.Background())

	want(t, "head after cleanup", jay.tus("HEAD", up, nil, nil).status, 404)
	entries, _ := os.ReadDir(env.media)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nsh-upload-") {
			t.Errorf("abandoned upload data left behind: %s", e.Name())
		}
	}
}
