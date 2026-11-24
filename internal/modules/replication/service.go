package replication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/modules/shares"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/ratelimit"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// Shares is what replication needs from the shares module.
type Shares interface {
	Get(ctx context.Context, id int64) (shares.Share, error)
	Paths(ctx context.Context) (map[string]string, error)
}

// Jobs is the background job queue.
type Jobs interface {
	Submit(ctx context.Context, in jobs.Submit) (jobs.Job, error)
	Statuses(ctx context.Context, ids []string) (map[string]jobs.Status, error)
}

// Alerts raises "a replication failed".
type Alerts interface {
	Raise(ctx context.Context, in alerts.Raise) (alerts.Alert, error)
	Resolve(ctx context.Context, kind, key string) error
}

const alertFailed = "replication.failed"

// keepRuns is how many runs of each job are kept.
const keepRuns = 100

type Service struct {
	repo      *repo
	shares    Shares
	jobs      Jobs
	alerts    Alerts
	system    sys.Replication
	protected []string
	now       func() time.Time
	pairer    pairClient
	limiter   *ratelimit.Limiter

	// mu serializes changes to peers and receivers (database + root side).
	mu sync.Mutex
}

func NewService(d *db.DB, s Shares, j Jobs, a Alerts, system sys.Replication, protected ...string) *Service {
	return &Service{repo: &repo{db: d}, shares: s, jobs: j, alerts: a, system: system, protected: protected, now: time.Now,
		pairer: httpPairer(), limiter: ratelimit.New(10, 15*time.Minute)}
}

func errJobNotFound() error {
	return errs.NotFound(CodeJobNotFound, "This replication job does not exist.")
}

// sysPeer and sysReceiver are the ids the root side knows them by.
func sysPeer(id int64) string     { return "p" + strconv.FormatInt(id, 10) }
func sysReceiver(id int64) string { return "r" + strconv.FormatInt(id, 10) }

// unavailable turns a "not installed" from the root side into a clear error.
func unavailable(err error) error {
	return errs.Conflict(CodeUnavailable, sys.Message(err)).WithCause(err)
}

func (s *Service) Status(ctx context.Context) (ReplicationStatus, error) {
	st, err := s.system.ReplicationStatus(ctx)
	if err != nil {
		return ReplicationStatus{}, err
	}
	out := ReplicationStatus{Rsync: st.Rsync, SSHClient: st.SSHClient, SSHServer: st.SSHServer, Rrsync: st.Rrsync, Hints: []string{}}
	if !st.Rsync {
		out.Hints = append(out.Hints, "rsync is not installed: apt install rsync")
	}
	if !st.SSHClient {
		out.Hints = append(out.Hints, "To copy to another NetShare Hub: apt install openssh-client")
	}
	if !st.SSHServer || !st.Rrsync {
		out.Hints = append(out.Hints, "To receive copies from another NetShare Hub: apt install openssh-server rsync (3.2.4 or newer, for rrsync)")
	}
	return out, nil
}

// --- jobs ---

func (s *Service) ListJobs(ctx context.Context) ([]ReplicationJob, error) {
	list, err := s.repo.jobs(ctx, s.repo.db.Read, "")
	if err != nil {
		return nil, err
	}
	last, err := s.repo.lastRuns(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if r, ok := last[list[i].ID]; ok {
			list[i].LastRun = &r
		}
	}
	return list, nil
}

func (s *Service) GetJob(ctx context.Context, id int64) (ReplicationJob, error) {
	j, ok, err := s.repo.job(ctx, s.repo.db.Read, id)
	if err != nil {
		return ReplicationJob{}, err
	}
	if !ok {
		return ReplicationJob{}, errJobNotFound()
	}
	if runs, err := s.repo.runs(ctx, id, 50); err == nil {
		for _, r := range runs {
			if !r.DryRun {
				r := r
				j.LastRun = &r
				break
			}
		}
	}
	return j, nil
}

