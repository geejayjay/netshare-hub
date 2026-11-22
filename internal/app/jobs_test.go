package app_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysfake"
)

// jobEnv is one running application on a data directory. Several can be
// started on the same directory one after another, to simulate restarts.
type jobEnv struct {
	app   *app.App
	base  string
	close func()
}

func startJobApp(t *testing.T, dataDir string, kinds map[string]jobs.Handler, opts jobs.Options) *jobEnv {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.SecureCookies = false
	cfg.Sys = "fake"
	system := sysfake.New().System()
	if opts.SaveEvery == 0 {
		opts.SaveEvery = time.Millisecond
	}
	if opts.PublishEvery == 0 {
		opts.PublishEvery = time.Millisecond
	}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system, JobKinds: kinds, JobOptions: opts})
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	server := httptest.NewServer(application.Handler)
	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			server.Close()
			application.Close()
		})
	}
	t.Cleanup(closeFn)
	return &jobEnv{app: application, base: server.URL + app.APIPrefix, close: closeFn}
}

// admin returns a logged-in admin client, running first-time setup if needed.
func (e *jobEnv) admin(t *testing.T) *client {
	t.Helper()
	c := newClient(t, e.base)
	var setup struct{ NeedsSetup bool }
	c.do("GET", "/setup", nil, &setup)
	if setup.NeedsSetup {
		var s session
		c.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
		c.csrf = s.CSRFToken
		return c
	}
	c.login("admin", pw)
	return c
}

func (e *jobEnv) submit(t *testing.T, kind string, owner int64, payload any) jobs.Job {
	t.Helper()
	j, err := e.app.Jobs.Submit(context.Background(), jobs.Submit{Kind: kind, OwnerID: owner, Title: "Test " + kind, Payload: payload})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return j
}

