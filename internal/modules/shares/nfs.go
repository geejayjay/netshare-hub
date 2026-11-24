package shares

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// NFS (step 4.2): a share can also be exported over NFS, to the computers
// named in its client list. See sys.NFSExport for why NFS is stricter than
// SMB: it trusts the user id a client claims.

// NFS modes.
const (
	// NFSModeUser: every request from the clients acts as one NetShare Hub
	// user, and gets exactly that user's access to the share.
	NFSModeUser = "user"
	// NFSModeClientIDs: the clients' own user ids are trusted (root is
	// still mapped to nobody). Only for Linux machines with the same
	// accounts, trusted as much as the server itself.
	NFSModeClientIDs = "client-ids"
)

// NFSClient is a computer, or range of computers, that may mount the share.
type NFSClient struct {
	Address  string `json:"address" doc:"An IP address (192.168.1.50) or a range in CIDR form (192.168.1.0/24). IPv4 or IPv6."`
	ReadOnly bool   `json:"readOnly" required:"false" doc:"This client can only read, whatever the share allows."`
}

// NFS is a share's NFS export.
type NFS struct {
	Enabled bool   `json:"enabled" doc:"Export the share over NFS to the listed clients."`
	Mode    string `json:"mode" required:"false" enum:"user,client-ids" doc:"\"user\": the clients act as one user (asUserId) and get that user's access. \"client-ids\": the clients' own user ids are trusted (only for trusted Linux machines with the same accounts); root is always mapped to nobody."`
	// AsUserID is the user every request acts as (mode "user").
	AsUserID   int64       `json:"asUserId,omitempty" doc:"Mode \"user\": the user the clients act as. Must have read or write on the share."`
	AsUsername string      `json:"asUsername,omitempty" readOnly:"true"`
	Clients    []NFSClient `json:"clients" required:"false" nullable:"false" maxItems:"64" doc:"The computers that may mount the share. Required: an NFS export is never open to every address."`
}

func defaultNFS() NFS { return NFS{Mode: NFSModeUser, Clients: []NFSClient{}} }

// Error codes for NFS.
const (
	CodeNFSUnavailable errs.Code = "NFS_UNAVAILABLE"
	CodeNFSRefused     errs.Code = "NFS_CONFIG_REFUSED"
)

const alertNFSFailed = "nfs.apply_failed"

func nfsJSON(n NFS) string {
	n.AsUsername = ""
	if n.Clients == nil {
		n.Clients = []NFSClient{}
	}
	b, _ := json.Marshal(n) // plain data: cannot fail
	return string(b)
}

func parseNFS(raw string) (NFS, error) {
	n := defaultNFS()
	if raw == "" || raw == "{}" {
		return n, nil
	}
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return defaultNFS(), err
	}
	if n.Clients == nil {
		n.Clients = []NFSClient{}
	}
	if n.Mode == "" {
		n.Mode = NFSModeUser
	}
	return n, nil
}

