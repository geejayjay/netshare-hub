package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Step 2.7b: the recycle bin.

func TestRecycleBin(t *testing.T) {
	env := newFileEnv(t)
	var sh struct {
		ID          int64
		RecycleDays int
	}
	// No bin yet: deleting is permanent, and the bin API says so.
	var p problem
	status, _ := env.jay.do("GET", "/files/media/recycle", nil, &p)
	if status != 404 || p.Code != "SHARE_NO_RECYCLE_BIN" {
		t.Errorf("no bin: %d %s", status, p.Code)
	}

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
	status, _ = env.admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"recycleDays": 30}, &sh)
	want(t, "turn the bin on", status, 200)
	want(t, "recycleDays", sh.RecycleDays, 30)
	sec := section(t, env.fake, "media")
	for _, l := range []string{"vfs objects = recycle", "recycle:repository = .recycle", "recycle:keeptree = yes", "hide files = /.recycle/"} {
		if !strings.Contains(sec, l) {
			t.Errorf("missing %q:\n%s", l, sec)
		}
	}
	status, _ = env.admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"recycleDays": 99999}, &p)
	want(t, "retention out of range", status, 422)

	// Deleting moves into the bin; the file list no longer shows it.
	status, _ = env.jay.do("POST", "/files/delete", map[string]any{"items": []map[string]any{{"share": "media", "path": "notes.txt"}}}, nil)
	want(t, "delete", status, 200)
	if _, err := os.Stat(filepath.Join(env.media, ".recycle", "notes.txt")); err != nil {
		t.Fatalf("not in the bin: %v", err)
	}
	var files listBody
	env.jay.do("GET", "/files/media/entries", nil, &files)
	if strings.Contains(files.names(), "notes.txt") || strings.Contains(files.names(), ".recycle") {
		t.Errorf("files: %s", files.names())
	}
	var bin listBody
	status, _ = env.jay.do("GET", "/files/media/recycle", nil, &bin)
	want(t, "list the bin", status, 200)
	if bin.names() != "notes.txt" {
		t.Errorf("bin: %s", bin.names())
	}

	// A deleted folder comes back with its contents, and a file whose
	// folder is gone brings the folder back.
	env.jay.do("POST", "/files/delete", map[string]any{"items": []map[string]any{{"share": "media", "path": "Films/holiday.mp4"}}}, nil)
	var del struct{ Job *struct{ ID string } }
	env.jay.do("POST", "/files/delete", map[string]any{"items": []map[string]any{{"share": "media", "path": "Films"}}}, &del)
	if del.Job == nil {
		t.Fatal("deleting a folder did not start a job")
	}
	if st, code := waitJob(t, env.jay, del.Job.ID); st != "succeeded" {
		t.Fatalf("delete job: %s %s", st, code)
	}
	var e struct{ Name string }
	status, _ = env.jay.do("POST", "/files/media/recycle/restore", map[string]any{"path": "Films/holiday.mp4"}, &e)
	want(t, "restore into a deleted folder", status, 200)
	if b, err := os.ReadFile(filepath.Join(env.media, "Films", "holiday.mp4")); err != nil || string(b) != "0123456789" {
		t.Errorf("restored file: %q %v", b, err)
	}
	// Restoring over a name taken since keeps both.
	os.WriteFile(filepath.Join(env.media, "notes.txt"), []byte("new"), 0o644)
	status, _ = env.jay.do("POST", "/files/media/recycle/restore", map[string]any{"path": "notes.txt"}, &e)
	want(t, "restore over a taken name", status, 200)
	want(t, "restored as", e.Name, "notes (2).txt")

	// Read-only users cannot restore or empty.
	status, _ = env.jay.do("POST", "/files/docs/recycle/empty", nil, &p)
	if status != 404 {
		t.Errorf("empty a share without a bin: %d", status)
	}

	// Emptying is for good.
	status, _ = env.jay.do("POST", "/files/media/recycle/empty", nil, nil)
	want(t, "empty", status, 204)
	if _, err := os.Stat(filepath.Join(env.media, ".recycle")); !os.IsNotExist(err) {
		t.Errorf("the bin is still there: %v", err)
	}
	bin = listBody{}
	env.jay.do("GET", "/files/media/recycle", nil, &bin)
	if len(bin.Entries) != 0 {
		t.Errorf("after empty: %s", bin.names())
	}
}
