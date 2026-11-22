package users

import (
	"errors"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/sys"
)

// Error codes owned by this module. The frontend maps them to messages.
const (
	CodeUserNotFound       errs.Code = "USER_NOT_FOUND"
	CodeInvalidCredentials errs.Code = "INVALID_CREDENTIALS"
	CodeLastAdmin          errs.Code = "LAST_ADMIN"
	CodeCannotModifySelf   errs.Code = "CANNOT_MODIFY_SELF"
)

func errNotFound() error {
	return errs.NotFound(CodeUserNotFound, "This user does not exist.")
}

// errInvalidCredentials is deliberately the same for "no such user", "wrong
// password" and "disabled account", so a caller cannot probe for usernames.
func errInvalidCredentials() error {
	return errs.Unauthenticated(CodeInvalidCredentials, "Wrong username or password.")
}

func errLastAdmin() error {
	return errs.Conflict(CodeLastAdmin, "At least one active admin must remain.")
}

func errCannotModifySelf(what string) error {
	return errs.Conflict(CodeCannotModifySelf, "You cannot "+what+" your own account.")
}

// CodeSystemSyncFailed means the Linux/Samba account could not be changed.
// The user's data in NetShare Hub was put back as it was.
const CodeSystemSyncFailed errs.Code = "SYSTEM_SYNC_FAILED"

// mapSystemError turns an error from the system layer into one the UI can show.
func mapSystemError(err error) error {
	if errors.Is(err, sys.ErrNameTaken) || errors.Is(err, sys.ErrNotManaged) {
		return errs.Invalid(errs.CodeValidationFailed, "Some fields need your attention.", errs.FieldError{
			Field: "username", Code: "reserved",
			Message: "This name is used by a system account. Pick another.",
		})
	}
	return errs.Unavailable(CodeSystemSyncFailed,
		"The system account could not be updated, so nothing was changed. Check the logs for details.").WithCause(err)
}
