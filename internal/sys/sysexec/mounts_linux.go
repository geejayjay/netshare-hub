//go:build linux

package sysexec

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"netshare-hub/internal/sys"
)

func inspectMounts(paths []string) sys.MountReport {
	env := detectContainerEnv()
	unpriv := isUnprivilegedUserNS()

	var mounts []mountInfoEntry
	if f, err := os.Open("/proc/self/mountinfo"); err == nil {
		mounts = parseMountInfo(f)
		_ = f.Close()
	}

	checks := make([]sys.MountCheck, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		checks = append(checks, checkMount(p, mounts, env, unpriv))
	}

	return sys.MountReport{
		Environment:  env,
		Unprivileged: unpriv,
		Checks:       checks,
	}
}

func checkMount(p string, mounts []mountInfoEntry, env string, unpriv bool) sys.MountCheck {
	c := sys.MountCheck{Path: p, Warnings: []string{}}
	if m, ok := bestMountFor(p, mounts); ok {
		c.MountPoint = m.MountPoint
		c.FSType = m.FSType
		c.ReadOnly = m.ReadOnly
		if m.ReadOnly {
			c.Warnings = append(c.Warnings, sys.MountWarnReadOnly)
		}
		if m.MountPoint == "/" && env != "host" {
			c.OnRootDisk = true
			c.Warnings = append(c.Warnings, sys.MountWarnRootDisk)
		}
	}

	info, err := os.Stat(p)
	if err != nil {
		c.Warnings = append(c.Warnings, sys.MountWarnMissing)
	} else if st, ok := info.Sys().(*syscall.Stat_t); ok {
		// In an unprivileged LXC, an unmapped host UID appears as overflow UID 65534 (nobody/nogroup).
		if unpriv && (st.Uid == 65534 || st.Gid == 65534) {
			c.UIDMismatch = true
			c.Warnings = append(c.Warnings, sys.MountWarnUIDMismatch)
		}
	}
	return c
}

func detectContainerEnv() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	if data, err := os.ReadFile("/proc/1/environ"); err == nil {
		if strings.Contains(string(data), "container=lxc") {
			return "lxc"
		}
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "lxc") {
			return "lxc"
		}
		if strings.Contains(s, "docker") || strings.Contains(s, "containerd") {
			return "docker"
		}
	}
	return "host"
}

func isUnprivilegedUserNS() bool {
	data, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		inside, err1 := strconv.Atoi(fields[0])
		outside, err2 := strconv.Atoi(fields[1])
		if err1 == nil && err2 == nil && inside == 0 && outside != 0 {
			return true
		}
	}
	return false
}
