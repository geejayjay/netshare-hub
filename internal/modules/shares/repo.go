package shares

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/page"
)

// Repo is the only code that knows the shares and share_access tables.
type Repo struct {
	db *db.DB
}

func NewRepo(d *db.DB) *Repo { return &Repo{db: d} }

// querier is satisfied by both a connection pool and a transaction, so every
// query can run inside the transaction that guards a config change.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const columns = "id, name, path, comment, read_only, guest_access, preset, hide_unreadable, time_machine, time_machine_max_gib, network_rules, created_at, updated_at"

func scan(s interface{ Scan(...any) error }) (Share, error) {
	var sh Share
	var created, updated int64
	var rules string
	err := s.Scan(&sh.ID, &sh.Name, &sh.Path, &sh.Comment, &sh.ReadOnly, &sh.GuestAccess, &sh.Preset, &sh.HideUnreadable,
		&sh.TimeMachine, &sh.TimeMachineMaxGiB, &rules, &created, &updated)
	if err == nil {
		err = json.Unmarshal([]byte(rules), &sh.NetworkRules)
	}
	if sh.NetworkRules == nil {
		sh.NetworkRules = []NetworkRule{}
	}
	sh.CreatedAt = time.Unix(created, 0).UTC()
	sh.UpdatedAt = time.Unix(updated, 0).UTC()
	sh.Access = []ShareAccess{}
	return sh, err
}

// List returns one page of shares (with their access lists) and the total.
func (r *Repo) List(ctx context.Context, p page.Request) ([]Share, int, error) {
	where, args := "", []any{}
	if p.Search != "" {
		where = ` WHERE name LIKE ? ESCAPE '\' OR path LIKE ? ESCAPE '\'`
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p.Search) + "%"
		args = append(args, like, like)
	}

	var total int
	if err := r.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM shares"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := "SELECT " + columns + " FROM shares" + where +
		" ORDER BY " + p.OrderBy("name ASC") + " LIMIT ? OFFSET ?"
	shares, err := r.query(ctx, r.db.Read, query, append(args, p.Size, p.Offset())...)
	return shares, total, err
}

// All returns every share with its access list. It takes a querier so a
// config can be rendered from inside an open transaction.
func (r *Repo) All(ctx context.Context, q querier) ([]Share, error) {
	return r.query(ctx, q, "SELECT "+columns+" FROM shares ORDER BY name")
}

