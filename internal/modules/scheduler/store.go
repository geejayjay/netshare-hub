package scheduler

import (
	"context"
	"time"

	"netshare-hub/internal/platform/db"
)

// Store persists admin schedule overrides so disabled tasks and custom
// frequencies survive server restarts.
type Store struct {
	db *db.DB
}

func newStore(database *db.DB) *Store {
	return &Store{db: database}
}

type savedRule struct {
	Name         string
	Enabled      bool
	Mode         string
	EveryMinutes int
	DailyMinutes int
	Weekday      int
}

func (st *Store) loadAll(ctx context.Context) (map[string]savedRule, error) {
	rows, err := st.db.Read.QueryContext(ctx,
		`SELECT name, enabled, mode, every_minutes, daily_minutes, weekday FROM scheduler_tasks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]savedRule{}
	for rows.Next() {
		var r savedRule
		var enabled int
		if err := rows.Scan(&r.Name, &enabled, &r.Mode, &r.EveryMinutes, &r.DailyMinutes, &r.Weekday); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		out[r.Name] = r
	}
	return out, rows.Err()
}

func (st *Store) save(ctx context.Context, r savedRule) error {
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	_, err := st.db.Write.ExecContext(ctx, `
		INSERT INTO scheduler_tasks (name, enabled, mode, every_minutes, daily_minutes, weekday, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			enabled = excluded.enabled,
			mode = excluded.mode,
			every_minutes = excluded.every_minutes,
			daily_minutes = excluded.daily_minutes,
			weekday = excluded.weekday,
			updated_at = excluded.updated_at`,
		r.Name, enabled, r.Mode, r.EveryMinutes, r.DailyMinutes, r.Weekday, time.Now().Unix(),
	)
	return err
}

func (st *Store) delete(ctx context.Context, name string) error {
	_, err := st.db.Write.ExecContext(ctx, `DELETE FROM scheduler_tasks WHERE name = ?`, name)
	return err
}
