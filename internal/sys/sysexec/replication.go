package sysexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/sys"
)

// DefaultReplicationDir holds the replication key, the peer list and the
// pinned host keys. It belongs to root alone (0700): the web server never
// sees the private key. It is NOT inside NetShare Hub's data folder, which
// the unprivileged web server owns.
const DefaultReplicationDir = "/var/lib/netshare-hub-agent/replication"

// replicaHome is the receiving account's home. It and its .ssh folder are
// root's, so the account cannot change which keys may log in.
const replicaHome = "/var/lib/nsh-replica"

// rrsync is rsync's own restricted wrapper (in Debian's rsync package).
var rrsyncPaths = []string{"/usr/bin/rrsync", "/usr/share/doc/rsync/scripts/rrsync"}

// Replication is the real sys.Replication.
type Replication struct {
	runner Runner
	samba  *Samba
	dir    string
	home   string
	// protected folders (the data folder) are never a source or target.
	protected []string
	mu        sync.Mutex // key creation and the peer/receiver files
}

func NewReplication(r Runner, samba *Samba, dir string, protected ...string) *Replication {
	return &Replication{runner: r, samba: samba, dir: dir, home: replicaHome, protected: protected}
}

func firstExisting(paths []string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (r *Replication) ReplicationStatus(context.Context) (sys.ReplicationStatus, error) {
	return sys.ReplicationStatus{
		Rsync:     firstExisting(allowed["rsync"]) != "",
		SSHClient: firstExisting([]string{"/usr/bin/ssh"}) != "",
		SSHServer: firstExisting([]string{"/usr/sbin/sshd"}) != "",
		Rrsync:    firstExisting(rrsyncPaths) != "",
	}, nil
}

// --- keys and peers (the sending side) ---

func (r *Replication) keyFile() string { return filepath.Join(r.dir, "id_ed25519") }

func (r *Replication) ensureDir() error {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", r.dir, err)
	}
	return os.Chmod(r.dir, 0o700)
}

func (r *Replication) PublicKey(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureDir(); err != nil {
		return "", err
	}
	if _, err := os.Stat(r.keyFile()); errors.Is(err, fs.ErrNotExist) {
		if _, err := run(ctx, r.runner, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "netshare-hub-replication",
			"-f", r.keyFile()); err != nil {
			return "", err
		}
	}
	raw, err := os.ReadFile(r.keyFile() + ".pub")
	if err != nil {
		return "", fmt.Errorf("read replication key: %w", err)
	}
	f := strings.Fields(string(raw))
	if len(f) < 2 || !sys.ValidSSHKey(f[0]+" "+f[1]) {
		return "", errors.New("the replication key is not an ed25519 key")
	}
	return f[0] + " " + f[1], nil
}

func (r *Replication) peersFile() string { return filepath.Join(r.dir, "peers.json") }

func (r *Replication) knownHosts(id string) string { return filepath.Join(r.dir, "known_hosts-"+id) }

// knownHostsLine pins a peer's host key for its address and port.
func knownHostsLine(p sys.Peer) string {
	host := p.Host
	if p.Port != 22 {
		host = "[" + p.Host + "]:" + strconv.Itoa(p.Port)
	}
	return host + " " + p.HostKey + "\n"
}

