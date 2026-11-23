package app_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysfake"
)

// These tests cover the parts that touch the operating system (through the
// fake): user accounts stay in step with the database, and the Samba config
// always matches the shares.

const pw = "correct-horse"

// newSystemServer starts the app with a fake system the test can inspect,
// and returns a logged-in admin client.
func newSystemServer(t *testing.T) (*client, *sysfake.Fake, string) {
	t.Helper()
	admin, fake, base, _ := newSystemServerWith(t, app.Options{})
	return admin, fake, base
}

// newSystemServerWith is newSystemServer with app options, and the app.
func newSystemServerWith(t *testing.T, opts app.Options) (*client, *sysfake.Fake, string, *app.App) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	cfg.Sys = "fake"

	fake := sysfake.New()
	system := fake.System()
	opts.System = &system
	application, err := app.New(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	server := httptest.NewServer(application.Handler)
	t.Cleanup(func() {
		server.Close()
		application.Close()
	})
	base := server.URL + app.APIPrefix

	admin := newClient(t, base)
	var s session
	if status, _ := admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s); status != 201 {
		t.Fatalf("setup: status %d", status)
	}
	admin.csrf = s.CSRFToken
	return admin, fake, base, application
}

type userBody struct {
	ID        int64
	Username  string
	SMBAccess bool
	Disabled  bool
}

func createUser(t *testing.T, c *client, name string) userBody {
	t.Helper()
	var u userBody
	status, _ := c.do("POST", "/users", map[string]any{"username": name, "role": "user", "password": pw}, &u)
	if status != 201 {
		t.Fatalf("create %s: status %d", name, status)
	}
	return u
}

func userCount(t *testing.T, c *client) int {
	t.Helper()
	var list struct{ Page struct{ Total int } }
	c.do("GET", "/users", nil, &list)
	return list.Page.Total
}

func fieldCodes(p problem) map[string]string {
	out := map[string]string{}
	for _, e := range p.Errors {
		out[e.Field] = e.Code
	}
	return out
}

