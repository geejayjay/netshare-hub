package app_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysfake"
)

// Step 4.1: replication.

func replicationModules() config.Modules {
	m := config.Default().Modules
	m.Replication = true
	return m
}

type jobBody struct {
	ID        int64
	Name      string
	ShareName string
	LocalPath string
	PeerID    int64
	PeerName  string
	Schedule  string
	NextRunAt *time.Time
	LastRun   *runBody
}

type runBody struct {
	ID          int64
	Status      string
	DryRun      bool
	Transferred int64
	Bytes       int64
	Changes     []string
	Error       string
}

type taskBody struct{ ID string }

func webURL(c *client) string { return strings.TrimSuffix(c.base, app.APIPrefix) }

// replicationPair sets up a sender A and a receiver B and pairs them.
func replicationPair(t *testing.T) (a *client, fa *sysfake.Fake, b *client, fb *sysfake.Fake, peerID int64) {
	t.Helper()
	a, fa, _ = moduleServer(t, replicationModules())
	b, fb, _ = moduleServer(t, replicationModules())
	fa.Replication().Key = sysfake.FakeKey("sender A")
	fb.Replication().Host = sysfake.FakeKey("host B")

	var rc struct {
		Receiver struct {
			ID     int64
			Paired bool
		}
		Code string
	}
	status, _ := b.do("POST", "/replication/receivers", map[string]any{"name": "office", "path": "/srv/replicas/office"}, &rc)
	want(t, "add receiver", status, 201)
	if !strings.HasPrefix(rc.Code, "r") || rc.Receiver.Paired {
		t.Fatalf("receiver: %+v", rc)
	}
	var peer struct {
		ID      int64
		Host    string
		HostKey string
	}
	status, _ = a.do("POST", "/replication/peers", map[string]any{"name": "office", "url": webURL(b), "code": rc.Code}, &peer)
	want(t, "pair", status, 201)
	if peer.HostKey != fb.Replication().Host || peer.Host != "127.0.0.1" {
		t.Fatalf("peer: %+v", peer)
	}
	return a, fa, b, fb, peer.ID
}

func TestReplicationPairing(t *testing.T) {
	_, fa, b, fb, _ := replicationPair(t)

	// The sender pinned the receiver's host key on its root side, and the
	// receiver let the sender's key (and only it) write into the folder.
	if peers := fa.Replication().PeerList(); len(peers) != 1 || peers[0].HostKey != fb.Replication().Host || peers[0].Port != 22 {
		t.Errorf("sender's peers: %+v", peers)
	}
	rcs := fb.Replication().ReceiverList()
	if len(rcs) != 1 || rcs[0].PublicKey != fa.Replication().Key || rcs[0].Path != "/srv/replicas/office" {
		t.Errorf("receiver's keys: %+v", rcs)
	}

	// A code works once.
	var list struct {
		Items []struct {
			ID     int64
			Paired bool
		}
	}
	b.do("GET", "/replication/receivers", nil, &list)
	if len(list.Items) != 1 || !list.Items[0].Paired {
		t.Fatalf("receivers: %+v", list)
	}
}

