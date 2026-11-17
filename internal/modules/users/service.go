package users

import (
	"context"
	"regexp"
	"strings"
	"time"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/password"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
)

// Service holds every rule about users. It knows nothing about HTTP or SQL.
type Service struct {
	repo *Repo
	now  func() time.Time
}

func NewService(repo *Repo) *Service {
	return &Service{repo: repo, now: time.Now}
}

// Usernames must also be valid Linux and Samba account names later on.
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
	return s.Get(ctx, id)
}

// Update applies the set fields. actorID is the user making the change.
func (s *Service) Update(ctx context.Context, actorID, id int64, in UpdateInput) (User, error) {
	u, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	wasActiveAdmin := u.Role == rbac.RoleAdmin && !u.Disabled

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
	hash := ""
	if in.Password != nil {
		checkPassword(v, *in.Password)
	}
	if err := v.Err(); err != nil {
		return User{}, err
	}

	stillActiveAdmin := u.Role == rbac.RoleAdmin && !u.Disabled
	if wasActiveAdmin && !stillActiveAdmin {
		if id == actorID {
			return User{}, errCannotModifySelf("disable or demote")
		}
		if err := s.ensureAnotherAdmin(ctx); err != nil {
			return User{}, err
		}
	}

	if in.Password != nil {
		if hash, err = password.Hash(*in.Password); err != nil {
			return User{}, err
		}
	}
	if err := s.repo.Update(ctx, u, hash, s.now()); err != nil {
		return User{}, err
	}
	return s.Get(ctx, id)
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
	return s.repo.Delete(ctx, id)
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
}

// IsInvalidCredentials lets callers react to a failed login (rate limiting).
func IsInvalidCredentials(err error) bool {
	return errs.Is(err, CodeInvalidCredentials)
}
