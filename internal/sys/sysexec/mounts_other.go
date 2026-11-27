//go:build !linux

package sysexec

import (
	"os"

	"netshare-hub/internal/sys"
)

func inspectMounts(paths []string) sys.MountReport {
	checks := make([]sys.MountCheck, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		c := sys.MountCheck{Path: p, MountPoint: "/", Warnings: []string{}}
		if _, err := os.Stat(p); err != nil {
			c.Warnings = append(c.Warnings, sys.MountWarnMissing)
		}
		checks = append(checks, c)
	}
	return sys.MountReport{
		Environment:  "host",
		Unprivileged: false,
		Checks:       checks,
	}
}
