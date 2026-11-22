package sysexec

// Share permissions on the filesystem.
//
// Samba decides who may connect to a share; the kernel decides what they
// may do with each file. Both must say the same thing, or users "can read
// but not write" (or, worse, the web file manager, which runs as the user,
// reaches more than SMB does). So every share's folder carries a POSIX ACL
// built from the same access lists as the Samba config:
//
//	writer     user:<name>:rwx   (files rw-)
//	reader     user:<name>:r-x   (files r--)
//	uploader   user:<name>:-wx   on the top folder only: can add, cannot list
//	guest      user:nobody:r-x and group:netshare-hub:r-x
//
// Folders also get the same entries as their default ACL (uploaders
// excepted), so new files and folders inherit them.
//
// Only "our" entries are ever changed: named entries for managed accounts,
// the guest account, the managed group, and accounts that no longer exist
// (their UID could be given to a new user). The owner, owning group, other
// users, and entries an admin added for other accounts are kept as they are.
//
// The ACL is stored in the extended attributes system.posix_acl_access and
// system.posix_acl_default, in the kernel's binary format; this file holds
// the format and the rules, acl_linux.go the system calls.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"netshare-hub/internal/sys"
)

const (
	aclVersion = 2

	tagUserObj  = 0x01
	tagUser     = 0x02
	tagGroupObj = 0x04
	tagGroup    = 0x08
	tagMask     = 0x10
	tagOther    = 0x20

	noID = 0xffffffff

	permR = 4
	permW = 2
	permX = 1

	// overflowUID is how an unprivileged container shows IDs it cannot map.
	overflowUID = 65534
)

type aclEntry struct {
	Tag  uint16
	Perm uint16
	ID   uint32
}

type acl []aclEntry

func decodeACL(b []byte) (acl, error) {
	if len(b) < 4 || (len(b)-4)%8 != 0 || binary.LittleEndian.Uint32(b) != aclVersion {
		return nil, errors.New("malformed ACL")
	}
	var out acl
	for i := 4; i < len(b); i += 8 {
		out = append(out, aclEntry{
			Tag:  binary.LittleEndian.Uint16(b[i:]),
			Perm: binary.LittleEndian.Uint16(b[i+2:]),
			ID:   binary.LittleEndian.Uint32(b[i+4:]),
		})
	}
	return out, nil
}

func (a acl) encode() []byte {
	a = a.sorted()
	b := make([]byte, 4+8*len(a))
	binary.LittleEndian.PutUint32(b, aclVersion)
	for i, e := range a {
		o := 4 + 8*i
		binary.LittleEndian.PutUint16(b[o:], e.Tag)
		binary.LittleEndian.PutUint16(b[o+2:], e.Perm)
		binary.LittleEndian.PutUint32(b[o+4:], e.ID)
	}
	return b
}

// sorted orders entries the way the kernel requires: by tag, then by ID.
func (a acl) sorted() acl {
	out := slices.Clone(a)
	slices.SortFunc(out, func(x, y aclEntry) int {
		if x.Tag != y.Tag {
			return int(x.Tag) - int(y.Tag)
		}
		switch {
		case x.ID < y.ID:
			return -1
		case x.ID > y.ID:
			return 1
		}
		return 0
	})
	return out
}

func (a acl) equal(b acl) bool { return slices.Equal(a.sorted(), b.sorted()) }

// aclFromMode is the ACL a file without one behaves as.
func aclFromMode(mode fs.FileMode) acl {
	p := uint16(mode.Perm())
	return acl{
		{tagUserObj, p >> 6 & 7, noID},
		{tagGroupObj, p >> 3 & 7, noID},
		{tagOther, p & 7, noID},
	}
}

// base returns only the owner, owning group and other entries. The owning
// group's real permissions are in its entry, not in the mode's group bits
// (those show the mask when there is one).
func (a acl) base() acl {
	var out acl
	for _, e := range a {
		if e.Tag == tagUserObj || e.Tag == tagGroupObj || e.Tag == tagOther {
			out = append(out, e)
		}
	}
	return out
}

// grant is a named entry NetShare Hub wants on a folder or file.
type grant struct {
	Tag  uint16 // tagUser or tagGroup
	ID   uint32
	Perm uint16 // for folders; files drop x (see forFile)
}

// forFile turns folder permissions into file permissions: no execute bit,
// unless the file is already executable (like setfacl's "X").
func forFile(grants []grant, mode fs.FileMode) []grant {
	if mode.Perm()&0o111 != 0 {
		return grants
	}
	out := make([]grant, len(grants))
	for i, g := range grants {
		g.Perm &^= permX
		out[i] = g
	}
	return out
}

// merge returns current with every entry owned by NetShare Hub replaced by
// grants, and the mask recomputed. Everything else is kept.
func merge(current acl, grants []grant, owned func(aclEntry) bool) acl {
	isGranted := func(e aclEntry) bool {
		return slices.ContainsFunc(grants, func(g grant) bool { return g.Tag == e.Tag && g.ID == e.ID })
	}
	var out acl
	for _, e := range current {
		if e.Tag == tagMask {
			continue
		}
		if (e.Tag == tagUser || e.Tag == tagGroup) && (owned(e) || isGranted(e)) {
			continue
		}
		out = append(out, e)
	}
	for _, g := range grants {
		out = append(out, aclEntry{g.Tag, g.Perm, g.ID})
	}
	// With named entries the kernel needs a mask. Make it the union of the
	// group class, so no entry is cut down (what setfacl does too).
	var mask uint16
	named := false
	for _, e := range out {
		switch e.Tag {
		case tagUser, tagGroup:
			named = true
			mask |= e.Perm
		case tagGroupObj:
			mask |= e.Perm
		}
	}
	if named {
		out = append(out, aclEntry{tagMask, mask, noID})
	}
	return out.sorted()
}

