package sys

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Replication (step 4.1) copies a share, one way, with rsync: to a folder
// on this machine (a second disk), or to another NetShare Hub over SSH.
//
// The root side takes names, never paths or hosts, from the caller where it
// can: the source is a SHARE NAME (its folder comes from the applied Samba
// config), and a remote destination is a PEER ID (its address and pinned
// host key come from the peer list the root side keeps). Only a local
// destination is a path, and it is checked like a share folder and may not
// overlap any share or protected folder.
//
// A remote NetShare Hub receives into one folder per pairing, through one
// unprivileged account (ReplicaAccount) whose every SSH key is forced to
// "rrsync -wo <folder>": a paired sender can write into its folder and do
// nothing else (no shell, no reading back, no other folder).
//
// Accepted risk (a security decision of step 4.1): with the replication
// module on, a compromised web server could pair a host it controls and
// copy shares to it. That is why the module is off by default.

// ReplicaAccount is the account a remote sender writes as. It is a system
// account (not a NetShare Hub user): it cannot sign in to SMB or the web.
const ReplicaAccount = "nsh-replica"

var peerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidPeerID reports whether id names a peer or receiver.
func ValidPeerID(id string) bool { return peerID.MatchString(id) }

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*$`)

// ValidHost reports whether h is an IP address or a DNS name (never
// anything ssh could read as an option or a user@host).
func ValidHost(h string) bool {
	if len(h) > 253 || h == "" {
		return false
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Zone() == "" && !a.IsUnspecified()
	}
	return hostName.MatchString(h) && !strings.HasPrefix(h, "-")
}

// ValidSSHKey reports whether k is one ed25519 public key line, without a
// comment or options: "ssh-ed25519 <base64>".
func ValidSSHKey(k string) bool {
	f := strings.Split(k, " ")
	if len(f) != 2 || f[0] != "ssh-ed25519" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(f[1])
	// The wire form: string "ssh-ed25519" + string(32-byte key).
	return err == nil && len(raw) == 51 && string(raw[4:15]) == "ssh-ed25519"
}

// MaxExcludes bounds a job's exclude patterns.
const MaxExcludes = 64

// ValidExclude reports whether p can be passed to rsync as one exclude
// pattern: printable, one line, and not an option.
func ValidExclude(p string) bool {
	if p == "" || len(p) > 200 || !utf8.ValidString(p) || strings.HasPrefix(p, "-") || p != strings.TrimSpace(p) {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// MaxBandwidthKiB is the largest bandwidth limit (about 10 GB/s).
const MaxBandwidthKiB = 10_000_000

// ReplicationRun is one run of a replication job.
type ReplicationRun struct {
	// Share is the source: a share in the applied Samba config.
	Share string `json:"share"`
	// LocalPath is a folder on this machine, or Peer a paired NetShare Hub
	// (exactly one of them).
	LocalPath string `json:"localPath,omitempty"`
	Peer      string `json:"peer,omitempty"`
	// Mirror deletes files at the destination that are gone at the source;
	// otherwise deleted files are kept there.
	Mirror       bool     `json:"mirror,omitempty"`
	BandwidthKiB int      `json:"bandwidthKiB,omitempty"`
	Excludes     []string `json:"excludes,omitempty"`
	// DryRun changes nothing and lists what would change.
	DryRun bool `json:"dryRun,omitempty"`
}

// Check validates a run request (the folder rules are the root side's).
func (r ReplicationRun) Check() error {
	if !ValidShareName(r.Share) {
		return fmt.Errorf("unsafe share name %q", r.Share)
	}
	switch {
	case r.LocalPath != "" && r.Peer != "":
		return fmt.Errorf("a run has one destination")
	case r.LocalPath != "":
		if !ValidSharePath(r.LocalPath) || !SharablePath(r.LocalPath) {
			return fmt.Errorf("unsafe destination folder %q", r.LocalPath)
		}
	case r.Peer != "":
		if !ValidPeerID(r.Peer) {
			return fmt.Errorf("unsafe peer id %q", r.Peer)
		}
	default:
		return fmt.Errorf("a run needs a destination")
	}
	if r.BandwidthKiB < 0 || r.BandwidthKiB > MaxBandwidthKiB {
		return fmt.Errorf("invalid bandwidth limit")
	}
	if len(r.Excludes) > MaxExcludes {
		return fmt.Errorf("too many exclude patterns")
	}
	for _, p := range r.Excludes {
		if !ValidExclude(p) {
			return fmt.Errorf("unsafe exclude pattern %q", p)
		}
	}
	return nil
}

// ReplicationProgress is reported while a run copies.
type ReplicationProgress struct {
	Bytes   int64 `json:"bytes"`
	Percent int   `json:"percent"`
}

// MaxChanges bounds the list of changes a dry run returns.
const MaxChanges = 1000

// ReplicationStats is how a run ended.
type ReplicationStats struct {
	Files       int64 `json:"files"`       // files at the source
	Transferred int64 `json:"transferred"` // files copied (or that would be)
	Created     int64 `json:"created"`     // new files and folders
	Deleted     int64 `json:"deleted"`     // removed at the destination (mirror)
	Bytes       int64 `json:"bytes"`       // size of the files copied
	// Changes lists what changed (or would), as rsync itemizes it, capped
	// at MaxChanges; Truncated says there were more.
	Changes   []string `json:"changes,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Peer is a NetShare Hub this machine sends to.
