//go:build !linux

package fileworker

import "os"

// readable cannot ask the kernel outside Linux. Workers only run on Linux;
// the development fake shows everything.
func readable(*os.File, string) bool { return true }
