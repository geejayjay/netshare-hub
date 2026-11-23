package app_test

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"netshare-hub/internal/sys/sysfake"
)

// Step 3.6: config backup and restore.

const backupPass = "a long backup passphrase"

func exportBackup(t *testing.T, c *client, body map[string]any) []byte {
	t.Helper()
	var file json.RawMessage
	status, h := c.do("POST", "/system/backup", body, &file)
	if status != 200 {
		t.Fatalf("export: %d", status)
	}
	if !strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="netshare-hub-config-`) || h.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", h)
	}
	return file
}

type restoreSummary struct {
	Users, SMBUsers, Admins, Shares int
	MissingFolders, Warnings        []string
	Applied, SignInAgain            bool
	RollbackCopy                    string
}

func restore(t *testing.T, c *client, file []byte, extra map[string]any, out any) int {
	t.Helper()
	body := map[string]any{"backup": file, "passphrase": backupPass}
	for k, v := range extra {
		body[k] = v
	}
	status, _ := c.do("POST", "/system/restore", body, out)
	return status
}

// source is an install with users, shares and settings to back up.
func source(t *testing.T) (*client, *sysfake.Fake) {
	t.Helper()
	admin, fake, _, _ := activityServer(t)
	jay := createUser(t, admin, "jay")
	ann := createUser(t, admin, "ann")
	admin.do("POST", "/users", map[string]any{"username": "web", "role": "user", "password": pw, "smbAccess": false}, nil)
	for _, s := range []map[string]any{
		{"name": "docs", "path": "/srv/docs", "comment": "Office", "networkRules": []map[string]any{{"action": "allow", "address": "192.168.1.0/24"}},
			"access": []map[string]any{{"userId": jay.ID, "level": "write"}, {"userId": ann.ID, "level": "read"}}},
		{"name": "backup", "path": "/srv/backup", "preset": "time-machine", "timeMachine": true, "timeMachineMaxGiB": 300,
			"access": []map[string]any{{"userId": jay.ID, "level": "write"}}},
		{"name": "media", "path": "/srv/media", "guestAccess": true, "readOnly": true, "hideUnreadable": true},
	} {
		if status, _ := admin.do("POST", "/shares", s, nil); status != 201 {
			t.Fatalf("create share %v: %d", s["name"], status)
		}
	}
	settings := withMac(true)
	settings["serverName"], settings["workgroup"], settings["activityLog"] = "NAS", "HOME", true
	if status, _ := admin.do("PUT", "/settings/samba", settings, nil); status != 200 {
		t.Fatalf("server settings: %d", status)
	}
	admin.do("PUT", "/guard/settings", map[string]any{"mode": "protect", "renames": 30, "noteFolders": 4}, nil)
	admin.do("PUT", "/guard/bans/settings", map[string]any{"enabled": true, "failures": 7, "windowMinutes": 5, "banMinutes": 20,
		"maxBanHours": 24, "followDevice": false, "allowList": []string{"192.168.1.10"}}, nil)
	admin.do("PUT", "/settings/discovery", map[string]any{"wsd": true, "mdns": true}, nil)
	return admin, fake
}

