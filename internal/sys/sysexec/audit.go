package sysexec

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sambalog"
)

// moduleDirGlobs are where distributions put Samba's VFS modules.
var moduleDirGlobs = []string{
	"/usr/lib/*/samba/vfs", // Debian, Ubuntu (multiarch)
	"/usr/lib64/samba/vfs", // Fedora, RHEL
	"/usr/lib/samba/vfs",   // Arch, Alpine
}

// moduleInstalled reports whether a Samba VFS module can be loaded. The
// name comes from a fixed list in package sys, never from a request.
func moduleInstalled(name string) bool {
	for _, g := range moduleDirGlobs {
		if m, _ := filepath.Glob(filepath.Join(g, name+".so")); len(m) > 0 {
			return true
		}
	}
	return false
}

// checkModules refuses a config whose modules are not all installed.
// Samba's own checker does not notice a missing module, and a share naming
// one stops working, so this is checked before anything changes.
func checkModules(cfg sys.SambaConfig) error {
	var missing []string
	for _, m := range cfg.RequiredModules() {
		if !moduleInstalled(m) {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s (on Debian and Ubuntu: apt install samba-vfs-modules)", sys.ErrModuleMissing, strings.Join(missing, ", "))
	}
	return nil
}

// AuditLog reads Samba's log files (sys.Audit).
type AuditLog struct {
	runner     Runner
	mainConfig string
	poll       time.Duration
}

func NewAuditLog(r Runner, mainConfig string) *AuditLog {
	return &AuditLog{runner: r, mainConfig: mainConfig, poll: 500 * time.Millisecond}
}

var sambaVariable = regexp.MustCompile(`%[A-Za-z]`)

// logPattern finds where Samba writes its logs ("log file", usually
// /var/log/samba/log.%m: one file per client) and turns it into a pattern
// matching all of them.
func (a *AuditLog) logPattern(ctx context.Context) (string, error) {
	res, err := run(ctx, a.runner, "testparm", "-s", "--parameter-name", "log file", a.mainConfig)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(res.Stdout)
	if value == "" {
		value = "/var/log/samba/log.%m"
	}
	pattern := sambaVariable.ReplaceAllString(value, "*")
	if !filepath.IsAbs(pattern) || strings.Contains(pattern, "..") {
		return "", fmt.Errorf("unexpected samba log file setting %q", value)
	}
	return filepath.Clean(pattern), nil
}

// Events follows Samba's log files from now on.
func (a *AuditLog) Events(ctx context.Context) (<-chan sambalog.Event, error) {
	pattern, err := a.logPattern(ctx)
	if err != nil {
		return nil, err
	}
	in := sambalog.New()
	events := in.Subscribe("stream", 10000)
	go func() {
		_ = in.TailDir(ctx, pattern, a.poll)
		in.Close()
	}()
	return events, nil
}
