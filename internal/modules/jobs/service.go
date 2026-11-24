package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Handler does the work of one job kind. See the package documentation.
type Handler func(ctx context.Context, run *Run) error

// Lanes keep kinds of work from waiting behind each other. Each lane has
// its own slots: a copy that takes hours in the maintenance lane never
// makes a user's file copy wait.
const (
	// LaneUser is for work a user is waiting on (file copies, moves,
	// deletes). The default lane.
	LaneUser = "user"
	// LaneMaintenance is for long background work (replication,
	// permission fixes).
	LaneMaintenance = "maintenance"
)

// Options tune the engine. Zero values fall back to DefaultOptions.
type Options struct {
	// MaxConcurrent is how many jobs of the user lane run at the same time.
	MaxConcurrent int
	// MaintenanceConcurrent is the same for the maintenance lane.
	MaintenanceConcurrent int
	// MaxAttempts is how often a job may be interrupted by a restart before
	// it is given up as failed (so a job that crashes the server cannot
	// bring it down forever).
	MaxAttempts int
	// Retention is how long finished jobs are kept.
	Retention time.Duration
	// SaveEvery and PublishEvery limit how often progress is written to the
	// database and sent to browsers, however often a handler reports it.
	SaveEvery    time.Duration
	PublishEvery time.Duration
}

func DefaultOptions() Options {
	return Options{
		MaxConcurrent:         2,
		MaintenanceConcurrent: 1,
		MaxAttempts:           5,
		Retention:             7 * 24 * time.Hour,
		SaveEvery:             time.Second,
		PublishEvery:          250 * time.Millisecond,
	}
}

// Publisher sends live updates to the browsers that may see them.
type Publisher interface {
	PublishTo(event string, payload any, allow func(*httpx.Principal) bool)
}

// EventJob is the live event sent whenever a job changes.
const EventJob = "job"

var (
	errCancelled = errors.New("cancelled by the user")
	errShutdown  = errors.New("server is shutting down")
)

// Service is the job engine.
type Service struct {
	repo   *Repo
	opts   Options
	events Publisher
	now    func() time.Time

	mu       sync.Mutex
	handlers map[string]Handler
	lanes    map[string]string // kind -> lane
	running  map[string]*running
	started  bool
	stopping bool
	wake     chan struct{}
	stop     chan struct{}
	wg       sync.WaitGroup
}

// NewService creates the engine. events may be nil.
func NewService(repo *Repo, events Publisher, opts Options) *Service {
	def := DefaultOptions()
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = def.MaxConcurrent
	}
	if opts.MaintenanceConcurrent <= 0 {
		opts.MaintenanceConcurrent = def.MaintenanceConcurrent
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = def.MaxAttempts
	}
	if opts.Retention <= 0 {
		opts.Retention = def.Retention
	}
	if opts.SaveEvery <= 0 {
		opts.SaveEvery = def.SaveEvery
	}
	if opts.PublishEvery <= 0 {
		opts.PublishEvery = def.PublishEvery
	}
	return &Service{
		repo: repo, opts: opts, events: events, now: time.Now,
		handlers: map[string]Handler{},
		lanes:    map[string]string{},
		running:  map[string]*running{},
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
	}
}

// running is a job being run now.
type running struct {
	cancel context.CancelCauseFunc
	lane   string
	key    string // serial key, "" for none
}

// Register adds a job kind in the user lane. Call it while wiring the
// application, before Start. Kind names look like "module.action", e.g.
// "files.copy".
func (s *Service) Register(kind string, h Handler) { s.RegisterIn(LaneUser, kind, h) }

// RegisterIn adds a job kind in a lane.
func (s *Service) RegisterIn(lane, kind string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		panic("jobs: Register after Start: " + kind)
	}
	if _, dup := s.handlers[kind]; dup {
		panic("jobs: kind registered twice: " + kind)
	}
	if lane != LaneUser && lane != LaneMaintenance {
		panic("jobs: unknown lane " + lane)
	}
	s.handlers[kind] = h
	s.lanes[kind] = lane
}

