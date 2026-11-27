//go:build linux

package sysexec

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"netshare-hub/internal/sys"
)

const (
	xattrAccess  = "system.posix_acl_access"
	xattrDefault = "system.posix_acl_default"
)

// getXattr returns an extended attribute, or nil when it is not set.
func getXattr(fd int, name string) ([]byte, error) {
	buf := make([]byte, 256)
	for {
		n, err := unix.Fgetxattr(fd, name, buf)
		switch {
		case err == nil:
			return buf[:n], nil
		case errors.Is(err, unix.ENODATA):
			return nil, nil
		case errors.Is(err, unix.ERANGE) && len(buf) < 1<<20:
			buf = make([]byte, len(buf)*4)
		default:
			return nil, aclErr(err)
		}
	}
}

func aclErr(err error) error {
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) {
		return sys.ErrACLUnsupported
	}
	return err
}

// accessACL returns a file's ACL: the stored one, or the one its mode means.
func accessACL(fd int, mode fs.FileMode) (acl, error) {
	raw, err := getXattr(fd, xattrAccess)
	if err != nil || raw == nil {
		return aclFromMode(mode), err
	}
	return decodeACL(raw)
}

// defaultACL returns a folder's default ACL, or nil when it has none.
func defaultACL(fd int) (acl, error) {
	raw, err := getXattr(fd, xattrDefault)
	if err != nil || raw == nil {
		return nil, err
	}
	return decodeACL(raw)
}

// applyItem gives one open file or folder our entries. def is the folder's
// default (ignored for files). It reports whether anything changed.
func applyItem(f *os.File, info fs.FileInfo, access, def []grant, owned func(aclEntry) bool) (bool, error) {
	fd := int(f.Fd())
	if !info.IsDir() {
		access = forFile(access, info.Mode())
	}
	current, err := accessACL(fd, info.Mode())
	if err != nil {
		return false, err
	}
	changed := false
	if want := merge(current, access, owned); !want.equal(current) {
		if err := unix.Fsetxattr(fd, xattrAccess, want.encode(), 0); err != nil {
			return false, aclErr(err)
		}
		changed = true
	}
	if !info.IsDir() {
		return changed, nil
	}

	currentDef, err := defaultACL(fd)
	if err != nil {
		return changed, err
	}
	start := currentDef
	if start == nil {
		if len(def) == 0 {
			return changed, nil // no default ACL, and none needed
		}
		// A new default starts from the folder's own owner/group/other.
		start = current.base()
	}
	if want := merge(start, def, owned); currentDef == nil || !want.equal(currentDef) {
		if err := unix.Fsetxattr(fd, xattrDefault, want.encode(), 0); err != nil {
			return changed, aclErr(err)
		}
		changed = true
	}
	return changed, nil
}

// openShareDir opens a share's folder without following a link in its last
// part, then checks where the open folder REALLY is: the folder cannot be
// swapped for a link into a protected folder between check and use.
func openShareDir(dir string, protected []string) (*os.File, error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
	if err != nil || !pathAllowed(real, protected) {
		f.Close()
		return nil, fmt.Errorf("%w: %s is not a folder that may be shared", sys.ErrInvalid, dir)
	}
	return f, nil
}

// aclBackup is a share folder's ACL before a change, to put it back.
type aclBackup struct {
	path   string
	mode   fs.FileMode // permission and sticky bits
	access acl
	def    acl // nil: the folder had no default ACL
}

func (b *aclBackup) restore(protected []string) error {
	f, err := openShareDir(b.path, protected)
	if err != nil {
		return err
	}
	defer f.Close()
	fd := int(f.Fd())
	if err := unix.Fsetxattr(fd, xattrAccess, b.access.encode(), 0); err != nil {
		return err
	}
	if err := f.Chmod(b.mode); err != nil {
		return err
	}
	if b.def == nil {
		if err := unix.Fremovexattr(fd, xattrDefault); err != nil && !errors.Is(err, unix.ENODATA) {
			return err
		}
		return nil
	}
	return unix.Fsetxattr(fd, xattrDefault, b.def.encode(), 0)
}

// setShareACL gives a share's own folder the share's access. It returns how
// the folder was before, or nil when the folder does not exist (Samba will
// report the share as unavailable; there is nothing to protect).
//
// A drop box also gets the sticky bit: uploaders may add files to the
// folder (write access to it), and without the bit that would also let
// them delete or replace someone else's upload by guessing its name. With
// it, only a file's owner can remove or rename it. wasDropBox says the
// previous version of the share had uploaders, so the bit is NetShare
// Hub's to clear; a sticky bit an admin set on another folder is kept.
func setShareACL(s sys.ShareSpec, ids identities, protected []string, wasDropBox bool) (*aclBackup, error) {
	top, inner, err := shareGrants(s, ids)
	if err != nil {
		return nil, err
	}
	f, err := openShareDir(s.Path, protected)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	fd := int(f.Fd())
	backup := &aclBackup{path: s.Path, mode: info.Mode() & (fs.ModePerm | fs.ModeSticky | fs.ModeSetgid | fs.ModeSetuid)}
	if backup.access, err = accessACL(fd, info.Mode()); err != nil {
		return nil, &sys.ACLError{Share: s.Name, Path: s.Path, Err: err}
	}
	if backup.def, err = defaultACL(fd); err != nil {
		return nil, &sys.ACLError{Share: s.Name, Path: s.Path, Err: err}
	}
	if _, err := applyItem(f, info, top, inner, ids.owned); err != nil {
		return nil, &sys.ACLError{Share: s.Name, Path: s.Path, Err: err}
	}
	if dropBox := len(normalized(s).Uploaders) > 0; dropBox || wasDropBox {
		if err := setSticky(f, dropBox); err != nil {
			backup.restore(protected)
			return nil, &sys.ACLError{Share: s.Name, Path: s.Path, Err: err}
		}
	}
	return backup, nil
}

