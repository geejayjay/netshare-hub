// Package sysfake is an in-memory stand-in for the operating system. It is
// used by tests and by development mode (sys = "fake"), so the whole app can
// be built and clicked through on any machine without Linux, root or Samba.
//
// It enforces the same rules as the real implementation (name and password
// validation, managed vs. system accounts), so behavior seen here matches
// production.
package sysfake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sambalog"
)

// Account is one fake Linux + Samba account.
type Account struct {
	Password string
	// Hash is the account's Samba password hash (NTHash of the password).
	Hash    string
	Enabled bool
}

// NTHash is the fake's Samba password hash: a stand-in, not Samba's.
func NTHash(password string) string {
	sum := sha256.Sum256([]byte("nt:" + password))
	return strings.ToUpper(hex.EncodeToString(sum[:16]))
}

// Fake implements sys.Accounts, sys.Samba and sys.Files.
type Fake struct {
	mu sync.Mutex

	accounts map[string]*Account
	// system holds names that exist but are not managed, like real system accounts.
	system map[string]bool
	dirs   map[string]bool

	// Config is the Samba config rendered from the last applied shares.
	Config string
	// Applied counts successful ApplyConfig calls.
	Applied int
	// fixed lists the shares FixPermissions ran for, in order.
	fixed []string

	// failures makes the next call of an operation fail. See FailNext.
	failures map[string]error

	// shares are the last applied shares; workers serve them from real
	// folders under FilesDir, so the file manager works in development.
	shares  []sys.ShareSpec
	workers map[string]*fakeWorker
	// FilesDir holds one folder per share. It is created on first use;
	// tests may set it to a temporary directory.
	FilesDir string

	// Exports and ExportsFile are the last applied NFS exports.
	Exports     []sys.NFSExport
	ExportsFile string
	nfs         sys.NFSStatus
	replication *Replication
	block       *Block

	// MissingModules pretends these Samba modules are not installed.
	MissingModules []string
	disconnected   []netip.Addr
	macs           map[netip.Addr]string
	sessions       []sys.Session
	discovery      sys.DiscoveryStatus
	// listeners are the open Events streams (EmitAudit feeds them).
	listeners map[chan sambalog.Event]bool
}

type fakeWorker struct {
	server   *http.Server
	endpoint sys.WorkerEndpoint
	roots    []sys.WorkerRoot // as returned by RootsFor (share paths, not fake folders)
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

// MissingPrefix exists, but nothing inside it does until it is created, so
// "folder not found" and "create folder" can be tried out in development.
const MissingPrefix = "/missing"

// New returns a fake with a few system accounts already present.
func New() *Fake {
	return &Fake{
		accounts: map[string]*Account{},
		system:   map[string]bool{"root": true, "daemon": true, "nobody": true, "www-data": true, "sshd": true},
		dirs:     map[string]bool{},
		failures: map[string]error{},
		workers:  map[string]*fakeWorker{},
		nfs:      sys.NFSStatus{Installed: true, Running: true, Supported: true},
		discovery: sys.DiscoveryStatus{Systemd: true,
			WSD:  sys.ServiceState{Installed: true, Unit: "wsdd.service"},
			MDNS: sys.ServiceState{Installed: true, Unit: "avahi-daemon.service"}},
	}
}

// System returns the fake as a sys.System.
func (f *Fake) System() sys.System {
	return sys.System{Accounts: f, Samba: f, Files: f, Workers: f, Audit: f, Neighbors: f, Discovery: f, NFS: f,
		Replication: f.Replication(), Block: f.Block()}
}

// --- sys.NFS ---

// SetNFSStatus sets what NFSStatus reports (Exports is filled in).
func (f *Fake) SetNFSStatus(st sys.NFSStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nfs = st
}

// CurrentExports returns the applied NFS exports file.
func (f *Fake) CurrentExports() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ExportsFile
}

// NFSStatus reports the pretend NFS server: installed and running.
func (f *Fake) NFSStatus(context.Context) (sys.NFSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.nfs
	st.Exports = append([]string{}, sys.ParseExportShares(f.ExportsFile)...)
	return st, nil
}

