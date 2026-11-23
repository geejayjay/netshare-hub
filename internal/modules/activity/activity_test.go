package activity

import (
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"netshare-hub/internal/modules/activity/migrations"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/sys/sambalog"
)

type paths map[string]string

func (p paths) Paths(context.Context) (map[string]string, error) { return p, nil }

func newService(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(context.Background(), migrations.FS, ""); err != nil {
		t.Fatal(err)
	}
	return NewService(d, paths{"media": "/srv/media"}, Options{BatchSize: 100, BatchDelay: 20 * time.Millisecond})
}

func ev(op string, write bool, path, path2 string) sambalog.Event {
	return sambalog.Event{Kind: sambalog.KindFile, Time: time.Now(), User: "jay", ClientIP: netip.MustParseAddr("192.168.1.20"),
		ClientName: "DESKTOP", Share: "media", Op: op, OK: true, Write: write, Path: path, Path2: path2}
}

func record(t *testing.T, s *Service, events ...sambalog.Event) {
	t.Helper()
	ch := make(chan sambalog.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	s.Record(context.Background(), ch) // returns when the channel is drained, after the last batch
}

func TestRecordKeepsOnlyWhatMatters(t *testing.T) {
	s := newService(t)
	record(t, s,
		sambalog.Event{Kind: sambalog.KindConnect, User: "jay", ClientIP: netip.MustParseAddr("192.168.1.20"), Share: "media", OK: true},
		ev("openat", false, "/srv/media/Films", ""), // a read: not recorded
		ev("mkdirat", false, "/srv/media/Films/2025", ""),
		ev("openat", true, "/srv/media/Films/2025/a.mp4", ""),
		ev("renameat", false, "/srv/media/Films/2025/a.mp4", "/srv/media/Films/2025/b.mp4"),
		ev("unlinkat", false, "/srv/media/Films/2025/b.mp4", ""),
		ev("chmod", false, "/srv/media/x", ""), // not an operation the log uses
		sambalog.Event{Kind: sambalog.KindAuth, User: "jay", ClientIP: netip.MustParseAddr("10.0.0.9"), AuthStatus: "NT_STATUS_WRONG_PASSWORD"},
	)
	items, next, err := s.List(context.Background(), Filter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if next != 0 || len(items) != 6 {
		t.Fatalf("got %d entries (next %d): %+v", len(items), next, items)
	}
	// Newest first.
	want := []struct{ op, path, path2 string }{
		{"login_failed", "", ""},
		{"delete", "Films/2025/b.mp4", ""},
		{"rename", "Films/2025/a.mp4", "Films/2025/b.mp4"},
		{"write", "Films/2025/a.mp4", ""},
		{"create_folder", "Films/2025", ""},
		{"connect", "", ""},
	}
	for i, w := range want {
		if e := items[i]; e.Op != w.op || e.Path != w.path || e.Path2 != w.path2 {
			t.Errorf("entry %d = %+v, want %+v", i, e, w)
		}
	}
	if items[0].Error != "NT_STATUS_WRONG_PASSWORD" || items[0].ClientIP != "10.0.0.9" {
		t.Errorf("failed login: %+v", items[0])
	}
}

func TestListFiltersAndPages(t *testing.T) {
	s := newService(t)
	var events []sambalog.Event
	for i := 0; i < 250; i++ {
		e := ev("openat", true, fmt.Sprintf("/srv/media/f%03d.txt", i), "")
		if i%2 == 1 {
			e.User = "ana"
		}
		events = append(events, e)
	}
	record(t, s, events...)

	// Pages of 100 by cursor: 100 + 100 + 50, nothing twice.
	seen := map[int64]bool{}
	var before int64
	for page := 0; ; page++ {
		items, next, err := s.List(context.Background(), Filter{Before: before}, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range items {
			if seen[e.ID] {
				t.Fatalf("entry %d seen twice", e.ID)
			}
			seen[e.ID] = true
		}
		if next == 0 {
			break
		}
		before = next
	}
	if len(seen) != 250 {
		t.Errorf("paged through %d entries, want 250", len(seen))
	}

	items, _, _ := s.List(context.Background(), Filter{User: "ana"}, 500)
	if len(items) != 125 {
		t.Errorf("user filter: %d", len(items))
	}
	items, _, _ = s.List(context.Background(), Filter{Path: "f01"}, 500)
	if len(items) != 10 { // f010..f019
		t.Errorf("path filter: %d", len(items))
	}
	// LIKE wildcards in the search are literal.
	items, _, _ = s.List(context.Background(), Filter{Path: "f%"}, 500)
	if len(items) != 0 {
		t.Errorf("a %% in the search matched %d entries", len(items))
	}
}

func TestPruneRemovesOldEntries(t *testing.T) {
	s := newService(t)
	old := ev("mkdirat", false, "/srv/media/old", "")
	old.Time = time.Now().Add(-100 * 24 * time.Hour)
	record(t, s, old, ev("mkdirat", false, "/srv/media/new", ""))
	if err := s.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _, _ := s.List(context.Background(), Filter{}, 10)
	if len(items) != 1 || items[0].Path != "new" {
		t.Errorf("after pruning: %+v", items)
	}
}

func TestCSVCellsAreNotFormulas(t *testing.T) {
	for in, want := range map[string]string{
		"=HYPERLINK(\"http://x\")": "'=HYPERLINK(\"http://x\")",
		"+1":                       "'+1",
		"-1":                       "'-1",
		"@SUM(A1)":                 "'@SUM(A1)",
		"report.pdf":               "report.pdf",
		"":                         "",
	} {
		if got := cell(in); got != want {
			t.Errorf("cell(%q) = %q, want %q", in, got, want)
		}
	}
}
