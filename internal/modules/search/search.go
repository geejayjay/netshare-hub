// Package search indexes files across shares into SQLite FTS5 and serves
// access-checked cross-share search results.
package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sambalog"
)

const (
	maxIndexedPerShare = 200_000
	defaultLimit       = 30
	maxLimit           = 100
)

// ShareRef describes a share to index.
type ShareRef struct {
	ID   int64
	Name string
	Path string
}

// ShareProvider abstracts share discovery and per-user read authorization.
type ShareProvider interface {
	AllShares(ctx context.Context) ([]ShareRef, error)
	ReadableShareIDs(ctx context.Context, userID int64) ([]int64, error)
}

// ProviderFuncs adapts function closures to ShareProvider.
type ProviderFuncs struct {
	All      func(ctx context.Context) ([]ShareRef, error)
	Readable func(ctx context.Context, userID int64) ([]int64, error)
}

func (p ProviderFuncs) AllShares(ctx context.Context) ([]ShareRef, error) { return p.All(ctx) }
func (p ProviderFuncs) ReadableShareIDs(ctx context.Context, userID int64) ([]int64, error) {
	return p.Readable(ctx, userID)
}

// SearchHit is one file or directory matching a query.
type SearchHit struct {
	ShareID   int64  `json:"shareId"`
	ShareName string `json:"shareName"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Ext       string `json:"ext,omitempty"`
	IsDir     bool   `json:"isDir"`
	Size      int64  `json:"size"`
	ModTime   string `json:"modTime"`
}

// SearchStatus reports index freshness and size.
type SearchStatus struct {
	IndexedCount   int64  `json:"indexedCount"`
	LastIndexedAt  string `json:"lastIndexedAt,omitempty"`
	LastDurationMs int64  `json:"lastDurationMs"`
	Indexing       bool   `json:"indexing"`
}

// SearchResponse is returned by GET /search.
type SearchResponse struct {
	Items        []SearchHit `json:"items" nullable:"false"`
	Query        string      `json:"query"`
	IndexedCount int64       `json:"indexedCount"`
	IndexedAt    string      `json:"indexedAt,omitempty"`
}

// Query filters a search request.
type Query struct {
	Text    string
	ShareID int64
	Kind    string // "", "all", "file", "dir"
	Limit   int
}

// Service manages the FTS5 index and executes access-checked queries.
type Service struct {
	db       *db.DB
	shares   ShareProvider
	files    Files
	indexing atomic.Bool
}

// Files reads the share folders. It is the system layer (sys.Samba): the
// web server itself cannot read them, only their users and the agent can.
type Files interface {
	ScanShare(ctx context.Context, share string, fn func(sys.ShareEntry) error) error
	StatFiles(ctx context.Context, share string, paths []string, hash bool) ([]sys.ShareFile, error)
}

// errEnough stops a scan at maxIndexedPerShare entries.
var errEnough = errors.New("search: enough entries")

// NewService creates the search service.
func NewService(database *db.DB, shares ShareProvider, files Files) *Service {
	return &Service{db: database, shares: shares, files: files}
}

// Status returns the current index metadata.
func (s *Service) Status(ctx context.Context) (SearchStatus, error) {
	var st SearchStatus
	err := s.db.Read.QueryRowContext(ctx, `
		SELECT indexed_count, last_indexed_at, last_duration_ms
		FROM search_meta WHERE id = 1
	`).Scan(&st.IndexedCount, &st.LastIndexedAt, &st.LastDurationMs)
	if err != nil && err != sql.ErrNoRows {
		return SearchStatus{}, fmt.Errorf("search: status: %w", err)
	}
	st.Indexing = s.indexing.Load()
	return st, nil
}

// Reindex walks every configured share (never following symlinks) and rebuilds
// the FTS5 index atomically. Safe to run on a schedule or manually.
func (s *Service) Reindex(ctx context.Context) error {
	if !s.indexing.CompareAndSwap(false, true) {
		return nil
	}
	defer s.indexing.Store(false)

	start := time.Now()
	shares, err := s.shares.AllShares(ctx)
	if err != nil {
		return fmt.Errorf("search: list shares: %w", err)
	}

	type row struct {
		shareID   int64
		shareName string
		relPath   string
		name      string
		ext       string
		isDir     int
		size      int64
		modTime   int64
	}
	entries := make([]row, 0, 512)

	for _, sh := range shares {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sh.Path == "" {
			continue
		}
		// The agent walks the share: the web server cannot read the folders.
		count := 0
		err := s.files.ScanShare(ctx, sh.Name, func(e sys.ShareEntry) error {
			name := path.Base(e.Path)
			ext := ""
			isDir := 0
			if e.IsDir {
				isDir = 1
			} else {
				ext = strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
			}
			entries = append(entries, row{
				shareID:   sh.ID,
				shareName: sh.Name,
				relPath:   e.Path,
				name:      name,
				ext:       ext,
				isDir:     isDir,
				size:      e.Size,
				modTime:   e.ModTime,
			})
			count++
			if count >= maxIndexedPerShare {
				return errEnough
			}
			return nil
		})
		switch {
		case err == nil || errors.Is(err, errEnough):
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, sys.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
			// Not live (no one may use it) or no folder yet: nothing to index.
		default:
			slog.WarnContext(ctx, "search: could not scan a share", "share", sh.Name, "err", err)
		}
	}

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("search: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM search_entries`); err != nil {
		return fmt.Errorf("search: clear: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO search_entries (share_id, share_name, path, name, ext, is_dir, size, mod_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("search: prepare: %w", err)
	}
	defer stmt.Close()

	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.shareID, e.shareName, e.relPath, e.name, e.ext, e.isDir, e.size, e.modTime); err != nil {
			return fmt.Errorf("search: insert: %w", err)
		}
	}

	durMs := time.Since(start).Milliseconds()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
		UPDATE search_meta SET last_indexed_at = ?, last_duration_ms = ?, indexed_count = ? WHERE id = 1
	`, now, durMs, len(entries)); err != nil {
		return fmt.Errorf("search: meta: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("search: commit: %w", err)
	}
	slog.InfoContext(ctx, "search index updated", "entries", len(entries), "duration_ms", durMs)
	return nil
}

const (
	smbBatchWindow = 500 * time.Millisecond
	smbBatchMax    = 200
)

// WatchSMBEvents incrementally updates the search index from live SMB audit
// events, batching and deduplicating stat calls to avoid flooding the agent
// during heavy file writes.
func (s *Service) WatchSMBEvents(ctx context.Context, events <-chan sambalog.Event) {
	pendingUpserts := make(map[string]map[string]struct{})
	pendingDeletes := make(map[string]map[string]struct{})
	pendingCount := 0

	timer := time.NewTimer(smbBatchWindow)
	timer.Stop()
	defer timer.Stop()
	timerRunning := false

	flush := func() {
		if pendingCount == 0 {
			return
		}
		s.flushSMBBatch(ctx, pendingDeletes, pendingUpserts)
		pendingUpserts = make(map[string]map[string]struct{})
		pendingDeletes = make(map[string]map[string]struct{})
		pendingCount = 0
		timerRunning = false
	}

	queueDelete := func(share, rawPath string) {
		if rawPath == "" {
			return
		}
		if m := pendingUpserts[share]; m != nil {
			delete(m, rawPath)
		}
		if pendingDeletes[share] == nil {
			pendingDeletes[share] = make(map[string]struct{})
		}
		if _, exists := pendingDeletes[share][rawPath]; !exists {
			pendingDeletes[share][rawPath] = struct{}{}
			pendingCount++
		}
	}

	queueUpsert := func(share, rawPath string) {
		if rawPath == "" {
			return
		}
		if m := pendingDeletes[share]; m != nil {
			delete(m, rawPath)
		}
		if pendingUpserts[share] == nil {
			pendingUpserts[share] = make(map[string]struct{})
		}
		if _, exists := pendingUpserts[share][rawPath]; !exists {
			pendingUpserts[share][rawPath] = struct{}{}
			pendingCount++
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			flush()
		case ev, ok := <-events:
			if !ok {
				flush()
				return
			}
			if !ev.OK || ev.Kind != sambalog.KindFile || ev.Share == "" || ev.Path == "" {
				continue
			}
			switch ev.Op {
			case "unlinkat":
				queueDelete(ev.Share, ev.Path)
			case "renameat":
				queueDelete(ev.Share, ev.Path)
				if ev.Path2 != "" {
					queueUpsert(ev.Share, ev.Path2)
				}
			case "mkdirat", "pwrite":
				queueUpsert(ev.Share, ev.Path)
			case "openat":
				if ev.Write {
					queueUpsert(ev.Share, ev.Path)
				}
			}
			if pendingCount >= smbBatchMax {
				if timerRunning {
					timer.Stop()
				}
				flush()
			} else if pendingCount > 0 && !timerRunning {
				timer.Reset(smbBatchWindow)
				timerRunning = true
			}
		}
	}
}

func (s *Service) flushSMBBatch(ctx context.Context, deletes, upserts map[string]map[string]struct{}) {
	shares, err := s.shares.AllShares(ctx)
	if err != nil {
		return
	}
	byName := make(map[string]ShareRef, len(shares))
	for _, sh := range shares {
		byName[sh.Name] = sh
	}

	toRel := func(sh ShareRef, raw string) string {
		clean := filepath.ToSlash(raw)
		prefix := strings.TrimRight(filepath.ToSlash(sh.Path), "/") + "/"
		clean = strings.TrimPrefix(clean, prefix)
		return strings.TrimPrefix(clean, "/")
	}

	for shareName, paths := range deletes {
		sh, ok := byName[shareName]
		if !ok {
			continue
		}
		for raw := range paths {
			if rel := toRel(sh, raw); rel != "" {
				_, _ = s.db.Write.ExecContext(ctx, `DELETE FROM search_entries WHERE share_id = ? AND path = ?`, sh.ID, rel)
			}
		}
	}

	for shareName, paths := range upserts {
		sh, ok := byName[shareName]
		if !ok {
			continue
		}
		rels := make([]string, 0, len(paths))
		for raw := range paths {
			if rel := toRel(sh, raw); rel != "" {
				rels = append(rels, rel)
			}
		}
		if len(rels) == 0 {
			continue
		}
		const chunkSize = 500
		for i := 0; i < len(rels); i += chunkSize {
			end := min(i+chunkSize, len(rels))
			s.upsertBatch(ctx, sh, rels[i:end])
		}
	}
}

func (s *Service) upsertBatch(ctx context.Context, sh ShareRef, rels []string) {
	found, err := s.files.StatFiles(ctx, sh.Name, rels, false)
	if err != nil || len(found) == 0 {
		return
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO search_entries (share_id, share_name, path, name, ext, is_dir, size, mod_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return
	}
	defer stmt.Close()
	for _, fi := range found {
		rel := fi.Path
		name := path.Base(rel)
		isDir := 0
		ext := ""
		if fi.IsDir {
			isDir = 1
		} else {
			ext = strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
		}
		_, _ = stmt.ExecContext(ctx, sh.ID, sh.Name, rel, name, ext, isDir, fi.Size, fi.ModTime)
	}
	_ = tx.Commit()
}

// Search queries the FTS5 index, strictly restricting results to shares the user may read.
func (s *Service) Search(ctx context.Context, userID int64, q Query) (SearchResponse, error) {
	st, err := s.Status(ctx)
	if err != nil {
		return SearchResponse{}, err
	}
	text := strings.TrimSpace(q.Text)
	resp := SearchResponse{
		Items:        make([]SearchHit, 0),
		Query:        text,
		IndexedCount: st.IndexedCount,
		IndexedAt:    st.LastIndexedAt,
	}
	if text == "" {
		return resp, nil
	}

	allowed, err := s.shares.ReadableShareIDs(ctx, userID)
	if err != nil {
		return SearchResponse{}, fmt.Errorf("search: readable shares: %w", err)
	}
	if len(allowed) == 0 {
		return resp, nil
	}
	if q.ShareID > 0 {
		found := false
		for _, id := range allowed {
			if id == q.ShareID {
				found = true
				break
			}
		}
		if !found {
			return resp, nil
		}
		allowed = []int64{q.ShareID}
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	placeholders := make([]string, len(allowed))
	args := make([]any, 0, len(allowed)+4)
	for i, id := range allowed {
		placeholders[i] = "?"
		args = append(args, id)
	}
	inClause := strings.Join(placeholders, ",")

	kindClause := ""
	switch q.Kind {
	case "file":
		kindClause = " AND e.is_dir = 0"
	case "dir":
		kindClause = " AND e.is_dir = 1"
	}

	ftsExpr := buildFTSQuery(text)
	var rows *sql.Rows
	if ftsExpr != "" {
		ftsArgs := append([]any{ftsExpr}, args...)
		ftsArgs = append(ftsArgs, limit)
		rows, err = s.db.Read.QueryContext(ctx, fmt.Sprintf(`
			SELECT e.share_id, e.share_name, e.path, e.name, e.ext, e.is_dir, e.size, e.mod_time
			FROM search_entries_fts f
			JOIN search_entries e ON e.id = f.rowid
			WHERE search_entries_fts MATCH ? AND e.share_id IN (%s)%s
			ORDER BY bm25(search_entries_fts), e.is_dir DESC, e.name COLLATE NOCASE ASC
			LIMIT ?
		`, inClause, kindClause), ftsArgs...)
	}

	if err != nil || rows == nil {
		like := "%" + escapeLike(text) + "%"
		likeArgs := append([]any{}, args...)
		likeArgs = append(likeArgs, like, like, limit)
		rows, err = s.db.Read.QueryContext(ctx, fmt.Sprintf(`
			SELECT e.share_id, e.share_name, e.path, e.name, e.ext, e.is_dir, e.size, e.mod_time
			FROM search_entries e
			WHERE e.share_id IN (%s)%s
			  AND (e.name LIKE ? ESCAPE '\' OR e.path LIKE ? ESCAPE '\')
			ORDER BY e.is_dir DESC, e.name COLLATE NOCASE ASC
			LIMIT ?
		`, inClause, kindClause), likeArgs...)
	}
	if err != nil {
		return SearchResponse{}, fmt.Errorf("search: query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var h SearchHit
		var isDir int
		var modUnix int64
		if err := rows.Scan(&h.ShareID, &h.ShareName, &h.Path, &h.Name, &h.Ext, &isDir, &h.Size, &modUnix); err != nil {
			return SearchResponse{}, err
		}
		h.IsDir = isDir != 0
		if modUnix > 0 {
			h.ModTime = time.Unix(modUnix, 0).UTC().Format(time.RFC3339)
		}
		resp.Items = append(resp.Items, h)
	}
	return resp, rows.Err()
}

func buildFTSQuery(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r > 127)
	})
	if len(parts) == 0 {
		return ""
	}
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		clean := strings.ReplaceAll(p, `"`, "")
		if clean != "" {
			tokens = append(tokens, `"`+clean+`"*`)
		}
	}
	return strings.Join(tokens, " ")
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