func TestUserAccountSync(t *testing.T) {
	admin, fake, base := newSystemServer(t)

	// The setup admin is web-only: no system account is created for it.
	if fake.Account("admin") != nil {
		t.Error("the setup admin must not get a system account")
	}

	// Creating a user creates the system account with the same password.
	jay := createUser(t, admin, "jay")
	want(t, "smbAccess", jay.SMBAccess, true)
	acc := fake.Account("jay")
	if acc == nil || acc.Password != pw || !acc.Enabled {
		t.Fatalf("system account for jay = %+v", acc)
	}

	// A name that belongs to a system account is refused, and no user is left behind.
	var p problem
	status, _ := admin.do("POST", "/users", map[string]any{"username": "root", "role": "user", "password": pw}, &p)
	want(t, "system name status", status, 422)
	want(t, "system name field", fieldCodes(p)["username"], "reserved")
	want(t, "users after refused create", userCount(t, admin), 2)

	// If the system step fails, the database row is rolled back.
	fake.FailNext("CreateUser", errors.New("useradd: cannot lock /etc/passwd"))
	p = problem{}
	status, _ = admin.do("POST", "/users", map[string]any{"username": "ana", "role": "user", "password": pw}, &p)
	want(t, "failed create status", status, 503)
	want(t, "failed create code", p.Code, "SYSTEM_SYNC_FAILED")
	if strings.Contains(p.Detail, "passwd") {
		t.Errorf("internal error text leaked to the client: %q", p.Detail)
	}
	want(t, "users after failed create", userCount(t, admin), 2)

	// A password with a line break could confuse the system tools: refused.
	p = problem{}
	status, _ = admin.do("POST", "/users", map[string]any{"username": "ana", "role": "user", "password": "line-one\nline-two"}, &p)
	want(t, "newline password status", status, 422)
	want(t, "newline password field", fieldCodes(p)["password"], "invalid_format")

	// Changing the password changes both sides.
	path := "/users/" + itoa(jay.ID)
	status, _ = admin.do("PATCH", path, map[string]any{"password": "brand-new-pass"}, nil)
	want(t, "password change status", status, 200)
	want(t, "system password", fake.Account("jay").Password, "brand-new-pass")
	newClient(t, base).login("jay", "brand-new-pass")

	// If the system step fails, the old password keeps working everywhere.
	fake.FailNext("SetPassword", errors.New("smbpasswd failed"))
	status, _ = admin.do("PATCH", path, map[string]any{"password": "never-applied-1"}, nil)
	want(t, "failed password change status", status, 503)
	want(t, "system password after failure", fake.Account("jay").Password, "brand-new-pass")
	newClient(t, base).login("jay", "brand-new-pass") // fails the test if the old password stopped working

	// Disabling blocks SMB logins too.
	admin.do("PATCH", path, map[string]any{"disabled": true}, nil)
	want(t, "system account enabled", fake.Account("jay").Enabled, false)
	admin.do("PATCH", path, map[string]any{"disabled": false}, nil)
	want(t, "system account re-enabled", fake.Account("jay").Enabled, true)

	// Turning SMB access off removes the system account.
	status, _ = admin.do("PATCH", path, map[string]any{"smbAccess": false}, nil)
	want(t, "smb off status", status, 200)
	if fake.Account("jay") != nil {
		t.Error("system account should be gone after turning SMB access off")
	}

	// Turning it back on needs the password (we only store a hash).
	p = problem{}
	status, _ = admin.do("PATCH", path, map[string]any{"smbAccess": true}, &p)
	want(t, "smb on without password", status, 422)
	want(t, "smb on field", fieldCodes(p)["password"], "required")
	status, _ = admin.do("PATCH", path, map[string]any{"smbAccess": true, "password": "third-password"}, nil)
	want(t, "smb on status", status, 200)
	want(t, "system password after re-enable", fake.Account("jay").Password, "third-password")

	// Deleting the user removes the system account.
	status, _ = admin.do("DELETE", path, nil, nil)
	want(t, "delete status", status, 204)
	if fake.Account("jay") != nil {
		t.Error("system account should be gone after deleting the user")
	}
}

type shareBody struct {
	ID     int64
	Name   string
	Path   string
	Access []struct {
		UserID   int64
		Username string
		Level    string
	}
}

