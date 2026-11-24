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
	"strconv"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/validate"
)

// Schedule modes supported for admin customization.
const (
	ModeInterval = "interval"
	ModeDaily    = "daily"
	ModeWeekly   = "weekly"
)

var weekdayNames = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

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

// rule is the active timing configuration for one task.
type rule struct {
	Enabled      bool
	Mode         string        // interval | daily | weekly
	Every        time.Duration // used when Mode == interval
	DailyMinutes int           // 0..1439, used when Mode == daily or weekly
	Weekday      int           // 0=Sun..6=Sat, used when Mode == weekly
}

func ruleFromTask(t Task) rule {
	if t.Every > 0 {
		return rule{Enabled: true, Mode: ModeInterval, Every: t.Every}
	}
	return rule{Enabled: true, Mode: ModeDaily, DailyMinutes: int(t.Daily / time.Minute)}
}

func (r rule) describe() string {
	switch r.Mode {
	case ModeDaily:
		return fmt.Sprintf("daily at %02d:%02d", r.DailyMinutes/60, r.DailyMinutes%60)
	case ModeWeekly:
		day := weekdayNames[r.Weekday%7]
		return fmt.Sprintf("weekly on %s at %02d:%02d", day, r.DailyMinutes/60, r.DailyMinutes%60)
	default:
		return "every " + r.Every.String()
	}
}

func (r rule) dailyTimeString() string {
	return fmt.Sprintf("%02d:%02d", r.DailyMinutes/60, r.DailyMinutes%60)
}

func (r rule) next(now time.Time) time.Time {
	switch r.Mode {
	case ModeDaily:
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		at := midnight.Add(time.Duration(r.DailyMinutes) * time.Minute)
		if !at.After(now) {
			at = midnight.AddDate(0, 0, 1).Add(time.Duration(r.DailyMinutes) * time.Minute)
		}
		return at
	case ModeWeekly:
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		daysUntil := (r.Weekday - int(now.Weekday()) + 7) % 7
		at := midnight.AddDate(0, 0, daysUntil).Add(time.Duration(r.DailyMinutes) * time.Minute)
		if !at.After(now) {
			at = at.AddDate(0, 0, 7)
		}
		return at
	default:
		return now.Add(r.Every)
	}
}

func (t Task) schedule() string        { return ruleFromTask(t).describe() }
func (t Task) next(now time.Time) time.Time { return ruleFromTask(t).next(now) }

