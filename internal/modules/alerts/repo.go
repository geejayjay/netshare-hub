package alerts

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/page"
)

// Repo is the only code that knows the alerts table.
type Repo struct{ db *db.DB }

func NewRepo(d *db.DB) *Repo { return &Repo{db: d} }

const columns = `id, kind, key, severity, source, title, message, details, count, first_at, last_at,
	resolved_at, acknowledged_at, acknowledged_by`

func scan(s interface{ Scan(...any) error }) (Alert, error) {
	var a Alert
	var details string
	var first, last int64
	var resolved, acked, by sql.NullInt64
	err := s.Scan(&a.ID, &a.Kind, &a.Key, &a.Severity, &a.Source, &a.Title, &a.Message, &details, &a.Count,
		&first, &last, &resolved, &acked, &by)
	a.Details = []byte(details)
	a.FirstAt, a.LastAt = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
	if resolved.Valid {
		t := time.Unix(resolved.Int64, 0).UTC()
		a.ResolvedAt = &t
	}
	if acked.Valid {
		t := time.Unix(acked.Int64, 0).UTC()
		a.AcknowledgedAt = &t
	}
	if by.Valid {
		a.AcknowledgedBy = &by.Int64
	}
	a.Active = a.ResolvedAt == nil && a.AcknowledgedAt == nil
	return a, err
}

// raise inserts an alert, or counts up the active one with the same kind
// and key (and takes the new severity, text and details).
func (r *Repo) raise(ctx context.Context, in Raise, details string, now time.Time) (Alert, error) {
	return scan(r.db.Write.QueryRowContext(ctx, `
		INSERT INTO alerts (kind, key, severity, source, title, message, details, first_at, last_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind, key) WHERE resolved_at IS NULL AND acknowledged_at IS NULL DO UPDATE SET
			count = count + 1, last_at = excluded.last_at, severity = excluded.severity, source = excluded.source,
			title = excluded.title, message = excluded.message, details = excluded.details
		RETURNING `+columns,
		in.Kind, in.Key, in.Severity, in.Source, in.Title, in.Message, details, now.Unix(), now.Unix()))
}

// resolve ends the active alert of a kind and key. ok is false when none is.
func (r *Repo) resolve(ctx context.Context, kind, key string, now time.Time) (Alert, bool, error) {
	a, err := scan(r.db.Write.QueryRowContext(ctx, `
		UPDATE alerts SET resolved_at = ? WHERE kind = ? AND key = ? AND resolved_at IS NULL AND acknowledged_at IS NULL
		RETURNING `+columns, now.Unix(), kind, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, false, nil
	}
	return a, err == nil, err
}

func (r *Repo) acknowledge(ctx context.Context, id, by int64, now time.Time) (Alert, bool, error) {
	a, err := scan(r.db.Write.QueryRowContext(ctx, `
		UPDATE alerts SET acknowledged_at = COALESCE(acknowledged_at, ?), acknowledged_by = COALESCE(acknowledged_by, ?)
		WHERE id = ? RETURNING `+columns, now.Unix(), by, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, false, nil
	}
	return a, err == nil, err
}

func (r *Repo) get(ctx context.Context, id int64) (Alert, bool, error) {
	a, err := scan(r.db.Read.QueryRowContext(ctx, "SELECT "+columns+" FROM alerts WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, false, nil
	}
	return a, err == nil, err
}

func (r *Repo) delete(ctx context.Context, id int64) (bool, error) {
	res, err := r.db.Write.ExecContext(ctx, "DELETE FROM alerts WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Filter narrows a list.
type Filter struct {
	ActiveOnly bool
	Severity   Severity
	Source     Source
}

func (r *Repo) list(ctx context.Context, f Filter, p page.Request) ([]Alert, int, error) {
	where, args := " WHERE 1 = 1", []any{}
	if f.ActiveOnly {
		where += " AND resolved_at IS NULL AND acknowledged_at IS NULL"
	}
	if f.Severity != "" {
		where += " AND severity = ?"
		args = append(args, f.Severity)
	}
	if f.Source != "" {
		where += " AND source = ?"
		args = append(args, f.Source)
	}
	var total int
	if err := r.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM alerts"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Read.QueryContext(ctx,
		"SELECT "+columns+" FROM alerts"+where+" ORDER BY "+p.OrderBy("last_at DESC")+", id DESC LIMIT ? OFFSET ?",
		append(args, p.Size, p.Offset())...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// pruneBefore removes alerts that ended (resolved or acknowledged) before t.
func (r *Repo) pruneBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := r.db.Write.ExecContext(ctx, `DELETE FROM alerts
		WHERE MAX(COALESCE(resolved_at, 0), COALESCE(acknowledged_at, 0)) BETWEEN 1 AND ?`, t.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
