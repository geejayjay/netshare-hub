// Package shares manages SMB shares: what is shared, who may use it, and the
// Samba configuration generated from that.
package shares

import (
	"context"
	"time"

	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/errs"
)

// Level is what a user may do on a share.
type Level string

const (
	LevelRead  Level = "read"
	LevelWrite Level = "write"
)

func (l Level) Valid() bool { return l == LevelRead || l == LevelWrite }

// ShareAccess is one user's access to a share.
type ShareAccess struct {
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	Level    Level  `json:"level" enum:"read,write" doc:"\"read\" = read only, \"write\" = read and write."`
}

// Share is a folder shared over SMB.
type Share struct {
	ID        int64         `json:"id"`
	Name      string        `json:"name" doc:"The name clients connect to: \\\\server\\name."`
	Path      string        `json:"path" doc:"The folder on the server, with symbolic links resolved."`
	Comment   string        `json:"comment"`
	Access    []ShareAccess `json:"access" nullable:"false" doc:"Users who may use the share. Nobody else can connect to it."`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

// MyShare is a share visible to the signed-in user in their personal portal.
type MyShare struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
	Level   Level  `json:"level" enum:"read,write"`
}

// AccessInput grants a user access.
type AccessInput struct {
	UserID int64
	Level  Level
}

// CreateInput is what the service needs to create a share.
type CreateInput struct {
	Name            string
	Path            string
	Comment         string
	CreateDirectory bool
	Access          []AccessInput
}

// UpdateInput changes only the fields that are set (non-nil).
type UpdateInput struct {
	Comment *string
	// Access, when set, replaces the whole access list.
	Access *[]AccessInput
}

// Users is what shares needs from the users module.
type Users interface {
	GetMany(ctx context.Context, ids []int64) (map[int64]users.User, error)
}

// Error codes owned by this module.
const (
	CodeShareNotFound     errs.Code = "SHARE_NOT_FOUND"
	CodeSambaReloadFailed errs.Code = "SAMBA_RELOAD_FAILED"
)

func errNotFound() error {
	return errs.NotFound(CodeShareNotFound, "This share does not exist.")
}
