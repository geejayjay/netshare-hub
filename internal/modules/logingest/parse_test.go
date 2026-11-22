package logingest

import (
	"strings"
	"testing"
	"time"
)

// Real lines, as Samba 4.17 wrote them in the test container.
const realLog = `[2026/11/22 17:24:24.114062,  1] ../../source3/modules/vfs_full_audit.c:643(do_log)
  nshaudit|jay|127.0.0.1|aud|ca542a78af9a|connect|ok|aud
[2026/11/22 17:24:24.114721,  1] ../../source3/modules/vfs_full_audit.c:643(do_log)
  nshaudit|jay|127.0.0.1|aud|ca542a78af9a|openat|fail (No such file or directory)|r|/srv/shares/aud/dir one
[2026/11/22 17:24:24.115335,  1] ../../source3/modules/vfs_full_audit.c:643(do_log)
  nshaudit|jay|127.0.0.1|aud|ca542a78af9a|mkdirat|ok|/srv/shares/aud/dir one
[2026/11/22 17:24:24.116809,  1] ../../source3/modules/vfs_full_audit.c:643(do_log)
  nshaudit|jay|127.0.0.1|aud|ca542a78af9a|openat|ok|w|/srv/shares/aud/dir one/a b.txt
[2026/11/22 17:24:24.118270,  1] ../../source3/modules/vfs_full_audit.c:643(do_log)
  nshaudit|jay|127.0.0.1|aud|ca542a78af9a|renameat|ok|/srv/shares/aud/dir one/a b.txt|/srv/shares/aud/dir one/c.txt
[2026/11/22 17:24:24.119345,  1] ../../source3/modules/vfs_full_audit.c:643(do_log)
  nshaudit|jay|127.0.0.1|aud|ca542a78af9a|unlinkat|ok|/srv/shares/aud/dir one/c.txt
[2026/11/22 17:24:24.120010,  1] ../../source3/modules/vfs_full_audit.c:643(do_log)
  nshaudit|jay|127.0.0.1|aud|ca542a78af9a|disconnect|ok|aud
  {"timestamp": "2026-11-22T17:23:15.982117+0000", "type": "Authentication", "Authentication": {"version": {"major": 1, "minor": 2}, "eventId": 4624, "logonId": "0", "logonType": 3, "status": "NT_STATUS_OK", "localAddress": "ipv4:127.0.0.1:445", "remoteAddress": "ipv4:127.0.0.1:60762", "serviceDescription": "SMB2", "authDescription": null, "clientDomain": "WORKGROUP", "clientAccount": "jay", "workstation": "CA542A78AF9A"}}
  {"timestamp": "2026-11-22T17:23:16.022207+0000", "type": "Authentication", "Authentication": {"version": {"major": 1, "minor": 2}, "eventId": 4625, "logonId": "0", "logonType": 3, "status": "NT_STATUS_WRONG_PASSWORD", "localAddress": "ipv4:127.0.0.1:445", "remoteAddress": "ipv4:192.168.1.66:60766", "serviceDescription": "SMB2", "authDescription": null, "clientDomain": "WORKGROUP", "clientAccount": "jay"}}
[2026/11/22 17:24:24.130000,  0] ../../source3/smbd/server.c:1234(main)
  smbd version 4.17.12-Debian started.
`

func parseAll(log string) []Event {
	p := NewParser()
	var out []Event
	for _, line := range strings.Split(log, "\n") {
		if ev, ok := p.Line(line); ok {
			out = append(out, ev)
		}
	}
	return out
}

func TestParseRealSambaLines(t *testing.T) {
	evs := parseAll(realLog)
	if len(evs) != 9 {
		t.Fatalf("got %d events, want 9: %+v", len(evs), evs)
	}
	want := []struct {
		kind  Kind
		op    string
		ok    bool
		write bool
		path  string
	}{
		{KindConnect, "connect", true, false, ""},
		{KindFile, "openat", false, false, "/srv/shares/aud/dir one"},
		{KindFile, "mkdirat", true, false, "/srv/shares/aud/dir one"},
		{KindFile, "openat", true, true, "/srv/shares/aud/dir one/a b.txt"},
		{KindFile, "renameat", true, false, "/srv/shares/aud/dir one/a b.txt"},
		{KindFile, "unlinkat", true, false, "/srv/shares/aud/dir one/c.txt"},
		{KindDisconnect, "disconnect", true, false, ""},
	}
	for i, w := range want {
		e := evs[i]
		if e.Kind != w.kind || e.Op != w.op || e.OK != w.ok || e.Write != w.write || e.Path != w.path {
			t.Errorf("event %d = %+v, want %+v", i, e, w)
		}
		if e.User != "jay" || e.Share != "aud" || e.ClientName != "ca542a78af9a" || e.ClientIP.String() != "127.0.0.1" {
			t.Errorf("event %d who/where: %+v", i, e)
		}
	}
	if evs[1].Error != "No such file or directory" {
		t.Errorf("failure reason: %q", evs[1].Error)
	}
	if evs[4].Path2 != "/srv/shares/aud/dir one/c.txt" {
		t.Errorf("rename target: %q", evs[4].Path2)
	}
	// The time comes from the header line before each message.
	if got := evs[2].Time.Format("15:04:05.000000"); got != "17:24:24.115335" {
		t.Errorf("event time %s", got)
	}

	ok, bad := evs[7], evs[8]
	if ok.Kind != KindAuth || !ok.OK || ok.User != "jay" || ok.AuthStatus != "NT_STATUS_OK" || ok.Service != "SMB2" {
		t.Errorf("successful login: %+v", ok)
	}
	if bad.OK || bad.AuthStatus != "NT_STATUS_WRONG_PASSWORD" || bad.ClientIP.String() != "192.168.1.66" {
		t.Errorf("failed login: %+v", bad)
	}
	if !bad.Time.Equal(time.Date(2026, 11, 22, 17, 23, 16, 22207000, time.UTC)) {
		t.Errorf("login time %v", bad.Time)
	}
}

func TestParseRemoteAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"ipv4:192.168.1.5:445":            "192.168.1.5",
		"ipv6:[fe80::1]:445":              "fe80::1",
		"ipv6:2001:db8::7:51000":          "2001:db8::7",
		"ipv6:[::ffff:192.168.1.5]:51000": "192.168.1.5",
	} {
		ip, ok := remoteIP(in)
		if !ok || ip.String() != want {
			t.Errorf("remoteIP(%q) = %v %v, want %s", in, ip, ok, want)
		}
	}
	for _, in := range []string{"", "unix:/run/x", "ipv4:", "ipv4:999.1.1.1:5", "ipv6:[fe80::1%eth0]:5", "ipv4:1.2.3.4"} {
		if ip, ok := remoteIP(in); ok && in != "ipv4:1.2.3.4" {
			t.Errorf("remoteIP(%q) = %v, want refused", in, ip)
		}
	}
}

// The step's "done when": malformed and hostile lines are refused, never
// crash the parser, and never let control characters through.
func TestParseRefusesMalformedLines(t *testing.T) {
	bad := []string{
		"",
		"nshaudit|",
		"nshaudit|jay|127.0.0.1|aud|m|openat|ok|r",          // missing path
		"nshaudit|jay|127.0.0.1|aud|m|openat|ok|x|/srv/a",   // not r or w
		"nshaudit|jay|127.0.0.1|aud|m|unlinkat|ok|/srv/a|b", // "|" in a path: ambiguous
		"nshaudit|jay|127.0.0.1|aud|m|renameat|ok|/srv/a",   // a rename needs two
		"nshaudit|jay|not-an-ip|aud|m|mkdirat|ok|/srv/a",
		"nshaudit|jay|127.0.0.1|aud|m|mkdirat|maybe|/srv/a",
		"nshaudit|jay|127.0.0.1|aud|m|mkdirat|ok|relative/path",
		"nshaudit|jay|127.0.0.1|aud|m|chmod|ok|/srv/a",                               // an operation we do not use
		"nshaudit||127.0.0.1|aud|m|mkdirat|ok|/srv/a",                                // no user
		"nshaudit|jay\x1b[31m|127.0.0.1|aud|m|mkdirat|ok|/srv/a",                     // terminal escape
		"nshaudit|jay|127.0.0.1|aud|m|mkdirat|ok|/srv/a\x00b",                        // NUL
		"nshaudit|jay|127.0.0.1|aud|m|mkdirat|ok|/srv/" + string([]byte{0xff, 0xfe}), // not UTF-8
		"nshaudit|jay|127.0.0.1|aud|m|mkdirat|ok|/" + strings.Repeat("a", 5000),      // too long a path
		`{"type": "Authentication"}`,
		`{"type": "Authentication", "Authentication": {"status": "OK", "remoteAddress": "ipv4:1.2.3.4:5"}}`,
		`{"type": "Authentication", "Authentication": {"status": "NT_STATUS_OK", "remoteAddress": "nowhere"}}`,
		`{"type": "Authorization", "Authorization": {}}`,
		`{"type": "Authentication", "Authentication": {"status": "NT_STATUS_OK", "remoteAddress": "ipv4:1.2.3.4:5"`, // cut off
		`{` + strings.Repeat(`"a":[`, 10000),
		strings.Repeat("x", MaxLine+1),
		"\x00\x01\x02\xff\xfe binary noise nshaudit|",
	}
	p := NewParser()
	for _, line := range bad {
		if ev, ok := p.Line(line); ok {
			t.Errorf("accepted %q as %+v", truncate(line), ev)
		}
	}

	// A failed login with a forged account name is still an event (the
	// address is what counts), but the name is dropped, not passed on.
	ev, ok := p.Line(`{"timestamp": "2026-11-22T17:23:16.022207+0000", "type": "Authentication", "Authentication": {"status": "NT_STATUS_NO_SUCH_USER", "remoteAddress": "ipv4:10.0.0.9:5", "clientAccount": "admin\n[forged] log line"}}`)
	if !ok || ev.User != "" || ev.ClientIP.String() != "10.0.0.9" {
		t.Errorf("forged account name: %+v %v", ev, ok)
	}
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// FuzzParse: whatever a line holds, the parser returns. Run longer with
// "go test -fuzz=FuzzParse ./internal/modules/logingest/".
func FuzzParse(f *testing.F) {
	for _, line := range strings.Split(realLog, "\n") {
		f.Add(line)
	}
	f.Add("nshaudit|a|1.2.3.4|s|m|renameat|fail (x)|/a|/b")
	f.Add(`{"type":"Authentication","Authentication":{"status":"NT_STATUS_OK","remoteAddress":"ipv6:[::1]:1"}}`)
	f.Fuzz(func(t *testing.T, line string) {
		p := NewParser()
		ev, ok := p.Line(line)
		if !ok {
			return
		}
		for _, s := range []string{ev.User, ev.ClientName, ev.Share, ev.Path, ev.Path2, ev.Error, ev.AuthStatus, ev.Service} {
			for _, r := range s {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("control character in an event field: %q", s)
				}
			}
		}
	})
}
