//go:build linux

package sysexec

import (
	"io/fs"
	"syscall"
)

// fileOwner returns the UID that owns a file.
func fileOwner(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
