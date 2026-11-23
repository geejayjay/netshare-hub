package backup

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

// ExportRequest asks for a backup.
type ExportRequest struct {
	Passphrase   string `json:"passphrase" minLength:"12" maxLength:"1024" doc:"Encrypts the file. Needed to restore it: it cannot be recovered."`
	SMBPasswords *bool  `json:"smbPasswords,omitempty" doc:"Include the Samba password hashes, so users keep their SMB passwords after a restore (default true). Without them, SMB users must be given new passwords."`
}

type exportInput struct {
	Body ExportRequest
}

type exportOutput struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	CacheControl       string `header:"Cache-Control"`
	Body               []byte
}

// RestoreRequest restores a backup.
type RestoreRequest struct {
	Backup               []byte `json:"backup" maxLength:"22369624" doc:"The backup file (base64 in JSON)."`
	Passphrase           string `json:"passphrase" minLength:"12" maxLength:"1024"`
	DryRun               bool   `json:"dryRun,omitempty" doc:"Only check the backup and say what restoring it would do. Nothing is changed."`
	CreateMissingFolders bool   `json:"createMissingFolders,omitempty" doc:"Create share folders that do not exist on this machine (their parent folder must exist)."`
}

type restoreInput struct {
	Body RestoreRequest
}

type restoreOutput struct {
	Body Summary
}

// Register adds the routes.
func Register(api huma.API, svc *Service) {
	const tag = "System"
	httpx.Register(api, httpx.Route{ID: "downloadConfigBackup", Method: http.MethodPost, Path: "/system/backup",
		Summary: "Download an encrypted backup of users, shares and settings", Tag: tag,
		Access: httpx.Require(rbac.BackupManage), Binary: "application/octet-stream"},
		func(ctx context.Context, in *exportInput) (*exportOutput, error) {
			smb := in.Body.SMBPasswords == nil || *in.Body.SMBPasswords
			file, name, err := svc.Export(ctx, httpx.PrincipalFrom(ctx), in.Body.Passphrase, smb)
			if err != nil {
				return nil, err
			}
			return &exportOutput{ContentType: "application/octet-stream", CacheControl: "no-store",
				ContentDisposition: `attachment; filename="` + name + `"`, Body: file}, nil
		})
	httpx.Register(api, httpx.Route{ID: "restoreConfigBackup", Method: http.MethodPost, Path: "/system/restore",
		Summary: "Check a backup, or restore it (replaces every user, share and setting)", Tag: tag,
		Access: httpx.Require(rbac.BackupManage), MaxBody: 24 << 20},
		func(ctx context.Context, in *restoreInput) (*restoreOutput, error) {
			sum, err := svc.Restore(ctx, httpx.PrincipalFrom(ctx), RestoreInput{File: in.Body.Backup,
				Passphrase: in.Body.Passphrase, DryRun: in.Body.DryRun, CreateMissingFolders: in.Body.CreateMissingFolders})
			if err != nil {
				return nil, err
			}
			return &restoreOutput{Body: sum}, nil
		})
}
