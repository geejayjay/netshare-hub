package sys

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// SambaConfig is everything NetShare Hub puts in its Samba config: the
// server-wide settings and the shares. It is applied as one unit, so a
// change to either goes through the same checks, and Samba never sees half
// of a change.
type SambaConfig struct {
	Server ServerSettings `json:"server"`
	Shares []ShareSpec    `json:"shares"`
	// Audit turns on the activity log: Samba's full_audit module on every
	// share, and the audit log levels (see AuditOptions).
	Audit bool `json:"audit,omitempty"`
}

// The activity log's Samba settings. Only the operations the activity log
// and the ransomware guard use are logged (Samba cannot log only the
// "open for writing" kind of openat, so reads are dropped by the reader).
// Logins come from auth_json_audit (brute-force protection, 2.4).
const (
	AuditPrefix   = "nshaudit|%u|%I|%S|%m"
	AuditSuccess  = "connect disconnect mkdirat renameat unlinkat openat"
	AuditFailure  = "connect mkdirat renameat unlinkat"
	AuditLogLevel = "0 full_audit:1 auth_json_audit:3"
)

// ServerSettings are the server-wide Samba settings NetShare Hub manages.
// An empty value means "not managed": the admin's smb.conf (or Samba's
// default) applies. Set values override smb.conf, because the managed
// config is included at its end.
type ServerSettings struct {
	// NetbiosName is the server's name on the network (\\NAME), and
	// Workgroup the Windows workgroup. Both: 1 to 15 letters, digits or -.
	NetbiosName string `json:"netbiosName,omitempty"`
	Workgroup   string `json:"workgroup,omitempty"`
	// MinProtocol and MaxProtocol limit the SMB versions clients may use:
	// one of Protocols. SMB1 is never offered.
	MinProtocol string `json:"minProtocol,omitempty"`
	MaxProtocol string `json:"maxProtocol,omitempty"`
	// Encryption is Samba's "server smb encrypt": off, desired (used when
	// the client supports it) or required (clients without it are refused).
	Encryption string `json:"encryption,omitempty"`
	// Multichannel is "yes" or "no" (several network links per client).
	Multichannel string `json:"multichannel,omitempty"`
}

// Protocols are the SMB versions that may be set, oldest first.
var Protocols = []string{"SMB2_02", "SMB2_10", "SMB3_00", "SMB3_02", "SMB3_11"}

// EncryptionModes are the values of ServerSettings.Encryption.
var EncryptionModes = []string{"off", "desired", "required"}

var netbiosName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,13}[A-Za-z0-9])?$`)

// ValidNetbiosName reports whether s can be a server name or workgroup.
// A name of digits only is refused: Windows takes it for an address.
func ValidNetbiosName(s string) bool {
	return netbiosName.MatchString(s) && strings.Trim(s, "0123456789") != ""
}

// Check validates every value. The renderer and the root agent rely on it.
func (s ServerSettings) Check() error {
	if s.NetbiosName != "" && !ValidNetbiosName(s.NetbiosName) {
		return fmt.Errorf("unsafe netbios name %q", s.NetbiosName)
	}
	if s.Workgroup != "" && !ValidNetbiosName(s.Workgroup) {
		return fmt.Errorf("unsafe workgroup %q", s.Workgroup)
	}
	minIdx, maxIdx := slices.Index(Protocols, s.MinProtocol), slices.Index(Protocols, s.MaxProtocol)
	if s.MinProtocol != "" && minIdx < 0 {
		return fmt.Errorf("unknown min protocol %q", s.MinProtocol)
	}
	if s.MaxProtocol != "" && maxIdx < 0 {
		return fmt.Errorf("unknown max protocol %q", s.MaxProtocol)
	}
	if minIdx >= 0 && maxIdx >= 0 && minIdx > maxIdx {
		return fmt.Errorf("min protocol %s is newer than max protocol %s", s.MinProtocol, s.MaxProtocol)
	}
	if s.Encryption != "" && !slices.Contains(EncryptionModes, s.Encryption) {
		return fmt.Errorf("unknown encryption mode %q", s.Encryption)
	}
	// Encryption exists from SMB 3.0 on; requiring it while allowing older
	// versions lets those clients connect and then fail on every share.
	if s.Encryption == "required" && minIdx >= 0 && minIdx < slices.Index(Protocols, "SMB3_00") {
		return fmt.Errorf("required encryption needs min protocol SMB3_00 or newer, not %s", s.MinProtocol)
	}
	if s.Multichannel != "" && s.Multichannel != "yes" && s.Multichannel != "no" {
		return fmt.Errorf("multichannel must be yes or no, not %q", s.Multichannel)
	}
	return nil
}

func (s ServerSettings) empty() bool { return s == ServerSettings{} }

// renderServer writes the [global] part of the managed config (nothing
// when no server setting is managed and the activity log is off).
func renderServer(b *strings.Builder, s ServerSettings, audit bool) {
	if s.empty() && !audit {
		return
	}
	// The managed config is included from smb.conf's [global] section, but
	// naming the section again keeps these global even if an admin moved
	// the include line.
	b.WriteString("\n[global]\n")
	for _, p := range []struct{ key, value string }{
		{"netbios name", strings.ToUpper(s.NetbiosName)},
		{"workgroup", strings.ToUpper(s.Workgroup)},
		{"server min protocol", s.MinProtocol},
		{"server max protocol", s.MaxProtocol},
		{"server smb encrypt", s.Encryption},
		{"server multi channel support", s.Multichannel},
	} {
		if p.value != "" {
			param(b, p.key, p.value)
		}
	}
	if audit {
		// Each connection's process reads the config again, so the log
		// level must be here (setting it at runtime does not last). It
		// replaces a log level set in smb.conf while the activity log is on.
		param(b, "log level", AuditLogLevel)
	}
}

// renderAudit adds the activity log to one share.
func renderAudit(b *strings.Builder) {
	param(b, "vfs objects", "full_audit")
	param(b, "full_audit:prefix", AuditPrefix)
	param(b, "full_audit:success", AuditSuccess)
	param(b, "full_audit:failure", AuditFailure)
	param(b, "full_audit:syslog", "no")
}
