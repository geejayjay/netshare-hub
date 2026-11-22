//go:build !linux

package fileworker

import "os"

// noFollow: os.Root already keeps every path inside the share.
const noFollow = 0

// readable cannot ask the kernel outside Linux. Workers only run on Linux;
// the development fake shows everything.
func readable(*os.File, string) bool { return true }
