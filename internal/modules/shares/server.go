package shares

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Server-wide Samba settings (step 1.16a). They live in the same managed
// config file as the shares and change through the same path (see
// Service.change): validated, rendered, checked by Samba, written in one
// step and reloaded. A setting Samba refuses changes nothing.

// ServerSettings are the server-wide SMB settings, in the API's words.
// "default" (or an empty name) means NetShare Hub leaves the setting to
// smb.conf or Samba's own default.
type ServerSettings struct {
	ServerName   string    `json:"serverName" maxLength:"15" doc:"The server's name on the network (\\\\NAME): 1-15 letters, digits or -. Empty: keep the system's."`
	Workgroup    string    `json:"workgroup" maxLength:"15" doc:"The Windows workgroup: 1-15 letters, digits or -. Empty: keep the system's (usually WORKGROUP)."`
	MinProtocol  string    `json:"minProtocol" enum:"default,SMB2_02,SMB2_10,SMB3_00,SMB3_02,SMB3_11" doc:"The oldest SMB version clients may use. SMB1 is never allowed."`
	MaxProtocol  string    `json:"maxProtocol" enum:"default,SMB2_02,SMB2_10,SMB3_00,SMB3_02,SMB3_11" doc:"The newest SMB version clients may use."`
	Encryption   string    `json:"encryption" enum:"default,off,if-supported,required" doc:"\"if-supported\" encrypts when the client can; \"required\" refuses clients that cannot (needs minProtocol SMB3_00 or newer)."`
	Multichannel string    `json:"multichannel" enum:"default,on,off" doc:"Several network links per client, for speed."`
	ActivityLog  bool      `json:"activityLog" required:"false" doc:"Record who did what over SMB (the activity log). Needs Samba's full_audit module (Debian, Ubuntu: samba-vfs-modules)."`
	MacSupport   bool      `json:"macSupport" required:"false" doc:"Apple's SMB extensions on every share: faster folders and correct file details on Macs. Needs Samba's fruit, catia and streams_xattr modules (Debian, Ubuntu: samba-vfs-modules). On while any share is a Time Machine share."`
	UpdatedAt    time.Time `json:"updatedAt,omitzero"`
}

// toSys turns API values into the system layer's.
func (s ServerSettings) toSys() sys.ServerSettings {
	val := func(v string) string {
		if v == "default" {
			return ""
		}
		return v
	}
	out := sys.ServerSettings{
		NetbiosName: strings.TrimSpace(s.ServerName), Workgroup: strings.TrimSpace(s.Workgroup),
		MinProtocol: val(s.MinProtocol), MaxProtocol: val(s.MaxProtocol),
		Encryption: val(s.Encryption), Multichannel: val(s.Multichannel),
		MacSupport: s.MacSupport,
	}
	switch out.Encryption {
	case "if-supported":
		out.Encryption = "desired"
	}
	switch out.Multichannel {
	case "on":
		out.Multichannel = "yes"
	case "off":
		out.Multichannel = "no"
	}
	return out
}

func fromSys(s sys.ServerSettings, activity bool, updated time.Time) ServerSettings {
	def := func(v string) string {
		if v == "" {
			return "default"
		}
		return v
	}
	out := ServerSettings{
		ServerName: s.NetbiosName, Workgroup: s.Workgroup,
		MinProtocol: def(s.MinProtocol), MaxProtocol: def(s.MaxProtocol),
		Encryption: def(s.Encryption), Multichannel: def(s.Multichannel),
		ActivityLog: activity, MacSupport: s.MacSupport,
	}
	if out.Encryption == "desired" {
		out.Encryption = "if-supported"
	}
	switch out.Multichannel {
	case "yes":
		out.Multichannel = "on"
	case "no":
		out.Multichannel = "off"
	}
	if updated.Unix() > 0 {
		out.UpdatedAt = updated
	}
	return out
}

// --- storage ---

// serverSettings returns the server-wide settings and whether the activity
// log is on.
func (r *Repo) serverSettings(ctx context.Context, q querier) (sys.ServerSettings, bool, time.Time, error) {
	var s sys.ServerSettings
	var activity bool
	var updated int64
	err := q.QueryRowContext(ctx, `SELECT netbios_name, workgroup, min_protocol, max_protocol, encryption, multichannel,
		mac_support, activity_log, updated_at FROM samba_server_settings WHERE id = 1`).
		Scan(&s.NetbiosName, &s.Workgroup, &s.MinProtocol, &s.MaxProtocol, &s.Encryption, &s.Multichannel, &s.MacSupport,
			&activity, &updated)
	if err == sql.ErrNoRows {
		return sys.ServerSettings{}, false, time.Time{}, nil
	}
	return s, activity, time.Unix(updated, 0).UTC(), err
}

