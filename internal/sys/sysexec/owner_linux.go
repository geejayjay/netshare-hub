//go:build linux

package sysexec

import (
	"io/fs"
	"syscall"
	"time"
)

// fileOwner returns the UID that owns a file.
func fileOwner(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// changed is a file's change time (ctime): when it was last renamed,
// moved or had its metadata changed.
func changed(info fs.FileInfo) time.Time {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.ModTime()
	}
	return time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
}
