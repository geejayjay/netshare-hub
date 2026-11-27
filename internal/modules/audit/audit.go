// Package audit provides an append-only record of administrative actions.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/logger"
	"netshare-hub/internal/platform/page"
)

// AuditEntry is one immutable administrative audit record.
type AuditEntry struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	ActorID   int64     `json:"actorId"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Before    string    `json:"before,omitempty"`
	After     string    `json:"after,omitempty"`
	RequestID string    `json:"requestId,omitempty"`
	ClientIP  string    `json:"clientIp,omitempty"`
}

// Filter narrows listed audit entries.
type Filter struct {
	Actor    string
	Action   string
	Q        string
	Page     int
	PageSize int
}

// Service stores and queries audit records.
type Service struct {
	db *db.DB
}

func NewService(database *db.DB) *Service {
	return &Service{db: database}
}

// Record appends an audit event. It never modifies or deletes existing rows.
func (s *Service) Record(ctx context.Context, e AuditEntry) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO audit_log (at, actor_id, actor, action, target, before_json, after_json, request_id, client_ip)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.At.Unix(), e.ActorID, e.Actor, e.Action, e.Target, e.Before, e.After, e.RequestID, e.ClientIP,
	)
	return err
}

// List returns matching audit records newest-first with pagination.
func (s *Service) List(ctx context.Context, f Filter) (page.List[AuditEntry], error) {
	req, err := (page.Query{Page: f.Page, PageSize: f.PageSize}).Resolve(page.Options{DefaultSize: 50, MaxSize: 200})
	if err != nil {
		return page.List[AuditEntry]{}, err
	}

	var where []string
	var args []any
	if actor := strings.TrimSpace(f.Actor); actor != "" {
		where = append(where, "actor LIKE ?")
		args = append(args, "%"+actor+"%")
	}
	if action := strings.TrimSpace(f.Action); action != "" {
		where = append(where, "action LIKE ?")
		args = append(args, "%"+action+"%")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		like := "%" + q + "%"
		where = append(where, "(actor LIKE ? OR action LIKE ? OR target LIKE ? OR after_json LIKE ? OR request_id LIKE ?)")
		args = append(args, like, like, like, like, like)
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_log"+clause, args...).Scan(&total); err != nil {
		return page.List[AuditEntry]{}, err
	}

	queryArgs := append(append([]any{}, args...), req.Size, req.Offset())
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT id, at, actor_id, actor, action, target, before_json, after_json, request_id, client_ip
		FROM audit_log`+clause+`
		ORDER BY at DESC, id DESC
		LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return page.List[AuditEntry]{}, err
	}
	defer rows.Close()

	items := make([]AuditEntry, 0, req.Size)
	for rows.Next() {
		var e AuditEntry
		var unix int64
		if err := rows.Scan(&e.ID, &unix, &e.ActorID, &e.Actor, &e.Action, &e.Target, &e.Before, &e.After, &e.RequestID, &e.ClientIP); err != nil {
			return page.List[AuditEntry]{}, err
		}
		e.At = time.Unix(unix, 0).UTC()
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return page.List[AuditEntry]{}, err
	}
	return page.Paged(items, total, req), nil
}

// Middleware automatically records successful mutating admin API requests with
// sanitized payloads so secrets are never persisted in the audit log.
func Middleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !shouldAudit(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			var reqBody []byte
			if r.Body != nil && strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
				const maxCapture = 64 << 10
				raw, _ := io.ReadAll(io.LimitReader(r.Body, maxCapture+1))
				r.Body = io.NopCloser(bytes.NewReader(raw))
				if len(raw) <= maxCapture {
					reqBody = raw
				}
			}

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			if rec.status < 200 || rec.status >= 300 {
				return
			}

			p := httpx.PrincipalFrom(r.Context())
			var actorID int64
			actor := "system"
			if p != nil {
				actorID = p.UserID
				actor = p.Username
				if p.TokenID > 0 {
					actor = fmt.Sprintf("%s (token #%d)", p.Username, p.TokenID)
				}
			}

			relPath := strings.TrimPrefix(r.URL.Path, "/api/v1")
			entry := AuditEntry{
				At:        time.Now(),
				ActorID:   actorID,
				Actor:     actor,
				Action:    r.Method + " " + normalizeAction(relPath),
				Target:    relPath,
				After:     sanitizeJSON(reqBody),
				RequestID: logger.RequestID(r.Context()),
				ClientIP:  r.RemoteAddr,
			}
			if err := svc.Record(r.Context(), entry); err != nil {
				slog.WarnContext(r.Context(), "audit record failed", "err", err)
			}
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func shouldAudit(method, path string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	rel := strings.TrimPrefix(path, "/api/v1")
	skipPrefixes := []string{
		"/auth/",
		"/files/",
		"/uploads",
		"/shares/effective-access",
		"/system/speedtest/",
	}
	for _, pref := range skipPrefixes {
		if strings.HasPrefix(rel, pref) {
			return false
		}
	}
	return true
}

func normalizeAction(rel string) string {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	if len(parts) == 0 {
		return rel
	}
	return parts[0]
}

var secretKeys = map[string]bool{
	"password":    true,
	"passphrase":  true,
	"secret":      true,
	"chapsecret":  true,
	"token":       true,
	"backup":      true,
	"code":        true,
	"smbpassword": true,
}

func sanitizeJSON(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	scrubValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(out)
}

func scrubValue(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if secretKeys[strings.ToLower(k)] {
				x[k] = "[redacted]"
			} else {
				scrubValue(val)
			}
		}
	case []any:
		for _, item := range x {
			scrubValue(item)
		}
	}
}