func TestReplicationPairingAttacks(t *testing.T) {
	a, fa, b, fb := func() (*client, *sysfake.Fake, *client, *sysfake.Fake) {
		a, fa, _ := moduleServer(t, replicationModules())
		b, fb, _ := moduleServer(t, replicationModules())
		return a, fa, b, fb
	}()
	fa.Replication().Key = sysfake.FakeKey("sender A")
	var rc struct {
		Receiver struct{ ID int64 }
		Code     string
	}
	b.do("POST", "/replication/receivers", map[string]any{"name": "office", "path": "/srv/replicas/office"}, &rc)

	// A wrong secret is refused, and never reaches the root side.
	var p problem
	bad := rc.Code[:len(rc.Code)-4] + "aaaa"
	if bad == rc.Code {
		bad = rc.Code[:len(rc.Code)-4] + "bbbb"
	}
	status, _ := a.do("POST", "/replication/peers", map[string]any{"name": "x", "url": webURL(b), "code": bad}, &p)
	want(t, "wrong code", status, 422)
	want(t, "code", p.Code, "REPLICATION_PAIRING_FAILED")
	if len(fb.Replication().ReceiverList()) != 0 {
		t.Fatal("a wrong code installed a key")
	}

	// Pair for real; afterwards any request for this code, replayed or
	// forged, is refused because the code is burned.
	var captured []byte
	status, _ = a.do("POST", "/replication/peers", map[string]any{"name": "office", "url": webURL(b), "code": rc.Code}, nil)
	want(t, "pair", status, 201)
	// Rebuild what was sent (the sender's key, a nonce, the HMAC): a replay
	// of any request for this code is refused, because the code is burned.
	captured, _ = json.Marshal(map[string]string{"receiver": strings.SplitN(rc.Code, "-", 2)[0], "nonce": strings.Repeat("ab", 16),
		"publicKey": sysfake.FakeKey("attacker"), "mac": strings.Repeat("00", 32)})
	res, err := http.Post(webURL(b)+"/api/v1/replication/pair", "application/json", bytes.NewReader(captured))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	want(t, "replayed / forged request", res.StatusCode, 403)
	if rcs := fb.Replication().ReceiverList(); len(rcs) != 1 || rcs[0].PublicKey != fa.Replication().Key {
		t.Errorf("receiver keys after the attack: %+v", rcs)
	}

	// A forged request with a fresh code but a swapped key (an attacker who
	// saw the code's id but not its secret) is refused too.
	var code struct{ Code string }
	b.do("POST", "/replication/receivers/"+itoa(rc.Receiver.ID)+"/pairing-code", nil, &code)
	forged, _ := json.Marshal(map[string]string{"receiver": strings.SplitN(code.Code, "-", 2)[0], "nonce": strings.Repeat("cd", 16),
		"publicKey": sysfake.FakeKey("attacker"), "mac": strings.Repeat("11", 32)})
	res, _ = http.Post(webURL(b)+"/api/v1/replication/pair", "application/json", bytes.NewReader(forged))
	res.Body.Close()
	want(t, "forged request", res.StatusCode, 403)

	// Guessing is rate-limited per address.
	for i := 0; i < 12; i++ {
		res, _ = http.Post(webURL(b)+"/api/v1/replication/pair", "application/json", bytes.NewReader(forged))
		res.Body.Close()
	}
	want(t, "rate limited", res.StatusCode, 429)

	// Without a session, nothing else is reachable.
	res, _ = http.Get(webURL(b) + "/api/v1/replication/receivers")
	res.Body.Close()
	want(t, "receivers without signing in", res.StatusCode, 401)
}

func TestReplicationToPeer(t *testing.T) {
	a, fa, _, _, peerID := replicationPair(t)
	jay := createUser(t, a, "jay")
	var sh struct{ ID int64 }
	a.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs", "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)

	var j jobBody
	status, _ := a.do("POST", "/replication/jobs", map[string]any{"name": "offsite", "shareId": sh.ID, "peerId": peerID, "mirror": true,
		"bandwidthKiB": 5000, "excludes": []string{"*.tmp", "cache/"}}, &j)
	want(t, "create job", status, 201)
	if j.ShareName != "docs" || j.PeerName != "office" || j.Schedule != "manual" || j.NextRunAt != nil {
		t.Errorf("job: %+v", j)
	}
	fa.Replication().Set(sys.ReplicationStats{Files: 10, Transferred: 3, Bytes: 3000}, nil)
	var task taskBody
	status, _ = a.do("POST", "/replication/jobs/"+itoa(j.ID)+"/run", nil, &task)
	want(t, "run", status, 202)
	if st, code := waitJob(t, a, task.ID); st != "succeeded" {
		t.Fatalf("run: %s %s", st, code)
	}
	run := fa.Replication().LastRun()
	if run.Share != "docs" || run.Peer != "p"+itoa(peerID) || run.LocalPath != "" || !run.Mirror || run.BandwidthKiB != 5000 ||
		strings.Join(run.Excludes, ",") != "*.tmp,cache/" || run.DryRun {
		t.Errorf("asked the root side for: %+v", run)
	}
	a.do("GET", "/replication/jobs/"+itoa(j.ID), nil, &j)
	if j.LastRun == nil || j.LastRun.Status != "done" || j.LastRun.Transferred != 3 || j.LastRun.Bytes != 3000 {
		t.Errorf("last run: %+v", j.LastRun)
	}

	// Deleting the peer deletes its jobs.
	status, _ = a.do("DELETE", "/replication/peers/"+itoa(peerID), nil, nil)
	want(t, "delete peer", status, 204)
	status, _ = a.do("GET", "/replication/jobs/"+itoa(j.ID), nil, nil)
	want(t, "job after its peer is gone", status, 404)
	if len(fa.Replication().PeerList()) != 0 {
		t.Error("the root side still has the peer")
	}
}

