package sysfake

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"

	"netshare-hub/internal/sys"
)

// FakeKey returns a valid ed25519 public key line made from seed.
func FakeKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	var b []byte
	b = binary.BigEndian.AppendUint32(b, 11)
	b = append(b, "ssh-ed25519"...)
	b = binary.BigEndian.AppendUint32(b, 32)
	b = append(b, sum[:]...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b)
}

// Replication is the fake sys.Replication: it checks requests like the
// real one and records them; it copies nothing.
type Replication struct {
	fake *Fake
	mu   sync.Mutex
	// Runs are the runs asked for, in order.
	Runs      []sys.ReplicationRun
	Peers     []sys.Peer
	Receivers []sys.Receiver
	// Key is this pretend machine's replication key; Host its SSH host key.
	Key, Host string
	// Result is what the next runs return; Fail makes them fail.
	Result sys.ReplicationStats
	Fail   error
	// Block, when set, makes a run wait until it is closed (or ctx ends).
	Block chan struct{}
}

// Replication returns the fake's replication part (one per fake).
func (f *Fake) Replication() *Replication {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replication == nil {
		f.replication = &Replication{fake: f, Key: FakeKey("key:" + fmt.Sprint(len(f.accounts))), Host: FakeKey("host")}
	}
	return f.replication
}

func (r *Replication) ReplicationStatus(context.Context) (sys.ReplicationStatus, error) {
	return sys.ReplicationStatus{Rsync: true, SSHClient: true, SSHServer: true, Rrsync: true}, nil
}

func (r *Replication) Replicate(ctx context.Context, run sys.ReplicationRun, progress func(sys.ReplicationProgress)) (sys.ReplicationStats, error) {
	if err := run.Check(); err != nil {
		return sys.ReplicationStats{}, fmt.Errorf("%w: %v", sys.ErrInvalid, err)
	}
	r.fake.mu.Lock()
	found := false
	for _, sh := range r.fake.shares {
		found = found || strings.EqualFold(sh.Name, run.Share)
		if run.LocalPath != "" && sys.Overlaps(run.LocalPath, sh.Path) {
			r.fake.mu.Unlock()
			return sys.ReplicationStats{}, fmt.Errorf("%w: %s overlaps share %q", sys.ErrInvalid, run.LocalPath, sh.Name)
		}
	}
	r.fake.mu.Unlock()
	if !found {
		return sys.ReplicationStats{}, fmt.Errorf("%w: no share named %q", sys.ErrNotFound, run.Share)
	}
	r.mu.Lock()
	if run.Peer != "" {
		ok := false
		for _, p := range r.Peers {
			ok = ok || p.ID == run.Peer
		}
		if !ok {
			r.mu.Unlock()
			return sys.ReplicationStats{}, fmt.Errorf("%w: no peer %q", sys.ErrNotFound, run.Peer)
		}
	}
	r.Runs = append(r.Runs, run)
	block, res, fail := r.Block, r.Result, r.Fail
	r.mu.Unlock()
	if progress != nil && !run.DryRun {
		progress(sys.ReplicationProgress{Bytes: res.Bytes / 2, Percent: 50})
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return sys.ReplicationStats{}, ctx.Err()
		}
	}
	if fail != nil {
		return res, fail
	}
	if run.DryRun && len(res.Changes) == 0 {
		res.Changes = []string{">f+++++++++ new.txt"}
	}
	return res, nil
}

// RunCount returns how many runs were asked for.
func (r *Replication) RunCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.Runs)
}

// LastRun returns the latest run.
func (r *Replication) LastRun() sys.ReplicationRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.Runs) == 0 {
		return sys.ReplicationRun{}
	}
	return r.Runs[len(r.Runs)-1]
}

// Set changes what the next runs return.
func (r *Replication) Set(res sys.ReplicationStats, fail error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Result, r.Fail = res, fail
}

func (r *Replication) PublicKey(context.Context) (string, error) { return r.Key, nil }

func (r *Replication) HostKey(context.Context) (string, error) { return r.Host, nil }

func (r *Replication) SetPeers(_ context.Context, peers []sys.Peer) error {
	for _, p := range peers {
		if err := p.Check(); err != nil {
			return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Peers = append([]sys.Peer(nil), peers...)
	return nil
}

func (r *Replication) SetReceivers(_ context.Context, receivers []sys.Receiver) error {
	for _, rc := range receivers {
		if err := rc.Check(); err != nil {
			return fmt.Errorf("%w: %v", sys.ErrInvalid, err)
		}
	}
	r.fake.mu.Lock()
	for _, rc := range receivers {
		for _, sh := range r.fake.shares {
			if sys.Overlaps(rc.Path, sh.Path) {
				r.fake.mu.Unlock()
				return fmt.Errorf("%w: %s overlaps share %q", sys.ErrInvalid, rc.Path, sh.Name)
			}
		}
	}
	r.fake.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Receivers = append([]sys.Receiver(nil), receivers...)
	return nil
}

// ReceiverList returns the applied receivers.
func (r *Replication) ReceiverList() []sys.Receiver {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sys.Receiver(nil), r.Receivers...)
}

// PeerList returns the applied peers.
func (r *Replication) PeerList() []sys.Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sys.Peer(nil), r.Peers...)
}
