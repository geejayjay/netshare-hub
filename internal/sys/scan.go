package sys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// ShareEntry is one file or folder found in a share by Samba.ScanShare.
type ShareEntry struct {
	// Path is relative to the share's folder, with forward slashes.
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir,omitempty"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"` // Unix seconds
}

// Limits of Samba.StatFiles: how many paths one call may name, and the
// biggest file it hashes (bigger ones get no hash).
const (
	MaxStatFiles     = 1000
	MaxHashFileBytes = 512 << 20
)

// UploadsDir is where the file manager keeps unfinished uploads, inside a
// share's folder.
const UploadsDir = ".nsh-uploads"

// scanSkipped are folders a scan never enters: they hold no files of the
// share's own (the recycle bin, unfinished uploads, a desktop's trash).
var scanSkipped = map[string]bool{RecycleDir: true, UploadsDir: true, ".Trash": true}

// WalkShareFS is the walk behind Samba.ScanShare: it calls fn for every
// file and folder under fsys's root (not for the root itself). Links are
// never followed or reported, nor are devices, sockets and pipes. Entries
// that cannot be read are skipped. It stops at the first error fn returns,
// or when ctx is done.
func WalkShareFS(ctx context.Context, fsys fs.FS, fn func(ShareEntry) error) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if p == "." {
				return err // the share's folder itself is unreadable
			}
			return nil
		}
		if p == "." || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if scanSkipped[d.Name()] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil
		}
		return fn(ShareEntry{Path: p, IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime().Unix()})
	})
}

// ShareFile is what Samba.StatFiles reports for one path.
type ShareFile struct {
	ShareEntry
	// SHA256 (hex) is set when asked for, for regular files of at most
	// MaxHashFileBytes.
	SHA256 string `json:"sha256,omitempty"`
}

// StatShareFS is the lookup behind Samba.StatFiles. Paths that are not
// valid relative paths, missing, links, inside a folder a scan skips, or
// not a regular file or folder are left out of the result.
func StatShareFS(ctx context.Context, fsys fs.FS, paths []string, hash bool) ([]ShareFile, error) {
	if len(paths) > MaxStatFiles {
		return nil, fmt.Errorf("%w: at most %d files", ErrInvalid, MaxStatFiles)
	}
	out := make([]ShareFile, 0, len(paths))
	for _, p := range paths {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if p == "." || !fs.ValidPath(p) || inSkipped(p) {
			continue
		}
		info, err := fs.Lstat(fsys, p)
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue
		}
		f := ShareFile{ShareEntry: ShareEntry{Path: p, IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime().Unix()}}
		if hash && info.Mode().IsRegular() && info.Size() <= MaxHashFileBytes {
			f.SHA256, _ = hashOne(fsys, p)
		}
		out = append(out, f)
	}
	return out, nil
}

func inSkipped(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if scanSkipped[part] {
			return true
		}
	}
	return false
}

func hashOne(fsys fs.FS, p string) (string, bool) {
	f, err := fsys.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxHashFileBytes {
		return "", false
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, MaxHashFileBytes+1)); err != nil {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}
