package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func migrationsUpTo(n int) fstest.MapFS {
	all := []string{
		"-- +goose Up\nCREATE TABLE notes (id INTEGER PRIMARY KEY, text TEXT);\n-- +goose Down\nDROP TABLE notes;\n",
		"-- +goose Up\nALTER TABLE notes ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;\n-- +goose Down\nSELECT 1;\n",
		"-- +goose Up\nCREATE TABLE tags (id INTEGER PRIMARY KEY);\n-- +goose Down\nDROP TABLE tags;\n",
	}
	fsys := fstest.MapFS{}
	for i := 0; i < n; i++ {
		fsys[filepath.ToSlash(filepath.Join(".", "0000"+string(rune('1'+i))+"_m.sql"))] = &fstest.MapFile{Data: []byte(all[i])}
	}
	return fsys
}

func TestMigrateBacksUpFirst(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	d, err := Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// A new database: nothing to back up.
	if err := d.Migrate(ctx, migrationsUpTo(1), backups); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 0 {
		t.Errorf("a new database was backed up: %v", entries)
	}
	if _, err := d.Write.Exec("INSERT INTO notes (text) VALUES ('keep me')"); err != nil {
		t.Fatal(err)
	}

	// Nothing pending: no backup either.
	if err := d.Migrate(ctx, migrationsUpTo(1), backups); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 0 {
		t.Errorf("backed up with nothing to migrate: %v", entries)
	}

	// An upgrade: the database as it was is copied first.
	if err := d.Migrate(ctx, migrationsUpTo(2), backups); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(backups)
	if len(entries) != 1 {
		t.Fatalf("want one backup, got %v", entries)
	}
	path := filepath.Join(backups, entries[0].Name())
	if info, _ := os.Stat(path); info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("the backup holds password hashes and must be private, mode %v", info.Mode().Perm())
	}
	old, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var text string
	if err := old.Read.QueryRow("SELECT text FROM notes").Scan(&text); err != nil || text != "keep me" {
		t.Errorf("backup data: %q %v", text, err)
	}
	// The copy is from BEFORE the migration: no "pinned" column yet.
	if _, err := old.Read.Exec("SELECT pinned FROM notes"); err == nil {
		t.Error("the backup was taken after the migration")
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"netshare-hub-v3-20260101-000000.db", "netshare-hub-v12-20260102-000000.db", "netshare-hub-v4-20260103-000000.db",
		"netshare-hub-v5-20260104-000000.db", "netshare-hub-v6-20260105-000000.db", "netshare-hub-v7-20260106-000000.db",
		"netshare-hub-v8-20260107-000000.db", "something-else.txt",
	}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	prune(dir)
	left := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left[e.Name()] = true
	}
	// Oldest by date go, whatever their version number.
	for _, gone := range names[:2] {
		if left[gone] {
			t.Errorf("%s should have been removed", gone)
		}
	}
	for _, kept := range names[2:] {
		if !left[kept] {
			t.Errorf("%s should have been kept", kept)
		}
	}
}