// refreshExports re-checks the applied exports against the shares (the
// real system's NFS.Refresh). f.mu is held.
func (f *Fake) refreshExports() {
	// Like the real system: requests are kept; only what is allowed now
	// is exported.
	var resolved []sys.ResolvedExport
	for _, e := range f.Exports {
		if r, err := f.resolveExport(e); err == nil {
			resolved = append(resolved, r)
		}
	}
	if content, err := sys.RenderExports(resolved); err == nil {
		f.ExportsFile = content
	}
}

// resolveExport is the real system's lookup: the share from the applied
// config, the account from the managed ones. f.mu is held.
func (f *Fake) resolveExport(e sys.NFSExport) (sys.ResolvedExport, error) {
	var share *sys.ShareSpec
	for i := range f.shares {
		if strings.EqualFold(f.shares[i].Name, e.Share) {
			share = &f.shares[i]
		}
	}
	if share == nil {
		return sys.ResolvedExport{}, fmt.Errorf("%w: no share named %q", sys.ErrInvalid, e.Share)
	}
	uid, gid := -1, -1
	if e.AsUser != "" {
		acc, ok := f.accounts[e.AsUser]
		if !ok {
			return sys.ResolvedExport{}, fmt.Errorf("%w: %q is not an account NetShare Hub manages", sys.ErrInvalid, e.AsUser)
		}
		if !acc.Enabled {
			return sys.ResolvedExport{}, fmt.Errorf("%w: %q is disabled", sys.ErrDisabled, e.AsUser)
		}
		uid, gid = 1000+len(e.AsUser), 1000
	}
	r, err := sys.ResolveExport(e, *share, uid, gid)
	if err != nil {
		return sys.ResolvedExport{}, fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	return r, nil
}

// ApplyExports checks exports like the real system (against the applied
// shares) and keeps them in Exports and ExportsFile.
func (f *Fake) ApplyExports(_ context.Context, exports []sys.NFSExport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(exports) > 0 && (!f.nfs.Installed || !f.nfs.Supported) {
		return fmt.Errorf("%w: the NFS server cannot run here", sys.ErrNotFound)
	}
	var resolved []sys.ResolvedExport
	for _, e := range exports {
		if err := e.Check(); err != nil {
			return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
		}
		r, err := f.resolveExport(e)
		if errors.Is(err, sys.ErrDisabled) {
			continue // left out while the account is disabled
		}
		if err != nil {
			return err
		}
		resolved = append(resolved, r)
	}
	content, err := sys.RenderExports(resolved)
	if err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	if err := f.fail("ApplyExports"); err != nil {
		return err
	}
	f.Exports = append([]sys.NFSExport(nil), exports...)
	f.ExportsFile = content
	return nil
}

// --- sys.Audit ---

// Events streams what EmitAudit sends, until ctx is done.
func (f *Fake) Events(ctx context.Context) (<-chan sambalog.Event, error) {
	ch := make(chan sambalog.Event, 10000)
	f.mu.Lock()
	if f.listeners == nil {
		f.listeners = map[chan sambalog.Event]bool{}
	}
	f.listeners[ch] = true
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		delete(f.listeners, ch)
		close(ch)
		f.mu.Unlock()
	}()
	return ch, nil
}

