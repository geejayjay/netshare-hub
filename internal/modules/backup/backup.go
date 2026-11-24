// Package backup downloads and restores NetShare Hub's configuration: every
// user (with password hashes, and optionally Samba password hashes), every
// share with its access list, and the server-wide settings. History (the
// activity log, alerts, incidents, bans, jobs) is not part of it.
//
// A restore replaces everything, so it is checked in full first (the same
// rules as the API, against this machine's folders) and refused with
// nothing changed if anything is wrong. Before it changes anything it keeps
// a copy of the database; if anything fails on the way, every change is
// undone.
package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/modules/groups"
	"netshare-hub/internal/modules/guard"
	"netshare-hub/internal/modules/shares"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Error codes owned by this module.
const (
	CodeBackupUnreadable errs.Code = "BACKUP_UNREADABLE"
	CodeBackupTooNew     errs.Code = "BACKUP_TOO_NEW"
	CodeBackupInvalid    errs.Code = "BACKUP_INVALID"
	CodeRestoreBusy      errs.Code = "RESTORE_BUSY"
)

// payloadFormat names the payload inside the encrypted file.
const payloadFormat = "netshare-hub-config"

// Payload is what a backup holds.
type Payload struct {
	Format    string    `json:"format"`
	CreatedAt time.Time `json:"createdAt"`
	App       string    `json:"app"`
	Hostname  string    `json:"hostname"`

	Users []users.Record `json:"users"`
	// SMBHashes are the Samba password hashes by username (empty when the
	// backup was made without SMB passwords).
	SMBHashes map[string]string     `json:"smbHashes,omitempty"`
	Shares    []shares.Share        `json:"shares"`
	Groups    []groups.Record       `json:"groups,omitempty"`
	Samba     shares.ServerSettings `json:"samba"`
	Guard     guard.Settings        `json:"guard"`
	Bans      guard.BanSettings     `json:"bans"`
	// Discovery is whether the discovery daemons were on (nil: unknown,
	// the machine had no systemd).
	Discovery *Discovery `json:"discovery,omitempty"`
}

// Discovery is the state of the discovery daemons in a backup.
type Discovery struct {
	WSD  bool `json:"wsd"`
	MDNS bool `json:"mdns"`
}

// Summary describes a backup being restored, and what restoring it did.
type Summary struct {
	CreatedAt time.Time `json:"createdAt" doc:"When the backup was made."`
	Hostname  string    `json:"hostname" doc:"The machine it was made on."`
	App       string    `json:"app" doc:"The NetShare Hub version that made it."`
	Users     int       `json:"users"`
	SMBUsers  int       `json:"smbUsers" doc:"Users with SMB access after the restore."`
	Admins    int       `json:"admins"`
	Shares    int       `json:"shares"`
	// Missing are share folders that do not exist on this machine.
	MissingFolders []string `json:"missingFolders" nullable:"false" doc:"Share folders not on this machine (created when createMissingFolders is set)."`
	Warnings       []string `json:"warnings" nullable:"false"`
	Applied        bool     `json:"applied" doc:"False for a dry run: nothing was changed."`
	RollbackCopy   string   `json:"rollbackCopy,omitempty" doc:"The copy of the database kept before restoring (in the data folder's backups)."`
	SignInAgain    bool     `json:"signInAgain" doc:"Everyone was signed out, the caller too: sign in with an account from the backup."`
}

// Jobs is what a restore needs from the jobs module.
type Jobs interface {
	ActiveCount(ctx context.Context) (int, error)
}

// Service makes and restores backups.
type Service struct {
	db      *db.DB
	users   *users.Service
	shares  *shares.Service
	guard   *guard.Service
	bans    *guard.Bans
	groups  *groups.Service
	jobs    Jobs
	system  sys.System
	dataDir string
	version string
	now     func() time.Time

	restoring sync.Mutex
}

func NewService(d *db.DB, u *users.Service, s *shares.Service, gr *groups.Service, g *guard.Service, b *guard.Bans, j Jobs,
	system sys.System, dataDir, version string) *Service {
	return &Service{db: d, users: u, shares: s, groups: gr, guard: g, bans: b, jobs: j, system: system,
		dataDir: dataDir, version: version, now: time.Now}
}

func checkPassphrase(v *validate.Errors, p string) {
	v.Check(len(p) >= MinPassphrase, "passphrase", "too_short",
		fmt.Sprintf("Use at least %d characters: the file can be taken anywhere and guessed at offline.", MinPassphrase))
	v.Check(len(p) <= MaxPassphrase, "passphrase", "too_long", fmt.Sprintf("At most %d characters.", MaxPassphrase))
}

