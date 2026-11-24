package shares

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Config backup and restore (step 3.6): the shares side. The backup module
// orchestrates; this file knows the shares tables, their rules, and how
// the Samba config is built from them.

// MaxRestoreShares is the most shares a backup may hold.
const MaxRestoreShares = 1000

// Records returns every share (with its access list) and the server-wide
// settings, for a backup.
func (s *Service) Records(ctx context.Context) ([]Share, ServerSettings, error) {
	shares, err := s.repo.All(ctx, s.repo.db.Read)
	if err != nil {
		return nil, ServerSettings{}, err
	}
	if shares == nil {
		shares = []Share{}
	}
	server, err := s.ServerSettings(ctx)
	return shares, server, err
}

// Restoring is the shares part of a backup being restored.
type Restoring struct {
	Shares []Share
	Server ServerSettings
	// Users are the restored users, by id.
	Users map[int64]users.User
	// GroupMembers and GroupNames are the restored groups, by id.
	GroupMembers map[int64][]int64
	GroupNames   map[int64]string
}

// CheckRestore validates the shares and server settings of a backup with
// the API's rules, against the restored users, and looks up each folder on
// this machine. It fixes up r in place: real paths, normalized network
// rules, Mac support on when a Time Machine share needs it. Folders that do
// not exist are returned in missing (by share index); with create they are
// only checked to be creatable. Field names: "shares[i].<field>", "samba.<field>".
func (s *Service) CheckRestore(ctx context.Context, v *validate.Errors, r *Restoring, create bool) (missing []int, err error) {
	if len(r.Shares) > MaxRestoreShares {
		v.Add("shares", "too_many", fmt.Sprintf("At most %d shares.", MaxRestoreShares))
		return nil, nil
	}
	ids, names := map[int64]bool{}, map[string]bool{}
	anyTM := false
	for i := range r.Shares {
		sh := &r.Shares[i]
		f := fmt.Sprintf("shares[%d].", i)
		one := validate.New()
		one.Check(sh.ID > 0 && !ids[sh.ID], "id", "invalid", "Each share needs its own positive id.")
		ids[sh.ID] = true
		one.Check(sys.ValidShareName(sh.Name), "name", "invalid_format", "Not a valid share name.")
		one.Check(!names[strings.ToLower(sh.Name)], "name", "duplicate", "This share name appears twice.")
		names[strings.ToLower(sh.Name)] = true
		one.Check(sys.ValidShareComment(sh.Comment), "comment", "invalid_format", "Not a valid comment.")
		if sh.Preset == "" {
			sh.Preset = PresetCustom
		}
		one.Check(validPreset(sh.Preset), "preset", "invalid_choice", "Unknown preset.")
		one.Check(sys.ValidSharePath(sh.Path) && sys.SharablePath(sh.Path), "path", "not_allowed", "This folder cannot be shared.")

		seen := map[int64]bool{}
		access := make([]AccessInput, len(sh.Access))
		for j, a := range sh.Access {
			af := fmt.Sprintf("access[%d]", j)
			access[j] = AccessInput{UserID: a.UserID, Level: a.Level}
			one.Check(a.Level.Valid(), af+".level", "invalid_choice", "Must be \"read\", \"upload\" or \"write\".")
			u, ok := r.Users[a.UserID]
			switch {
			case !ok:
				one.Add(af+".userId", "unknown_user", "This user is not in the backup.")
			case !u.SMBAccess:
				one.Add(af+".userId", "no_smb_access", "This user has no SMB access in the backup.")
			case seen[a.UserID]:
				one.Add(af+".userId", "duplicate", "This user is listed twice.")
			}
			seen[a.UserID] = true
			sh.Access[j].Username = u.Username
		}
		rules := Rules{ReadOnly: sh.ReadOnly, GuestAccess: sh.GuestAccess, TimeMachine: sh.TimeMachine}
		checkUploads(one, access, rules)
		checkTimeMachine(one, access, rules, sh.TimeMachineMaxGiB)
		checkGroupAccess(one, groupInputs(*sh), r.GroupNames)
		checkGroupLevels(one, groupInputs(*sh), rules)
		for j := range sh.GroupAccess {
			sh.GroupAccess[j].GroupName = r.GroupNames[sh.GroupAccess[j].GroupID]
		}
		sh.NetworkRules = checkNetworkRules(one, sh.NetworkRules)
		anyTM = anyTM || sh.TimeMachine

		// The folder on this machine, if the path itself is fine.
		if !one.Has("path") {
			isMissing, err := s.checkRestoreDir(ctx, one, sh, create)
			if err != nil {
				return nil, err
			}
			if isMissing {
				missing = append(missing, i)
			}
		}
		v.Merge(f, one)
	}
	// One folder, one set of permissions (checked on the real paths).
	for i := range r.Shares {
		for j := range r.Shares[:i] {
			if sys.Overlaps(r.Shares[i].Path, r.Shares[j].Path) {
				v.Add(fmt.Sprintf("shares[%d].path", i), "overlaps",
					fmt.Sprintf("This folder overlaps with share %q.", r.Shares[j].Name))
			}
		}
	}

	if anyTM {
		r.Server.MacSupport = true
	}
	server := validate.New()
	settings := r.Server.toSys()
	checkServer(server, settings)
	if err := settings.Check(); err != nil && server.Empty() {
		server.Add("", "invalid", err.Error())
	}
	v.Merge("samba.", server)
	return missing, nil
}

