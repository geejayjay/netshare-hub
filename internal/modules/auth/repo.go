package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"netshare-hub/internal/platform/db"
)

// Repo is the only code that knows the sessions table.
type Repo struct {
	db *db.DB
}

func NewRepo(d *db.DB) *Repo { return &Repo{db: d} }

func (r *Repo) Create(ctx context.Context, s Session) error {
	_, err := r.db.Write.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, csrf_token, ip, user_agent, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.TokenHash, s.UserID, s.CSRFToken, s.IP, s.UserAgent, s.CreatedAt.Unix(), s.ExpiresAt.Unix())
	return err
}

// Get returns a session that has not expired.
func (r *Repo) Get(ctx context.Context, tokenHash string, now time.Time) (Session, bool, error) {
	var s Session
	var created, expires int64
	err := r.db.Read.QueryRowContext(ctx,
		`SELECT token_hash, user_id, csrf_token, ip, user_agent, created_at, expires_at
		 FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, now.Unix()).
		Scan(&s.TokenHash, &s.UserID, &s.CSRFToken, &s.IP, &s.UserAgent, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	s.CreatedAt = time.Unix(created, 0).UTC()
	s.ExpiresAt = time.Unix(expires, 0).UTC()
	return s, err == nil, err
}

func (r *Repo) Delete(ctx context.Context, tokenHash string) error {
	_, err := r.db.Write.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

func (r *Repo) DeleteForUser(ctx context.Context, userID int64) error {
	_, err := r.db.Write.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
	return err
}

func (r *Repo) DeleteExpired(ctx context.Context, now time.Time) error {
	_, err := r.db.Write.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", now.Unix())
	return err
}
