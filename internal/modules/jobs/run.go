package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"
)

// Run is a handler's view of the job it is running.
type Run struct {
	s          *Service
	job        Job
	payload    string
	checkpoint string

	mu        sync.Mutex
	done      int64
	total     int64
	message   string
	dirty     bool
	lastSave  time.Time
	lastEvent time.Time
}

// ID is the job's id.
func (r *Run) ID() string { return r.job.ID }

// OwnerID is the user the job runs for. Handlers that touch files do so as
// this user (through the user's file worker), never as the server.
func (r *Run) OwnerID() int64 { return r.job.OwnerID }

// Attempt is 1 the first time the job runs, and higher when it is resumed
// after a restart.
func (r *Run) Attempt() int { return r.job.Attempts }

// Payload decodes what the job should do.
func (r *Run) Payload(into any) error {
	if err := json.Unmarshal([]byte(r.payload), into); err != nil {
		return fmt.Errorf("jobs: decode payload: %w", err)
	}
	return nil
}

// Checkpoint decodes the last saved checkpoint. It returns false when there
// is none (the job is starting fresh).
func (r *Run) Checkpoint(into any) bool {
	r.mu.Lock()
	raw := r.checkpoint
	r.mu.Unlock()
	if raw == "" {
		return false
	}
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		slog.Warn("ignoring unreadable job checkpoint", "job", r.job.ID, "err", err)
		return false
	}
	return true
}

// SaveCheckpoint records how far the job got. It is written at once, so a
// restart resumes from here. Keep it small: it is rewritten every time.
func (r *Run) SaveCheckpoint(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("jobs: encode checkpoint: %w", err)
	}
	if err := r.s.repo.SaveCheckpoint(context.Background(), r.job.ID, string(raw)); err != nil {
		return err
	}
	r.mu.Lock()
	r.checkpoint = string(raw)
	r.mu.Unlock()
	r.flush(false)
	return nil
}

const maxMessageLen = 300

// Progress reports how far the job is. total may be 0 while unknown. Call
// it as often as convenient: writes and live updates are rate-limited.
func (r *Run) Progress(done, total int64, message string) {
	if utf8.RuneCountInString(message) > maxMessageLen {
		message = string([]rune(message)[:maxMessageLen])
	}
	r.mu.Lock()
	r.done, r.total, r.message, r.dirty = done, total, message, true
	r.mu.Unlock()
	r.flush(false)
}

// flush writes and publishes progress when it is due (or always, when
// force is set at the end of the job).
func (r *Run) flush(force bool) {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	now := time.Now()
	save := force || now.Sub(r.lastSave) >= r.s.opts.SaveEvery
	event := force || now.Sub(r.lastEvent) >= r.s.opts.PublishEvery
	done, total, message := r.done, r.total, r.message
	if save {
		r.lastSave, r.dirty = now, false
	}
	if event {
		r.lastEvent = now
	}
	r.mu.Unlock()

	if save {
		if err := r.s.repo.SaveProgress(context.Background(), r.job.ID, done, total, message); err != nil {
			slog.Warn("could not save job progress", "job", r.job.ID, "err", err)
		}
	}
	if event {
		j := r.job
		j.Status, j.Done, j.Total, j.Message = StatusRunning, done, total, message
		r.s.publish(j)
	}
}
