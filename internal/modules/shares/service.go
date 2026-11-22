package shares

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Service holds every rule about shares.
//
// The database and the Samba config must always agree. Every change follows
// one path (see change): edit the database inside a transaction, hand the
// full list of shares to the system layer (which checks them, builds the
// config and has Samba validate and load it), and only then commit. If the
// shares are refused, the transaction is rolled back and nothing has changed
// anywhere.
type Service struct {
	repo  *Repo
	users Users
	samba sys.Samba
	files sys.Files
	now   func() time.Time
	// protected are folders that must never be exposed through a share, on
	// top of the system folders: NetShare Hub's own data (the database holds
	// password hashes).
	protected []string

	// mu serializes config changes, so two requests can never interleave
	// "render" and "apply" and leave Samba with the older of two configs.
	mu sync.Mutex
}

func NewService(repo *Repo, u Users, system sys.System, protected ...string) *Service {
	return &Service{repo: repo, users: u, samba: system.Samba, files: system.Files, now: time.Now, protected: protected}
}

// allowed reports whether a real path may be shared.
func (s *Service) allowed(real string) bool {
	if !sys.ValidSharePath(real) || !sys.SharablePath(real) {
		return false
	}
	for _, dir := range s.protected {
		if sys.Overlaps(real, dir) {
			return false
		}
	}
	return true
}

func (s *Service) List(ctx context.Context, p page.Request) ([]Share, int, error) {
	shares, total, err := s.repo.List(ctx, p)
	if err != nil {
		return nil, 0, err
	}
	return shares, total, s.fillUsernames(ctx, shares)
}

func (s *Service) ListForUser(ctx context.Context, userID int64) ([]MyShare, error) {
	return s.repo.ListForUser(ctx, userID)
}

func (s *Service) Get(ctx context.Context, id int64) (Share, error) {
	sh, ok, err := s.repo.Get(ctx, s.repo.db.Read, id)
	if err != nil {
		return Share{}, err
	}
	if !ok {
		return Share{}, errNotFound()
	}
	one := []Share{sh}
	return one[0], s.fillUsernames(ctx, one)
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Share, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Path = strings.TrimSpace(in.Path)
	in.Comment = strings.TrimSpace(in.Comment)

	v := validate.New()
	v.Check(sys.ValidShareName(in.Name), "name", "invalid_format",
		"Use 1-32 letters, digits, - or _, starting with a letter or digit. Some names (like \"global\" or \"homes\") are reserved.")
	v.Check(sys.ValidSharePath(in.Path), "path", "invalid_format",
		"Enter a full folder path like /srv/media, without %, backslashes, \"..\" or a trailing slash.")
	v.Check(sys.ValidShareComment(in.Comment), "comment", "invalid_format",
		"Must be at most 100 characters, without % or backslashes.")
	if !v.Has("path") {
		v.Check(sys.SharablePath(in.Path), "path", "not_allowed", "System folders cannot be shared.")
	}
	if !v.Has("name") {
		taken, err := s.repo.NameTaken(ctx, in.Name)
		if err != nil {
			return Share{}, err
		}
		v.Check(!taken, "name", "taken", "A share with this name already exists.")
	}
	if err := s.checkAccess(ctx, v, in.Access); err != nil {
		return Share{}, err
	}
	if err := v.Err(); err != nil {
		return Share{}, err
	}

	// Everything else is valid: now look at (and maybe create) the folder.
	real, err := s.resolveDir(ctx, in.Path, in.CreateDirectory)
	if err != nil {
		return Share{}, err
	}
	in.Path = real

	var id int64
	err = s.change(ctx, func(tx *sql.Tx) (bool, error) {
		var err error
		if id, err = s.repo.Insert(ctx, tx, in, s.now()); err != nil {
			return false, err
		}
		return true, s.repo.ReplaceAccess(ctx, tx, id, in.Access, s.now())
	})
	if err != nil && !errs.Is(err, CodeSambaReloadFailed) {
		return Share{}, err
	}
	sh, getErr := s.Get(ctx, id)
	if getErr != nil {
		return Share{}, getErr
	}
	return sh, err
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (Share, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return Share{}, err
	}

	v := validate.New()
	if in.Comment != nil {
		*in.Comment = strings.TrimSpace(*in.Comment)
		v.Check(sys.ValidShareComment(*in.Comment), "comment", "invalid_format",
			"Must be at most 100 characters, without % or backslashes.")
	}
	if in.Access != nil {
		if err := s.checkAccess(ctx, v, *in.Access); err != nil {
			return Share{}, err
		}
	}
	if err := v.Err(); err != nil {
		return Share{}, err
	}

	err := s.change(ctx, func(tx *sql.Tx) (bool, error) {
		if in.Comment != nil {
			if err := s.repo.UpdateComment(ctx, tx, id, *in.Comment, s.now()); err != nil {
				return false, err
			}
		}
		if in.Access != nil {
			if err := s.repo.ReplaceAccess(ctx, tx, id, *in.Access, s.now()); err != nil {
				return false, err
			}
		}
		return in.Comment != nil || in.Access != nil, nil
	})
	if err != nil && !errs.Is(err, CodeSambaReloadFailed) {
		return Share{}, err
	}
	sh, getErr := s.Get(ctx, id)
	if getErr != nil {
		return Share{}, getErr
	}
	return sh, err
}

