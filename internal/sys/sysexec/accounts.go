package sysexec

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"netshare-hub/internal/sys"
)

// Accounts manages Linux + Samba accounts with the standard system tools.
type Accounts struct {
	runner Runner
	// onRevoke is called when an account loses the right to act: it is
	// disabled or about to be deleted. The file workers use it to stop the
	// account's worker.
	onRevoke func(name string)
}

func (a *Accounts) revoked(name string) {
	if a.onRevoke != nil {
		a.onRevoke(name)
	}
}

func NewAccounts(r Runner) *Accounts { return &Accounts{runner: r} }

// account is what we know about an existing Linux account.
type account struct {
	uid     int
	managed bool // created by NetShare Hub: in ManagedGroup and not a system UID
}

// lookup finds a Linux account. It returns nil when there is none.
func (a *Accounts) lookup(ctx context.Context, name string) (*account, error) {
	res, err := a.runner.Run(ctx, Command{Name: "getent", Args: []string{"passwd", name}})
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
	case 2: // getent: key not found
		return nil, nil
	default:
		return nil, fmt.Errorf("getent passwd exited with code %d", res.ExitCode)
	}

	// name:x:uid:gid:gecos:home:shell
	fields := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(fields) < 3 || fields[0] != name {
		return nil, fmt.Errorf("unexpected getent output for %q", name)
	}
	uid, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("unexpected uid for %q", name)
	}

	groups, err := run(ctx, a.runner, "id", "-Gn", name)
	if err != nil {
		return nil, err
	}
	inGroup := slices.Contains(strings.Fields(groups.Stdout), sys.ManagedGroup)
	return &account{uid: uid, managed: inGroup && uid >= sys.MinManagedUID}, nil
}

// managed returns the account only if NetShare Hub may change it.
func (a *Accounts) managed(ctx context.Context, name string) error {
	if !sys.ValidAccountName(name) {
		return fmt.Errorf("%w: account name", sys.ErrInvalid)
	}
	acc, err := a.lookup(ctx, name)
	switch {
	case err != nil:
		return err
	case acc == nil:
		return sys.ErrNotFound
	case !acc.managed:
		return sys.ErrNotManaged
	}
	return nil
}

func (a *Accounts) CreateUser(ctx context.Context, name, password string) error {
	if !sys.ValidAccountName(name) {
		return fmt.Errorf("%w: account name", sys.ErrInvalid)
	}
	if !sys.ValidPassword(password) {
		return fmt.Errorf("%w: password", sys.ErrInvalid)
	}

	acc, err := a.lookup(ctx, name)
	if err != nil {
		return err
	}
	if acc != nil && !acc.managed {
		return sys.ErrNameTaken
	}

	created := false
	if acc == nil {
		if err := ensureManagedGroup(ctx, a.runner); err != nil {
			return err
		}
		// No home directory, no login shell, and the Linux password stays
		// locked: the account exists only so Samba can map the user to a UID.
		_, err := run(ctx, a.runner, "useradd",
			"--no-create-home", "--home-dir", "/nonexistent",
			"--shell", "/usr/sbin/nologin",
			"--groups", sys.ManagedGroup,
			"--comment", "NetShare Hub user",
			name)
		if err != nil {
			return err
		}
		created = true
	}

	if err := a.setSambaPassword(ctx, name, password); err != nil {
		if created {
			// All-or-nothing: do not leave a Linux account without a Samba one.
			if _, delErr := run(ctx, a.runner, "userdel", name); delErr != nil {
				slog.ErrorContext(ctx, "could not remove half-created account", "account", name, "err", delErr)
			}
		}
		return err
	}
	// An adopted leftover account may have been disabled.
	_, err = run(ctx, a.runner, "smbpasswd", "-e", name)
	return err
}

func (a *Accounts) SetPassword(ctx context.Context, name, password string) error {
	if !sys.ValidPassword(password) {
		return fmt.Errorf("%w: password", sys.ErrInvalid)
	}
	if err := a.managed(ctx, name); err != nil {
		return err
	}
	return a.setSambaPassword(ctx, name, password)
}

// setSambaPassword adds the Samba account or updates its password. The
// password goes over stdin (-s), so it never appears in the process list.
func (a *Accounts) setSambaPassword(ctx context.Context, name, password string) error {
	_, err := runInput(ctx, a.runner, password+"\n"+password+"\n", "smbpasswd", "-s", "-a", name)
	return err
}

func (a *Accounts) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if err := a.managed(ctx, name); err != nil {
		return err
	}
	flag := "-d"
	if enabled {
		flag = "-e"
	}
	if _, err := run(ctx, a.runner, "smbpasswd", flag, name); err != nil {
		return err
	}
	if !enabled {
		a.revoked(name)
	}
	return nil
}

func (a *Accounts) DeleteUser(ctx context.Context, name string) error {
	if !sys.ValidAccountName(name) {
		return fmt.Errorf("%w: account name", sys.ErrInvalid)
	}
	acc, err := a.lookup(ctx, name)
	if err != nil {
		return err
	}
	if acc == nil {
		return nil // already gone
	}
	if !acc.managed {
		return sys.ErrNotManaged
	}

	// Stop the account's file worker first: userdel refuses to delete a
	// user that still has a running process.
	a.revoked(name)

	// The Samba entry may already be missing; that must not block the delete.
	if _, err := run(ctx, a.runner, "smbpasswd", "-x", name); err != nil {
		slog.WarnContext(ctx, "could not remove samba account, continuing", "account", name, "err", err)
	}
	_, err = run(ctx, a.runner, "userdel", name)
	return err
}

// ensureManagedGroup creates the marker group on first use.
func ensureManagedGroup(ctx context.Context, r Runner) error {
	res, err := r.Run(ctx, Command{Name: "getent", Args: []string{"group", sys.ManagedGroup}})
	if err != nil {
		return err
	}
	switch res.ExitCode {
	case 0:
		return nil
	case 2:
		_, err := run(ctx, r, "groupadd", sys.ManagedGroup)
		return err
	default:
		return fmt.Errorf("getent group exited with code %d", res.ExitCode)
	}
}

// managedGroupID returns the numeric id of the marker group.
func managedGroupID(ctx context.Context, r Runner) (int, error) {
	if err := ensureManagedGroup(ctx, r); err != nil {
		return 0, err
	}
	res, err := run(ctx, r, "getent", "group", sys.ManagedGroup)
	if err != nil {
		return 0, err
	}
	// name:x:gid:members
	fields := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(fields) < 3 {
		return 0, fmt.Errorf("unexpected getent output for group %q", sys.ManagedGroup)
	}
	return strconv.Atoi(fields[2])
}

// disabled reports whether the account's Samba entry is disabled. A missing
// Samba entry counts as disabled: such an account cannot use the shares.
func (a *Accounts) disabled(ctx context.Context, name string) (bool, error) {
	res, err := a.runner.Run(ctx, Command{Name: "pdbedit", Args: []string{"-u", name, "-w"}})
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return true, nil
	}
	// name:uid:LM hash:NT hash:[account flags]:last change
	fields := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(fields) < 5 || fields[0] != name {
		return false, fmt.Errorf("unexpected pdbedit output for %q", name)
	}
	return strings.Contains(fields[4], "D"), nil
}
