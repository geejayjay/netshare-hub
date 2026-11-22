package users

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/password"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Service holds every rule about users. It knows nothing about HTTP or SQL.
//
// A user with SMB access exists in two places: our database and the system
// (Linux + Samba account). Every change keeps the two in step: the database
// is written first, then the system, and if the system step fails the
// database is put back. Either both change or neither does.
type Service struct {
	repo     *Repo
	accounts sys.Accounts
	revoker  AccessRevoker
	now      func() time.Time
}

func NewService(repo *Repo, accounts sys.Accounts) *Service {
	return &Service{repo: repo, accounts: accounts, now: time.Now}
}

// SetAccessRevoker wires in the module that grants SMB access (shares).
func (s *Service) SetAccessRevoker(r AccessRevoker) { s.revoker = r }

// Usernames are also Linux and Samba account names. This must stay at least
// as strict as sys.ValidAccountName.
var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

const (
	minPasswordLen = 8
	maxPasswordLen = 128
)

// dummyHash is verified when the username is unknown, so "no such user" takes
// as long as "wrong password" and cannot be told apart by timing.
var dummyHash, _ = password.Hash("netshare-hub-dummy-password")

func (s *Service) List(ctx context.Context, p page.Request) ([]User, int, error) {
	return s.repo.List(ctx, p)
}

func (s *Service) Get(ctx context.Context, id int64) (User, error) {
	u, ok, err := s.repo.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, errNotFound()
	}
	return u, nil
}

// GetMany returns the users with the given ids, keyed by id.
func (s *Service) GetMany(ctx context.Context, ids []int64) (map[int64]User, error) {
	return s.repo.GetMany(ctx, ids)
}

func (s *Service) Count(ctx context.Context) (int, error) {
	return s.repo.Count(ctx)
}

func (s *Service) Create(ctx context.Context, in CreateInput) (User, error) {
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	in.DisplayName = strings.TrimSpace(in.DisplayName)

	v := validate.New()
	v.Check(usernamePattern.MatchString(in.Username), "username", "invalid_format",
		"Use 2-32 lowercase letters, digits, - or _, starting with a letter.")
	v.Check(in.Role.Valid(), "role", "invalid_choice", "Pick one of the available roles.")
	checkPassword(v, in.Password)

	if !v.Has("username") {
		_, _, taken, err := s.repo.GetByUsername(ctx, in.Username)
		if err != nil {
			return User{}, err
		}
		v.Check(!taken, "username", "taken", "This username is already in use.")
	}
	if err := v.Err(); err != nil {
		return User{}, err
	}

	hash, err := password.Hash(in.Password)
	if err != nil {
		return User{}, err
	}
	id, err := s.repo.Create(ctx, in, hash, s.now())
	if err != nil {
		return User{}, err
	}

	if in.SMBAccess {
		if err := s.accounts.CreateUser(ctx, in.Username, in.Password); err != nil {
			// Undo the database row so no half-created user is left behind.
			if delErr := s.repo.Delete(ctx, id); delErr != nil {
				slog.ErrorContext(ctx, "could not undo user creation", "username", in.Username, "err", delErr)
			}
			return User{}, mapSystemError(err)
		}
	}
	return s.Get(ctx, id)
}

