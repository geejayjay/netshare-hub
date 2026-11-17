package users

import "netshare-hub/internal/platform/errs"

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