// EmitAudit sends an event to every open Events stream, as Samba's logs
// would. Like the real reader, a full stream waits for its reader (a
// burst is never lost); after 5 seconds without a reader the event is
// dropped, so a test without one cannot hang.
func (f *Fake) EmitAudit(ev sambalog.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.listeners {
		select {
		case ch <- ev:
		default:
			select {
			case ch <- ev:
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// AuditListeners says how many Events streams are open (tests wait for one).
func (f *Fake) AuditListeners() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.listeners)
}

// FailNext makes the next call of op fail with err. Operations:
// "CreateUser", "SetPassword", "SetEnabled", "DeleteUser", "ApplyConfig",
// "FixPermissions".
func (f *Fake) FailNext(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[op] = err
}

// Account returns a copy of a managed account, or nil.
func (f *Fake) Account(name string) *Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.accounts[name]; ok {
		c := *a
		return &c
	}
	return nil
}

// AddDir marks a directory as existing.
func (f *Fake) AddDir(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[dir] = true
}

// fail consumes an injected failure. Callers hold the lock.
func (f *Fake) fail(op string) error {
	err := f.failures[op]
	delete(f.failures, op)
	return err
}

// --- sys.Accounts ---

func (f *Fake) CreateUser(_ context.Context, name, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sys.ValidAccountName(name) {
		return fmt.Errorf("%w: account name", sys.ErrInvalid)
	}
	if !sys.ValidPassword(password) {
		return fmt.Errorf("%w: password", sys.ErrInvalid)
	}
	if f.system[name] {
		return sys.ErrNameTaken
	}
	if err := f.fail("CreateUser"); err != nil {
		return err
	}
	f.accounts[name] = &Account{Password: password, Hash: NTHash(password), Enabled: true}
	return nil
}

func (f *Fake) CreateUserFromHash(_ context.Context, name, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sys.ValidAccountName(name) {
		return fmt.Errorf("%w: account name", sys.ErrInvalid)
	}
	if !sys.ValidNTHash(hash) {
		return fmt.Errorf("%w: password hash", sys.ErrInvalid)
	}
	if f.system[name] {
		return sys.ErrNameTaken
	}
	if err := f.fail("CreateUserFromHash"); err != nil {
		return err
	}
	f.accounts[name] = &Account{Hash: hash, Enabled: true}
	return nil
}

func (f *Fake) SMBHashes(_ context.Context, names []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, name := range names {
		if !sys.ValidAccountName(name) {
			return nil, fmt.Errorf("%w: account name", sys.ErrInvalid)
		}
		if f.system[name] {
			return nil, sys.ErrNotManaged
		}
		if a, ok := f.accounts[name]; ok {
			out[name] = a.Hash
		}
	}
	return out, nil
}

func (f *Fake) managed(name string) (*Account, error) {
	if f.system[name] {
		return nil, sys.ErrNotManaged
	}
	a, ok := f.accounts[name]
	if !ok {
		return nil, sys.ErrNotFound
	}
	return a, nil
}

func (f *Fake) SetPassword(_ context.Context, name, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sys.ValidPassword(password) {
		return fmt.Errorf("%w: password", sys.ErrInvalid)
	}
	a, err := f.managed(name)
	if err != nil {
		return err
	}
	if err := f.fail("SetPassword"); err != nil {
		return err
	}
	a.Password = password
	return nil
}

func (f *Fake) SetEnabled(_ context.Context, name string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.managed(name)
	if err != nil {
		return err
	}
	if err := f.fail("SetEnabled"); err != nil {
		return err
	}
	a.Enabled = enabled
	if !enabled {
		f.stopWorkers(name)
	}
	f.refreshExports()
	return nil
}

func (f *Fake) DeleteUser(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.system[name] {
		return sys.ErrNotManaged
	}
	if err := f.fail("DeleteUser"); err != nil {
		return err
	}
	delete(f.accounts, name)
	f.stopWorkers(name)
	return nil
}

// --- sys.Samba ---

func (f *Fake) ApplyConfig(_ context.Context, cfg sys.SambaConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	shares := cfg.Shares
	var missing []string
	for _, m := range cfg.RequiredModules() {
		if slices.Contains(f.MissingModules, m) {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s (on Debian and Ubuntu: apt install samba-vfs-modules)", sys.ErrModuleMissing, strings.Join(missing, ", "))
	}
	// Same rules and same renderer as the real system.
	config, err := sys.RenderConfig(cfg)
	if err != nil {
		return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	// Like the real system: disk image folders and replication receiving
	// folders are never shared.
	var reserved []string
	if f.block != nil {
		for _, d := range f.block.Disks() {
			if d.ImageFolder != "" {
				reserved = append(reserved, d.ImageFolder)
			}
		}
	}
	if f.replication != nil {
		for _, rc := range f.replication.ReceiverList() {
			reserved = append(reserved, rc.Path)
		}
	}
	for i, sh := range shares {
		for _, r := range reserved {
			if sys.Overlaps(sh.Path, r) {
				return fmt.Errorf("%w: share %q would expose %s", sys.ErrReservedFolder, sh.Name, r)
			}
		}
		for _, other := range shares[:i] {
			if sys.Overlaps(sh.Path, other.Path) {
				return fmt.Errorf("%w: shares %q and %q overlap", sys.ErrInvalid, other.Name, sh.Name)
			}
		}
		// Like the real system: only managed accounts can be given access.
		for _, list := range [][]string{sh.Readers, sh.Uploaders} {
			for _, name := range list {
				if _, ok := f.accounts[name]; !ok {
					return fmt.Errorf("%w: %q is not an account NetShare Hub manages", sys.ErrInvalid, name)
				}
			}
		}
	}
	// Like the real system, a failed reload still leaves the new config in
	// place; any other failure changes nothing.
	failure := f.fail("ApplyConfig")
	if failure != nil && !errors.Is(failure, sys.ErrReloadFailed) {
		return failure
	}
	f.Config = config
	f.Applied++
	f.shares = append([]sys.ShareSpec(nil), shares...)
	// Like the real system: no NFS export outlives what its share allows.
	f.refreshExports()
	// Like the real system: restart the workers whose access changed.
	for name := range f.workers {
		if !sameRoots(f.workers[name].roots, sys.RootsFor(name, f.shares)) {
			f.stopWorkers(name)
		}
	}
	return failure
}

// FixPermissions only checks the share is live and counts the call: the
// fake does not simulate folder permissions.
func (f *Fake) FixPermissions(_ context.Context, share string) (sys.PermissionStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sys.ValidShareName(share) {
		return sys.PermissionStats{}, fmt.Errorf("%w: share name", sys.ErrInvalid)
	}
	if err := f.fail("FixPermissions"); err != nil {
		return sys.PermissionStats{}, err
	}
	for _, sh := range f.shares {
		if strings.EqualFold(sh.Name, share) && sh.Live() {
			f.fixed = append(f.fixed, sh.Name)
			return sys.PermissionStats{Updated: 1}, nil
		}
	}
	return sys.PermissionStats{}, fmt.Errorf("%w: no live share named %q", sys.ErrNotFound, share)
}

// Fixed returns the shares FixPermissions ran for, in order.
func (f *Fake) Fixed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fixed...)
}

// DisconnectClient records the address (Disconnected lists them).
func (f *Fake) DisconnectClient(_ context.Context, ip netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !ip.IsValid() || ip.IsUnspecified() {
		return fmt.Errorf("%w: client address", sys.ErrInvalid)
	}
	f.disconnected = append(f.disconnected, ip.Unmap())
	return nil
}

// CurrentConfig returns Config under the lock: for tests that read it while
// background work (bans, the ransomware guard) may be applying a new one.
func (f *Fake) CurrentConfig() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Config
}

