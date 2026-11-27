package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
)

// API tokens let scripts and tools call the API without a browser. A token
// belongs to one user and acts as that user (so it can never do more than
// the user may, and stops working when the user is disabled or deleted). It
// may be read-only and may expire. Only its hash is stored; the token
// itself is shown once, when it is created.

// TokenPrefix starts every API token, so a leaked one is easy to recognize
// (and to search for in logs and repositories).
const TokenPrefix = "nsh_"

const (
	maxTokensPerUser = 20
	maxTokenName     = 64
	maxTokenDays     = 3650
	// tokenTouchEvery limits how often "last used" is written.
	tokenTouchEvery = time.Minute
)

// Error codes owned by API tokens.
const (
	CodeTokenLimit errs.Code = "TOKEN_LIMIT"
)

// APIToken describes a token. The token itself is never in it.
type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix" doc:"The first characters of the token, to tell tokens apart."`
	ReadOnly   bool       `json:"readOnly"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

// CreatedAPIToken is returned once, when a token is created.
type CreatedAPIToken struct {
	APIToken
	Token string `json:"token" doc:"The token. It is shown only now: store it safely. Send it as 'Authorization: Bearer <token>'."`
}

type tokenRow struct {
	APIToken
	userID int64
}

// --- repo ---

func (r *Repo) createToken(ctx context.Context, userID int64, name, hash, prefix string, readOnly bool, created time.Time, expires int64) (int64, error) {
	res, err := r.db.Write.ExecContext(ctx,
		`INSERT INTO api_tokens (user_id, name, token_hash, prefix, read_only, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, name, hash, prefix, readOnly, created.Unix(), expires)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const tokenColumns = `id, user_id, name, prefix, read_only, created_at, last_used_at, expires_at`

func scanToken(row interface{ Scan(...any) error }) (tokenRow, error) {
	var t tokenRow
	var created, used, expires int64
	if err := row.Scan(&t.ID, &t.userID, &t.Name, &t.Prefix, &t.ReadOnly, &created, &used, &expires); err != nil {
		return tokenRow{}, err
	}
	t.CreatedAt = time.Unix(created, 0).UTC()
	if used > 0 {
		u := time.Unix(used, 0).UTC()
		t.LastUsedAt = &u
	}
	if expires > 0 {
		e := time.Unix(expires, 0).UTC()
		t.ExpiresAt = &e
	}
	return t, nil
}

func (r *Repo) listTokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := r.db.Read.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]APIToken, 0)
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t.APIToken)
	}
	return out, rows.Err()
}

