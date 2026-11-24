package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

// Publisher sends live events (the events hub).
type Publisher interface {
	PublishTo(eventType string, payload any, allow func(*httpx.Principal) bool)
}

// Retention is how long an alert that ended is kept.
const Retention = 30 * 24 * time.Hour

// Limits on what a feature may put in an alert.
const (
	maxTitle   = 200
	maxMessage = 2000
	maxKey     = 200
	maxDetails = 8 << 10
	// publishEvery is the fastest the same alert is re-sent live: a burst
	// counts up in the database but does not flood the browsers.
	publishEvery = time.Second
)

// Service holds the alert rules.
type Service struct {
	repo   *Repo
	events Publisher
	now    func() time.Time

	mu        sync.Mutex
	published map[int64]time.Time
	onRaise   func(Alert)
}

func NewService(repo *Repo, events Publisher) *Service {
	return &Service{repo: repo, events: events, now: time.Now, published: map[int64]time.Time{}}
}

// SetOnRaise registers a callback fired whenever a new alert is raised (Count == 1).
func (s *Service) SetOnRaise(fn func(Alert)) {
	s.mu.Lock()
	s.onRaise = fn
	s.mu.Unlock()
}

// canSee is who receives alerts: those who may see the system's state.
func canSee(p *httpx.Principal) bool { return p != nil && p.Can(rbac.SystemRead) }

func (s *Service) publish(a Alert, force bool) {
	s.mu.Lock()
	last, seen := s.published[a.ID]
	due := force || !seen || s.now().Sub(last) >= publishEvery
	if due {
		s.published[a.ID] = s.now()
	}
	if !a.Active {
		delete(s.published, a.ID)
	}
	s.mu.Unlock()
	if due && s.events != nil {
		s.events.PublishTo("alert", a, canSee)
	}
}

// Raise records an alert, or counts up the active one with the same kind
// and key. It never fails the caller's work: a bad or failing alert is
// logged, and the error is returned for callers that care.
func (s *Service) Raise(ctx context.Context, in Raise) (Alert, error) {
	details := "{}"
	if in.Details != nil {
		raw, err := json.Marshal(in.Details)
		if err != nil {
			return Alert{}, fmt.Errorf("alerts: encode details: %w", err)
		}
		details = string(raw)
	}
	switch {
	case in.Kind == "" || len(in.Kind) > 64:
		return Alert{}, fmt.Errorf("alerts: bad kind %q", in.Kind)
	case len(in.Key) > maxKey || !utf8.ValidString(in.Key):
		return Alert{}, fmt.Errorf("alerts: bad key for %s", in.Kind)
	case !in.Severity.valid() || !in.Source.valid():
		return Alert{}, fmt.Errorf("alerts: bad severity or source for %s", in.Kind)
	case in.Title == "" || utf8.RuneCountInString(in.Title) > maxTitle:
		return Alert{}, fmt.Errorf("alerts: bad title for %s", in.Kind)
	case utf8.RuneCountInString(in.Message) > maxMessage:
		return Alert{}, fmt.Errorf("alerts: message too long for %s", in.Kind)
	case len(details) > maxDetails:
		return Alert{}, fmt.Errorf("alerts: details too large for %s", in.Kind)
	}
	a, err := s.repo.raise(ctx, in, details, s.now())
	if err != nil {
		slog.ErrorContext(ctx, "could not record an alert", "kind", in.Kind, "key", in.Key, "err", err)
		return Alert{}, err
	}
	level := slog.LevelInfo
	if in.Severity == Error || in.Severity == Critical {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "alert", "kind", a.Kind, "key", a.Key, "severity", a.Severity, "count", a.Count, "title", a.Title)
	s.publish(a, a.Count == 1)
	if a.Count == 1 {
		s.mu.Lock()
		fn := s.onRaise
		s.mu.Unlock()
		if fn != nil {
			fn(a)
		}
	}
	return a, nil
}

// Resolve ends the active alert of this kind and key, if there is one: the
// cause went away (Samba reloads again, the ban expired).
func (s *Service) Resolve(ctx context.Context, kind, key string) error {
	a, ok, err := s.repo.resolve(ctx, kind, key, s.now())
	if err != nil || !ok {
		return err
	}
	slog.InfoContext(ctx, "alert resolved", "kind", kind, "key", key)
	s.publish(a, true)
	return nil
}

func (s *Service) List(ctx context.Context, f Filter, p page.Request) ([]Alert, int, error) {
	return s.repo.list(ctx, f, p)
}

func (s *Service) Get(ctx context.Context, id int64) (Alert, error) {
	a, ok, err := s.repo.get(ctx, id)
	if err != nil {
		return Alert{}, err
	}
	if !ok {
		return Alert{}, errNotFound()
	}
	return a, nil
}

// Acknowledge marks an alert as seen by an admin; it is no longer active.
// Acknowledging twice keeps the first time and admin.
func (s *Service) Acknowledge(ctx context.Context, actor *httpx.Principal, id int64) (Alert, error) {
	a, ok, err := s.repo.acknowledge(ctx, id, actor.UserID, s.now())
	if err != nil {
		return Alert{}, err
	}
	if !ok {
		return Alert{}, errNotFound()
	}
	// Until the audit log exists (1.15), the server log is the record.
	slog.InfoContext(ctx, "alert acknowledged", "alert", a.ID, "kind", a.Kind, "key", a.Key, "by", actor.Username)
	s.publish(a, true)
	return a, nil
}

// Delete removes an alert from the list.
func (s *Service) Delete(ctx context.Context, actor *httpx.Principal, id int64) error {
	ok, err := s.repo.delete(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errNotFound()
	}
	slog.InfoContext(ctx, "alert removed", "alert", id, "by", actor.Username)
	return nil
}

// Prune removes alerts that ended more than Retention ago (a scheduled task).
func (s *Service) Prune(ctx context.Context) error {
	n, err := s.repo.pruneBefore(ctx, s.now().Add(-Retention))
	if err == nil && n > 0 {
		slog.InfoContext(ctx, "old alerts removed", "count", n)
	}
	return err
}
