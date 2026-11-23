// Package sambalog reads Samba's logs and turns them into events for the
// features that react to what happens over SMB: the activity log (2.2a),
// the ransomware guard (2.3) and brute-force protection (2.4). There is ONE
// parser and ONE reader; features subscribe instead of reading logs
// themselves.
//
// Log lines are untrusted input: a client chooses the user name it tries to
// log in with, and file names. So the parser never panics (it also recovers,
// as a second guard), never allocates more than a bounded amount for a line,
// keeps only printable text, and refuses a line rather than guessing.
package sambalog

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind says what an event is about.
type Kind string

const (
	KindFile       Kind = "file"       // a file operation (vfs_full_audit)
	KindConnect    Kind = "connect"    // a user opened a share
	KindDisconnect Kind = "disconnect" // and closed it
	KindAuth       Kind = "auth"       // a login attempt (auth_json_audit)
)

// Event is one thing that happened over SMB.
type Event struct {
	Time time.Time `json:"time"`
	Kind Kind      `json:"kind"`
	// User is the account: the Unix name for file events, the name the
	// client sent for logins (anything, for a failed one).
	User       string     `json:"user,omitempty"`
	ClientIP   netip.Addr `json:"clientIp"`
	ClientName string     `json:"clientName,omitempty"` // the client's NetBIOS name (file events)
	Share      string     `json:"share,omitempty"`
	// Op is the operation (file events): openat, mkdirat, renameat,
	// unlinkat, pwrite...
	Op    string `json:"op,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"` // why it failed, as Samba said it
	// Write is true for an openat that opened for writing.
	Write bool `json:"write,omitempty"`
	// Path is absolute, as Samba logs it; Path2 is the new name of a rename.
	Path  string `json:"path,omitempty"`
	Path2 string `json:"path2,omitempty"`
	// AuthStatus is Samba's NT status for a login ("NT_STATUS_OK",
	// "NT_STATUS_WRONG_PASSWORD"...).
	AuthStatus string `json:"authStatus,omitempty"`
	Service    string `json:"service,omitempty"` // for logins: "SMB2", "SMB", ...
}

// AuditMarker starts every line vfs_full_audit writes for NetShare Hub. The
// managed config sets the prefix (2.2a):
//
//	full_audit:prefix = nshaudit|%u|%I|%S|%m
//
// so a line is: nshaudit|user|ip|share|machine|op|ok or fail (reason)|args.
// SMB does not allow "|" in file names, but paths can still contain it (a
// file made on the server itself), so a line with more fields than its
// operation takes is refused rather than split wrongly.
const AuditMarker = "nshaudit|"

const (
	// MaxLine is the longest line looked at. Samba's lines are far shorter;
	// anything longer is not one of ours.
	MaxLine = 16 << 10
	maxText = 4096 // longest kept text field (a path)
	maxName = 256  // longest kept name (user, machine)
)

// Parser turns lines into events. It keeps one piece of state: the time
// from Samba's last debug header line ("[2026/11/22 17:24:24.114062,  1]
// ..."), which belongs to the message line that follows it.
type Parser struct {
	now  func() time.Time
	last time.Time
}

func NewParser() *Parser { return &Parser{now: time.Now} }

var debugHeader = regexp.MustCompile(`^\[(\d{4}/\d\d/\d\d \d\d:\d\d:\d\d(?:\.\d{1,9})?),\s*\d+[^\]]*\]`)

// Line parses one line. ok is false for anything that is not an event:
// headers, other log messages, and malformed or hostile lines.
func (p *Parser) Line(line string) (ev Event, ok bool) {
	defer func() {
		// A bug here must never stop ingestion: drop the line.
		if recover() != nil {
			ev, ok = Event{}, false
		}
	}()
	if len(line) > MaxLine {
		return Event{}, false
	}
	if m := debugHeader.FindStringSubmatch(line); m != nil {
		if t, err := time.ParseInLocation("2006/01/02 15:04:05.999999999", m[1], time.Local); err == nil {
			p.last = t
		}
		return Event{}, false
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "{") {
		return p.auth(trimmed)
	}
	if i := strings.Index(line, AuditMarker); i >= 0 {
		return p.audit(line[i+len(AuditMarker):])
	}
	return Event{}, false
}

func (p *Parser) eventTime() time.Time {
	if !p.last.IsZero() {
		t := p.last
		p.last = time.Time{} // one header, one message
		return t
	}
	return p.now()
}

// argsFor is how many argument fields each audited operation has.
var argsFor = map[string]int{
	"connect": 1, "disconnect": 1, // the share
	"openat":      2, // "r" or "w", the path
	"mkdirat":     1,
	"unlinkat":    1,
	"renameat":    2, // from, to
	"pwrite":      1,
	"pwrite_send": 1,
}

