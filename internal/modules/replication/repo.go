package replication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"netshare-hub/internal/platform/db"
)

// repo is the only code that knows the replication tables.
type repo struct {
	db *db.DB
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func unix(t time.Time) int64 { return t.Unix() }

func timeOf(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

// --- jobs ---

const jobColumns = `j.id, j.name, j.share_id, COALESCE(s.name, ''), j.local_path, COALESCE(j.peer_id, 0), COALESCE(p.name, ''),
	j.mirror, j.bandwidth_kib, j.excludes, j.schedule, j.at_minute, j.weekday, j.enabled, j.next_run_at, j.created_at, j.updated_at`

const jobFrom = ` FROM replication_jobs j LEFT JOIN shares s ON s.id = j.share_id LEFT JOIN replication_peers p ON p.id = j.peer_id`

func scanJob(sc interface{ Scan(...any) error }) (ReplicationJob, error) {
	var j ReplicationJob
	var excludes string
	var next sql.NullInt64
	var created, updated int64
	err := sc.Scan(&j.ID, &j.Name, &j.ShareID, &j.ShareName, &j.LocalPath, &j.PeerID, &j.PeerName, &j.Mirror, &j.BandwidthKiB,
		&excludes, &j.Schedule, &j.AtMinute, &j.Weekday, &j.Enabled, &next, &created, &updated)
	if err != nil {
		return ReplicationJob{}, err
	}
	if err := json.Unmarshal([]byte(excludes), &j.Excludes); err != nil || j.Excludes == nil {
		j.Excludes = []string{}
	}
	j.NextRunAt = timeOf(next)
	j.CreatedAt, j.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return j, nil
}

func (r *repo) jobs(ctx context.Context, q querier, where string, args ...any) ([]ReplicationJob, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+jobColumns+jobFrom+where+" ORDER BY j.name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReplicationJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (r *repo) job(ctx context.Context, q querier, id int64) (ReplicationJob, bool, error) {
	list, err := r.jobs(ctx, q, " WHERE j.id = ?", id)
	if err != nil || len(list) == 0 {
		return ReplicationJob{}, false, err
	}
	return list[0], true, nil
}

func excludesJSON(e []string) string {
	if e == nil {
		e = []string{}
	}
	b, _ := json.Marshal(e) // plain strings: cannot fail
	return string(b)
}

func peerArg(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (r *repo) insertJob(ctx context.Context, j ReplicationJob, by int64, now time.Time) (int64, error) {
	res, err := r.db.Write.ExecContext(ctx, `INSERT INTO replication_jobs (name, share_id, local_path, peer_id, mirror, bandwidth_kib,
		excludes, schedule, at_minute, weekday, enabled, next_run_at, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.Name, j.ShareID, j.LocalPath, peerArg(j.PeerID), j.Mirror, j.BandwidthKiB, excludesJSON(j.Excludes), j.Schedule,
		j.AtMinute, j.Weekday, j.Enabled, nullTime(j.NextRunAt), by, unix(now), unix(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *repo) updateJob(ctx context.Context, j ReplicationJob, now time.Time) error {
	_, err := r.db.Write.ExecContext(ctx, `UPDATE replication_jobs SET name = ?, local_path = ?, peer_id = ?, mirror = ?,
		bandwidth_kib = ?, excludes = ?, schedule = ?, at_minute = ?, weekday = ?, enabled = ?, next_run_at = ?, updated_at = ?
		WHERE id = ?`,
		j.Name, j.LocalPath, peerArg(j.PeerID), j.Mirror, j.BandwidthKiB, excludesJSON(j.Excludes), j.Schedule, j.AtMinute,
		j.Weekday, j.Enabled, nullTime(j.NextRunAt), unix(now), j.ID)
	return err
}

func (r *repo) setNextRun(ctx context.Context, id int64, next *time.Time) error {
	_, err := r.db.Write.ExecContext(ctx, "UPDATE replication_jobs SET next_run_at = ? WHERE id = ?", nullTime(next), id)
	return err
}

func (r *repo) deleteJob(ctx context.Context, id int64) error {
	_, err := r.db.Write.ExecContext(ctx, "DELETE FROM replication_jobs WHERE id = ?", id)
	return err
}

func (r *repo) nameTaken(ctx context.Context, table, name string, except int64) (bool, error) {
	var one int
	err := r.db.Read.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE name = ? AND id != ?", name, except).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// --- runs ---

const runColumns = `id, job_id, task_id, dry_run, status, files, transferred, created, deleted, bytes, changes, truncated, error,
	queued_at, started_at, finished_at`

func scanRun(sc interface{ Scan(...any) error }) (ReplicationRun, error) {
	var r ReplicationRun
	var changes string
	var queued int64
	var started, finished sql.NullInt64
	err := sc.Scan(&r.ID, &r.JobID, &r.TaskID, &r.DryRun, &r.Status, &r.Files, &r.Transferred, &r.Created, &r.Deleted, &r.Bytes,
		&changes, &r.Truncated, &r.Error, &queued, &started, &finished)
	if err != nil {
		return ReplicationRun{}, err
	}
	if err := json.Unmarshal([]byte(changes), &r.Changes); err != nil || r.Changes == nil {
		r.Changes = []string{}
	}
	r.QueuedAt = time.Unix(queued, 0).UTC()
	r.StartedAt, r.FinishedAt = timeOf(started), timeOf(finished)
	return r, nil
}

func (r *repo) runs(ctx context.Context, jobID int64, limit int) ([]ReplicationRun, error) {
	rows, err := r.db.Read.QueryContext(ctx, "SELECT "+runColumns+" FROM replication_runs WHERE job_id = ? ORDER BY id DESC LIMIT ?",
		jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReplicationRun{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (r *repo) run(ctx context.Context, id int64) (ReplicationRun, bool, error) {
	run, err := scanRun(r.db.Read.QueryRowContext(ctx, "SELECT "+runColumns+" FROM replication_runs WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return ReplicationRun{}, false, nil
	}
	return run, err == nil, err
}

// lastRuns returns each job's latest real (not dry) run.
func (r *repo) lastRuns(ctx context.Context) (map[int64]ReplicationRun, error) {
	rows, err := r.db.Read.QueryContext(ctx, "SELECT "+runColumns+` FROM replication_runs WHERE id IN
		(SELECT MAX(id) FROM replication_runs WHERE dry_run = 0 GROUP BY job_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ReplicationRun{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out[run.JobID] = run
	}
	return out, rows.Err()
}

// insertRun records a run that is starting.
func (r *repo) insertRun(ctx context.Context, jobID int64, dryRun bool, task string, now time.Time) (int64, error) {
	res, err := r.db.Write.ExecContext(ctx, `INSERT INTO replication_runs (job_id, task_id, dry_run, status, queued_at, started_at)
		VALUES (?, ?, ?, 'running', ?, ?)`, jobID, task, dryRun, unix(now), unix(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *repo) startRun(ctx context.Context, id int64, now time.Time) error {
	_, err := r.db.Write.ExecContext(ctx, "UPDATE replication_runs SET status = 'running', started_at = ? WHERE id = ?", unix(now), id)
	return err
}

func (r *repo) finishRun(ctx context.Context, run ReplicationRun, now time.Time) error {
	changes, _ := json.Marshal(run.Changes)
	_, err := r.db.Write.ExecContext(ctx, `UPDATE replication_runs SET status = ?, files = ?, transferred = ?, created = ?,
		deleted = ?, bytes = ?, changes = ?, truncated = ?, error = ?, finished_at = ? WHERE id = ?`,
		run.Status, run.Files, run.Transferred, run.Created, run.Deleted, run.Bytes, string(changes), run.Truncated, run.Error,
		unix(now), run.ID)
	return err
}

// pruneRuns keeps the latest keep runs of each job.
func (r *repo) pruneRuns(ctx context.Context, keep int) error {
	_, err := r.db.Write.ExecContext(ctx, `DELETE FROM replication_runs WHERE id IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY job_id ORDER BY id DESC) AS n FROM replication_runs)
		WHERE n > ?) AND status NOT IN ('queued', 'running')`, keep)
	return err
}

// --- peers ---

func (r *repo) peers(ctx context.Context, q querier) ([]ReplicationPeer, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, name, url, host, ssh_port, host_key, created_at FROM replication_peers ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReplicationPeer{}
	for rows.Next() {
		var p ReplicationPeer
		var created int64
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &p.Host, &p.SSHPort, &p.HostKey, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- receivers ---

type receiverRow struct {
	ReplicationReceiver
	PublicKey   string
	PairSecret  string
	PairExpires time.Time
}

func (r *repo) receivers(ctx context.Context, q querier) ([]receiverRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, path, public_key, pair_secret, pair_expires, paired_at, created_at
		FROM replication_receivers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []receiverRow{}
	for rows.Next() {
		var rc receiverRow
		var expires, created int64
		var paired sql.NullInt64
		if err := rows.Scan(&rc.ID, &rc.Name, &rc.Path, &rc.PublicKey, &rc.PairSecret, &expires, &paired, &created); err != nil {
			return nil, err
		}
		rc.PairExpires = time.Unix(expires, 0).UTC()
		rc.PairedAt = timeOf(paired)
		rc.Paired = rc.PublicKey != ""
		rc.CreatedAt = time.Unix(created, 0).UTC()
		if rc.PairSecret != "" {
			t := rc.PairExpires
			rc.PairingExpires = &t
		}
		out = append(out, rc)
	}
	return out, rows.Err()
}
