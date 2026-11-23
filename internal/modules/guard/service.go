package guard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys/sambalog"
)

// Mode is what the guard does when it finds something.
type Mode string

const (
	ModeOff      Mode = "off"
	ModeLearning Mode = "learning" // raise an alert, nothing else
	ModeProtect  Mode = "protect"  // also block the user and disconnect them
)

// Settings are the guard's settings.
type Settings struct {
	Mode        Mode      `json:"mode" enum:"off,learning,protect" doc:"\"learning\" only raises alerts (start here); \"protect\" also makes the user read-only on every share and disconnects their computer."`
	Renames     int       `json:"renames" minimum:"3" maximum:"100000" doc:"Suspicious renames (to an unusual or known-bad extension) by one user from one computer within a minute that trigger the guard."`
	NoteFolders int       `json:"noteFolders" minimum:"2" maximum:"1000" doc:"In how many folders a ransom note (HOW_TO_DECRYPT...) must appear within a minute."`
	UpdatedAt   time.Time `json:"updatedAt,omitzero"`
}

// Incident is one time the guard was triggered.
type Incident struct {
	ID         int64      `json:"id"`
	UserID     *int64     `json:"userId,omitempty"`
	Username   string     `json:"username"`
	ClientIP   string     `json:"clientIp"`
	Rule       string     `json:"rule" enum:"renames,ransom_notes"`
	Count      int        `json:"count"`
	Shares     []string   `json:"shares" nullable:"false"`
	Samples    []string   `json:"samples" nullable:"false" doc:"Some of the files involved."`
	Mode       Mode       `json:"mode" enum:"off,learning,protect" doc:"The mode when it happened."`
	Blocked    bool       `json:"blocked" doc:"The user was made read-only (until restored)."`
	At         time.Time  `json:"at"`
	RestoredAt *time.Time `json:"restoredAt,omitempty"`
	RestoredBy *int64     `json:"restoredBy,omitempty"`
}

// Users is what the guard needs from the users module.
type Users interface {
	GetByUsername(ctx context.Context, username string) (users.User, bool, error)
}

// Shares is what the guard needs from the shares module.
type Shares interface {
	Reapply(ctx context.Context) error
}

// Samba is what the guard needs from the system layer.
type Samba interface {
	DisconnectClient(ctx context.Context, ip netip.Addr) error
}

// Alerts is what the guard needs from the alerts module.
type Alerts interface {
	Raise(ctx context.Context, in alerts.Raise) (alerts.Alert, error)
	Resolve(ctx context.Context, kind, key string) error
}

const (
	alertKind = "guard.triggered"

	CodeIncidentNotFound errs.Code = "GUARD_INCIDENT_NOT_FOUND"
)

// Service runs the guard.
type Service struct {
	db     *db.DB
	users  Users
	shares Shares
	samba  Samba
	alerts Alerts
	now    func() time.Time

	mu       sync.Mutex
	settings Settings
	detector *Detector
}

func NewService(database *db.DB, u Users, s Shares, samba Samba, a Alerts) *Service {
	return &Service{db: database, users: u, shares: s, samba: samba, alerts: a, now: time.Now}
}

// Start loads the settings. Run feeds it events.
func (s *Service) Start(ctx context.Context) error {
	st, err := s.load(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.settings, s.detector = st, NewDetector(thresholds(st))
	s.mu.Unlock()
	return nil
}

func thresholds(st Settings) Thresholds {
	return Thresholds{Window: DefaultThresholds.Window, Renames: st.Renames, NoteFolders: st.NoteFolders}
}

// Run watches events until the channel closes or ctx ends.
func (s *Service) Run(ctx context.Context, events <-chan sambalog.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			s.mu.Lock()
			mode, det := s.settings.Mode, s.detector
			var f Finding
			var found bool
			if mode != ModeOff && det != nil {
				f, found = det.Observe(ev)
			}
			s.mu.Unlock()
			if found {
				s.react(context.WithoutCancel(ctx), f, mode)
			}
		}
	}
}

