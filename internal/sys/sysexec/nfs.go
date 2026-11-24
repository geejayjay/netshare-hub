package sysexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"netshare-hub/internal/sys"
)

// DefaultExportsFile is the one exports file NetShare Hub owns. exportfs
// reads every *.exports file in /etc/exports.d next to /etc/exports, so the
// admin's own exports are never touched.
const DefaultExportsFile = "/etc/exports.d/netshare-hub.exports"

// NFS is the real sys.NFS: nfs-kernel-server driven by exportfs.
type NFS struct {
	runner   Runner
	accounts *Accounts
	samba    *Samba
	file     string
	// proc is where the kernel's NFS server state is read (/proc).
	proc string
}

func NewNFS(r Runner, accounts *Accounts, samba *Samba, exportsFile string) *NFS {
	return &NFS{runner: r, accounts: accounts, samba: samba, file: exportsFile, proc: "/proc"}
}

// installed reports whether exportfs is on the machine.
func (n *NFS) installed() bool {
	for _, p := range allowed["exportfs"] {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// unsupported says why no NFS server can run here, or "" when one can.
// The kernel's NFS server needs real root: inside an unprivileged container
// (Proxmox's default LXC) it cannot be started.
func (n *NFS) unsupported() string {
	if unprivilegedContainer(n.proc) {
		return "This machine is an unprivileged container: the kernel's NFS server cannot run in it. " +
			"Export NFS from the Proxmox host or a VM, or use SMB."
	}
	return ""
}

// unprivilegedContainer reports whether this process runs in a user
// namespace that does not map the real root (an unprivileged LXC).
func unprivilegedContainer(proc string) bool {
	raw, err := os.ReadFile(filepath.Join(proc, "self", "uid_map"))
	if err != nil {
		return false
	}
	f := strings.Fields(string(raw))
	// The initial namespace maps everything: "0 0 4294967295".
	return !(len(f) >= 3 && f[0] == "0" && f[1] == "0" && f[2] == "4294967295")
}

// running reports whether the kernel NFS server has threads.
func (n *NFS) running() bool {
	raw, err := os.ReadFile(filepath.Join(n.proc, "fs", "nfsd", "threads"))
	if err != nil {
		return false
	}
	threads, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	return err == nil && threads > 0
}

func (n *NFS) NFSStatus(context.Context) (sys.NFSStatus, error) {
	st := sys.NFSStatus{Installed: n.installed(), Running: n.running(), Exports: []string{}}
	st.Reason = n.unsupported()
	st.Supported = st.Reason == ""
	if raw, err := os.ReadFile(n.file); err == nil {
		st.Exports = append(st.Exports, sys.ParseExportShares(string(raw))...)
	}
	return st, nil
}

func (n *NFS) ApplyExports(ctx context.Context, exports []sys.NFSExport) error {
	for _, e := range exports {
		if err := e.Check(); err != nil {
			return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
		}
	}
	old, err := os.ReadFile(n.file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", n.file, err)
	}
	if len(exports) == 0 && len(old) == 0 {
		return nil // never used NFS: nothing to do, even without an NFS server
	}
	if len(exports) > 0 {
		if reason := n.unsupported(); reason != "" {
			return fmt.Errorf("%w: %s", sys.ErrNotFound, reason)
		}
	}
	if !n.installed() {
		if len(exports) == 0 {
			return os.Remove(n.file) // nothing reads it without a server
		}
		return fmt.Errorf("%w: the NFS server is not installed (on Debian and Ubuntu: apt install nfs-kernel-server)", sys.ErrNotFound)
	}

	resolved, err := n.resolve(ctx, exports)
	if err != nil {
		return err
	}
	content, err := sys.RenderExports(resolved)
	if err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	if len(content) > maxConfigSize {
		return fmt.Errorf("%w: exports too large", sys.ErrInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(n.file), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(n.file), err)
	}
	if err := writeAtomic(n.file, content); err != nil {
		return err
	}
	if err := n.reexport(ctx); err != nil {
		n.rollback(ctx, old)
		return err
	}
	n.saveRequests(ctx, exports)
	return nil
}

// rollback puts the previous exports file back and re-exports it.
func (n *NFS) rollback(ctx context.Context, old []byte) {
	var err error
	if old == nil {
		err = os.Remove(n.file)
	} else {
		err = writeAtomic(n.file, string(old))
	}
	if err == nil {
		err = n.reexport(ctx)
	}
	if err != nil {
		slog.ErrorContext(ctx, "could not put the previous NFS exports back", "file", n.file, "err", err)
	}
}

// requestsFile keeps the last applied requests (share, clients, account),
// so the root side can check them again whenever the shares change. It does
// not end in .exports, so exportfs never reads it.
func (n *NFS) requestsFile() string { return n.file + ".json" }

func (n *NFS) saveRequests(ctx context.Context, exports []sys.NFSExport) {
	if exports == nil {
		exports = []sys.NFSExport{}
	}
	raw, _ := json.Marshal(exports) // plain data: cannot fail
	if err := writeAtomicMode(n.requestsFile(), string(raw), 0o600); err != nil {
		slog.WarnContext(ctx, "could not record the NFS exports", "err", err)
	}
}

// Refresh checks the requested exports against the shares and accounts as
// they are now, after a Samba change or an account being enabled or
// disabled: an export whose share is gone, or whose account lost access or
// is disabled, is left out; one whose account now only reads becomes
// read-only. So no NFS export ever outlives what the share allows, even if
// the caller never asks again. Requests that are left out are kept, so an
// account enabled again gets its exports back.
func (n *NFS) Refresh(ctx context.Context) {
	raw, err := os.ReadFile(n.requestsFile())
	if err != nil {
		return // NFS never used
	}
	var requests []sys.NFSExport
	if err := json.Unmarshal(raw, &requests); err != nil {
		slog.ErrorContext(ctx, "the recorded NFS exports cannot be read; exporting nothing", "err", err)
		requests = nil
	}
	var resolved []sys.ResolvedExport
	for _, e := range requests {
		r, err := n.resolve(ctx, []sys.NFSExport{e})
		if err != nil {
			slog.WarnContext(ctx, "NFS export left out: the share or account does not allow it now", "share", e.Share, "err", err)
			continue
		}
		resolved = append(resolved, r...)
	}
	content, err := sys.RenderExports(resolved)
	if err != nil {
		slog.ErrorContext(ctx, "could not render the NFS exports", "err", err)
		return
	}
	old, _ := os.ReadFile(n.file)
	if string(old) == content || (len(resolved) == 0 && len(old) == 0) {
		return
	}
	if !n.installed() {
		return
	}
	if err := writeAtomic(n.file, content); err != nil {
		slog.ErrorContext(ctx, "could not update the NFS exports", "err", err)
		return
	}
	if err := n.reexport(ctx); err != nil {
		// The previous exports may now give more than allowed: export
		// nothing rather than put them back.
		slog.ErrorContext(ctx, "the NFS server refused the updated exports; exporting none", "err", err)
		if writeAtomic(n.file, sys.ExportsHeader) == nil {
			_ = n.reexport(ctx)
		}
	}
}

// resolve looks up each export's share in the applied Samba config, and the
// account it acts as, and decides what it may do (sys.ResolveExport).
func (n *NFS) resolve(ctx context.Context, exports []sys.NFSExport) ([]sys.ResolvedExport, error) {
	if len(exports) == 0 {
		return nil, nil
	}
	applied, err := n.samba.applied()
	if err != nil {
		return nil, err
	}
	byName := map[string]sys.ShareSpec{}
	for _, sh := range applied {
		byName[strings.ToLower(sh.Name)] = sh
	}
	out := make([]sys.ResolvedExport, 0, len(exports))
	for _, e := range exports {
		sh, ok := byName[strings.ToLower(e.Share)]
		if !ok {
			return nil, fmt.Errorf("%w: no share named %q", sys.ErrInvalid, e.Share)
		}
		// The same folder checks as Samba, on the folder as it is now.
		if err := n.samba.checkPath(sh); err != nil {
			return nil, err
		}
		uid, gid := -1, -1
		if e.AsUser != "" {
			if uid, gid, err = n.managedIDs(ctx, e.AsUser); errors.Is(err, sys.ErrDisabled) {
				slog.InfoContext(ctx, "NFS export left out while its account is disabled", "share", e.Share, "account", e.AsUser)
				continue
			} else if err != nil {
				return nil, err
			}
		}
		r, err := sys.ResolveExport(e, sh, uid, gid)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", sys.ErrInvalid, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// managedIDs returns the UID and primary GID of an account NetShare Hub
// created. Any other account (root, a service account) is refused, so an
// export can never act as one.
func (n *NFS) managedIDs(ctx context.Context, name string) (uid, gid int, err error) {
	if err := n.accounts.managed(ctx, name); err != nil {
		if errors.Is(err, sys.ErrNotFound) || errors.Is(err, sys.ErrNotManaged) {
			return 0, 0, fmt.Errorf("%w: %q is not an account NetShare Hub manages", sys.ErrInvalid, name)
		}
		return 0, 0, err
	}
	res, err := run(ctx, n.runner, "getent", "passwd", name)
	if err != nil {
		return 0, 0, err
	}
	f := strings.Split(strings.TrimSpace(res.Stdout), ":")
	if len(f) < 4 || f[0] != name {
		return 0, 0, fmt.Errorf("unexpected getent output for %q", name)
	}
	if uid, err = strconv.Atoi(f[2]); err == nil {
		gid, err = strconv.Atoi(f[3])
	}
	if err != nil || uid < sys.MinManagedUID {
		return 0, 0, fmt.Errorf("%w: %q is not an account NetShare Hub manages", sys.ErrInvalid, name)
	}
	// Samba refuses a disabled account; NFS signs nobody in, so the root
	// side must refuse it here.
	disabled, err := n.accounts.disabled(ctx, name)
	if err != nil {
		return 0, 0, err
	}
	if disabled {
		return 0, 0, fmt.Errorf("%w: %q is disabled", sys.ErrDisabled, name)
	}
	return uid, gid, nil
}

// exportfsProblem matches what exportfs prints for an entry it refused.
// exportfs exits 0 for some of them, so for a file NetShare Hub wrote
// (where any complaint is a bug) the exit code is not enough.
var exportfsProblem = regexp.MustCompile(`(?i)exportfs: .*(error|failed|unknown|bad|invalid|cannot|does not support)`)

// reexport makes the kernel serve exactly what the exports files say.
func (n *NFS) reexport(ctx context.Context) error {
	res, err := n.runner.Run(ctx, Command{Name: "exportfs", Args: []string{"-ra"}})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 || exportfsProblem.MatchString(res.Stderr) {
		return &sys.ConfigError{Output: truncate(strings.TrimSpace(res.Stderr), 2000)}
	}
	return nil
}
