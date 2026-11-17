// Package auth handles login sessions and the first-run setup.
package auth

import (
	"context"
	"time"

	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/rbac"
)

// Users is what auth needs from the users module. Auth declares the interface
// itself, so it depends on a small contract and never on users' internals.
type Users interface {
	Authenticate(ctx context.Context, username, password string) (users.User, error)
	Get(ctx context.Context, id int64) (users.User, error)
	Count(ctx context.Context) (int, error)
	Create(ctx context.Context, in users.CreateInput) (users.User, error)
}

// Session is one logged-in browser.
type Session struct {
	TokenHash string
	UserID    int64
	CSRFToken string
	IP        string
	UserAgent string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionInfo is what the frontend needs to know about the current login.
type SessionInfo struct {
	User        users.User        `json:"user"`
	Permissions []rbac.Permission `json:"permissions" nullable:"false" doc:"Everything this user may do. Use it to show or hide UI; the server still checks every request."`
	CSRFToken   string            `json:"csrfToken" doc:"Send this back in the X-CSRF-Token header on every POST, PATCH, PUT and DELETE."`
}

// Error codes owned by this module.
const (
	CodeSetupAlreadyDone errs.Code = "SETUP_ALREADY_DONE"
)
