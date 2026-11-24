package sys

import (
	"strings"
	"testing"
)

func TestNFSExportCheck(t *testing.T) {
	ok := NFSExport{Share: "media", AsUser: "jay", Clients: []NFSClient{{Address: "192.168.1.0/24"}, {Address: "fd00::5"}}}
	if err := ok.Check(); err != nil {
		t.Fatalf("valid export refused: %v", err)
	}
	bad := map[string]NFSExport{
		"no clients":        {Share: "media"},
		"everyone v4":       {Share: "media", Clients: []NFSClient{{Address: "0.0.0.0/0"}}},
		"everyone v6":       {Share: "media", Clients: []NFSClient{{Address: "::/0"}}},
		"host name":         {Share: "media", Clients: []NFSClient{{Address: "nas.lan"}}},
		"wildcard":          {Share: "media", Clients: []NFSClient{{Address: "*"}}},
		"option injection":  {Share: "media", Clients: []NFSClient{{Address: "10.0.0.1(rw,no_root_squash)"}}},
		"not canonical":     {Share: "media", Clients: []NFSClient{{Address: " 10.0.0.1"}}},
		"duplicate":         {Share: "media", Clients: []NFSClient{{Address: "10.0.0.1"}, {Address: "10.0.0.1"}}},
		"bad share":         {Share: "../etc", Clients: []NFSClient{{Address: "10.0.0.1"}}},
		"bad account":       {Share: "media", AsUser: "root;x", Clients: []NFSClient{{Address: "10.0.0.1"}}},
		"newline in client": {Share: "media", Clients: []NFSClient{{Address: "10.0.0.1\n/etc 10.0.0.2(rw)"}}},
	}
	for name, e := range bad {
		if err := e.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestResolveExport(t *testing.T) {
	share := ShareSpec{Name: "media", Path: "/srv/media", Readers: []string{"jay", "ana"}, Writers: []string{"jay"},
		Uploaders: []string{"bob"}}
	clients := []NFSClient{{Address: "192.168.1.0/24"}}

	r, err := ResolveExport(NFSExport{Share: "media", AsUser: "jay", Clients: clients}, share, 1001, 1001)
	if err != nil || !r.Writable || r.SquashUID != 1001 || r.Path != "/srv/media" {
		t.Errorf("writer: %+v %v", r, err)
	}
	r, err = ResolveExport(NFSExport{Share: "media", AsUser: "ana", Clients: clients}, share, 1002, 1002)
	if err != nil || r.Writable {
		t.Errorf("reader must be read-only: %+v %v", r, err)
	}
	for name, c := range map[string]struct {
		user     string
		uid, gid int
	}{"upload only": {"bob", 1003, 1003}, "not on share": {"eve", 1004, 1004}, "system uid": {"jay", 0, 0}} {
		if _, err := ResolveExport(NFSExport{Share: "media", AsUser: c.user, Clients: clients}, share, c.uid, c.gid); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r, err = ResolveExport(NFSExport{Share: "media", Clients: clients}, share, -1, -1)
	if err != nil || !r.Writable || r.SquashUID != -1 {
		t.Errorf("client ids: %+v %v", r, err)
	}
	if _, err := ResolveExport(NFSExport{Share: "media", Clients: clients}, ShareSpec{Name: "media", Path: "/srv/media"}, -1, -1); err == nil {
		t.Error("a share nobody may use was exported")
	}
}

func TestRenderExports(t *testing.T) {
	out, err := RenderExports([]ResolvedExport{
		{Share: "media", Path: "/srv/my media/ünï \"#x", Writable: true, SquashUID: 1001, SquashGID: 1001,
			Clients: []NFSClient{{Address: "192.168.1.0/24"}, {Address: "192.168.2.7", ReadOnly: true}}},
		{Share: "docs", Path: "/srv/docs", SquashUID: -1, SquashGID: -1, Clients: []NFSClient{{Address: "fd00::/64"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, ExportsHeader) {
		t.Error("no header")
	}
	lines := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			lines[strings.Fields(l)[0]] = l
		}
	}
	media := lines[`/srv/my\040media/\303\274n\303\257\040\042\043x`]
	if media == "" {
		t.Fatalf("media path not escaped as expected:\n%s", out)
	}
	if len(strings.Fields(media)) != 3 {
		t.Errorf("the path split into several fields: %q", media)
	}
	for _, want := range []string{"192.168.1.0/24(rw,sync,no_subtree_check,secure,root_squash,sec=sys,fsid=",
		",all_squash,anonuid=1001,anongid=1001)", "192.168.2.7(ro,"} {
		if !strings.Contains(media, want) {
			t.Errorf("media: missing %q in %q", want, media)
		}
	}
	docs := lines["/srv/docs"]
	if !strings.Contains(docs, "fd00::/64(ro,") || strings.Contains(docs, "all_squash") || !strings.Contains(docs, "root_squash") {
		t.Errorf("docs: %q", docs)
	}
	if strings.Contains(out, "no_root_squash") || strings.Contains(out, "insecure") {
		t.Error("an unsafe option was written")
	}
	if got := ParseExportShares(out); strings.Join(got, ",") != "media,docs" {
		t.Errorf("parse: %v", got)
	}
	// Stable file handles: the same path always gets the same fsid.
	if exportFSID("/srv/docs") != exportFSID("/srv/docs") || exportFSID("/srv/docs") == exportFSID("/srv/media") {
		t.Error("fsid not stable or not unique")
	}

	for name, e := range map[string]ResolvedExport{
		"system folder": {Share: "etc", Path: "/etc", Clients: []NFSClient{{Address: "10.0.0.1"}}},
		"relative":      {Share: "x", Path: "srv/x", Clients: []NFSClient{{Address: "10.0.0.1"}}},
		"no clients":    {Share: "x", Path: "/srv/x"},
	} {
		if _, err := RenderExports([]ResolvedExport{e}); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
	if _, err := RenderExports([]ResolvedExport{
		{Share: "a", Path: "/srv/a", Clients: []NFSClient{{Address: "10.0.0.1"}}},
		{Share: "b", Path: "/srv/a", Clients: []NFSClient{{Address: "10.0.0.2"}}},
	}); err == nil {
		t.Error("one folder exported twice")
	}
}