// A backup from one install restores on another: users sign in with their
// passwords (web and SMB), shares, access and every setting are back.
func TestRestoreOnAnotherInstall(t *testing.T) {
	src, srcFake := source(t)
	file := exportBackup(t, src, map[string]any{"passphrase": backupPass})
	if bytes.Contains(file, []byte("jay")) || bytes.Contains(file, []byte("argon2id$")) {
		t.Fatal("the backup is readable without the passphrase")
	}

	dst, dstFake, base, dataDir := activityServer(t)
	// The new install had its own user and share, which the restore replaces.
	createUser(t, dst, "old")
	dst.do("POST", "/shares", map[string]any{"name": "scratch", "path": "/srv/scratch"}, nil)

	// A dry run first: what it would do, nothing changed.
	var sum restoreSummary
	if status := restore(t, dst, file, map[string]any{"dryRun": true}, &sum); status != 200 {
		t.Fatalf("dry run: %d", status)
	}
	if sum.Applied || sum.Users != 4 || sum.SMBUsers != 2 || sum.Admins != 1 || sum.Shares != 3 {
		t.Errorf("dry run summary: %+v", sum)
	}
	if dstFake.Account("old") == nil || dstFake.Account("jay") != nil {
		t.Error("the dry run changed accounts")
	}

	if status := restore(t, dst, file, nil, &sum); status != 200 {
		t.Fatalf("restore: %d %+v", status, sum)
	}
	if !sum.Applied || !sum.SignInAgain || !strings.HasPrefix(sum.RollbackCopy, "pre-restore-") {
		t.Errorf("summary: %+v", sum)
	}
	_ = dataDir

	// Everyone was signed out, the caller too.
	if status, _ := dst.do("GET", "/users", nil, nil); status != 401 {
		t.Errorf("old session still works: %d", status)
	}
	admin := newClient(t, base)
	admin.login("admin", pw) // the source's admin and password
	jay := newClient(t, base)
	jay.login("jay", pw)

	// SMB: the same password hash, so the same SMB password.
	if a := dstFake.Account("jay"); a == nil || a.Hash != srcFake.Account("jay").Hash || !a.Enabled {
		t.Errorf("jay's account: %+v", a)
	}
	if dstFake.Account("old") != nil || dstFake.Account("web") != nil {
		t.Error("accounts not in the backup should be gone, web-only users have none")
	}

	// Shares and their settings.
	cfg := dstFake.CurrentConfig()
	for _, line := range []string{"[docs]", "hosts allow = 192.168.1.0/24", "write list = jay", "[backup]", "fruit:time machine max size = 300G",
		"[media]", "guest ok = yes", "netbios name = NAS", "workgroup = HOME", "full_audit"} {
		if !strings.Contains(cfg, line) {
			t.Errorf("config is missing %q:\n%s", line, cfg)
		}
	}
	if strings.Contains(cfg, "[scratch]") {
		t.Error("the new install's own share is still shared")
	}
	var mine struct {
		Items []struct{ Name, Level string }
	}
	jay.do("GET", "/my/shares", nil, &mine)
	if len(mine.Items) != 3 {
		t.Errorf("jay's shares: %+v", mine.Items)
	}
	var guard struct {
		Mode    string
		Renames int
	}
	admin.do("GET", "/guard/settings", nil, &guard)
	if guard.Mode != "protect" || guard.Renames != 30 {
		t.Errorf("guard: %+v", guard)
	}
	var bans struct {
		Failures  int
		AllowList []string
	}
	admin.do("GET", "/guard/bans/settings", nil, &bans)
	if bans.Failures != 7 || len(bans.AllowList) != 1 {
		t.Errorf("bans: %+v", bans)
	}
	var d discoveryBody
	admin.do("GET", "/settings/discovery", nil, &d)
	if !d.WSD.On || !d.MDNS.On {
		t.Errorf("discovery: %+v", d)
	}
	// The files inside get the restored permissions.
	var list struct{ Items []struct{ Kind string } }
	admin.do("GET", "/jobs?all=true", nil, &list)
	fixes := 0
	for _, j := range list.Items {
		if j.Kind == "shares.fix-permissions" {
			fixes++
		}
	}
	if fixes != 3 {
		t.Errorf("%d fix-permissions jobs, want 3", fixes)
	}
}

// Without SMB passwords in the backup, SMB users come back web-only and off
// their shares, and the summary says so.
func TestRestoreWithoutSMBPasswords(t *testing.T) {
	src, _ := source(t)
	file := exportBackup(t, src, map[string]any{"passphrase": backupPass, "smbPasswords": false})
	dst, dstFake, _, _ := activityServer(t)
	var sum restoreSummary
	if status := restore(t, dst, file, nil, &sum); status != 200 {
		t.Fatalf("restore: %d", status)
	}
	if sum.SMBUsers != 0 || len(sum.Warnings) < 2 || !strings.Contains(strings.Join(sum.Warnings, " "), "jay: SMB access is turned off") {
		t.Errorf("summary: %+v", sum)
	}
	if dstFake.Account("jay") != nil || strings.Contains(dstFake.CurrentConfig(), "jay") {
		t.Errorf("jay should have no SMB access:\n%s", dstFake.CurrentConfig())
	}
}

