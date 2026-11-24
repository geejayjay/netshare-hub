package app_test

import (
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysfake"
)

// Step 4.2: NFS exports.

type nfsBody struct {
	Enabled    bool
	Mode       string
	AsUserID   int64
	AsUsername string
	Clients    []struct {
		Address  string
		ReadOnly bool
	}
}

type nfsShareBody struct {
	ID  int64
	NFS nfsBody
}

func nfsModules() config.Modules {
	m := config.Default().Modules
	m.NFS = true
	return m
}

func nfsOn(userID int64, clients ...string) map[string]any {
	list := []map[string]any{}
	for _, c := range clients {
		list = append(list, map[string]any{"address": c})
	}
	return map[string]any{"enabled": true, "mode": "user", "asUserId": userID, "clients": list}
}

// exportLine returns the exports line of a folder.
func exportLine(t *testing.T, fake *sysfake.Fake, path string) string {
	t.Helper()
	for _, l := range strings.Split(fake.CurrentExports(), "\n") {
		if strings.HasPrefix(l, path+" ") {
			return l
		}
	}
	return ""
}

func TestNFSExportAsUser(t *testing.T) {
	admin, fake, _ := moduleServer(t, nfsModules())
	jay := createUser(t, admin, "jay")
	ana := createUser(t, admin, "ana")

	var sh nfsShareBody
	status, _ := admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}, {"userId": ana.ID, "level": "read"}},
		"nfs":    nfsOn(jay.ID, " 192.168.1.0/24 ", "fd00::7")}, &sh)
	want(t, "create", status, 201)
	if !sh.NFS.Enabled || sh.NFS.Mode != "user" || sh.NFS.AsUserID != jay.ID || len(sh.NFS.Clients) != 2 ||
		sh.NFS.Clients[0].Address != "192.168.1.0/24" {
		t.Errorf("nfs: %+v", sh.NFS)
	}
	line := exportLine(t, fake, "/srv/media")
	if !strings.Contains(line, "192.168.1.0/24(rw,") || !strings.Contains(line, "fd00::7(rw,") ||
		!strings.Contains(line, "all_squash,anonuid=") {
		t.Fatalf("export: %q\n%s", line, fake.CurrentExports())
	}

	var st struct {
		Installed, Running, Supported bool
		Exports                       []string
	}
	status, _ = admin.do("GET", "/shares/nfs/status", nil, &st)
	want(t, "status", status, 200)
	if !st.Supported || strings.Join(st.Exports, ",") != "media" {
		t.Errorf("status: %+v", st)
	}

	// Acting as a reader: the export becomes read-only.
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"nfs": nfsOn(ana.ID, "192.168.1.0/24")}, &sh)
	want(t, "as reader", status, 200)
	if line := exportLine(t, fake, "/srv/media"); !strings.Contains(line, "192.168.1.0/24(ro,") {
		t.Errorf("reader export: %q", line)
	}
	// A per-client read-only flag, back as jay.
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"nfs": map[string]any{"enabled": true, "mode": "user",
		"asUserId": jay.ID, "clients": []map[string]any{{"address": "192.168.1.5", "readOnly": true}, {"address": "192.168.1.6"}}}}, &sh)
	want(t, "per-client", status, 200)
	if line := exportLine(t, fake, "/srv/media"); !strings.Contains(line, "192.168.1.5(ro,") || !strings.Contains(line, "192.168.1.6(rw,") {
		t.Errorf("per-client export: %q", line)
	}

	// A read-only share is read-only over NFS too.
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"readOnly": true}, nil)
	want(t, "read-only share", status, 200)
	if line := exportLine(t, fake, "/srv/media"); strings.Contains(line, "(rw,") {
		t.Errorf("read-only share exported writable: %q", line)
	}

	// Off: the export goes.
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"nfs": map[string]any{"enabled": false}}, &sh)
	want(t, "off", status, 200)
	if line := exportLine(t, fake, "/srv/media"); line != "" {
		t.Errorf("still exported: %q", line)
	}
}

