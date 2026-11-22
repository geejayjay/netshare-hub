//go:build linux

package sysexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"netshare-hub/internal/sys"
)

// DefaultWorkerDir holds one socket per running file worker.
const DefaultWorkerDir = "/run/netshare-hub/workers"

// workerIdle is how long a worker waits without requests before exiting.
const workerIdle = 5 * time.Minute

// Workers starts file workers: one process per user, running as that user.
// This is the only place a process is started with another identity.
type Workers struct {
	runner        Runner
	accounts      *Accounts
	managedConfig string
	dir           string
	// allowUID/allowGID identify the web server. Only it (and root) may
	// connect to a worker. 0 means "root only" (single-process mode).
	allowUID, allowGID int
	idle               time.Duration
	// exe is the program run as "<exe> fileworker"; "" means this program.
	exe string

	mu    sync.Mutex
	procs map[string]*workerProc
}

type workerProc struct {
	account    string
	background bool
	cmd        *exec.Cmd
	socket     string
	roots      []sys.WorkerRoot // what the worker was started with
	done       chan struct{}    // closed when the process has exited
}

func newWorkers(r Runner, accounts *Accounts, managedConfig, dir string, allowUID, allowGID int) *Workers {
	return &Workers{
		runner: r, accounts: accounts, managedConfig: managedConfig, dir: dir,
		allowUID: allowUID, allowGID: allowGID, idle: workerIdle,
		procs: map[string]*workerProc{},
	}
}

// workerKey names one worker: each account has at most one normal and one
// background worker.
func workerKey(account string, background bool) string {
	if background {
		return account + ".bg"
	}
	return account
}

func (w *Workers) Endpoint(ctx context.Context, account string, background bool) (sys.WorkerEndpoint, error) {
	// Only accounts NetShare Hub created can get a worker. This also
	// validates the name, so nothing below handles unchecked input.
	if err := w.accounts.managed(ctx, account); err != nil {
		return sys.WorkerEndpoint{}, err
	}
	// A disabled account gets no new worker (its running ones were stopped
	// when it was disabled). Jobs queued earlier then fail instead of acting.
	if off, err := w.accounts.disabled(ctx, account); err != nil {
		return sys.WorkerEndpoint{}, err
	} else if off {
		return sys.WorkerEndpoint{}, sys.ErrDisabled
	}
	roots, err := w.roots(account)
	if err != nil {
		return sys.WorkerEndpoint{}, err
	}
	if len(roots) == 0 {
		return sys.WorkerEndpoint{}, fmt.Errorf("%w: account has no shares", sys.ErrNotFound)
	}

	key := workerKey(account, background)
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := w.procs[key]; p != nil {
		select {
		case <-p.done: // exited (idle); start a new one below
		default:
			return sys.WorkerEndpoint{Network: "unix", Address: p.socket}, nil
		}
	}

	p, err := w.start(ctx, account, background, roots)
	if err != nil {
		return sys.WorkerEndpoint{}, err
	}
	w.procs[key] = p
	return sys.WorkerEndpoint{Network: "unix", Address: p.socket}, nil
}

// roots returns the shares the account may reach, read from the managed
// Samba config: the one record of which shares are live and who may use them.
func (w *Workers) roots(account string) ([]sys.WorkerRoot, error) {
	config, err := os.ReadFile(w.managedConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read managed config: %w", err)
	}
	return sys.RootsFor(account, sys.ParseShares(string(config))), nil
}