// A damaged file, a wrong passphrase or a backup that cannot work here is
// refused, and nothing changes.
func TestRestoreRefusesAndChangesNothing(t *testing.T) {
	src, _ := source(t)
	file := exportBackup(t, src, map[string]any{"passphrase": backupPass})
	dst, dstFake, _, _ := activityServer(t)
	createUser(t, dst, "old")
	before := dstFake.CurrentConfig()
	unchanged := func(what string) {
		t.Helper()
		if dstFake.CurrentConfig() != before || dstFake.Account("old") == nil || dstFake.Account("jay") != nil {
			t.Errorf("%s changed something", what)
		}
		var users struct{ Items []userBody }
		if status, _ := dst.do("GET", "/users", nil, &users); status != 200 || len(users.Items) != 2 {
			t.Errorf("%s: users %d %+v", what, status, users.Items)
		}
	}

	damaged := bytes.Clone(file)
	i := bytes.Index(damaged, []byte(`"data": "`)) + 40
	damaged[i] ^= 'A' ^ 'B'
	for _, c := range []struct {
		name string
		file []byte
		pass string
		code string
	}{
		{"wrong passphrase", file, "not the right passphrase", "BACKUP_UNREADABLE"},
		{"damaged", damaged, backupPass, "BACKUP_UNREADABLE"},
		{"not a backup", []byte(`{"format":"something else"}`), backupPass, "BACKUP_UNREADABLE"},
	} {
		var p problem
		status, _ := dst.do("POST", "/system/restore", map[string]any{"backup": c.file, "passphrase": c.pass}, &p)
		if status != 422 || p.Code != c.code {
			t.Errorf("%s: %d %s", c.name, status, p.Code)
		}
		unchanged(c.name)
	}

	// Well-formed backups whose contents must not be applied.
	payload := func(edit func(p map[string]any)) []byte {
		p := map[string]any{"format": "netshare-hub-config", "createdAt": "2026-11-01T00:00:00Z", "app": "dev", "hostname": "x",
			"users": []map[string]any{{"id": 1, "username": "admin", "displayName": "", "role": "admin",
				"passwordHash": validHash(t), "disabled": false, "smbAccess": false, "createdAt": "2026-11-01T00:00:00Z", "updatedAt": "2026-11-01T00:00:00Z"}},
			"shares": []map[string]any{}, "samba": withMac(false),
			"guard": map[string]any{"mode": "learning", "renames": 20, "noteFolders": 3},
			"bans":  map[string]any{"enabled": true, "failures": 10, "windowMinutes": 10, "banMinutes": 15, "maxBanHours": 168, "followDevice": true, "allowList": []string{}},
		}
		edit(p)
		return sealBackup(t, p, backupPass)
	}
	share := func(path string) map[string]any {
		return map[string]any{"id": 1, "name": "x", "path": path, "comment": "", "readOnly": false, "guestAccess": false, "preset": "custom",
			"hideUnreadable": false, "timeMachine": false, "timeMachineMaxGiB": 0, "networkRules": []any{}, "access": []any{},
			"createdAt": "2026-11-01T00:00:00Z", "updatedAt": "2026-11-01T00:00:00Z"}
	}
	for _, c := range []struct {
		name  string
		edit  func(p map[string]any)
		field string
	}{
		{"a system folder", func(p map[string]any) { p["shares"] = []any{share("/etc")} }, "shares[0].path"},
		{"a missing folder", func(p map[string]any) { p["shares"] = []any{share(sysfake.MissingPrefix + "/x")} }, "shares[0].path"},
		{"no admin", func(p map[string]any) { p["users"].([]map[string]any)[0]["role"] = "user" }, "users"},
		{"a broken password hash", func(p map[string]any) { p["users"].([]map[string]any)[0]["passwordHash"] = "plain" }, "users[0].passwordHash"},
		{"a bad server setting", func(p map[string]any) { p["samba"].(map[string]any)["minProtocol"] = "NT1" }, "samba."},
		{"a bad SMB password hash", func(p map[string]any) {
			p["users"].([]map[string]any)[0]["smbAccess"] = true
			p["smbHashes"] = map[string]string{"admin": "zz"}
		}, "smbHashes.admin"},
		{"guard threshold too low", func(p map[string]any) { p["guard"].(map[string]any)["renames"] = 1 }, "guard.renames"},
	} {
		var p problem
		status := restore(t, dst, payload(c.edit), nil, &p)
		found := false
		for _, e := range p.Errors {
			found = found || strings.HasPrefix(e.Field, c.field)
		}
		if status != 422 || p.Code != "BACKUP_INVALID" || !found {
			t.Errorf("%s: %d %s %+v", c.name, status, p.Code, p.Errors)
		}
		unchanged(c.name)
	}

	// A user named like a system account on this machine: found while
	// applying, everything undone.
	sys := payload(func(p map[string]any) {
		p["users"] = append(p["users"].([]map[string]any), map[string]any{"id": 2, "username": "daemon", "displayName": "", "role": "user",
			"passwordHash": validHash(t), "disabled": false, "smbAccess": true, "createdAt": "2026-11-01T00:00:00Z", "updatedAt": "2026-11-01T00:00:00Z"},
			map[string]any{"id": 3, "username": "jay", "displayName": "", "role": "user",
				"passwordHash": validHash(t), "disabled": false, "smbAccess": true, "createdAt": "2026-11-01T00:00:00Z", "updatedAt": "2026-11-01T00:00:00Z"})
		p["smbHashes"] = map[string]string{"daemon": sysfake.NTHash("x"), "jay": sysfake.NTHash("y")}
	})
	var p problem
	if status := restore(t, dst, sys, nil, &p); status != 422 || p.Code != "BACKUP_INVALID" || len(p.Errors) != 1 || p.Errors[0].Code != "taken_by_system" {
		t.Errorf("system account name: %d %+v", status, p)
	}
	unchanged("a system account's name")

	// Samba refuses the new config: everything undone, accounts too.
	dstFake.FailNext("ApplyConfig", errors.New("boom"))
	jayOnly := payload(func(p map[string]any) {
		p["users"] = append(p["users"].([]map[string]any), map[string]any{"id": 3, "username": "jay", "displayName": "", "role": "user",
			"passwordHash": validHash(t), "disabled": false, "smbAccess": true, "createdAt": "2026-11-01T00:00:00Z", "updatedAt": "2026-11-01T00:00:00Z"})
		p["smbHashes"] = map[string]string{"jay": sysfake.NTHash("y")}
	})
	p = problem{}
	if status := restore(t, dst, jayOnly, nil, &p); status != 500 || p.Code != "INTERNAL" {
		t.Errorf("samba refused: %d %+v", status, p)
	}
	if dstFake.Account("jay") != nil {
		t.Error("the account made for the restore is still there")
	}
	unchanged("a refused config")

	// Only an admin may back up or restore.
	jay := newClientFor(t, dst, "old")
	if status, _ := jay.do("POST", "/system/backup", map[string]any{"passphrase": backupPass}, nil); status != 403 {
		t.Errorf("a user downloaded a backup: %d", status)
	}
	if status := restore(t, jay, file, map[string]any{"dryRun": true}, nil); status != 403 {
		t.Errorf("a user checked a restore: %d", status)
	}
	// A short passphrase is refused.
	if status, _ := dst.do("POST", "/system/backup", map[string]any{"passphrase": "short"}, nil); status != 422 {
		t.Errorf("short passphrase: %d", status)
	}
}

