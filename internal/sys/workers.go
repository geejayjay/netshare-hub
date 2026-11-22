package sys

import (
	"context"
	"errors"
)

// File workers
//
// The web file manager must behave exactly like SMB: a file a user creates
// belongs to that user, and a user can only touch what the kernel lets them
// touch. The web server cannot do that itself (it runs as one unprivileged
// user), so each logged-in user gets a small "file worker" process that runs
// AS that user. The web server sends file operations to it; the kernel
// enforces the permissions.
//
// The system layer decides what a worker may reach: only the shares the
// account has access to, taken from the shares the system layer itself last
// applied (see Samba.ApplyShares). The web server names a share; it never
// supplies a path on the server.

// ErrReadOnly means the account has read-only access to the share.
var ErrReadOnly = errors.New("share is read-only for this user")

// WorkerRoot is one share a worker may reach.
type WorkerRoot struct {
	Share    string `json:"share"`
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
}

// WorkerEndpoint says how to connect to a running worker.
type WorkerEndpoint struct {
	Network string `json:"network"` // "unix" in production
	Address string `json:"address"`
}

// Workers starts and finds file workers.
type Workers interface {
	// Endpoint returns the endpoint of the file worker for an account,
	// starting one if none is running. Workers stop on their own when idle,
	// and are stopped when shares or the account change, so callers ask
	// again (and retry once) when a connection fails.
	// Errors: ErrInvalid, ErrNotFound (no such account, or it has no
	// shares), ErrNotManaged.
	Endpoint(ctx context.Context, account string) (WorkerEndpoint, error)
}

// RootsFor returns the shares an account may reach, from a list of applied
// shares. It is the single definition of "which shares does this user see".
func RootsFor(account string, shares []ShareSpec) []WorkerRoot {
	var roots []WorkerRoot
	for _, sh := range shares {
		if !contains(sh.Readers, account) {
			continue
		}
		roots = append(roots, WorkerRoot{Share: sh.Name, Path: sh.Path, Writable: contains(sh.Writers, account)})
	}
	return roots
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
