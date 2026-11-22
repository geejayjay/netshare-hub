// Package sys is the boundary between NetShare Hub and the operating system.
//
// It is the ONLY package tree allowed to run OS commands or change system
// files. Everything above it (services, handlers) talks to these interfaces,
// so the same code runs against the real system (sysexec) or an in-memory
// fake (sysfake) for development and tests.
//
// Security rules for every implementation:
//   - Arguments are validated here again, even though services validate too.
//   - Commands run with a fixed argument list. No shell, ever.
//   - Secrets travel over stdin, never on the command line or into logs.
//   - Only accounts NetShare Hub created ("managed") can be changed or deleted.
package sys

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// ManagedGroup marks the Linux accounts NetShare Hub created. An account
// outside this group is never modified or deleted, so a NetShare Hub user
// named like a system account (root, www-data...) can never take it over.
const ManagedGroup = "netshare-hub"

// MinManagedUID is a second guard: system accounts live below it.
const MinManagedUID = 1000

var (
	// ErrInvalid means an argument failed validation at the system boundary.
	ErrInvalid = errors.New("invalid argument")
	// ErrNameTaken means a Linux account with that name exists and is not ours.
	ErrNameTaken = errors.New("account name is used by a system account")
	// ErrNotFound means the account does not exist.
	ErrNotFound = errors.New("account not found")
	// ErrNotManaged means the account exists but NetShare Hub did not create it.
	ErrNotManaged = errors.New("account is not managed by NetShare Hub")
	// ErrNotDir means the path exists but is not a directory.
	ErrNotDir = errors.New("not a directory")
	// ErrReloadFailed means the new config is valid and saved, but the running
	// Samba could not be told to reload it.
	ErrReloadFailed = errors.New("samba reload failed")
)

// ConfigError means Samba's own checker rejected a config. Nothing was changed.
type ConfigError struct {
	// Output is what the checker printed. It is for the logs and for admins.
	Output string
}

func (e *ConfigError) Error() string { return "samba config rejected: " + e.Output }

var accountName = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// ValidAccountName reports whether name is safe to use as a Linux and Samba
// account name. It starts with a letter, so it can never be read as an option.
func ValidAccountName(name string) bool {
	return accountName.MatchString(name)
}

// ValidPassword reports whether a password can be passed to the system tools.
// They read it line by line from stdin, so line breaks and NUL are refused.
func ValidPassword(password string) bool {
	return password != "" && len(password) <= 512 && !strings.ContainsAny(password, "\n\r\x00")
}

// Accounts manages the Linux + Samba account behind a NetShare Hub user.
type Accounts interface {
	// CreateUser creates the Linux account (no login shell, no home) and its
	// Samba account with the given password. It is all-or-nothing. A leftover
	// managed account with the same name is adopted and reset.
	// Errors: ErrInvalid, ErrNameTaken.
	CreateUser(ctx context.Context, name, password string) error
	// SetPassword changes the Samba password. Errors: ErrNotFound, ErrNotManaged.
	SetPassword(ctx context.Context, name, password string) error
	// SetEnabled allows or blocks SMB logins. Errors: ErrNotFound, ErrNotManaged.
	SetEnabled(ctx context.Context, name string, enabled bool) error
	// DeleteUser removes the Samba and Linux accounts. A missing account is
	// not an error. Errors: ErrNotManaged.
	DeleteUser(ctx context.Context, name string) error
}

// Samba manages the Samba configuration NetShare Hub owns.
type Samba interface {
	// ApplyShares makes Samba serve exactly these shares. The system layer
	// checks every share against the rules, builds the config itself, lets
	// Samba's own validator check it, atomically replaces the managed config
	// file, and reloads Samba. If anything is refused, nothing changes.
	// Errors: ErrInvalid, *ConfigError, ErrReloadFailed.
	ApplyShares(ctx context.Context, shares []ShareSpec) error
}

// Files answers questions about directories that are about to be shared.
type Files interface {
	// ResolveDir returns the real path of an existing directory with every
	// symbolic link resolved, so policy checks cannot be dodged with a link.
	// Errors: fs.ErrNotExist, ErrNotDir.
	ResolveDir(ctx context.Context, path string) (string, error)
	// CreateShareDir creates one new directory (its parent must exist) that
	// members of ManagedGroup can read and write. It returns the real path.
	// Errors: fs.ErrNotExist (parent), fs.ErrExist.
	CreateShareDir(ctx context.Context, path string) (string, error)
}

// System bundles the interfaces for wiring.
type System struct {
	Accounts Accounts
	Samba    Samba
	Files    Files
	Workers  Workers
}