// Missing folders are reported by the dry run and created when asked.
func TestRestoreCreatesMissingFolders(t *testing.T) {
	src, _ := activityServer2(t)
	jay := createUser(t, src, "jay")
	src.do("POST", "/shares", map[string]any{"name": "new", "path": sysfake.MissingPrefix + "/new", "createDirectory": true,
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, nil)
	file := exportBackup(t, src, map[string]any{"passphrase": backupPass})

	dst, _, _, _ := activityServer(t)
	var sum restoreSummary
	if status := restore(t, dst, file, map[string]any{"dryRun": true, "createMissingFolders": true}, &sum); status != 200 ||
		len(sum.MissingFolders) != 1 || sum.MissingFolders[0] != sysfake.MissingPrefix+"/new" {
		t.Fatalf("dry run: %d %+v", status, sum)
	}
	var p problem
	if status := restore(t, dst, file, nil, &p); status != 422 || p.Code != "BACKUP_INVALID" {
		t.Errorf("without createMissingFolders: %d %+v", status, p)
	}
	if status := restore(t, dst, file, map[string]any{"createMissingFolders": true}, &sum); status != 200 || !sum.Applied {
		t.Errorf("with createMissingFolders: %d %+v", status, sum)
	}
}

// --- helpers ---

func activityServer2(t *testing.T) (*client, *sysfake.Fake) {
	c, f, _, _ := activityServer(t)
	return c, f
}

// newClientFor creates a plain user on an install and signs them in.
func newClientFor(t *testing.T, admin *client, name string) *client {
	t.Helper()
	c := newClient(t, admin.base)
	c.login(name, pw)
	return c
}

var hashCache string

// validHash is a real argon2id password hash (of pw).
func validHash(t *testing.T) string {
	if hashCache == "" {
		salt := make([]byte, 16)
		rand.Read(salt)
		key := argon2.IDKey([]byte(pw), salt, 2, 19*1024, 1, 32)
		hashCache = fmt.Sprintf("$argon2id$v=%d$m=%d,t=2,p=1$%s$%s", argon2.Version, 19*1024,
			base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	}
	return hashCache
}

// sealBackup writes a backup file the documented way (format.go), so the
// tests can make well-formed backups with any contents.
func sealBackup(t *testing.T, payload any, passphrase string) []byte {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	zw.Write(raw)
	zw.Close()
	salt, nonce := make([]byte, 16), make([]byte, chacha20poly1305.NonceSizeX)
	rand.Read(salt)
	rand.Read(nonce)
	key := argon2.IDKey([]byte(passphrase), salt, 1, 8*1024, 1, chacha20poly1305.KeySize)
	aead, _ := chacha20poly1305.NewX(key)
	header := fmt.Appendf(nil, "netshare-hub-backup|1|argon2id|1|%d|1|%x|xchacha20-poly1305", 8*1024, salt)
	out, _ := json.Marshal(map[string]any{"format": "netshare-hub-backup", "version": 1,
		"kdf":    map[string]any{"name": "argon2id", "time": 1, "memoryKiB": 8 * 1024, "threads": 1, "salt": salt},
		"cipher": "xchacha20-poly1305", "nonce": nonce, "data": aead.Seal(nil, nonce, packed.Bytes(), header)})
	return out
}