// Export makes an encrypted backup. With smbPasswords, the Samba password
// hashes go in too, so users keep their SMB passwords after a restore.
func (s *Service) Export(ctx context.Context, actor *httpx.Principal, passphrase string, smbPasswords bool) (file []byte, name string, err error) {
	v := validate.New()
	checkPassphrase(v, passphrase)
	if err := v.Err(); err != nil {
		return nil, "", err
	}
	p := Payload{Format: payloadFormat, CreatedAt: s.now().UTC().Truncate(time.Second), App: s.version}
	p.Hostname, _ = os.Hostname()
	if p.Users, err = s.users.Records(ctx); err != nil {
		return nil, "", err
	}
	if p.Shares, p.Samba, err = s.shares.Records(ctx); err != nil {
		return nil, "", err
	}
	if p.Groups, err = s.groups.Records(ctx); err != nil {
		return nil, "", err
	}
	if p.Guard, err = s.guard.Settings(ctx); err != nil {
		return nil, "", err
	}
	if p.Bans, err = s.bans.Settings(ctx); err != nil {
		return nil, "", err
	}
	if s.system.Discovery != nil {
		if st, err := s.system.Discovery.DiscoveryStatus(ctx); err == nil && st.Systemd {
			p.Discovery = &Discovery{WSD: st.WSD.Running && st.WSD.Enabled, MDNS: st.MDNS.Running && st.MDNS.Enabled}
		}
	}
	if smbPasswords {
		var names []string
		for _, u := range p.Users {
			if u.SMBAccess {
				names = append(names, u.Username)
			}
		}
		if len(names) > 0 {
			if p.SMBHashes, err = s.system.Accounts.SMBHashes(ctx, names); err != nil {
				return nil, "", fmt.Errorf("read samba password hashes: %w", err)
			}
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, "", err
	}
	if file, err = seal(raw, passphrase); err != nil {
		return nil, "", err
	}
	host := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return -1
	}, p.Hostname)
	name = "netshare-hub-config-" + p.CreatedAt.Format("20060102-150405") + ".nshbackup"
	if host != "" {
		name = "netshare-hub-config-" + host + "-" + p.CreatedAt.Format("20060102-150405") + ".nshbackup"
	}
	slog.InfoContext(ctx, "config backup downloaded", "users", len(p.Users), "shares", len(p.Shares),
		"smb_passwords", len(p.SMBHashes), "by", actor.Username)
	return file, name, nil
}

// RestoreInput is a restore request.
type RestoreInput struct {
	File                 []byte
	Passphrase           string
	DryRun               bool
	CreateMissingFolders bool
}

// plan is a checked backup, ready to apply.
type plan struct {
	payload  Payload
	restore  shares.Restoring
	missing  []int
	smbUsers map[string]string // username -> hash, for the users who keep SMB access
	summary  Summary
}

// Restore checks a backup and, unless it is a dry run, replaces the whole
// configuration with it.
func (s *Service) Restore(ctx context.Context, actor *httpx.Principal, in RestoreInput) (Summary, error) {
	if !s.restoring.TryLock() {
		return Summary{}, errs.Conflict(CodeRestoreBusy, "Another restore is running.")
	}
	defer s.restoring.Unlock()

	p, err := s.read(in)
	if err != nil {
		return Summary{}, err
	}
	pl, err := s.check(ctx, p, in.CreateMissingFolders)
	if err != nil {
		return Summary{}, err
	}
	if in.DryRun {
		return pl.summary, nil
	}
	if n, err := s.jobs.ActiveCount(ctx); err != nil {
		return Summary{}, err
	} else if n > 0 {
		return Summary{}, errs.Conflict(CodeRestoreBusy, fmt.Sprintf(
			"%d background job(s) are queued or running. Wait for them to finish (or cancel them), then restore.", n))
	}
	return s.apply(ctx, actor, pl)
}