// start launches a worker for the account. Callers hold w.mu.
func (w *Workers) start(ctx context.Context, account string, background bool, roots []sys.WorkerRoot) (*workerProc, error) {
	key := workerKey(account, background)
	uid, groups, err := w.identity(ctx, account)
	if err != nil {
		return nil, err
	}
	// Same primary group Samba forces on the shares ("force group"), so a
	// file made in the browser gets the same group as one made over SMB.
	gid, err := managedGroupID(ctx, w.runner)
	if err != nil {
		return nil, err
	}
	if uid == 0 || uid < sys.MinManagedUID {
		return nil, fmt.Errorf("%w: refusing to start a worker for uid %d", sys.ErrNotManaged, uid)
	}

	// The root side creates the socket, so it has the right owner and mode
	// from the first instant; the worker only inherits it.
	listener, socket, err := w.listen(key)
	if err != nil {
		return nil, err
	}
	file, err := listener.File()
	listener.Close() // the worker keeps the socket open through its copy
	if err != nil {
		os.Remove(socket)
		return nil, fmt.Errorf("hand over worker socket: %w", err)
	}
	defer file.Close()

	config, err := json.Marshal(struct {
		Roots       []sys.WorkerRoot `json:"roots"`
		AllowUID    int              `json:"allowUid"`
		IdleSeconds int              `json:"idleSeconds"`
	}{roots, w.allowUID, int(w.idle.Seconds())})
	if err != nil {
		os.Remove(socket)
		return nil, err
	}
	exe := w.exe
	if exe == "" {
		exe, err = os.Executable()
	}
	if err != nil {
		os.Remove(socket)
		return nil, err
	}

	cmd := exec.Command(exe, "fileworker")
	if background {
		// Lowest IO priority ("idle": only uses the disk when nobody else
		// does) and lower CPU priority. Started through ionice/nice so the
		// priority is set before the worker's first thread exists and every
		// thread inherits it.
		if wrapped := lowPriority(exe); wrapped != nil {
			cmd = wrapped
		} else {
			slog.WarnContext(ctx, "ionice or nice not found; background worker runs at normal priority")
		}
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"} // nothing inherited from root's environment
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(config)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{file} // becomes file descriptor 3 in the worker
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// The kernel switches identity before the worker's first instruction
		// runs: it is never root, not even briefly.
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups},
		Setsid:     true,
	}
	if err := cmd.Start(); err != nil {
		os.Remove(socket)
		return nil, fmt.Errorf("start file worker: %w", err)
	}
	slog.InfoContext(ctx, "file worker started", "account", account, "background", background,
		"uid", uid, "pid", cmd.Process.Pid, "shares", len(roots))

	p := &workerProc{account: account, background: background, cmd: cmd, socket: socket, roots: roots, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
		w.mu.Lock()
		if w.procs[key] == p {
			delete(w.procs, key)
			os.Remove(socket)
		}
		w.mu.Unlock()
		slog.Info("file worker exited", "account", account, "background", background, "pid", cmd.Process.Pid)
	}()
	return p, nil
}

// identity returns the account's uid and all its group ids.
func (w *Workers) identity(ctx context.Context, account string) (int, []uint32, error) {
	res, err := run(ctx, w.runner, "id", "-u", account)
	if err != nil {
		return 0, nil, err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		return 0, nil, fmt.Errorf("unexpected uid for %q", account)
	}
	res, err = run(ctx, w.runner, "id", "-G", account)
	if err != nil {
		return 0, nil, err
	}
	var groups []uint32
	for _, f := range strings.Fields(res.Stdout) {
		g, err := strconv.Atoi(f)
		if err != nil {
			return 0, nil, fmt.Errorf("unexpected group id for %q", account)
		}
		groups = append(groups, uint32(g))
	}
	return uid, groups, nil
}

