package sysexec

import (
	"context"
	"fmt"
	"os"
	"strings"

	"netshare-hub/internal/sys"
)

// ScanShare walks a live share's folder through an os.Root, so nothing
// outside the share can be reached, and never follows a link.
func (s *Samba) ScanShare(ctx context.Context, name string, fn func(sys.ShareEntry) error) error {
	root, err := s.openLiveShare(name)
	if err != nil {
		return err
	}
	defer root.Close()
	return sys.WalkShareFS(ctx, root.FS(), fn)
}

// StatFiles looks up files of a live share through an os.Root on the
// share's folder.
func (s *Samba) StatFiles(ctx context.Context, name string, paths []string, hash bool) ([]sys.ShareFile, error) {
	if len(paths) > sys.MaxStatFiles {
		return nil, fmt.Errorf("%w: at most %d files", sys.ErrInvalid, sys.MaxStatFiles)
	}
	root, err := s.openLiveShare(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return sys.StatShareFS(ctx, root.FS(), paths, hash)
}

// openLiveShare finds a live share in the applied config (never trusting a
// path from the caller), checks its folder against the rules, and opens it.
func (s *Samba) openLiveShare(name string) (*os.Root, error) {
	if !sys.ValidShareName(name) {
		return nil, fmt.Errorf("%w: share name", sys.ErrInvalid)
	}
	shares, err := s.applied()
	if err != nil {
		return nil, err
	}
	for _, sh := range shares {
		if !strings.EqualFold(sh.Name, name) || !sh.Live() {
			continue
		}
		if err := s.checkPath(sh); err != nil {
			return nil, err
		}
		return os.OpenRoot(sh.Path)
	}
	return nil, fmt.Errorf("%w: no live share named %q", sys.ErrNotFound, name)
}
