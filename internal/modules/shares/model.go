// Package shares manages SMB shares: what is shared, who may use it, and the
// Samba configuration and folder permissions generated from that.
package shares

import (
	"context"
	"time"

	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/errs"
)

// Level is what a user may do on a share.
type Level string

const (
	LevelNone   Level = "none" // only in effective access, never stored
	LevelRead   Level = "read"
	LevelUpload Level = "upload"
	LevelWrite  Level = "write"
)

// Valid reports whether a level can be given to someone.
func (l Level) Valid() bool { return l == LevelRead || l == LevelUpload || l == LevelWrite }

// ShareAccess is one user's access to a share, as configured.
type ShareAccess struct {
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	Level    Level  `json:"level" enum:"read,upload,write" doc:"\"read\" = read only, \"upload\" = add files without seeing the folder (drop box), \"write\" = read and write."`
}

// Share is a folder shared over SMB.
type Share struct {
	ID          int64  `json:"id"`
	Name        string `json:"name" doc:"The name clients connect to: \\\\server\\name."`
	Path        string `json:"path" doc:"The folder on the server, with symbolic links resolved."`
	Comment     string `json:"comment"`
	ReadOnly    bool   `json:"readOnly" doc:"Nobody can change files, whatever their level."`
	GuestAccess bool   `json:"guestAccess" doc:"Anyone on the network can read without a password, and every user with SMB access can read with theirs."`
	Preset      string `json:"preset" doc:"The preset the share was set up with, or \"custom\". A label only: the rules are the other fields."`
	// Users never see shares they cannot open (always on); this also hides
	// the files and folders inside that they cannot read.
	HideUnreadable bool `json:"hideUnreadable" doc:"Hide files and folders a user cannot read, over SMB and in the file manager. Slows down listing very big folders."`
	TimeMachine    bool `json:"timeMachine" doc:"Macs can back up into this share with Time Machine. Its users need \"write\"; never a guest or read-only share."`
	// TimeMachineMaxGiB keeps Time Machine from filling the real disk.
	TimeMachineMaxGiB int           `json:"timeMachineMaxGiB" doc:"The size Time Machine sees as the disk, in GiB (0: the real disk's size)."`
	NetworkRules      []NetworkRule `json:"networkRules" nullable:"false" doc:"Which computers may connect. With any \"allow\" rule, only those addresses may; \"block\" rules never may. Empty: any address."`
	Access            []ShareAccess `json:"access" nullable:"false" doc:"Users given access. Nobody else can connect, unless guest access is on."`
	CreatedAt         time.Time     `json:"createdAt"`
	UpdatedAt         time.Time     `json:"updatedAt"`
}

// MyShare is a share visible to the signed-in user in their personal portal.
type MyShare struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
	Level   Level  `json:"level" enum:"read,upload,write" doc:"What the user can effectively do."`
}

// NetworkRule lets in, or keeps out, one address or range of addresses.
type NetworkRule struct {
	Action  string `json:"action" enum:"allow,block"`
	Address string `json:"address" doc:"An IP address (192.168.1.50) or a range in CIDR form (192.168.1.0/24). IPv4 or IPv6."`
}

const (
	RuleAllow = "allow"
	RuleBlock = "block"
)

// AccessInput grants a user access.
type AccessInput struct {
	UserID int64
	Level  Level
}

// Rules are the share-wide settings that shape everyone's access.
type Rules struct {
	ReadOnly    bool
	GuestAccess bool
	// TimeMachine shares take no guests, no read-only and no uploads.
	TimeMachine bool
}

// CreateInput is what the service needs to create a share.
type CreateInput struct {
	Name            string
	Path            string
	Comment         string
	CreateDirectory bool
	Rules
	Preset            string
	HideUnreadable    bool
	TimeMachineMaxGiB int
	NetworkRules      []NetworkRule
	Access            []AccessInput
}

// UpdateInput changes only the fields that are set (non-nil).
type UpdateInput struct {
	Comment           *string
	ReadOnly          *bool
	GuestAccess       *bool
	Preset            *string
	HideUnreadable    *bool
	TimeMachine       *bool
	TimeMachineMaxGiB *int
	// NetworkRules and Access, when set, replace the whole list.
	NetworkRules *[]NetworkRule
	Access       *[]AccessInput
}

// Users is what shares needs from the users module.
type Users interface {
	GetMany(ctx context.Context, ids []int64) (map[int64]users.User, error)
}

// Blocks says which users the ransomware guard stopped writing (the guard
// module implements it).
type Blocks interface {
	BlockedUsers(ctx context.Context) (map[int64]bool, error)
}

// Logins is SMB brute-force protection (the guard module): who Samba must
// refuse, and whether it must log logins.
type Logins interface {
	BannedAddresses(ctx context.Context) ([]string, error)
	LoginAudit(ctx context.Context) (bool, error)
}

// Alerts is what shares needs from the alerts module.
type Alerts interface {
	Raise(ctx context.Context, in alerts.Raise) (alerts.Alert, error)
	Resolve(ctx context.Context, kind, key string) error
}

// Jobs is what shares needs from the jobs module.
type Jobs interface {
	Submit(ctx context.Context, in jobs.Submit) (jobs.Job, error)
}

// Error codes owned by this module.
const (
	CodeShareNotFound          errs.Code = "SHARE_NOT_FOUND"
	CodeSambaReloadFailed      errs.Code = "SAMBA_RELOAD_FAILED"
	CodePermissionsUnsupported errs.Code = "SHARE_PERMISSIONS_UNSUPPORTED"
	CodePermissionsFailed      errs.Code = "SHARE_PERMISSIONS_FAILED"
	CodeSambaConfigRefused     errs.Code = "SAMBA_CONFIG_REFUSED"
	CodeSambaModuleMissing     errs.Code = "SAMBA_MODULE_MISSING"
)

// TimeMachineNeedsMacSupport explains a refused "Mac support off".
const timeMachineNeedsMacSupport = "Time Machine shares need Mac support. Turn Time Machine off on those shares first."

func errNotFound() error {
	return errs.NotFound(CodeShareNotFound, "This share does not exist.")
}