// read decrypts and decodes a backup file.
func (s *Service) read(in RestoreInput) (Payload, error) {
	v := validate.New()
	checkPassphrase(v, in.Passphrase)
	if err := v.Err(); err != nil {
		return Payload{}, err
	}
	raw, err := open(in.File, in.Passphrase)
	switch {
	case errors.Is(err, errTooNew):
		return Payload{}, errs.Invalid(CodeBackupTooNew, "This backup was made by a newer version of NetShare Hub. Update this one first.")
	case errors.Is(err, errUnreadable):
		return Payload{}, errs.Invalid(CodeBackupUnreadable, "The passphrase is wrong, or the file is damaged. Nothing was changed.")
	case errors.Is(err, errTooLarge):
		return Payload{}, errs.Invalid(CodeBackupUnreadable, "This file is too large to be a NetShare Hub backup. Nothing was changed.")
	case err != nil:
		return Payload{}, errs.Invalid(CodeBackupUnreadable, "This is not a NetShare Hub backup file. Nothing was changed.")
	}
	var p Payload
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || p.Format != payloadFormat {
		return Payload{}, errs.Invalid(CodeBackupUnreadable, "The backup's contents cannot be read. Nothing was changed.")
	}
	return p, nil
}

// check validates everything in a backup against this machine and builds
// the plan. Nothing is changed.
func (s *Service) check(ctx context.Context, p Payload, createMissing bool) (*plan, error) {
	pl := &plan{payload: p, smbUsers: map[string]string{}}
	sum := &pl.summary
	sum.CreatedAt, sum.Hostname, sum.App = p.CreatedAt, p.Hostname, p.App
	sum.MissingFolders, sum.Warnings = []string{}, []string{}
	v := validate.New()
	users.CheckRecords(v, p.Users)

	// SMB access needs a Samba password. A backup made without them gives
	// those users web access only, and they lose their place on shares.
	noSMB := map[int64]bool{}
	byID := map[int64]users.User{}
	for i := range p.Users {
		u := &p.Users[i]
		if u.SMBAccess {
			hash, ok := p.SMBHashes[u.Username]
			switch {
			case !ok:
				u.SMBAccess = false
				noSMB[u.ID] = true
				sum.Warnings = append(sum.Warnings, fmt.Sprintf(
					"%s: SMB access is turned off (the backup has no SMB password for them). Set a password to turn it on, then give them their shares again.", u.Username))
			case !sys.ValidNTHash(hash):
				v.Add("smbHashes."+u.Username, "invalid_format", "Not a Samba password hash.")
			default:
				pl.smbUsers[u.Username] = hash
			}
		}
		byID[u.ID] = u.User()
		if u.Role == rbac.RoleAdmin && !u.Disabled {
			sum.Admins++
		}
	}
	for name := range p.SMBHashes {
		if _, ok := pl.smbUsers[name]; !ok && !v.Has("smbHashes."+name) {
			found := false
			for _, u := range p.Users {
				found = found || u.Username == name
			}
			if !found {
				v.Add("smbHashes."+name, "unknown_user", "This user is not in the backup.")
			}
		}
	}
	for i := range p.Shares {
		kept := p.Shares[i].Access[:0]
		for _, a := range p.Shares[i].Access {
			if !noSMB[a.UserID] {
				kept = append(kept, a)
			}
		}
		p.Shares[i].Access = kept
	}

	// Groups: members without SMB access in the restore are left out, like
	// their share access.
	usernames, smb := map[string]bool{}, map[int64]bool{}
	for id, u := range byID {
		usernames[u.Username] = true
		smb[id] = u.SMBAccess && !noSMB[id]
	}
	for i := range p.Groups {
		kept := []int64{}
		for _, m := range p.Groups[i].Members {
			if !noSMB[m] {
				kept = append(kept, m)
			}
		}
		p.Groups[i].Members = kept
	}
	groups.CheckRecords(v, p.Groups, usernames, smb)
	members, names := map[int64][]int64{}, map[int64]string{}
	for _, g := range p.Groups {
		names[g.ID], members[g.ID] = g.Name, g.Members
	}
	pl.payload.Groups = p.Groups
	pl.restore = shares.Restoring{Shares: p.Shares, Server: p.Samba, Users: byID, GroupMembers: members, GroupNames: names}
	missing, err := s.shares.CheckRestore(ctx, v, &pl.restore, createMissing)
	if err != nil {
		return nil, err
	}
	pl.missing = missing
	for _, i := range missing {
		sum.MissingFolders = append(sum.MissingFolders, pl.restore.Shares[i].Path)
	}

	gv := validate.New()
	guard.CheckSettings(gv, p.Guard)
	v.Merge("guard.", gv)
	bv := validate.New()
	pl.payload.Bans.AllowList = guard.CheckBanSettings(bv, p.Bans)
	v.Merge("bans.", bv)

	if err := v.Err(); err != nil {
		var e *errs.E
		if errors.As(err, &e) {
			return nil, errs.Invalid(CodeBackupInvalid, "This backup cannot be restored on this machine. Nothing was changed.", e.Fields...)
		}
		return nil, err
	}
	pl.payload.Users = p.Users
	sum.Users, sum.SMBUsers, sum.Shares = len(p.Users), len(pl.smbUsers), len(pl.restore.Shares)
	return pl, nil
}

