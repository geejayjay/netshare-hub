package jobs

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/page"
)

// Repo is the only code that knows the jobs table.
type Repo struct {
	db *db.DB
}

func NewRepo(d *db.DB) *Repo { return &Repo{db: d} }

const columns = `id, kind, owner_id, title, status, progress_done, progress_total, message,
	error_code, error_message, attempts, cancel_requested, created_at, started_at, finished_at`

func scan(s interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var created, started, finished int64
	err := s.Scan(&j.ID, &j.Kind, &j.OwnerID, &j.Title, &j.Status, &j.Done, &j.Total, &j.Message,
		&j.ErrorCode, &j.ErrorMessage, &j.Attempts, &j.CancelRequested, &created, &started, &finished)
	j.CreatedAt = time.Unix(created, 0).UTC()
	j.StartedAt = optionalTime(started)
	j.FinishedAt = optionalTime(finished)
	return j, err
}

func optionalTime(unix int64) *time.Time {
	if unix == 0 {
		return nil
	}
	t := time.Unix(unix, 0).UTC()
	return &t
}

func (r *Repo) Insert(ctx context.Context, j Job, payload string) error {
	_, err := r.db.Write.ExecContext(ctx,
		`INSERT INTO jobs (id, kind, owner_id, title, status, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Kind, j.OwnerID, j.Title, StatusQueued, payload, j.CreatedAt.Unix())
	return err
}

// Get returns a job, or (Job{}, false, nil) when it does not exist.
func (r *Repo) Get(ctx context.Context, id string) (Job, bool, error) {
	j, err := scan(r.db.Read.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	return j, err == nil, err
}

// List returns one page of jobs, newest first. ownerID 0 means every owner.
func (r *Repo) List(ctx context.Context, ownerID int64, active bool, p page.Request) ([]Job, int, error) {
	where, args := " WHERE 1 = 1", []any{}
	if ownerID != 0 {
		where += " AND owner_id = ?"
		args = append(args, ownerID)
	}
	if active {
		where += " AND status IN ('queued', 'running')"
	}
	var total int
	if err := r.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Read.QueryContext(ctx,
		"SELECT "+columns+" FROM jobs"+where+" ORDER BY "+p.OrderBy("created_at DESC")+", rowid DESC LIMIT ? OFFSET ?",
		append(args, p.Size, p.Offset())...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

// Claim marks the oldest queued job (first in, first out: ties within the
// same second are broken by insertion order, the table's rowid) of one of the given kinds as running
// and returns it with its payload and checkpoint. ok is false when nothing
// is waiting.
func (r *Repo) Claim(ctx context.Context, kinds []string, now time.Time) (j Job, payload, checkpoint string, ok bool, err error) {
	if len(kinds) == 0 {
		return Job{}, "", "", false, nil
	}
	tx, err := r.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, "", "", false, err
	}
	defer tx.Rollback()

	placeholders := "?"
	args := []any{kinds[0]}
	for _, k := range kinds[1:] {
		placeholders += ",?"
		args = append(args, k)
	}
	var id string
	err = tx.QueryRowContext(ctx,
		"SELECT id, payload, checkpoint FROM jobs WHERE status = 'queued' AND kind IN ("+placeholders+") ORDER BY created_at, rowid LIMIT 1",
		args...).Scan(&id, &payload, &checkpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, "", "", false, nil
	}
	if err != nil {
		return Job{}, "", "", false, err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE jobs SET status = 'running', attempts = attempts + 1, started_at = ? WHERE id = ?", now.Unix(), id); err != nil {
		return Job{}, "", "", false, err
	}
	j, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE id = ?", id))
	if err != nil {
		return Job{}, "", "", false, err
	}
	return j, payload, checkpoint, true, tx.Commit()
}

// FailUnknownKinds ends queued jobs whose kind no code handles any more
// (for example after a feature was removed), so they do not wait forever.
func (r *Repo) FailUnknownKinds(ctx context.Context, known []string, now time.Time) error {
	query := "UPDATE jobs SET status = 'failed', error_code = ?, error_message = ?, finished_at = ? WHERE status = 'queued'"
	args := []any{CodeUnknownJobKind, "This kind of job is no longer supported.", now.Unix()}
	for _, k := range known {
		query += " AND kind != ?"
		args = append(args, k)
	}
	_, err := r.db.Write.ExecContext(ctx, query, args...)
	return err
}

// Interrupted returns jobs left "running" by a server that stopped.
func (r *Repo) Interrupted(ctx context.Context) ([]Job, error) {
	rows, err := r.db.Read.QueryContext(ctx, "SELECT "+columns+" FROM jobs WHERE status = 'running'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Requeue puts a job back in the queue (it resumes from its checkpoint).
func (r *Repo) Requeue(ctx context.Context, id string) error {
	_, err := r.db.Write.ExecContext(ctx, "UPDATE jobs SET status = 'queued' WHERE id = ? AND status = 'running'", id)
	return err
}

func (r *Repo) SaveProgress(ctx context.Context, id string, done, total int64, message string) error {
	_, err := r.db.Write.ExecContext(ctx,
		"UPDATE jobs SET progress_done = ?, progress_total = ?, message = ? WHERE id = ?", done, total, message, id)
	return err
}

func (r *Repo) SaveCheckpoint(ctx context.Context, id, checkpoint string) error {
	_, err := r.db.Write.ExecContext(ctx, "UPDATE jobs SET checkpoint = ? WHERE id = ?", checkpoint, id)
	return err
}

// Finish records the end of a job.
func (r *Repo) Finish(ctx context.Context, id string, status Status, code, message string, now time.Time) error {
	_, err := r.db.Write.ExecContext(ctx,
		"UPDATE jobs SET status = ?, error_code = ?, error_message = ?, finished_at = ? WHERE id = ?",
		status, code, message, now.Unix(), id)
	return err
}

// RequestCancel marks a job for cancellation. A queued job is cancelled at
// once; ok reports whether it was queued (true) or is running (false).
func (r *Repo) RequestCancel(ctx context.Context, id string, now time.Time) (wasQueued bool, err error) {
	res, err := r.db.Write.ExecContext(ctx,
		"UPDATE jobs SET status = 'cancelled', cancel_requested = 1, finished_at = ? WHERE id = ? AND status = 'queued'",
		now.Unix(), id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	_, err = r.db.Write.ExecContext(ctx, "UPDATE jobs SET cancel_requested = 1 WHERE id = ? AND status = 'running'", id)
	return false, err
}

// DeleteFinishedBefore removes old finished jobs and returns how many.
func (r *Repo) DeleteFinishedBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := r.db.Write.ExecContext(ctx,
		"DELETE FROM jobs WHERE status IN ('succeeded', 'failed', 'cancelled') AND finished_at < ?", t.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
