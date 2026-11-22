package sys

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseHostRule(t *testing.T) {
	good := map[string]string{
		"192.168.1.50":    "192.168.1.50",
		" 10.0.0.0/8 ":    "10.0.0.0/8",
		"192.168.1.50/32": "192.168.1.50", // a one-address range is an address
		"0.0.0.0/0":       "0.0.0.0/0",
		"2001:DB8::/32":   "2001:db8::/32",
		"fd00::1":         "fd00::1",
	}
	for in, want := range good {
		if got, err := ParseHostRule(in); err != nil || got != want {
			t.Errorf("ParseHostRule(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "ALL", "LOCAL", "EXCEPT", "nas.local", "192.168.1.", "192.168.1.*",
		"10.0.0.0/8 EXCEPT 10.0.0.1", "10.0.0.1, 10.0.0.2", "10.0.0.1\n   guest ok = yes",
		"300.1.1.1", "10.0.0.0/33", "fe80::1%eth0", "::ffff:10.0.0.1", "10.0.0.0/255.0.0.0",
	}
	for _, in := range bad {
		if got, err := ParseHostRule(in); err == nil {
			t.Errorf("ParseHostRule(%q) = %q, want an error", in, got)
		}
	}
	// A range with host bits set gets a suggestion.
	if _, err := ParseHostRule("192.168.1.5/24"); err == nil || !strings.Contains(err.Error(), "192.168.1.0/24") {
		t.Errorf("want a suggestion, got %v", err)
	}
}

func TestHostAllowed(t *testing.T) {
	ip := netip.MustParseAddr
	cases := []struct {
		allow, block []string
		addr         string
		want         bool
	}{
		{nil, nil, "203.0.113.9", true}, // no rules: everyone
		{[]string{"192.168.1.0/24"}, nil, "192.168.1.7", true},
		{[]string{"192.168.1.0/24"}, nil, "192.168.2.7", false}, // not on the allow list
		{[]string{"192.168.1.0/24"}, []string{"192.168.1.50"}, "192.168.1.50", false},
		{[]string{"192.168.1.0/24"}, []string{"192.168.1.50"}, "192.168.1.51", true},
		// Unlike Samba's raw allow+deny, an address on neither list is out.
		{[]string{"192.168.1.0/24"}, []string{"10.0.0.5"}, "172.16.0.1", false},
		{nil, []string{"10.0.0.0/8"}, "10.1.2.3", false},
		{nil, []string{"10.0.0.0/8"}, "192.168.1.1", true},
		{[]string{"2001:db8::/32"}, nil, "2001:db8::1", true},
		{[]string{"192.168.1.0/24"}, nil, "::ffff:192.168.1.7", true}, // IPv4 seen through IPv6
		// No exception for the server itself (that is Samba's, not ours).
		{[]string{"192.168.1.0/24"}, nil, "127.0.0.1", false},
	}
	for _, c := range cases {
		if got := HostAllowed(c.allow, c.block, ip(c.addr)); got != c.want {
			t.Errorf("allow %v block %v: %s allowed = %v, want %v", c.allow, c.block, c.addr, got, c.want)
		}
	}
}

func TestRenderNetworkRules(t *testing.T) {
	shares := []ShareSpec{
		{Name: "a", Path: "/srv/a", Readers: []string{"jay"}, Allow: []string{"192.168.1.0/24", "10.0.0.5"}},
		{Name: "b", Path: "/srv/b", Readers: []string{"jay"}, Allow: []string{"192.168.1.0/24"}, Block: []string{"192.168.1.50"}},
		{Name: "c", Path: "/srv/c", Readers: []string{"jay"}, Block: []string{"2001:db8::/32"}},
		{Name: "d", Path: "/srv/d", Readers: []string{"jay"}},
	}
	got, err := RenderShares(shares)
	if err != nil {
		t.Fatal(err)
	}
	for share, line := range map[string]string{
		"a": "hosts allow = 192.168.1.0/24, 10.0.0.5",
		"b": "hosts allow = 192.168.1.0/24 EXCEPT 192.168.1.50",
		"c": "hosts deny = 2001:db8::/32",
	} {
		if !strings.Contains(section(got, share), line) {
			t.Errorf("share %s is missing %q:\n%s", share, line, got)
		}
	}
	if strings.Contains(section(got, "d"), "hosts") {
		t.Errorf("a share without rules has no hosts lines:\n%s", got)
	}
	for i, p := range ParseShares(got) {
		w := shares[i]
		if strings.Join(p.Allow, ",") != strings.Join(w.Allow, ",") || strings.Join(p.Block, ",") != strings.Join(w.Block, ",") {
			t.Errorf("round trip %s: allow %v block %v, want %v %v", p.Name, p.Allow, p.Block, w.Allow, w.Block)
		}
	}

	// The system layer refuses anything not in canonical form, so nothing
	// unchecked (keywords, host names, line breaks) reaches the config.
	for _, bad := range [][]string{
		{"ALL"}, {"10.0.0.0/8 EXCEPT 10.0.0.1"}, {"192.168.1.5/24"}, {"nas.local"},
		{"10.0.0.1\n[global]"}, {"10.0.0.1", "10.0.0.1"}, {"2001:DB8::/32"},
	} {
		for _, field := range []string{"allow", "block"} {
			s := ShareSpec{Name: "x", Path: "/srv/x", Readers: []string{"jay"}}
			if field == "allow" {
				s.Allow = bad
			} else {
				s.Block = bad
			}
			if out, err := RenderShares([]ShareSpec{s}); err == nil {
				t.Errorf("%s %q was rendered:\n%s", field, bad, out)
			}
		}
	}
	tooMany := make([]string, MaxHostRules+1)
	for i := range tooMany {
		tooMany[i] = netip.AddrFrom4([4]byte{10, 0, byte(i / 256), byte(i % 256)}).String()
	}
	if _, err := RenderShares([]ShareSpec{{Name: "x", Path: "/srv/x", Readers: []string{"jay"}, Allow: tooMany}}); err == nil {
		t.Error("more than MaxHostRules entries was rendered")
	}
}
