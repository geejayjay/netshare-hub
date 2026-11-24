package app_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys/sysfake"
)

// The replication queue: the background job table is the only record of
// what is queued, so nothing can leave a job "busy" for good.

type replApp struct {
	admin *client
	fake  *sysfake.Fake
	app   *app.App
	dir   string
	share int64
}

func startReplApp(t *testing.T, dir string, opts jobs.Options) *replApp {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.SecureCookies = false
	cfg.Modules = replicationModules()
	fake := sysfake.New()
	system := fake.System()
	opts.SaveEvery, opts.PublishEvery = time.Millisecond, time.Millisecond
	a, err := app.New(context.Background(), cfg, app.Options{System: &system, JobOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, a)
	admin := newClient(t, server)
	var setup struct{ NeedsSetup bool }
	admin.do("GET", "/setup", nil, &setup)
	if setup.NeedsSetup {
		var s session
		admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
		admin.csrf = s.CSRFToken
	} else {
		admin.login("admin", pw)
	}
	r := &replApp{admin: admin, fake: fake, app: a, dir: dir}
	return r
}

func (r *replApp) job(t *testing.T, name, dest string) int64 {
	t.Helper()
	if r.share == 0 {
		jay := createUser(t, r.admin, "jay")
		var sh struct{ ID int64 }
		r.admin.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs", "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
		r.share = sh.ID
	}
	var j jobBody
	if status, _ := r.admin.do("POST", "/replication/jobs", map[string]any{"name": name, "shareId": r.share, "localPath": dest}, &j); status != 201 {
		t.Fatalf("create job: %d", status)
	}
	return j.ID
}

func (r *replApp) run(t *testing.T, c *client, id int64) (int, string) {
	t.Helper()
	var task taskBody
	status, _ := c.do("POST", "/replication/jobs/"+itoa(id)+"/run", nil, &task)
	return status, task.ID
}

func TestReplicationCancelWhileQueued(t *testing.T) {
	r := startReplApp(t, t.TempDir(), jobs.Options{})
	block := make(chan struct{})
	r.fake.Replication().Block = block
	defer close(block)
	first := r.job(t, "first", "/mnt/a")
	second := r.job(t, "second", "/mnt/b")
	_, running := r.run(t, r.admin, first)
	status, queued := r.run(t, r.admin, second)
	want(t, "queue the second", status, 202)
	waitFor(t, r.admin, running, jobs.StatusRunning)

	// Cancelled before it ever ran: it leaves no run behind and the job is
	// not busy.
	r.admin.do("POST", "/jobs/"+queued+"/cancel", nil, nil)
	waitFor(t, r.admin, queued, jobs.StatusCancelled)
	var runs struct{ Items []runBody }
	r.admin.do("GET", "/replication/jobs/"+itoa(second)+"/runs", nil, &runs)
	want(t, "runs of a job cancelled while queued", len(runs.Items), 0)
	status, _ = r.run(t, r.admin, second)
	want(t, "run again after cancelling", status, 202)
	// The running one is still refused a second time.
	status, _ = r.run(t, r.admin, first)
	want(t, "second run of a running job", status, 409)
}

func TestReplicationOwnerDeleted(t *testing.T) {
	r := startReplApp(t, t.TempDir(), jobs.Options{})
	block := make(chan struct{})
	r.fake.Replication().Block = block
	defer close(block)
	busyJob := r.job(t, "busy", "/mnt/a")
	target := r.job(t, "target", "/mnt/b")
	r.run(t, r.admin, busyJob) // takes the maintenance lane's slot

	// A second admin queues a run, then is deleted: the queued job goes
	// with them, and the replication job is free again.
	var other userBody
	r.admin.do("POST", "/users", map[string]any{"username": "other", "role": "admin", "password": pw}, &other)
	oc := newClient(t, r.admin.base)
	oc.login("other", pw)
	status, _ := r.run(t, oc, target)
	want(t, "queued by the other admin", status, 202)
	status, _ = r.run(t, r.admin, target)
	want(t, "while their run is queued", status, 409)
	if status, _ := r.admin.do("DELETE", "/users/"+itoa(other.ID), nil, nil); status != 204 {
		t.Fatalf("delete user: %d", status)
	}
	status, _ = r.run(t, r.admin, target)
	want(t, "after the owner of the queued run was deleted", status, 202)
}

// Two copies to one destination never run together, even with free
// slots; a copy elsewhere runs meanwhile.
func TestReplicationOneCopyPerDestination(t *testing.T) {
	r := startReplApp(t, t.TempDir(), jobs.Options{MaintenanceConcurrent: 3})
	block := make(chan struct{})
	r.fake.Replication().Block = block
	a1 := r.job(t, "a1", "/mnt/same")
	a2 := r.job(t, "a2", "/mnt/same")
	b := r.job(t, "b", "/mnt/other")
	_, t1 := r.run(t, r.admin, a1)
	_, t2 := r.run(t, r.admin, a2)
	_, t3 := r.run(t, r.admin, b)
	waitFor(t, r.admin, t1, jobs.StatusRunning)
	waitFor(t, r.admin, t3, jobs.StatusRunning)
	time.Sleep(100 * time.Millisecond)
	var j jobs.Job
	r.admin.do("GET", "/jobs/"+t2, nil, &j)
	want(t, "second copy to the same destination", j.Status, jobs.StatusQueued)
	close(block)
	waitFor(t, r.admin, t2, jobs.StatusSucceeded)
}

// A crash can leave a run record open (the job given up, or gone with its
// owner): the next start closes it, and the job can run again.
func TestReplicationReconcileAfterCrash(t *testing.T) {
	dir := t.TempDir()
	r := startReplApp(t, dir, jobs.Options{})
	id := r.job(t, "nightly", "/mnt/a")
	r.app.Close()

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "netshare-hub.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO replication_runs (job_id, task_id, dry_run, status, queued_at, started_at)
		VALUES (?, 'gone-job', 0, 'running', 1, 1)`, id); err != nil {
		t.Fatal(err)
	}
	db.Close()

	r2 := startReplApp(t, dir, jobs.Options{})
	var runs struct{ Items []runBody }
	r2.admin.do("GET", "/replication/jobs/"+itoa(id)+"/runs", nil, &runs)
	if len(runs.Items) != 1 || runs.Items[0].Status != "failed" || runs.Items[0].Error == "" {
		t.Fatalf("open run after restart: %+v", runs.Items)
	}
	status, task := r2.run(t, r2.admin, id)
	want(t, "run after the crash", status, 202)
	// (The new fake forgot jay's account, so the share is not live: the run
	// itself fails, and says so plainly.)
	j := waitFor(t, r2.admin, task, jobs.StatusFailed)
	if j.ErrorMessage == "" || strings.Contains(j.ErrorMessage, "account not found") {
		t.Errorf("error message: %q", j.ErrorMessage)
	}
}
