// Package replication copies shares one way with rsync (step 4.1): to a
// folder on this machine (a second disk), or to another NetShare Hub,
// paired by exchanging keys. Runs are background jobs (progress, cancel,
// resume after a restart); scheduled jobs start themselves; a failure
// raises an alert.
//
// Pairing, between a SENDER (this machine copies to a peer) and a RECEIVER
// (a folder on the other machine that takes the copies):
//
//  1. On the receiver, an admin adds a receiving folder and gets a one-time
//     pairing code "<receiver>-<secret>" (valid 15 minutes).
//  2. On the sender, an admin adds a peer with the receiver's address and
//     the code. The sender sends its public key, a fresh nonce and an HMAC
//     over them keyed with the secret. The secret itself never travels.
//  3. The receiver checks the HMAC, installs the key (it can only write
//     into that folder, through rrsync), burns the code, and answers with
//     its SSH host key and an HMAC over the whole exchange.
//  4. The sender checks that HMAC and pins the host key.
//
// So a stolen pairing request is useless: it holds no secret, the code is
// burned once used, and the key in it cannot be swapped without the secret
// (the HMAC would not match). A man in the middle on plain HTTP cannot
// make either side accept another key either, for the same reason.
package replication

import (
	"time"

	"netshare-hub/internal/platform/errs"
)

// Schedules.
const (
	ScheduleManual = "manual"
	ScheduleHourly = "hourly"
	ScheduleDaily  = "daily"
	ScheduleWeekly = "weekly"
)

// ReplicationJob is a replication job: what to copy, where, and when.
type ReplicationJob struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ShareID   int64  `json:"shareId"`
	ShareName string `json:"shareName" readOnly:"true"`
	// Exactly one of LocalPath and PeerID is the destination.
	LocalPath    string   `json:"localPath,omitempty" doc:"A folder on this machine (a second disk). It may not be or hold a share."`
	PeerID       int64    `json:"peerId,omitempty" doc:"A paired NetShare Hub."`
	PeerName     string   `json:"peerName,omitempty" readOnly:"true"`
	Mirror       bool     `json:"mirror" doc:"Delete files at the destination that are gone from the share. Off: deleted files are kept there."`
	BandwidthKiB int      `json:"bandwidthKiB" doc:"Bandwidth limit in KiB/s (0: no limit)."`
	Excludes     []string `json:"excludes" nullable:"false" doc:"rsync patterns of files to leave out (like *.tmp or cache/)."`
	Schedule     string   `json:"schedule" enum:"manual,hourly,daily,weekly"`
	// AtMinute is the minute of the hour (hourly) or of the day (daily,
	// weekly); Weekday 0 is Sunday.
	AtMinute  int             `json:"atMinute" minimum:"0" maximum:"1439"`
	Weekday   int             `json:"weekday" minimum:"0" maximum:"6"`
	Enabled   bool            `json:"enabled" doc:"Scheduled runs happen. Manual runs work either way."`
	NextRunAt *time.Time      `json:"nextRunAt,omitempty"`
	LastRun   *ReplicationRun `json:"lastRun,omitempty" readOnly:"true"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// ReplicationRun is one run of a job.
type ReplicationRun struct {
	ID          int64      `json:"id"`
	JobID       int64      `json:"jobId"`
	TaskID      string     `json:"taskId" doc:"The background job that runs it (see /jobs)."`
	DryRun      bool       `json:"dryRun" doc:"A preview: nothing was changed."`
	Status      string     `json:"status" enum:"queued,running,done,partial,failed,cancelled"`
	Files       int64      `json:"files" doc:"Files and folders at the source."`
	Transferred int64      `json:"transferred" doc:"Files copied (or, in a preview, that would be)."`
	Created     int64      `json:"created"`
	Deleted     int64      `json:"deleted"`
	Bytes       int64      `json:"bytes"`
	Changes     []string   `json:"changes" nullable:"false" doc:"A preview's list of changes (at most 1000), as rsync itemizes them."`
	Truncated   bool       `json:"truncated"`
	Error       string     `json:"error,omitempty"`
	QueuedAt    time.Time  `json:"queuedAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

// ReplicationPeer is a NetShare Hub this machine copies to.
type ReplicationPeer struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Host      string    `json:"host"`
	SSHPort   int       `json:"sshPort"`
	HostKey   string    `json:"hostKey" doc:"The peer's SSH host key, pinned when it was paired."`
	CreatedAt time.Time `json:"createdAt"`
}

// ReplicationReceiver is a folder on this machine that takes one sender's copies.
type ReplicationReceiver struct {
	ID       int64      `json:"id"`
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Paired   bool       `json:"paired"`
	PairedAt *time.Time `json:"pairedAt,omitempty"`
	// PairingExpires is when the current pairing code stops working.
	PairingExpires *time.Time `json:"pairingExpires,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// ReplicationStatus says what this machine can do.
type ReplicationStatus struct {
	Rsync     bool     `json:"rsync"`
	SSHClient bool     `json:"sshClient" doc:"Needed to copy to a peer."`
	SSHServer bool     `json:"sshServer" doc:"Needed to receive copies."`
	Rrsync    bool     `json:"rrsync" doc:"Needed to receive copies (comes with rsync 3.2.4 or newer)."`
	Hints     []string `json:"hints" nullable:"false"`
}

// Error codes.
const (
	CodeJobNotFound      errs.Code = "REPLICATION_JOB_NOT_FOUND"
	CodePeerNotFound     errs.Code = "REPLICATION_PEER_NOT_FOUND"
	CodeReceiverNotFound errs.Code = "REPLICATION_RECEIVER_NOT_FOUND"
	CodeUnavailable      errs.Code = "REPLICATION_UNAVAILABLE"
	CodeFolderRefused    errs.Code = "REPLICATION_FOLDER_REFUSED"
	CodePairingFailed    errs.Code = "REPLICATION_PAIRING_FAILED"
	CodePairingRefused   errs.Code = "REPLICATION_PAIRING_REFUSED"
	CodeBusy             errs.Code = "REPLICATION_BUSY"
	CodeRunFailed        errs.Code = "REPLICATION_FAILED"
	CodePartlyDone       errs.Code = "REPLICATION_PARTLY_DONE"
)

// JobKind is the background job kind of a run.
const JobKind = "replication.run"

const pairingValidity = 15 * time.Minute
