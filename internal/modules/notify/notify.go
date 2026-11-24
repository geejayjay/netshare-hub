// Package notify delivers NetShare Hub alerts to external channels:
// ntfy, Gotify, generic/Discord/Slack webhooks, and SMTP email.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
)

const (
	KindNtfy    = "ntfy"
	KindGotify  = "gotify"
	KindWebhook = "webhook"
	KindSMTP    = "smtp"
)

const (
	CodeChannelNotFound errs.Code = "NOTIFY_CHANNEL_NOT_FOUND"
	CodeChannelConflict errs.Code = "NOTIFY_CHANNEL_CONFLICT"
)

// ChannelConfig holds per-provider connection details. Secret fields
// (Token, Secret, Password) are stored in SQLite and masked in API responses.
type ChannelConfig struct {
	// URL is used by ntfy (e.g. https://ntfy.sh/my-topic), Gotify (base URL),
	// and webhook (target URL).
	URL string `json:"url,omitempty"`
	// Token is the bearer/app token for ntfy or Gotify.
	Token string `json:"token,omitempty"`
	// Secret is an optional HMAC-SHA256 signing key for webhooks (X-NetShare-Signature).
	Secret string `json:"secret,omitempty"`

	// SMTP settings.
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
}

func (c ChannelConfig) hasSecret() bool {
	return c.Token != "" || c.Secret != "" || c.Password != ""
}

func (c ChannelConfig) redacted() ChannelConfig {
	out := c
	out.Token = ""
	out.Secret = ""
	out.Password = ""
	return out
}

// NotificationChannel is the public API representation of a configured channel.
type NotificationChannel struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	Kind        string        `json:"kind" enum:"ntfy,gotify,webhook,smtp"`
	Enabled     bool          `json:"enabled"`
	MinSeverity string        `json:"minSeverity" enum:"info,warning,error,critical"`
	Config      ChannelConfig `json:"config"`
	HasSecret   bool          `json:"hasSecret"`
	LastSentAt  string        `json:"lastSentAt,omitempty"`
	LastError   string        `json:"lastError,omitempty"`
	CreatedAt   string        `json:"createdAt"`
	UpdatedAt   string        `json:"updatedAt"`
}

// ChannelInput creates or updates a notification channel.
type ChannelInput struct {
	Name        string        `json:"name"`
	Kind        string        `json:"kind" enum:"ntfy,gotify,webhook,smtp"`
	Enabled     *bool         `json:"enabled,omitempty"`
	MinSeverity string        `json:"minSeverity,omitempty" enum:"info,warning,error,critical"`
	Config      ChannelConfig `json:"config"`
}

// ChannelPatch partially updates a channel.
type ChannelPatch struct {
	Name        *string        `json:"name,omitempty"`
	Enabled     *bool          `json:"enabled,omitempty"`
	MinSeverity *string        `json:"minSeverity,omitempty" enum:"info,warning,error,critical"`
	Config      *ChannelConfig `json:"config,omitempty"`
}

// ChannelTestResult reports the outcome of a test dispatch.
type ChannelTestResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

type storedChannel struct {
	NotificationChannel
	rawConfig ChannelConfig
}

// Service manages notification channels and dispatches alerts asynchronously.
type Service struct {
	db     *db.DB
	client *http.Client
	queue  chan alerts.Alert
	done   chan struct{}
}

// NewService constructs the notification service. Call Start/Stop if you want
// background alert delivery.
func NewService(database *db.DB) *Service {
	return &Service{
		db:     database,
		client: &http.Client{Timeout: 10 * time.Second},
		queue:  make(chan alerts.Alert, 128),
		done:   make(chan struct{}),
	}
}

// Run processes queued alerts until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-s.queue:
			s.dispatch(ctx, a)
		}
	}
}

// Enqueue queues an alert for asynchronous external delivery. Never blocks.
func (s *Service) Enqueue(a alerts.Alert) {
	select {
	case s.queue <- a:
	default:
		slog.Warn("notification queue full, dropping alert", "alert_id", a.ID, "kind", a.Kind)
	}
}

func severityRank(sev string) int {
	switch strings.ToLower(sev) {
	case "info":
		return 1
	case "warning":
		return 2
	case "error":
		return 3
	case "critical":
		return 4
	default:
		return 2
	}
}