// checkRestoreDir looks up a restored share's folder and replaces its path
// with the real one. A missing folder is reported; with create, its parent
// must exist and the folder must be allowed.
func (s *Service) checkRestoreDir(ctx context.Context, v *validate.Errors, sh *Share, create bool) (missing bool, err error) {
	real, err := s.files.ResolveDir(ctx, sh.Path)
	switch {
	case err == nil:
		if !sys.SharablePath(real) || !s.allowed(real) {
			v.Add("path", "not_allowed", "On this machine the folder points into a protected folder.")
		}
		sh.Path = real
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		if !create {
			v.Add("path", "missing", "This folder does not exist on this machine. Create it, or restore with \"create missing folders\".")
			return true, nil
		}
		parent, perr := s.files.ResolveDir(ctx, path.Dir(sh.Path))
		if perr != nil {
			v.Add("path", "missing_parent", "Neither this folder nor its parent exists on this machine.")
			return true, nil
		}
		target := path.Join(parent, path.Base(sh.Path))
		if !sys.SharablePath(target) || !s.allowed(target) {
			v.Add("path", "not_allowed", "On this machine the folder would be in a protected folder.")
		}
		sh.Path = target
		return true, nil
	case errors.Is(err, sys.ErrNotDir):
		v.Add("path", "not_a_folder", "On this machine this path is a file, not a folder.")
	case errors.Is(err, sys.ErrInvalid):
		v.Add("path", "not_allowed", "This folder cannot be shared.")
	default:
		return false, err
	}
	return false, nil
}

// CreateRestoreDirs creates the missing folders CheckRestore found (it
// checked them first). It returns the real paths.
func (s *Service) CreateRestoreDirs(ctx context.Context, r *Restoring, missing []int) error {
	for _, i := range missing {
		real, _, err := s.resolveDir(ctx, r.Shares[i].Path, true)
		if err != nil {
			return fmt.Errorf("create folder for share %q: %w", r.Shares[i].Name, err)
		}
		r.Shares[i].Path = real
	}
	return nil
}

// Replacement is a whole restore, from the shares module's point of view.
type Replacement struct {
	Restoring
	// WriteUsers writes the restored users in the same transaction, first
	// (the shares' access lists refer to them).
	WriteUsers func(tx *sql.Tx) error
	// Prepare runs right before Samba gets the new config: it creates the
	// system accounts the config names. undo puts them back as they were;
	// it is called if anything after Prepare fails.
	Prepare func(ctx context.Context) (undo func(), err error)
}

// Replace replaces every share, the server-wide settings and (through
// WriteUsers) every user, and applies the new Samba config, as one change:
// if Samba refuses the config or anything fails, the database is unchanged
// and Prepare's work is undone. r must have passed CheckRestore.
func (s *Service) Replace(ctx context.Context, r Replacement) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.repo.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := r.WriteUsers(tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM shares"); err != nil {
		return err
	}
	for _, sh := range r.Shares {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO shares (id, name, path, comment, read_only, guest_access, preset, hide_unreadable, time_machine,
				time_machine_max_gib, network_rules, nfs, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sh.ID, sh.Name, sh.Path, sh.Comment, sh.ReadOnly, sh.GuestAccess, sh.Preset, sh.HideUnreadable, sh.TimeMachine,
			sh.TimeMachineMaxGiB, rulesJSON(sh.NetworkRules), nfsJSON(sh.NFS), sh.CreatedAt.Unix(), sh.UpdatedAt.Unix()); err != nil {
			return err
		}
		for _, a := range sh.Access {
			if _, err := tx.ExecContext(ctx, "INSERT INTO share_access (share_id, user_id, level) VALUES (?, ?, ?)",
				sh.ID, a.UserID, a.Level); err != nil {
				return err
			}
		}
		for _, g := range sh.GroupAccess {
			if _, err := tx.ExecContext(ctx, "INSERT INTO share_group_access (share_id, group_id, level) VALUES (?, ?, ?)",
				sh.ID, g.GroupID, g.Level); err != nil {
				return err
			}
		}
	}
	server := r.Server.toSys()
	if err := s.repo.saveServerSettings(ctx, tx, server, r.Server.ActivityLog, s.now()); err != nil {
		return err
	}

	// The config comes from the restored state itself (the user names are
	// not committed yet, so it cannot be read back from the database).
	specs := make([]sys.ShareSpec, 0, len(r.Shares))
	for _, sh := range expandShares(r.Shares, r.GroupMembers, r.GroupNames) {
		specs = append(specs, spec(sh, r.Users, nil)) // nobody is blocked: incidents go with the old users
	}
	cfg := sys.SambaConfig{Server: server, Shares: specs, Audit: r.Server.ActivityLog}
	if s.logins != nil {
		if cfg.Banned, err = s.logins.BannedAddresses(ctx); err != nil {
			return err
		}
		if cfg.LoginAudit, err = s.logins.LoginAudit(ctx); err != nil {
			return err
		}
	}
	if _, err := sys.RenderConfig(cfg); err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}

	undo, err := r.Prepare(ctx)
	if err != nil {
		return err
	}
	applyErr := s.samba.ApplyConfig(ctx, cfg)
	if err := applyFailure(applyErr); err != nil {
		undo()
		return err
	}
	if err := tx.Commit(); err != nil {
		s.restoreConfig(ctx)
		undo()
		return err
	}
	// NFS exports of the restored shares: this machine may have no NFS
	// server, which only raises an alert.
	s.nfsAlert(ctx, s.exportNFS(ctx, r.Shares, specs))
	return s.afterApply(ctx, applyErr)
}

// QueueFixAll queues the fix-permissions job of every share anyone may
// use (after a restore: the files inside get the restored access).
func (s *Service) QueueFixAll(ctx context.Context, actor int64) int {
	all, err := s.repo.All(ctx, s.repo.db.Read)
	if err != nil {
		return 0
	}
	n := 0
	for _, sh := range all {
		if len(sh.Access) > 0 || sh.GuestAccess {
			s.queueFix(ctx, actor, sh)
			n++
		}
	}
	return n
}