func TestNFSRules(t *testing.T) {
	admin, _, _ := moduleServer(t, nfsModules())
	jay := createUser(t, admin, "jay")
	bob := createUser(t, admin, "bob")
	eve := createUser(t, admin, "eve")
	access := []map[string]any{{"userId": jay.ID, "level": "write"}, {"userId": bob.ID, "level": "upload"}}

	cases := []struct {
		name, field, code string
		nfs               map[string]any
		extra             map[string]any
	}{
		{"no clients", "nfs.clients", "required", nfsOn(jay.ID), nil},
		{"every address", "nfs.clients[0].address", "every_address", nfsOn(jay.ID, "0.0.0.0/0"), nil},
		{"host name", "nfs.clients[0].address", "invalid_format", nfsOn(jay.ID, "nas.lan"), nil},
		{"injection", "nfs.clients[0].address", "invalid_format", nfsOn(jay.ID, "10.0.0.1(rw,no_root_squash)"), nil},
		{"duplicate", "nfs.clients[1].address", "duplicate", nfsOn(jay.ID, "10.0.0.1", "10.0.0.1"), nil},
		{"no user", "nfs.asUserId", "required", nfsOn(0, "10.0.0.1"), nil},
		{"not on share", "nfs.asUserId", "not_on_share", nfsOn(eve.ID, "10.0.0.1"), nil},
		{"upload only", "nfs.asUserId", "upload_only", nfsOn(bob.ID, "10.0.0.1"), nil},
		{"user with client ids", "nfs.asUserId", "not_with_client_ids",
			map[string]any{"enabled": true, "mode": "client-ids", "asUserId": jay.ID, "clients": []map[string]any{{"address": "10.0.0.1"}}}, nil},
		{"time machine", "nfs.enabled", "not_with_time_machine", nfsOn(jay.ID, "10.0.0.1"),
			map[string]any{"timeMachine": true, "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}},
	}
	for i, c := range cases {
		body := map[string]any{"name": "s" + itoa(int64(i+1)), "path": "/srv/s" + itoa(int64(i+1)), "access": access, "nfs": c.nfs}
		for k, v := range c.extra {
			body[k] = v
		}
		var p problem
		status, _ := admin.do("POST", "/shares", body, &p)
		if status != 422 || fieldCodes(p)[c.field] != c.code {
			t.Errorf("%s: status %d, %+v", c.name, status, p.Errors)
		}
	}

	// Trusting client ids needs no user, and still squashes root.
	var sh nfsShareBody
	status, _ := admin.do("POST", "/shares", map[string]any{"name": "lab", "path": "/srv/lab", "access": access,
		"nfs": map[string]any{"enabled": true, "mode": "client-ids", "clients": []map[string]any{{"address": "10.0.0.0/8"}}}}, &sh)
	want(t, "client ids", status, 201)
}

// An export never outlives the access it acts with: the user losing access,
// losing SMB access, or being blocked by the ransomware guard.
func TestNFSFollowsAccess(t *testing.T) {
	admin, fake, _ := moduleServer(t, nfsModules())
	jay := createUser(t, admin, "jay")
	ana := createUser(t, admin, "ana")
	var sh nfsShareBody
	admin.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}, {"userId": ana.ID, "level": "write"}},
		"nfs":    nfsOn(jay.ID, "192.168.1.0/24")}, &sh)

	// Taking jay off the share while the export acts as jay is refused.
	var p problem
	status, _ := admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"access": []map[string]any{{"userId": ana.ID, "level": "write"}}}, &p)
	want(t, "remove the NFS user", status, 422)
	want(t, "field", fieldCodes(p)["nfs.asUserId"], "not_on_share")

	// Lowering jay to read makes the export read-only.
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"access": []map[string]any{{"userId": jay.ID, "level": "read"},
		{"userId": ana.ID, "level": "write"}}}, nil)
	want(t, "jay reads", status, 200)
	if line := exportLine(t, fake, "/srv/docs"); !strings.Contains(line, "(ro,") || strings.Contains(line, "(rw,") {
		t.Errorf("after jay reads: %q", line)
	}

	// Removing jay's SMB access turns the export off (there is no account
	// to act as any more).
	status, _ = admin.do("PATCH", "/users/"+itoa(jay.ID), map[string]any{"smbAccess": false}, nil)
	want(t, "smb access off", status, 200)
	if line := exportLine(t, fake, "/srv/docs"); line != "" {
		t.Errorf("export kept after jay lost SMB access: %q", line)
	}
	id := sh.ID
	sh = nfsShareBody{} // asUserId is left out when 0
	admin.do("GET", "/shares/"+itoa(id), nil, &sh)
	if sh.NFS.Enabled || sh.NFS.AsUserID != 0 {
		t.Errorf("nfs after jay lost SMB access: %+v", sh.NFS)
	}
}