// listen creates the worker's socket: root:<web server group>, mode 0660,
// inside a 0750 directory, so only root and the web server can connect.
// name is a worker key: a validated account name, optionally with ".bg".
func (w *Workers) listen(name string) (*net.UnixListener, string, error) {
	// Both the worker folder and its parent (NetShare Hub's runtime folder,
	// shared with the agent's socket) must let the web server through, and
	// nobody else. Set them up here rather than relying on the agent having
	// started first.
	for _, dir := range []string{filepath.Dir(w.dir), w.dir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, "", fmt.Errorf("create worker directory: %w", err)
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			return nil, "", fmt.Errorf("worker directory %s must be a real directory, not a link", dir)
		}
		if err := os.Chown(dir, 0, w.allowGID); err != nil {
			return nil, "", fmt.Errorf("secure worker directory: %w", err)
		}
		if err := os.Chmod(dir, 0o750); err != nil {
			return nil, "", fmt.Errorf("secure worker directory: %w", err)
		}
	}

	socket := filepath.Join(w.dir, name+".sock")
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&fs.ModeSocket == 0 {
			return nil, "", fmt.Errorf("%s exists and is not a socket; refusing to replace it", socket)
		}
		if err := os.Remove(socket); err != nil {
			return nil, "", err
		}
	}

	old := syscall.Umask(0o177) // created as 0600: never more open than intended
	l, err := net.Listen("unix", socket)
	syscall.Umask(old)
	if err != nil {
		return nil, "", fmt.Errorf("listen on %s: %w", socket, err)
	}
	listener := l.(*net.UnixListener)
	// Closing our copy must not delete the socket file the worker is using.
	listener.SetUnlinkOnClose(false)
	if err := os.Chown(socket, 0, w.allowGID); err == nil {
		err = os.Chmod(socket, 0o660)
	}
	if err != nil {
		listener.Close()
		os.Remove(socket)
		return nil, "", fmt.Errorf("secure worker socket: %w", err)
	}
	return listener, socket, nil
}

// Stop ends the account's workers (normal and background), if any run. It
// is called when the account is disabled or deleted.
func (w *Workers) Stop(account string) {
	w.mu.Lock()
	var stop []*workerProc
	for _, key := range []string{workerKey(account, false), workerKey(account, true)} {
		if p := w.procs[key]; p != nil {
			stop = append(stop, p)
			delete(w.procs, key)
		}
	}
	w.mu.Unlock()
	for _, p := range stop {
		w.kill(p)
	}
}

// StopAll ends every worker. It is called when shares change, so no worker
// keeps reaching a share its user was just removed from; the next request
// starts a fresh worker with the new list.
func (w *Workers) StopAll() {
	w.mu.Lock()
	procs := w.procs
	w.procs = map[string]*workerProc{}
	w.mu.Unlock()
	for _, p := range procs {
		w.kill(p)
	}
}

// Refresh stops only the workers whose shares changed, so a share change for
// one user does not cut off everyone else's downloads. A worker that can no
// longer read the new config is stopped too: when in doubt, stop.
func (w *Workers) Refresh() {
	w.mu.Lock()
	var stale []*workerProc
	for key, p := range w.procs {
		now, err := w.roots(p.account)
		if err != nil || !sameRoots(p.roots, now) {
			stale = append(stale, p)
			delete(w.procs, key)
		}
	}
	w.mu.Unlock()
	for _, p := range stale {
		w.kill(p)
	}
}

func sameRoots(a, b []sys.WorkerRoot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (w *Workers) kill(p *workerProc) {
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	os.Remove(p.socket)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
	}
}

// webIdentity returns the uid and gid of the user the web server runs as.
// An empty name means the web server is this same root process.
func webIdentity(ctx context.Context, r Runner, name string) (uid, gid int, err error) {
	if name == "" {
		return 0, 0, nil
	}
	if !sys.ValidAccountName(name) {
		return 0, 0, fmt.Errorf("%w: web server user name", sys.ErrInvalid)
	}
	u, err := run(ctx, r, "id", "-u", name)
	if err != nil {
		return 0, 0, fmt.Errorf("web server user %q does not exist: %w", name, err)
	}
	g, err := run(ctx, r, "id", "-g", name)
	if err != nil {
		return 0, 0, err
	}
	uid, err1 := strconv.Atoi(strings.TrimSpace(u.Stdout))
	gid, err2 := strconv.Atoi(strings.TrimSpace(g.Stdout))
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("unexpected id output for %q", name)
	}
	return uid, gid, nil
}

// lowPriority returns "ionice -c 3 nice -n 10 <exe> fileworker", or nil
// when either tool is missing.
func lowPriority(exe string) *exec.Cmd {
	const ionice, nice = "/usr/bin/ionice", "/usr/bin/nice"
	for _, tool := range []string{ionice, nice} {
		if _, err := os.Stat(tool); err != nil {
			return nil
		}
	}
	return exec.Command(ionice, "-c", "3", nice, "-n", "10", exe, "fileworker")
}
