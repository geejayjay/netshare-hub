package app_test

import (
	"slices"
	"strings"
	"testing"
)

// Step 1.5: groups.

type groupBody struct {
	ID      int64
	Name    string
	Members []struct {
		UserID   int64
		Username string
	}
}

func createGroup(t *testing.T, c *client, name string, members ...int64) groupBody {
	t.Helper()
	if members == nil {
		members = []int64{}
	}
	var g groupBody
	if status, _ := c.do("POST", "/groups", map[string]any{"name": name, "members": members}, &g); status != 201 {
		t.Fatalf("create group %s: %d", name, status)
	}
	return g
}

func TestGroupsAreLinuxGroups(t *testing.T) {
	admin, fake, _, _ := activityServer(t)
	jay := createUser(t, admin, "jay")
	ann := createUser(t, admin, "ann")
	g := createGroup(t, admin, "staff", jay.ID, ann.ID)
	if len(g.Members) != 2 || g.Members[0].Username == "" {
		t.Errorf("group: %+v", g)
	}
	if m := fake.Groups()["staff"]; !slices.Equal(m, []string{"jay", "ann"}) && !slices.Equal(m, []string{"ann", "jay"}) {
		t.Errorf("Linux group: %v", fake.Groups())
	}
	status, _ := admin.do("PATCH", "/groups/"+itoa(g.ID), map[string]any{"members": []int64{jay.ID}}, &g)
	want(t, "change members", status, 200)
	if m := fake.Groups()["staff"]; !slices.Equal(m, []string{"jay"}) {
		t.Errorf("after the change: %v", m)
	}

	// Names: not a user's, not a system group, not taken.
	var p problem
	for name, code := range map[string]string{"jay": "taken_by_user", "root": "taken_by_system", "staff": "taken",
		"netshare-hub": "invalid_format", "Bad Name": "invalid_format"} {
		status, _ := admin.do("POST", "/groups", map[string]any{"name": name}, &p)
		if status != 422 || fieldCodes(p)["name"] != code {
			t.Errorf("group name %q: %d %+v", name, status, p.Errors)
		}
	}
	if len(fake.Groups()) != 1 {
		t.Errorf("a refused group reached the system: %v", fake.Groups())
	}
	// A new user cannot take a group's name.
	status, _ = admin.do("POST", "/users", map[string]any{"username": "staff", "role": "user", "password": pw}, &p)
	if status != 422 || fieldCodes(p)["username"] != "taken_by_group" {
		t.Errorf("user named like a group: %d %+v", status, p.Errors)
	}
	// Web-only users have no Linux account to put in a group.
	var web userBody
	admin.do("POST", "/users", map[string]any{"username": "web", "role": "user", "password": pw, "smbAccess": false}, &web)
	status, _ = admin.do("PATCH", "/groups/"+itoa(g.ID), map[string]any{"members": []int64{web.ID}}, &p)
	if status != 422 || fieldCodes(p)["members[0]"] != "no_smb_access" {
		t.Errorf("web-only member: %d %+v", status, p.Errors)
	}

	status, _ = admin.do("DELETE", "/groups/"+itoa(g.ID), nil, nil)
	want(t, "delete", status, 204)
	if len(fake.Groups()) != 0 {
		t.Errorf("Linux group kept: %v", fake.Groups())
	}
}

