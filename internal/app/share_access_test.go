package app_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysfake"
)

// Step 1.6a: presets, read-only and guest shares, drop boxes, the
// effective-access preview, and the job that fixes existing files.

type effectiveBody struct {
	Users []struct {
		Username string
		Given    string
		Level    string
		Reason   string
	}
	Guests string
	Others string
	Note   string
}

func (e effectiveBody) level(name string) string {
	for _, u := range e.Users {
		if u.Username == name {
			return u.Level
		}
	}
	return "missing"
}

// section returns one share's part of the generated config.
func section(t *testing.T, fake *sysfake.Fake, name string) string {
	t.Helper()
	config := fake.CurrentConfig() // background work may be applying a new one
	start := strings.Index(config, "["+name+"]")
	if start < 0 {
		t.Fatalf("share %s is not in the config:\n%s", name, config)
	}
	rest := config[start+1:]
	if end := strings.Index(rest, "\n["); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// waitFixed waits until the fix-permissions job has run for a share n times.
func waitFixed(t *testing.T, fake *sysfake.Fake, share string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		for _, s := range fake.Fixed() {
			if s == share {
				count++
			}
		}
		if count >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fix-permissions did not run %d time(s) for %s; ran for %v", n, share, fake.Fixed())
}

func TestSharePresetsAndPreview(t *testing.T) {
	admin, _, _ := newSystemServer(t)
	jay := createUser(t, admin, "jay")
	ana := createUser(t, admin, "ana")
	bob := createUser(t, admin, "bob")
	admin.do("PATCH", "/users/"+itoa(bob.ID), map[string]any{"disabled": true}, nil)

	var presets struct {
		Items []struct {
			ID           string
			ReadOnly     bool
			GuestAccess  bool
			DefaultLevel string
		}
	}
	status, _ := admin.do("GET", "/shares/presets", nil, &presets)
	want(t, "presets status", status, 200)
	var ids []string
	for _, p := range presets.Items {
		ids = append(ids, p.ID)
	}
	want(t, "presets", strings.Join(ids, ","), "private,team,public-read,dropbox,media,read-only")

	access := []map[string]any{
		{"userId": jay.ID, "level": "write"},
		{"userId": ana.ID, "level": "upload"},
		{"userId": bob.ID, "level": "read"},
	}

	var e effectiveBody
	status, _ = admin.do("POST", "/shares/effective-access", map[string]any{"access": access}, &e)
	want(t, "preview status", status, 200)
	want(t, "jay", e.level("jay"), "write")
	want(t, "ana", e.level("ana"), "upload")
	want(t, "bob (disabled)", e.level("bob"), "none")
	want(t, "guests", e.Guests, "none")
	want(t, "others", e.Others, "none")

	// A read-only share overrides everyone: writers read, uploads stop.
	e = effectiveBody{}
	admin.do("POST", "/shares/effective-access", map[string]any{"access": access, "readOnly": true}, &e)
	want(t, "jay on read-only", e.level("jay"), "read")
	want(t, "ana on read-only", e.level("ana"), "none")
	if !strings.Contains(e.Users[0].Reason, "read-only") {
		t.Errorf("reason should say why: %q", e.Users[0].Reason)
	}

	// Guest access: everyone else reads.
	e = effectiveBody{}
	admin.do("POST", "/shares/effective-access", map[string]any{"guestAccess": true}, &e)
	want(t, "guests on guest share", e.Guests, "read")
	want(t, "others on guest share", e.Others, "read")

	// The preview validates like a save does.
	var p problem
	status, _ = admin.do("POST", "/shares/effective-access", map[string]any{
		"access": []map[string]any{{"userId": 1, "level": "read"}},
	}, &p)
	want(t, "preview of invalid access", status, 422)
	want(t, "preview field", fieldCodes(p)["access[0].userId"], "no_smb_access")
}

func TestShareAccessModes(t *testing.T) {
	admin, fake, base := newSystemServer(t)
	jay := createUser(t, admin, "jay")
	ana := createUser(t, admin, "ana")

	// --- drop box: ana adds files, jay collects them ---
	var inbox shareBody
	status, _ := admin.do("POST", "/shares", map[string]any{
		"name": "inbox", "path": "/srv/inbox", "preset": "dropbox",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}, {"userId": ana.ID, "level": "upload"}},
	}, &inbox)
	want(t, "create drop box", status, 201)
	sec := section(t, fake, "inbox")
	for _, line := range []string{"valid users = jay, ana", "write list = jay, ana", "# upload only: ana", "inherit acls = yes"} {
		if !strings.Contains(sec, line) {
			t.Errorf("drop box config is missing %q:\n%s", line, sec)
		}
	}
	// The folder already existed, so its files get the permissions too.
	waitFixed(t, fake, "inbox", 1)

	// --- read-only overrides everyone ---
	var updated struct {
		ReadOnly bool
		Preset   string
	}
	status, _ = admin.do("PATCH", "/shares/"+itoa(inbox.ID), map[string]any{"readOnly": true, "preset": "read-only"}, &updated)
	want(t, "make read-only", status, 200)
	want(t, "readOnly", updated.ReadOnly, true)
	want(t, "preset", updated.Preset, "read-only")
	sec = section(t, fake, "inbox")
	if strings.Contains(sec, "write list") || strings.Contains(sec, "ana") {
		t.Errorf("read-only share: no writers, and no uploads (ana):\n%s", sec)
	}
	waitFixed(t, fake, "inbox", 2) // access changed: fixed again

	// A comment change does not touch anyone's access: no new job.
	admin.do("PATCH", "/shares/"+itoa(inbox.ID), map[string]any{"comment": "Letters"}, nil)
	time.Sleep(200 * time.Millisecond)
	if n := len(slices.DeleteFunc(fake.Fixed(), func(s string) bool { return s != "inbox" })); n != 2 {
		t.Errorf("a comment change queued a permission job (%d runs)", n)
	}

	// --- guest share: anyone reads, no user list ---
	var tv shareBody
	status, _ = admin.do("POST", "/shares", map[string]any{
		"name": "tv", "path": "/missing/tv", "createDirectory": true, "guestAccess": true, "preset": "media",
	}, &tv)
	want(t, "create guest share", status, 201)
	sec = section(t, fake, "tv")
	if !strings.Contains(sec, "guest ok = yes") || strings.Contains(sec, "available = no") || strings.Contains(sec, "valid users") {
		t.Errorf("guest share config:\n%s", sec)
	}

	// --- the user portal shows what each user can effectively do ---
	mine := func(name string) map[string]string {
		c := newClient(t, base)
		c.login(name, pw)
		var list struct {
			Items []struct{ Name, Level string }
		}
		c.do("GET", "/my/shares", nil, &list)
		out := map[string]string{}
		for _, s := range list.Items {
			out[s.Name] = s.Level
		}
		return out
	}
	if got := mine("jay"); got["inbox"] != "read" || got["tv"] != "read" || len(got) != 2 {
		t.Errorf("jay's shares = %v, want inbox:read (read-only) and tv:read (guest)", got)
	}
	if got := mine("ana"); got["inbox"] != "" || got["tv"] != "read" || len(got) != 1 {
		t.Errorf("ana's shares = %v, want only tv:read (uploads blocked on a read-only share)", got)
	}

	// --- shares cannot overlap: one folder, one set of permissions ---
	for _, path := range []string{"/srv/inbox/sub", "/srv"} {
		var p problem
		status, _ = admin.do("POST", "/shares", map[string]any{"name": "nested", "path": path}, &p)
		if status != 422 || fieldCodes(p)["path"] != "overlaps" {
			t.Errorf("%s: status %d, errors %+v; want 422 path=overlaps", path, status, p.Errors)
		}
	}

	// --- the "Fix permissions" button queues a job the admin can follow ---
	var job struct {
		ID, Kind, Status, Title string
	}
	status, _ = admin.do("POST", "/shares/"+itoa(inbox.ID)+"/fix-permissions", nil, &job)
	want(t, "fix permissions status", status, 202)
	want(t, "job kind", job.Kind, "shares.fix-permissions")
	waitFixed(t, fake, "inbox", 3)
	deadline := time.Now().Add(5 * time.Second)
	for job.Status != "succeeded" && time.Now().Before(deadline) {
		admin.do("GET", "/jobs/"+job.ID, nil, &job)
		time.Sleep(20 * time.Millisecond)
	}
	want(t, "job status", job.Status, "succeeded")

	// --- a folder on a filesystem without ACLs: refused, with a hint ---
	fake.FailNext("ApplyConfig", &sys.ACLError{Share: "nfs", Path: "/srv/nfs", Err: sys.ErrACLUnsupported})
	var p problem
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "nfs", "path": "/srv/nfs"}, &p)
	want(t, "no ACL support status", status, 422)
	want(t, "no ACL support code", p.Code, "SHARE_PERMISSIONS_UNSUPPORTED")
	if !strings.Contains(p.Detail, "acltype=posixacl") {
		t.Errorf("the message should say how to fix it: %q", p.Detail)
	}
	if strings.Contains(fake.Config, "[nfs]") {
		t.Error("a refused share must not be in the config")
	}

	// --- an unknown preset or level is refused ---
	p = problem{}
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "x", "path": "/srv/x", "preset": "everyone"}, &p)
	want(t, "unknown preset", status, 422)
	want(t, "unknown preset field", fieldCodes(p)["preset"], "invalid_choice")
}

