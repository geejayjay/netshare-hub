// Package thumbs generates and caches on-demand thumbnails for images (pure Go)
// and videos (optional ffmpeg detected at startup). All file reads go through
// the authenticated user's file worker so share/kernel permissions are always enforced.
package thumbs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/modules/files"
)

const (
	defaultSize    = 320
	minSize        = 64
	maxSize        = 640
	maxImageBytes  = 32 << 20 // 32 MiB cap for pure-Go image decode
	maxVideoBytes  = 64 << 20 // 64 MiB cap when piping video to ffmpeg
	maxConcurrent  = 4
	cacheRetention = 30 * 24 * time.Hour
	ffmpegTimeout  = 8 * time.Second
)

// FileSource reads files strictly through the user's file worker.
type FileSource interface {
	Stat(ctx context.Context, a files.Actor, share, p string) (files.Entry, error)
	Download(ctx context.Context, a files.Actor, share, p, rangeHeader string) (*fileworker.Download, error)
}

// ThumbnailCapabilities reports which media types can be thumbnailed on this host.
type ThumbnailCapabilities struct {
	Images bool `json:"images"`
	Video  bool `json:"video"`
}

// Service generates and caches thumbnails on disk.
type Service struct {
	cacheDir   string
	files      FileSource
	ffmpegPath string
	sem        chan struct{}
}

// NewService creates the thumbnail service and probes for optional ffmpeg at startup.
func NewService(dataDir string, src FileSource) *Service {
	cacheDir := filepath.Join(dataDir, "thumbs")
	_ = os.MkdirAll(cacheDir, 0o750)
	ffmpegPath, _ := exec.LookPath("ffmpeg")
	if ffmpegPath != "" {
		slog.Info("ffmpeg detected: video thumbnails enabled", "path", ffmpegPath)
	} else {
		slog.Info("ffmpeg not found: image thumbnails enabled, video thumbnails disabled")
	}
	return &Service{
		cacheDir:   cacheDir,
		files:      src,
		ffmpegPath: ffmpegPath,
		sem:        make(chan struct{}, maxConcurrent),
	}
}

// Capabilities returns whether image/video thumbnails are available.
func (s *Service) Capabilities() ThumbnailCapabilities {
	return ThumbnailCapabilities{
		Images: true,
		Video:  s.ffmpegPath != "",
	}
}

// IsImageExt reports whether ext (with leading dot) is supported by the pure-Go decoder.
func IsImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	default:
		return false
	}
}

// IsVideoExt reports whether ext (with leading dot) is supported when ffmpeg is installed.
func IsVideoExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".mkv", ".mov", ".webm", ".avi", ".m4v":
		return true
	default:
		return false
	}
}

// Get returns JPEG thumbnail bytes and a stable ETag for the requested file.
// Returns (nil, "", nil) when the file type is unsupported (e.g. video without ffmpeg).
func (s *Service) Get(ctx context.Context, actor files.Actor, share, relPath string, maxPx int) ([]byte, string, error) {
	if maxPx <= 0 {
		maxPx = defaultSize
	}
	if maxPx < minSize {
		maxPx = minSize
	}
	if maxPx > maxSize {
		maxPx = maxSize
	}

	ext := strings.ToLower(path.Ext(relPath))
	isImg := IsImageExt(ext)
	isVid := IsVideoExt(ext)
	if !isImg && (!isVid || s.ffmpegPath == "") {
		return nil, "", nil
	}

	entry, err := s.files.Stat(ctx, actor, share, relPath)
	if err != nil {
		return nil, "", err
	}
	if entry.IsDir || entry.Size <= 0 {
		return nil, "", nil
	}
	if isImg && entry.Size > maxImageBytes {
		return nil, "", nil
	}

	key := cacheKey(share, relPath, entry.ModTime, entry.Size, maxPx)
	cacheFile := filepath.Join(s.cacheDir, key+".jpg")
	if raw, err := os.ReadFile(cacheFile); err == nil && len(raw) > 0 {
		// Touch mtime lazily for retention tracking.
		now := time.Now()
		_ = os.Chtimes(cacheFile, now, now)
		return raw, `"` + key + `"`, nil
	}

	// Acquire bounded worker slot.
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}

	// Re-check cache after waiting for semaphore.
	if raw, err := os.ReadFile(cacheFile); err == nil && len(raw) > 0 {
		return raw, `"` + key + `"`, nil
	}

	dl, err := s.files.Download(ctx, actor, share, relPath, "")
	if err != nil {
		return nil, "", err
	}
	defer dl.Body.Close()

	var jpegBytes []byte
	if isImg {
		jpegBytes, err = renderImageThumb(io.LimitReader(dl.Body, maxImageBytes+1), maxPx)
	} else {
		jpegBytes, err = s.renderVideoThumb(ctx, io.LimitReader(dl.Body, maxVideoBytes), ext, maxPx)
	}
	if err != nil {
		return nil, "", err
	}

	tmp := cacheFile + ".tmp"
	if os.WriteFile(tmp, jpegBytes, 0o600) == nil {
		_ = os.Rename(tmp, cacheFile)
	}
	return jpegBytes, `"` + key + `"`, nil
}

