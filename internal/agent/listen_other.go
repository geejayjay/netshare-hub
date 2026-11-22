//go:build !linux

package agent

import (
	"context"
	"errors"

	"netshare-hub/internal/sys"
)

// Serve is only available on Linux: the agent relies on the kernel telling
// it who is on the other end of the socket.
func Serve(context.Context, string, string, sys.System, string) error {
	return errors.New(`the agent needs Linux; on this machine use sys = "fake" for development`)
}
