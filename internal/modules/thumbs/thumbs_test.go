package thumbs

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
	"time"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/modules/files"
)

type fakeSource struct {
	data    []byte
	modTime time.Time
	reads   int
}

func (f *fakeSource) Stat(_ context.Context, _ files.Actor, _, p string) (files.Entry, error) {
	return files.Entry{
		Name:    p,
		Size:    int64(len(f.data)),
		ModTime: f.modTime,
	}, nil
}

func (f *fakeSource) Download(_ context.Context, _ files.Actor, _, _, _ string) (*fileworker.Download, error) {
	f.reads++
	return &fileworker.Download{
		Body: io.NopCloser(bytes.NewReader(f.data)),
	}, nil
}

func TestImageThumbnailResizeAndDiskCache(t *testing.T) {
	// Generate a 800x400 PNG in memory.
	img := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 180, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}

	src := &fakeSource{
		data:    pngBuf.Bytes(),
		modTime: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}
	svc := NewService(t.TempDir(), src)
	svc.ffmpegPath = "" // verify video gracefully turns off when ffmpeg is absent

	actor := files.Actor{UserID: 1}
	raw, etag, err := svc.Get(context.Background(), actor, "photos", "trip/beach.png", 200)
	if err != nil {
		t.Fatalf("thumb failed: %v", err)
	}
	if len(raw) == 0 || etag == "" {
		t.Fatalf("expected non-empty thumbnail and etag")
	}
	decoded, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("invalid jpeg output: %v", err)
	}
	if decoded.Bounds().Dx() != 200 || decoded.Bounds().Dy() != 100 {
		t.Fatalf("expected 200x100 thumbnail, got %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}

	// Second request hits disk cache without re-reading from fileworker.
	raw2, etag2, err := svc.Get(context.Background(), actor, "photos", "trip/beach.png", 200)
	if err != nil || etag2 != etag || len(raw2) != len(raw) {
		t.Fatalf("cache hit failed: err=%v etag=%q", err, etag2)
	}
	if src.reads != 1 {
		t.Fatalf("expected 1 read (cache hit on 2nd), got %d", src.reads)
	}

	// Video without ffmpeg returns empty cleanly (no error).
	vidRaw, _, err := svc.Get(context.Background(), actor, "media", "clip.mp4", 200)
	if err != nil || len(vidRaw) != 0 {
		t.Fatalf("expected empty video thumb when ffmpeg missing, got len=%d err=%v", len(vidRaw), err)
	}
}
