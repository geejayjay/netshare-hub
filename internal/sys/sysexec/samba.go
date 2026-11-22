package sysexec

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"netshare-hub/internal/sys"
)

// Default locations on Debian and Ubuntu.
const (
	DefaultManagedConfig = "/etc/samba/netshare-hub.conf"
	DefaultMainConfig    = "/etc/samba/smb.conf"
)

// maxConfigSize is far above any real config and stops a runaway write.
const maxConfigSize = 4 << 20

// Samba owns exactly one file: the managed config. The admin's own smb.conf
// is touched once, to add a single include line, and is backed up first.
type Samba struct {
	runner        Runner
	managedConfig string
	mainConfig    string
	// protected are folders no share may expose, on top of the system
	// folders: NetShare Hub's own data (the database holds password hashes).
	protected []string
	// afterApply is called once a new set of shares is live. The file
	// workers use it to restart the ones whose access changed, so none keeps
	// reaching a share its user was just removed from.
	afterApply func()
}

func NewSamba(r Runner, managedConfig, mainConfig string, protected ...string) *Samba {
	return &Samba{runner: r, managedConfig: managedConfig, mainConfig: mainConfig, protected: protected}
}

// ApplyShares is the enforcement point for what may be shared. It does not
// trust its caller: every share is checked here, against where its folder
// really is right now, and the config is built here.
func (s *Samba) ApplyShares(ctx context.Context, shares []sys.ShareSpec) error {
	for i, sh := range shares {
		if err := s.checkPath(sh); err != nil {
			return err
		}
		// One folder, one set of permissions: shares may not overlap, or
		// each would keep rewriting the other's ACL.
		for _, other := range shares[:i] {
			if sys.Overlaps(sh.Path, other.Path) {
				return fmt.Errorf("%w: shares %q and %q overlap", sys.ErrInvalid, other.Name, sh.Name)
			}
		}
	}
	// RenderShares checks names, comments, account names and the path rules.
	config, err := sys.RenderShares(shares)
	if err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}

	// Folder permissions first: only for shares whose access changed, and
	// put back as they were if anything after this fails.
	changed, removed, before := s.diff(shares)
	var ids identities
	if len(changed)+len(removed) > 0 {
		if ids, err = loadIdentities(ctx, s.runner); err != nil {
			return err
		}
	}
	var backups []*aclBackup
	undo := func() {
		for i := len(backups) - 1; i >= 0; i-- {
			if err := backups[i].restore(s.protected); err != nil {
				slog.ErrorContext(ctx, "could not restore share folder permissions", "path", backups[i].path, "err", err)
			}
		}
	}
	for _, sh := range changed {
		b, err := setShareACL(sh, ids, s.protected, len(before[sh.Name].Uploaders) > 0)
		if err != nil {
			undo()
			return err
		}
		if b != nil {
			backups = append(backups, b)
		}
	}

	err = s.applyConfig(ctx, config)
	// "Reload failed" still means the new config is in place.
	if err != nil && !errors.Is(err, sys.ErrReloadFailed) {
		undo()
		return err
	}
	// Folders no longer shared lose the entries NetShare Hub gave them.
	for _, old := range removed {
		if _, stripErr := setShareACL(sys.ShareSpec{Name: old.Name, Path: old.Path}, ids, s.protected, len(old.Uploaders) > 0); stripErr != nil {
			slog.WarnContext(ctx, "could not remove permissions from a folder that is no longer shared", "path", old.Path, "err", stripErr)
		}
	}
	if s.afterApply != nil {
		s.afterApply()
	}
	return err
}

// applied returns the shares in the managed config now.
func (s *Samba) applied() ([]sys.ShareSpec, error) {
	config, err := os.ReadFile(s.managedConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read managed config: %w", err)
	}
	return sys.ParseShares(string(config)), nil
}