// Cleanup deletes cached thumbnails not accessed within cacheRetention.
func (s *Service) Cleanup(ctx context.Context) error {
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-cacheRetention)
	removed := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(s.cacheDir, e.Name())) == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		slog.InfoContext(ctx, "pruned old thumbnails", "count", removed)
	}
	return nil
}

func cacheKey(share, relPath string, modTime time.Time, size int64, maxPx int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00%d", share, relPath, modTime.UnixNano(), size, maxPx)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func renderImageThumb(r io.Reader, maxPx int) ([]byte, error) {
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("thumbs: decode image: %w", err)
	}
	dst := resizeBilinear(src, maxPx)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, fmt.Errorf("thumbs: encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

func (s *Service) renderVideoThumb(ctx context.Context, r io.Reader, ext string, maxPx int) ([]byte, error) {
	tmpFile, err := os.CreateTemp(s.cacheDir, "vid-*"+ext)
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, r); err != nil {
		tmpFile.Close()
		return nil, err
	}
	tmpFile.Close()

	runCtx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()

	scale := fmt.Sprintf("scale='min(%d,iw)':-2", maxPx)
	cmd := exec.CommandContext(runCtx, s.ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-ss", "00:00:01",
		"-i", tmpPath,
		"-frames:v", "1",
		"-vf", scale,
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-q:v", "5",
		"-",
	)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		// Retry at frame 0 for very short videos (< 1s).
		cmd0 := exec.CommandContext(runCtx, s.ffmpegPath,
			"-hide_banner", "-loglevel", "error",
			"-i", tmpPath,
			"-frames:v", "1",
			"-vf", scale,
			"-f", "image2pipe",
			"-vcodec", "mjpeg",
			"-q:v", "5",
			"-",
		)
		out, err = cmd0.Output()
		if err != nil || len(out) == 0 {
			return nil, fmt.Errorf("thumbs: ffmpeg failed: %w", err)
		}
	}
	return out, nil
}

// resizeBilinear scales src proportionally so its longest side <= maxPx.
// Pure Go, zero CGO, fast and crisp for thumbnails.
func resizeBilinear(src image.Image, maxPx int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return src
	}
	if sw <= maxPx && sh <= maxPx {
		return src
	}
	var dw, dh int
	if sw >= sh {
		dw = maxPx
		dh = max(1, int(math.Round(float64(sh)*float64(maxPx)/float64(sw))))
	} else {
		dh = maxPx
		dw = max(1, int(math.Round(float64(sw)*float64(maxPx)/float64(sh))))
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xRatio := float64(sw) / float64(dw)
	yRatio := float64(sh) / float64(dh)

	for y := 0; y < dh; y++ {
		sy := (float64(y)+0.5)*yRatio - 0.5
		y0 := int(math.Floor(sy))
		y1 := y0 + 1
		wy := sy - float64(y0)
		if y0 < 0 {
			y0 = 0
			wy = 0
		}
		if y1 >= sh {
			y1 = sh - 1
		}
		for x := 0; x < dw; x++ {
			sx := (float64(x)+0.5)*xRatio - 0.5
			x0 := int(math.Floor(sx))
			x1 := x0 + 1
			wx := sx - float64(x0)
			if x0 < 0 {
				x0 = 0
				wx = 0
			}
			if x1 >= sw {
				x1 = sw - 1
			}

			r00, g00, b00, a00 := src.At(b.Min.X+x0, b.Min.Y+y0).RGBA()
			r10, g10, b10, a10 := src.At(b.Min.X+x1, b.Min.Y+y0).RGBA()
			r01, g01, b01, a01 := src.At(b.Min.X+x0, b.Min.Y+y1).RGBA()
			r11, g11, b11, a11 := src.At(b.Min.X+x1, b.Min.Y+y1).RGBA()

			w00 := (1 - wx) * (1 - wy)
			w10 := wx * (1 - wy)
			w01 := (1 - wx) * wy
			w11 := wx * wy

			off := dst.PixOffset(x, y)
			dst.Pix[off+0] = uint8((float64(r00)*w00 + float64(r10)*w10 + float64(r01)*w01 + float64(r11)*w11) / 257.0)
			dst.Pix[off+1] = uint8((float64(g00)*w00 + float64(g10)*w10 + float64(g01)*w01 + float64(g11)*w11) / 257.0)
			dst.Pix[off+2] = uint8((float64(b00)*w00 + float64(b10)*w10 + float64(b01)*w01 + float64(b11)*w11) / 257.0)
			dst.Pix[off+3] = uint8((float64(a00)*w00 + float64(a10)*w10 + float64(a01)*w01 + float64(a11)*w11) / 257.0)
		}
	}
	return dst
}
