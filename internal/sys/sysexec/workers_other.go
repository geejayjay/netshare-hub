//go:build !linux

package sysexec

import (
	"context"
	"errors"

	"netshare-hub/internal/sys"
)

// DefaultWorkerDir holds one socket per running file worker.
const DefaultWorkerDir = "/run/netshare-hub/workers"

// Workers cannot start processes as other users outside Linux. The real
// system layer refuses to start there anyway (see New); this stub only keeps
// the package compiling on development machines.
type Workers struct{ exe string }

func newWorkers(Runner, *Accounts, string, string, int, int) *Workers { return &Workers{} }

func (*Workers) Endpoint(context.Context, string, bool) (sys.WorkerEndpoint, error) {
	return sys.WorkerEndpoint{}, errors.New("file workers need Linux")
}

func (*Workers) Stop(string) {}
func (*Workers) StopAll()    {}
func (*Workers) Refresh()    {}

func webIdentity(context.Context, Runner, string) (int, int, error) { return 0, 0, nil }
