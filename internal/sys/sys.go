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
	"net/netip"
	"regexp"
	"strings"

	"netshare-hub/internal/sys/sambalog"
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
	// ErrDisabled means the account is disabled, so it may not act.
	ErrDisabled = errors.New("account is disabled")
	// ErrNotDir means the path exists but is not a directory.
	ErrNotDir = errors.New("not a directory")
	// ErrReloadFailed means the new config is valid and saved, but the running
	// Samba could not be told to reload it.
	ErrReloadFailed = errors.New("samba reload failed")
	// ErrACLUnsupported means a share's folder is on a filesystem without
	// POSIX ACLs (for example ZFS without acltype=posixacl). Nothing changed.
	ErrACLUnsupported = errors.New("filesystem does not support ACLs")
	// ErrModuleMissing means a Samba module the config needs is not
	// installed (full_audit is in Debian's samba-vfs-modules). A share that
	// names a missing module stops working, so nothing was changed.
	ErrModuleMissing = errors.New("samba module not installed")
)

// ACLError says which share's folder could not take its permissions.
type ACLError struct {
	Share string
	Path  string
	Err   error
}

func (e *ACLError) Error() string {
	return "set permissions on share " + e.Share + " (" + e.Path + "): " + e.Err.Error()
}
func (e *ACLError) Unwrap() error { return e.Err }

// PermissionStats reports what FixPermissions did.
type PermissionStats struct {
	Updated int `json:"updated"` // files and folders whose ACL was set
	Skipped int `json:"skipped"` // symbolic links, devices, and files with several hard links
}

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
	// ApplyConfig makes Samba serve exactly these shares, with these server
	// settings. The system layer
	// checks every share against the rules, builds the config itself, lets
	// Samba's own validator check it, atomically replaces the managed config
	// file, and reloads Samba. If anything is refused, nothing changes.
	//
	// It also sets the ACL of each share's top folder whose access changed,
	// so the kernel enforces the same access as Samba (for the file manager
	// too). Files already inside keep their ACL until FixPermissions runs.
	// Only managed accounts may be named in a share.
	// Server settings in cfg.Server are written to the same file, in the
	// same step: a setting Samba refuses changes nothing either.
	// Errors: ErrInvalid, *ConfigError, ErrReloadFailed, *ACLError
	// (wrapping ErrACLUnsupported when the filesystem has no ACLs).
	ApplyConfig(ctx context.Context, cfg SambaConfig) error
	// FixPermissions re-applies a live share's access to every file and
	// folder inside it. It takes the access from the applied config, never
	// from the caller. It can run for a long time on a big share and stops
	// when ctx is done. Symbolic links are never followed.
	// Errors: ErrInvalid, ErrNotFound (no such live share), *ACLError.
	FixPermissions(ctx context.Context, share string) (PermissionStats, error)
	// DisconnectClient drops every SMB connection from one computer (the
	// ransomware guard uses it so a blocked user's open connections cannot
	// keep writing). The client may reconnect; what it may do then is
	// whatever the config says. Errors: ErrInvalid.
	DisconnectClient(ctx context.Context, ip netip.Addr) error
}

// Files answers questions about directories that are about to be shared.
type Files interface {
	// ResolveDir returns the real path of an existing directory with every
	// symbolic link resolved, so policy checks cannot be dodged with a link.
	// Errors: fs.ErrNotExist, ErrNotDir.
	ResolveDir(ctx context.Context, path string) (string, error)
	// CreateShareDir creates one new directory (its parent must exist) owned
	// by root that nobody else can enter until ApplyConfig gives the share's
	// users access through its ACL. It returns the real path.
	// Errors: fs.ErrNotExist (parent), fs.ErrExist.
	CreateShareDir(ctx context.Context, path string) (string, error)
}

// Audit reads what happens over SMB (the activity log's source).
type Audit interface {
	// Events streams the events parsed from Samba's logs until ctx is done
	// or the source fails; the channel is closed then, and the caller asks
	// again. Only events from after the call are sent.
	Events(ctx context.Context) (<-chan sambalog.Event, error)
}

// System bundles the interfaces for wiring.
type System struct {
	Accounts Accounts
	Samba    Samba
	Files    Files
	Workers  Workers
	Audit    Audit
}
