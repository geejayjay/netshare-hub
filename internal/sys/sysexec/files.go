package sysexec

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

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

	// Mkdir (not MkdirAll) fails if the name already exists, including as a
	// symlink, so we only ever chown a directory we just created ourselves.
	if err := os.Mkdir(target, 0o700); err != nil {
		return "", err // fs.ErrExist when taken
	}
	// root:root, rwx------: nobody else can enter until the share's ACL
	// (set by ApplyConfig) names who may. The owning group and "other" get
	// nothing, and so nothing through the default ACL either: new files are
	// reachable only through the share's own entries.
	err = os.Chown(target, 0, 0)
	if err == nil {
		err = os.Chmod(target, 0o700)
	}
	if err != nil {
		os.Remove(target)
		return "", fmt.Errorf("set permissions on %s: %w", target, err)
	}
	return target, nil
}

// BenchmarkDisk runs a sequential write+fsync and read benchmark in an allowed
// share directory. It never touches protected system directories.
func (f *Files) BenchmarkDisk(ctx context.Context, path string, sizeMiB int) (sys.DiskBenchmark, error) {
	if sizeMiB < 1 || sizeMiB > 128 {
		return sys.DiskBenchmark{}, fmt.Errorf("%w: sizeMiB must be 1..128", sys.ErrInvalid)
	}
	resolved, err := f.ResolveDir(ctx, path)
	if err != nil {
		return sys.DiskBenchmark{}, err
	}
	if !pathAllowed(filepath.ToSlash(resolved), f.protected) {
		return sys.DiskBenchmark{}, fmt.Errorf("%w: folder is inside a protected directory", sys.ErrInvalid)
	}

	tmp, err := os.CreateTemp(resolved, ".netshare-speedtest-*")
	if err != nil {
		return sys.DiskBenchmark{}, fmt.Errorf("create benchmark file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	chunk := make([]byte, 1<<20) // 1 MiB non-zero pattern
	for i := range chunk {
		chunk[i] = byte((i * 31) ^ 0xA5)
	}

	wStart := time.Now()
	for i := 0; i < sizeMiB; i++ {
		if ctx.Err() != nil {
			_ = tmp.Close()
			return sys.DiskBenchmark{}, ctx.Err()
		}
		if _, err := tmp.Write(chunk); err != nil {
			_ = tmp.Close()
			return sys.DiskBenchmark{}, err
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return sys.DiskBenchmark{}, err
	}
	wDur := time.Since(wStart)
	if err := tmp.Close(); err != nil {
		return sys.DiskBenchmark{}, err
	}

	rf, err := os.Open(tmpPath)
	if err != nil {
		return sys.DiskBenchmark{}, err
	}
	rStart := time.Now()
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(io.Discard, rf, buf); err != nil {
		_ = rf.Close()
		return sys.DiskBenchmark{}, err
	}
	rDur := time.Since(rStart)
	_ = rf.Close()

	wSec := math.Max(wDur.Seconds(), 0.001)
	rSec := math.Max(rDur.Seconds(), 0.001)
	return sys.DiskBenchmark{
		Path:      filepath.Clean(resolved),
		SizeMiB:   sizeMiB,
		WriteMBps: math.Round((float64(sizeMiB)/wSec)*10) / 10,
		ReadMBps:  math.Round((float64(sizeMiB)/rSec)*10) / 10,
		WriteMs:   wDur.Milliseconds(),
		ReadMs:    rDur.Milliseconds(),
	}, nil
}
