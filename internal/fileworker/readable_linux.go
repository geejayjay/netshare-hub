//go:build linux

package fileworker

import (
	"os"

	"golang.org/x/sys/unix"
)

// readable asks the kernel whether this process may read an entry of an
// open folder. The worker runs as the user, so this is the user's answer.
func readable(dir *os.File, name string) bool {
	return unix.Faccessat(int(dir.Fd()), name, unix.R_OK, 0) == nil
}
