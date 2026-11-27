package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"netshare-hub/internal/db/migrations"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/sys"
)

type fakeProvider struct {
	shares   []ShareRef
	readable map[int64][]int64
}

func (f *fakeProvider) AllShares(context.Context) ([]ShareRef, error) { return f.shares, nil }
func (f *fakeProvider) ReadableShareIDs(_ context.Context, userID int64) ([]int64, error) {
	return f.readable[userID], nil
}

// dirFiles stands in for the agent: each share name maps to a folder, read
// with the system layer's own walk and lookup.
type dirFiles map[string]string

func (d dirFiles) ScanShare(ctx context.Context, share string, fn func(sys.ShareEntry) error) error {
	dir, ok := d[share]
	if !ok {
		return sys.ErrNotFound
	}
	return sys.WalkShareFS(ctx, os.DirFS(dir), fn)
}

func (d dirFiles) StatFiles(ctx context.Context, share string, paths []string, hash bool) ([]sys.ShareFile, error) {
	dir, ok := d[share]
	if !ok {
		return nil, sys.ErrNotFound
	}
	return sys.StatShareFS(ctx, os.DirFS(dir), paths, hash)
}

func TestSearchIndexAndAccessIsolation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(ctx, migrations.FS, filepath.Join(dir, "bak")); err != nil {
		t.Fatal(err)
	}

	shareA := filepath.Join(dir, "docs")
	shareB := filepath.Join(dir, "secret")
	if err := os.MkdirAll(filepath.Join(shareA, "finance"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shareB, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(shareA, "finance", "budget-2026.pdf"), []byte("hello"), 0o644)
	_ = os.WriteFile(filepath.Join(shareA, "README.md"), []byte("readme"), 0o644)
	_ = os.WriteFile(filepath.Join(shareB, "budget-secret.pdf"), []byte("secret"), 0o644)
	// The recycle bin is never indexed.
	_ = os.MkdirAll(filepath.Join(shareA, ".recycle"), 0o755)
	_ = os.WriteFile(filepath.Join(shareA, ".recycle", "budget-old.pdf"), []byte("old"), 0o644)

	prov := &fakeProvider{
		shares: []ShareRef{
			{ID: 1, Name: "docs", Path: shareA},
			{ID: 2, Name: "secret", Path: shareB},
		},
		readable: map[int64][]int64{
			10: {1},    // Alice can only read share 1
			20: {1, 2}, // Admin can read both
			30: {},     // Drop-box or no-access user
		},
	}

	svc := NewService(database, prov, dirFiles{"docs": shareA, "secret": shareB})
	if err := svc.Reindex(ctx); err != nil {
		t.Fatalf("reindex failed: %v", err)
	}

	st, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.IndexedCount != 4 { // finance dir + 3 files
		t.Fatalf("expected 4 indexed entries, got %d", st.IndexedCount)
	}

	// Alice searches "budget" — must only see share 1, NEVER share 2.
	resAlice, err := svc.Search(ctx, 10, Query{Text: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resAlice.Items) != 1 || resAlice.Items[0].ShareID != 1 || resAlice.Items[0].Name != "budget-2026.pdf" {
		t.Fatalf("unexpected alice results: %+v", resAlice.Items)
	}

	// Alice tries to force shareId=2 — must get 0 results.
	resSneak, err := svc.Search(ctx, 10, Query{Text: "budget", ShareID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(resSneak.Items) != 0 {
		t.Fatalf("access leak! got %+v", resSneak.Items)
	}

	// Admin searches "budget" — sees both.
	resAdmin, err := svc.Search(ctx, 20, Query{Text: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resAdmin.Items) != 2 {
		t.Fatalf("expected 2 hits for admin, got %d", len(resAdmin.Items))
	}

	// User 30 has no readable shares — sees nothing.
	resNone, err := svc.Search(ctx, 30, Query{Text: "budget"})
	if err != nil || len(resNone.Items) != 0 {
		t.Fatalf("expected 0 hits for user 30, got %d (%v)", len(resNone.Items), err)
	}

	// Kind=dir filter works.
	resDir, err := svc.Search(ctx, 10, Query{Text: "fin", Kind: "dir"})
	if err != nil || len(resDir.Items) != 1 || !resDir.Items[0].IsDir {
		t.Fatalf("expected finance dir hit, got %+v (%v)", resDir.Items, err)
	}
}
