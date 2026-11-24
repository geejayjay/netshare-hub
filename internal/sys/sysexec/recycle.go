package sysexec

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"netshare-hub/internal/sys"
)

// PurgeRecycle removes what has been in a share's recycle bin longer than
// its retention. The time an item went into the bin is its change time
// (moving a file there, by Samba or the file manager, updates it).
//
// It walks the bin through an os.Root on the share's folder, so nothing
// outside the share can be reached, and never follows a link (a link in
// the bin is removed as a link).
func (s *Samba) PurgeRecycle(ctx context.Context, name string) (sys.PurgeStats, error) {
	var stats sys.PurgeStats
	if !sys.ValidShareName(name) {
		return stats, fmt.Errorf("%w: share name", sys.ErrInvalid)
	}
	shares, err := s.applied()
	if err != nil {
		return stats, err
	}
	var share *sys.ShareSpec
	for i := range shares {
		if strings.EqualFold(shares[i].Name, name) {
			share = &shares[i]
		}
	}
	if share == nil || share.RecycleDays <= 0 {
		return stats, fmt.Errorf("%w: no share %q with a recycle bin", sys.ErrNotFound, name)
	}
	if err := s.checkPath(*share); err != nil {
		return stats, err
	}
	root, err := os.OpenRoot(share.Path)
	if err != nil {
		return stats, err
	}
	defer root.Close()
	if info, err := root.Lstat(sys.RecycleDir); errors.Is(err, fs.ErrNotExist) {
		return stats, nil
	} else if err != nil {
		return stats, err
	} else if !info.IsDir() {
		return stats, nil // not a folder (someone made a file of that name): leave it
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	cutoff := now.Add(-time.Duration(share.RecycleDays) * 24 * time.Hour)
	_, err = purgeDir(ctx, root, sys.RecycleDir, cutoff, &stats)
	return stats, err
}

// purgeDir removes expired entries below dir and reports whether dir is
// empty afterwards.
func purgeDir(ctx context.Context, root *os.Root, dir string, cutoff time.Time, stats *sys.PurgeStats) (empty bool, err error) {
	f, err := root.Open(dir)
	if err != nil {
		return false, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return false, err
	}
	left := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		p := filepath.ToSlash(filepath.Join(dir, e.Name()))
		info, err := root.Lstat(p)
		if err != nil {
			continue // gone meanwhile
		}
		if info.IsDir() {
			emptyNow, err := purgeDir(ctx, root, p, cutoff, stats)
			if err != nil {
				return false, err
			}
			if emptyNow && changed(info).Before(cutoff) {
				if root.Remove(p) == nil {
					stats.Removed++
					continue
				}
			}
			left++
			continue
		}
		if changed(info).Before(cutoff) {
			if err := root.Remove(p); err == nil {
				stats.Removed++
				stats.Bytes += info.Size()
				continue
			}
		}
		left++
	}
	return left == 0, nil
}