// Submit is what a feature passes to start a job.
type Submit struct {
	Kind    string
	OwnerID int64
	// Title is shown to the user, e.g. "Copy 3 items to Media".
	Title string
	// Payload says what to do. It is stored as JSON and handed to the
	// handler, also after a restart, so it must hold everything needed.
	Payload any
	// SerialKey, when set, keeps jobs with the same key from running at the
	// same time (one copy per destination). A job waiting for its key does
	// not take a slot: the next job that is free to run starts instead.
	SerialKey string
	// UniqueKey, when set, refuses the job (CodeJobDuplicate) while a job
	// of the same kind with the same key is queued or running.
	UniqueKey string
}

const maxTitleLen = 200

// Submit queues a job and returns it.
func (s *Service) Submit(ctx context.Context, in Submit) (Job, error) {
	s.mu.Lock()
	_, known := s.handlers[in.Kind]
	s.mu.Unlock()
	if !known {
		return Job{}, errs.Internal(fmt.Errorf("jobs: unknown kind %q", in.Kind))
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || utf8.RuneCountInString(title) > maxTitleLen {
		return Job{}, validate.New().Add("title", "invalid", "A job needs a title of at most 200 characters.").Err()
	}
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return Job{}, errs.Internal(fmt.Errorf("jobs: encode payload: %w", err))
	}

	if len(in.SerialKey) > 300 || len(in.UniqueKey) > 300 {
		return Job{}, errs.Internal(errors.New("jobs: key too long"))
	}
	job := Job{ID: newID(), Kind: in.Kind, OwnerID: in.OwnerID, Title: title, Status: StatusQueued, CreatedAt: s.now().UTC().Truncate(time.Second)}
	if err := s.repo.Insert(ctx, job, string(payload), in.SerialKey, in.UniqueKey); err != nil {
		if errors.Is(err, errDuplicate) {
			return Job{}, errs.Conflict(CodeJobDuplicate, "This job is already queued or running.")
		}
		return Job{}, err
	}
	slog.InfoContext(ctx, "job queued", "job", job.ID, "kind", job.Kind, "owner", job.OwnerID)
	s.publish(job)
	s.poke()
	return job, nil
}

// Start resumes interrupted jobs and begins running queued ones. It returns
// at once; Stop ends it.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("jobs: already started")
	}
	s.started = true
	kinds := s.kindsLocked()
	s.mu.Unlock()

	// Jobs that were running when the server stopped: resume or give up.
	interrupted, err := s.repo.Interrupted(ctx)
	if err != nil {
		return err
	}
	for _, j := range interrupted {
		switch {
		case j.CancelRequested:
			err = s.repo.Finish(ctx, j.ID, StatusCancelled, "", "", s.now())
		case j.Attempts >= s.opts.MaxAttempts:
			slog.WarnContext(ctx, "job given up after repeated interruptions", "job", j.ID, "kind", j.Kind, "attempts", j.Attempts)
			err = s.repo.Finish(ctx, j.ID, StatusFailed, string(CodeJobInterrupted),
				"The job was interrupted too many times and was stopped.", s.now())
		default:
			slog.InfoContext(ctx, "resuming interrupted job", "job", j.ID, "kind", j.Kind, "attempt", j.Attempts+1)
			err = s.repo.Requeue(ctx, j.ID)
		}
		if err != nil {
			return err
		}
	}
	if err := s.repo.FailUnknownKinds(ctx, kinds, s.now()); err != nil {
		return err
	}
	if err := s.Cleanup(ctx); err != nil {
		slog.WarnContext(ctx, "could not remove old jobs", "err", err)
	}

	s.wg.Add(1)
	go s.dispatch()
	s.poke()
	return nil
}

