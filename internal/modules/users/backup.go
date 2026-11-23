package users

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"netshare-hub/internal/platform/password"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
)

// Config backup and restore (step 3.6). A backup holds every user with
// their password hash, so a restore on another install keeps everyone's
// web password. The backup module orchestrates; this file only knows the
// users table and its rules.

// Record is a user as a config backup holds it.
type Record struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"displayName"`
	Role         rbac.Role `json:"role"`
	PasswordHash string    `json:"passwordHash"`
	Disabled     bool      `json:"disabled"`
	SMBAccess    bool      `json:"smbAccess"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// User returns the record without its password hash.
func (r Record) User() User {
	return User{ID: r.ID, Username: r.Username, DisplayName: r.DisplayName, Role: r.Role, Disabled: r.Disabled,
		SMBAccess: r.SMBAccess, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// Records returns every user with their password hash.
func (s *Service) Records(ctx context.Context) ([]Record, error) {
	rows, err := s.repo.db.Read.QueryContext(ctx,
		"SELECT "+columns+", password_hash FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var created, updated int64
		if err := rows.Scan(&r.ID, &r.Username, &r.DisplayName, &r.Role, &r.Disabled, &r.SMBAccess,
			&created, &updated, &r.PasswordHash); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// MaxRecords is the most users a backup may hold.
const MaxRecords = 10000

// CheckRecords validates the users of a backup with the same rules as the
// API, plus what a restore needs: unique ids, real password hashes, and at
// least one enabled admin (or nobody could sign in afterwards). Field names
// are "users[i].<field>".
func CheckRecords(v *validate.Errors, recs []Record) {
	if len(recs) > MaxRecords {
		v.Add("users", "too_many", fmt.Sprintf("At most %d users.", MaxRecords))
		return
	}
	ids, names := map[int64]bool{}, map[string]bool{}
	admins := 0
	for i, r := range recs {
		f := fmt.Sprintf("users[%d].", i)
		v.Check(r.ID > 0 && !ids[r.ID], f+"id", "invalid", "Each user needs its own positive id.")
		ids[r.ID] = true
		name := strings.ToLower(r.Username)
		v.Check(usernamePattern.MatchString(r.Username) && name == r.Username, f+"username", "invalid_format",
			"Use 2-32 lowercase letters, digits, - or _, starting with a letter.")
		v.Check(!names[name], f+"username", "duplicate", "This username appears twice.")
		names[name] = true
		v.Check(len(r.DisplayName) <= 200 && !strings.ContainsAny(r.DisplayName, "\x00\r\n"), f+"displayName", "invalid_format",
			"At most 200 characters, on one line.")
		v.Check(r.Role.Valid(), f+"role", "invalid_choice", "Unknown role.")
		v.Check(password.Valid(r.PasswordHash), f+"passwordHash", "invalid_format", "Not a password hash NetShare Hub can use.")
		if r.Role == rbac.RoleAdmin && !r.Disabled {
			admins++
		}
	}
	v.Check(admins > 0, "users", "no_admin", "The backup has no enabled admin, so nobody could sign in after restoring it.")
}

// ReplaceAll replaces every user with recs, inside tx. Deleting the old
// users also deletes everything that belongs to them (sessions, jobs,
// uploads, share access): everyone signs in again afterwards.
func (s *Service) ReplaceAll(ctx context.Context, tx *sql.Tx, recs []Record) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM users"); err != nil {
		return err
	}
	for _, r := range recs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO users (id, username, display_name, role, password_hash, disabled, smb_access, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Username, r.DisplayName, r.Role, r.PasswordHash, r.Disabled, r.SMBAccess,
			r.CreatedAt.Unix(), r.UpdatedAt.Unix()); err != nil {
			return err
		}
	}
	return nil
}
