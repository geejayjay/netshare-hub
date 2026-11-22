// Package files is the web file manager's API: browse, create folders,
// rename, move, copy, delete and download, on the shares the signed-in user
// can reach.
//
// It holds no permission logic of its own. Every operation runs in the
// user's file worker (see internal/fileworker), a process running AS the
// user and confined to their shares, so the kernel applies exactly the
// permissions the user has over SMB. This package only adds what the
// kernel cannot: the share's network rules, clear errors, and long work
// (copies, moves between shares, deleting folders) as background jobs.
package files

import (
	"context"
	"errors"
	"io/fs"
	"net/netip"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Entry is one file or folder.
type Entry = fileworker.Entry

// Item names a file or folder in a share.
type Item struct {
	Share string `json:"share" minLength:"1" maxLength:"32" example:"media"`
	Path  string `json:"path" maxLength:"4096" example:"Films/holiday.mp4" doc:"Inside the share, with / between folders. \"\" is the share's top folder."`
}

// Conflict says what to do when the destination already has a file with
// the same name.
type Conflict string

const (
	ConflictRename  Conflict = "rename"  // keep both: "photo (2).jpg"
	ConflictReplace Conflict = "replace" // overwrite the file there
	ConflictSkip    Conflict = "skip"    // leave the file there, skip this one
)

func (c Conflict) valid() bool {
	return c == ConflictRename || c == ConflictReplace || c == ConflictSkip
}

// Result is the answer to an operation that may continue in the background.
type Result struct {
	Done int       `json:"done" doc:"Items handled right away."`
	Job  *jobs.Job `json:"job,omitempty" doc:"Set when the rest runs as a background job; follow it in the jobs list."`
}

// Actor is who asks, and from which address (for the share's network rules).
type Actor struct {
	UserID int64
	IP     netip.Addr
}

// Users is what files needs from the users module.
type Users interface {
	GetMany(ctx context.Context, ids []int64) (map[int64]users.User, error)
}

// Shares is what files needs from the shares module.
type Shares interface {
	NetworkAllowed(ctx context.Context, share string, ip netip.Addr) (bool, error)
}

// Jobs is what files needs from the jobs module.
type Jobs interface {
	Submit(ctx context.Context, in jobs.Submit) (jobs.Job, error)
}

// Job kinds owned by this module.
const (
	JobCopy   = "files.copy"   // copy, or move between shares
	JobDelete = "files.delete" // delete folders (and anything with them)
)

// Error codes owned by this module.
const (
	CodeNoSMBAccess      errs.Code = "FILES_NO_SMB_ACCESS"
	CodeAccountDisabled  errs.Code = "FILES_ACCOUNT_DISABLED"
	CodeNetworkBlocked   errs.Code = "FILES_NETWORK_BLOCKED"
	CodeNotFound         errs.Code = "FILE_NOT_FOUND"
	CodeExists           errs.Code = "FILE_EXISTS"
	CodePermissionDenied errs.Code = "FILE_PERMISSION_DENIED"
	CodeReadOnly         errs.Code = "SHARE_READ_ONLY"
	CodeFolderNotEmpty   errs.Code = "FOLDER_NOT_EMPTY"
	CodeIsFolder         errs.Code = "FILE_IS_FOLDER"
	CodeNotFolder        errs.Code = "FILE_NOT_FOLDER"
	CodeRangeInvalid     errs.Code = "FILE_RANGE_INVALID"
	CodeZipExpired       errs.Code = "ZIP_LINK_EXPIRED"
	CodePartlyDone       errs.Code = "FILES_PARTLY_DONE"
	CodeNoSpace          errs.Code = "FILES_NO_SPACE"
	CodeUploadNotFound   errs.Code = "UPLOAD_NOT_FOUND"
	CodeUploadBusy       errs.Code = "UPLOAD_BUSY"
	CodeUploadOffset     errs.Code = "UPLOAD_OFFSET_MISMATCH"
)

// fail turns a file worker or system error into an answer for the user.
// field names the input the error is about ("path", "items[2].path"...).
func fail(err error, field string) error {
	var e *errs.E
	switch {
	case err == nil:
		return nil
	case errors.As(err, &e):
		return err
	case errors.Is(err, fileworker.ErrInvalidPath):
		return validate.New().Add(field, "invalid_format",
			"Use a path inside the share like Photos/2025, without \"..\" or a leading /.").Err()
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, sys.ErrNotFound):
		// sys.ErrNotFound: the user has no share by that name. Same answer
		// as a missing file: nothing about other shares is revealed.
		return errs.NotFound(CodeNotFound, "This file or folder does not exist.")
	case errors.Is(err, fs.ErrExist):
		return errs.Conflict(CodeExists, "Something with this name already exists here.")
	case errors.Is(err, sys.ErrReadOnly):
		return errs.Forbidden(CodeReadOnly, "You can only read this share.")
	case errors.Is(err, fs.ErrPermission):
		return errs.Forbidden(CodePermissionDenied, "You do not have permission to do this here.")
	case errors.Is(err, fileworker.ErrNotEmpty):
		return errs.Conflict(CodeFolderNotEmpty, "This folder is not empty.")
	case errors.Is(err, fileworker.ErrIsDir):
		return errs.Conflict(CodeIsFolder, "This is a folder.")
	case errors.Is(err, fileworker.ErrNotDir):
		return errs.Conflict(CodeNotFolder, "This is not a folder.")
	case errors.Is(err, fileworker.ErrInvalidRange):
		return errs.RangeNotSatisfiable(CodeRangeInvalid, "The requested part of the file is outside the file.")
	case errors.Is(err, fileworker.ErrNoSpace):
		return errs.Unavailable(CodeNoSpace, "The disk of this share is full.")
	case errors.Is(err, sys.ErrDisabled):
		return errs.Forbidden(CodeAccountDisabled, "Your account is disabled.")
	case errors.Is(err, sys.ErrNotManaged):
		return errs.Forbidden(CodeNoSMBAccess, "Your account has no SMB access, so it cannot open shares.")
	default:
		return errs.Internal(err)
	}
}
