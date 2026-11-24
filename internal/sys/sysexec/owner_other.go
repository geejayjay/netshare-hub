//go:build !linux

package sysexec

import (
	"io/fs"
	"time"
)

// fileOwner is only known on Linux.
func fileOwner(fs.FileInfo) (int, bool) { return 0, false }

// changed falls back to the modification time outside Linux.
func changed(info fs.FileInfo) time.Time { return info.ModTime() }
