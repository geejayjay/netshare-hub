package sysexec

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"netshare-hub/internal/sys"
)

// Files inspects and creates share directories.
type Files struct {
	runner Runner
	// protected are folders that must not be exposed (see Samba.protected).
	protected []string
}

func NewFiles(r Runner, protected ...string) *Files { return &Files{runner: r, protected: protected} }

func (f *Files) ResolveDir(_ context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", sys.ErrInvalid)
	}
	// EvalSymlinks resolves every link in the path, so the caller checks its
	// policy against where the directory really is.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err // fs.ErrNotExist when missing
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", sys.ErrNotDir
	}
	return resolved, nil
}

func (f *Files) CreateShareDir(ctx context.Context, path string) (string, error) {
	parent, err := f.ResolveDir(ctx, filepath.Dir(path))
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, filepath.Base(path))
	// Check where the folder would REALLY be before creating anything: the
	// parent may be a symbolic link into a system folder.
	if !pathAllowed(filepath.ToSlash(target), f.protected) {
		return "", fmt.Errorf("%w: folder would be inside a protected folder", sys.ErrInvalid)
	}

	gid, err := managedGroupID(ctx, f.runner)
	if err != nil {
		return "", err
	}

	// Mkdir (not MkdirAll) fails if the name already exists, including as a
	// symlink, so we only ever chown a directory we just created ourselves.
	if err := os.Mkdir(target, 0o770); err != nil {
		return "", err // fs.ErrExist when taken
	}
	// root:netshare-hub, rwxrws---: managed users can read and write, new
	// files inherit the group, and nobody else can enter.
	if err := os.Chown(target, 0, gid); err == nil {
		err = os.Chmod(target, 0o770|fs.ModeSetgid)
	}
	if err != nil {
		os.Remove(target)
		return "", fmt.Errorf("set permissions on %s: %w", target, err)
	}
	return target, nil
}