func TestShares(t *testing.T) {
	admin, fake, base := newSystemServer(t)
	jay := createUser(t, admin, "jay")
	ana := createUser(t, admin, "ana")

	// --- create: the Samba config is generated and applied ---
	var media shareBody
	status, _ := admin.do("POST", "/shares", map[string]any{
		"name": "media", "path": "/srv/media", "comment": "Films",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}, {"userId": ana.ID, "level": "read"}},
	}, &media)
	want(t, "create share status", status, 201)
	want(t, "access entries", len(media.Access), 2)
	for _, line := range []string{"[media]", "path = /srv/media", "comment = Films", "read only = yes", "write list = jay"} {
		if !strings.Contains(fake.Config, line) {
			t.Errorf("config is missing %q:\n%s", line, fake.Config)
		}
	}
	if !strings.Contains(fake.Config, "valid users = ana, jay") && !strings.Contains(fake.Config, "valid users = jay, ana") {
		t.Errorf("config has the wrong valid users:\n%s", fake.Config)
	}

	// --- validation: every bad input names its field ---
	bad := []struct {
		body  map[string]any
		field string
		code  string
	}{
		{map[string]any{"name": "global", "path": "/srv/x"}, "name", "invalid_format"},
		{map[string]any{"name": "a b", "path": "/srv/x"}, "name", "invalid_format"},
		{map[string]any{"name": "MEDIA", "path": "/srv/x"}, "name", "taken"}, // names are case-insensitive
		{map[string]any{"name": "x", "path": "/etc/samba"}, "path", "not_allowed"},
		{map[string]any{"name": "x", "path": "/srv/%U"}, "path", "invalid_format"},
		{map[string]any{"name": "x", "path": "/srv/../etc"}, "path", "invalid_format"},
		{map[string]any{"name": "x", "path": "relative/path"}, "path", "invalid_format"},
		{map[string]any{"name": "x", "path": "/missing/folder"}, "path", "not_found"},
		{map[string]any{"name": "x", "path": "/srv/x", "comment": "100%"}, "comment", "invalid_format"},
		// The admin is web-only, so it cannot be given SMB access to a share.
		{map[string]any{"name": "x", "path": "/srv/x", "access": []map[string]any{{"userId": 1, "level": "read"}}}, "access[0].userId", "no_smb_access"},
		{map[string]any{"name": "x", "path": "/srv/x", "access": []map[string]any{{"userId": 999, "level": "read"}}}, "access[0].userId", "not_found"},
	}
	applied := fake.Applied
	for _, c := range bad {
		var p problem
		status, _ := admin.do("POST", "/shares", c.body, &p)
		if status != 422 || fieldCodes(p)[c.field] != c.code {
			t.Errorf("%v: status %d, errors %+v; want 422 with %s=%s", c.body, status, p.Errors, c.field, c.code)
		}
	}
	want(t, "config untouched by invalid requests", fake.Applied, applied)

	// "Create folder" makes a missing folder.
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "new", "path": "/missing/folder", "createDirectory": true}, nil)
	want(t, "create with folder status", status, 201)

	// A share nobody has access to is switched off, never open to everyone.
	section := fake.Config[strings.Index(fake.Config, "[new]"):]
	if !strings.Contains(section, "available = no") || strings.Contains(section, "valid users") {
		t.Errorf("share without users must be unavailable:\n%s", section)
	}

	// --- Samba rejects the config: nothing changes anywhere ---
	before := fake.Config
	fake.FailNext("ApplyConfig", &sys.ConfigError{Output: "Unknown parameter encountered"})
	var p problem
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "broken", "path": "/srv/broken"}, &p)
	want(t, "rejected config status", status, 500)
	if strings.Contains(p.Detail, "Unknown parameter") {
		t.Errorf("internal error text leaked to the client: %q", p.Detail)
	}
	want(t, "config after rejection", fake.Config, before)
	var list struct{ Page struct{ Total int } }
	admin.do("GET", "/shares", nil, &list)
	want(t, "shares after rejection", list.Page.Total, 2)

	// --- reload fails: saved on both sides, and the caller is told ---
	fake.FailNext("ApplyConfig", sys.ErrReloadFailed)
	p = problem{}
	status, _ = admin.do("PATCH", "/shares/"+itoa(media.ID), map[string]any{"comment": "Films and music"}, &p)
	want(t, "reload failure status", status, 503)
	want(t, "reload failure code", p.Code, "SAMBA_RELOAD_FAILED")
	var again shareBody
	admin.do("GET", "/shares/"+itoa(media.ID), nil, &again)
	if again.ID != media.ID {
		t.Error("share should still exist after a reload failure")
	}

	// --- update replaces the access list ---
	status, _ = admin.do("PATCH", "/shares/"+itoa(media.ID), map[string]any{
		"access": []map[string]any{{"userId": ana.ID, "level": "write"}},
	}, &again)
	want(t, "update status", status, 200)
	mediaSection := fake.Config[strings.Index(fake.Config, "[media]"):]
	if strings.Contains(mediaSection[:strings.Index(mediaSection, "[new]")], "jay") {
		t.Errorf("jay should be gone from the share:\n%s", fake.Config)
	}

	// --- removing a user takes them off every share BEFORE the account goes ---
	// If Samba cannot be updated, the user must stay (a stale config naming a
	// deleted account would give access to whoever gets that name next).
	fake.FailNext("ApplyConfig", &sys.ConfigError{Output: "boom"})
	status, _ = admin.do("DELETE", "/users/"+itoa(ana.ID), nil, nil)
	want(t, "delete user with failing samba", status, 500)
	if fake.Account("ana") == nil {
		t.Error("account must survive when it could not be taken off its shares")
	}
	if !strings.Contains(fake.Config, "valid users = ana") {
		t.Error("config should be unchanged after the failed delete")
	}

	status, _ = admin.do("DELETE", "/users/"+itoa(ana.ID), nil, nil)
	want(t, "delete user status", status, 204)
	if strings.Contains(fake.Config, "= ana") || strings.Contains(fake.Config, ", ana") {
		t.Errorf("deleted user is still named in the config:\n%s", fake.Config)
	}
	if fake.Account("ana") != nil {
		t.Error("account should be gone")
	}

	// --- delete share ---
	status, _ = admin.do("DELETE", "/shares/"+itoa(media.ID), nil, nil)
	want(t, "delete share status", status, 204)
	if strings.Contains(fake.Config, "[media]") {
		t.Error("deleted share is still in the config")
	}

	// --- RBAC: a plain user cannot see or change shares ---
	jayClient := newClient(t, base)
	jayClient.login("jay", pw)
	p = problem{}
	status, _ = jayClient.do("GET", "/shares", nil, &p)
	want(t, "plain user list status", status, 403)
	status, _ = jayClient.do("POST", "/shares", map[string]any{"name": "mine", "path": "/srv/mine"}, &p)
	want(t, "plain user create status", status, 403)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// linkFiles makes some paths behave like symbolic links (also when the link
