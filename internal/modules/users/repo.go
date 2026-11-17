package users

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

// Repo is the only code that knows the users table.
type Repo struct {
	db *db.DB
}

func NewRepo(d *db.DB) *Repo { return &Repo{db: d} }

const columns = "id, username, display_name, role, disabled, created_at, updated_at"

type scanner interface{ Scan(dest ...any) error }

func scan(s scanner) (User, error) {
	var u User
	var created, updated int64
	err := s.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Disabled, &created, &updated)
	u.CreatedAt = time.Unix(created, 0).UTC()
	u.UpdatedAt = time.Unix(updated, 0).UTC()
	return u, err
}

// List returns one page of users and the total number of matches.
func (r *Repo) List(ctx context.Context, p page.Request) ([]User, int, error) {
	where, args := "", []any{}
	if p.Search != "" {
		where = ` WHERE username LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\'`
		like := "%" + escapeLike(p.Search) + "%"
		args = append(args, like, like)
	}

	var total int
	if err := r.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM users"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := "SELECT " + columns + " FROM users" + where +
		" ORDER BY " + p.OrderBy("username ASC") + " LIMIT ? OFFSET ?"
	rows, err := r.db.Read.QueryContext(ctx, query, append(args, p.Size, p.Offset())...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// Get returns a user, or (User{}, false, nil) when it does not exist.
func (r *Repo) Get(ctx context.Context, id int64) (User, bool, error) {
	return found(scan(r.db.Read.QueryRowContext(ctx, "SELECT "+columns+" FROM users WHERE id = ?", id)))
}

// GetByUsername also returns the password hash, for login only.
func (r *Repo) GetByUsername(ctx context.Context, username string) (User, string, bool, error) {
	var hash string
	row := r.db.Read.QueryRowContext(ctx,
		"SELECT "+columns+", password_hash FROM users WHERE username = ?", username)
	u, ok, err := found(scan(scanWith{row, &hash}))
	return u, hash, ok, err
}

func (r *Repo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// CountActiveAdmins is used to protect the last admin.
func (r *Repo) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.db.Read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0", rbac.RoleAdmin).Scan(&n)
	return n, err
}

func (r *Repo) Create(ctx context.Context, in CreateInput, passwordHash string, now time.Time) (int64, error) {
	res, err := r.db.Write.ExecContext(ctx,
		`INSERT INTO users (username, display_name, role, password_hash, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		in.Username, in.DisplayName, in.Role, passwordHash, now.Unix(), now.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Update writes the full new state of a user. passwordHash "" keeps the old one.
func (r *Repo) Update(ctx context.Context, u User, passwordHash string, now time.Time) error {
	_, err := r.db.Write.ExecContext(ctx,
		`UPDATE users SET display_name = ?, role = ?, disabled = ?, updated_at = ?,
		        password_hash = CASE WHEN ? = '' THEN password_hash ELSE ? END
		 WHERE id = ?`,
		u.DisplayName, u.Role, u.Disabled, now.Unix(), passwordHash, passwordHash, u.ID)
	return err
}

func (r *Repo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.Write.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	return err
}

// --- small helpers ---

func found(u User, err error) (User, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	return u, err == nil, err
}

// scanWith scans the standard columns plus extra trailing ones.
type scanWith struct {
	row   scanner
	extra any
}

func (s scanWith) Scan(dest ...any) error { return s.row.Scan(append(dest, s.extra)...) }

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
