package sys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// NFS exports a live share to other computers over NFS (step 4.2).
//
// NFS (with sec=sys, the only kind that works without Kerberos) does not
// sign anyone in: a client says which user it is, and the server believes
// it. So the rules here are stricter than for SMB:
//
//   - An export always names the computers allowed to mount it (addresses
//     or CIDR ranges). There is no "everyone".
//   - The root side takes only the SHARE NAME from the caller. The folder,
//     and who may read or write it, come from the applied Samba config, the
//     single record of live shares. NFS can never reach a folder that is not
//     a live share, or give more than the share gives.
//   - Root on a client is always mapped to nobody (root_squash). There is no
//     way to turn that off.
//   - By default every request from the clients acts as ONE NetShare Hub
//     account (all_squash to its UID): the clients get exactly what that
//     user gets on the share, whatever user they claim to be. Trusting the
//     clients' own user ids (AsUser empty) is possible, for Linux machines
//     with the same accounts that are trusted as much as the server itself:
//     the folder's ACL then decides per user id.

// NFSClient is one computer (or range) that may mount an export.
type NFSClient struct {
	// Address is an IP address or a CIDR range, canonical (see
	// ParseHostRule): never a host name or a wildcard.
	Address string `json:"address"`
	// ReadOnly exports read-only to this client, whatever the share allows.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// NFSExport is what the web server asks for.
type NFSExport struct {
	// Share is the name of a live share in the applied Samba config.
	Share   string      `json:"share"`
	Clients []NFSClient `json:"clients"`
	// AsUser is the managed account every request acts as. Empty trusts the
	// clients' own user ids (root is still mapped to nobody).
	AsUser string `json:"asUser,omitempty"`
}

// MaxNFSClients bounds the client list of one export.
const MaxNFSClients = 64

// Check validates an export request.
func (e NFSExport) Check() error {
	if !ValidShareName(e.Share) {
		return fmt.Errorf("unsafe share name %q", e.Share)
	}
	if e.AsUser != "" && !ValidAccountName(e.AsUser) {
		return fmt.Errorf("export %q: unsafe account name %q", e.Share, e.AsUser)
	}
	if len(e.Clients) == 0 {
		return fmt.Errorf("export %q: no clients (an NFS export always names the computers that may mount it)", e.Share)
	}
	if len(e.Clients) > MaxNFSClients {
		return fmt.Errorf("export %q: too many clients", e.Share)
	}
	seen := map[string]bool{}
	for _, c := range e.Clients {
		norm, err := ParseHostRule(c.Address)
		if err != nil || norm != c.Address {
			return fmt.Errorf("export %q: invalid client address %q", e.Share, c.Address)
		}
		if p, err := netip.ParsePrefix(norm); err == nil && p.Bits() == 0 {
			return fmt.Errorf("export %q: %q means every address", e.Share, c.Address)
		}
		if seen[norm] {
			return fmt.Errorf("export %q: client %q listed twice", e.Share, c.Address)
		}
		seen[norm] = true
	}
	return nil
}

// ResolvedExport is an export with everything the root side looked up
// itself: the folder, and what the share allows.
type ResolvedExport struct {
	Share string
	Path  string
	// Writable is false when nobody may change files through this export:
	// the user it acts as only reads the share, or (trusting client ids) the
	// share has no writers at all.
	Writable bool
	// SquashUID/SquashGID are the account every request acts as (AsUser);
	// -1 when the clients' own ids are trusted.
	SquashUID, SquashGID int
	Clients              []NFSClient
}

// ResolveExport checks an export against the applied share and decides what
// it may do. uid and gid are AsUser's ids (ignored when AsUser is empty).
func ResolveExport(e NFSExport, share ShareSpec, uid, gid int) (ResolvedExport, error) {
	if err := e.Check(); err != nil {
		return ResolvedExport{}, err
	}
	if !share.Live() {
		return ResolvedExport{}, fmt.Errorf("share %q is not live", e.Share)
	}
	out := ResolvedExport{Share: share.Name, Path: share.Path, SquashUID: -1, SquashGID: -1, Clients: e.Clients}
	if e.AsUser == "" {
		out.Writable = len(share.Writers) > 0
		return out, nil
	}
	if uid < MinManagedUID || gid < 0 {
		return ResolvedExport{}, fmt.Errorf("export %q: %q is not a managed account", e.Share, e.AsUser)
	}
	switch {
	case slices.Contains(share.Writers, e.AsUser):
		out.Writable = true
	case slices.Contains(share.Readers, e.AsUser):
	default:
		// Upload-only users cannot be offered over NFS (a drop box hides the
		// folder, which an NFS mount cannot), and guests are not accounts.
		return ResolvedExport{}, fmt.Errorf("export %q: %q cannot read the share", e.Share, e.AsUser)
	}
	out.SquashUID, out.SquashGID = uid, gid
	return out, nil
}

// ExportsHeader starts the managed exports file.
const ExportsHeader = `# Managed by NetShare Hub. Do not edit this file: it is rewritten
# every time an NFS export changes. Manage NFS in the web UI instead.
`

// RenderExports builds the managed exports file (exports(5) format). Every
// value comes from checked data; paths are escaped so no character in a
// folder name can end the path or start an option.
func RenderExports(exports []ResolvedExport) (string, error) {
	var b strings.Builder
	b.WriteString(ExportsHeader)
	seen := map[string]bool{}
	for _, e := range exports {
		if !ValidSharePath(e.Path) || !SharablePath(e.Path) {
			return "", fmt.Errorf("export %q: unsafe path", e.Share)
		}
		if seen[e.Path] {
			return "", fmt.Errorf("export %q: folder exported twice", e.Share)
		}
		seen[e.Path] = true
		if err := (NFSExport{Share: e.Share, Clients: e.Clients}).Check(); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n# share: %s\n%s", e.Share, escapeExportPath(e.Path))
		for _, c := range e.Clients {
			b.WriteString(" " + c.Address + "(" + strings.Join(exportOptions(e, c), ",") + ")")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// exportOptions are the fixed options of one client entry. Only these are
// ever written: no option comes from a request.
func exportOptions(e ResolvedExport, c NFSClient) []string {
	mode := "ro"
	if e.Writable && !c.ReadOnly {
		mode = "rw"
	}
	opts := []string{mode, "sync", "no_subtree_check", "secure", "root_squash", "sec=sys", "fsid=" + exportFSID(e.Path)}
	if e.SquashUID >= 0 {
		opts = append(opts, "all_squash", "anonuid="+strconv.Itoa(e.SquashUID), "anongid="+strconv.Itoa(e.SquashGID))
	}
	return opts
}

// exportFSID is a stable id for an export, from its path. NFS needs one for
// filesystems without a device id (overlay, some FUSE and ZFS setups), and
// a fixed one keeps clients' file handles valid across reboots.
func exportFSID(path string) string {
	sum := sha256.Sum256([]byte("netshare-hub nfs:" + path))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// escapeExportPath writes every byte that is not plainly safe as an octal
// escape (\ooo), which exportfs decodes. A space, quote, # or any non-ASCII
// letter in a folder name stays part of the path.
func escapeExportPath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("/._-+", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "\\%03o", c)
	}
	return b.String()
}

// ParseExportShares returns the share names in a managed exports file.
func ParseExportShares(content string) []string {
	var names []string
	for _, line := range strings.Split(content, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "# share: "); ok {
			names = append(names, name)
		}
	}
	return names
}

// NFSStatus is whether this machine can serve NFS.
type NFSStatus struct {
	// Installed: the NFS server tools (exportfs) are present.
	Installed bool `json:"installed"`
	// Running: the kernel NFS server is up.
	Running bool `json:"running"`
	// Supported is false where an NFS server cannot run at all, with the
	// reason: an unprivileged container cannot start the kernel's NFS server.
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	// Exports are the share names exported now.
	Exports []string `json:"exports"`
}

// NFS manages the NFS exports NetShare Hub owns (one exports file).
type NFS interface {
	NFSStatus(ctx context.Context) (NFSStatus, error)
	// ApplyExports makes the NFS server export exactly these shares. Each
	// share's folder and access are taken from the applied Samba config
	// (see NFSExport). The file is replaced atomically and the server told
	// to re-read it; if it refuses, the previous exports are put back.
	// An empty list with no NFS server installed is not an error.
	// Errors: ErrInvalid, ErrNotFound (NFS server not installed or cannot
	// run here), *ConfigError (exportfs refused).
	ApplyExports(ctx context.Context, exports []NFSExport) error
}
