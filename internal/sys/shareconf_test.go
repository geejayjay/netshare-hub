package sys

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	got, err := RenderShares([]ShareSpec{
		{Name: "media", Path: "/srv/media", Comment: "Films and music", Readers: []string{"ana", "jay"}, Writers: []string{"jay"}},
		{Name: "Archive", Path: "/mnt/tank/Архив 2025", Readers: []string{"ana"}},
		{Name: "inbox", Path: "/srv/inbox", Readers: []string{"jay"}, Writers: []string{"jay"}, Uploaders: []string{"ana", "bob"}},
		{Name: "public", Path: "/srv/public", Readers: []string{"jay"}, Writers: []string{"jay"}, Guest: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := ConfigHeader + `
[Archive]
   path = /mnt/tank/Архив 2025
   browseable = yes
   guest ok = no
   read only = yes
   valid users = ana
   access based share enum = yes
   inherit acls = yes
   create mask = 0660
   directory mask = 0770
   veto files = /.nsh-upload-*/
   delete veto files = yes

[inbox]
   path = /srv/inbox
   browseable = yes
   guest ok = no
   read only = yes
   valid users = jay, ana, bob
   write list = jay, ana, bob
   # upload only: ana, bob
   access based share enum = yes
   inherit acls = yes
   create mask = 0660
   directory mask = 0770
   veto files = /.nsh-upload-*/
   delete veto files = yes

[media]
   path = /srv/media
   comment = Films and music
   browseable = yes
   guest ok = no
   read only = yes
   valid users = ana, jay
   write list = jay
   access based share enum = yes
   inherit acls = yes
   create mask = 0660
   directory mask = 0770
   veto files = /.nsh-upload-*/
   delete veto files = yes

[public]
   path = /srv/public
   browseable = yes
   guest ok = yes
   read only = yes
   write list = jay
   access based share enum = yes
   inherit acls = yes
   create mask = 0660
   directory mask = 0770
   veto files = /.nsh-upload-*/
   delete veto files = yes
`
	if got != want {
		t.Errorf("config:\n%s\nwant:\n%s", got, want)
	}
}

// In Samba an empty "valid users" lets EVERY user in. A share without users
// must be switched off, never rendered with an empty or missing list.
func TestRenderShareWithoutUsersIsUnavailable(t *testing.T) {
	got, err := RenderShares([]ShareSpec{{Name: "empty", Path: "/srv/empty"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "available = no") {
		t.Errorf("share without users must be unavailable:\n%s", got)
	}
	if strings.Contains(got, "valid users") || strings.Contains(got, "write list") {
		t.Errorf("share without users must not have user lists:\n%s", got)
	}
}

// The renderer is the last line of defense: even if a bad value reached the
// database, it must never reach the config file.
func TestRenderRefusesUnsafeValues(t *testing.T) {
	ok := ShareSpec{Name: "media", Path: "/srv/media", Readers: []string{"jay"}}
	cases := map[string]func(*ShareSpec){
		"newline in comment":       func(s *ShareSpec) { s.Comment = "x\n[global]\n   guest ok = yes" },
		"line continuation":        func(s *ShareSpec) { s.Comment = `x\` },
		"samba variable":           func(s *ShareSpec) { s.Path = "/srv/%U" },
		"newline in path":          func(s *ShareSpec) { s.Path = "/srv/media\n   guest ok = yes" },
		"relative path":            func(s *ShareSpec) { s.Path = "srv/media" },
		"path with dot-dot":        func(s *ShareSpec) { s.Path = "/srv/../etc" },
		"system folder":            func(s *ShareSpec) { s.Path = "/etc/samba" },
		"root folder":              func(s *ShareSpec) { s.Path = "/" },
		"section in name":          func(s *ShareSpec) { s.Name = "x]\n[global" },
		"reserved name":            func(s *ShareSpec) { s.Name = "Global" },
		"group syntax in user":     func(s *ShareSpec) { s.Readers = []string{"@wheel"} },
		"list injection in user":   func(s *ShareSpec) { s.Writers = []string{"jay, root"} },
		"writer not a reader":      func(s *ShareSpec) { s.Writers = []string{"ana"} },
		"uploader also a reader":   func(s *ShareSpec) { s.Uploaders = []string{"jay"} },
		"bad uploader name":        func(s *ShareSpec) { s.Uploaders = []string{"@wheel"} },
		"guest share with uploads": func(s *ShareSpec) { s.Guest = true; s.Uploaders = []string{"ana"} },
	}
	for name, breakIt := range cases {
		s := ok
		breakIt(&s)
		if out, err := RenderShares([]ShareSpec{s}); err == nil {
			t.Errorf("%s: rendered an unsafe value:\n%s", name, out)
		}
	}
}

func TestPathRules(t *testing.T) {
	valid := []string{"/srv/media", "/mnt/tank/My Files", "/data/Фото", "/home/jay/share"}
	for _, p := range valid {
		if !ValidSharePath(p) || !SharablePath(p) {
			t.Errorf("%q should be allowed", p)
		}
	}
	invalid := []string{
		"", "srv", "/srv/", "/srv//x", "/srv/./x", "/srv/../x", "/srv/ x", "/srv/x ", "/srv/100%",
		`/srv/a\b`, "/srv/a\tb", "/srv/a\x00b", strings.Repeat("/a", 600),
	}
	for _, p := range invalid {
		if ValidSharePath(p) {
			t.Errorf("%q should be refused", p)
		}
	}
	system := []string{"/", "/etc", "/etc/samba", "/var/lib/samba/private", "/root", "/proc/1", "/usr/share", "/tmp/x"}
	for _, p := range system {
		if SharablePath(p) {
			t.Errorf("%q is a system folder and must not be sharable", p)
		}
	}
	// A folder that merely starts with a system name is fine.
	if !SharablePath("/etcetera") || !SharablePath("/variety/x") {
		t.Error("prefix check must respect folder boundaries")
	}
}

func TestOverlaps(t *testing.T) {
	const data = "/srv/nsh/data"
	for _, p := range []string{"/srv/nsh/data", "/srv/nsh/data/backups", "/srv/nsh", "/srv"} {
		if !Overlaps(p, data) {
			t.Errorf("sharing %q would expose %q", p, data)
		}
	}
	for _, p := range []string{"/srv/media", "/srv/nsh-other", "/srv/nsh/database"} {
		if Overlaps(p, data) {
			t.Errorf("%q does not touch %q", p, data)
		}
	}
}

// What the system layer reads back must be exactly what it wrote: the file
// workers rely on it to know who may reach which share.
func TestParseSharesRoundTrip(t *testing.T) {
	shares := []ShareSpec{
		{Name: "Archive", Path: "/mnt/tank/Архив 2025", Readers: []string{"ana"}},
		{Name: "empty", Path: "/srv/empty"},
		{Name: "inbox", Path: "/srv/inbox", Readers: []string{"jay"}, Writers: []string{"jay"}, Uploaders: []string{"ana"}},
		{Name: "media", Path: "/srv/media", Comment: "Films = music", Readers: []string{"ana", "jay"}, Writers: []string{"jay"}},
		// A guest share does not record its plain readers (everyone reads).
		{Name: "public", Path: "/srv/public", Readers: []string{"jay"}, Writers: []string{"jay"}, Guest: true},
		{Name: "tv", Path: "/srv/tv", Guest: true},
	}
	config, err := RenderShares(shares)
	if err != nil {
		t.Fatal(err)
	}
	got := ParseShares(config)
	if len(got) != len(shares) {
		t.Fatalf("parsed %d shares, want %d:\n%s", len(got), len(shares), config)
	}
	for i, want := range shares {
		g := got[i]
		if g.Name != want.Name || g.Path != want.Path || g.Comment != want.Comment ||
			strings.Join(g.Readers, ",") != strings.Join(want.Readers, ",") ||
			strings.Join(g.Writers, ",") != strings.Join(want.Writers, ",") ||
			strings.Join(g.Uploaders, ",") != strings.Join(want.Uploaders, ",") || g.Guest != want.Guest {
			t.Errorf("share %d: got %+v, want %+v", i, g, want)
		}
	}

	// Who reaches what, as a worker would see it.
	describe := func(roots []WorkerRoot) string {
		var parts []string
		for _, r := range roots {
			mode := "ro"
			if r.Writable {
				mode = "rw"
			}
			parts = append(parts, r.Share+":"+mode)
		}
		return strings.Join(parts, " ")
	}
	for account, want := range map[string]string{
		"jay": "inbox:rw media:rw public:rw tv:ro",
		// ana: reader of Archive and media, uploader on inbox (writable, the
		// kernel stops listing), and reader of the guest shares.
		"ana": "Archive:ro inbox:rw media:ro public:ro tv:ro",
		// Someone on no share still reads the guest shares.
		"carol": "public:ro tv:ro",
	} {
		if got := describe(RootsFor(account, got)); got != want {
			t.Errorf("roots for %s = %q, want %q", account, got, want)
		}
	}
	if len(ParseShares("")) != 0 {
		t.Error("an empty config has no shares")
	}
}

func TestGuestShareHasNoUserList(t *testing.T) {
	got, err := RenderShares([]ShareSpec{{Name: "tv", Path: "/srv/tv", Guest: true}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "available = no") || !strings.Contains(got, "guest ok = yes") {
		t.Errorf("a guest share must be live:\n%s", got)
	}
	if strings.Contains(got, "valid users") || strings.Contains(got, "write list") {
		t.Errorf("a guest share without writers has no user lists:\n%s", got)
	}
}

// Step 1.7: users only see shares they can open (always), and optionally
// only the files they can read.
func TestRenderVisibility(t *testing.T) {
	shares := []ShareSpec{
		{Name: "big", Path: "/srv/big", Readers: []string{"jay"}},
		{Name: "hr", Path: "/srv/hr", Readers: []string{"jay"}, HideUnreadable: true},
	}
	got, err := RenderShares(shares)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "access based share enum = yes") != 2 {
		t.Errorf("every share must hide itself from users who cannot open it:\n%s", got)
	}
	if strings.Count(got, "hide unreadable = yes") != 1 || strings.Contains(section(got, "big"), "hide unreadable") {
		t.Errorf("hide unreadable only where asked:\n%s", got)
	}
	parsed := ParseShares(got)
	if parsed[0].HideUnreadable || !parsed[1].HideUnreadable {
		t.Errorf("parsed: %+v", parsed)
	}
	roots := RootsFor("jay", parsed)
	if roots[0].HideUnreadable || !roots[1].HideUnreadable {
		t.Errorf("the file manager must hide the same: %+v", roots)
	}
}

func section(config, name string) string {
	start := strings.Index(config, "["+name+"]")
	rest := config[start+1:]
	if end := strings.Index(rest, "\n["); end >= 0 {
		rest = rest[:end]
	}
	return rest
}