// SetDiscoveryStatus sets the state of the pretend discovery daemons.
func (f *Fake) SetDiscoveryStatus(st sys.DiscoveryStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discovery = st
}

// DiscoveryStatus returns the pretend daemons' state: by default both are
// installed and stopped.
func (f *Fake) DiscoveryStatus(context.Context) (sys.DiscoveryStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.discovery, nil
}

// SetDiscovery switches a pretend daemon on or off, like the real one.
func (f *Fake) SetDiscovery(_ context.Context, svc sys.DiscoveryService, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := &f.discovery.WSD
	switch svc {
	case sys.WSD:
	case sys.MDNS:
		st = &f.discovery.MDNS
	default:
		return fmt.Errorf("%w: discovery service %q", sys.ErrInvalid, svc)
	}
	if !f.discovery.Systemd || !st.Installed {
		return fmt.Errorf("%w: no %s daemon is installed", sys.ErrNotFound, svc)
	}
	st.Running, st.Enabled = on, on
	return nil
}

// SetSessions sets what Sessions returns.
func (f *Fake) SetSessions(s []sys.Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append([]sys.Session(nil), s...)
}

// Sessions returns what SetSessions set (minus disconnected ones).
func (f *Fake) Sessions(context.Context) ([]sys.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sys.Session{}, f.sessions...), nil
}

