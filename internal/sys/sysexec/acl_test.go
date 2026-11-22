package sysexec

import (
	"errors"
	"testing"

	"netshare-hub/internal/sys"
)

const testPasswd = `root:x:0:0:root:/root:/bin/bash
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
netshare:x:999:999::/nonexistent:/usr/sbin/nologin
plex:x:998:998::/var/lib/plex:/usr/sbin/nologin
jay:x:1001:1001:NetShare Hub user:/nonexistent:/usr/sbin/nologin
ana:x:1002:1002:NetShare Hub user:/nonexistent:/usr/sbin/nologin
admin:x:1000:1000::/home/admin:/bin/bash
`

// "admin" is a normal login of the machine, not one NetShare Hub made.
const testGroup = "netshare-hub:x:1500:jay,ana\n"

func testIDs(t *testing.T) identities {
	t.Helper()
	ids, err := parseIdentities(testPasswd, testGroup)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestACLEncodeDecode(t *testing.T) {
	a := acl{
		{tagOther, 0, noID},
		{tagUser, permR | permX, 1002},
		{tagUserObj, 7, noID},
		{tagMask, 7, noID},
		{tagGroupObj, 0, noID},
		{tagUser, 7, 1001},
	}
	got, err := decodeACL(a.encode())
	if err != nil {
		t.Fatal(err)
	}
	if !got.equal(a) {
		t.Errorf("round trip: %v, want %v", got, a)
	}
	// The kernel needs entries in order: by tag, then by ID.
	if got[0].Tag != tagUserObj || got[1].ID != 1001 || got[2].ID != 1002 || got[len(got)-1].Tag != tagOther {
		t.Errorf("not sorted: %v", got)
	}
	for _, bad := range [][]byte{nil, {1, 0, 0, 0}, {2, 0, 0, 0, 1}} {
		if _, err := decodeACL(bad); err == nil {
			t.Errorf("decoded malformed %v", bad)
		}
	}
}

func TestShareGrants(t *testing.T) {
	ids := testIDs(t)
	top, inner, err := shareGrants(sys.ShareSpec{
		Name: "team", Path: "/srv/team",
		Readers: []string{"jay", "ana"}, Writers: []string{"jay"},
	}, ids)
	if err != nil {
		t.Fatal(err)
	}
	want := []grant{{tagUser, 1001, 7}, {tagUser, 1002, permR | permX}}
	if len(inner) != 2 || inner[0] != want[0] || inner[1] != want[1] || len(top) != 2 {
		t.Errorf("grants: top %v inner %v", top, inner)
	}

	// Drop box: the uploader gets -wx on the top folder only.
	top, inner, err = shareGrants(sys.ShareSpec{
		Name: "inbox", Path: "/srv/inbox",
		Readers: []string{"jay"}, Writers: []string{"jay"}, Uploaders: []string{"ana"},
	}, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(inner) != 1 || len(top) != 2 || top[1] != (grant{tagUser, 1002, permW | permX}) {
		t.Errorf("drop box grants: top %v inner %v", top, inner)
	}

	// Guest: nobody and the managed group read; plain readers need no entry.
	top, inner, err = shareGrants(sys.ShareSpec{
		Name: "tv", Path: "/srv/tv", Guest: true,
		Readers: []string{"jay", "ana"}, Writers: []string{"jay"},
	}, ids)
	if err != nil {
		t.Fatal(err)
	}
	wantGuest := []grant{{tagUser, 1001, 7}, {tagUser, 65534, permR | permX}, {tagGroup, 1500, permR | permX}}
	if len(inner) != 3 || inner[0] != wantGuest[0] || inner[1] != wantGuest[1] || inner[2] != wantGuest[2] {
		t.Errorf("guest grants: %v, want %v", inner, wantGuest)
	}
	_ = top
}

// The ACL must never become a way to let a system account (or the web
// server's own account, or a normal login of the machine) into a folder.
func TestShareGrantsRefuseUnmanagedAccounts(t *testing.T) {
	ids := testIDs(t)
	for _, name := range []string{"root", "netshare", "plex", "admin", "nobody", "ghost"} {
		_, _, err := shareGrants(sys.ShareSpec{Name: "x", Path: "/srv/x", Readers: []string{name}}, ids)
		if !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
		_, _, err = shareGrants(sys.ShareSpec{Name: "x", Path: "/srv/x", Uploaders: []string{name}}, ids)
		if !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("%s as uploader: got %v, want ErrInvalid", name, err)
		}
	}
}

func TestMergeKeepsWhatIsNotOurs(t *testing.T) {
	ids := testIDs(t)
	current := acl{
		{tagUserObj, 7, noID},
		{tagGroupObj, permR | permX, noID},
		{tagUser, permR | permX, 998},   // plex, added by the admin: kept
		{tagUser, 7, 1002},              // ana, managed, no longer on the share: removed
		{tagUser, 7, 1234},              // a deleted account: removed (its UID may be reused)
		{tagUser, permR, overflowUID},   // "nobody", the guest account, and not a guest share: removed
		{tagGroup, permR | permX, 1500}, // managed group, not a guest share: removed
		{tagMask, permR | permX, noID},
		{tagOther, 0, noID},
	}
	got := merge(current, []grant{{tagUser, 1001, 7}}, ids.owned)
	want := acl{
		{tagUserObj, 7, noID},
		{tagUser, 7, 1001},
		{tagUser, permR | permX, 998},
		{tagGroupObj, permR | permX, noID},
		{tagMask, 7, noID}, // union of the group class: nothing cut down
		{tagOther, 0, noID},
	}
	if !got.equal(want) {
		t.Errorf("merge:\n got %v\nwant %v", got.sorted(), want.sorted())
	}

	// With no named entries left, there is no mask either.
	plain := merge(acl{{tagUserObj, 7, noID}, {tagGroupObj, 0, noID}, {tagUser, 7, 1001}, {tagMask, 7, noID}, {tagOther, 0, noID}}, nil, ids.owned)
	if !plain.equal(acl{{tagUserObj, 7, noID}, {tagGroupObj, 0, noID}, {tagOther, 0, noID}}) {
		t.Errorf("merge without grants: %v", plain)
	}
}

func TestForFileDropsExecute(t *testing.T) {
	g := []grant{{tagUser, 1001, 7}, {tagUser, 1002, permR | permX}}
	files := forFile(g, 0o640)
	if files[0].Perm != permR|permW || files[1].Perm != permR {
		t.Errorf("plain file: %v", files)
	}
	if exe := forFile(g, 0o750); exe[0].Perm != 7 {
		t.Errorf("an executable keeps x: %v", exe)
	}
	if g[0].Perm != 7 {
		t.Error("forFile changed its input")
	}
}

func TestSameAccess(t *testing.T) {
	a := sys.ShareSpec{Name: "m", Path: "/srv/m", Readers: []string{"jay", "ana"}, Writers: []string{"jay"}}
	b := a
	b.Readers = []string{"ana", "jay"}
	b.Comment = "a new comment does not touch the folder"
	if !sameAccess(a, b) {
		t.Error("order and comment must not count as an access change")
	}
	c := a
	c.Writers = nil
	if sameAccess(a, c) {
		t.Error("losing a writer is an access change")
	}
	// On a guest share, plain readers make no difference.
	g1 := sys.ShareSpec{Name: "g", Path: "/srv/g", Guest: true, Readers: []string{"ana"}}
	g2 := sys.ShareSpec{Name: "g", Path: "/srv/g", Guest: true}
	if !sameAccess(g1, g2) {
		t.Error("guest share readers are not recorded and must not count")
	}
}