// diff compares new shares with the applied ones: changed are the shares
// whose folder needs new permissions, removed the folders no longer shared,
// and before the applied shares by name.
func (s *Samba) diff(shares []sys.ShareSpec) (changed, removed []sys.ShareSpec, before map[string]sys.ShareSpec) {
	before = map[string]sys.ShareSpec{}
	previous, err := s.applied()
	if err != nil {
		// Unknown starting point: set every folder.
		return shares, nil, before
	}
	for _, p := range previous {
		before[p.Name] = p
	}
	stillShared := map[string]bool{}
	for _, sh := range shares {
		stillShared[sh.Path] = true
		if p, ok := before[sh.Name]; !ok || !sameAccess(p, sh) {
			changed = append(changed, sh)
		}
	}
	for _, p := range previous {
		if !stillShared[p.Path] {
			removed = append(removed, p)
		}
	}
	return changed, removed, before
}

func (s *Samba) FixPermissions(ctx context.Context, name string) (sys.PermissionStats, error) {
	if !sys.ValidShareName(name) {
		return sys.PermissionStats{}, fmt.Errorf("%w: share name", sys.ErrInvalid)
	}
	shares, err := s.applied()
	if err != nil {
		return sys.PermissionStats{}, err
	}
	for _, sh := range shares {
		if !strings.EqualFold(sh.Name, name) {
			continue
		}
		if !sh.Live() {
			break
		}
		if err := s.checkPath(sh); err != nil {
			return sys.PermissionStats{}, err
		}
		ids, err := loadIdentities(ctx, s.runner)
		if err != nil {
			return sys.PermissionStats{}, err
		}
		return fixShareTree(ctx, sh, ids, s.protected)
	}
	return sys.PermissionStats{}, fmt.Errorf("%w: no live share named %q", sys.ErrNotFound, name)
}

// loadIdentities reads every account and the managed group's members.
func loadIdentities(ctx context.Context, r Runner) (identities, error) {
	if err := ensureManagedGroup(ctx, r); err != nil {
		return identities{}, err
	}
	passwd, err := run(ctx, r, "getent", "passwd")
	if err != nil {
		return identities{}, err
	}
	group, err := run(ctx, r, "getent", "group", sys.ManagedGroup)
	if err != nil {
		return identities{}, err
	}
	return parseIdentities(passwd.Stdout, group.Stdout)
}

// checkPath refuses a share whose folder is, or has become, a way into a
// protected folder: directly, or because a symbolic link now sits in its path.
func (s *Samba) checkPath(sh sys.ShareSpec) error {
	paths := []string{sh.Path}
	// A folder that does not exist (yet, or any more) cannot be resolved;
	// the rules still hold for the path as written.
	if real, err := filepath.EvalSymlinks(sh.Path); err == nil {
		paths = append(paths, filepath.ToSlash(real))
	}
	for _, p := range paths {
		if !pathAllowed(p, s.protected) {
			return fmt.Errorf("%w: share %q points into a protected folder", sys.ErrInvalid, sh.Name)
		}
	}
	return nil
}

// pathAllowed reports whether a real path may be shared: it passes the
// rules, is not a system folder, and does not expose a protected folder.
func pathAllowed(real string, protected []string) bool {
	if !sys.ValidSharePath(real) || !sys.SharablePath(real) {
		return false
	}
	for _, dir := range protected {
		if sys.Overlaps(real, dir) {
			return false
		}
	}
	return true
}