// Delete stops sharing a folder. The folder and its files are not touched.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	return s.change(ctx, func(tx *sql.Tx) (bool, error) {
		return true, s.repo.Delete(ctx, tx, id)
	})
}

// RevokeUser removes a user from every share and updates Samba. The users
// module calls it before it removes a user's SMB access (users.AccessRevoker).
func (s *Service) RevokeUser(ctx context.Context, userID int64) error {
	return s.change(ctx, func(tx *sql.Tx) (bool, error) {
		n, err := s.repo.DeleteUserAccess(ctx, tx, userID)
		return n > 0, err
	})
}

// change is the only way the database and the Samba config are modified.
// edit reports whether it changed anything; if not, Samba is left alone.
func (s *Service) change(ctx context.Context, edit func(tx *sql.Tx) (changed bool, err error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.repo.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful commit

	changed, err := edit(tx)
	if err != nil || !changed {
		return err
	}

	// Build the share list from inside the transaction, so it reflects the edit.
	shares, err := s.repo.All(ctx, tx)
	if err != nil {
		return err
	}
	specs, err := s.specs(ctx, shares)
	if err != nil {
		return err
	}

	applyErr := s.samba.ApplyShares(ctx, specs)
	switch {
	case applyErr == nil:
	case errors.Is(applyErr, sys.ErrReloadFailed):
		// The config is valid and on disk; only the running Samba did not
		// pick it up yet. Keep database and config in step, and say so.
	default:
		// The system layer or Samba refused the shares (or the config could
		// not be written): nothing changed on the system, and the deferred
		// rollback undoes the edit.
		return errs.Internal(fmt.Errorf("apply samba config: %w", applyErr))
	}

	if err := tx.Commit(); err != nil {
		// Extremely unlikely, but then Samba has a config the database does
		// not know about. Put the previous config back.
		s.restoreConfig(ctx)
		return err
	}
	if applyErr != nil {
		return errs.Unavailable(CodeSambaReloadFailed,
			"The change was saved, but Samba could not be reloaded. It will take effect when Samba restarts.").
			WithCause(applyErr)
	}
	return nil
}

// restoreConfig re-applies the config for what is committed in the database.
func (s *Service) restoreConfig(ctx context.Context) {
	shares, err := s.repo.All(ctx, s.repo.db.Read)
	if err == nil {
		var specs []sys.ShareSpec
		if specs, err = s.specs(ctx, shares); err == nil {
			err = s.samba.ApplyShares(ctx, specs)
		}
	}
	if err != nil {
		slog.ErrorContext(ctx, "samba config is out of step with the database", "err", err)
	}
}

// specs turns shares into what the system layer needs: the account names of
// the users that may read and write. The system layer builds the Samba config.
func (s *Service) specs(ctx context.Context, shares []Share) ([]sys.ShareSpec, error) {
	byID, err := s.usersOf(ctx, shares)
	if err != nil {
		return nil, err
	}
	out := make([]sys.ShareSpec, 0, len(shares))
	for _, sh := range shares {
		spec := sys.ShareSpec{Name: sh.Name, Path: sh.Path, Comment: sh.Comment}
		for _, a := range sh.Access {
			// Only users that still have a Samba account are passed on.
			u, ok := byID[a.UserID]
			if !ok || !u.SMBAccess {
				continue
			}
			spec.Readers = append(spec.Readers, u.Username)
			if a.Level == LevelWrite {
				spec.Writers = append(spec.Writers, u.Username)
			}
		}
		out = append(out, spec)
	}
	return out, nil
}

func (s *Service) usersOf(ctx context.Context, shares []Share) (map[int64]users.User, error) {
	seen := map[int64]bool{}
	var ids []int64
	for _, sh := range shares {
		for _, a := range sh.Access {
			if !seen[a.UserID] {
				seen[a.UserID] = true
				ids = append(ids, a.UserID)
			}
		}
	}
	return s.users.GetMany(ctx, ids)
}

func (s *Service) fillUsernames(ctx context.Context, shares []Share) error {
	byID, err := s.usersOf(ctx, shares)
	if err != nil {
		return err
	}
	for i := range shares {
		for j := range shares[i].Access {
			shares[i].Access[j].Username = byID[shares[i].Access[j].UserID].Username
		}
	}
	return nil
}

// checkAccess validates an access list: known levels, no duplicates, and
// only users that exist and have SMB access.
func (s *Service) checkAccess(ctx context.Context, v *validate.Errors, access []AccessInput) error {
	ids := make([]int64, len(access))
	for i, a := range access {
		ids[i] = a.UserID
	}
	byID, err := s.users.GetMany(ctx, ids)
	if err != nil {
		return err
	}

	seen := map[int64]bool{}
	for i, a := range access {
		field := fmt.Sprintf("access[%d]", i)
		v.Check(a.Level.Valid(), field+".level", "invalid_choice", "Must be \"read\" or \"write\".")
		u, ok := byID[a.UserID]
		switch {
		case !ok:
			v.Add(field+".userId", "not_found", "This user does not exist.")
		case !u.SMBAccess:
			v.Add(field+".userId", "no_smb_access", u.Username+" has no SMB access. Turn it on for the user first.")
		case seen[a.UserID]:
			v.Add(field+".userId", "duplicate", u.Username+" is listed more than once.")
		}
		seen[a.UserID] = true
	}
	return nil
}

// resolveDir returns the real path of the folder to share, creating it when
// asked to, and checks the policy again on the real path so a symbolic link
// cannot be used to share a system folder.
func (s *Service) resolveDir(ctx context.Context, dir string, create bool) (string, error) {
	real, err := s.files.ResolveDir(ctx, dir)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist) && create:
		// Find out where the new folder would REALLY be (its parent may be a
		// symbolic link) and check the policy before anything is created.
		parent, perr := s.files.ResolveDir(ctx, path.Dir(dir))
		if errors.Is(perr, fs.ErrNotExist) || errors.Is(perr, sys.ErrNotDir) {
			return "", pathError("not_found", "The parent folder does not exist. Create it on the server first.")
		}
		if errors.Is(perr, sys.ErrInvalid) {
			return "", pathError("not_allowed", "A folder cannot be created here. Create it on the server first.")
		}
		if perr != nil {
			return "", errs.Internal(fmt.Errorf("look up parent folder: %w", perr))
		}
		target := path.Join(parent, path.Base(dir))
		if !s.allowed(target) {
			return "", pathError("not_allowed", "This path points into a protected folder, which cannot be shared.")
		}
		real, err = s.files.CreateShareDir(ctx, target)
		if errors.Is(err, sys.ErrInvalid) {
			return "", pathError("not_allowed", "This path points into a protected folder, which cannot be shared.")
		}
		if err != nil {
			return "", errs.Internal(fmt.Errorf("create share folder: %w", err))
		}
	case errors.Is(err, fs.ErrNotExist):
		return "", pathError("not_found", "This folder does not exist. Turn on \"Create folder\" to make it.")
	case errors.Is(err, sys.ErrNotDir):
		return "", pathError("not_a_folder", "This path is a file, not a folder.")
	case errors.Is(err, sys.ErrInvalid):
		return "", pathError("not_allowed", "This path points into a protected folder, which cannot be shared.")
	default:
		return "", errs.Internal(fmt.Errorf("look up share folder: %w", err))
	}

	if !s.allowed(real) {
		return "", pathError("not_allowed", "This path points into a protected folder, which cannot be shared.")
	}
	return real, nil
}

func pathError(code, message string) error {
	return validate.New().Add("path", code, message).Err()
}
