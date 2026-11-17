package httpx

import (
	"context"

	"netshare-hub/internal/platform/rbac"
)

type ctxKey int

const (
	keyPrincipal ctxKey = iota
	keyClientIP
)

// Principal is the logged-in user for the current request.
type Principal struct {
	UserID      int64
	Username    string
	DisplayName string
	Role        rbac.Role
	SessionID   string
	CSRFToken   string
}

// Can reports whether the principal holds a permission.
func (p *Principal) Can(perm rbac.Permission) bool {
	return p != nil && rbac.Can(p.Role, perm)
}

// WithPrincipal stores the logged-in user. The auth middleware calls it.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFrom returns the logged-in user, or nil for anonymous requests.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(keyPrincipal).(*Principal)
	return p
}

// ClientIP returns the caller's IP address.
func ClientIP(ctx context.Context) string {
	ip, _ := ctx.Value(keyClientIP).(string)
	return ip
}
