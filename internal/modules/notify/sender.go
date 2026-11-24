package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"netshare-hub/internal/modules/alerts"
)

func (s *Service) send(ctx context.Context, ch storedChannel, a alerts.Alert) error {
	switch ch.Kind {
	case KindNtfy:
		return s.sendNtfy(ctx, ch.rawConfig, a)
	case KindGotify:
		return s.sendGotify(ctx, ch.rawConfig, a)
	case KindWebhook:
		return s.sendWebhook(ctx, ch.rawConfig, a)
	case KindSMTP:
		return s.sendSMTP(ctx, ch.rawConfig, a)
	default:
		return fmt.Errorf("unknown channel kind %q", ch.Kind)
	}
}

func (s *Service) sendNtfy(ctx context.Context, cfg ChannelConfig, a alerts.Alert) error {
	body := a.Message
	if body == "" {
		body = a.Title
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(cfg.URL), strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", a.Title)
	req.Header.Set("Tags", fmt.Sprintf("netshare-hub,%s,%s", a.Source, a.Severity))
	switch a.Severity {
	case alerts.Critical:
		req.Header.Set("Priority", "5")
	case alerts.Error:
		req.Header.Set("Priority", "4")
	case alerts.Warning:
		req.Header.Set("Priority", "3")
	default:
		req.Header.Set("Priority", "2")
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	return s.doHTTP(req)
}

func (s *Service) sendGotify(ctx context.Context, cfg ChannelConfig, a alerts.Alert) error {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	endpoint := base + "/message"
	priority := 4
	switch a.Severity {
	case alerts.Critical:
		priority = 10
	case alerts.Error:
		priority = 8
	case alerts.Warning:
		priority = 5
	case alerts.Info:
		priority = 2
	}
	payload := map[string]any{
		"title":    a.Title,
		"message":  a.Message,
		"priority": priority,
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", cfg.Token)
	return s.doHTTP(req)
}

func (s *Service) sendWebhook(ctx context.Context, cfg ChannelConfig, a alerts.Alert) error {
	// Include both structured fields and Discord/Slack-compatible "content"/"text".
	summary := fmt.Sprintf("[%s] %s", strings.ToUpper(string(a.Severity)), a.Title)
	if a.Message != "" {
		summary += "\n" + a.Message
	}
	payload := map[string]any{
		"event":    "alert",
		"content":  summary,
		"text":     summary,
		"severity": a.Severity,
		"source":   a.Source,
		"kind":     a.Kind,
		"title":    a.Title,
		"message":  a.Message,
		"time":     time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(cfg.URL), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		mac.Write(raw)
		req.Header.Set("X-NetShare-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return s.doHTTP(req)
}

func (s *Service) sendSMTP(ctx context.Context, cfg ChannelConfig, a alerts.Alert) error {
	addr := net.JoinHostPort(strings.TrimSpace(cfg.Host), strconv.Itoa(cfg.Port))
	recipients := make([]string, 0)
	for _, r := range strings.Split(cfg.To, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			recipients = append(recipients, r)
		}
	}
	if len(recipients) == 0 {
		return fmt.Errorf("no SMTP recipients configured")
	}
	subject := fmt.Sprintf("[NetShare Hub %s] %s", strings.ToUpper(string(a.Severity)), sanitizeHeader(a.Title))
	var msg strings.Builder
	msg.WriteString("From: " + sanitizeHeader(cfg.From) + "\r\n")
	msg.WriteString("To: " + sanitizeHeader(strings.Join(recipients, ", ")) + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	msg.WriteString(a.Title + "\r\n\r\n")
	if a.Message != "" {
		msg.WriteString(a.Message + "\r\n\r\n")
	}
	msg.WriteString(fmt.Sprintf("Source: %s | Severity: %s | Kind: %s\r\n", a.Source, a.Severity, a.Kind))

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, strings.TrimSpace(cfg.Host))
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- smtp.SendMail(addr, auth, strings.TrimSpace(cfg.From), recipients, []byte(msg.String()))
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func sanitizeHeader(v string) string {
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	return strings.TrimSpace(v)
}

func (s *Service) doHTTP(req *http.Request) error {
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}