func validateChannel(name, kind, minSev string, cfg ChannelConfig) error {
	var fields []errs.FieldError
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		fields = append(fields, errs.FieldError{Field: "name", Code: "invalid", Message: "Name must be 1–80 characters."})
	}
	if minSev != "" && minSev != "info" && minSev != "warning" && minSev != "error" && minSev != "critical" {
		fields = append(fields, errs.FieldError{Field: "minSeverity", Code: "invalid_choice", Message: "Choose info, warning, error, or critical."})
	}
	checkURL := func(raw string) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fields = append(fields, errs.FieldError{Field: "config.url", Code: "invalid_url", Message: "Enter a valid http:// or https:// URL."})
		}
	}
	switch kind {
	case KindNtfy:
		checkURL(cfg.URL)
	case KindGotify:
		checkURL(cfg.URL)
		if strings.TrimSpace(cfg.Token) == "" {
			fields = append(fields, errs.FieldError{Field: "config.token", Code: "required", Message: "Gotify application token is required."})
		}
	case KindWebhook:
		checkURL(cfg.URL)
	case KindSMTP:
		if strings.TrimSpace(cfg.Host) == "" {
			fields = append(fields, errs.FieldError{Field: "config.host", Code: "required", Message: "SMTP host is required."})
		}
		if cfg.Port < 1 || cfg.Port > 65535 {
			fields = append(fields, errs.FieldError{Field: "config.port", Code: "out_of_range", Message: "Port must be between 1 and 65535."})
		}
		if strings.TrimSpace(cfg.From) == "" || !strings.Contains(cfg.From, "@") {
			fields = append(fields, errs.FieldError{Field: "config.from", Code: "invalid_email", Message: "Enter a valid sender email address."})
		}
		if strings.TrimSpace(cfg.To) == "" || !strings.Contains(cfg.To, "@") {
			fields = append(fields, errs.FieldError{Field: "config.to", Code: "invalid_email", Message: "Enter at least one recipient email address."})
		}
	default:
		fields = append(fields, errs.FieldError{Field: "kind", Code: "invalid_choice", Message: "Choose ntfy, gotify, webhook, or smtp."})
	}
	if len(fields) > 0 {
		return errs.Invalid(errs.CodeValidationFailed, "Check the notification channel settings.", fields...)
	}
	return nil
}

func (s *Service) List(ctx context.Context) ([]NotificationChannel, error) {
	stored, err := s.listStored(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]NotificationChannel, 0, len(stored))
	for _, c := range stored {
		out = append(out, c.NotificationChannel)
	}
	return out, nil
}

func (s *Service) listStored(ctx context.Context) ([]storedChannel, error) {
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT id, name, kind, enabled, min_severity, config_json, last_sent_at, last_error, created_at, updated_at
		FROM notification_channels
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("notify: list: %w", err)
	}
	defer rows.Close()
	out := make([]storedChannel, 0)
	for rows.Next() {
		sc, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Service) getStored(ctx context.Context, id int64) (storedChannel, error) {
	row := s.db.Read.QueryRowContext(ctx, `
		SELECT id, name, kind, enabled, min_severity, config_json, last_sent_at, last_error, created_at, updated_at
		FROM notification_channels WHERE id = ?
	`, id)
	sc, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return storedChannel{}, errs.NotFound(CodeChannelNotFound, "Notification channel not found.")
	}
	return sc, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanChannel(r scanner) (storedChannel, error) {
	var sc storedChannel
	var enabled int
	var raw string
	if err := r.Scan(
		&sc.ID, &sc.Name, &sc.Kind, &enabled, &sc.MinSeverity, &raw,
		&sc.LastSentAt, &sc.LastError, &sc.CreatedAt, &sc.UpdatedAt,
	); err != nil {
		return storedChannel{}, err
	}
	sc.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(raw), &sc.rawConfig)
	sc.HasSecret = sc.rawConfig.hasSecret()
	sc.Config = sc.rawConfig.redacted()
	return sc, nil
}

func (s *Service) Create(ctx context.Context, in ChannelInput) (NotificationChannel, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.MinSeverity == "" {
		in.MinSeverity = "warning"
	}
	if in.Kind == KindSMTP && in.Config.Port == 0 {
		in.Config.Port = 587
	}
	if err := validateChannel(in.Name, in.Kind, in.MinSeverity, in.Config); err != nil {
		return NotificationChannel{}, err
	}
	enabled := 1
	if in.Enabled != nil && !*in.Enabled {
		enabled = 0
	}
	raw, _ := json.Marshal(in.Config)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO notification_channels (name, kind, enabled, min_severity, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, in.Name, in.Kind, enabled, in.MinSeverity, string(raw), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return NotificationChannel{}, errs.Conflict(CodeChannelConflict, "A notification channel with that name already exists.")
		}
		return NotificationChannel{}, fmt.Errorf("notify: create: %w", err)
	}
	id, _ := res.LastInsertId()
	sc, err := s.getStored(ctx, id)
	if err != nil {
		return NotificationChannel{}, err
	}
	return sc.NotificationChannel, nil
}