func validName(n string) bool {
	if n == "" || utf8.RuneCountInString(n) > 64 || n != strings.TrimSpace(n) {
		return false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// checkJob validates a job and normalizes it.
func (s *Service) checkJob(ctx context.Context, j *ReplicationJob) error {
	v := validate.New()
	j.Name = strings.TrimSpace(j.Name)
	j.LocalPath = strings.TrimSpace(j.LocalPath)
	v.Check(validName(j.Name), "name", "invalid_format", "Use 1-64 characters.")
	if !v.Has("name") {
		taken, err := s.repo.nameTaken(ctx, "replication_jobs", j.Name, j.ID)
		if err != nil {
			return err
		}
		v.Check(!taken, "name", "taken", "A replication job with this name already exists.")
	}
	sh, err := s.shares.Get(ctx, j.ShareID)
	switch {
	case errs.Is(err, shares.CodeShareNotFound):
		v.Add("shareId", "not_found", "This share does not exist.")
	case err != nil:
		return err
	default:
		j.ShareName = sh.Name
	}
	switch {
	case j.LocalPath != "" && j.PeerID != 0:
		v.Add("peerId", "one_destination", "Choose a folder on this machine or a peer, not both.")
	case j.LocalPath == "" && j.PeerID == 0:
		v.Add("localPath", "required", "Choose where the copy goes: a folder on this machine or a paired NetShare Hub.")
	case j.LocalPath != "":
		if err := s.checkLocalFolder(ctx, v, "localPath", j.LocalPath); err != nil {
			return err
		}
	default:
		peers, err := s.repo.peers(ctx, s.repo.db.Read)
		if err != nil {
			return err
		}
		found := false
		for _, p := range peers {
			if p.ID == j.PeerID {
				found, j.PeerName = true, p.Name
			}
		}
		v.Check(found, "peerId", "not_found", "This peer does not exist.")
	}
	v.Check(j.BandwidthKiB >= 0 && j.BandwidthKiB <= sys.MaxBandwidthKiB, "bandwidthKiB", "out_of_range",
		fmt.Sprintf("Use 0 (no limit) to %d KiB/s.", sys.MaxBandwidthKiB))
	if j.Excludes == nil {
		j.Excludes = []string{}
	}
	if len(j.Excludes) > sys.MaxExcludes {
		v.Add("excludes", "too_many", fmt.Sprintf("At most %d patterns.", sys.MaxExcludes))
	}
	for i, p := range j.Excludes {
		v.Check(sys.ValidExclude(p), fmt.Sprintf("excludes[%d]", i), "invalid_format",
			"One pattern per entry, like *.tmp or cache/, without leading - or spaces.")
	}
	if j.Schedule == "" {
		j.Schedule = ScheduleManual
	}
	switch j.Schedule {
	case ScheduleManual:
	case ScheduleHourly:
		v.Check(j.AtMinute >= 0 && j.AtMinute < 60, "atMinute", "out_of_range", "For an hourly job, use the minute of the hour (0-59).")
	case ScheduleDaily, ScheduleWeekly:
		v.Check(j.AtMinute >= 0 && j.AtMinute < 24*60, "atMinute", "out_of_range", "Use the minute of the day (0-1439).")
		v.Check(j.Weekday >= 0 && j.Weekday <= 6, "weekday", "out_of_range", "Use 0 (Sunday) to 6 (Saturday).")
	default:
		v.Add("schedule", "invalid_choice", "Use manual, hourly, daily or weekly.")
	}
	return v.Err()
}

// checkLocalFolder applies the folder rules the root side applies too: an
// absolute, clean path, not a system or protected folder, not a share or
// inside one (the copy would bypass the share's access), and not a
// receiving folder.
func (s *Service) checkLocalFolder(ctx context.Context, v *validate.Errors, field, path string) error {
	if !sys.ValidSharePath(path) || !sys.SharablePath(path) {
		v.Add(field, "invalid_format", "Enter a full folder path like /mnt/backup, outside the system folders.")
		return nil
	}
	for _, p := range s.protected {
		if sys.Overlaps(path, p) {
			v.Add(field, "not_allowed", "This folder holds NetShare Hub's own data.")
			return nil
		}
	}
	paths, err := s.shares.Paths(ctx)
	if err != nil {
		return err
	}
	for name, p := range paths {
		if sys.Overlaps(path, p) {
			v.Add(field, "overlaps_share", fmt.Sprintf("This folder is, holds or is inside share %q. Copies go outside every share.", name))
			return nil
		}
	}
	receivers, err := s.repo.receivers(ctx, s.repo.db.Read)
	if err != nil {
		return err
	}
	for _, rc := range receivers {
		if sys.Overlaps(path, rc.Path) {
			v.Add(field, "overlaps_receiver", fmt.Sprintf("This folder overlaps the receiving folder %q.", rc.Name))
			return nil
		}
	}
	return nil
}

func (s *Service) CreateJob(ctx context.Context, actor int64, in ReplicationJob) (ReplicationJob, error) {
	in.ID = 0
	if err := s.checkJob(ctx, &in); err != nil {
		return ReplicationJob{}, err
	}
	in.NextRunAt = nextRun(in, s.now())
	id, err := s.repo.insertJob(ctx, in, actor, s.now())
	if err != nil {
		return ReplicationJob{}, err
	}
	slog.InfoContext(ctx, "replication job created", "job", in.Name, "share", in.ShareName, "local_path", in.LocalPath, "peer", in.PeerName)
	return s.GetJob(ctx, id)
}

// JobUpdate changes the fields that are set. The share of a job is fixed:
// make a new job to copy another share.
type JobUpdate struct {
	Name         *string
	LocalPath    *string
	PeerID       *int64
	Mirror       *bool
	BandwidthKiB *int
	Excludes     *[]string
	Schedule     *string
	AtMinute     *int
	Weekday      *int
	Enabled      *bool
}

func (s *Service) UpdateJob(ctx context.Context, id int64, in JobUpdate) (ReplicationJob, error) {
	j, err := s.GetJob(ctx, id)
	if err != nil {
		return ReplicationJob{}, err
	}
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&j.Name, in.Name)
	set(&j.Schedule, in.Schedule)
	if in.LocalPath != nil {
		j.LocalPath = *in.LocalPath
		if *in.LocalPath != "" && in.PeerID == nil {
			j.PeerID = 0
		}
	}
	if in.PeerID != nil {
		j.PeerID = *in.PeerID
		if *in.PeerID != 0 && in.LocalPath == nil {
			j.LocalPath = ""
		}
	}
	if in.Mirror != nil {
		j.Mirror = *in.Mirror
	}
	if in.BandwidthKiB != nil {
		j.BandwidthKiB = *in.BandwidthKiB
	}
	if in.Excludes != nil {
		j.Excludes = *in.Excludes
	}
	if in.AtMinute != nil {
		j.AtMinute = *in.AtMinute
	}
	if in.Weekday != nil {
		j.Weekday = *in.Weekday
	}
	if in.Enabled != nil {
		j.Enabled = *in.Enabled
	}
	if err := s.checkJob(ctx, &j); err != nil {
		return ReplicationJob{}, err
	}
	j.NextRunAt = nextRun(j, s.now())
	if err := s.repo.updateJob(ctx, j, s.now()); err != nil {
		return ReplicationJob{}, err
	}
	return s.GetJob(ctx, id)
}

func (s *Service) DeleteJob(ctx context.Context, id int64) error {
	if _, err := s.GetJob(ctx, id); err != nil {
		return err
	}
	if err := s.repo.deleteJob(ctx, id); err != nil {
		return err
	}
	if s.alerts != nil {
		_ = s.alerts.Resolve(ctx, alertFailed, strconv.FormatInt(id, 10))
	}
	return nil
}

func (s *Service) Runs(ctx context.Context, jobID int64) ([]ReplicationRun, error) {
	if _, err := s.GetJob(ctx, jobID); err != nil {
		return nil, err
	}
	return s.repo.runs(ctx, jobID, keepRuns)
}

// nextRun is when a scheduled job runs next after t (local time), or nil.
func nextRun(j ReplicationJob, t time.Time) *time.Time {
	if !j.Enabled || j.Schedule == ScheduleManual || j.Schedule == "" {
		return nil
	}
	t = t.Local()
	var next time.Time
	switch j.Schedule {
	case ScheduleHourly:
		next = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), j.AtMinute, 0, 0, time.Local)
		if !next.After(t) {
			next = next.Add(time.Hour)
		}
	case ScheduleDaily, ScheduleWeekly:
		next = time.Date(t.Year(), t.Month(), t.Day(), j.AtMinute/60, j.AtMinute%60, 0, 0, time.Local)
		for !next.After(t) || (j.Schedule == ScheduleWeekly && int(next.Weekday()) != j.Weekday) {
			next = next.AddDate(0, 0, 1)
		}
	default:
		return nil
	}
	next = next.UTC()
	return &next
}