// Stop stops taking new work and asks running jobs to stop. They stay
// "running" in the database and resume, from their last checkpoint, at the
// next Start. It waits until they have returned or ctx ends.
func (s *Service) Stop(ctx context.Context) {
	s.mu.Lock()
	if !s.started || s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	close(s.stop)
	for _, r := range s.running {
		r.cancel(errShutdown)
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("jobs still running at shutdown; they will resume at the next start")
	}
}

// Cancel stops a job. Users may cancel their own jobs; jobs:manage may
// cancel anyone's. Someone else's job is reported as not found.
func (s *Service) Cancel(ctx context.Context, actor *httpx.Principal, id string) (Job, error) {
	j, err := s.Get(ctx, actor, id)
	if err != nil {
		return Job{}, err
	}
	if j.Status.Finished() {
		return Job{}, errs.Conflict(CodeJobAlreadyEnded, "This job has already finished.")
	}
	wasQueued, err := s.repo.RequestCancel(ctx, id, s.now())
	if err != nil {
		return Job{}, err
	}
	if !wasQueued {
		s.mu.Lock()
		if r, ok := s.running[id]; ok {
			r.cancel(errCancelled)
		}
		s.mu.Unlock()
	}
	slog.InfoContext(ctx, "job cancel requested", "job", id, "by", actor.Username)
	j, _, err = s.repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	s.publish(j)
	return j, nil
}

// Get returns a job the actor may see.
func (s *Service) Get(ctx context.Context, actor *httpx.Principal, id string) (Job, error) {
	j, ok, err := s.repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !ok || !visible(actor, j) {
		return Job{}, errNotFound()
	}
	return j, nil
}

// List returns jobs, newest first: the actor's own, or with all (and the
// jobs:manage permission) everyone's.
func (s *Service) List(ctx context.Context, actor *httpx.Principal, all, active bool, p page.Request) ([]Job, int, error) {
	owner := actor.UserID
	if all {
		if !actor.Can(rbac.JobsManage) {
			return nil, 0, errs.Forbidden(errs.CodeForbidden, "You can only see your own jobs.")
		}
		owner = 0
	}
	return s.repo.List(ctx, owner, active, p)
}

func visible(actor *httpx.Principal, j Job) bool {
	return actor != nil && (actor.UserID == j.OwnerID || actor.Can(rbac.JobsManage))
}

// --- the runner ---

func (s *Service) dispatch() {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second) // catches anything missed
	defer ticker.Stop()
	for {
		s.fill()
		select {
		case <-s.wake:
		case <-ticker.C:
		case <-s.stop:
			return
		}
	}
}

// fill starts queued jobs in every lane until its slots are full. In each
// lane the oldest job that is free to run starts first: a job whose serial
// key is taken waits without holding a slot.
func (s *Service) fill() {
	for _, lane := range []string{LaneUser, LaneMaintenance} {
		for {
			s.mu.Lock()
			limit := s.opts.MaxConcurrent
			if lane == LaneMaintenance {
				limit = s.opts.MaintenanceConcurrent
			}
			n, busy := 0, []string{}
			for _, r := range s.running {
				if r.lane == lane {
					n++
				}
				if r.key != "" {
					busy = append(busy, r.key)
				}
			}
			var kinds []string
			for _, k := range s.kindsLocked() {
				if s.lanes[k] == lane {
					kinds = append(kinds, k)
				}
			}
			stopping := s.stopping
			s.mu.Unlock()
			if stopping || n >= limit {
				break
			}
			job, payload, checkpoint, ok, err := s.repo.Claim(context.Background(), kinds, busy, s.now())
			if err != nil {
				slog.Error("could not take the next job", "err", err)
				return
			}
			if !ok {
				break
			}
			s.launch(job, payload, checkpoint, lane)
		}
	}
}