func TestNFSBlockedUserOnlyReads(t *testing.T) {
	admin, fake, _ := moduleServer(t, nfsModules())
	jay := createUser(t, admin, "jay")
	var sh nfsShareBody
	admin.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}, "nfs": nfsOn(jay.ID, "192.168.1.0/24")}, &sh)
	for i := 0; i < 100 && fake.AuditListeners() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := admin.do("PUT", "/guard/settings", map[string]any{"mode": "protect", "renames": 20, "noteFolders": 3}, nil)
	want(t, "protect", status, 200)
	encrypt(fake, 25)
	waitIncidents(t, admin, 1)
	deadline := time.Now().Add(5 * time.Second)
	line := ""
	for time.Now().Before(deadline) {
		if line = exportLine(t, fake, "/srv/docs"); strings.Contains(line, "(ro,") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(line, "(ro,") {
		t.Errorf("blocked user still writes over NFS: %q", line)
	}
}

func TestNFSUnavailable(t *testing.T) {
	admin, fake, _ := moduleServer(t, nfsModules())
	jay := createUser(t, admin, "jay")
	fake.SetNFSStatus(sys.NFSStatus{Supported: false, Reason: "This machine is an unprivileged container."})

	// Turning NFS on is refused with the reason; nothing is saved.
	var p problem
	status, _ := admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}, "nfs": nfsOn(jay.ID, "192.168.1.0/24")}, &p)
	want(t, "create with NFS", status, 409)
	want(t, "code", p.Code, "NFS_UNAVAILABLE")
	var list struct{ Total int }
	admin.do("GET", "/shares", nil, &list)
	want(t, "nothing saved", list.Total, 0)
	var st struct {
		Supported bool
		Hint      string
	}
	admin.do("GET", "/shares/nfs/status", nil, &st)
	if st.Supported || !strings.Contains(st.Hint, "unprivileged") {
		t.Errorf("status: %+v", st)
	}

	// An SMB-only change still works while the NFS server refuses, and
	// raises an alert instead of failing.
	fake.SetNFSStatus(sys.NFSStatus{Installed: true, Running: true, Supported: true})
	var sh nfsShareBody
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}, "nfs": nfsOn(jay.ID, "192.168.1.0/24")}, &sh)
	want(t, "create", status, 201)
	fake.FailNext("ApplyExports", &sys.ConfigError{Output: "exportfs: bad"})
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"comment": "films"}, nil)
	want(t, "SMB change with NFS failing", status, 200)
	found := false
	for _, a := range activeAlerts(t, admin) {
		found = found || a.Kind == "nfs.apply_failed"
	}
	if !found {
		t.Error("no nfs.apply_failed alert")
	}
	// A change to NFS itself is undone when the NFS server refuses.
	fake.FailNext("ApplyExports", &sys.ConfigError{Output: "exportfs: bad"})
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"nfs": nfsOn(jay.ID, "10.0.0.1")}, &p)
	want(t, "NFS change refused", status, 422)
	want(t, "code", p.Code, "NFS_CONFIG_REFUSED")
	admin.do("GET", "/shares/"+itoa(sh.ID), nil, &sh)
	if sh.NFS.Clients[0].Address != "192.168.1.0/24" {
		t.Errorf("refused change was saved: %+v", sh.NFS)
	}
}

// A disabled account cannot sign in over SMB, and NFS (which signs nobody
// in) must not act as it either; enabling it again brings the export back.
func TestNFSDisabledUser(t *testing.T) {
	admin, fake, _ := moduleServer(t, nfsModules())
	jay := createUser(t, admin, "jay")
	var sh nfsShareBody
	admin.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}, "nfs": nfsOn(jay.ID, "192.168.1.0/24")}, &sh)
	if exportLine(t, fake, "/srv/docs") == "" {
		t.Fatal("not exported")
	}
	status, _ := admin.do("PATCH", "/users/"+itoa(jay.ID), map[string]any{"disabled": true}, nil)
	want(t, "disable", status, 200)
	if line := exportLine(t, fake, "/srv/docs"); line != "" {
		t.Errorf("exported as a disabled account: %q", line)
	}
	status, _ = admin.do("PATCH", "/users/"+itoa(jay.ID), map[string]any{"disabled": false}, nil)
	want(t, "enable", status, 200)
	if line := exportLine(t, fake, "/srv/docs"); line == "" {
		t.Error("export did not come back after enabling the account")
	}
}