// checkNFS validates a share's NFS settings against its access list. It
// returns the settings in canonical form.
func checkNFS(v *validate.Errors, n NFS, access []AccessInput, r Rules, byID map[int64]users.User) NFS {
	if n.Mode == "" {
		n.Mode = NFSModeUser
	}
	v.Check(n.Mode == NFSModeUser || n.Mode == NFSModeClientIDs, "nfs.mode", "invalid_choice", `Use "user" or "client-ids".`)
	if len(n.Clients) > sys.MaxNFSClients {
		v.Add("nfs.clients", "too_many", fmt.Sprintf("At most %d clients.", sys.MaxNFSClients))
		return n
	}
	seen := map[string]bool{}
	clients := make([]NFSClient, 0, len(n.Clients))
	for i, c := range n.Clients {
		field := fmt.Sprintf("nfs.clients[%d].address", i)
		addr, err := sys.ParseHostRule(c.Address)
		if err != nil {
			v.Add(field, "invalid_format", err.Error())
			continue
		}
		if p, err := netip.ParsePrefix(addr); err == nil && p.Bits() == 0 {
			v.Add(field, "every_address", "This range is every address. Name the computers, or their network (like 192.168.1.0/24).")
			continue
		}
		if seen[addr] {
			v.Add(field, "duplicate", "This address is already listed.")
			continue
		}
		seen[addr] = true
		clients = append(clients, NFSClient{Address: addr, ReadOnly: c.ReadOnly})
	}
	n.Clients = clients
	if n.Mode == NFSModeClientIDs {
		v.Check(n.AsUserID == 0, "nfs.asUserId", "not_with_client_ids", `Leave it empty in mode "client-ids".`)
	}
	if !n.Enabled {
		return n
	}
	if r.TimeMachine {
		v.Add("nfs.enabled", "not_with_time_machine", "A Time Machine share cannot be exported over NFS: Macs keep their backups in a format NFS clients would damage.")
	}
	v.Check(len(n.Clients) > 0 || v.Has("nfs.clients"), "nfs.clients", "required", "Add at least one computer or network that may mount the share.")
	if n.Mode != NFSModeUser {
		return n
	}
	if n.AsUserID == 0 {
		v.Add("nfs.asUserId", "required", "Choose the user NFS clients act as.")
		return n
	}
	idx := slices.IndexFunc(access, func(a AccessInput) bool { return a.UserID == n.AsUserID })
	u, known := byID[n.AsUserID]
	switch {
	case idx < 0:
		v.Add("nfs.asUserId", "not_on_share", "This user has no access to the share. Add them first.")
	case !known || !u.SMBAccess:
		v.Add("nfs.asUserId", "no_smb_access", "This user has no SMB access, so has no account on the server.")
	default:
		if level, _ := configured(access[idx].Level, r); level != LevelRead && level != LevelWrite {
			v.Add("nfs.asUserId", "upload_only", "NFS clients must be able to see the folder: choose a user with read or write.")
		}
	}
	return n
}

// checkNFS is checkNFS, and refuses turning NFS on while the module is off.
func (s *Service) checkNFS(v *validate.Errors, n NFS, access []AccessInput, r Rules, byID map[int64]users.User) NFS {
	n = checkNFS(v, n, access, r, byID)
	if n.Enabled && s.nfs == nil {
		v.Add("nfs.enabled", "module_off", "NFS is switched off on this server ([modules] nfs = true in the config turns it on).")
	}
	return n
}

// nfsExports turns the shares into what the system layer exports. specs are
// the same shares' effective access: a user the ransomware guard blocked
// only reads, over NFS too. An export whose user lost access is skipped
// (and logged); the root side would refuse it anyway.
func nfsExports(ctx context.Context, shares []Share, specs []sys.ShareSpec, byID map[int64]users.User) []sys.NFSExport {
	out := []sys.NFSExport{}
	for i, sh := range shares {
		n := sh.NFS
		if !n.Enabled || len(n.Clients) == 0 {
			continue
		}
		e := sys.NFSExport{Share: sh.Name}
		for _, c := range n.Clients {
			e.Clients = append(e.Clients, sys.NFSClient{Address: c.Address, ReadOnly: c.ReadOnly})
		}
		if n.Mode == NFSModeUser {
			u, ok := byID[n.AsUserID]
			// A disabled user's export is still asked for: the root side
			// leaves it out while the account is disabled, and serves it
			// again once it is enabled (without a share change).
			if !ok || !slices.Contains(specs[i].Readers, u.Username) {
				slog.WarnContext(ctx, "NFS export skipped: its user cannot read the share", "share", sh.Name, "user_id", n.AsUserID)
				continue
			}
			e.AsUser = u.Username
		} else if !specs[i].Live() {
			continue
		}
		out = append(out, e)
	}
	return out
}

// exportNFS exports what the shares say, and returns the system layer's
// error as it is. It runs inside a change's transaction, so it must not
// write to the database (nfsAlert does that after the commit).
func (s *Service) exportNFS(ctx context.Context, shares []Share, specs []sys.ShareSpec) error {
	if s.nfs == nil {
		return nil
	}
	byID, err := s.usersOf(ctx, shares)
	if err != nil {
		return err
	}
	return s.nfs.ApplyExports(ctx, nfsExports(ctx, shares, specs, byID))
}