// DisconnectSession removes a session.
func (f *Fake) DisconnectSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sys.ValidSessionID(id) {
		return fmt.Errorf("%w: session id", sys.ErrInvalid)
	}
	for i, s := range f.sessions {
		if s.ID == id {
			f.sessions = append(f.sessions[:i], f.sessions[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: no session %s", sys.ErrNotFound, id)
}

// SetMAC makes MAC return mac for ip (a computer on the local network).
func (f *Fake) SetMAC(ip netip.Addr, mac string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.macs == nil {
		f.macs = map[netip.Addr]string{}
	}
	f.macs[ip] = mac
}

// MAC returns what SetMAC set.
func (f *Fake) MAC(ip netip.Addr) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mac, ok := f.macs[ip.Unmap()]
	return mac, ok
}

// Disconnected lists the clients DisconnectClient dropped, in order.
func (f *Fake) Disconnected() []netip.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]netip.Addr(nil), f.disconnected...)
}

// --- sys.Workers ---

// Endpoint starts an in-process file worker for the account. It runs the
// same handler as a real worker, but not as another user: the fake cannot
// switch identity, so file ownership is not simulated.
// IO priority is not simulated: background requests use the same worker.
func (f *Fake) Endpoint(_ context.Context, account string, _ bool) (sys.WorkerEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sys.ValidAccountName(account) {
		return sys.WorkerEndpoint{}, fmt.Errorf("%w: account name", sys.ErrInvalid)
	}
	acc, err := f.managed(account)
	if err != nil {
		return sys.WorkerEndpoint{}, err
	}
	if !acc.Enabled {
		return sys.WorkerEndpoint{}, sys.ErrDisabled
	}
	if w := f.workers[account]; w != nil {
		return w.endpoint, nil
	}

	original := sys.RootsFor(account, f.shares)
	roots := append([]sys.WorkerRoot(nil), original...)
	if len(roots) == 0 {
		return sys.WorkerEndpoint{}, fmt.Errorf("%w: account has no shares", sys.ErrNotFound)
	}
	for i := range roots {
		dir, err := f.shareDir(roots[i].Share)
		if err != nil {
			return sys.WorkerEndpoint{}, err
		}
		roots[i].Path = dir
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return sys.WorkerEndpoint{}, err
	}
	w := &fakeWorker{
		server:   &http.Server{Handler: fileworker.NewHandler(roots)},
		endpoint: sys.WorkerEndpoint{Network: "tcp", Address: listener.Addr().String()},
		roots:    original,
	}
	go w.server.Serve(listener)
	f.workers[account] = w
	return w.endpoint, nil
}

// ShareDir returns the real folder that stands in for a share's files.
func (f *Fake) ShareDir(share string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir, _ := f.shareDir(share)
	return dir
}

// shareDir creates and returns the folder for a share. Callers hold the lock.
func (f *Fake) shareDir(share string) (string, error) {
	if !sys.ValidShareName(share) {
		return "", fmt.Errorf("%w: share name", sys.ErrInvalid)
	}
	if f.FilesDir == "" {
		dir, err := os.MkdirTemp("", "netshare-hub-fake-")
		if err != nil {
			return "", err
		}
		f.FilesDir = dir
	}
	dir := filepath.Join(f.FilesDir, share)
	return dir, os.MkdirAll(dir, 0o755)
}

// stopWorkers stops one account's worker, or all of them for "". Callers
// hold the lock.
func (f *Fake) stopWorkers(account string) {
	for name, w := range f.workers {
		if account == "" || account == name {
			w.server.Close()
			delete(f.workers, name)
		}
	}
}

// --- sys.Files ---

func (f *Fake) ResolveDir(_ context.Context, dir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !path.IsAbs(dir) {
		return "", fmt.Errorf("%w: path must be absolute", sys.ErrInvalid)
	}
	dir = path.Clean(dir)
	if strings.HasPrefix(dir, MissingPrefix+"/") && !f.dirs[dir] {
		return "", fs.ErrNotExist
	}
	return dir, nil
}

func (f *Fake) CreateShareDir(_ context.Context, dir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir = path.Clean(dir)
	if f.dirs[dir] {
		return "", fs.ErrExist
	}
	f.dirs[dir] = true
	return dir, nil
}