type Peer struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port"`
	// HostKey is the peer's SSH host key, pinned at pairing: a server that
	// cannot prove it holds it is never sent anything.
	HostKey string `json:"hostKey"`
}

// Check validates a peer.
func (p Peer) Check() error {
	if !ValidPeerID(p.ID) {
		return fmt.Errorf("unsafe peer id %q", p.ID)
	}
	if !ValidHost(p.Host) {
		return fmt.Errorf("peer %q: unsafe host", p.ID)
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("peer %q: invalid port", p.ID)
	}
	if !ValidSSHKey(p.HostKey) {
		return fmt.Errorf("peer %q: invalid host key", p.ID)
	}
	return nil
}

// Receiver is a folder this machine receives one sender's copies into.
type Receiver struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// PublicKey is the sender's key; the only thing it can do is write
	// into Path.
	PublicKey string `json:"publicKey"`
}

// Check validates a receiver (the folder rules are the root side's).
func (r Receiver) Check() error {
	if !ValidPeerID(r.ID) {
		return fmt.Errorf("unsafe receiver id %q", r.ID)
	}
	if !ValidSharePath(r.Path) || !SharablePath(r.Path) {
		return fmt.Errorf("receiver %q: unsafe folder", r.ID)
	}
	if !ValidSSHKey(r.PublicKey) {
		return fmt.Errorf("receiver %q: invalid key", r.ID)
	}
	return nil
}

// ReplicationStatus says what this machine can do.
type ReplicationStatus struct {
	// Rsync is installed (needed for everything).
	Rsync bool `json:"rsync"`
	// SSHClient is installed (sending to a peer).
	SSHClient bool `json:"sshClient"`
	// SSHServer and Rrsync are installed (receiving from a peer).
	SSHServer bool `json:"sshServer"`
	Rrsync    bool `json:"rrsync"`
}

// Replication copies shares and manages the SSH keys of pairings.
type Replication interface {
	ReplicationStatus(ctx context.Context) (ReplicationStatus, error)
	// Replicate runs rsync. progress is called while it copies (not for a
	// dry run). It stops when ctx is done.
	// Errors: ErrInvalid, ErrNotFound (no such share or peer, or rsync /
	// ssh not installed), *ReplicationError (rsync failed).
	Replicate(ctx context.Context, run ReplicationRun, progress func(ReplicationProgress)) (ReplicationStats, error)
	// PublicKey returns this machine's replication key (an ed25519 key
	// made on first use, kept by the root side; the private half never
	// leaves it).
	PublicKey(ctx context.Context) (string, error)
	// SetPeers replaces the list of peers (and their pinned host keys).
	SetPeers(ctx context.Context, peers []Peer) error
	// HostKey returns this machine's SSH host key (what a sender pins).
	// Errors: ErrNotFound (no SSH server).
	HostKey(ctx context.Context) (string, error)
	// SetReceivers replaces the list of receiving folders and the keys
	// that may write into them. Each folder is created if needed (owned by
	// ReplicaAccount, 0750). Errors: ErrInvalid, ErrNotFound (no SSH
	// server or rrsync).
	SetReceivers(ctx context.Context, receivers []Receiver) error
}

// ReplicationError is a run that rsync could not finish.
type ReplicationError struct {
	ExitCode int
	// Message is rsync's own explanation, for the logs and the admin.
	Message string
}

func (e *ReplicationError) Error() string {
	return fmt.Sprintf("rsync failed (exit code %d): %s", e.ExitCode, e.Message)
}

// Partial reports whether rsync copied everything it could but some files
// failed (vanished during the copy, or could not be read).
func (e *ReplicationError) Partial() bool { return e.ExitCode == 23 || e.ExitCode == 24 }