func TestReplicationLocal(t *testing.T) {
	a, fa, _ := moduleServer(t, replicationModules())
	jay := createUser(t, a, "jay")
	var sh struct{ ID int64 }
	a.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs", "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
	a.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media", "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, nil)

	for name, c := range map[string]struct {
		body  map[string]any
		field string
		code  string
	}{
		"inside a share":   {map[string]any{"localPath": "/srv/media/backup"}, "localPath", "overlaps_share"},
		"holds a share":    {map[string]any{"localPath": "/srv"}, "localPath", "overlaps_share"},
		"system folder":    {map[string]any{"localPath": "/etc/backup"}, "localPath", "invalid_format"},
		"no destination":   {map[string]any{}, "localPath", "required"},
		"two destinations": {map[string]any{"localPath": "/mnt/b", "peerId": 1}, "peerId", "one_destination"},
		"bad exclude":      {map[string]any{"localPath": "/mnt/b", "excludes": []string{"--delete-after"}}, "excludes[0]", "invalid_format"},
		"bad minute":       {map[string]any{"localPath": "/mnt/b", "schedule": "hourly", "atMinute": 75}, "atMinute", "out_of_range"},
	} {
		body := map[string]any{"name": name, "shareId": sh.ID}
		for k, v := range c.body {
			body[k] = v
		}
		var p problem
		status, _ := a.do("POST", "/replication/jobs", body, &p)
		if status != 422 || fieldCodes(p)[c.field] != c.code {
			t.Errorf("%s: %d %+v", name, status, p.Errors)
		}
	}

	var j jobBody
	status, _ := a.do("POST", "/replication/jobs", map[string]any{"name": "second disk", "shareId": sh.ID, "localPath": "/mnt/backup/docs",
		"schedule": "daily", "atMinute": 150}, &j)
	want(t, "create", status, 201)
	if j.NextRunAt == nil {
		t.Fatal("a daily job has no next run")
	}
	if l := j.NextRunAt.Local(); l.Hour() != 2 || l.Minute() != 30 || time.Until(l) > 24*time.Hour || time.Until(l) < 0 {
		t.Errorf("next run: %v", l)
	}

	// A preview changes nothing and lists what would change.
	fa.Replication().Set(sys.ReplicationStats{Transferred: 1, Changes: []string{">f+++++++++ a.txt", "*deleting   old.txt"}}, nil)
	var task taskBody
	a.do("POST", "/replication/jobs/"+itoa(j.ID)+"/preview", nil, &task)
	waitJob(t, a, task.ID)
	if run := fa.Replication().LastRun(); !run.DryRun || run.LocalPath != "/mnt/backup/docs" {
		t.Errorf("preview asked for: %+v", run)
	}
	var runs struct{ Items []runBody }
	a.do("GET", "/replication/jobs/"+itoa(j.ID)+"/runs", nil, &runs)
	if len(runs.Items) != 1 || !runs.Items[0].DryRun || len(runs.Items[0].Changes) != 2 {
		t.Errorf("runs: %+v", runs.Items)
	}
	// A preview is not the job's last run.
	a.do("GET", "/replication/jobs/"+itoa(j.ID), nil, &j)
	if j.LastRun != nil {
		t.Errorf("a preview counted as a run: %+v", j.LastRun)
	}

	// A failure raises an alert; the next success resolves it.
	fa.Replication().Set(sys.ReplicationStats{}, &sys.ReplicationError{ExitCode: 11, Message: "No space left on device (28)"})
	a.do("POST", "/replication/jobs/"+itoa(j.ID)+"/run", nil, &task)
	if st, code := waitJob(t, a, task.ID); st != "failed" || code != "REPLICATION_FAILED" {
		t.Errorf("failed run: %s %s", st, code)
	}
	found := false
	for _, al := range activeAlerts(t, a) {
		found = found || (al.Kind == "replication.failed" && al.Source == "replication")
	}
	if !found {
		t.Error("no alert for a failed run")
	}
	fa.Replication().Set(sys.ReplicationStats{}, &sys.ReplicationError{ExitCode: 24, Message: "some files vanished"})
	a.do("POST", "/replication/jobs/"+itoa(j.ID)+"/run", nil, &task)
	if st, code := waitJob(t, a, task.ID); st != "failed" || code != "REPLICATION_PARTLY_DONE" {
		t.Errorf("partial run: %s %s", st, code)
	}
	fa.Replication().Set(sys.ReplicationStats{Transferred: 2}, nil)
	a.do("POST", "/replication/jobs/"+itoa(j.ID)+"/run", nil, &task)
	waitJob(t, a, task.ID)
	for _, al := range activeAlerts(t, a) {
		if al.Kind == "replication.failed" {
			t.Errorf("alert still active after a good run: %+v", al)
		}
	}

	// One real run of a job at a time.
	block := make(chan struct{})
	fa.Replication().Block = block
	a.do("POST", "/replication/jobs/"+itoa(j.ID)+"/run", nil, &task)
	var p problem
	status, _ = a.do("POST", "/replication/jobs/"+itoa(j.ID)+"/run", nil, &p)
	want(t, "second run while one is queued", status, 409)
	close(block)
	waitJob(t, a, task.ID)
	fa.Replication().Block = nil

	// A share that becomes a destination's parent is not checked here (the
	// root side checks every run), but a receiving folder may not overlap a
	// job's destination.
	status, _ = a.do("POST", "/replication/receivers", map[string]any{"name": "in", "path": "/mnt/backup"}, &p)
	want(t, "receiver over a job's destination", status, 422)
}