// react records a finding and, in protect mode, blocks and disconnects.
func (s *Service) react(ctx context.Context, f Finding, mode Mode) {
	var userID *int64
	if u, ok, err := s.users.GetByUsername(ctx, f.User); err == nil && ok {
		userID = &u.ID
	}
	// A user NetShare Hub does not know cannot be blocked here; the alert
	// still goes out, and the computer is still disconnected.
	blocked := mode == ModeProtect && userID != nil
	in := Incident{UserID: userID, Username: f.User, ClientIP: f.ClientIP.String(), Rule: f.Rule, Count: f.Count,
		Shares: f.Shares, Samples: f.Samples, Mode: mode, Blocked: blocked, At: f.At}
	id, err := s.insert(ctx, in)
	if err != nil {
		slog.ErrorContext(ctx, "ransomware guard: could not record an incident", "err", err)
	}
	in.ID = id

	var problems []string
	if blocked {
		if err := s.shares.Reapply(ctx); err != nil {
			problems = append(problems, "making the user read-only failed")
			slog.ErrorContext(ctx, "ransomware guard: could not block the user", "user", f.User, "err", err)
		}
	}
	if mode == ModeProtect {
		if err := s.samba.DisconnectClient(ctx, f.ClientIP); err != nil {
			problems = append(problems, "disconnecting the computer failed")
			slog.ErrorContext(ctx, "ransomware guard: could not disconnect", "ip", f.ClientIP, "err", err)
		}
	}
	slog.WarnContext(ctx, "ransomware guard triggered", "user", f.User, "ip", f.ClientIP, "rule", f.Rule,
		"count", f.Count, "mode", mode, "blocked", blocked, "incident", id)

	what := map[string]string{
		"renames":      fmt.Sprintf("%d files renamed to unusual extensions within a minute", f.Count),
		"ransom_notes": fmt.Sprintf("ransom-note files written in %d folders within a minute", f.Count),
	}[f.Rule]
	var msg string
	switch {
	case mode == ModeLearning:
		msg = fmt.Sprintf("%s by %s from %s. Learning mode: nothing was blocked. If this was normal work, raise the thresholds; otherwise check that computer now.", what, f.User, f.ClientIP)
	case blocked:
		msg = fmt.Sprintf("%s by %s from %s. %s can now only read, on every share, and the computer was disconnected. Check it, then restore access under Security.", what, f.User, f.ClientIP, f.User)
	default:
		msg = fmt.Sprintf("%s by %s from %s. The computer was disconnected.", what, f.User, f.ClientIP)
	}
	if len(problems) > 0 {
		msg += " Warning: " + strings.Join(problems, "; ") + " (see the logs)."
	}
	if s.alerts != nil {
		_, _ = s.alerts.Raise(ctx, alerts.Raise{
			Kind: alertKind, Key: f.User + "|" + f.ClientIP.String(), Severity: alerts.Critical, Source: alerts.SourceSecurity,
			Title:   fmt.Sprintf("Possible ransomware: %s on %s", f.User, f.ClientIP),
			Message: msg,
			Details: map[string]any{"incidentId": id, "rule": f.Rule, "count": f.Count, "shares": f.Shares, "samples": f.Samples, "blocked": blocked},
		})
	}
}

// Restore lifts the block of an incident ("Restore access").
func (s *Service) Restore(ctx context.Context, actor *httpx.Principal, id int64) (Incident, error) {
	in, ok, err := s.get(ctx, id)
	if err != nil {
		return Incident{}, err
	}
	if !ok {
		return Incident{}, errs.NotFound(CodeIncidentNotFound, "This incident does not exist.")
	}
	if in.RestoredAt == nil {
		if _, err := s.db.Write.ExecContext(ctx, "UPDATE guard_incidents SET restored_at = ?, restored_by = ? WHERE id = ? AND restored_at IS NULL",
			s.now().Unix(), actor.UserID, id); err != nil {
			return Incident{}, err
		}
		if in.Blocked {
			if err := s.shares.Reapply(ctx); err != nil {
				return Incident{}, err
			}
		}
		if s.alerts != nil {
			_ = s.alerts.Resolve(ctx, alertKind, in.Username+"|"+in.ClientIP)
		}
		// Until the audit log exists (1.15), the server log is the record.
		slog.InfoContext(ctx, "ransomware guard: access restored", "incident", id, "user", in.Username, "by", actor.Username)
	}
	in, _, err = s.get(ctx, id)
	return in, err
}

