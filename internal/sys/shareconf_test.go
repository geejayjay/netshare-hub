package sys

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	got, err := RenderShares([]ShareSpec{
		{Name: "media", Path: "/srv/media", Comment: "Films and music", Readers: []string{"ana", "jay"}, Writers: []string{"jay"}},
		{Name: "Archive", Path: "/mnt/tank/Архив 2025", Readers: []string{"ana"}},
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
   force group = netshare-hub
   create mask = 0660
   directory mask = 2770

[media]
   path = /srv/media
   comment = Films and music
   browseable = yes
   guest ok = no
   read only = yes
   valid users = ana, jay
   write list = jay
   force group = netshare-hub
   create mask = 0660
   directory mask = 2770
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
		"newline in comment":     func(s *ShareSpec) { s.Comment = "x\n[global]\n   guest ok = yes" },
		"line continuation":      func(s *ShareSpec) { s.Comment = `x\` },
		"samba variable":         func(s *ShareSpec) { s.Path = "/srv/%U" },
		"newline in path":        func(s *ShareSpec) { s.Path = "/srv/media\n   guest ok = yes" },
		"relative path":          func(s *ShareSpec) { s.Path = "srv/media" },
		"path with dot-dot":      func(s *ShareSpec) { s.Path = "/srv/../etc" },
		"system folder":          func(s *ShareSpec) { s.Path = "/etc/samba" },
		"root folder":            func(s *ShareSpec) { s.Path = "/" },
		"section in name":        func(s *ShareSpec) { s.Name = "x]\n[global" },
		"reserved name":          func(s *ShareSpec) { s.Name = "Global" },
		"group syntax in user":   func(s *ShareSpec) { s.Readers = []string{"@wheel"} },
		"list injection in user": func(s *ShareSpec) { s.Writers = []string{"jay, root"} },
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
		{Name: "media", Path: "/srv/media", Comment: "Films = music", Readers: []string{"ana", "jay"}, Writers: []string{"jay"}},
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
			strings.Join(g.Writers, ",") != strings.Join(want.Writers, ",") {
			t.Errorf("share %d: got %+v, want %+v", i, g, want)
		}
	}

	// Who reaches what, as a worker would see it.
	roots := RootsFor("jay", got)
	if len(roots) != 1 || roots[0].Share != "media" || !roots[0].Writable {
		t.Errorf("roots for jay = %+v", roots)
	}
	roots = RootsFor("ana", got)
	if len(roots) != 2 || roots[0].Writable || roots[1].Writable {
		t.Errorf("roots for ana = %+v (want two read-only shares)", roots)
	}
	if roots := RootsFor("nobody-here", got); len(roots) != 0 {
		t.Errorf("a user without access got roots: %+v", roots)
	}
	if len(ParseShares("")) != 0 {
		t.Error("an empty config has no shares")
	}
}