// The system layer refuses what the service let through if the world
// changed in between (here: the destination became a share).
func TestReplicationRootSideRefuses(t *testing.T) {
	a, fa, _ := moduleServer(t, replicationModules())
	jay := createUser(t, a, "jay")
	var sh struct{ ID int64 }
	a.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs", "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
	var j jobBody
	a.do("POST", "/replication/jobs", map[string]any{"name": "copy", "shareId": sh.ID, "localPath": "/mnt/copy"}, &j)
	a.do("POST", "/shares", map[string]any{"name": "copy", "path": "/mnt/copy", "access": []map[string]any{{"userId": jay.ID, "level": "read"}}}, nil)
	var task taskBody
	a.do("POST", "/replication/jobs/"+itoa(j.ID)+"/run", nil, &task)
	if st, _ := waitJob(t, a, task.ID); st != "failed" {
		t.Errorf("a copy into a share ran: %s", st)
	}
	_ = fa
}

func TestReplicationSchedule(t *testing.T) {
	a, fa, _ := moduleServer(t, replicationModules())
	jay := createUser(t, a, "jay")
	var sh struct{ ID int64 }
	a.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs", "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
	var j jobBody
	a.do("POST", "/replication/jobs", map[string]any{"name": "hourly", "shareId": sh.ID, "localPath": "/mnt/h", "schedule": "hourly",
		"atMinute": time.Now().Minute()}, &j)
	if j.NextRunAt == nil || time.Until(*j.NextRunAt) < 59*time.Minute {
		t.Errorf("next run of an hourly job: %v", j.NextRunAt)
	}
	// Disabled: no next run.
	id := j.ID
	j = jobBody{} // nextRunAt is left out when there is none
	a.do("PATCH", "/replication/jobs/"+itoa(id), map[string]any{"enabled": false}, &j)
	if j.NextRunAt != nil {
		t.Errorf("a disabled job is scheduled: %v", j.NextRunAt)
	}
	_ = fa
}