// ListForUser returns the shares a user is listed on, and every guest share,
// with the user's configured level ("" for a guest share they are not on).
// The service turns that into what the user can effectively do.
func (r *Repo) ListForUser(ctx context.Context, userID int64) ([]Share, []Level, error) {
	rows, err := r.db.Read.QueryContext(ctx, `
		SELECT s.id, s.name, s.path, s.comment, s.read_only, s.guest_access, s.preset, s.created_at, s.updated_at,
		       COALESCE(sa.level, '')
		FROM shares s
		LEFT JOIN share_access sa ON sa.share_id = s.id AND sa.user_id = ?
		WHERE sa.user_id IS NOT NULL OR s.guest_access = 1
		ORDER BY s.name ASC`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var shares []Share
	var levels []Level
	for rows.Next() {
		var sh Share
		var created, updated int64
		var level Level
		if err := rows.Scan(&sh.ID, &sh.Name, &sh.Path, &sh.Comment, &sh.ReadOnly, &sh.GuestAccess, &sh.Preset,
			&created, &updated, &level); err != nil {
			return nil, nil, err
		}
		shares = append(shares, sh)
		levels = append(levels, level)
	}
	return shares, levels, rows.Err()
}

// Get returns one share, or (Share{}, false, nil) when it does not exist.
func (r *Repo) Get(ctx context.Context, q querier, id int64) (Share, bool, error) {
	shares, err := r.query(ctx, q, "SELECT "+columns+" FROM shares WHERE id = ?", id)
	if err != nil || len(shares) == 0 {
		return Share{}, false, err
	}
	return shares[0], true, nil
}

// GetByName returns a share by name (names are case-insensitive).
func (r *Repo) GetByName(ctx context.Context, name string) (Share, bool, error) {
	shares, err := r.query(ctx, r.db.Read, "SELECT "+columns+" FROM shares WHERE name = ?", name)
	if err != nil || len(shares) == 0 {
		return Share{}, false, err
	}
	return shares[0], true, nil
}

// NameTaken reports whether another share already uses the name.
func (r *Repo) NameTaken(ctx context.Context, name string) (bool, error) {
	var one int
	err := r.db.Read.QueryRowContext(ctx, "SELECT 1 FROM shares WHERE name = ?", name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// query loads shares and attaches their access rows (user ids and levels;
// the service fills in usernames).
func (r *Repo) query(ctx context.Context, q querier, query string, args ...any) ([]Share, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var shares []Share
	index := map[int64]int{}
	for rows.Next() {
		sh, err := scan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		index[sh.ID] = len(shares)
		shares = append(shares, sh)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(shares) == 0 {
		return shares, err
	}

	ids := make([]any, 0, len(shares))
	for _, sh := range shares {
		ids = append(ids, sh.ID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	access, err := q.QueryContext(ctx,
		"SELECT share_id, user_id, level FROM share_access WHERE share_id IN ("+placeholders+") ORDER BY user_id", ids...)
	if err != nil {
		return nil, err
	}
	defer access.Close()
	for access.Next() {
		var shareID int64
		var a ShareAccess
		if err := access.Scan(&shareID, &a.UserID, &a.Level); err != nil {
			return nil, err
		}
		i := index[shareID]
		shares[i].Access = append(shares[i].Access, a)
	}
	return shares, access.Err()
}

func (r *Repo) Insert(ctx context.Context, q querier, in CreateInput, now time.Time) (int64, error) {
	res, err := q.ExecContext(ctx,
		`INSERT INTO shares (name, path, comment, read_only, guest_access, preset, hide_unreadable, time_machine, time_machine_max_gib,
			network_rules, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Path, in.Comment, in.ReadOnly, in.GuestAccess, in.Preset, in.HideUnreadable, in.TimeMachine, in.TimeMachineMaxGiB,
		rulesJSON(in.NetworkRules), now.Unix(), now.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repo) UpdateComment(ctx context.Context, q querier, id int64, comment string, now time.Time) error {
	_, err := q.ExecContext(ctx, "UPDATE shares SET comment = ?, updated_at = ? WHERE id = ?", comment, now.Unix(), id)
	return err
}

// settings are a share's settings other than its comment, network rules
// and access list.
type settings struct {
	Rules
	Preset            string
	HideUnreadable    bool
	TimeMachineMaxGiB int
}

// UpdateSettings changes the share-wide settings.
func (r *Repo) UpdateSettings(ctx context.Context, q querier, id int64, s settings, now time.Time) error {
	_, err := q.ExecContext(ctx,
		`UPDATE shares SET read_only = ?, guest_access = ?, preset = ?, hide_unreadable = ?, time_machine = ?, time_machine_max_gib = ?,
			updated_at = ? WHERE id = ?`,
		s.ReadOnly, s.GuestAccess, s.Preset, s.HideUnreadable, s.TimeMachine, s.TimeMachineMaxGiB, now.Unix(), id)
	return err
}

// timeMachineShares returns the names of the Time Machine shares.
func (r *Repo) timeMachineShares(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM shares WHERE time_machine = 1 ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// enableMacSupport turns Mac support on (a Time Machine share needs it).
func (r *Repo) enableMacSupport(ctx context.Context, q querier, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO samba_server_settings (id, mac_support, updated_at) VALUES (1, 1, ?)
		ON CONFLICT (id) DO UPDATE SET mac_support = 1`, now.Unix())
	return err
}

// UpdateNetworkRules replaces a share's network rules.
func (r *Repo) UpdateNetworkRules(ctx context.Context, q querier, id int64, rules []NetworkRule, now time.Time) error {
	_, err := q.ExecContext(ctx, "UPDATE shares SET network_rules = ?, updated_at = ? WHERE id = ?", rulesJSON(rules), now.Unix(), id)
	return err
}

func rulesJSON(rules []NetworkRule) string {
	if len(rules) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(rules) // plain strings: cannot fail
	return string(b)
}

// Paths returns every share's folder, by share name.
func (r *Repo) Paths(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.Read.QueryContext(ctx, "SELECT name, path FROM shares")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, path string
		if err := rows.Scan(&name, &path); err != nil {
			return nil, err
		}
		out[name] = path
	}
	return out, rows.Err()
}

// ReplaceAccess sets the whole access list of a share.
func (r *Repo) ReplaceAccess(ctx context.Context, q querier, id int64, access []AccessInput, now time.Time) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM share_access WHERE share_id = ?", id); err != nil {
		return err
	}
	for _, a := range access {
		if _, err := q.ExecContext(ctx,
			"INSERT INTO share_access (share_id, user_id, level) VALUES (?, ?, ?)", id, a.UserID, a.Level); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx, "UPDATE shares SET updated_at = ? WHERE id = ?", now.Unix(), id)
	return err
}

func (r *Repo) Delete(ctx context.Context, q querier, id int64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM shares WHERE id = ?", id)
	return err
}

// DeleteUserAccess removes a user from every share and reports how many
// shares were affected.
func (r *Repo) DeleteUserAccess(ctx context.Context, q querier, userID int64) (int64, error) {
	res, err := q.ExecContext(ctx, "DELETE FROM share_access WHERE user_id = ?", userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