// Upload-only users on a guest share would be a false promise: everyone can
// read a guest share, the uploads included.
func TestUploadsRefusedOnGuestShares(t *testing.T) {
	admin, fake, _ := newSystemServer(t)
	jay := createUser(t, admin, "jay")
	upload := []map[string]any{{"userId": jay.ID, "level": "upload"}}

	for _, c := range []struct {
		method, path string
		body         map[string]any
	}{
		{"POST", "/shares", map[string]any{"name": "pub", "path": "/srv/pub", "guestAccess": true, "access": upload}},
		{"POST", "/shares/effective-access", map[string]any{"guestAccess": true, "access": upload}},
	} {
		var p problem
		status, _ := admin.do(c.method, c.path, c.body, &p)
		if status != 422 || fieldCodes(p)["access[0].level"] != "not_with_guest" {
			t.Errorf("%s %s: status %d, errors %+v", c.method, c.path, status, p.Errors)
		}
	}

	// Turning guest access on for an existing drop box is refused too.
	var inbox shareBody
	admin.do("POST", "/shares", map[string]any{"name": "inbox", "path": "/srv/inbox", "access": upload}, &inbox)
	var p problem
	status, _ := admin.do("PATCH", "/shares/"+itoa(inbox.ID), map[string]any{"guestAccess": true}, &p)
	want(t, "guest on a drop box", status, 422)
	want(t, "guest on a drop box field", fieldCodes(p)["access[0].level"], "not_with_guest")
	if strings.Contains(section(t, fake, "inbox"), "guest ok = yes") {
		t.Error("a refused change reached the config")
	}
}