func setSticky(f *os.File, on bool) error {
	info, err := f.Stat() // after the ACL change: group bits now show the mask
	if err != nil {
		return err
	}
	mode := info.Mode() & (fs.ModePerm | fs.ModeSetgid | fs.ModeSetuid)
	if on {
		mode |= fs.ModeSticky
	}
	if mode == info.Mode()&(fs.ModePerm|fs.ModeSticky|fs.ModeSetgid|fs.ModeSetuid) {
		return nil
	}
	return f.Chmod(mode)
}

// fixShareTree applies a share's access to everything inside its folder.
//
// The walk is confined to the folder with os.Root and never follows a
// symbolic link, so a link a user planted (to /etc/shadow, say) is skipped,
// not given an ACL. Files with several hard links are skipped as well: one
// of the other names may be outside the share.
func fixShareTree(ctx context.Context, s sys.ShareSpec, ids identities, protected []string) (sys.PermissionStats, error) {
	var stats sys.PermissionStats
	top, inner, err := shareGrants(s, ids)
	if err != nil {
		return stats, err
	}
	fail := func(err error) (sys.PermissionStats, error) {
		return stats, &sys.ACLError{Share: s.Name, Path: s.Path, Err: err}
	}

	f, err := openShareDir(s.Path, protected)
	if err != nil {
		return stats, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if changed, err := applyItem(f, info, top, inner, ids.owned); err != nil {
		return fail(err)
	} else if changed {
		stats.Updated++
	}

	// Open the root through the folder already checked, not by its name.
	root, err := os.OpenRoot("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
	if err != nil {
		return fail(err)
	}
	defer root.Close()

	const flags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_NOCTTY | syscall.O_CLOEXEC
	dirs := []string{"."}
	for len(dirs) > 0 {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]

		d, err := root.OpenFile(dir, flags|syscall.O_DIRECTORY, 0)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
			continue // removed or replaced while we walked
		}
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			stats.Skipped++ // unmapped host UID in unprivileged LXC or inaccessible dir
			continue
		}
		if err != nil {
			return fail(err)
		}
		for {
			entries, readErr := d.ReadDir(512)
			for _, e := range entries {
				if err := ctx.Err(); err != nil {
					d.Close()
					return stats, err
				}
				if !e.Type().IsDir() && !e.Type().IsRegular() {
					stats.Skipped++ // symbolic link, device, socket, pipe
					continue
				}
				rel := path.Join(dir, e.Name())
				changed, isDir, err := fixItem(root, rel, flags, inner, ids.owned)
				switch {
				case errors.Is(err, errSkipped):
					stats.Skipped++
				case errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES):
					// In unprivileged LXC containers, files/folders owned by unmapped host
					// UIDs (65534/nobody) return EPERM on fsetxattr. Skip them and keep
					// fixing the rest of the share instead of aborting the whole walk.
					stats.Skipped++
					if isDir {
						dirs = append(dirs, rel)
					}
				case err != nil:
					d.Close()
					return fail(fmt.Errorf("%s: %w", rel, err))
				default:
					if changed {
						stats.Updated++
					}
					if isDir {
						dirs = append(dirs, rel)
					}
				}
			}
			if readErr != nil {
				break // io.EOF, or the folder went away
			}
		}
		d.Close()
	}
	return stats, nil
}

var errSkipped = errors.New("skipped")

func fixItem(root *os.Root, rel string, flags int, grants []grant, owned func(aclEntry) bool) (changed, isDir bool, err error) {
	g, err := root.OpenFile(rel, flags, 0)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
		return false, false, errSkipped // removed, or replaced by a link
	}
	if err != nil {
		return false, false, err
	}
	defer g.Close()
	info, err := g.Stat()
	if err != nil {
		return false, false, err
	}
	switch {
	case info.IsDir():
		changed, err = applyItem(g, info, grants, grants, owned)
		return changed, true, err
	case info.Mode().IsRegular():
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
			return false, false, errSkipped
		}
		changed, err = applyItem(g, info, grants, nil, owned)
		return changed, false, err
	default:
		return false, false, errSkipped // replaced while we walked
	}
}