func (s *Service) Update(ctx context.Context, id int64, patch ChannelPatch) (NotificationChannel, error) {
	cur, err := s.getStored(ctx, id)
	if err != nil {
		return NotificationChannel{}, err
	}
	name := cur.Name
	if patch.Name != nil {
		name = strings.TrimSpace(*patch.Name)
	}
	enabled := cur.Enabled
	if patch.Enabled != nil {
		enabled = *patch.Enabled
	}
	minSev := cur.MinSeverity
	if patch.MinSeverity != nil && *patch.MinSeverity != "" {
		minSev = *patch.MinSeverity
	}
	cfg := cur.rawConfig
	if patch.Config != nil {
		next := *patch.Config
		// Preserve stored secrets if omitted in edit payload.
		if next.Token == "" {
			next.Token = cur.rawConfig.Token
		}
		if next.Secret == "" {
			next.Secret = cur.rawConfig.Secret
		}
		if next.Password == "" {
			next.Password = cur.rawConfig.Password
		}
		if cur.Kind == KindSMTP && next.Port == 0 {
			next.Port = 587
		}
		cfg = next
	}
	if err := validateChannel(name, cur.Kind, minSev, cfg); err != nil {
		return NotificationChannel{}, err
	}
	raw, _ := json.Marshal(cfg)
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Write.ExecContext(ctx, `
		UPDATE notification_channels
		SET name = ?, enabled = ?, min_severity = ?, config_json = ?, updated_at = ?
		WHERE id = ?
	`, name, enabledInt, minSev, string(raw), now, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return NotificationChannel{}, errs.Conflict(CodeChannelConflict, "A notification channel with that name already exists.")
		}
		return NotificationChannel{}, fmt.Errorf("notify: update: %w", err)
	}
	sc, err := s.getStored(ctx, id)
	if err != nil {
		return NotificationChannel{}, err
	}
	return sc.NotificationChannel, nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM notification_channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("notify: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.NotFound(CodeChannelNotFound, "Notification channel not found.")
	}
	return nil
}

func (s *Service) recordOutcome(ctx context.Context, id int64, sendErr error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if sendErr != nil {
		msg := sendErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		_, _ = s.db.Write.ExecContext(ctx, `UPDATE notification_channels SET last_error = ?, updated_at = ? WHERE id = ?`, msg, now, id)
		return
	}
	_, _ = s.db.Write.ExecContext(ctx, `UPDATE notification_channels SET last_sent_at = ?, last_error = '', updated_at = ? WHERE id = ?`, now, now, id)
}

func (s *Service) dispatch(ctx context.Context, a alerts.Alert) {
	channels, err := s.listStored(ctx)
	if err != nil {
		slog.WarnContext(ctx, "notify: failed to load channels", "err", err)
		return
	}
	aRank := severityRank(string(a.Severity))
	for _, ch := range channels {
		if !ch.Enabled || aRank < severityRank(ch.MinSeverity) {
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.send(sendCtx, ch, a)
		cancel()
		s.recordOutcome(ctx, ch.ID, err)
		if err != nil {
			slog.WarnContext(ctx, "notify: channel delivery failed", "channel", ch.Name, "kind", ch.Kind, "err", err)
		}
	}
}

// Test sends a synthetic test alert through one channel and reports latency and status.
func (s *Service) Test(ctx context.Context, id int64) (ChannelTestResult, error) {
	ch, err := s.getStored(ctx, id)
	if err != nil {
		return ChannelTestResult{}, err
	}
	sample := alerts.Alert{
		ID:       0,
		Kind:     "notify.test",
		Severity: alerts.Info,
		Source:   alerts.SourceSystem,
		Title:    "NetShare Hub test notification",
		Message:  fmt.Sprintf("Channel %q (%s) is connected and delivering alerts.", ch.Name, ch.Kind),
		Count:    1,
		FirstAt:  time.Now().UTC(),
		LastAt:   time.Now().UTC(),
		Active:   true,
	}
	start := time.Now()
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	sendErr := s.send(sendCtx, ch, sample)
	cancel()
	latency := time.Since(start).Milliseconds()
	s.recordOutcome(ctx, ch.ID, sendErr)
	if sendErr != nil {
		return ChannelTestResult{OK: false, LatencyMs: latency, Error: sendErr.Error()}, nil
	}
	return ChannelTestResult{OK: true, LatencyMs: latency}, nil
}