// Step 1.7: shares are hidden from users who cannot open them, and "hide
// unreadable" is a per-share switch.
func TestShareVisibilitySettings(t *testing.T) {
	admin, fake, _ := newSystemServer(t)
	jay := createUser(t, admin, "jay")

	var sh struct {
		ID             int64
		HideUnreadable bool
	}
	status, _ := admin.do("POST", "/shares", map[string]any{
		"name": "hr", "path": "/srv/hr", "access": []map[string]any{{"userId": jay.ID, "level": "read"}},
	}, &sh)
	want(t, "create", status, 201)
	want(t, "hide unreadable off by default", sh.HideUnreadable, false)
	sec := section(t, fake, "hr")
	if !strings.Contains(sec, "access based share enum = yes") || strings.Contains(sec, "hide unreadable") {
		t.Errorf("config:\n%s", sec)
	}

	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"hideUnreadable": true}, &sh)
	want(t, "turn on", status, 200)
	want(t, "hideUnreadable", sh.HideUnreadable, true)
	if !strings.Contains(section(t, fake, "hr"), "hide unreadable = yes") {
		t.Errorf("config:\n%s", fake.Config)
	}
}

// Step 1.8: per-share network rules, strictly validated.
func TestShareNetworkRules(t *testing.T) {
	admin, fake, _ := newSystemServer(t)
	jay := createUser(t, admin, "jay")
	access := []map[string]any{{"userId": jay.ID, "level": "write"}}

	var sh struct {
		ID           int64
		NetworkRules []struct{ Action, Address string }
	}
	status, _ := admin.do("POST", "/shares", map[string]any{
		"name": "finance", "path": "/srv/finance", "access": access,
		"networkRules": []map[string]any{
			{"action": "allow", "address": " 192.168.1.0/24 "},
			{"action": "allow", "address": "10.0.0.7/32"},
			{"action": "block", "address": "192.168.1.66"},
		},
	}, &sh)
	want(t, "create", status, 201)
	// Stored as Samba gets them: trimmed, a one-address range as an address.
	want(t, "rule 0", sh.NetworkRules[0].Address, "192.168.1.0/24")
	want(t, "rule 1", sh.NetworkRules[1].Address, "10.0.0.7")
	if !strings.Contains(section(t, fake, "finance"), "hosts allow = 192.168.1.0/24, 10.0.0.7 EXCEPT 192.168.1.66") {
		t.Errorf("config:\n%s", fake.Config)
	}

	applied := fake.Applied
	for _, c := range []struct {
		rules []map[string]any
		field string
		code  string
	}{
		{[]map[string]any{{"action": "allow", "address": "nas.local"}}, "networkRules[0].address", "invalid_format"},
		{[]map[string]any{{"action": "allow", "address": "ALL"}}, "networkRules[0].address", "invalid_format"},
		{[]map[string]any{{"action": "block", "address": "10.0.0.0/8 EXCEPT 10.0.0.1"}}, "networkRules[0].address", "invalid_format"},
		{[]map[string]any{{"action": "allow", "address": "192.168.1.5/24"}}, "networkRules[0].address", "invalid_format"},
		{[]map[string]any{{"action": "allow", "address": "10.0.0.1"}, {"action": "allow", "address": "10.0.0.1/32"}}, "networkRules[1].address", "duplicate"},
		{[]map[string]any{{"action": "allow", "address": "10.0.0.1"}, {"action": "block", "address": "10.0.0.1"}}, "networkRules[1].address", "conflict"},
	} {
		var p problem
		status, _ := admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"networkRules": c.rules}, &p)
		if status != 422 || fieldCodes(p)[c.field] != c.code {
			t.Errorf("%v: status %d, errors %+v; want %s=%s", c.rules, status, p.Errors, c.field, c.code)
		}
	}
	want(t, "config untouched by invalid rules", fake.Applied, applied)

	// The error suggests the range that was probably meant.
	var p problem
	admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{
		"networkRules": []map[string]any{{"action": "allow", "address": "192.168.1.5/24"}},
	}, &p)
	if len(p.Errors) == 0 || !strings.Contains(p.Errors[0].Message, "192.168.1.0/24") {
		t.Errorf("want a suggestion, got %+v", p.Errors)
	}

	// Block-only rules become "hosts deny"; an empty list removes them.
	admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{
		"networkRules": []map[string]any{{"action": "block", "address": "2001:DB8::/32"}},
	}, nil)
	if !strings.Contains(section(t, fake, "finance"), "hosts deny = 2001:db8::/32") {
		t.Errorf("config:\n%s", fake.Config)
	}
	admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"networkRules": []map[string]any{}}, nil)
	if strings.Contains(section(t, fake, "finance"), "hosts ") {
		t.Errorf("rules should be gone:\n%s", fake.Config)
	}

	// The preview says it in words.
	var withNetwork struct{ Network string }
	admin.do("POST", "/shares/effective-access", map[string]any{
		"access": access,
		"networkRules": []map[string]any{
			{"action": "allow", "address": "192.168.1.0/24"}, {"action": "block", "address": "192.168.1.66"},
		},
	}, &withNetwork)
	want(t, "preview network", withNetwork.Network, "Only from 192.168.1.0/24, except 192.168.1.66.")
}
