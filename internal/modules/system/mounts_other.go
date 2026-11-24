//go:build !linux

package system

import "os"

func inspectMounts(paths []string) MountReport {
	checks := make([]MountCheck, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		c := MountCheck{Path: p, MountPoint: "/", Warnings: []string{}}
		if _, err := os.Stat(p); err != nil {
			c.Warnings = append(c.Warnings, WarnMissing)
		}
		checks = append(checks, c)
	}
	return MountReport{
		Environment:  "host",
		Unprivileged: false,
		Checks:       checks,
	}
}