func (r *Replication) SetPeers(_ context.Context, peers []sys.Peer) error {
	seen := map[string]bool{}
	for _, p := range peers {
		if err := p.Check(); err != nil {
			return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
		}
		if seen[p.ID] {
			return fmt.Errorf("%w: peer %q listed twice", sys.ErrInvalid, p.ID)
		}
		seen[p.ID] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureDir(); err != nil {
		return err
	}
	for _, p := range peers {
		if err := writeAtomicMode(r.knownHosts(p.ID), knownHostsLine(p), 0o600); err != nil {
			return err
		}
	}
	if peers == nil {
		peers = []sys.Peer{}
	}
	raw, _ := json.Marshal(peers) // plain data: cannot fail
	if err := writeAtomicMode(r.peersFile(), string(raw), 0o600); err != nil {
		return err
	}
	// Pins of peers no longer listed go.
	entries, _ := os.ReadDir(r.dir)
	for _, e := range entries {
		if id, ok := strings.CutPrefix(e.Name(), "known_hosts-"); ok && !seen[id] {
			_ = os.Remove(filepath.Join(r.dir, e.Name()))
		}
	}
	return nil
}

func (r *Replication) peer(id string) (sys.Peer, error) {
	raw, err := os.ReadFile(r.peersFile())
	if errors.Is(err, fs.ErrNotExist) {
		return sys.Peer{}, fmt.Errorf("%w: no peer %q", sys.ErrNotFound, id)
	}
	if err != nil {
		return sys.Peer{}, err
	}
	var peers []sys.Peer
	if err := json.Unmarshal(raw, &peers); err != nil {
		return sys.Peer{}, fmt.Errorf("read peers: %w", err)
	}
	for _, p := range peers {
		if p.ID == id && p.Check() == nil {
			return p, nil
		}
	}
	return sys.Peer{}, fmt.Errorf("%w: no peer %q", sys.ErrNotFound, id)
}

// --- receivers (the receiving side) ---

func (r *Replication) HostKey(context.Context) (string, error) {
	raw, err := os.ReadFile("/etc/ssh/ssh_host_ed25519_key.pub")
	if err != nil {
		return "", fmt.Errorf("%w: no SSH server host key (on Debian and Ubuntu: apt install openssh-server)", sys.ErrNotFound)
	}
	f := strings.Fields(string(raw))
	if len(f) < 2 || !sys.ValidSSHKey(f[0]+" "+f[1]) {
		return "", fmt.Errorf("%w: the SSH server has no ed25519 host key", sys.ErrNotFound)
	}
	return f[0] + " " + f[1], nil
}

// receiverPath is stricter than a share folder: the path goes into the
// forced command of an SSH key, which sshd runs through a shell. Only
// characters with no meaning to a shell are allowed.
var receiverPath = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)