// Status is what the admin sees about a task.
type Status struct {
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	Schedule        string     `json:"schedule" doc:"Human-readable schedule, e.g. \"every 1h0m0s\", \"daily at 03:00\", or \"weekly on Sun at 02:30\"."`
	DefaultSchedule string     `json:"defaultSchedule"`
	Customized      bool       `json:"customized"`
	Mode            string     `json:"mode" enum:"interval,daily,weekly"`
	EveryMinutes    int        `json:"everyMinutes,omitempty"`
	DailyTime       string     `json:"dailyTime,omitempty" doc:"Local wall-clock time HH:MM for daily or weekly mode."`
	Weekday         int        `json:"weekday,omitempty" doc:"Day of week (0=Sun..6=Sat) for weekly mode."`
	Running         bool       `json:"running"`
	Runs            int        `json:"runs"`
	LastStart       *time.Time `json:"lastStart,omitempty"`
	LastEnd         *time.Time `json:"lastEnd,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	NextRun         *time.Time `json:"nextRun,omitempty"`
}

// UpdateRequest updates whether a task is enabled and how often it runs.
type UpdateRequest struct {
	Enabled      bool   `json:"enabled" doc:"Turn this background task on or off. Disabled tasks never run automatically."`
	Mode         string `json:"mode" enum:"interval,daily,weekly" doc:"\"interval\" (every N minutes), \"daily\" (at HH:MM), or \"weekly\" (weekday + HH:MM)."`
	EveryMinutes int    `json:"everyMinutes,omitempty" minimum:"0" maximum:"10080" doc:"Interval in minutes (1–10080) when mode is \"interval\"."`
	DailyTime    string `json:"dailyTime,omitempty" doc:"Local time HH:MM (00:00–23:59) when mode is \"daily\" or \"weekly\"."`
	Weekday      int    `json:"weekday,omitempty" minimum:"0" maximum:"6" doc:"Day of week (0=Sunday..6=Saturday) when mode is \"weekly\"."`
}

// Scheduler runs the registered tasks.
type Scheduler struct {
	mu      sync.Mutex
	tasks   []*entry
	store   *Store
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	// FirstDelay is the most an interval task waits before its first run
	// (a random part of it, so tasks do not all start at once).
	FirstDelay time.Duration
}

type entry struct {
	task       Task
	defRule    rule
	curRule    rule
	customized bool
	status     Status
	now        chan struct{} // RunNow
	changed    chan struct{} // schedule or enabled flag changed
}

func (e *entry) syncStatusLocked() {
	e.status.Enabled = e.curRule.Enabled
	e.status.Schedule = e.curRule.describe()
	e.status.DefaultSchedule = e.defRule.describe()
	e.status.Customized = e.customized
	e.status.Mode = e.curRule.Mode
	if e.curRule.Mode == ModeInterval {
		mins := int(e.curRule.Every / time.Minute)
		if mins < 1 {
			mins = 1
		}
		e.status.EveryMinutes = mins
		e.status.DailyTime = ""
		e.status.Weekday = 0
	} else {
		e.status.EveryMinutes = 0
		e.status.DailyTime = e.curRule.dailyTimeString()
		if e.curRule.Mode == ModeWeekly {
			e.status.Weekday = e.curRule.Weekday
		} else {
			e.status.Weekday = 0
		}
	}
	if !e.curRule.Enabled {
		e.status.NextRun = nil
	}
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
	def := ruleFromTask(t)
	e := &entry{
		task:    t,
		defRule: def,
		curRule: def,
		status:  Status{Name: t.Name},
		now:     make(chan struct{}, 1),
		changed: make(chan struct{}, 1),
	}
	e.syncStatusLocked()
	s.tasks = append(s.tasks, e)
}

// Load attaches the database and restores any saved schedule overrides.
// Call after Add and before Start.
func (s *Scheduler) Load(ctx context.Context, database *db.DB) error {
	if database == nil {
		return nil
	}
	st := newStore(database)
	saved, err := st.loadAll(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = st
	for _, e := range s.tasks {
		r, ok := saved[e.task.Name]
		if !ok {
			continue
		}
		if parsed, ok := ruleFromSaved(r); ok {
			e.curRule = parsed
			e.customized = true
			e.syncStatusLocked()
		}
	}
	return nil
}

func ruleFromSaved(r savedRule) (rule, bool) {
	switch r.Mode {
	case ModeInterval:
		if r.EveryMinutes < 1 || r.EveryMinutes > 10080 {
			return rule{}, false
		}
		return rule{Enabled: r.Enabled, Mode: ModeInterval, Every: time.Duration(r.EveryMinutes) * time.Minute}, true
	case ModeDaily:
		if r.DailyMinutes < 0 || r.DailyMinutes >= 24*60 {
			return rule{}, false
		}
		return rule{Enabled: r.Enabled, Mode: ModeDaily, DailyMinutes: r.DailyMinutes}, true
	case ModeWeekly:
		if r.DailyMinutes < 0 || r.DailyMinutes >= 24*60 || r.Weekday < 0 || r.Weekday > 6 {
			return rule{}, false
		}
		return rule{Enabled: r.Enabled, Mode: ModeWeekly, DailyMinutes: r.DailyMinutes, Weekday: r.Weekday}, true
	default:
		return rule{}, false
	}
}

func parseHHMM(raw string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 2 || len(parts[1]) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// Update changes a task's enabled flag and schedule, persists it, and wakes
// its loop immediately so the new timer takes effect without a restart.
func (s *Scheduler) Update(ctx context.Context, name string, req UpdateRequest) (Status, error) {
	var v validate.Errors
	var nextRule rule
	nextRule.Enabled = req.Enabled
	nextRule.Mode = req.Mode

	switch req.Mode {
	case ModeInterval:
		v.Check(req.EveryMinutes >= 1 && req.EveryMinutes <= 10080, "everyMinutes", "out_of_range", "Choose between 1 minute and 7 days (10080 minutes).")
		nextRule.Every = time.Duration(req.EveryMinutes) * time.Minute
	case ModeDaily:
		mins, ok := parseHHMM(req.DailyTime)
		v.Check(ok, "dailyTime", "invalid_format", "Use 24-hour HH:MM format (e.g. 03:30).")
		nextRule.DailyMinutes = mins
	case ModeWeekly:
		mins, ok := parseHHMM(req.DailyTime)
		v.Check(ok, "dailyTime", "invalid_format", "Use 24-hour HH:MM format (e.g. 03:30).")
		v.Check(req.Weekday >= 0 && req.Weekday <= 6, "weekday", "out_of_range", "Choose a day between 0 (Sunday) and 6 (Saturday).")
		nextRule.DailyMinutes = mins
		nextRule.Weekday = req.Weekday
	default:
		v.Add("mode", "invalid_choice", `Use "interval", "daily", or "weekly".`)
	}
	if err := v.Err(); err != nil {
		return Status{}, err
	}

	s.mu.Lock()
	var target *entry
	for _, e := range s.tasks {
		if e.task.Name == name {
			target = e
			break
		}
	}
	if target == nil {
		s.mu.Unlock()
		return Status{}, errs.NotFound("TASK_NOT_FOUND", "Unknown scheduled task.")
	}
	store := s.store
	s.mu.Unlock()

	if store != nil {
		err := store.save(ctx, savedRule{
			Name:         name,
			Enabled:      nextRule.Enabled,
			Mode:         nextRule.Mode,
			EveryMinutes: int(nextRule.Every / time.Minute),
			DailyMinutes: nextRule.DailyMinutes,
			Weekday:      nextRule.Weekday,
		})
		if err != nil {
			return Status{}, err
		}
	}

	s.mu.Lock()
	target.curRule = nextRule
	target.customized = true
	target.syncStatusLocked()
	if nextRule.Enabled {
		at := nextRule.next(time.Now())
		target.status.NextRun = &at
	}
	out := target.status
	select {
	case target.changed <- struct{}{}:
	default:
	}
	s.mu.Unlock()

	slog.InfoContext(ctx, "scheduled task updated", "task", name, "enabled", nextRule.Enabled, "schedule", nextRule.describe())
	return out, nil
}

// Reset restores a task to its built-in default schedule and enabled state.
func (s *Scheduler) Reset(ctx context.Context, name string) (Status, error) {
	s.mu.Lock()
	var target *entry
	for _, e := range s.tasks {
		if e.task.Name == name {
			target = e
			break
		}
	}
	if target == nil {
		s.mu.Unlock()
		return Status{}, errs.NotFound("TASK_NOT_FOUND", "Unknown scheduled task.")
	}
	store := s.store
	s.mu.Unlock()

	if store != nil {
		if err := store.delete(ctx, name); err != nil {
			return Status{}, err
		}
	}

	s.mu.Lock()
	target.curRule = target.defRule
	target.customized = false
	target.syncStatusLocked()
	at := target.curRule.next(time.Now())
	target.status.NextRun = &at
	out := target.status
	select {
	case target.changed <- struct{}{}:
	default:
	}
	s.mu.Unlock()

	slog.InfoContext(ctx, "scheduled task reset", "task", name, "schedule", target.defRule.describe())
	return out, nil
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

// RunNow runs a task as soon as it is free (at once, if it is idle). It works
// even when the task is paused, for an admin's "run now" button and tests.
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
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}

func (s *Scheduler) loop(ctx context.Context, e *entry) {
	defer s.wg.Done()
	first := true
	for {
		s.mu.Lock()
		r := e.curRule
		s.mu.Unlock()

		if !r.Enabled {
			s.mu.Lock()
			e.status.NextRun = nil
			s.mu.Unlock()

			select {
			case <-ctx.Done():
				return
			case <-e.changed:
				first = false
				continue
			case <-e.now:
				first = false
				s.run(ctx, e)
				continue
			}
		}

		var next time.Time
		if first && r.Mode == ModeInterval {
			wait := time.Duration(0)
			if maxWait := min(s.FirstDelay, r.Every); maxWait > 0 {
				wait = rand.N(maxWait)
			}
			next = time.Now().Add(wait)
		} else {
			next = r.next(time.Now())
		}
		first = false

		s.mu.Lock()
		at := next
		e.status.NextRun = &at
		s.mu.Unlock()

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-e.changed:
			timer.Stop()
			continue
		case <-timer.C:
			s.run(ctx, e)
		case <-e.now:
			timer.Stop()
			s.run(ctx, e)
		}
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