// identities is what the permission code needs to know about accounts.
type identities struct {
	uid        map[string]uint32 // every account, by name
	known      map[uint32]bool   // every account's UID
	managed    map[uint32]bool   // UIDs of accounts NetShare Hub created
	managedGID uint32
	nobody     uint32 // the guest account
	hasNobody  bool
}

// parseIdentities reads "getent passwd" and "getent group netshare-hub".
func parseIdentities(passwd, group string) (identities, error) {
	ids := identities{uid: map[string]uint32{}, known: map[uint32]bool{}, managed: map[uint32]bool{}}
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) < 3 || f[0] == "" {
			continue
		}
		uid, err := strconv.ParseUint(f[2], 10, 32)
		if err != nil {
			continue
		}
		ids.uid[f[0]] = uint32(uid)
		ids.known[uint32(uid)] = true
	}
	if n, ok := ids.uid["nobody"]; ok {
		ids.nobody, ids.hasNobody = n, true
	}

	// name:x:gid:member,member
	f := strings.Split(strings.TrimSpace(group), ":")
	if len(f) < 3 || f[0] != sys.ManagedGroup {
		return identities{}, fmt.Errorf("unexpected getent output for group %q", sys.ManagedGroup)
	}
	gid, err := strconv.ParseUint(f[2], 10, 32)
	if err != nil {
		return identities{}, fmt.Errorf("unexpected gid for group %q", sys.ManagedGroup)
	}
	ids.managedGID = uint32(gid)
	if len(f) >= 4 && f[3] != "" {
		for _, name := range strings.Split(f[3], ",") {
			if uid, ok := ids.uid[name]; ok && uid >= sys.MinManagedUID {
				ids.managed[uid] = true
			}
		}
	}
	return ids, nil
}

// owned reports whether an ACL entry is NetShare Hub's to change.
func (ids identities) owned(e aclEntry) bool {
	switch e.Tag {
	case tagUser:
		if ids.managed[e.ID] || (ids.hasNobody && e.ID == ids.nobody) {
			return true
		}
		// A deleted account's entry would grant its access to whoever gets
		// the UID next. (The overflow UID is not a real account.)
		return !ids.known[e.ID] && e.ID >= sys.MinManagedUID && e.ID != overflowUID
	case tagGroup:
		return e.ID == ids.managedGID
	}
	return false
}

// shareGrants turns a share's access into ACL entries: top for the share's
// own folder, inner for everything inside it (and every folder's default).
// Every named account must be managed: the ACL is never a way to give a
// system account (or the web server's own) access to a folder.
func shareGrants(s sys.ShareSpec, ids identities) (top, inner []grant, err error) {
	s = normalized(s)
	user := func(name string, perm uint16) (grant, error) {
		uid, ok := ids.uid[name]
		if !ok || !ids.managed[uid] {
			return grant{}, fmt.Errorf("%w: %q is not an account NetShare Hub manages", sys.ErrInvalid, name)
		}
		return grant{tagUser, uid, perm}, nil
	}
	for _, name := range s.Readers {
		perm := uint16(permR | permX)
		if slices.Contains(s.Writers, name) {
			perm |= permW
		}
		g, err := user(name, perm)
		if err != nil {
			return nil, nil, err
		}
		inner = append(inner, g)
	}
	if s.Guest {
		if !ids.hasNobody {
			return nil, nil, fmt.Errorf("guest access needs the \"nobody\" account, which does not exist")
		}
		inner = append(inner, grant{tagUser, ids.nobody, permR | permX}, grant{tagGroup, ids.managedGID, permR | permX})
	}
	top = slices.Clone(inner)
	for _, name := range s.Uploaders {
		g, err := user(name, permW|permX)
		if err != nil {
			return nil, nil, err
		}
		top = append(top, g)
	}
	return top, inner, nil
}

// normalized drops what does not change access: on a guest share every
// managed user may read anyway, so only the writers need their own entry
// (the applied config does not record the other readers either).
func normalized(s sys.ShareSpec) sys.ShareSpec {
	if s.Guest {
		s.Readers = slices.Clone(s.Writers)
	}
	if !s.Live() {
		s.Readers, s.Writers, s.Uploaders = nil, nil, nil
	}
	return s
}

// sameAccess reports whether two specs give the same folder the same ACL.
func sameAccess(a, b sys.ShareSpec) bool {
	a, b = normalized(a), normalized(b)
	set := func(l []string) []string { s := slices.Clone(l); slices.Sort(s); return s }
	return a.Path == b.Path && a.Guest == b.Guest &&
		slices.Equal(set(a.Readers), set(b.Readers)) &&
		slices.Equal(set(a.Writers), set(b.Writers)) &&
		slices.Equal(set(a.Uploaders), set(b.Uploaders))
}
