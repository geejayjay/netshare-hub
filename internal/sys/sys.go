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
	// ErrReservedFolder means a share would expose a folder another
	// feature owns: disk images (block storage) or a replication
	// receiving folder. Nothing was changed.
	ErrReservedFolder = errors.New("folder is used by block storage or replication")
	// ErrModuleMissing means a Samba module the config needs is not
	// installed (full_audit and Apple's fruit, catia and streams_xattr are
	// in Debian's samba-vfs-modules). A share that
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

// PurgeStats reports what PurgeRecycle did.
type PurgeStats struct {
	Removed int   `json:"removed"` // files and folders removed
	Bytes   int64 `json:"bytes"`
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

var ntHash = regexp.MustCompile(`^[0-9A-F]{32}$`)

// ValidNTHash reports whether s is a Samba password hash (the NT hash: 32
// upper-case hex digits), as SMBHashes returns it.
func ValidNTHash(s string) bool { return ntHash.MatchString(s) }

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
	// SMBHashes returns the Samba password hash of each named managed
	// account (for a config backup). An account without a Samba entry is
	// left out. The hash is password-equivalent for SMB: it is never
	// logged, and only goes into an encrypted backup.
	// Errors: ErrInvalid, ErrNotManaged.
	SMBHashes(ctx context.Context, names []string) (map[string]string, error)
	// CreateUserFromHash is CreateUser with a password hash from SMBHashes
	// instead of a password (restoring a backup): the user keeps their SMB
	// password. An existing managed account gets the hash and is enabled.
	// The hash never appears on a command line.
	// Errors: ErrInvalid, ErrNameTaken.
	CreateUserFromHash(ctx context.Context, name, hash string) error
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
	// PurgeRecycle removes what has been in a live share's recycle bin for
	// longer than its retention (read from the applied config, never from
	// the caller). Links are never followed. It can take long on a big bin.
	// Errors: ErrInvalid, ErrNotFound (no such share, or no recycle bin).
	PurgeRecycle(ctx context.Context, share string) (PurgeStats, error)
	// ScanShare calls fn for every file and folder in a live share's folder
	// (read from the applied config, never from the caller), for the search
	// index and the storage report. It runs in the system layer because the
	// web server cannot read the share folders: only their users can. Links
	// are never followed; the recycle bin and unfinished uploads are skipped
	// (see WalkShareFS). It stops when fn returns an error or ctx is done.
	// Errors: ErrInvalid, ErrNotFound (no such live share), fs.ErrNotExist.
	ScanShare(ctx context.Context, share string, fn func(ShareEntry) error) error
	// StatFiles reports files and folders of a live share by path relative
	// to its folder (see StatShareFS), with their SHA-256 when hash is set
	// (duplicate detection). At most MaxStatFiles paths per call.
	// Errors: ErrInvalid, ErrNotFound (no such live share), fs.ErrNotExist.
	StatFiles(ctx context.Context, share string, paths []string, hash bool) ([]ShareFile, error)
	// DisconnectClient drops every SMB connection from one computer (the
	// ransomware guard uses it so a blocked user's open connections cannot
	// keep writing). The client may reconnect; what it may do then is
	// whatever the config says. Errors: ErrInvalid.
	DisconnectClient(ctx context.Context, ip netip.Addr) error
	// Sessions lists the SMB sessions connected now.
	Sessions(ctx context.Context) ([]Session, error)
	// DisconnectSession ends one session: the process serving it, found
	// again from Samba's own list (never a process id from the caller).
	// Errors: ErrInvalid, ErrNotFound (no such session now).
	DisconnectSession(ctx context.Context, id string) error
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
	// InspectMounts reports, for each folder, the mount it lives on (and
	// whether that mount is read-only or the container's root disk) and
	// whether its owner is unmapped in an unprivileged container. It must
	// run with the system's real view of the mounts: a sandboxed process
	// (systemd ProtectSystem=strict) sees every folder as read-only.
	// Empty paths are skipped. Errors: ErrInvalid (more than MaxMountPaths).
	InspectMounts(ctx context.Context, paths []string) (MountReport, error)
}

// Audit reads what happens over SMB (the activity log's source).
type Audit interface {
	// Events streams the events parsed from Samba's logs until ctx is done
	// or the source fails; the channel is closed then, and the caller asks
	// again. Only events from after the call are sent.
	Events(ctx context.Context) (<-chan sambalog.Event, error)
}

// Neighbors finds the hardware (MAC) address of a computer on the local
// network, from the server's own neighbour table. It only knows computers
// on the same network segment (behind a router, every client looks like the
// router), and only IPv4.
type Neighbors interface {
	MAC(ip netip.Addr) (string, bool)
}

// DiscoveryService is one way computers find the server on the network.
type DiscoveryService string

const (
	// WSD is WS-Discovery: the server shows up in Windows' Network folder.
	// The daemon is wsdd (or wsdd2).
	WSD DiscoveryService = "wsd"
	// MDNS is mDNS / DNS-SD ("Bonjour"): the server shows up in macOS
	// Finder and Linux file managers, and Macs find Time Machine shares.
	// The daemon is avahi-daemon; Samba registers its shares with it.
	MDNS DiscoveryService = "mdns"
)

// ServiceState is the state of one discovery daemon on this machine.
type ServiceState struct {
	// Installed is false when no package provides the daemon.
	Installed bool `json:"installed"`
	// Unit is the systemd unit in use ("wsdd.service").
	Unit string `json:"unit,omitempty"`
	// Running now, and Enabled to start at boot.
	Running bool `json:"running"`
	Enabled bool `json:"enabled"`
}

// DiscoveryStatus is what Discovery reports.
type DiscoveryStatus struct {
	// Systemd is false when the machine is not run by systemd: the daemons
	// cannot be controlled from NetShare Hub then.
	Systemd bool         `json:"systemd"`
	WSD     ServiceState `json:"wsd"`
	MDNS    ServiceState `json:"mdns"`
}

// Discovery controls the daemons that announce the server on the network.
// Their state lives in systemd (enabled or not), never in NetShare Hub, so
// what is reported is always what really runs.
type Discovery interface {
	DiscoveryStatus(ctx context.Context) (DiscoveryStatus, error)
	// SetDiscovery starts the service's daemon and enables it at boot (on),
	// or stops and disables it (off). Only fixed unit names are used.
	// Errors: ErrInvalid (unknown service), ErrNotFound (no daemon
	// installed, or no systemd).
	SetDiscovery(ctx context.Context, service DiscoveryService, on bool) error
}

// System bundles the interfaces for wiring.
type System struct {
	Accounts  Accounts
	Samba     Samba
	Files     Files
	Workers   Workers
	Audit     Audit
	Neighbors Neighbors
	Discovery Discovery
	// NFS is nil where NFS is not offered at all.
	NFS NFS
	// Replication is nil when the replication module is off.
	Replication Replication
	// Block is nil when the block storage module is off.
	Block Block
	// Groups are the managed Linux groups (always on).
	Groups Groups
}

// Message is an error from the system layer in words for an admin: without
// the "agent: " prefix and the generic text of the sentinel it wraps
// ("account not found: no share named x" becomes "no share named x").
func Message(err error) string {
	msg := strings.TrimPrefix(err.Error(), "agent: ")
	for _, s := range []error{ErrNotFound, ErrInvalid, ErrDisabled, ErrNotManaged, ErrReservedFolder} {
		msg = strings.TrimPrefix(msg, s.Error()+": ")
	}
	return msg
}