func (r *Repo) saveServerSettings(ctx context.Context, q querier, s sys.ServerSettings, activity bool, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO samba_server_settings
		(id, netbios_name, workgroup, min_protocol, max_protocol, encryption, multichannel, mac_support, activity_log, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET netbios_name = excluded.netbios_name, workgroup = excluded.workgroup,
			min_protocol = excluded.min_protocol, max_protocol = excluded.max_protocol,
			encryption = excluded.encryption, multichannel = excluded.multichannel, mac_support = excluded.mac_support,
			activity_log = excluded.activity_log, updated_at = excluded.updated_at`,
		s.NetbiosName, s.Workgroup, s.MinProtocol, s.MaxProtocol, s.Encryption, s.Multichannel, s.MacSupport, activity, now.Unix())
	return err
}

// --- rules ---

// ServerSettings returns the server-wide settings.
func (s *Service) ServerSettings(ctx context.Context) (ServerSettings, error) {
	settings, activity, updated, err := s.repo.serverSettings(ctx, s.repo.db.Read)
	if err != nil {
		return ServerSettings{}, err
	}
	return fromSys(settings, activity, updated), nil
}

// UpdateServerSettings replaces the server-wide settings. They take effect
// for new connections; clients already connected keep theirs.
func (s *Service) UpdateServerSettings(ctx context.Context, in ServerSettings) (ServerSettings, error) {
	settings := in.toSys()
	v := validate.New()
	v.Check(settings.NetbiosName == "" || sys.ValidNetbiosName(settings.NetbiosName), "serverName", "invalid_format",
		"Use 1-15 letters, digits or -, not starting or ending with -, and not only digits.")
	v.Check(settings.Workgroup == "" || sys.ValidNetbiosName(settings.Workgroup), "workgroup", "invalid_format",
		"Use 1-15 letters, digits or -, not starting or ending with -, and not only digits.")
	minIdx, maxIdx := slices.Index(sys.Protocols, settings.MinProtocol), slices.Index(sys.Protocols, settings.MaxProtocol)
	if minIdx >= 0 && maxIdx >= 0 && minIdx > maxIdx {
		v.Add("maxProtocol", "older_than_min", "The newest version allowed cannot be older than the oldest.")
	}
	if settings.Encryption == "required" && minIdx >= 0 && minIdx < slices.Index(sys.Protocols, "SMB3_00") {
		v.Add("encryption", "needs_smb3",
			"Encryption needs SMB 3. Set the oldest version to SMB3_00 or newer, or clients on older versions connect and then cannot open anything.")
	}
	if err := v.Err(); err != nil {
		return ServerSettings{}, err
	}
	err := s.change(ctx, func(tx *sql.Tx) (bool, error) {
		if !settings.MacSupport {
			// Checked inside the change, so a Time Machine share cannot be
			// added in between.
			tm, err := s.repo.timeMachineShares(ctx, tx)
			if err != nil {
				return false, err
			}
			if len(tm) > 0 {
				v := validate.New()
				v.Add("macSupport", "needed_by_time_machine", timeMachineNeedsMacSupport+" ("+strings.Join(tm, ", ")+")")
				return false, v.Err()
			}
		}
		return true, s.repo.saveServerSettings(ctx, tx, settings, in.ActivityLog, s.now())
	})
	var refused *sys.ConfigError
	if errors.As(err, &refused) {
		// Everything is validated first, so Samba refusing it means this
		// Samba version does not accept one of the settings. Nothing
		// changed. Samba's own words go to the log, not to the client.
		slog.WarnContext(ctx, "samba refused the server settings", "output", refused.Output)
		return ServerSettings{}, errs.Invalid(CodeSambaConfigRefused,
			"Samba refused these settings (this Samba version may not support one of them), so nothing was changed. The server log has Samba's message.").
			WithCause(err)
	}
	if err != nil {
		// A failed reload kept the change (CodeSambaReloadFailed).
		return ServerSettings{}, err
	}
	return s.ServerSettings(ctx)
}
