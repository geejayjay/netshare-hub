package app

import (
	"context"

	"netshare-hub/internal/modules/users"
)

// revokers runs several users.AccessRevoker in order (groups, then shares).
type revokers []users.AccessRevoker

func (r revokers) RevokeUser(ctx context.Context, userID int64) error {
	for _, x := range r {
		if err := x.RevokeUser(ctx, userID); err != nil {
			return err
		}
	}
	return nil
}
