// Package config loads the bootstrap settings: the few values the server needs
// before it can open its database. Everything else is a database-backed
// setting edited in the UI, so a setting never has two sources.
//
// Order: built-in defaults < TOML file < NSH_* environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Listen is the address the web server binds to.
	Listen string `toml:"listen"`
	// DataDir holds the database and other state.
	DataDir string `toml:"data_dir"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `toml:"log_level"`
	// LogFormat is json (default) or text (nicer in a dev terminal).
	LogFormat string `toml:"log_format"`
	// SecureCookies marks the session cookie HTTPS-only. Turn it off only for
	// local development over plain http.
	SecureCookies bool `toml:"secure_cookies"`
	// Sys chooses how the server reaches the operating system:
	//   "agent"  through the root agent over a local socket. The server
	//            itself runs as an unprivileged user. Use this in production.
	//   "real"   directly, in this process. The server must run as root.
	//   "fake"   nothing real is touched; everything stays in memory. For
	//            development and tests on any machine.
	Sys string `toml:"sys"`
	// AgentSocket is where the root agent listens (sys = "agent").
	AgentSocket string `toml:"agent_socket"`
	// AgentUser is the only user (besides root) the agent accepts calls
	// from: the account the web server runs as.
	AgentUser string `toml:"agent_user"`
	// MaxUploadBytes is the largest file the web file manager accepts in
	// one upload. 0 means no limit (the disk is the limit).
	MaxUploadBytes int64 `toml:"max_upload_bytes"`
	// ActivityRetentionDays is how long the SMB activity log is kept.
	ActivityRetentionDays int `toml:"activity_retention_days"`
	// Modules switches optional features on or off (see Modules).
	Modules Modules `toml:"modules"`
}

// Modules are the optional features. A module that is off costs nothing:
// its routes, background work and scheduled tasks do not exist, and the
// root agent does not offer its system operations at all (the agent reads
// the same file), so a minimal setup also has a smaller privileged surface.
// Shares, users, sign-in and jobs are the core and always on.
//
// They are bootstrap settings, not UI settings, on purpose: switching one
// changes what the server and the agent ARE, so it takes a restart of both.
type Modules struct {
	// FileManager: browsing, uploads and downloads in the browser (per-user
	// file workers). Off: shares are reached over SMB only.
	FileManager bool `toml:"file_manager"`
	// ActivityLog: the SMB activity log (Samba's audit log, read by the
	// agent). Needed by the ransomware guard.
	ActivityLog bool `toml:"activity_log"`
	// RansomwareGuard watches SMB activity for mass encryption.
	RansomwareGuard bool `toml:"ransomware_guard"`
	// BruteForce bans addresses with many failed SMB logins.
	BruteForce bool `toml:"brute_force"`
	// Discovery: switching WSD (Windows) and mDNS (macOS) from the UI.
	Discovery bool `toml:"discovery"`
	// Sessions: the list of SMB sessions, and ending one.
	Sessions bool `toml:"sessions"`
	// Backup: encrypted backup and restore of the app's configuration.
	Backup bool `toml:"backup"`
	// NFS: exporting shares over NFS as well (needs nfs-kernel-server).
	NFS bool `toml:"nfs"`
	// Replication: copying shares to another folder or NetShare Hub.
	Replication bool `toml:"replication"`
	// BlockStorage: iSCSI / NVMe-oF disks (needs the kernel targets: on a
	// Proxmox host or a VM, not in an unprivileged LXC).
	BlockStorage bool `toml:"block_storage"`
}

// AllModules is every module on (the API spec is built with it, so the
// frontend has the types of every module).
func AllModules() Modules {
	return Modules{FileManager: true, ActivityLog: true, RansomwareGuard: true, BruteForce: true, Discovery: true,
		Sessions: true, Backup: true, NFS: true, Replication: true, BlockStorage: true}
}

// SMBEvents reports whether anything reads Samba's logs: without it the
// agent never tails them.
func (m Modules) SMBEvents() bool { return m.ActivityLog || m.RansomwareGuard || m.BruteForce }

func (m Modules) check() error {
	if m.RansomwareGuard && !m.ActivityLog {
		return errors.New("modules.ransomware_guard needs modules.activity_log: the guard watches the activity log")
	}
	return nil
}

// ProtectedDirs are folders a share must never expose: NetShare Hub's own
// data directory, which holds the database with the password hashes.
func (c Config) ProtectedDirs() []string {
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return []string{filepath.ToSlash(abs)}
}

func Default() Config {
	return Config{
		Listen:        "127.0.0.1:8080",
		DataDir:       "./data",
		LogLevel:      "info",
		LogFormat:     "json",
		SecureCookies: true,
		Sys:           "agent",
		AgentSocket:   "/run/netshare-hub/agent.sock",
		AgentUser:     "netshare",

		ActivityRetentionDays: 90,
		// Everything built before step 4 is on; the newer, heavier
		// protocols are opt-in.
		Modules: Modules{FileManager: true, ActivityLog: true, RansomwareGuard: true, BruteForce: true,
			Discovery: true, Sessions: true, Backup: true},
	}
}

// Load reads the config. An empty path means "no file"; a path that does not
// exist is an error, because a typo should not silently fall back to defaults.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return cfg, fmt.Errorf("config file %q not found", path)
			}
			return cfg, fmt.Errorf("read config %q: %w", path, err)
		}
	}

	env(&cfg.Listen, "NSH_LISTEN")
	env(&cfg.DataDir, "NSH_DATA_DIR")
	env(&cfg.LogLevel, "NSH_LOG_LEVEL")
	env(&cfg.LogFormat, "NSH_LOG_FORMAT")
	env(&cfg.Sys, "NSH_SYS")
	env(&cfg.AgentSocket, "NSH_AGENT_SOCKET")
	env(&cfg.AgentUser, "NSH_AGENT_USER")
	if v, ok := os.LookupEnv("NSH_SECURE_COOKIES"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("NSH_SECURE_COOKIES must be true or false, got %q", v)
		}
		cfg.SecureCookies = b
	}
	if cfg.Sys != "agent" && cfg.Sys != "real" && cfg.Sys != "fake" {
		return cfg, fmt.Errorf(`sys must be "agent", "real" or "fake", got %q`, cfg.Sys)
	}
	if err := cfg.Modules.check(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func env(dst *string, name string) {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		*dst = v
	}
}
