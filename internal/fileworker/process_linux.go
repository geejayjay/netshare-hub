//go:build linux

package fileworker

import "syscall"

// prepareProcess sets process-wide defaults for a worker.
func prepareProcess() {
	// New files are never readable by "others", whatever mode is asked for.
	syscall.Umask(0o007)
}