func (r *Repo) countTokens(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.db.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// tokenByHash returns a token that has not expired.
func (r *Repo) tokenByHash(ctx context.Context, hash string, now time.Time) (tokenRow, bool, error) {
	t, err := scanToken(r.db.Read.QueryRowContext(ctx,
		`SELECT `+tokenColumns+` FROM api_tokens
		 WHERE token_hash = ? AND (expires_at = 0 OR expires_at > ?)`, hash, now.Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return tokenRow{}, false, nil
	}
	return t, err == nil, err
}

func (r *Repo) touchToken(ctx context.Context, id int64, now time.Time) error {
	_, err := r.db.Write.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now.Unix(), id)
	return err
}

func (r *Repo) deleteToken(ctx context.Context, userID, id int64) (bool, error) {
	res, err := r.db.Write.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// --- service ---

// CreateToken makes a new API token for the logged-in user. expiresInDays
// 0 means it never expires.
func (s *Service) CreateToken(ctx context.Context, p *httpx.Principal, name string, readOnly bool, expiresInDays int) (CreatedAPIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenName {
		return CreatedAPIToken{}, errs.Invalid(errs.CodeValidationFailed, "Check the token's name.",
			errs.FieldError{Field: "name", Message: "Give the token a name of at most 64 characters."})
	}
	if expiresInDays < 0 || expiresInDays > maxTokenDays {
		return CreatedAPIToken{}, errs.Invalid(errs.CodeValidationFailed, "Check the expiry.",
			errs.FieldError{Field: "expiresInDays", Message: "Use 0 (never) up to 3650 days."})
	}
	n, err := s.repo.countTokens(ctx, p.UserID)
	if err != nil {
		return CreatedAPIToken{}, err
	}
	if n >= maxTokensPerUser {
		return CreatedAPIToken{}, errs.Conflict(CodeTokenLimit, "You have 20 API tokens already. Revoke one you no longer use first.")
	}

	token := TokenPrefix + randomToken()
	now := s.now().UTC().Truncate(time.Second)
	var expires int64
	var expiresAt *time.Time
	if expiresInDays > 0 {
		e := now.AddDate(0, 0, expiresInDays)
		expires, expiresAt = e.Unix(), &e
	}
	prefix := token[:len(TokenPrefix)+6]
	id, err := s.repo.createToken(ctx, p.UserID, name, hashToken(token), prefix, readOnly, now, expires)
	if err != nil {
		return CreatedAPIToken{}, err
	}
	slog.InfoContext(ctx, "api token created", "token_id", id, "name", name, "read_only", readOnly, "expires_in_days", expiresInDays)
	return CreatedAPIToken{
		APIToken: APIToken{ID: id, Name: name, Prefix: prefix, ReadOnly: readOnly, CreatedAt: now, ExpiresAt: expiresAt},
		Token:    token,
	}, nil
}

// ListTokens lists the logged-in user's API tokens.
func (s *Service) ListTokens(ctx context.Context, p *httpx.Principal) ([]APIToken, error) {
	return s.repo.listTokens(ctx, p.UserID)
}

// RevokeToken deletes one of the logged-in user's API tokens.
func (s *Service) RevokeToken(ctx context.Context, p *httpx.Principal, id int64) error {
	ok, err := s.repo.deleteToken(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	if !ok {
		return errs.NotFound(errs.CodeNotFound, "That API token does not exist.")
	}
	slog.InfoContext(ctx, "api token revoked", "token_id", id)
	return nil
}

// ResolveToken turns an API token into its owner's principal, or nil.
func (s *Service) ResolveToken(ctx context.Context, token string) (*httpx.Principal, error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, nil
	}
	now := s.now()
	t, ok, err := s.repo.tokenByHash(ctx, hashToken(token), now)
	if err != nil || !ok {
		return nil, err
	}
	user, err := s.users.Get(ctx, t.userID)
	if err != nil || user.Disabled {
		return nil, nil
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= tokenTouchEvery {
		_ = s.repo.touchToken(ctx, t.ID, now)
	}
	return &httpx.Principal{
		UserID:      user.ID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		TokenID:     t.ID,
		ReadOnly:    t.ReadOnly,
	}, nil
}

// bearerToken returns the token of an "Authorization: Bearer" header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// --- handlers ---

type createTokenInput struct {
	Body struct {
		Name          string `json:"name" minLength:"1" maxLength:"64"`
		ReadOnly      bool   `json:"readOnly,omitempty" doc:"Read-only tokens may only read (GET)."`
		ExpiresInDays int    `json:"expiresInDays,omitempty" minimum:"0" maximum:"3650" doc:"0 = never expires."`
	}
}

type createTokenOutput struct{ Body CreatedAPIToken }

type listTokensOutput struct {
	Body struct {
		Items []APIToken `json:"items" nullable:"false"`
	}
}

type tokenIDInput struct {
	ID int64 `path:"id"`
}

func registerTokens(api huma.API, svc *Service) {
	// Managing tokens needs a browser login: a token can never make,
	// list or revoke tokens, so a leaked one cannot keep itself alive.
	access := httpx.Authenticated().BrowserOnly()

	httpx.Register(api, httpx.Route{
		ID: "listApiTokens", Method: http.MethodGet, Path: "/auth/tokens",
		Summary: "List your API tokens", Tag: "Auth", Access: access,
	}, func(ctx context.Context, _ *struct{}) (*listTokensOutput, error) {
		items, err := svc.ListTokens(ctx, httpx.PrincipalFrom(ctx))
		if err != nil {
			return nil, err
		}
		out := &listTokensOutput{}
		out.Body.Items = items
		return out, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "createApiToken", Method: http.MethodPost, Path: "/auth/tokens",
		Summary: "Create an API token", Tag: "Auth", Access: access, Status: http.StatusCreated,
	}, func(ctx context.Context, in *createTokenInput) (*createTokenOutput, error) {
		t, err := svc.CreateToken(ctx, httpx.PrincipalFrom(ctx), in.Body.Name, in.Body.ReadOnly, in.Body.ExpiresInDays)
		if err != nil {
			return nil, err
		}
		return &createTokenOutput{Body: t}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "revokeApiToken", Method: http.MethodDelete, Path: "/auth/tokens/{id}",
		Summary: "Revoke an API token", Tag: "Auth", Access: access, Status: http.StatusNoContent,
	}, func(ctx context.Context, in *tokenIDInput) (*struct{}, error) {
		return nil, svc.RevokeToken(ctx, httpx.PrincipalFrom(ctx), in.ID)
	})
}