// waitFor polls a job through the API until it reaches the status.
func waitFor(t *testing.T, c *client, id string, status jobs.Status) jobs.Job {
	t.Helper()
	var j jobs.Job
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		j = jobs.Job{}
		c.do("GET", "/jobs/"+id, nil, &j)
		if j.Status == status {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s: status %q, want %q (job: %+v)", id, j.Status, status, j)
	return j
}

func TestJobRunsAndReportsProgress(t *testing.T) {
	var background atomic.Bool
	env := startJobApp(t, t.TempDir(), map[string]jobs.Handler{
		"test.count": func(ctx context.Context, run *jobs.Run) error {
			background.Store(sys.IsBackground(ctx))
			var p struct{ N int64 }
			if err := run.Payload(&p); err != nil {
				return err
			}
			for i := int64(1); i <= p.N; i++ {
				run.Progress(i, p.N, "counting")
			}
			return nil
		},
	}, jobs.Options{})
	admin := env.admin(t)

	j := env.submit(t, "test.count", 1, map[string]int64{"N": 5})
	want(t, "new job status", j.Status, jobs.StatusQueued)

	done := waitFor(t, admin, j.ID, jobs.StatusSucceeded)
	want(t, "done", done.Done, int64(5))
	want(t, "total", done.Total, int64(5))
	want(t, "attempts", done.Attempts, 1)
	if done.StartedAt == nil || done.FinishedAt == nil {
		t.Error("a finished job must have start and finish times")
	}
	if !background.Load() {
		t.Error("a job's context must be marked as background work (low IO priority)")
	}

	var list struct {
		Items []jobs.Job
		Page  struct{ Total int }
	}
	admin.do("GET", "/jobs", nil, &list)
	want(t, "jobs listed", list.Page.Total, 1)
}

func TestJobConcurrencyLimit(t *testing.T) {
	var current, peak atomic.Int32
	release := make(chan struct{})
	env := startJobApp(t, t.TempDir(), map[string]jobs.Handler{
		"test.block": func(ctx context.Context, run *jobs.Run) error {
			n := current.Add(1)
			defer current.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}, jobs.Options{MaxConcurrent: 2})
	admin := env.admin(t)

	var ids []string
	for range 5 {
		ids = append(ids, env.submit(t, "test.block", 1, nil).ID)
	}
	time.Sleep(200 * time.Millisecond)
	want(t, "running at once", current.Load(), int32(2))
	close(release)
	for _, id := range ids {
		waitFor(t, admin, id, jobs.StatusSucceeded)
	}
	want(t, "peak concurrency", peak.Load(), int32(2))
}

func TestJobCancel(t *testing.T) {
	var ran atomic.Int32
	started := make(chan string, 10)
	env := startJobApp(t, t.TempDir(), map[string]jobs.Handler{
		"test.wait": func(ctx context.Context, run *jobs.Run) error {
			ran.Add(1)
			started <- run.ID()
			<-ctx.Done()
			return ctx.Err()
		},
	}, jobs.Options{MaxConcurrent: 1})
	admin := env.admin(t)

	running := env.submit(t, "test.wait", 1, nil)
	queued := env.submit(t, "test.wait", 1, nil)
	if id := <-started; id != running.ID {
		t.Fatalf("wrong job started first: %s", id)
	}

	// A queued job is cancelled at once and never runs.
	var j jobs.Job
	status, _ := admin.do("POST", "/jobs/"+queued.ID+"/cancel", nil, &j)
	want(t, "cancel queued status", status, 200)
	want(t, "cancelled queued job", j.Status, jobs.StatusCancelled)

	// A running job is told to stop and ends as cancelled.
	status, _ = admin.do("POST", "/jobs/"+running.ID+"/cancel", nil, &j)
	want(t, "cancel running status", status, 200)
	waitFor(t, admin, running.ID, jobs.StatusCancelled)

	// A finished job cannot be cancelled again.
	var p problem
	status, _ = admin.do("POST", "/jobs/"+running.ID+"/cancel", nil, &p)
	want(t, "cancel finished status", status, 409)
	want(t, "cancel finished code", p.Code, "JOB_ALREADY_FINISHED")

	time.Sleep(100 * time.Millisecond)
	want(t, "handlers run", ran.Load(), int32(1))
}

func TestJobFailures(t *testing.T) {
	env := startJobApp(t, t.TempDir(), map[string]jobs.Handler{
		"test.user-error": func(context.Context, *jobs.Run) error {
			return errs.Conflict("TARGET_EXISTS", "A file with this name already exists in Media.")
		},
		"test.internal-error": func(context.Context, *jobs.Run) error {
			return errors.New("open /srv/media/secret-internal-path: input/output error")
		},
		"test.panic": func(context.Context, *jobs.Run) error {
			panic("boom")
		},
	}, jobs.Options{})
	admin := env.admin(t)

	// A specific error is shown to the user as it is.
	j := waitFor(t, admin, env.submit(t, "test.user-error", 1, nil).ID, jobs.StatusFailed)
	want(t, "user error code", j.ErrorCode, "TARGET_EXISTS")
	want(t, "user error message", j.ErrorMessage, "A file with this name already exists in Media.")

	// An internal error is logged; the user sees a generic message, never the details.
	j = waitFor(t, admin, env.submit(t, "test.internal-error", 1, nil).ID, jobs.StatusFailed)
	want(t, "internal error code", j.ErrorCode, "JOB_FAILED")
	if j.ErrorMessage == "" || j.ErrorMessage != "The job failed. Check the logs for details." {
		t.Errorf("internal error message = %q", j.ErrorMessage)
	}

	// A panicking job fails; the server keeps running other jobs.
	waitFor(t, admin, env.submit(t, "test.panic", 1, nil).ID, jobs.StatusFailed)
	waitFor(t, admin, env.submit(t, "test.user-error", 1, nil).ID, jobs.StatusFailed)
}

// The step's main requirement: a long job survives a restart.
func TestJobResumesAfterRestart(t *testing.T) {
	dir := t.TempDir()
	type checkpoint struct{ Next int }

	// A job of 5 items. The first server stops it after item 2.
	var processed []int
	var mu sync.Mutex
	stopAfter := 2
	handler := func(ctx context.Context, run *jobs.Run) error {
		var cp checkpoint
		run.Checkpoint(&cp)
		for i := cp.Next; i < 5; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			mu.Lock()
			processed = append(processed, i)
			reached := len(processed) == stopAfter
			mu.Unlock()
			if err := run.SaveCheckpoint(checkpoint{Next: i + 1}); err != nil {
				return err
			}
			run.Progress(int64(i+1), 5, "item")
			if reached {
				<-ctx.Done() // the server shuts down while the job is running
				return ctx.Err()
			}
		}
		return nil
	}
	kinds := map[string]jobs.Handler{"test.items": handler}

	first := startJobApp(t, dir, kinds, jobs.Options{})
	admin := first.admin(t)
	id := first.submit(t, "test.items", 1, nil).ID
	waitFor(t, admin, id, jobs.StatusRunning)
	for {
		mu.Lock()
		n := len(processed)
		mu.Unlock()
		if n == stopAfter {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	first.close() // graceful shutdown: the job pauses, it is not failed

	// The second server picks the job up from its checkpoint.
	stopAfter = -1
	second := startJobApp(t, dir, kinds, jobs.Options{})
	j := waitFor(t, second.admin(t), id, jobs.StatusSucceeded)

	mu.Lock()
	defer mu.Unlock()
	want(t, "items processed (no item done twice)", len(processed), 5)
	for i, item := range processed {
		want(t, "item order", item, i)
	}
	want(t, "attempts", j.Attempts, 2)
	want(t, "final progress", j.Done, int64(5))
}

// A job that keeps getting interrupted (for example because it crashes the
// server) is given up, so it cannot block the queue forever.
func TestJobGivenUpAfterTooManyInterruptions(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{}, 10)
	kinds := map[string]jobs.Handler{
		"test.hang": func(ctx context.Context, run *jobs.Run) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	opts := jobs.Options{MaxAttempts: 2}

	env := startJobApp(t, dir, kinds, opts)
	env.admin(t)
	id := env.submit(t, "test.hang", 1, nil).ID
	for range 2 {
		<-started
		env.close()
		env = startJobApp(t, dir, kinds, opts)
	}
	j := waitFor(t, env.admin(t), id, jobs.StatusFailed)
	want(t, "error code", j.ErrorCode, "JOB_INTERRUPTED")
}

func TestJobVisibility(t *testing.T) {
	env := startJobApp(t, t.TempDir(), map[string]jobs.Handler{
		"test.noop": func(context.Context, *jobs.Run) error { return nil },
	}, jobs.Options{})
	admin := env.admin(t)
	var jay, ana userBody
	admin.do("POST", "/users", map[string]any{"username": "jay", "role": "user", "password": pw}, &jay)
	admin.do("POST", "/users", map[string]any{"username": "ana", "role": "user", "password": pw}, &ana)

	jayJob := env.submit(t, "test.noop", jay.ID, nil)
	env.submit(t, "test.noop", ana.ID, nil)
	waitFor(t, admin, jayJob.ID, jobs.StatusSucceeded)

	jayClient := newClient(t, env.base)
	jayClient.login("jay", pw)
	anaClient := newClient(t, env.base)
	anaClient.login("ana", pw)

	// Users see only their own jobs.
	var list struct{ Items []jobs.Job }
	jayClient.do("GET", "/jobs", nil, &list)
	if len(list.Items) != 1 || list.Items[0].ID != jayJob.ID {
		t.Errorf("jay sees %+v, want only his own job", list.Items)
	}
	// Someone else's job looks like it does not exist.
	var p problem
	status, _ := anaClient.do("GET", "/jobs/"+jayJob.ID, nil, &p)
	want(t, "other user's job status", status, 404)
	want(t, "other user's job code", p.Code, "JOB_NOT_FOUND")
	status, _ = anaClient.do("POST", "/jobs/"+jayJob.ID+"/cancel", nil, &p)
	want(t, "cancel other user's job", status, 404)
	// Only jobs:manage may list everyone's jobs.
	status, _ = anaClient.do("GET", "/jobs?all=true", nil, &p)
	want(t, "plain user all=true", status, 403)

	list.Items = nil
	admin.do("GET", "/jobs?all=true", nil, &list)
	want(t, "admin sees all jobs", len(list.Items), 2)
	status, _ = admin.do("GET", "/jobs/"+jayJob.ID, nil, nil)
	want(t, "admin reads any job", status, 200)
}
