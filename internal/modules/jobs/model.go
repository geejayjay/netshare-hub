// Package jobs runs long work in the background: one queue for the whole
// application, with a concurrency limit, progress, cancellation and resume
// after a restart. Features never build their own background runner; they
// register a job kind here (see Service.Register) and submit jobs.
//
// Writing a job kind:
//
//	jobs.Register("files.copy", func(ctx context.Context, run *jobs.Run) error {
//		var p copyPayload
//		if err := run.Payload(&p); err != nil { return err }
//		var done copyCheckpoint
//		run.Checkpoint(&done)            // where a previous attempt stopped
//		for each item not in done {
//			if err := ctx.Err(); err != nil { return err }   // cancelled or shutting down
//			... do one item ...
//			done.add(item); run.SaveCheckpoint(done)          // so a restart resumes here
//			run.Progress(n, total, "Copying holiday.mp4")
//		}
//		return nil
//	})
//
// Rules for a handler:
//   - Stop promptly when ctx is done, and return ctx.Err() (or any error).
//   - Save a checkpoint after each unit of work. After a restart the handler
//     runs again with the last checkpoint; it must be safe to repeat the
//     unit that was in progress.
//   - Return an *errs.E to show the user a specific message; any other
//     error is logged and shown as a generic failure.
//   - Act as the job's owner (Run.OwnerID), never as an account named in
//     the payload: file operations go through the owner's file worker, so
//     the kernel applies the owner's permissions. If the owner is disabled
//     or deleted meanwhile, those operations fail and so does the job.
package jobs

import (
	"time"

	"netshare-hub/internal/platform/errs"
)

// Status is where a job is in its life.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Finished reports whether the job will not run again.
func (s Status) Finished() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Job is what users and the UI see. Payload and checkpoint stay internal.
type Job struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind" doc:"What the job does, e.g. \"files.copy\"."`
	OwnerID         int64      `json:"ownerId"`
	Title           string     `json:"title" doc:"Shown to the user, e.g. \"Copy 3 items to Media\"."`
	Status          Status     `json:"status" enum:"queued,running,succeeded,failed,cancelled"`
	Done            int64      `json:"done" doc:"Units of work done (bytes, files...)."`
	Total           int64      `json:"total" doc:"Units of work in total; 0 while unknown."`
	Message         string     `json:"message" doc:"The current step, e.g. \"Copying holiday.mp4\"."`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	Attempts        int        `json:"attempts" doc:"How many times the job was started; more than 1 means it was resumed after a restart."`
	CancelRequested bool       `json:"cancelRequested"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
}

// Error codes owned by this module.
const (
	CodeJobNotFound     errs.Code = "JOB_NOT_FOUND"
	CodeJobFailed       errs.Code = "JOB_FAILED"
	CodeJobInterrupted  errs.Code = "JOB_INTERRUPTED"
	CodeUnknownJobKind  errs.Code = "UNKNOWN_JOB_KIND"
	CodeJobAlreadyEnded errs.Code = "JOB_ALREADY_FINISHED"
)

func errNotFound() error {
	return errs.NotFound(CodeJobNotFound, "This job does not exist.")
}