func TestShareAccessThroughGroups(t *testing.T) {
	admin, fake, base, _ := activityServer(t)
	jay := createUser(t, admin, "jay")
	ann := createUser(t, admin, "ann")
	bob := createUser(t, admin, "bob")
	staff := createGroup(t, admin, "staff", jay.ID, ann.ID)
	editors := createGroup(t, admin, "editors", ann.ID)

	var sh struct {
		ID          int64
		GroupAccess []struct {
			GroupID   int64
			GroupName string
			Level     string
		}
	}
	// staff reads, editors write; bob has his own entry; ann's own entry
	// (read) wins over the editors group's write.
	status, _ := admin.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs",
		"access":      []map[string]any{{"userId": bob.ID, "level": "write"}, {"userId": ann.ID, "level": "read"}},
		"groupAccess": []map[string]any{{"groupId": staff.ID, "level": "read"}, {"groupId": editors.ID, "level": "write"}}}, &sh)
	want(t, "create", status, 201)
	if len(sh.GroupAccess) != 2 || sh.GroupAccess[0].GroupName == "" {
		t.Errorf("group access: %+v", sh.GroupAccess)
	}
	sec := section(t, fake, "docs")
	for _, l := range []string{"valid users = ann, bob, jay", "write list = bob"} {
		if !strings.Contains(sec, l) {
			t.Errorf("missing %q:\n%s", l, sec)
		}
	}

	// The preview says who gets access through which group.
	var prev struct {
		Users []struct {
			Username, Level, Via, Reason string
		}
	}
	status, _ = admin.do("POST", "/shares/effective-access", map[string]any{
		"groupAccess": []map[string]any{{"groupId": staff.ID, "level": "read"}, {"groupId": editors.ID, "level": "write"}}}, &prev)
	want(t, "preview", status, 200)
	got := map[string]string{}
	for _, u := range prev.Users {
		got[u.Username] = u.Level + "/" + u.Via
		if u.Via != "" && !strings.Contains(u.Reason, "group "+u.Via) {
			t.Errorf("reason: %+v", u)
		}
	}
	if got["jay"] != "read/staff" || got["ann"] != "write/editors" {
		t.Errorf("preview: %v (the highest group level wins)", got)
	}

	// Jay sees the share in "my shares".
	jc := newClient(t, base)
	jc.login("jay", pw)
	var mine struct {
		Items []struct{ Name, Level string }
	}
	jc.do("GET", "/my/shares", nil, &mine)
	if len(mine.Items) != 1 || mine.Items[0].Level != "read" {
		t.Errorf("my shares through a group: %+v", mine.Items)
	}

	// Membership changes reach Samba at once.
	admin.do("PATCH", "/groups/"+itoa(staff.ID), map[string]any{"members": []int64{ann.ID}}, nil)
	if sec := section(t, fake, "docs"); strings.Contains(sec, "jay") {
		t.Errorf("jay kept access after leaving the group:\n%s", sec)
	}
	// Removing a user's SMB access takes them out of every group.
	admin.do("PATCH", "/users/"+itoa(ann.ID), map[string]any{"smbAccess": false}, nil)
	if m := fake.Groups(); len(m["staff"])+len(m["editors"]) != 0 {
		t.Errorf("ann still in groups: %v", m)
	}
	// Deleting a group takes its access away.
	admin.do("PATCH", "/groups/"+itoa(staff.ID), map[string]any{"members": []int64{jay.ID}}, nil)
	admin.do("DELETE", "/groups/"+itoa(staff.ID), nil, nil)
	if sec := section(t, fake, "docs"); strings.Contains(sec, "jay") {
		t.Errorf("access kept after the group was deleted:\n%s", sec)
	}

	// Group levels follow the share's rules.
	var p problem
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "pub", "path": "/srv/pub", "guestAccess": true,
		"groupAccess": []map[string]any{{"groupId": editors.ID, "level": "upload"}}}, &p)
	if status != 422 || fieldCodes(p)["groupAccess[0].level"] != "not_with_guest" {
		t.Errorf("upload group on a guest share: %d %+v", status, p.Errors)
	}
	status, _ = admin.do("POST", "/shares", map[string]any{"name": "x", "path": "/srv/x",
		"groupAccess": []map[string]any{{"groupId": 999, "level": "read"}}}, &p)
	if status != 422 || fieldCodes(p)["groupAccess[0].groupId"] != "not_found" {
		t.Errorf("unknown group: %d %+v", status, p.Errors)
	}
}

func TestGroupsSurviveABackup(t *testing.T) {
	src, _ := source(t)
	var users struct{ Items []userBody }
	src.do("GET", "/users", nil, &users)
	var jay int64
	for _, u := range users.Items {
		if u.Username == "jay" {
			jay = u.ID
		}
	}
	g := createGroup(t, src, "staff", jay)
	var shares struct {
		Items []struct {
			ID   int64
			Name string
		}
	}
	src.do("GET", "/shares", nil, &shares)
	for _, s := range shares.Items {
		if s.Name == "docs" {
			src.do("PATCH", "/shares/"+itoa(s.ID), map[string]any{"groupAccess": []map[string]any{{"groupId": g.ID, "level": "write"}}}, nil)
		}
	}
	file := exportBackup(t, src, map[string]any{"passphrase": backupPass})

	dst, dstFake, _, _ := activityServer(t)
	var sum restoreSummary
	if status := restore(t, dst, file, nil, &sum); status != 200 {
		t.Fatalf("restore: %d %+v", status, sum)
	}
	dst.login("admin", pw)
	var list struct{ Items []groupBody }
	dst.do("GET", "/groups", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "staff" || len(list.Items[0].Members) != 1 {
		t.Fatalf("groups after restore: %+v", list.Items)
	}
	if m := dstFake.Groups()["staff"]; !slices.Equal(m, []string{"jay"}) {
		t.Errorf("Linux group after restore: %v", dstFake.Groups())
	}
	var sh struct {
		GroupAccess []struct{ GroupName, Level string }
	}
	dst.do("GET", "/shares", nil, &shares)
	for _, s := range shares.Items {
		if s.Name == "docs" {
			dst.do("GET", "/shares/"+itoa(s.ID), nil, &sh)
		}
	}
	if len(sh.GroupAccess) != 1 || sh.GroupAccess[0].GroupName != "staff" || sh.GroupAccess[0].Level != "write" {
		t.Errorf("group access after restore: %+v", sh.GroupAccess)
	}
}