// BlockedUsers is for the shares module: users who may only read.
func (s *Service) BlockedUsers(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		"SELECT DISTINCT user_id FROM guard_incidents WHERE blocked = 1 AND restored_at IS NULL AND user_id IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// --- settings ---

func (s *Service) load(ctx context.Context) (Settings, error) {
	var st Settings
	var updated int64
	err := s.db.Read.QueryRowContext(ctx, "SELECT mode, renames, note_folders, updated_at FROM guard_settings WHERE id = 1").
		Scan(&st.Mode, &st.Renames, &st.NoteFolders, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{Mode: ModeLearning, Renames: DefaultThresholds.Renames, NoteFolders: DefaultThresholds.NoteFolders}, nil
	}
	if updated > 0 {
		st.UpdatedAt = time.Unix(updated, 0).UTC()
	}
	return st, err
}

func (s *Service) Settings(ctx context.Context) (Settings, error) { return s.load(ctx) }

// UpdateSettings changes the mode and thresholds; they apply at once.
func (s *Service) UpdateSettings(ctx context.Context, actor *httpx.Principal, in Settings) (Settings, error) {
	v := validate.New()
	CheckSettings(v, in)
	if err := v.Err(); err != nil {
		return Settings{}, err
	}
	if _, err := s.db.Write.ExecContext(ctx, "UPDATE guard_settings SET mode = ?, renames = ?, note_folders = ?, updated_at = ? WHERE id = 1",
		in.Mode, in.Renames, in.NoteFolders, s.now().Unix()); err != nil {
		return Settings{}, err
	}
	st, err := s.load(ctx)
	if err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	s.settings, s.detector = st, NewDetector(thresholds(st))
	s.mu.Unlock()
	slog.InfoContext(ctx, "ransomware guard settings changed", "mode", st.Mode, "renames", st.Renames,
		"note_folders", st.NoteFolders, "by", actor.Username)
	return st, nil
}

// CheckSettings validates the guard's settings (the API's and a restored
// backup's).
func CheckSettings(v *validate.Errors, in Settings) {
	v.Check(in.Mode == ModeOff || in.Mode == ModeLearning || in.Mode == ModeProtect, "mode", "invalid_choice", "Must be off, learning or protect.")
	v.Check(in.Renames >= 3, "renames", "too_small", "At least 3: lower would trigger on normal work.")
	v.Check(in.Renames <= 100000, "renames", "too_large", "At most 100000.")
	v.Check(in.NoteFolders >= 2, "noteFolders", "too_small", "At least 2.")
	v.Check(in.NoteFolders <= 1000, "noteFolders", "too_large", "At most 1000.")
}

// --- incidents ---

const incidentColumns = "id, user_id, username, client_ip, rule, count, shares, samples, mode, blocked, at, restored_at, restored_by"

func scanIncident(sc interface{ Scan(...any) error }) (Incident, error) {
	var in Incident
	var userID, restored, by sql.NullInt64
	var shares, samples string
	var at int64
	err := sc.Scan(&in.ID, &userID, &in.Username, &in.ClientIP, &in.Rule, &in.Count, &shares, &samples, &in.Mode,
		&in.Blocked, &at, &restored, &by)
	if err != nil {
		return Incident{}, err
	}
	_ = json.Unmarshal([]byte(shares), &in.Shares)
	_ = json.Unmarshal([]byte(samples), &in.Samples)
	if in.Shares == nil {
		in.Shares = []string{}
	}
	if in.Samples == nil {
		in.Samples = []string{}
	}
	in.At = time.Unix(at, 0).UTC()
	if userID.Valid {
		in.UserID = &userID.Int64
	}
	if restored.Valid {
		t := time.Unix(restored.Int64, 0).UTC()
		in.RestoredAt = &t
	}
	if by.Valid {
		in.RestoredBy = &by.Int64
	}
	return in, nil
}

func (s *Service) insert(ctx context.Context, in Incident) (int64, error) {
	shares, _ := json.Marshal(in.Shares)
	samples, _ := json.Marshal(in.Samples)
	res, err := s.db.Write.ExecContext(ctx, `INSERT INTO guard_incidents
		(user_id, username, client_ip, rule, count, shares, samples, mode, blocked, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.UserID, in.Username, in.ClientIP, in.Rule, in.Count, string(shares), string(samples), in.Mode, in.Blocked, in.At.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Service) get(ctx context.Context, id int64) (Incident, bool, error) {
	in, err := scanIncident(s.db.Read.QueryRowContext(ctx, "SELECT "+incidentColumns+" FROM guard_incidents WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Incident{}, false, nil
	}
	return in, err == nil, err
}

func (s *Service) Incidents(ctx context.Context, p page.Request) ([]Incident, int, error) {
	var total int
	if err := s.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM guard_incidents").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Read.QueryContext(ctx, "SELECT "+incidentColumns+" FROM guard_incidents ORDER BY id DESC LIMIT ? OFFSET ?",
		p.Size, p.Offset())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		in, err := scanIncident(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, in)
	}
	return out, total, rows.Err()
}