func (r *Replication) SetReceivers(ctx context.Context, receivers []sys.Receiver) error {
	for i, rc := range receivers {
		if err := rc.Check(); err != nil {
			return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
		}
		if !receiverPath.MatchString(rc.Path) || strings.Contains(rc.Path, "/..") || strings.Contains(rc.Path, "/./") {
			return fmt.Errorf("%w: receiver %q: use only letters, digits, . _ - and / in the folder name", sys.ErrInvalid, rc.ID)
		}
		for _, other := range receivers[:i] {
			if other.ID == rc.ID || sys.Overlaps(other.Path, rc.Path) {
				return fmt.Errorf("%w: receivers %q and %q overlap", sys.ErrInvalid, other.ID, rc.ID)
			}
		}
		if err := r.checkFolder(rc.Path); err != nil {
			return err
		}
	}
	rrsync := firstExisting(rrsyncPaths)
	if len(receivers) > 0 {
		if rrsync == "" {
			return fmt.Errorf("%w: rrsync is not installed (it comes with rsync 3.2.4 or newer: apt install rsync)", sys.ErrNotFound)
		}
		if firstExisting([]string{"/usr/sbin/sshd"}) == "" {
			return fmt.Errorf("%w: no SSH server (on Debian and Ubuntu: apt install openssh-server)", sys.ErrNotFound)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	keysFile := filepath.Join(r.home, ".ssh", "authorized_keys")
	r.saveReceiverFolders(receivers)
	if len(receivers) == 0 {
		if _, err := os.Stat(keysFile); err == nil {
			return writeAtomic(keysFile, "")
		}
		return nil
	}
	uid, gid, err := r.ensureAccount(ctx)
	if err != nil {
		return err
	}
	for _, rc := range receivers {
		if err := receiverFolder(rc.Path, uid, gid); err != nil {
			return err
		}
	}
	var b strings.Builder
	b.WriteString("# Managed by NetShare Hub (replication receivers). Do not edit.\n")
	for _, rc := range receivers {
		// restrict: no shell, no forwarding, no pty. The command is the only
		// thing the key can run, whatever the sender asks for.
		fmt.Fprintf(&b, "restrict,command=\"%s -wo %s\" %s netshare-hub:%s\n", rrsync, rc.Path, rc.PublicKey, rc.ID)
	}
	if err := os.MkdirAll(filepath.Join(r.home, ".ssh"), 0o755); err != nil {
		return err
	}
	for _, d := range []string{r.home, filepath.Join(r.home, ".ssh")} {
		if err := os.Chown(d, 0, 0); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o755); err != nil {
			return err
		}
	}
	return writeAtomic(keysFile, b.String())
}

func (r *Replication) receiversFile() string { return filepath.Join(r.dir, "receivers.json") }

func (r *Replication) saveReceiverFolders(receivers []sys.Receiver) {
	paths := []string{}
	for _, rc := range receivers {
		paths = append(paths, rc.Path)
	}
	raw, _ := json.Marshal(paths) // plain strings: cannot fail
	if err := r.ensureDir(); err == nil {
		_ = writeAtomicMode(r.receiversFile(), string(raw), 0o600)
	}
}

// ReservedFolders are the receiving folders: no share may expose them.
func (r *Replication) ReservedFolders() []string {
	raw, err := os.ReadFile(r.receiversFile())
	if err != nil {
		return nil
	}
	var paths []string
	_ = json.Unmarshal(raw, &paths)
	return paths
}

// checkFolder refuses a destination or receiving folder that is, or holds,
// a share or a protected folder: a copy must never land inside a share
// (it would bypass the share's access) or overwrite NetShare Hub's data.
func (r *Replication) checkFolder(path string) error {
	paths := []string{path}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		paths = append(paths, filepath.ToSlash(real))
	}
	shares, err := r.samba.applied()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if !pathAllowed(p, append(r.protected, r.dir, r.home)) {
			return fmt.Errorf("%w: %s is a protected or system folder", sys.ErrInvalid, path)
		}
		for _, sh := range shares {
			if sys.Overlaps(p, sh.Path) {
				return fmt.Errorf("%w: %s overlaps share %q", sys.ErrInvalid, path, sh.Name)
			}
		}
	}
	return nil
}

// ensureAccount creates the receiving account if needed. An account with
// that name that is not a system account (or that NetShare Hub manages as
// a user) is refused: it is not ours.
func (r *Replication) ensureAccount(ctx context.Context) (uid, gid int, err error) {
	lookup := func() (bool, error) {
		res, err := r.runner.Run(ctx, Command{Name: "getent", Args: []string{"passwd", sys.ReplicaAccount}})
		if err != nil {
			return false, err
		}
		if res.ExitCode == 2 {
			return false, nil
		}
		f := strings.Split(strings.TrimSpace(res.Stdout), ":")
		if res.ExitCode != 0 || len(f) < 4 {
			return false, fmt.Errorf("unexpected getent output for %s", sys.ReplicaAccount)
		}
		if uid, err = strconv.Atoi(f[2]); err == nil {
			gid, err = strconv.Atoi(f[3])
		}
		return true, err
	}
	found, err := lookup()
	if err != nil {
		return 0, 0, err
	}
	if !found {
		// A system account with a shell (sshd runs the forced command
		// through it) and no password ("*": not locked, so key logins work,
		// but no password can ever match).
		if _, err := run(ctx, r.runner, "useradd", "--system", "--home-dir", r.home, "--no-create-home",
			"--shell", "/bin/sh", "--password", "*", "--comment", "NetShare Hub replication receiver", sys.ReplicaAccount); err != nil {
			return 0, 0, err
		}
		if found, err = lookup(); err != nil || !found {
			return 0, 0, fmt.Errorf("the account %s was not created: %v", sys.ReplicaAccount, err)
		}
	}
	if uid == 0 || uid >= sys.MinManagedUID {
		return 0, 0, fmt.Errorf("%w: the account %s exists and is not a system account; NetShare Hub will not use it", sys.ErrInvalid, sys.ReplicaAccount)
	}
	return uid, gid, nil
}