// apply replaces the configuration with a checked backup.
func (s *Service) apply(ctx context.Context, actor *httpx.Principal, pl *plan) (Summary, error) {
	sum := pl.summary

	// The copy of the database to go back to by hand, kept before anything
	// changes.
	copyName := "pre-restore-" + s.now().UTC().Format("20060102-150405.000000") + ".db"
	dir := filepath.Join(s.dataDir, "backups")
	if err := s.db.Snapshot(ctx, filepath.Join(dir, copyName)); err != nil {
		return Summary{}, fmt.Errorf("keep a copy of the database before restoring (nothing was changed): %w", err)
	}
	prunePreRestore(dir)
	sum.RollbackCopy = copyName

	// What the accounts are now, to put them back if the restore fails.
	current, err := s.users.Records(ctx)
	if err != nil {
		return Summary{}, err
	}
	oldSMB := map[string]users.Record{}
	var oldNames []string
	for _, u := range current {
		if u.SMBAccess {
			oldSMB[u.Username] = u
			oldNames = append(oldNames, u.Username)
		}
	}
	oldHashes, err := s.system.Accounts.SMBHashes(ctx, oldNames)
	if err != nil {
		return Summary{}, fmt.Errorf("read the current samba password hashes (nothing was changed): %w", err)
	}

	if len(pl.missing) > 0 {
		if err := s.shares.CreateRestoreDirs(ctx, &pl.restore, pl.missing); err != nil {
			return Summary{}, err
		}
	}

	err = s.shares.Replace(ctx, shares.Replacement{
		Restoring: pl.restore,
		WriteUsers: func(tx *sql.Tx) error {
			if err := s.users.ReplaceAll(ctx, tx, pl.payload.Users); err != nil {
				return err
			}
			return groups.ReplaceAll(ctx, tx, pl.payload.Groups, s.now())
		},
		Prepare: func(ctx context.Context) (func(), error) {
			return s.prepareAccounts(ctx, pl, oldSMB, oldHashes)
		},
	})
	if err != nil && !errs.Is(err, shares.CodeSambaReloadFailed) {
		return Summary{}, err
	}
	if err != nil {
		sum.Warnings = append(sum.Warnings, "Samba could not be reloaded; the restored shares take effect when Samba restarts.")
	}
	sum.Applied, sum.SignInAgain = true, true

	// The restore is in place. What follows cannot make it fail: problems
	// become warnings.
	var restoredAdmin int64
	for _, u := range pl.payload.Users {
		if u.Role == rbac.RoleAdmin && !u.Disabled && (restoredAdmin == 0 || u.ID < restoredAdmin) {
			restoredAdmin = u.ID
		}
	}
	by := &httpx.Principal{UserID: restoredAdmin, Username: actor.Username + " (restore)", Role: rbac.RoleAdmin}

	// The restored groups on this machine (the database has them already).
	if err := s.groups.Reapply(ctx); err != nil {
		sum.Warnings = append(sum.Warnings, "The Linux groups could not be set up: "+errs.From(err).Message)
	}
	// Accounts no longer in use: the new config no longer names them.
	for _, name := range oldNames {
		if _, keep := pl.smbUsers[name]; keep {
			continue
		}
		if err := s.system.Accounts.DeleteUser(ctx, name); err != nil {
			slog.WarnContext(ctx, "restore: could not remove an account that is no longer used", "account", name, "err", err)
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("The system account %q is no longer used but could not be removed.", name))
		}
	}
	if _, err := s.guard.UpdateSettings(ctx, by, pl.payload.Guard); err != nil {
		sum.Warnings = append(sum.Warnings, "The ransomware guard settings could not be restored: "+err.Error())
	}
	if _, err := s.bans.UpdateSettings(ctx, by, pl.payload.Bans); err != nil {
		sum.Warnings = append(sum.Warnings, "The brute-force protection settings could not be restored: "+err.Error())
	}
	sum.Warnings = append(sum.Warnings, s.restoreDiscovery(ctx, pl.payload.Discovery)...)
	if n := s.shares.QueueFixAll(ctx, restoredAdmin); n > 0 {
		sum.Warnings = append(sum.Warnings, fmt.Sprintf(
			"The files inside %d share(s) are being given the restored permissions (background jobs).", n))
	}
	slog.InfoContext(ctx, "config restored from a backup", "made_at", pl.payload.CreatedAt, "made_on", pl.payload.Hostname,
		"users", sum.Users, "shares", sum.Shares, "rollback_copy", copyName, "by", actor.Username)
	return sum, nil
}

