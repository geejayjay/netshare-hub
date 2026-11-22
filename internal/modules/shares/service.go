package shares

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// JobFixPermissions re-applies a share's access to every file inside it.
const JobFixPermissions = "shares.fix-permissions"

// Service holds every rule about shares.
//
// The database, the Samba config and the folder permissions must always
// agree. Every change follows one path (see change): edit the database
// inside a transaction, hand the full list of shares to the system layer
// (which checks them, sets the folders' permissions, builds the config and
// has Samba validate and load it), and only then commit. If the shares are
// refused, the transaction is rolled back and nothing has changed anywhere.
//
// Files already inside a folder get the new permissions from a background
// job (JobFixPermissions), queued after each change of who has access.
type Service struct {
	repo  *Repo
	users Users
	samba sys.Samba
	files sys.Files
	jobs  Jobs
	now   func() time.Time
	// protected are folders that must never be exposed through a share, on
	// top of the system folders: NetShare Hub's own data (the database holds
	// password hashes).
	protected []string

	// mu serializes config changes, so two requests can never interleave
	// "render" and "apply" and leave Samba with the older of two configs.
	mu sync.Mutex
}

func NewService(repo *Repo, u Users, system sys.System, j Jobs, protected ...string) *Service {
	return &Service{repo: repo, users: u, samba: system.Samba, files: system.Files, jobs: j, now: time.Now, protected: protected}
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

// ListForUser returns what the user can effectively reach: shares they are
// on, and guest shares (which every user with SMB access can read).
func (s *Service) ListForUser(ctx context.Context, userID int64) ([]MyShare, error) {
	byID, err := s.users.GetMany(ctx, []int64{userID})
	if err != nil {
		return nil, err
	}
	out := []MyShare{}
	u, ok := byID[userID]
	if !ok {
		return out, nil
	}
	shares, levels, err := s.repo.ListForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i, sh := range shares {
		given := levels[i]
		if given == "" {
			given = LevelRead // a guest share the user is not listed on
		}
		e := effective(AccessInput{UserID: userID, Level: given}, u, Rules{ReadOnly: sh.ReadOnly, GuestAccess: sh.GuestAccess})
		if e.Level == LevelNone {
			continue
		}
		out = append(out, MyShare{ID: sh.ID, Name: sh.Name, Comment: sh.Comment, Level: e.Level})
	}
	return out, nil
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

// NetworkAllowed reports whether a client address may use a share under the
// share's network rules (1.8). The file manager checks it on every request,
// so the rules mean the same there as over SMB. An unknown share is allowed
// here: the file worker answers "not found" for it.
func (s *Service) NetworkAllowed(ctx context.Context, share string, ip netip.Addr) (bool, error) {
	sh, ok, err := s.repo.GetByName(ctx, share)
	if err != nil || !ok {
		return true, err
	}
	allow, block := splitRules(sh.NetworkRules)
	return sys.HostAllowed(allow, block, ip), nil
}

// Preview returns the effective access a share would have with these
// settings, without changing anything.
func (s *Service) Preview(ctx context.Context, access []AccessInput, r Rules, network []NetworkRule) (EffectiveAccess, error) {
	v := validate.New()
	byID, err := s.checkAccess(ctx, v, access)
	if err != nil {
		return EffectiveAccess{}, err
	}
	checkUploads(v, access, r)
	network = checkNetworkRules(v, network)
	if err := v.Err(); err != nil {
		return EffectiveAccess{}, err
	}
	out := preview(access, byID, r)
	out.Network = networkNote(network)
	return out, nil
}

// Create shares a folder. actor is the user asking; they own the job that
// gives the folder's existing files the share's permissions.
func (s *Service) Create(ctx context.Context, actor int64, in CreateInput) (Share, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Path = strings.TrimSpace(in.Path)
	in.Comment = strings.TrimSpace(in.Comment)
	if in.Preset == "" {
		in.Preset = PresetCustom
	}

	v := validate.New()
	v.Check(sys.ValidShareName(in.Name), "name", "invalid_format",
		"Use 1-32 letters, digits, - or _, starting with a letter or digit. Some names (like \"global\" or \"homes\") are reserved.")
	v.Check(sys.ValidSharePath(in.Path), "path", "invalid_format",
		"Enter a full folder path like /srv/media, without %, backslashes, \"..\" or a trailing slash.")
	v.Check(sys.ValidShareComment(in.Comment), "comment", "invalid_format",
		"Must be at most 100 characters, without % or backslashes.")
	v.Check(validPreset(in.Preset), "preset", "invalid_choice", "Unknown preset.")
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
	if _, err := s.checkAccess(ctx, v, in.Access); err != nil {
		return Share{}, err
	}
	checkUploads(v, in.Access, in.Rules)
	in.NetworkRules = checkNetworkRules(v, in.NetworkRules)
	if err := v.Err(); err != nil {
		return Share{}, err
	}

	// Everything else is valid: now look at (and maybe create) the folder.
	// Overlaps are checked on the path as typed (before a folder is made)
	// and again on the real one.
	if err := s.checkOverlap(ctx, in.Name, in.Path); err != nil {
		return Share{}, err
	}
	real, created, err := s.resolveDir(ctx, in.Path, in.CreateDirectory)
	if err != nil {
		return Share{}, err
	}
	if err := s.checkOverlap(ctx, in.Name, real); err != nil {
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
	// A folder that already held files needs them updated too.
	if !created && len(sh.Access)+boolInt(sh.GuestAccess) > 0 {
		s.queueFix(ctx, actor, sh)
	}
	return sh, err
}

func (s *Service) Update(ctx context.Context, actor int64, id int64, in UpdateInput) (Share, error) {
	before, err := s.Get(ctx, id)
	if err != nil {
		return Share{}, err
	}

	v := validate.New()
	if in.Comment != nil {
		*in.Comment = strings.TrimSpace(*in.Comment)
		v.Check(sys.ValidShareComment(*in.Comment), "comment", "invalid_format",
			"Must be at most 100 characters, without % or backslashes.")
	}
	if in.Preset != nil {
		v.Check(validPreset(*in.Preset), "preset", "invalid_choice", "Unknown preset.")
	}
	rules := Rules{ReadOnly: before.ReadOnly, GuestAccess: before.GuestAccess}
	preset, hide := before.Preset, before.HideUnreadable
	if in.ReadOnly != nil {
		rules.ReadOnly = *in.ReadOnly
	}
	if in.GuestAccess != nil {
		rules.GuestAccess = *in.GuestAccess
	}
	if in.Preset != nil {
		preset = *in.Preset
	}
	if in.HideUnreadable != nil {
		hide = *in.HideUnreadable
	}
	settingsChanged := in.ReadOnly != nil || in.GuestAccess != nil || in.Preset != nil || in.HideUnreadable != nil

	access := accessOf(before)
	if in.Access != nil {
		if _, err := s.checkAccess(ctx, v, *in.Access); err != nil {
			return Share{}, err
		}
		access = *in.Access
	}
	checkUploads(v, access, rules)
	if in.NetworkRules != nil {
		*in.NetworkRules = checkNetworkRules(v, *in.NetworkRules)
	}
	if err := v.Err(); err != nil {
		return Share{}, err
	}

	err = s.change(ctx, func(tx *sql.Tx) (bool, error) {
		if in.Comment != nil {
			if err := s.repo.UpdateComment(ctx, tx, id, *in.Comment, s.now()); err != nil {
				return false, err
			}
		}
		if settingsChanged {
			if err := s.repo.UpdateSettings(ctx, tx, id, rules, preset, hide, s.now()); err != nil {
				return false, err
			}
		}
		if in.NetworkRules != nil {
			if err := s.repo.UpdateNetworkRules(ctx, tx, id, *in.NetworkRules, s.now()); err != nil {
				return false, err
			}
		}
		if in.Access != nil {
			if err := s.repo.ReplaceAccess(ctx, tx, id, *in.Access, s.now()); err != nil {
				return false, err
			}
		}
		return in.Comment != nil || settingsChanged || in.NetworkRules != nil || in.Access != nil, nil
	})
	if err != nil && !errs.Is(err, CodeSambaReloadFailed) {
		return Share{}, err
	}
	after, getErr := s.Get(ctx, id)
	if getErr != nil {
		return Share{}, getErr
	}
	if changed, specErr := s.accessChanged(ctx, before, after); specErr != nil {
		slog.ErrorContext(ctx, "could not compare share access", "share", after.Name, "err", specErr)
	} else if changed {
		s.queueFix(ctx, actor, after)
	}
	return after, err
}

// Delete stops sharing a folder. The folder and its files are not touched,
// except that the folder itself loses the permissions the share gave it.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	return s.change(ctx, func(tx *sql.Tx) (bool, error) {
		return true, s.repo.Delete(ctx, tx, id)
	})
}

