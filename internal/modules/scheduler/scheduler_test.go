package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestIntervalTaskRunsAndNeverOverlaps(t *testing.T) {
	s := New()
	s.FirstDelay = 0
	var runs, running, overlaps atomic.Int32
	s.Add(Task{Name: "slow", Every: 5 * time.Millisecond, Run: func(context.Context) error {
		if running.Add(1) > 1 {
			overlaps.Add(1)
		}
		time.Sleep(20 * time.Millisecond) // slower than its interval
		running.Add(-1)
		runs.Add(1)
		return nil
	}})
	s.Start()
	waitUntil(t, "three runs", func() bool { return runs.Load() >= 3 })
	s.Stop()
	if overlaps.Load() != 0 {
		t.Errorf("a task ran %d times while it was still running", overlaps.Load())
	}
	st := s.Status()[0]
	if st.Runs < 3 || st.LastStart == nil || st.LastEnd == nil || st.Running {
		t.Errorf("status: %+v", st)
	}
}

func TestFailuresAndPanicsAreKeptApart(t *testing.T) {
	s := New()
	s.FirstDelay = 0
	var good atomic.Int32
	s.Add(Task{Name: "fails", Every: 10 * time.Millisecond, Run: func(context.Context) error { return errors.New("disk on fire") }})
	s.Add(Task{Name: "panics", Every: 10 * time.Millisecond, Run: func(context.Context) error { panic("boom") }})
	s.Add(Task{Name: "good", Every: 10 * time.Millisecond, Run: func(context.Context) error { good.Add(1); return nil }})
	s.Start()
	waitUntil(t, "the good task keeps running", func() bool { return good.Load() >= 3 })
	waitUntil(t, "the panicking task runs again", func() bool { return byName(s, "panics").Runs >= 2 })
	s.Stop()
	for _, st := range s.Status() {
		switch st.Name {
		case "fails":
			if st.LastError != "disk on fire" {
				t.Errorf("fails: %+v", st)
			}
		case "panics":
			if st.LastError != "panic: boom" {
				t.Errorf("panics: %+v", st)
			}
		}
	}
}

func TestStopCancelsARunningTask(t *testing.T) {
	s := New()
	s.FirstDelay = 0
	started := make(chan struct{})
	s.Add(Task{Name: "long", Every: time.Hour, Run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	s.Start()
	s.RunNow("long")
	<-started
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not end a running task")
	}
}

func TestDailySchedule(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	task := Task{Name: "nightly", Daily: 3 * time.Hour}
	for now, want := range map[time.Time]time.Time{
		time.Date(2026, 11, 22, 1, 0, 0, 0, loc):  time.Date(2026, 11, 22, 3, 0, 0, 0, loc),
		time.Date(2026, 11, 22, 3, 0, 0, 0, loc):  time.Date(2026, 11, 23, 3, 0, 0, 0, loc),
		time.Date(2026, 11, 22, 23, 0, 0, 0, loc): time.Date(2026, 11, 23, 3, 0, 0, 0, loc),
	} {
		if got := task.next(now); !got.Equal(want) {
			t.Errorf("next after %v = %v, want %v", now, got, want)
		}
	}
	if task.schedule() != "daily at 03:00" {
		t.Errorf("schedule = %q", task.schedule())
	}
}

func TestMistakesAreCaughtAtStartup(t *testing.T) {
	for name, task := range map[string]Task{
		"no schedule":   {Name: "a", Run: func(context.Context) error { return nil }},
		"both":          {Name: "a", Every: time.Hour, Daily: time.Hour, Run: func(context.Context) error { return nil }},
		"no function":   {Name: "a", Every: time.Hour},
		"daily too far": {Name: "a", Daily: 25 * time.Hour, Run: func(context.Context) error { return nil }},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			New().Add(task)
		}()
	}
}

func TestWeeklyScheduleAndCustomization(t *testing.T) {
	loc := time.FixedZone("x", 0)
	// 2026-11-24 is a Tuesday (Weekday == 2).
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, loc)
	r := rule{Enabled: true, Mode: ModeWeekly, Weekday: int(time.Sunday), DailyMinutes: 2*60 + 30}
	want := time.Date(2026, 11, 29, 2, 30, 0, 0, loc)
	if got := r.next(now); !got.Equal(want) {
		t.Fatalf("weekly next = %v, want %v", got, want)
	}
	if r.describe() != "weekly on Sun at 02:30" {
		t.Fatalf("describe = %q", r.describe())
	}

	s := New()
	s.FirstDelay = 0
	var runs atomic.Int32
	s.Add(Task{Name: "cleanup", Every: 10 * time.Millisecond, Run: func(context.Context) error {
		runs.Add(1)
		return nil
	}})

	// Disable before start: must never run automatically, but RunNow still triggers on demand.
	st, err := s.Update(context.Background(), "cleanup", UpdateRequest{
		Enabled:      false,
		Mode:         ModeInterval,
		EveryMinutes: 15,
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if st.Enabled || st.NextRun != nil || !st.Customized || st.Schedule != "every 15m0s" {
		t.Fatalf("unexpected disabled status: %+v", st)
	}

	s.Start()
	defer s.Stop()
	time.Sleep(40 * time.Millisecond)
	if runs.Load() != 0 {
		t.Fatalf("disabled task ran %d times automatically", runs.Load())
	}

	if !s.RunNow("cleanup") {
		t.Fatal("RunNow returned false")
	}
	waitUntil(t, "manual run while paused", func() bool { return runs.Load() == 1 })

	// Reset restores default enabled=true and original schedule.
	reset, err := s.Reset(context.Background(), "cleanup")
	if err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if !reset.Enabled || reset.Customized || reset.Schedule != reset.DefaultSchedule {
		t.Fatalf("unexpected reset status: %+v", reset)
	}
	waitUntil(t, "resumed task runs automatically", func() bool { return runs.Load() >= 3 })
}

func byName(s *Scheduler, name string) Status {
	for _, st := range s.Status() {
		if st.Name == name {
			return st
		}
	}
	return Status{}
}