// nfsError turns an ApplyExports error into the error for the client.
func nfsError(err error) error {
	var cfgErr *sys.ConfigError
	switch {
	case errors.Is(err, sys.ErrNotFound):
		return errs.Conflict(CodeNFSUnavailable, "NFS cannot be served on this machine: "+sys.Message(err)+
			" Nothing was changed.").WithCause(err)
	case errors.As(err, &cfgErr):
		return errs.Invalid(CodeNFSRefused, "The NFS server refused the exports. Nothing was changed. Details are in the log.").WithCause(err)
	default:
		return errs.Internal(fmt.Errorf("apply nfs exports: %w", err))
	}
}

// nfsAlert raises or resolves "NFS could not follow a share change". It is
// called after the change is committed.
func (s *Service) nfsAlert(ctx context.Context, failed error) {
	if s.nfs == nil || s.alerts == nil {
		return
	}
	if failed == nil {
		_ = s.alerts.Resolve(ctx, alertNFSFailed, "")
		return
	}
	slog.ErrorContext(ctx, "the NFS exports could not be updated", "err", failed)
	_, _ = s.alerts.Raise(ctx, alerts.Raise{
		Kind: alertNFSFailed, Severity: alerts.Error, Source: alerts.SourceShares,
		Title:   "NFS exports could not be updated",
		Message: "A share change was saved for SMB, but the NFS server could not be updated, so NFS clients may still see the previous exports. Check that nfs-kernel-server is installed and running.",
	})
}

// dropNFSUser turns NFS off on every share that acts as userID (the user
// is losing SMB access, so there is no account to act as any more).
func (s *Service) dropNFSUser(ctx context.Context, tx *sql.Tx, userID int64) (bool, error) {
	shares, err := s.repo.All(ctx, tx)
	if err != nil {
		return false, err
	}
	changed := false
	for _, sh := range shares {
		if sh.NFS.Mode == NFSModeUser && sh.NFS.AsUserID == userID && (sh.NFS.Enabled || sh.NFS.AsUserID != 0) {
			n := sh.NFS
			n.Enabled, n.AsUserID = false, 0
			if err := s.repo.UpdateNFS(ctx, tx, sh.ID, n, s.now()); err != nil {
				return false, err
			}
			slog.InfoContext(ctx, "NFS export turned off: its user lost access", "share", sh.Name, "user_id", userID)
			changed = true
		}
	}
	return changed, nil
}

// NFSStatus says whether this machine can serve NFS.
type NFSStatus struct {
	Installed bool     `json:"installed" doc:"The NFS server (nfs-kernel-server) is installed."`
	Running   bool     `json:"running" doc:"The kernel NFS server is running."`
	Supported bool     `json:"supported" doc:"False where an NFS server cannot run at all (an unprivileged container)."`
	Hint      string   `json:"hint,omitempty" doc:"What to do when NFS cannot be used yet."`
	Exports   []string `json:"exports" nullable:"false" doc:"The shares exported over NFS right now."`
}

func (s *Service) NFSStatus(ctx context.Context) (NFSStatus, error) {
	if s.nfs == nil {
		return NFSStatus{Hint: "NFS is not offered on this machine.", Exports: []string{}}, nil
	}
	st, err := s.nfs.NFSStatus(ctx)
	if err != nil {
		return NFSStatus{}, err
	}
	out := NFSStatus{Installed: st.Installed, Running: st.Running, Supported: st.Supported, Exports: st.Exports}
	if out.Exports == nil {
		out.Exports = []string{}
	}
	switch {
	case !st.Supported:
		out.Hint = st.Reason
	case !st.Installed:
		out.Hint = "Not installed. On Debian and Ubuntu: apt install nfs-kernel-server"
	case !st.Running:
		out.Hint = "The NFS server is not running. Start it with: systemctl enable --now nfs-server"
	}
	return out, nil
}

type nfsStatusOutput struct {
	Body NFSStatus
}

// RegisterNFS adds the NFS status route.
func RegisterNFS(api huma.API, svc *Service) {
	httpx.Register(api, httpx.Route{ID: "getNFSStatus", Method: http.MethodGet, Path: "/shares/nfs/status",
		Summary: "Whether this machine can serve NFS, and what is exported", Tag: "Shares", Access: httpx.Require(rbac.SharesRead)},
		func(ctx context.Context, _ *struct{}) (*nfsStatusOutput, error) {
			st, err := svc.NFSStatus(ctx)
			if err != nil {
				return nil, err
			}
			return &nfsStatusOutput{Body: st}, nil
		})
}