// runPayload says which job to run. The run's record is made when the
// copy starts (its id is the background job's checkpoint), so a run that
// never starts (cancelled while queued, given up, or deleted with its
// owner) leaves nothing behind: the background job table is the only
// queue.
type runPayload struct {
	JobID  int64 `json:"jobId"`
	DryRun bool  `json:"dryRun,omitempty"`
}

type runCheckpoint struct {
	RunID int64 `json:"runId"`
}

// destinationKey serializes copies to one destination (the job engine
// starts another job meanwhile).
func destinationKey(j ReplicationJob) string {
	if j.PeerID != 0 {
		return "replication:peer:" + sysPeer(j.PeerID)
	}
	return "replication:local:" + j.LocalPath
}

// Start queues a run (dryRun: a preview that changes nothing). Only one
// real run of a job is queued or running at a time: the job engine
// refuses a second one atomically.
func (s *Service) Start(ctx context.Context, actor int64, id int64, dryRun bool) (jobs.Job, error) {
	j, err := s.GetJob(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	title := "Copy " + j.ShareName + " (" + j.Name + ")"
	in := jobs.Submit{Kind: JobKind, OwnerID: actor, Title: title, Payload: runPayload{JobID: id, DryRun: dryRun},
		SerialKey: destinationKey(j)}
	if dryRun {
		in.Title = "Preview: " + title
	} else {
		in.UniqueKey = "job:" + strconv.FormatInt(id, 10)
	}
	job, err := s.jobs.Submit(ctx, in)
	if errs.Is(err, jobs.CodeJobDuplicate) {
		return jobs.Job{}, errs.Conflict(CodeBusy, "This job is already queued or running.")
	}
	return job, err
}

// RunJob is the JobKind handler. Repeating it is safe: rsync copies what
// is missing. A resumed run keeps its record.
func (s *Service) RunJob(ctx context.Context, run *jobs.Run) error {
	var p runPayload
	if err := run.Payload(&p); err != nil {
		return err
	}
	j, ok, err := s.repo.job(ctx, s.repo.db.Read, p.JobID)
	if err != nil {
		return err
	}
	if !ok {
		run.Progress(0, 0, "The replication job no longer exists; nothing to do.")
		return nil
	}
	var rec ReplicationRun
	var cp runCheckpoint
	if run.Checkpoint(&cp) && cp.RunID != 0 {
		if rec, ok, err = s.repo.run(ctx, cp.RunID); err != nil {
			return err
		}
	}
	if !ok || rec.ID == 0 {
		id, err := s.repo.insertRun(ctx, j.ID, p.DryRun, run.ID(), s.now())
		if err != nil {
			return err
		}
		if err := run.SaveCheckpoint(runCheckpoint{RunID: id}); err != nil {
			return err
		}
		rec = ReplicationRun{ID: id, JobID: j.ID, DryRun: p.DryRun, TaskID: run.ID()}
	}

	req := sys.ReplicationRun{Share: j.ShareName, LocalPath: j.LocalPath, Mirror: j.Mirror, BandwidthKiB: j.BandwidthKiB,
		Excludes: j.Excludes, DryRun: rec.DryRun}
	if j.PeerID != 0 {
		req.Peer = sysPeer(j.PeerID)
	}

	if err := s.repo.startRun(ctx, rec.ID, s.now()); err != nil {
		return err
	}
	run.Progress(0, 100, "Comparing "+j.ShareName+" with the destination")
	stats, err := s.system.Replicate(ctx, req, func(pr sys.ReplicationProgress) {
		run.Progress(int64(pr.Percent), 100, fmt.Sprintf("Copying %s: %d%%", j.ShareName, pr.Percent))
	})
	rec.Files, rec.Transferred, rec.Created, rec.Deleted, rec.Bytes = stats.Files, stats.Transferred, stats.Created, stats.Deleted, stats.Bytes
	rec.Changes, rec.Truncated = stats.Changes, stats.Truncated
	if rec.Changes == nil {
		rec.Changes = []string{}
	}
	var re *sys.ReplicationError
	var out error
	switch {
	case err == nil:
		rec.Status = "done"
	case ctx.Err() != nil:
		// Cancelled, or the server is stopping (the job resumes then).
		rec.Status, rec.Error = "cancelled", "Stopped before it finished."
		out = ctx.Err()
	case errors.As(err, &re) && re.Partial():
		rec.Status, rec.Error = "partial", re.Message
		out = errs.Unavailable(CodePartlyDone, "Some files could not be copied (they changed or could not be read during the copy). "+
			"Everything else was copied; the next run tries them again.").WithCause(err)
	case errors.Is(err, sys.ErrNotFound):
		rec.Status, rec.Error = "failed", sys.Message(err)
		out = unavailable(err)
	default:
		rec.Status, rec.Error = "failed", sys.Message(err)
		if re != nil {
			rec.Error = re.Message
		}
		out = errs.Unavailable(CodeRunFailed, "The copy failed: "+truncate(rec.Error, 300)).WithCause(err)
	}
	// The record is written even when the server is stopping.
	if ferr := s.repo.finishRun(context.WithoutCancel(ctx), rec, s.now()); ferr != nil {
		slog.ErrorContext(ctx, "could not record a replication run", "run", rec.ID, "err", ferr)
	}
	if !rec.DryRun && ctx.Err() == nil {
		s.alert(context.WithoutCancel(ctx), j, rec)
	}
	msg := fmt.Sprintf("Copied %d files (%s).", rec.Transferred, sizeOf(rec.Bytes))
	if rec.DryRun {
		msg = fmt.Sprintf("Preview: %d changes.", len(rec.Changes))
	}
	if out == nil {
		run.Progress(100, 100, msg)
	}
	return out
}

// Reconcile closes the run records whose background job is no longer
// running (call it once the job engine has started: it has requeued the
// jobs it resumes by then). Only a crash can leave such a record: a run
// that was interrupted too often, or whose job went with its owner.
func (s *Service) Reconcile(ctx context.Context) error {
	rows, err := s.repo.db.Read.QueryContext(ctx, "SELECT id, task_id FROM replication_runs WHERE status IN ('queued', 'running')")
	if err != nil {
		return err
	}
	open := map[int64]string{}
	var tasks []string
	for rows.Next() {
		var id int64
		var task string
		if err := rows.Scan(&id, &task); err != nil {
			rows.Close()
			return err
		}
		open[id] = task
		tasks = append(tasks, task)
	}
	rows.Close()
	if len(open) == 0 {
		return nil
	}
	statuses, err := s.jobs.Statuses(ctx, tasks)
	if err != nil {
		return err
	}
	for id, task := range open {
		st, ok := statuses[task]
		if ok && !st.Finished() {
			continue // resumed: the handler takes the record up again
		}
		status, msg := "failed", "Interrupted: the server stopped before the copy finished, and it was not resumed."
		if st == jobs.StatusCancelled {
			status, msg = "cancelled", "Cancelled."
		}
		if _, err := s.repo.db.Write.ExecContext(ctx,
			"UPDATE replication_runs SET status = ?, error = ?, finished_at = ? WHERE id = ?", status, msg, s.now().Unix(), id); err != nil {
			return err
		}
		slog.InfoContext(ctx, "closed an unfinished replication run", "run", id, "status", status)
	}
	return nil
}

func (s *Service) alert(ctx context.Context, j ReplicationJob, rec ReplicationRun) {
	if s.alerts == nil {
		return
	}
	key := strconv.FormatInt(j.ID, 10)
	if rec.Status == "done" {
		_ = s.alerts.Resolve(ctx, alertFailed, key)
		return
	}
	severity := alerts.Error
	if rec.Status == "partial" {
		severity = alerts.Warning
	}
	_, _ = s.alerts.Raise(ctx, alerts.Raise{
		Kind: alertFailed, Key: key, Severity: severity, Source: alerts.SourceReplication,
		Title:   "Replication " + j.Name + " did not finish",
		Message: "The copy of " + j.ShareName + " failed: " + truncate(rec.Error, 300),
		Details: map[string]any{"jobId": j.ID, "runId": rec.ID},
	})
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func sizeOf(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Tick starts the scheduled runs that are due (every minute).
func (s *Service) Tick(ctx context.Context) error {
	rows, err := s.repo.db.Read.QueryContext(ctx,
		"SELECT id, created_by FROM replication_jobs WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?", s.now().Unix())
	if err != nil {
		return err
	}
	type due struct{ id, owner int64 }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.owner); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	for _, d := range list {
		j, ok, err := s.repo.job(ctx, s.repo.db.Read, d.id)
		if err != nil || !ok {
			continue
		}
		if err := s.repo.setNextRun(ctx, d.id, nextRun(j, s.now())); err != nil {
			return err
		}
		if _, err := s.Start(ctx, d.owner, d.id, false); err != nil && !errs.Is(err, CodeBusy) {
			slog.ErrorContext(ctx, "could not start a scheduled replication", "job", j.Name, "err", err)
		}
	}
	return s.repo.pruneRuns(ctx, keepRuns)
}

// --- peers (this machine sends) ---

func (s *Service) Peers(ctx context.Context) ([]ReplicationPeer, error) {
	return s.repo.peers(ctx, s.repo.db.Read)
}

// applyPeers hands every peer to the root side, from inside tx.
func (s *Service) applyPeers(ctx context.Context, tx *sql.Tx) error {
	list, err := s.repo.peers(ctx, tx)
	if err != nil {
		return err
	}
	out := make([]sys.Peer, 0, len(list))
	for _, p := range list {
		out = append(out, sys.Peer{ID: sysPeer(p.ID), Host: p.Host, Port: p.SSHPort, HostKey: p.HostKey})
	}
	return s.system.SetPeers(ctx, out)
}

func (s *Service) DeletePeer(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.repo.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM replication_peers WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errs.NotFound(CodePeerNotFound, "This peer does not exist.")
	}
	if err := s.applyPeers(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// --- receivers (this machine receives) ---

func (s *Service) Receivers(ctx context.Context) ([]ReplicationReceiver, error) {
	rows, err := s.repo.receivers(ctx, s.repo.db.Read)
	if err != nil {
		return nil, err
	}
	out := make([]ReplicationReceiver, 0, len(rows))
	for _, r := range rows {
		if r.PairSecret != "" && !r.PairExpires.After(s.now()) {
			r.PairingExpires = nil
		}
		out = append(out, r.ReplicationReceiver)
	}
	return out, nil
}

// applyReceivers hands every paired receiver to the root side, from tx.
func (s *Service) applyReceivers(ctx context.Context, tx *sql.Tx) error {
	list, err := s.repo.receivers(ctx, tx)
	if err != nil {
		return err
	}
	out := []sys.Receiver{}
	for _, r := range list {
		if r.PublicKey != "" {
			out = append(out, sys.Receiver{ID: sysReceiver(r.ID), Path: r.Path, PublicKey: r.PublicKey})
		}
	}
	return s.system.SetReceivers(ctx, out)
}

var receiverPath = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)

// AddReceiver adds a receiving folder and returns it with its first
// pairing code (shown once).
func (s *Service) AddReceiver(ctx context.Context, name, path string) (ReplicationReceiver, string, error) {
	name, path = strings.TrimSpace(name), strings.TrimSpace(path)
	v := validate.New()
	v.Check(validName(name), "name", "invalid_format", "Use 1-64 characters.")
	if !v.Has("name") {
		taken, err := s.repo.nameTaken(ctx, "replication_receivers", name, 0)
		if err != nil {
			return ReplicationReceiver{}, "", err
		}
		v.Check(!taken, "name", "taken", "A receiving folder with this name already exists.")
	}
	if !receiverPath.MatchString(path) || strings.Contains(path, "/..") || strings.Contains(path, "/./") {
		v.Add("path", "invalid_format", "Enter a full folder path like /srv/replicas/office, with only letters, digits, . _ - and /.")
	} else if err := s.checkLocalFolder(ctx, v, "path", path); err != nil {
		return ReplicationReceiver{}, "", err
	}
	if !v.Has("path") {
		jobs, err := s.repo.jobs(ctx, s.repo.db.Read, " WHERE j.local_path != ''")
		if err != nil {
			return ReplicationReceiver{}, "", err
		}
		for _, j := range jobs {
			if sys.Overlaps(path, j.LocalPath) {
				v.Add("path", "overlaps_job", fmt.Sprintf("This folder overlaps the destination of job %q.", j.Name))
				break
			}
		}
	}
	if err := v.Err(); err != nil {
		return ReplicationReceiver{}, "", err
	}
	res, err := s.repo.db.Write.ExecContext(ctx, "INSERT INTO replication_receivers (name, path, created_at) VALUES (?, ?, ?)",
		name, path, s.now().Unix())
	if err != nil {
		return ReplicationReceiver{}, "", err
	}
	id, _ := res.LastInsertId()
	code, err := s.NewPairingCode(ctx, id)
	if err != nil {
		return ReplicationReceiver{}, "", err
	}
	rc, err := s.receiver(ctx, id)
	return rc, code, err
}

func (s *Service) receiver(ctx context.Context, id int64) (ReplicationReceiver, error) {
	list, err := s.Receivers(ctx)
	if err != nil {
		return ReplicationReceiver{}, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return ReplicationReceiver{}, errs.NotFound(CodeReceiverNotFound, "This receiving folder does not exist.")
}

func (s *Service) DeleteReceiver(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.repo.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM replication_receivers WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errs.NotFound(CodeReceiverNotFound, "This receiving folder does not exist.")
	}
	// The sender's key goes at once; the folder and the copies stay.
	if err := s.applyReceivers(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
