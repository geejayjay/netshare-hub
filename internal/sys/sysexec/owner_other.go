//go:build !linux

package sysexec

import "io/fs"

// fileOwner is only known on Linux.
func fileOwner(fs.FileInfo) (int, bool) { return 0, false }