func (s *Service) launch(job Job, payload, checkpoint, lane string) {
	// Jobs run detached from any request, and as background work: file
	// operations they do through a file worker get low IO priority.
	ctx, cancel := context.WithCancelCause(sys.WithBackground(context.Background()))

	s.mu.Lock()
	handler := s.handlers[job.Kind]
	if s.stopping {
		s.mu.Unlock()
		cancel(errShutdown)
		_ = s.repo.Requeue(context.Background(), job.ID)
		return
	}
	s.running[job.ID] = &running{cancel: cancel, lane: lane, key: job.serialKey}
	if job.CancelRequested {
		cancel(errCancelled)
	}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer s.poke()
		defer func() {
			s.mu.Lock()
			delete(s.running, job.ID)
			s.mu.Unlock()
			cancel(nil)
		}()

		run := &Run{s: s, job: job, payload: payload, checkpoint: checkpoint}
		slog.Info("job started", "job", job.ID, "kind", job.Kind, "attempt", job.Attempts)
		s.publish(job)

		err := call(ctx, handler, run)
		run.flush(true)
		s.finish(ctx, job, err)
	}()
}

// call runs a handler, turning a panic into an error so one bad job cannot
// take the server down.
func call(ctx context.Context, h Handler, run *Run) (err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("job panicked", "job", run.job.ID, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return h(ctx, run)
}

// finish records how a job ended.
func (s *Service) finish(ctx context.Context, job Job, runErr error) {
	bg := context.Background()
	cause := context.Cause(ctx)

	var status Status
	var code, message string
	switch {
	case runErr == nil:
		status = StatusSucceeded
	case errors.Is(cause, errShutdown):
		// Not an ending: the job stays "running" and resumes at next start.
		slog.Info("job paused for shutdown", "job", job.ID)
		return
	case errors.Is(cause, errCancelled):
		status = StatusCancelled
	default:
		status = StatusFailed
		e := errs.From(runErr)
		if e.Kind == errs.KindInternal {
			slog.Error("job failed", "job", job.ID, "kind", job.Kind, "err", runErr)
			code, message = string(CodeJobFailed), "The job failed. Check the logs for details."
		} else {
			slog.Warn("job failed", "job", job.ID, "kind", job.Kind, "code", e.Code, "err", runErr)
			code, message = string(e.Code), e.Message
		}
	}

	if err := s.repo.Finish(bg, job.ID, status, code, message, s.now()); err != nil {
		slog.Error("could not record the end of a job", "job", job.ID, "err", err)
		return
	}
	slog.Info("job finished", "job", job.ID, "kind", job.Kind, "status", status)
	if j, ok, err := s.repo.Get(bg, job.ID); err == nil && ok {
		s.publish(j)
	}
}

// Cleanup removes finished jobs older than the retention time (a scheduled
// task).
func (s *Service) Cleanup(ctx context.Context) error {
	n, err := s.repo.DeleteFinishedBefore(ctx, s.now().Add(-s.opts.Retention))
	if err == nil && n > 0 {
		slog.InfoContext(ctx, "removed old jobs", "count", n)
	}
	return err
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// kindsLocked returns the registered kinds. Callers hold s.mu.
func (s *Service) kindsLocked() []string {
	kinds := make([]string, 0, len(s.handlers))
	for k := range s.handlers {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// publish sends a job to its owner and to users who manage all jobs.
func (s *Service) publish(j Job) {
	if s.events == nil {
		return
	}
	s.events.PublishTo(EventJob, j, func(p *httpx.Principal) bool { return visible(p, j) })
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Active reports whether a job of the kind with this unique key is queued
// or running.
func (s *Service) Active(ctx context.Context, kind, uniqueKey string) (bool, error) {
	return s.repo.Active(ctx, kind, uniqueKey)
}

// Statuses returns the status of each job id that still exists (a job
// that is not in the map was removed: its owner was deleted, or it was
// older than the retention time).
func (s *Service) Statuses(ctx context.Context, ids []string) (map[string]Status, error) {
	return s.repo.Statuses(ctx, ids)
}

// ActiveCount is how many jobs are queued or running (a config restore
// waits until there are none: it replaces the users the jobs belong to).
func (s *Service) ActiveCount(ctx context.Context) (int, error) {
	var n int
	err := s.repo.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE status IN ('queued', 'running')").Scan(&n)
	return n, err
}
