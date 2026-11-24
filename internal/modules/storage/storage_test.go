package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageScanAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0o755)

	shareA := filepath.Join(dir, "media")
	shareB := filepath.Join(dir, "backup")
	_ = os.MkdirAll(filepath.Join(shareA, "movies"), 0o755)
	_ = os.MkdirAll(shareB, 0o755)

	dupPayload := bytes.Repeat([]byte("A"), 4096)
	uniquePayload := bytes.Repeat([]byte("B"), 8192)
	oldPayload := bytes.Repeat([]byte("C"), 2048)

	_ = os.WriteFile(filepath.Join(shareA, "movies", "big.mp4"), uniquePayload, 0o644)
	_ = os.WriteFile(filepath.Join(shareA, "copy1.pdf"), dupPayload, 0o644)
	_ = os.WriteFile(filepath.Join(shareB, "copy2.pdf"), dupPayload, 0o644)
	oldFile := filepath.Join(shareB, "ancient.txt")
	_ = os.WriteFile(oldFile, oldPayload, 0o644)
	past := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(oldFile, past, past)

	svc := NewService(dataDir, func(context.Context) ([]ShareRef, error) {
		return []ShareRef{
			{ID: 1, Name: "media", Path: shareA},
			{ID: 2, Name: "backup", Path: shareB},
		}, nil
	})

	rep, err := svc.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if rep.TotalFiles != 4 || rep.TotalBytes != 8192+4096+4096+2048 {
		t.Fatalf("unexpected totals: files=%d bytes=%d", rep.TotalFiles, rep.TotalBytes)
	}
	if len(rep.LargestFiles) == 0 || rep.LargestFiles[0].Name != "big.mp4" {
		t.Fatalf("expected big.mp4 as largest, got %+v", rep.LargestFiles)
	}
	if len(rep.OldestFiles) == 0 || rep.OldestFiles[0].Name != "ancient.txt" {
		t.Fatalf("expected ancient.txt as oldest, got %+v", rep.OldestFiles)
	}
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].ReclaimableBytes != 4096 || len(rep.Duplicates[0].Files) != 2 {
		t.Fatalf("expected 1 duplicate group with 4096 reclaimable, got %+v", rep.Duplicates)
	}

	// Reload from disk verifies persistence.
	reloaded := NewService(dataDir, nil).Report()
	if reloaded.TotalFiles != 4 || reloaded.ReclaimableBytes != 4096 {
		t.Fatalf("reloaded report mismatch: %+v", reloaded)
	}
}
