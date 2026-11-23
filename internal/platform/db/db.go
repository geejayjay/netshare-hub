// Package db opens the SQLite database and runs migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGO
)

// DB holds two handles to the same file. SQLite allows one writer at a time,
// so all writes go through a single connection (no "database is locked"),
// while reads use a pool and never wait for each other.
type DB struct {
	Write *sql.DB
	Read  *sql.DB
}

// Open opens (and creates if needed) the database at path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	write, err := open(path, false)
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)

	read, err := open(path, true)
	if err != nil {
		write.Close()
		return nil, err
	}
	read.SetMaxOpenConns(8)

	return &DB{Write: write, Read: read}, nil
}

func open(path string, readOnly bool) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return conn, nil
}

func (d *DB) Close() error {
	d.Read.Close()
	return d.Write.Close()
}

// KeepBackups is how many pre-migration backups are kept.
const KeepBackups = 5

// Migrate applies every pending migration from the embedded files.
//
// Before a migration changes an existing database, a consistent copy is
// written to backupDir ("" turns this off): an upgrade that goes wrong can
// always be undone by putting the copy back. If the copy cannot be made,
// nothing is migrated.
func (d *DB) Migrate(ctx context.Context, migrations fs.FS, backupDir string) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, d.Write, migrations)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	pending, err := provider.HasPending(ctx)
	if err != nil {
		return fmt.Errorf("check migrations: %w", err)
	}
	if !pending {
		return nil
	}
	if backupDir != "" {
		version, err := provider.GetDBVersion(ctx)
		if err != nil {
			return fmt.Errorf("check database version: %w", err)
		}
		if version > 0 { // a new, empty database has nothing to lose
			path, err := d.backup(ctx, backupDir, version)
			if err != nil {
				return fmt.Errorf("back up the database before migrating (nothing was changed): %w", err)
			}
			slog.InfoContext(ctx, "database backed up before migrating", "backup", path, "from_version", version)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// backup writes a consistent copy of the database (VACUUM INTO works on a
// live database) and keeps only the newest KeepBackups copies. The copy
// holds password hashes, so only the owner can read it.
func (d *DB) backup(ctx context.Context, dir string, version int64) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("netshare-hub-v%d-%s.db", version, time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	literal := "'" + strings.ReplaceAll(filepath.ToSlash(path), "'", "''") + "'"
	if _, err := d.Write.ExecContext(ctx, "VACUUM INTO "+literal); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	prune(dir)
	return path, nil
}

// Snapshot writes a consistent copy of the database to path (a new file,
// readable by the owner only). The config restore keeps one before it
// replaces anything.
func (d *DB) Snapshot(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	literal := "'" + strings.ReplaceAll(filepath.ToSlash(path), "'", "''") + "'"
	if _, err := d.Write.ExecContext(ctx, "VACUUM INTO "+literal); err != nil {
		os.Remove(path)
		return err
	}
	return os.Chmod(path, 0o600)
}

// prune removes all but the newest KeepBackups backups.
func prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var backups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "netshare-hub-v") && strings.HasSuffix(e.Name(), ".db") {
			backups = append(backups, e.Name())
		}
	}
	// The names end in a UTC timestamp that sorts like time, after the
	// version: sort on the timestamp part.
	sort.Slice(backups, func(i, j int) bool { return stamp(backups[i]) < stamp(backups[j]) })
	for len(backups) > KeepBackups {
		os.Remove(filepath.Join(dir, backups[0]))
		backups = backups[1:]
	}
}

func stamp(name string) string {
	if i := strings.LastIndex(name, "-v"); i >= 0 {
		if j := strings.Index(name[i+2:], "-"); j >= 0 {
			return name[i+2+j+1:]
		}
	}
	return name
}
