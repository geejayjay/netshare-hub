//go:build !linux

package sysexec

import (
	"context"
	"errors"

	"netshare-hub/internal/sys"
)

// POSIX ACLs exist only on Linux; the real system layer only runs there.

type aclBackup struct{ path string }

func (*aclBackup) restore([]string) error { return errors.ErrUnsupported }

func setShareACL(sys.ShareSpec, identities, []string) (*aclBackup, error) {
	return nil, errors.ErrUnsupported
}

func fixShareTree(context.Context, sys.ShareSpec, identities, []string) (sys.PermissionStats, error) {
	return sys.PermissionStats{}, errors.ErrUnsupported
}