func (p *Parser) audit(rest string) (Event, bool) {
	// user|ip|share|machine|op|status|args...
	f := strings.Split(strings.TrimRight(rest, "\r\n "), "|")
	if len(f) < 7 {
		return Event{}, false
	}
	ev := Event{Time: p.eventTime(), Kind: KindFile, Op: f[4]}
	n, known := argsFor[ev.Op]
	if !known || len(f) != 6+n {
		return Event{}, false // an operation we do not use, or "|" in a path
	}
	var ok bool
	if ev.User, ok = name(f[0]); !ok || ev.User == "" {
		return Event{}, false
	}
	ip, err := netip.ParseAddr(f[1])
	if err != nil {
		return Event{}, false
	}
	ev.ClientIP = ip.Unmap()
	if ev.Share, ok = name(f[2]); !ok || ev.Share == "" {
		return Event{}, false
	}
	if ev.ClientName, ok = name(f[3]); !ok {
		return Event{}, false
	}
	switch status := f[5]; {
	case status == "ok":
		ev.OK = true
	case strings.HasPrefix(status, "fail"):
		ev.Error = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(status, "fail"), " ("), ")")
		ev.Error, _ = text(ev.Error, maxName)
	default:
		return Event{}, false
	}
	args := f[6:]
	switch ev.Op {
	case "connect", "disconnect":
		ev.Kind = Kind(ev.Op)
		return ev, true
	case "openat":
		if args[0] != "r" && args[0] != "w" {
			return Event{}, false
		}
		ev.Write = args[0] == "w"
		args = args[1:]
	}
	if ev.Path, ok = text(args[0], maxText); !ok || !strings.HasPrefix(ev.Path, "/") {
		return Event{}, false
	}
	if len(args) > 1 {
		if ev.Path2, ok = text(args[1], maxText); !ok || !strings.HasPrefix(ev.Path2, "/") {
			return Event{}, false
		}
	}
	return ev, true
}

// authLine is the part of Samba's auth_json_audit record that is used.
type authLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Auth      *struct {
		Status        string `json:"status"`
		RemoteAddress string `json:"remoteAddress"`
		ClientAccount string `json:"clientAccount"`
		Service       string `json:"serviceDescription"`
	} `json:"Authentication"`
}

func (p *Parser) auth(line string) (Event, bool) {
	var a authLine
	if err := json.Unmarshal([]byte(line), &a); err != nil || a.Type != "Authentication" || a.Auth == nil {
		return Event{}, false
	}
	ev := Event{Kind: KindAuth}
	var ok bool
	if ev.AuthStatus, ok = text(a.Auth.Status, 64); !ok || !strings.HasPrefix(ev.AuthStatus, "NT_STATUS_") {
		return Event{}, false
	}
	ev.OK = ev.AuthStatus == "NT_STATUS_OK"
	ip, ok := remoteIP(a.Auth.RemoteAddress)
	if !ok {
		return Event{}, false
	}
	ev.ClientIP = ip
	// The account name is whatever the client typed: keep it, as text.
	ev.User, _ = text(a.Auth.ClientAccount, maxName)
	ev.Service, _ = text(a.Auth.Service, 32)
	if t, err := time.Parse("2006-01-02T15:04:05.999999-0700", a.Timestamp); err == nil {
		ev.Time = t
	} else {
		ev.Time = p.eventTime()
	}
	return ev, true
}

// remoteIP reads Samba's "ipv4:1.2.3.4:5678" or "ipv6:[::1]:5678" (also
// without the brackets, "ipv6:::1:5678").
func remoteIP(s string) (netip.Addr, bool) {
	switch {
	case strings.HasPrefix(s, "ipv4:"):
		s = s[len("ipv4:"):]
		if i := strings.LastIndex(s, ":"); i >= 0 {
			s = s[:i]
		}
	case strings.HasPrefix(s, "ipv6:"):
		s = s[len("ipv6:"):]
		if strings.HasPrefix(s, "[") {
			if i := strings.Index(s, "]"); i > 0 {
				s = s[1:i]
			}
		} else if i := strings.LastIndex(s, ":"); i >= 0 {
			s = s[:i]
		}
	default:
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(s)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// text keeps a field only if it is valid UTF-8 of printable characters and
// not too long. Control characters (a newline in a forged user name, a
// terminal escape) never get through.
func text(s string, max int) (string, bool) {
	if len(s) > max || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return s, true
}

// name is text for names: no spaces at either end.
func name(s string) (string, bool) {
	s, ok := text(s, maxName)
	return s, ok && strings.TrimSpace(s) == s
}
