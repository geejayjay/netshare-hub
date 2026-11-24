package agent_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"netshare-hub/internal/agent"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysfake"
)

// A module that is off has no agent operations at all: the root surface
// of a minimal setup is the core (accounts, Samba config, share folders).
func TestFeaturesOff(t *testing.T) {
	fake := sysfake.New()
	authorize := func(*http.Request) (agent.Peer, bool) { return agent.Peer{UID: 1000}, true }
	server := httptest.NewServer(agent.NewServerWith(fake.System(), authorize, "test", agent.Features{}))
	t.Cleanup(server.Close)

	for _, op := range []string{"files.worker", "samba.audit-events", "samba.disconnect-client", "samba.sessions",
		"samba.disconnect-session", "discovery.status", "discovery.set", "accounts.smb-hashes", "accounts.create-from-hash",
		"nfs.status", "nfs.apply-exports"} {
		res, err := http.Post(server.URL+"/v1/"+op, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s with its feature off: status %d", op, res.StatusCode)
		}
	}
	// The core still works.
	client := agent.NewClient(server.Client(), server.URL)
	if err := client.System().Accounts.CreateUser(ctx, "jay", "a-password"); err != nil {
		t.Errorf("core operation: %v", err)
	}
	if err := client.ApplyConfig(ctx, sys.SambaConfig{}); err != nil {
		t.Errorf("apply config: %v", err)
	}
}

func TestNFSRoundTrip(t *testing.T) {
	client, fake, _ := start(t, true)
	system := client.System()
	if err := system.Accounts.CreateUser(ctx, "jay", "a-password"); err != nil {
		t.Fatal(err)
	}
	if err := system.Samba.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{{Name: "media", Path: "/srv/media",
		Readers: []string{"jay"}, Writers: []string{"jay"}}}}); err != nil {
		t.Fatal(err)
	}
	err := system.NFS.ApplyExports(ctx, []sys.NFSExport{{Share: "media", AsUser: "jay", Clients: []sys.NFSClient{{Address: "10.0.0.0/8"}}}})
	if err != nil {
		t.Fatalf("ApplyExports: %v", err)
	}
	if !strings.Contains(fake.CurrentExports(), "10.0.0.0/8(rw,") {
		t.Errorf("exports: %s", fake.CurrentExports())
	}
	st, err := system.NFS.NFSStatus(ctx)
	if err != nil || len(st.Exports) != 1 || st.Exports[0] != "media" {
		t.Errorf("status: %+v %v", st, err)
	}
	// Refused the same way across the socket: a share that is not live.
	err = system.NFS.ApplyExports(ctx, []sys.NFSExport{{Share: "nope", Clients: []sys.NFSClient{{Address: "10.0.0.1"}}}})
	if err == nil || !strings.Contains(err.Error(), "no share") {
		t.Errorf("unknown share: %v", err)
	}
	// Removing the share drops its export on the root side by itself.
	if err := system.Samba.ApplyConfig(ctx, sys.SambaConfig{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fake.CurrentExports(), "10.0.0.0/8") {
		t.Errorf("export outlived its share: %s", fake.CurrentExports())
	}
}

func TestReplicationStream(t *testing.T) {
	client, fake, _ := start(t, true)
	system := client.System()
	if err := system.Samba.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{{Name: "docs", Path: "/srv/docs"}}}); err != nil {
		t.Fatal(err)
	}
	fake.Replication().Set(sys.ReplicationStats{Transferred: 4, Bytes: 4096}, nil)
	var seen []sys.ReplicationProgress
	stats, err := system.Replication.Replicate(ctx, sys.ReplicationRun{Share: "docs", LocalPath: "/mnt/copy"},
		func(p sys.ReplicationProgress) { seen = append(seen, p) })
	if err != nil || stats.Transferred != 4 || stats.Bytes != 4096 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
	if len(seen) != 1 || seen[0].Percent != 50 {
		t.Errorf("progress: %+v", seen)
	}
	// rsync's failure comes back as a ReplicationError, with its exit code.
	fake.Replication().Set(sys.ReplicationStats{}, &sys.ReplicationError{ExitCode: 24, Message: "vanished"})
	_, err = system.Replication.Replicate(ctx, sys.ReplicationRun{Share: "docs", LocalPath: "/mnt/copy"}, nil)
	var re *sys.ReplicationError
	if !errors.As(err, &re) || !re.Partial() || re.Message != "vanished" {
		t.Errorf("rsync error: %v", err)
	}
	// Other errors keep their meaning.
	_, err = system.Replication.Replicate(ctx, sys.ReplicationRun{Share: "nope", LocalPath: "/mnt/copy"}, nil)
	if !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("unknown share: %v", err)
	}
	_, err = system.Replication.Replicate(ctx, sys.ReplicationRun{Share: "docs", LocalPath: "/srv/docs/in"}, nil)
	if !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("into a share: %v", err)
	}
	// Keys and peers.
	key, err := system.Replication.PublicKey(ctx)
	if err != nil || !sys.ValidSSHKey(key) {
		t.Errorf("key: %q %v", key, err)
	}
	if err := system.Replication.SetPeers(ctx, []sys.Peer{{ID: "p1", Host: "-oProxyCommand=x", Port: 22, HostKey: key}}); !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("a host that is an ssh option: %v", err)
	}
}