// FixPermissions queues a job that gives every file and folder inside the
// share the share's permissions (the "Fix permissions" button).
func (s *Service) FixPermissions(ctx context.Context, actor int64, id int64) (jobs.Job, error) {
	sh, err := s.Get(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	return s.submitFix(ctx, actor, sh)
}

// RevokeUser removes a user from every share and updates Samba. The users
// module calls it before it removes a user's SMB access (users.AccessRevoker).
//
// The share folders lose the user's entry at once (so the user cannot get
// in); entries on files inside are left until the share's permissions are
// next fixed. They cannot be reached without going through the folder.
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
	var aclErr *sys.ACLError
	switch {
	case applyErr == nil:
	case errors.Is(applyErr, sys.ErrReloadFailed):
		// The config is valid and on disk; only the running Samba did not
		// pick it up yet. Keep database and config in step, and say so.
	case errors.As(applyErr, &aclErr) && errors.Is(applyErr, sys.ErrACLUnsupported):
		return errs.Invalid(CodePermissionsUnsupported, fmt.Sprintf(
			"The folder of share %q (%s) is on a filesystem without access lists (ACLs), so its permissions cannot be set. "+
				"On ZFS, turn them on with: zfs set acltype=posixacl <dataset>. Nothing was changed.", aclErr.Share, aclErr.Path)).
			WithCause(applyErr)
	case errors.As(applyErr, &aclErr):
		return errs.Unavailable(CodePermissionsFailed, fmt.Sprintf(
			"The permissions of share %q (%s) could not be set. Nothing was changed.", aclErr.Share, aclErr.Path)).
			WithCause(applyErr)
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

// specs turns shares into what the system layer applies (see spec).
func (s *Service) specs(ctx context.Context, shares []Share) ([]sys.ShareSpec, error) {
	byID, err := s.usersOf(ctx, shares)
	if err != nil {
		return nil, err
	}
	out := make([]sys.ShareSpec, 0, len(shares))
	for _, sh := range shares {
		out = append(out, spec(sh, byID))
	}
	return out, nil
}

// accessChanged reports whether two versions of a share give anyone
// different access on the filesystem.
func (s *Service) accessChanged(ctx context.Context, before, after Share) (bool, error) {
	byID, err := s.usersOf(ctx, []Share{before, after})
	if err != nil {
		return false, err
	}
	a, b := spec(before, byID), spec(after, byID)
	return a.Guest != b.Guest || !slices.Equal(a.Readers, b.Readers) ||
		!slices.Equal(a.Writers, b.Writers) || !slices.Equal(a.Uploaders, b.Uploaders), nil
}

// queueFix submits the fix-permissions job after a change. The change
// itself already succeeded, so a failure here is logged, not returned: the
// admin can still press "Fix permissions".
func (s *Service) queueFix(ctx context.Context, actor int64, sh Share) {
	if _, err := s.submitFix(ctx, actor, sh); err != nil {
		slog.ErrorContext(ctx, "could not queue the fix-permissions job", "share", sh.Name, "err", err)
	}
}

type fixPayload struct {
	ShareID int64 `json:"shareId"`
}

func (s *Service) submitFix(ctx context.Context, actor int64, sh Share) (jobs.Job, error) {
	if s.jobs == nil {
		return jobs.Job{}, errs.Internal(errors.New("shares: no job queue"))
	}
	return s.jobs.Submit(ctx, jobs.Submit{
		Kind:    JobFixPermissions,
		OwnerID: actor,
		Title:   "Update permissions in " + sh.Name,
		Payload: fixPayload{ShareID: sh.ID},
	})
}

// RunFixPermissions is the JobFixPermissions handler. It is safe to repeat:
// it sets each file to what the share's access says, whatever it was.
//
// Unlike file jobs it does not act as its owner: it applies the share's own
// access (read from the applied config by the system layer), which the
// owner was allowed to set when the job was queued.
func (s *Service) RunFixPermissions(ctx context.Context, run *jobs.Run) error {
	var p fixPayload
	if err := run.Payload(&p); err != nil {
		return err
	}
	sh, ok, err := s.repo.Get(ctx, s.repo.db.Read, p.ShareID)
	if err != nil {
		return err
	}
	if !ok {
		run.Progress(0, 0, "The share no longer exists; nothing to do.")
		return nil
	}
	run.Progress(0, 0, "Updating permissions in "+sh.Name)
	stats, err := s.samba.FixPermissions(ctx, sh.Name)
	var aclErr *sys.ACLError
	switch {
	case errors.Is(err, sys.ErrNotFound):
		run.Progress(0, 0, "Nobody has access to this share; nothing to do.")
		return nil
	case errors.Is(err, sys.ErrACLUnsupported):
		return errs.Invalid(CodePermissionsUnsupported,
			"This folder is on a filesystem without access lists (ACLs). On ZFS, turn them on with: zfs set acltype=posixacl <dataset>.").
			WithCause(err)
	case errors.As(err, &aclErr):
		return errs.Unavailable(CodePermissionsFailed, "Some permissions could not be set. Check the logs for details.").WithCause(err)
	case err != nil:
		return err
	}
	msg := fmt.Sprintf("Updated %d items.", stats.Updated)
	if stats.Skipped > 0 {
		msg = fmt.Sprintf("Updated %d items; skipped %d links and special files.", stats.Updated, stats.Skipped)
	}
	run.Progress(int64(stats.Updated), int64(stats.Updated), msg)
	return nil
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
// only users that exist and have SMB access. It returns the users found.
func (s *Service) checkAccess(ctx context.Context, v *validate.Errors, access []AccessInput) (map[int64]users.User, error) {
	ids := make([]int64, len(access))
	for i, a := range access {
		ids[i] = a.UserID
	}
	byID, err := s.users.GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}

	seen := map[int64]bool{}
	for i, a := range access {
		field := fmt.Sprintf("access[%d]", i)
		v.Check(a.Level.Valid(), field+".level", "invalid_choice", "Must be \"read\", \"upload\" or \"write\".")
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
	return byID, nil
}

// checkUploads refuses upload-only users on a guest share: everyone can read
// a guest share, the uploads included, so "upload only" would hide nothing.
func checkUploads(v *validate.Errors, access []AccessInput, r Rules) {
	if !r.GuestAccess {
		return
	}
	for i, a := range access {
		if a.Level == LevelUpload {
			v.Add(fmt.Sprintf("access[%d].level", i), "not_with_guest",
				"Upload-only does not work on a share with guest access: everyone can read what is uploaded. Turn guest access off, or choose another level.")
		}
	}
}

// checkNetworkRules validates network rules and returns them normalized
// (canonical addresses), so what is stored is exactly what Samba gets.
func checkNetworkRules(v *validate.Errors, rules []NetworkRule) []NetworkRule {
	if len(rules) > 2*sys.MaxHostRules {
		v.Add("networkRules", "too_many", fmt.Sprintf("At most %d rules of each kind.", sys.MaxHostRules))
		return rules
	}
	out := make([]NetworkRule, len(rules))
	seen := map[string]string{}
	count := map[string]int{}
	for i, r := range rules {
		field := fmt.Sprintf("networkRules[%d]", i)
		out[i] = r
		if r.Action != RuleAllow && r.Action != RuleBlock {
			v.Add(field+".action", "invalid_choice", "Must be \"allow\" or \"block\".")
			continue
		}
		address, err := sys.ParseHostRule(r.Address)
		if err != nil {
			v.Add(field+".address", "invalid_format", err.Error())
			continue
		}
		out[i].Address = address
		if prev, ok := seen[address]; ok {
			if prev == r.Action {
				v.Add(field+".address", "duplicate", address+" is listed more than once.")
			} else {
				v.Add(field+".address", "conflict", address+" is both allowed and blocked.")
			}
			continue
		}
		seen[address] = r.Action
		if count[r.Action]++; count[r.Action] == sys.MaxHostRules+1 {
			v.Add("networkRules", "too_many", fmt.Sprintf("At most %d rules of each kind.", sys.MaxHostRules))
		}
	}
	return out
}

func accessOf(sh Share) []AccessInput {
	out := make([]AccessInput, len(sh.Access))
	for i, a := range sh.Access {
		out[i] = AccessInput{UserID: a.UserID, Level: a.Level}
	}
	return out
}

// checkOverlap refuses a folder inside (or containing) another share's: one
// folder can only carry one share's permissions.
func (s *Service) checkOverlap(ctx context.Context, name, real string) error {
	paths, err := s.repo.Paths(ctx)
	if err != nil {
		return err
	}
	for other, p := range paths {
		if other != name && sys.Overlaps(real, p) {
			return pathError("overlaps", fmt.Sprintf("This folder is inside, or contains, the folder of share %q. Shares cannot overlap.", other))
		}
	}
	return nil
}

// resolveDir returns the real path of the folder to share, creating it when
// asked to, and checks the policy again on the real path so a symbolic link
// cannot be used to share a system folder. created reports a new folder.
func (s *Service) resolveDir(ctx context.Context, dir string, create bool) (real string, created bool, err error) {
	real, err = s.files.ResolveDir(ctx, dir)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist) && create:
		// Find out where the new folder would REALLY be (its parent may be a
		// symbolic link) and check the policy before anything is created.
		parent, perr := s.files.ResolveDir(ctx, path.Dir(dir))
		if errors.Is(perr, fs.ErrNotExist) || errors.Is(perr, sys.ErrNotDir) {
			return "", false, pathError("not_found", "The parent folder does not exist. Create it on the server first.")
		}
		if errors.Is(perr, sys.ErrInvalid) {
			return "", false, pathError("not_allowed", "A folder cannot be created here. Create it on the server first.")
		}
		if perr != nil {
			return "", false, errs.Internal(fmt.Errorf("look up parent folder: %w", perr))
		}
		target := path.Join(parent, path.Base(dir))
		if !s.allowed(target) {
			return "", false, pathError("not_allowed", "This path points into a protected folder, which cannot be shared.")
		}
		real, err = s.files.CreateShareDir(ctx, target)
		if errors.Is(err, sys.ErrInvalid) {
			return "", false, pathError("not_allowed", "This path points into a protected folder, which cannot be shared.")
		}
		if err != nil {
			return "", false, errs.Internal(fmt.Errorf("create share folder: %w", err))
		}
		created = true
	case errors.Is(err, fs.ErrNotExist):
		return "", false, pathError("not_found", "This folder does not exist. Turn on \"Create folder\" to make it.")
	case errors.Is(err, sys.ErrNotDir):
		return "", false, pathError("not_a_folder", "This path is a file, not a folder.")
	case errors.Is(err, sys.ErrInvalid):
		return "", false, pathError("not_allowed", "This path points into a protected folder, which cannot be shared.")
	default:
		return "", false, errs.Internal(fmt.Errorf("look up share folder: %w", err))
	}

	if !s.allowed(real) {
		return "", false, pathError("not_allowed", "This path points into a protected folder, which cannot be shared.")
	}
	return real, created, nil
}

func pathError(code, message string) error {
	return validate.New().Add("path", code, message).Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
