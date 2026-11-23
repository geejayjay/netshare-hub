// Package activity is the SMB activity log: who did what, from where, on
// which share. Events come from Samba's audit log (sys.Audit, read by the
// root agent) through one fan-out (sambalog.Ingester).
//
// It lives in its own database file (activity.db) with its own writer, and
// writes in batches: a client copying a hundred thousand small files makes
// a few hundred transactions here, and none in the main database.
package activity

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/sys/sambalog"
)

// ActivityEntry is one thing that happened, as the activity feed shows it.
type ActivityEntry struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	User       string    `json:"user"`
	ClientIP   string    `json:"clientIp"`
	ClientName string    `json:"clientName,omitempty" doc:"The computer's name on the network."`
	Share      string    `json:"share,omitempty"`
	Op         string    `json:"op" enum:"connect,disconnect,create_folder,write,rename,delete,login,login_failed"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	Path       string    `json:"path,omitempty" doc:"Inside the share."`
	Path2      string    `json:"path2,omitempty" doc:"The new name, for a rename."`
}

// Ops are the operations recorded, by their feed name.
var Ops = []string{"connect", "disconnect", "create_folder", "write", "rename", "delete", "login", "login_failed"}

// SharePaths tells which folder each share is (to show paths inside the
// share instead of on the server).
type SharePaths interface {
	Paths(ctx context.Context) (map[string]string, error)
}

// Options tune the activity log.
type Options struct {
	// Retention is how long entries are kept (default 90 days).
	Retention time.Duration
	// BatchSize and BatchDelay: entries are written when BatchSize are
	// waiting or BatchDelay has passed since the first (defaults 500, 1s).
	BatchSize  int
	BatchDelay time.Duration
}

// Service records and reads the activity log.
type Service struct {
	db     *db.DB
	shares SharePaths
	opts   Options
	now    func() time.Time

	mu    sync.Mutex
	paths map[string]string
	at    time.Time // when paths was read
}

func NewService(database *db.DB, shares SharePaths, opts Options) *Service {
	if opts.Retention <= 0 {
		opts.Retention = 90 * 24 * time.Hour
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if opts.BatchDelay <= 0 {
		opts.BatchDelay = time.Second
	}
	return &Service{db: database, shares: shares, opts: opts, now: time.Now}
}

// sharePath returns a share's folder, from a cache refreshed every minute
// (or when a share is not in it yet).
func (s *Service) sharePath(ctx context.Context, share string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.paths[share]
	if ok && time.Since(s.at) < time.Minute {
		return p
	}
	if s.shares != nil && time.Since(s.at) > time.Second {
		if paths, err := s.shares.Paths(ctx); err == nil {
			s.paths, s.at = paths, time.Now()
		}
	}
	return s.paths[share]
}

// relative turns Samba's absolute path into one inside the share.
func relative(abs, sharePath string) string {
	if sharePath == "" {
		return abs
	}
	if abs == sharePath || abs == sharePath+"/" {
		return ""
	}
	if rest, ok := strings.CutPrefix(abs, sharePath+"/"); ok {
		return path.Clean(rest)
	}
	return abs
}

// entry turns an event into an entry; ok is false for events the feed does
// not show (opening a file to read it: Samba logs every one).
func (s *Service) entry(ctx context.Context, ev sambalog.Event) (ActivityEntry, bool) {
	e := ActivityEntry{At: ev.Time, User: ev.User, ClientName: ev.ClientName, Share: ev.Share, OK: ev.OK, Error: ev.Error}
	if ev.ClientIP.IsValid() {
		e.ClientIP = ev.ClientIP.String()
	}
	if e.At.IsZero() {
		e.At = s.now()
	}
	switch {
	case ev.Kind == sambalog.KindAuth && ev.OK:
		e.Op = "login"
		return e, true
	case ev.Kind == sambalog.KindAuth:
		e.Op, e.Error = "login_failed", ev.AuthStatus
		return e, true
	case ev.Kind == sambalog.KindConnect:
		e.Op = "connect"
		return e, true
	case ev.Kind == sambalog.KindDisconnect:
		e.Op = "disconnect"
		return e, true
	}
	switch ev.Op {
	case "mkdirat":
		e.Op = "create_folder"
	case "openat":
		if !ev.Write {
			return ActivityEntry{}, false
		}
		e.Op = "write"
	case "renameat":
		e.Op = "rename"
	case "unlinkat":
		e.Op = "delete"
	default:
		return ActivityEntry{}, false
	}
	base := s.sharePath(ctx, ev.Share)
	e.Path = relative(ev.Path, base)
	if ev.Path2 != "" {
		e.Path2 = relative(ev.Path2, base)
	}
	return e, true
}

// Record writes events from the channel until it closes or ctx ends,
// in batches.
func (s *Service) Record(ctx context.Context, events <-chan sambalog.Event) {
	batch := make([]ActivityEntry, 0, s.opts.BatchSize)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.insert(context.WithoutCancel(ctx), batch); err != nil {
			slog.ErrorContext(ctx, "could not write the activity log", "entries", len(batch), "err", err)
		}
		batch = batch[:0]
	}
	defer flush()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			e, keep := s.entry(ctx, ev)
			if !keep {
				continue
			}
			if len(batch) == 0 {
				timer.Reset(s.opts.BatchDelay)
			}
			batch = append(batch, e)
			if len(batch) >= s.opts.BatchSize {
				timer.Stop()
				flush()
			}
		case <-timer.C:
			flush()
		}
	}
}

func (s *Service) insert(ctx context.Context, entries []ActivityEntry) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO activity (at, user, client_ip, client_name, share, op, ok, error, path, path2)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.At.UnixMilli(), e.User, e.ClientIP, e.ClientName, e.Share, e.Op, e.OK,
			e.Error, e.Path, e.Path2); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Filter narrows the feed. Zero values mean "any".
type Filter struct {
	Share, User, Op, ClientIP string
	From, To                  time.Time
	// Path finds entries whose path (or new name) contains it.
	Path string
	// Before is a cursor: only entries older than this id (the "next" of
	// the previous page).
	Before int64
}

func (f Filter) where() (string, []any) {
	where, args := " WHERE 1 = 1", []any{}
	add := func(cond string, v ...any) {
		where += " AND " + cond
		args = append(args, v...)
	}
	if f.Share != "" {
		add("share = ?", f.Share)
	}
	if f.User != "" {
		add("user = ?", f.User)
	}
	if f.Op != "" {
		add("op = ?", f.Op)
	}
	if f.ClientIP != "" {
		add("client_ip = ?", f.ClientIP)
	}
	if !f.From.IsZero() {
		add("at >= ?", f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		add("at < ?", f.To.UnixMilli())
	}
	if f.Path != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Path) + "%"
		add(`(path LIKE ? ESCAPE '\' OR path2 LIKE ? ESCAPE '\')`, like, like)
	}
	if f.Before > 0 {
		add("id < ?", f.Before)
	}
	return where, args
}