// applyConfig validates and installs a config NetShare Hub generated.
func (s *Samba) applyConfig(ctx context.Context, content string) error {
	if len(content) > maxConfigSize {
		return fmt.Errorf("%w: config too large", sys.ErrInvalid)
	}

	// 1. Write the candidate next to the real file (same filesystem, so the
	//    final rename is atomic) and let Samba itself check it.
	dir := filepath.Dir(s.managedConfig)
	tmp, err := os.CreateTemp(dir, ".netshare-hub-*.conf")
	if err != nil {
		return fmt.Errorf("create candidate config: %w", err)
	}
	candidate := tmp.Name()
	defer os.Remove(candidate) // no-op once renamed

	if _, err := tmp.WriteString(content); err == nil {
		err = tmp.Chmod(0o644)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write candidate config: %w", err)
	}
	if err := s.check(ctx, candidate, true); err != nil {
		return err
	}

	// 2. Keep the previous version, then swap the new one in.
	if old, err := os.ReadFile(s.managedConfig); err == nil {
		if err := os.WriteFile(s.managedConfig+".bak", old, 0o644); err != nil {
			slog.WarnContext(ctx, "could not back up managed samba config", "err", err)
		}
	}
	if err := os.Rename(candidate, s.managedConfig); err != nil {
		return fmt.Errorf("replace managed config: %w", err)
	}

	// 3. Make sure smb.conf includes our file, then reload.
	if err := s.ensureInclude(ctx); err != nil {
		return err
	}
	if _, err := run(ctx, s.runner, "smbcontrol", "all", "reload-config"); err != nil {
		return fmt.Errorf("%w: %v", sys.ErrReloadFailed, err)
	}
	return nil
}

// problem matches what testparm prints for a config with a mistake in it.
// testparm exits 0 for an unknown parameter and only warns, so for a file we
// generated ourselves (where any warning is a bug) the exit code is not enough.
var problem = regexp.MustCompile(`(?i)unknown parameter|badly formed line|error loading`)

// check runs Samba's validator on a config file. strict also rejects
// warnings; use it for files NetShare Hub generates, not for the admin's own
// smb.conf, which may carry harmless warnings we must not block on.
func (s *Samba) check(ctx context.Context, file string, strict bool) error {
	res, err := s.runner.Run(ctx, Command{Name: "testparm", Args: []string{"-s", file}})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 || (strict && problem.MatchString(res.Stderr)) {
		return &sys.ConfigError{Output: truncate(strings.TrimSpace(res.Stderr), 2000)}
	}
	return nil
}

// ensureInclude adds "include = <managed config>" to smb.conf if it is not
// there yet. The original file is backed up once, and the edit is undone if
// Samba does not accept the result.
func (s *Samba) ensureInclude(ctx context.Context) error {
	main, err := os.ReadFile(s.mainConfig)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", s.mainConfig, err)
	}
	include := regexp.MustCompile(`(?mi)^[ \t]*include[ \t]*=[ \t]*` + regexp.QuoteMeta(s.managedConfig) + `[ \t]*\r?$`)
	if include.Match(main) {
		return nil
	}

	backup := s.mainConfig + ".netshare-hub.bak"
	if _, statErr := os.Stat(backup); errors.Is(statErr, fs.ErrNotExist) && len(main) > 0 {
		if err := os.WriteFile(backup, main, 0o644); err != nil {
			return fmt.Errorf("back up %s: %w", s.mainConfig, err)
		}
	}

	// Re-opening [global] is valid in smb.conf and keeps the include out of
	// whatever share section happens to be last in the admin's file.
	updated := string(main)
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += "\n# Added by NetShare Hub. Shares managed in the web UI live in the included file.\n" +
		"[global]\n   include = " + s.managedConfig + "\n"

	if err := writeAtomic(s.mainConfig, updated); err != nil {
		return err
	}
	if err := s.check(ctx, s.mainConfig, false); err != nil {
		// Put the admin's file back exactly as it was.
		if restoreErr := writeAtomic(s.mainConfig, string(main)); restoreErr != nil {
			slog.ErrorContext(ctx, "could not restore smb.conf", "backup", backup, "err", restoreErr)
		}
		return err
	}
	slog.InfoContext(ctx, "added include line to smb.conf", "file", s.mainConfig, "backup", backup)
	return nil
}

// writeAtomic replaces a file in one step, so a reader never sees half of it.
func writeAtomic(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".netshare-hub-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())

	if _, err = tmp.WriteString(content); err == nil {
		err = tmp.Chmod(0o644)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
