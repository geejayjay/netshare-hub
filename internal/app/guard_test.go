package app_test

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/sys/sambalog"
	"netshare-hub/internal/sys/sysfake"
)

// Step 2.3: the ransomware guard.

var jayPC = netip.MustParseAddr("192.168.1.20")

func encrypt(fake *sysfake.Fake, n int) {
	for i := 0; i < n; i++ {
		fake.EmitAudit(sambalog.Event{Kind: sambalog.KindFile, Time: time.Now(), User: "jay", ClientIP: jayPC, Share: "docs",
			Op: "renameat", OK: true, Path: fmt.Sprintf("/srv/docs/f%03d.docx", i), Path2: fmt.Sprintf("/srv/docs/f%03d.docx.locked", i)})
	}
}

type incidentBody struct {
	ID         int64
	Username   string
	ClientIP   string
	Rule       string
	Blocked    bool
	Mode       string
	RestoredAt *time.Time
}

func waitIncidents(t *testing.T, c *client, n int) []incidentBody {
	t.Helper()
	var list struct{ Items []incidentBody }
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list.Items = nil
		c.do("GET", "/guard/incidents", nil, &list)
		if len(list.Items) >= n {
			return list.Items
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%d incidents, want %d", len(list.Items), n)
	return nil
}

func guardSetup(t *testing.T) (*client, *client, *sysfake.Fake, int64) {
	t.Helper()
	admin, fake, base, _ := activityServer(t)
	jay := createUser(t, admin, "jay")
	var sh shareBody
	admin.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
	jc := newClient(t, base)
	jc.login("jay", pw)
	return admin, jc, fake, sh.ID
}

func TestGuardLearningModeOnlyAlerts(t *testing.T) {
	admin, _, fake, _ := guardSetup(t)
	var st struct{ Mode string }
	admin.do("GET", "/guard/settings", nil, &st)
	want(t, "default mode", st.Mode, "learning")

	encrypt(fake, 25)
	inc := waitIncidents(t, admin, 1)
	if inc[0].Username != "jay" || inc[0].ClientIP != "192.168.1.20" || inc[0].Rule != "renames" || inc[0].Blocked {
		t.Errorf("incident: %+v", inc[0])
	}
	got := activeAlerts(t, admin)
	if len(got) != 1 || got[0].Kind != "guard.triggered" || got[0].Severity != "critical" || got[0].Source != "security" {
		t.Errorf("alerts: %+v", got)
	}
	if !strings.Contains(section(t, fake, "docs"), "write list = jay") {
		t.Error("learning mode must not block anyone")
	}
	if len(fake.Disconnected()) != 0 {
		t.Error("learning mode must not disconnect anyone")
	}
}

func TestGuardProtectModeBlocksAndRestores(t *testing.T) {
	admin, jay, fake, shareID := guardSetup(t)
	status, _ := admin.do("PUT", "/guard/settings", map[string]any{"mode": "protect", "renames": 20, "noteFolders": 3}, nil)
	want(t, "protect mode", status, 200)

	encrypt(fake, 25)
	inc := waitIncidents(t, admin, 1)[0]
	if !inc.Blocked || inc.Mode != "protect" {
		t.Fatalf("incident: %+v", inc)
	}
	// The incident is saved first (the block is read from it), then the
	// config is rewritten, then the computer disconnected: wait for the end.
	for i := 0; i < 200 && len(fake.Disconnected()) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	// Samba: jay may only read now.
	sec := section(t, fake, "docs")
	if strings.Contains(sec, "write list") || !strings.Contains(sec, "valid users = jay") {
		t.Errorf("jay should be read-only:\n%s", sec)
	}
	// The computer was disconnected.
	if d := fake.Disconnected(); len(d) != 1 || d[0] != jayPC {
		t.Errorf("disconnected: %v", d)
	}
	// The file manager agrees, and the preview says why.
	var p problem
	status, _ = jay.do("POST", "/files/docs/folders", map[string]any{"path": "new"}, &p)
	if status != 403 || p.Code != "SHARE_READ_ONLY" {
		t.Errorf("jay writing in the file manager: %d %s", status, p.Code)
	}
	var mine struct {
		Items []struct{ Name, Level string }
	}
	jay.do("GET", "/my/shares", nil, &mine)
	if len(mine.Items) != 1 || mine.Items[0].Level != "read" {
		t.Errorf("jay's shares: %+v", mine.Items)
	}
	var e effectiveBody
	var me userBody
	admin.do("GET", "/users/2", nil, &me)
	admin.do("POST", "/shares/effective-access", map[string]any{"access": []map[string]any{{"userId": me.ID, "level": "write"}}}, &e)
	if len(e.Users) != 1 || e.Users[0].Level != "read" || !strings.Contains(e.Users[0].Reason, "ransomware guard") {
		t.Errorf("preview: %+v", e.Users)
	}

	// Only an admin restores.
	if status, _ := jay.do("POST", "/guard/incidents/"+itoa(inc.ID)+"/restore", nil, nil); status != 403 {
		t.Errorf("jay restored their own access: %d", status)
	}
	var restored incidentBody
	status, _ = admin.do("POST", "/guard/incidents/"+itoa(inc.ID)+"/restore", nil, &restored)
	want(t, "restore", status, 200)
	if restored.RestoredAt == nil {
		t.Error("restoredAt not set")
	}
	if !strings.Contains(section(t, fake, "docs"), "write list = jay") {
		t.Errorf("jay should write again:\n%s", fake.Config)
	}
	if len(activeAlerts(t, admin)) != 0 {
		t.Error("the alert should be resolved after restoring")
	}
	status, _ = jay.do("POST", "/files/docs/folders", map[string]any{"path": "new"}, nil)
	want(t, "jay writes again", status, 201)
	_ = shareID
}

// Normal heavy work in protect mode: nobody is blocked.
func TestGuardIgnoresNormalWork(t *testing.T) {
	admin, _, fake, _ := guardSetup(t)
	admin.do("PUT", "/guard/settings", map[string]any{"mode": "protect", "renames": 20, "noteFolders": 3}, nil)
	for i := 0; i < 3000; i++ {
		fake.EmitAudit(sambalog.Event{Kind: sambalog.KindFile, Time: time.Now(), User: "jay", ClientIP: jayPC, Share: "docs",
			Op: "openat", Write: true, OK: true, Path: fmt.Sprintf("/srv/docs/Copy/%05d.jpg", i)})
	}
	for i := 0; i < 500; i++ {
		fake.EmitAudit(sambalog.Event{Kind: sambalog.KindFile, Time: time.Now(), User: "jay", ClientIP: jayPC, Share: "docs",
			Op: "renameat", OK: true, Path: fmt.Sprintf("/srv/docs/Copy/%05d.jpg", i), Path2: fmt.Sprintf("/srv/docs/Sorted/%05d.jpg", i)})
	}
	time.Sleep(500 * time.Millisecond)
	var list struct{ Items []incidentBody }
	admin.do("GET", "/guard/incidents", nil, &list)
	if len(list.Items) != 0 {
		t.Errorf("normal work triggered the guard: %+v", list.Items)
	}
	if !strings.Contains(section(t, fake, "docs"), "write list = jay") {
		t.Error("normal work blocked the user")
	}
}

func TestGuardSettingsValidation(t *testing.T) {
	admin, jay, _, _ := guardSetup(t)
	var p problem
	status, _ := admin.do("PUT", "/guard/settings", map[string]any{"mode": "protect", "renames": 1, "noteFolders": 3}, &p)
	want(t, "too low a threshold", status, 422)
	if status, _ := jay.do("PUT", "/guard/settings", map[string]any{"mode": "off", "renames": 20, "noteFolders": 3}, nil); status != 403 {
		t.Errorf("a plain user changed the guard: %d", status)
	}
}