func scan(rows *sql.Rows) (ActivityEntry, error) {
	var e ActivityEntry
	var at int64
	err := rows.Scan(&e.ID, &at, &e.User, &e.ClientIP, &e.ClientName, &e.Share, &e.Op, &e.OK, &e.Error, &e.Path, &e.Path2)
	e.At = time.UnixMilli(at).UTC()
	return e, err
}

const columns = "id, at, user, client_ip, client_name, share, op, ok, error, path, path2"

// List returns one page, newest first, and the cursor for the next page (0
// when this is the last). Paging by id keeps a page fast however many
// millions of entries there are.
func (s *Service) List(ctx context.Context, f Filter, limit int) ([]ActivityEntry, int64, error) {
	where, args := f.where()
	rows, err := s.db.Read.QueryContext(ctx, "SELECT "+columns+" FROM activity"+where+" ORDER BY id DESC LIMIT ?",
		append(args, limit+1)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ActivityEntry{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	var next int64
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, rows.Err()
}

// Each calls fn for every matching entry, newest first (for exports).
func (s *Service) Each(ctx context.Context, f Filter, max int, fn func(ActivityEntry) error) error {
	where, args := f.where()
	rows, err := s.db.Read.QueryContext(ctx, "SELECT "+columns+" FROM activity"+where+" ORDER BY id DESC LIMIT ?",
		append(args, max)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Prune removes entries older than the retention time, a chunk at a time
// so the recorder never waits long, then gives the space back (a scheduled
// task).
func (s *Service) Prune(ctx context.Context) error {
	cutoff := s.now().Add(-s.opts.Retention).UnixMilli()
	var total int64
	for {
		res, err := s.db.Write.ExecContext(ctx,
			"DELETE FROM activity WHERE id IN (SELECT id FROM activity WHERE at < ? ORDER BY id LIMIT 5000)", cutoff)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < 5000 {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if total > 0 {
		if _, err := s.db.Write.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
			return fmt.Errorf("give back space: %w", err)
		}
		slog.InfoContext(ctx, "old activity removed", "entries", total)
	}
	return nil
}
