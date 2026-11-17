// Package db opens the SQLite database and runs migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

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

// Migrate applies every pending migration from the embedded files.
func (d *DB) Migrate(ctx context.Context, migrations fs.FS) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, d.Write, migrations)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
