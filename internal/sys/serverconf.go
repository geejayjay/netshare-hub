package sys

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
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
	// LoginAudit logs every SMB login (brute-force protection), also when
	// the activity log is off.
	LoginAudit bool `json:"loginAudit,omitempty"`
	// Banned are addresses (IPs, or IPv6 /64 ranges) refused before they
	// can even try a password: a global "hosts deny".
	Banned []string `json:"banned,omitempty"`
}

// Apple reports whether a config needs Apple's SMB extensions: when
// Mac support is on, or any share is a Time Machine target.
func (cfg SambaConfig) Apple() bool {
	if cfg.Server.MacSupport {
		return true
	}
	for _, s := range cfg.Shares {
		if s.TimeMachine {
			return true
		}
	}
	return false
}

// MaxBans is the most addresses one config bans.
const MaxBans = 1000

// LoginLogLevel logs logins only (brute-force protection without the
// activity log).
const LoginLogLevel = "0 auth_json_audit:3"

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
	// MacSupport turns on Apple's SMB extensions (vfs_fruit) on every
	// share: faster folder listings and correct file metadata on Macs.
	// It is on every share because a Mac decides on the first share it
	// opens whether the server speaks the extensions. A Time Machine share
	// turns it on too (see SambaConfig.Apple).
	MacSupport bool `json:"macSupport,omitempty"`
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
// when no server setting is managed, nothing is logged and nobody is
// banned).
func renderServer(b *strings.Builder, cfg SambaConfig) {
	s := cfg.Server
	if s.empty() && !cfg.Audit && !cfg.LoginAudit && len(cfg.Banned) == 0 && !cfg.Apple() {
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
	// Each connection's process reads the config again, so the log level
	// must be here (setting it at runtime does not last). It replaces a log
	// level set in smb.conf while these are on.
	switch {
	case cfg.Audit:
		param(b, "log level", AuditLogLevel)
	case cfg.LoginAudit:
		param(b, "log level", LoginLogLevel)
	}
	if cfg.Apple() {
		renderAppleGlobals(b)
	}
	if len(cfg.Banned) > 0 {
		// Checked when a client connects, before it can try a password.
		// It replaces a global "hosts deny" set in smb.conf while anyone is
		// banned.
		param(b, "hosts deny", strings.Join(cfg.Banned, ", "))
	}
}

// AppleModules are the Samba modules of Apple's SMB extensions, in the order
// Samba's documentation gives.
var AppleModules = []string{"catia", "fruit", "streams_xattr"}

// renderAppleGlobals writes the server-wide settings of Apple's extensions.
// They are defaults for every share.
func renderAppleGlobals(b *strings.Builder) {
	// Mac metadata lives in the file's extended attributes (streams_xattr),
	// not in extra "._" files.
	param(b, "fruit:metadata", "stream")
	// How the server looks in Finder.
	param(b, "fruit:model", "MacSamba")
	// A Mac must not change permissions through NFS-style entries: the
	// share's folder ACL decides who may do what.
	param(b, "fruit:nfs_aces", "no")
	// Keep "._" files other tools wrote; clean up empty ones Macs leave.
	param(b, "fruit:veto_appledouble", "no")
	param(b, "fruit:wipe_intentionally_left_blank_rfork", "yes")
	param(b, "fruit:delete_empty_adfiles", "yes")
}

// renderModules adds a share's Samba modules: Apple's extensions and the
// activity log. full_audit comes last, so it logs what reaches the disk
// (real file names, after the Apple modules translated them).
func renderModules(b *strings.Builder, cfg SambaConfig, s ShareSpec) {
	var modules []string
	if cfg.Apple() {
		modules = append(modules, AppleModules...)
	}
	if s.RecycleDays > 0 {
		modules = append(modules, "recycle")
	}
	if cfg.Audit {
		modules = append(modules, "full_audit") // last: it logs the real names
	}
	if len(modules) > 0 {
		param(b, "vfs objects", strings.Join(modules, " "))
	}
	if s.RecycleDays > 0 {
		param(b, "recycle:repository", RecycleDir)
		param(b, "recycle:keeptree", "yes")
		param(b, "recycle:versions", "yes")
		param(b, "recycle:touch", "yes")
		// The bin's folders take the share's inherited ACL (like every
		// folder made over SMB): Samba's default 0700 would cut it off.
		param(b, "recycle:directory_mode", "0770")
		param(b, "recycle:subdir_mode", "0770")
		param(b, "recycle:exclude", UploadTempPattern)
		param(b, "recycle:exclude_dir", RecycleDir)
	}
	if s.TimeMachine {
		param(b, "fruit:time machine", "yes")
		if s.TimeMachineMaxGiB > 0 {
			param(b, "fruit:time machine max size", strconv.Itoa(s.TimeMachineMaxGiB)+"G")
		}
	}
	if cfg.Audit {
		renderAudit(b)
	}
}

// RequiredModules are the Samba modules a config loads.
func (cfg SambaConfig) RequiredModules() []string {
	var m []string
	if cfg.Apple() {
		m = append(m, AppleModules...)
	}
	if cfg.Audit {
		m = append(m, "full_audit")
	}
	for _, s := range cfg.Shares {
		if s.RecycleDays > 0 && s.Live() {
			m = append(m, "recycle")
			break
		}
	}
	return m
}

// renderAudit adds the activity log's settings to one share.
func renderAudit(b *strings.Builder) {
	param(b, "full_audit:prefix", AuditPrefix)
	param(b, "full_audit:success", AuditSuccess)
	param(b, "full_audit:failure", AuditFailure)
	param(b, "full_audit:syslog", "no")
}