// Update applies the set fields. actorID is the user making the change.
func (s *Service) Update(ctx context.Context, actorID, id int64, in UpdateInput) (User, error) {
	old, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	u := old

	v := validate.New()
	if in.DisplayName != nil {
		u.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Role != nil {
		v.Check(in.Role.Valid(), "role", "invalid_choice", "Pick one of the available roles.")
		u.Role = *in.Role
	}
	if in.Disabled != nil {
		u.Disabled = *in.Disabled
	}
	if in.SMBAccess != nil {
		u.SMBAccess = *in.SMBAccess
	}
	if in.Password != nil {
		checkPassword(v, *in.Password)
	}
	// Turning SMB access on creates the Samba account, which needs the
	// password in plain text. We only ever store a hash, so it must be sent.
	v.Check(!(u.SMBAccess && !old.SMBAccess && in.Password == nil), "password", "required",
		"Enter a password to turn on SMB access.")
	if err := v.Err(); err != nil {
		return User{}, err
	}

	wasActiveAdmin := old.Role == rbac.RoleAdmin && !old.Disabled
	stillActiveAdmin := u.Role == rbac.RoleAdmin && !u.Disabled
	if wasActiveAdmin && !stillActiveAdmin {
		if id == actorID {
			return User{}, errCannotModifySelf("disable or demote")
		}
		if err := s.ensureAnotherAdmin(ctx); err != nil {
			return User{}, err
		}
	}

	// Removing SMB access: take the user off every share first (see AccessRevoker).
	if old.SMBAccess && !u.SMBAccess {
		if err := s.revoke(ctx, id); err != nil {
			return User{}, err
		}
	}

	// 1. Database, remembering how to put it back.
	oldHash, err := s.repo.PasswordHash(ctx, id)
	if err != nil {
		return User{}, err
	}
	newHash := ""
	if in.Password != nil {
		if newHash, err = password.Hash(*in.Password); err != nil {
			return User{}, err
		}
	}
	if err := s.repo.Update(ctx, u, newHash, s.now()); err != nil {
		return User{}, err
	}

	// 2. System. On failure, restore the database row exactly.
	if err := s.syncAccount(ctx, old, u, in.Password); err != nil {
		if undoErr := s.repo.Update(ctx, old, oldHash, old.UpdatedAt); undoErr != nil {
			slog.ErrorContext(ctx, "could not undo user update", "username", old.Username, "err", undoErr)
		}
		return User{}, mapSystemError(err)
	}
	return s.Get(ctx, id)
}

// syncAccount makes the system account match the new state of a user. Each
// branch is ordered so that a failure leaves the system as it was.
func (s *Service) syncAccount(ctx context.Context, old, u User, newPassword *string) error {
	name := u.Username
	switch {
	case !old.SMBAccess && !u.SMBAccess:
		return nil // web-only user: nothing on the system

	case old.SMBAccess && !u.SMBAccess:
		return s.accounts.DeleteUser(ctx, name)

	case !old.SMBAccess && u.SMBAccess:
		if err := s.accounts.CreateUser(ctx, name, *newPassword); err != nil {
			return err
		}
		if u.Disabled {
			if err := s.accounts.SetEnabled(ctx, name, false); err != nil {
				s.cleanup(ctx, name)
				return err
			}
		}
		return nil

	default: // had SMB access and keeps it
		// Enabled state first: it can be reverted. A password change cannot
		// (the old password is unknown), so it goes last.
		if old.Disabled != u.Disabled {
			if err := s.accounts.SetEnabled(ctx, name, !u.Disabled); err != nil {
				return err
			}
		}
		if newPassword != nil {
			if err := s.accounts.SetPassword(ctx, name, *newPassword); err != nil {
				if old.Disabled != u.Disabled {
					if undoErr := s.accounts.SetEnabled(ctx, name, !old.Disabled); undoErr != nil {
						slog.ErrorContext(ctx, "could not undo account state", "username", name, "err", undoErr)
					}
				}
				return err
			}
		}
		return nil
	}
}

func (s *Service) Delete(ctx context.Context, actorID, id int64) error {
	u, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if id == actorID {
		return errCannotModifySelf("delete")
	}
	if u.Role == rbac.RoleAdmin && !u.Disabled {
		if err := s.ensureAnotherAdmin(ctx); err != nil {
			return err
		}
	}

	if u.SMBAccess {
		// Order matters: off every share, then the system account, then the
		// row. If a step fails the user still exists and can be retried.
		if err := s.revoke(ctx, id); err != nil {
			return err
		}
		if err := s.accounts.DeleteUser(ctx, u.Username); err != nil {
			return mapSystemError(err)
		}
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		if u.SMBAccess {
			// The system account is already gone. Record that, so the row
			// does not claim an account that no longer exists.
			u.SMBAccess = false
			if fixErr := s.repo.Update(ctx, u, "", s.now()); fixErr != nil {
				slog.ErrorContext(ctx, "user row is out of step with the system", "username", u.Username, "err", fixErr)
			}
		}
		return err
	}
	return nil
}

// Authenticate checks a username and password. It is used by the auth module.
func (s *Service) Authenticate(ctx context.Context, username, plain string) (User, error) {
	u, hash, ok, err := s.repo.GetByUsername(ctx, strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return User{}, err
	}
	if !ok {
		password.Verify(dummyHash, plain)
		return User{}, errInvalidCredentials()
	}
	if !password.Verify(hash, plain) || u.Disabled {
		return User{}, errInvalidCredentials()
	}
	return u, nil
}

func (s *Service) revoke(ctx context.Context, userID int64) error {
	if s.revoker == nil {
		return nil
	}
	return s.revoker.RevokeUser(ctx, userID)
}

// cleanup removes a system account after a failed multi-step change.
func (s *Service) cleanup(ctx context.Context, name string) {
	if err := s.accounts.DeleteUser(ctx, name); err != nil {
		slog.ErrorContext(ctx, "could not remove system account after a failed change", "username", name, "err", err)
	}
}

func (s *Service) ensureAnotherAdmin(ctx context.Context) error {
	n, err := s.repo.CountActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return errLastAdmin()
	}
	return nil
}

func checkPassword(v *validate.Errors, plain string) {
	v.Check(len(plain) >= minPasswordLen, "password", "too_short", "Must be at least 8 characters.")
	v.Check(len(plain) <= maxPasswordLen, "password", "too_long", "Must be at most 128 characters.")
	// The system tools read passwords line by line.
	v.Check(!strings.ContainsAny(plain, "\n\r\x00"), "password", "invalid_format", "Cannot contain line breaks.")
}

// IsInvalidCredentials lets callers react to a failed login (rate limiting).
func IsInvalidCredentials(err error) bool {
	return errs.Is(err, CodeInvalidCredentials)
}