// is a parent folder, as the real filesystem does), and records which
// folders were created.
type linkFiles struct {
	sys.Files
	links   map[string]string // link -> real folder
	missing map[string]bool   // real paths that do not exist
	created []string
}

func (l *linkFiles) ResolveDir(ctx context.Context, p string) (string, error) {
	for link, real := range l.links {
		if p == link {
			p = real
		} else if rest, ok := strings.CutPrefix(p, link+"/"); ok {
			p = real + "/" + rest
		}
	}
	if l.missing[p] {
		return "", fs.ErrNotExist
	}
	return l.Files.ResolveDir(ctx, p)
}

func (l *linkFiles) CreateShareDir(ctx context.Context, p string) (string, error) {
	l.created = append(l.created, p)
	return l.Files.CreateShareDir(ctx, p)
}

// A symbolic link must not be a way around the "no system folders" rule.
func TestSharePathPolicyFollowsSymlinks(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false

	fake := sysfake.New()
	files := &linkFiles{Files: fake, links: map[string]string{
		"/srv/innocent": "/etc/samba", // looks harmless, points at a system folder
		"/srv/link":     "/etc",       // a parent that is a link into a system folder
	}, missing: map[string]bool{"/etc/new": true}}
	system := sys.System{Accounts: fake, Samba: fake, Files: files}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	t.Cleanup(func() {
		server.Close()
		application.Close()
	})

	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	applied := fake.Applied // the config written at start
	var p problem
	status, _ := admin.do("POST", "/shares", map[string]any{"name": "x", "path": "/srv/innocent"}, &p)
	want(t, "link to system folder status", status, 422)
	want(t, "link to system folder code", fieldCodes(p)["path"], "not_allowed")

	// Creating a folder through a linked parent must be refused BEFORE
	// anything is created inside the system folder.
	p = problem{}
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "y", "path": "/srv/link/new", "createDirectory": true}, &p)
	want(t, "create through linked parent status", status, 422)
	want(t, "create through linked parent code", fieldCodes(p)["path"], "not_allowed")
	if len(files.created) != 0 {
		t.Errorf("a folder was created before the policy check: %v", files.created)
	}
	want(t, "config never applied for the refused shares", fake.Applied, applied)
}
