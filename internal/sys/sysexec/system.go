package sysexec

import (
	"context"
	"errors"
	"os"
	"runtime"

	"netshare-hub/internal/sys"
)

// Options configure the real system layer.
type Options struct {
	// Protected lists folders that must never be shared: NetShare Hub's own
	// data (the database holds password hashes).
	Protected []string
	// WebUser is the account the web server runs as. Only it (and root) may
	// connect to a user's file worker. Leave empty when the web server is
	// this same root process.
	WebUser string
	// WorkerExe is the program started (as "<WorkerExe> fileworker") for a
	// user's file worker. Empty means this program. Only tests set it: a
	// test binary cannot act as a worker.
	WorkerExe string
}

// New returns the real system. It refuses to start anywhere it could not
// work, with a message that says what to do instead.
func New(opts Options) (sys.System, error) {
	if runtime.GOOS != "linux" {
		return sys.System{}, errors.New(`managing the real system needs Linux; set sys = "fake" in the config to develop on this machine`)
	}
	if os.Geteuid() != 0 {
		return sys.System{}, errors.New(`managing users and Samba needs root; run as root, or set sys = "fake" in the config for development`)
	}
	runner := ExecRunner{}
	webUID, webGID, err := webIdentity(context.Background(), runner, opts.WebUser)
	if err != nil {
		return sys.System{}, err
	}

	accounts := NewAccounts(runner)
	samba := NewSamba(runner, DefaultManagedConfig, DefaultMainConfig, opts.Protected...)
	workers := newWorkers(runner, accounts, DefaultManagedConfig, DefaultWorkerDir, webUID, webGID)
	workers.exe = opts.WorkerExe

	nfs := NewNFS(runner, accounts, samba, DefaultExportsFile)
	groups := NewGroups(runner, DefaultGroupsFile)
	// A worker runs with the groups it started with: restart the workers of
	// accounts whose groups changed.
	groups.changed = func(names []string) {
		for _, n := range names {
			workers.Stop(n)
		}
	}
	replication := NewReplication(runner, samba, DefaultReplicationDir, opts.Protected...)
	block := NewBlock(samba, DefaultBlockDir, opts.Protected...)
	samba.reserved = func() []string { return append(replication.ReservedFolders(), block.ReservedFolders()...) }
	// A worker, or an NFS export, must not outlive the access it was
	// started with.
	accounts.onRevoke = workers.Stop
	accounts.onEnabled = func() { nfs.Refresh(context.Background()) }
	samba.afterApply = func() {
		workers.Refresh()
		nfs.Refresh(context.Background())
	}

	return sys.System{
		Accounts:    accounts,
		Samba:       samba,
		Files:       NewFiles(runner, opts.Protected...),
		Workers:     workers,
		Audit:       NewAuditLog(runner, DefaultMainConfig),
		Neighbors:   NewNeighbors(),
		Discovery:   NewDiscovery(runner),
		NFS:         nfs,
		Replication: replication,
		Block:       block,
		Groups:      groups,
	}, nil
}
