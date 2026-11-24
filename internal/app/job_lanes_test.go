package app_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/sys/sysfake"
)

// Job lanes, serial keys and unique keys.

func laneApp(t *testing.T, user, maint map[string]jobs.Handler, opts jobs.Options) (*app.App, *client) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	system := sysfake.New().System()
	opts.SaveEvery, opts.PublishEvery = time.Millisecond, time.Millisecond
	a, err := app.New(context.Background(), cfg, app.Options{System: &system, JobKinds: user, MaintenanceJobKinds: maint, JobOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Handler)
	t.Cleanup(func() { server.Close(); a.Close() })
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken
	return a, admin
}

func blocking(release chan struct{}, started chan string) jobs.Handler {
	return func(ctx context.Context, run *jobs.Run) error {
		started <- run.ID()
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func submit(t *testing.T, a *app.App, in jobs.Submit) jobs.Job {
	t.Helper()
	in.OwnerID, in.Title = 1, "Test "+in.Kind
	j, err := a.Jobs.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return j
}

// A long maintenance job (a nightly copy) never makes user work wait.
func TestJobLanes(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 10)
	a, admin := laneApp(t,
		map[string]jobs.Handler{"test.user": func(context.Context, *jobs.Run) error { return nil }},
		map[string]jobs.Handler{"test.long": blocking(release, started)},
		jobs.Options{MaxConcurrent: 1, MaintenanceConcurrent: 1})
	defer close(release)

	long1 := submit(t, a, jobs.Submit{Kind: "test.long"})
	long2 := submit(t, a, jobs.Submit{Kind: "test.long"})
	<-started
	for i := 0; i < 3; i++ {
		u := submit(t, a, jobs.Submit{Kind: "test.user"})
		waitFor(t, admin, u.ID, jobs.StatusSucceeded)
	}
	// The maintenance lane still runs one at a time.
	var j jobs.Job
	admin.do("GET", "/jobs/"+long2.ID, nil, &j)
	want(t, "second long job", j.Status, jobs.StatusQueued)
	_ = long1
}

// Jobs with the same serial key never run together, and one waiting for
// its key does not hold a slot: the next free job starts instead.
func TestJobSerialKey(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 10)
	var running, peakSame atomic.Int32
	h := blocking(release, started)
	a, admin := laneApp(t, nil, map[string]jobs.Handler{"test.copy": func(ctx context.Context, run *jobs.Run) error {
		var p struct{ Key string }
		_ = run.Payload(&p)
		if p.Key == "A" {
			n := running.Add(1)
			defer running.Add(-1)
			if n > peakSame.Load() {
				peakSame.Store(n)
			}
		}
		return h(ctx, run)
	}}, jobs.Options{MaintenanceConcurrent: 2})

	a1 := submit(t, a, jobs.Submit{Kind: "test.copy", SerialKey: "A", Payload: map[string]string{"Key": "A"}})
	a2 := submit(t, a, jobs.Submit{Kind: "test.copy", SerialKey: "A", Payload: map[string]string{"Key": "A"}})
	b1 := submit(t, a, jobs.Submit{Kind: "test.copy", SerialKey: "B", Payload: map[string]string{"Key": "B"}})
	got := map[string]bool{<-started: true, <-started: true}
	if !got[a1.ID] || !got[b1.ID] {
		t.Fatalf("started %v, want the first A and B (A's second waits without a slot)", got)
	}
	var j jobs.Job
	admin.do("GET", "/jobs/"+a2.ID, nil, &j)
	want(t, "second job for key A", j.Status, jobs.StatusQueued)
	close(release)
	waitFor(t, admin, a2.ID, jobs.StatusSucceeded)
	want(t, "jobs for key A at once", peakSame.Load(), int32(1))
}

// A unique key admits one queued-or-running job, atomically, even when
// many submits race; after it ends, the next one is admitted.
func TestJobUniqueKey(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 10)
	a, admin := laneApp(t, nil, map[string]jobs.Handler{"test.run": blocking(release, started)}, jobs.Options{})

	var ok, dup atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.Jobs.Submit(context.Background(), jobs.Submit{Kind: "test.run", OwnerID: 1, Title: "t", UniqueKey: "job:1"})
			switch {
			case err == nil:
				ok.Add(1)
			case errs.Is(err, jobs.CodeJobDuplicate):
				dup.Add(1)
			default:
				t.Errorf("Submit: %v", err)
			}
		}()
	}
	wg.Wait()
	want(t, "admitted", ok.Load(), int32(1))
	want(t, "refused", dup.Load(), int32(19))
	id := <-started
	close(release)
	waitFor(t, admin, id, jobs.StatusSucceeded)
	if _, err := a.Jobs.Submit(context.Background(), jobs.Submit{Kind: "test.run", OwnerID: 1, Title: "t", UniqueKey: "job:1"}); err != nil {
		t.Errorf("after the first ended: %v", err)
	}
	// A cancelled queued job frees its key at once.
	j, _ := a.Jobs.Submit(context.Background(), jobs.Submit{Kind: "test.run", OwnerID: 1, Title: "t", UniqueKey: "job:2"})
	admin.do("POST", "/jobs/"+j.ID+"/cancel", nil, nil)
	if _, err := a.Jobs.Submit(context.Background(), jobs.Submit{Kind: "test.run", OwnerID: 1, Title: "t", UniqueKey: "job:2"}); err != nil {
		t.Errorf("after cancelling the queued one: %v", err)
	}
}
