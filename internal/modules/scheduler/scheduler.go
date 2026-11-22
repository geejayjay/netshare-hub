// Package scheduler runs everything timed: cleanups, retention, rescans,
// replication schedules. ONE scheduler for the whole application; features
// register a task instead of running their own timer.
//
// Each task runs in its own goroutine, never twice at once (a slow run
// delays the next instead of piling up), and a failing or panicking task is
// logged and tried again at its next time. Long work does not belong here:
// a task that has much to do submits a job (see the jobs module).
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"slices"
	"sync"
	"time"
)

// Task is one scheduled piece of work.
type Task struct {
	Name string
	// Every runs the task at this interval. Daily, when set instead, runs
	// it once a day at that time after local midnight (e.g. 3*time.Hour).
	Every time.Duration
	Daily time.Duration
	// Run does the work. ctx ends when the application stops.
	Run func(ctx context.Context) error
}

func (t Task) schedule() string {
	if t.Every > 0 {
		return "every " + t.Every.String()
	}
	return fmt.Sprintf("daily at %02d:%02d", int(t.Daily.Hours()), int(t.Daily.Minutes())%60)
}

// next is when the task runs after now.
func (t Task) next(now time.Time) time.Time {
	if t.Every > 0 {
		return now.Add(t.Every)
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := midnight.Add(t.Daily)
	if !at.After(now) {
		at = midnight.AddDate(0, 0, 1).Add(t.Daily)
	}
	return at
}

// Status is what the admin sees about a task.
type Status struct {
	Name      string     `json:"name"`
	Schedule  string     `json:"schedule" doc:"\"every 1h0m0s\" or \"daily at 03:00\"."`
	Running   bool       `json:"running"`
	Runs      int        `json:"runs"`
	LastStart *time.Time `json:"lastStart,omitempty"`
	LastEnd   *time.Time `json:"lastEnd,omitempty"`
	LastError string     `json:"lastError,omitempty"`
	NextRun   *time.Time `json:"nextRun,omitempty"`
}

// Scheduler runs the registered tasks.
type Scheduler struct {
	mu      sync.Mutex
	tasks   []*entry
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	// FirstDelay is the most an interval task waits before its first run
	// (a random part of it, so tasks do not all start at once).
	FirstDelay time.Duration
}

type entry struct {
	task   Task
	status Status
	now    chan struct{} // RunNow
}

func New() *Scheduler { return &Scheduler{FirstDelay: time.Minute} }

// Add registers a task. It panics on a mistake (bad schedule, duplicate
// name, after Start), which is a programming error found at startup.
func (s *Scheduler) Add(t Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.started:
		panic("scheduler: Add after Start: " + t.Name)
	case t.Name == "" || t.Run == nil:
		panic("scheduler: a task needs a name and a Run function")
	case (t.Every > 0) == (t.Daily > 0) || t.Daily >= 24*time.Hour || t.Every < 0 || t.Daily < 0:
		panic("scheduler: task " + t.Name + " needs exactly one of Every or Daily (under 24h)")
	}
	for _, e := range s.tasks {
		if e.task.Name == t.Name {
			panic("scheduler: duplicate task " + t.Name)
		}
	}
	s.tasks = append(s.tasks, &entry{task: t, status: Status{Name: t.Name, Schedule: t.schedule()}, now: make(chan struct{}, 1)})
}

// Start runs every task on its schedule until Stop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	for _, e := range s.tasks {
		s.wg.Add(1)
		go s.loop(ctx, e)
	}
}

// Stop cancels running tasks and waits for them to end.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// RunNow runs a task as soon as it is free (at once, if it is idle). It is
// for an admin's "run now" button, and for tests.
func (s *Scheduler) RunNow(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.tasks {
		if e.task.Name == name {
			select {
			case e.now <- struct{}{}:
			default: // already asked
			}
			return true
		}
	}
	return false
}

// Status lists every task, by name.
func (s *Scheduler) Status() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.tasks))
	for _, e := range s.tasks {
		out = append(out, e.status)
	}
	slices.SortFunc(out, func(a, b Status) int {
		if a.Name < b.Name {
			return -1
		}
		return 1
	})
	return out
}

func (s *Scheduler) loop(ctx context.Context, e *entry) {
	defer s.wg.Done()
	var next time.Time
	if e.task.Every > 0 {
		wait := time.Duration(0)
		if first := min(s.FirstDelay, e.task.Every); first > 0 {
			wait = rand.N(first)
		}
		next = time.Now().Add(wait)
	} else {
		next = e.task.next(time.Now())
	}
	for {
		s.mu.Lock()
		at := next
		e.status.NextRun = &at
		s.mu.Unlock()

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-e.now:
			timer.Stop()
		}
		s.run(ctx, e)
		next = e.task.next(time.Now())
	}
}

func (s *Scheduler) run(ctx context.Context, e *entry) {
	start := time.Now()
	s.mu.Lock()
	e.status.Running, e.status.LastStart = true, &start
	s.mu.Unlock()

	err := call(ctx, e.task.Run)

	end := time.Now()
	s.mu.Lock()
	e.status.Running, e.status.LastEnd = false, &end
	e.status.Runs++
	e.status.LastError = ""
	if err != nil {
		e.status.LastError = err.Error()
	}
	s.mu.Unlock()

	if err != nil && ctx.Err() == nil {
		slog.Warn("scheduled task failed", "task", e.task.Name, "err", err, "duration_ms", end.Sub(start).Milliseconds())
	} else {
		slog.Debug("scheduled task done", "task", e.task.Name, "duration_ms", end.Sub(start).Milliseconds())
	}
}

// call runs a task; a panic becomes an error, and the scheduler goes on.
func call(ctx context.Context, run func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("scheduled task panicked", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return run(ctx)
}