// prepareAccounts creates (or updates) the system account of every user
// who has SMB access in the backup, with their Samba password hash. It is
// all-or-nothing: on failure, what it did is undone, and the returned undo
// does the same later.
func (s *Service) prepareAccounts(ctx context.Context, pl *plan, oldSMB map[string]users.Record, oldHashes map[string]string) (func(), error) {
	acc := s.system.Accounts
	var steps []func()
	undo := func() {
		for i := len(steps) - 1; i >= 0; i-- {
			steps[i]()
		}
	}
	names := make([]string, 0, len(pl.smbUsers))
	for name := range pl.smbUsers {
		names = append(names, name)
	}
	sort.Strings(names)
	disabled := map[string]bool{}
	for _, u := range pl.payload.Users {
		disabled[u.Username] = u.Disabled
	}
	for _, name := range names {
		// CreateUserFromHash is all-or-nothing: when it fails, there is
		// nothing of this account to undo.
		if err := acc.CreateUserFromHash(ctx, name, pl.smbUsers[name]); err != nil {
			undo()
			if errors.Is(err, sys.ErrNameTaken) {
				return nil, errs.Invalid(CodeBackupInvalid, "This backup cannot be restored on this machine. Nothing was changed.",
					errs.FieldError{Field: "users." + name, Code: "taken_by_system",
						Message: "A system account on this machine already has this name."})
			}
			return nil, fmt.Errorf("create the account of %q (nothing was changed): %w", name, err)
		}
		if old, existed := oldSMB[name]; existed {
			steps = append(steps, func() {
				if h, ok := oldHashes[name]; ok {
					if err := acc.CreateUserFromHash(ctx, name, h); err != nil {
						slog.ErrorContext(ctx, "restore undo: could not put an account's password back", "account", name, "err", err)
					}
				}
				if err := acc.SetEnabled(ctx, name, !old.Disabled); err != nil {
					slog.ErrorContext(ctx, "restore undo: could not put an account's state back", "account", name, "err", err)
				}
			})
		} else {
			steps = append(steps, func() {
				if err := acc.DeleteUser(ctx, name); err != nil {
					slog.ErrorContext(ctx, "restore undo: could not remove an account", "account", name, "err", err)
				}
			})
		}
		if disabled[name] {
			if err := acc.SetEnabled(ctx, name, false); err != nil {
				undo()
				return nil, fmt.Errorf("disable the account of %q (nothing was changed): %w", name, err)
			}
		}
	}
	return undo, nil
}

// restoreDiscovery switches the discovery daemons as they were in the
// backup, where this machine can.
func (s *Service) restoreDiscovery(ctx context.Context, want *Discovery) []string {
	if want == nil || s.system.Discovery == nil {
		return nil
	}
	st, err := s.system.Discovery.DiscoveryStatus(ctx)
	if err != nil || !st.Systemd {
		return []string{"Network discovery was not changed: this machine is not run by systemd."}
	}
	var warnings []string
	for _, c := range []struct {
		svc   sys.DiscoveryService
		state sys.ServiceState
		on    bool
	}{{sys.WSD, st.WSD, want.WSD}, {sys.MDNS, st.MDNS, want.MDNS}} {
		on, off := c.state.Running && c.state.Enabled, !c.state.Running && !c.state.Enabled
		if (c.on && on) || (!c.on && off) {
			continue
		}
		if c.on && !c.state.Installed {
			warnings = append(warnings, fmt.Sprintf("Network discovery (%s) was on in the backup, but its daemon is not installed here.", c.svc))
			continue
		}
		if !c.on && !c.state.Installed {
			continue
		}
		if err := s.system.Discovery.SetDiscovery(ctx, c.svc, c.on); err != nil {
			warnings = append(warnings, fmt.Sprintf("Network discovery (%s) could not be switched: %v", c.svc, err))
		}
	}
	return warnings
}

// prunePreRestore keeps the newest 5 copies made before restores.
func prunePreRestore(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pre-restore-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // the names end in a sortable UTC time
	for len(names) > 5 {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}