// receiverFolder creates a receiving folder (its parent must exist), or
// accepts one that is already the receiving account's.
func receiverFolder(path string, uid, gid int) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(path, 0o750); err != nil {
			return fmt.Errorf("create receiving folder %s: %w", path, err)
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
		return os.Chmod(path, 0o750)
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%w: %s is not a folder", sys.ErrInvalid, path)
	}
	if owner, ok := fileOwner(info); !ok || owner != uid {
		return fmt.Errorf("%w: %s already exists and is not a receiving folder: choose a new folder", sys.ErrInvalid, path)
	}
	return nil
}

// --- running rsync ---

func (r *Replication) Replicate(ctx context.Context, run sys.ReplicationRun, progress func(sys.ReplicationProgress)) (sys.ReplicationStats, error) {
	if err := run.Check(); err != nil {
		return sys.ReplicationStats{}, fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	shares, err := r.samba.applied()
	if err != nil {
		return sys.ReplicationStats{}, err
	}
	var source *sys.ShareSpec
	for i := range shares {
		if strings.EqualFold(shares[i].Name, run.Share) {
			source = &shares[i]
		}
	}
	if source == nil {
		return sys.ReplicationStats{}, fmt.Errorf("%w: no share named %q", sys.ErrNotFound, run.Share)
	}
	if err := r.samba.checkPath(*source); err != nil {
		return sys.ReplicationStats{}, err
	}

	args := []string{"--stats", "--no-human-readable", "--exclude=" + sys.UploadTempPattern}
	var dest string
	if run.LocalPath != "" {
		if err := r.checkFolder(run.LocalPath); err != nil {
			return sys.ReplicationStats{}, err
		}
		if err := r.localTarget(run.LocalPath); err != nil {
			return sys.ReplicationStats{}, err
		}
		// Root on both sides: keep owners, groups, ACLs and xattrs, so the
		// copy can be shared again as it was.
		args = append(args, "-aHAX", "--numeric-ids")
		dest = run.LocalPath + "/"
	} else {
		p, err := r.peer(run.Peer)
		if err != nil {
			return sys.ReplicationStats{}, err
		}
		if _, err := os.Stat(r.keyFile()); err != nil {
			return sys.ReplicationStats{}, fmt.Errorf("%w: this machine has no replication key yet", sys.ErrNotFound)
		}
		// The receiver is an unprivileged account: no owners or ACLs.
		ssh := strings.Join([]string{"ssh", "-i", r.keyFile(), "-l", sys.ReplicaAccount, "-p", strconv.Itoa(p.Port),
			"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
			"-o", "UserKnownHostsFile=" + r.knownHosts(p.ID), "-o", "GlobalKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=30"}, " ")
		args = append(args, "-rltH", "--chmod=D0750,F0640", "-e", ssh)
		host := p.Host
		if a, err := netip.ParseAddr(host); err == nil && a.Is6() {
			host = "[" + host + "]"
		}
		dest = host + ":"
	}
	if run.Mirror {
		// Excluded files at the destination are kept (no --delete-excluded).
		args = append(args, "--delete")
	}
	if run.BandwidthKiB > 0 {
		args = append(args, "--bwlimit="+strconv.Itoa(run.BandwidthKiB))
	}
	for _, p := range run.Excludes {
		args = append(args, "--exclude="+p)
	}
	if run.DryRun {
		args = append(args, "--dry-run", "--itemize-changes", "--out-format=%i %n%L")
	} else {
		args = append(args, "--info=progress2", "--no-inc-recursive")
	}
	args = append(args, source.Path+"/", dest)

	var stats sys.ReplicationStats
	var tail []string
	exit, err := stream(ctx, "rsync", args, func(line string) {
		if parseStatsLine(line, &stats) {
			return
		}
		if run.DryRun && itemized.MatchString(line) {
			if len(stats.Changes) < sys.MaxChanges {
				stats.Changes = append(stats.Changes, line)
			} else {
				stats.Truncated = true
			}
			return
		}
		if m := progress2.FindStringSubmatch(line); m != nil {
			if progress != nil {
				b, _ := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
				pct, _ := strconv.Atoi(m[2])
				progress(sys.ReplicationProgress{Bytes: b, Percent: pct})
			}
			return
		}
		if line = strings.TrimSpace(line); line != "" {
			tail = append(tail, line)
			if len(tail) > 20 {
				tail = tail[1:]
			}
		}
	})
	if err != nil {
		return stats, err
	}
	if exit != 0 {
		return stats, &sys.ReplicationError{ExitCode: exit, Message: truncate(strings.Join(tail, "\n"), 2000)}
	}
	return stats, nil
}

// localTarget makes sure a local destination exists: an existing folder is
// used as it is; a new one is root's (0700) until the copy fills it.
func (r *Replication) localTarget(path string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("create destination %s: %w", path, err)
		}
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%w: %s is not a folder", sys.ErrInvalid, path)
	}
	return nil
}

