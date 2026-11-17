package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"

	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/ratelimit"
	"netshare-hub/internal/platform/rbac"
)

// Options are the session rules. They will move to database-backed settings
// when the settings module exists.
type Options struct {
	SessionTTL       time.Duration
	MaxLoginFailures int
	LoginWindow      time.Duration
}

func DefaultOptions() Options {
	return Options{SessionTTL: 12 * time.Hour, MaxLoginFailures: 5, LoginWindow: 5 * time.Minute}
}

// Service holds the login and session rules.
type Service struct {
	repo    *Repo
	users   Users
	opts    Options
	limiter *ratelimit.Limiter
	now     func() time.Time
}

func NewService(repo *Repo, u Users, opts Options) *Service {
	return &Service{
		repo:    repo,
		users:   u,
		opts:    opts,
		limiter: ratelimit.New(opts.MaxLoginFailures, opts.LoginWindow),
		now:     time.Now,
	}
}

// Login checks the credentials and starts a session. It returns the raw
// session token, which goes into the cookie and is never stored.
func (s *Service) Login(ctx context.Context, username, password, userAgent string) (string, SessionInfo, error) {
	ip := httpx.ClientIP(ctx)
	key := ip + "|" + strings.ToLower(strings.TrimSpace(username))

	if blocked, retry := s.limiter.Blocked(key); blocked {
		return "", SessionInfo{}, errs.RateLimited("Too many failed attempts. Please wait and try again.", retry)
	}

	user, err := s.users.Authenticate(ctx, username, password)
	if err != nil {
		if users.IsInvalidCredentials(err) {
			s.limiter.Fail(key)
			slog.WarnContext(ctx, "login failed", "username", username, "ip", ip)
		}
		return "", SessionInfo{}, err
	}
	s.limiter.Reset(key)

	token, info, err := s.start(ctx, user, userAgent)
	if err != nil {
		return "", SessionInfo{}, err
	}
	slog.InfoContext(ctx, "login succeeded", "username", user.Username, "ip", ip)
	return token, info, nil
}

func (s *Service) start(ctx context.Context, user users.User, userAgent string) (string, SessionInfo, error) {
	now := s.now()
	_ = s.repo.DeleteExpired(ctx, now) // opportunistic cleanup

	token, csrf := randomToken(), randomToken()
	err := s.repo.Create(ctx, Session{
		TokenHash: hashToken(token),
		UserID:    user.ID,
		CSRFToken: csrf,
		IP:        httpx.ClientIP(ctx),
		UserAgent: truncate(userAgent, 255),
		CreatedAt: now,
		ExpiresAt: now.Add(s.opts.SessionTTL),
	})
	if err != nil {
		return "", SessionInfo{}, err
	}
	return token, info(user, csrf), nil
}

// Resolve turns a cookie token into the logged-in principal, or nil.
func (s *Service) Resolve(ctx context.Context, token string) (*httpx.Principal, error) {
	if token == "" {
		return nil, nil
	}
	hash := hashToken(token)
	sess, ok, err := s.repo.Get(ctx, hash, s.now())
	if err != nil || !ok {
		return nil, err
	}
	user, err := s.users.Get(ctx, sess.UserID)
	if err != nil || user.Disabled {
		// The account is gone or disabled: end all of its sessions.
		_ = s.repo.DeleteForUser(ctx, sess.UserID)
		return nil, nil
	}
	return &httpx.Principal{
		UserID:      user.ID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		SessionID:   hash,
		CSRFToken:   sess.CSRFToken,
	}, nil
}

// Current returns the session info for the logged-in principal.
func (s *Service) Current(ctx context.Context, p *httpx.Principal) (SessionInfo, error) {
	user, err := s.users.Get(ctx, p.UserID)
	if err != nil {
		return SessionInfo{}, err
	}
	return info(user, p.CSRFToken), nil
}

func (s *Service) Logout(ctx context.Context, p *httpx.Principal) error {
	return s.repo.Delete(ctx, p.SessionID)
}

// NeedsSetup reports whether the first admin still has to be created.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.users.Count(ctx)
	return n == 0, err
}

// Setup creates the first admin and logs them in. It only works while there
// are no users at all.
func (s *Service) Setup(ctx context.Context, username, displayName, password, userAgent string) (string, SessionInfo, error) {
	needed, err := s.NeedsSetup(ctx)
	if err != nil {
		return "", SessionInfo{}, err
	}
	if !needed {
		return "", SessionInfo{}, errs.Conflict(CodeSetupAlreadyDone, "NetShare Hub is already set up. Please log in.")
	}
	admin, err := s.users.Create(ctx, users.CreateInput{
		Username: username, DisplayName: displayName, Role: rbac.RoleAdmin, Password: password,
	})
	if err != nil {
		return "", SessionInfo{}, err
	}
	slog.InfoContext(ctx, "first-run setup completed", "admin", admin.Username)
	return s.start(ctx, admin, userAgent)
}

func info(user users.User, csrf string) SessionInfo {
	return SessionInfo{User: user, Permissions: rbac.Permissions(user.Role), CSRFToken: csrf}
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