var (
	// progress2: "  1,234,567  45%   10.00MB/s    0:00:10 (xfr#3, to-chk=10/20)"
	progress2 = regexp.MustCompile(`^\s*([\d,]+)\s+(\d{1,3})%`)
	// itemized: ">f+++++++++ path", "cd..t...... dir/", "*deleting   path"
	itemized = regexp.MustCompile(`^([<>ch.][fdLDS][^ ]{9} |\*deleting )`)
	statLine = regexp.MustCompile(`^(Number of files|Number of created files|Number of deleted files|Number of regular files transferred|Total transferred file size): ([\d,]+)`)
)

func parseStatsLine(line string, st *sys.ReplicationStats) bool {
	m := statLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return false
	}
	n, _ := strconv.ParseInt(strings.ReplaceAll(m[2], ",", ""), 10, 64)
	switch m[1] {
	case "Number of files":
		st.Files = n
	case "Number of created files":
		st.Created = n
	case "Number of deleted files":
		st.Deleted = n
	case "Number of regular files transferred":
		st.Transferred = n
	case "Total transferred file size":
		st.Bytes = n
	}
	return true
}

// stream runs an allow-listed program until it ends or ctx is done, and
// calls onLine for each line of its output (stdout and stderr; rsync's
// progress lines end in \r). Like ExecRunner: no shell, fixed environment,
// logged. There is no time limit: a copy takes as long as it takes.
func stream(ctx context.Context, name string, args []string, onLine func(string)) (int, error) {
	path := firstExisting(allowed[name])
	if path == "" {
		if _, ok := allowed[name]; !ok {
			return 0, fmt.Errorf("program %q is not on the allow-list", name)
		}
		return 0, fmt.Errorf("%w: %s is not installed", sys.ErrNotFound, name)
	}
	c := exec.CommandContext(ctx, path, args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C", "HOME=/root"}
	c.Stdin = nil
	c.WaitDelay = 5 * time.Second
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	start := time.Now()
	if err := c.Start(); err != nil {
		return 0, fmt.Errorf("run %s: %w", name, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sc.Split(splitLines)
		for sc.Scan() {
			onLine(sc.Text())
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	err := c.Wait()
	pw.Close()
	<-done
	exit := 0
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		err = ctx.Err()
	case errors.As(err, &ee):
		exit, err = ee.ExitCode(), nil
	}
	slog.Log(ctx, map[bool]slog.Level{true: slog.LevelInfo, false: slog.LevelWarn}[err == nil && exit == 0], "os command",
		"program", name, "args", args, "exit_code", exit, "duration_ms", time.Since(start).Milliseconds(), "err", err)
	return exit, err
}

// splitLines splits on \n and \r.
func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
