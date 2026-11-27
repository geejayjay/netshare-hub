package auth

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/logger"
)

// CookieName is the session cookie.
const CookieName = "nsh_session"

// --- request and response types ---

// Request bodies are named types: the name becomes the schema name in the
// OpenAPI spec and the generated TypeScript.
type LoginRequest struct {
	Username string `json:"username" minLength:"1" maxLength:"64"`
	Password string `json:"password" minLength:"1" maxLength:"128"`
}

type SetupRequest struct {
	Username    string `json:"username" minLength:"2" maxLength:"32" example:"admin"`
	DisplayName string `json:"displayName,omitempty" maxLength:"100"`
	Password    string `json:"password" minLength:"8" maxLength:"128"`
}

type SetupStatus struct {
	NeedsSetup bool `json:"needsSetup" doc:"True until the first admin account has been created."`
}

type loginInput struct {
	UserAgent string `header:"User-Agent" hidden:"true"`
	Body      LoginRequest
}

type setupInput struct {
	UserAgent string `header:"User-Agent" hidden:"true"`
	Body      SetupRequest
}

// sessionOutput sets the session cookie and returns the session info.
type sessionOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      SessionInfo
}

type meOutput struct {
	Body SessionInfo
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type setupStatusOutput struct {
	Body SetupStatus
}

// --- routes ---

type handler struct {
	svc           *Service
	secureCookies bool
}

// Register adds the auth and setup routes.
func Register(api huma.API, svc *Service, secureCookies bool) {
	h := &handler{svc: svc, secureCookies: secureCookies}

	httpx.Register(api, httpx.Route{
		ID: "login", Method: http.MethodPost, Path: "/auth/login",
		Summary: "Log in", Tag: "Auth", Access: httpx.Public(),
	}, h.login)

	httpx.Register(api, httpx.Route{
		ID: "logout", Method: http.MethodPost, Path: "/auth/logout",
		Summary: "Log out", Tag: "Auth", Access: httpx.Authenticated().BrowserOnly(),
		Status: http.StatusNoContent,
	}, h.logout)

	httpx.Register(api, httpx.Route{
		ID: "getSession", Method: http.MethodGet, Path: "/auth/me",
		Summary: "Get the current session", Tag: "Auth", Access: httpx.Authenticated(),
	}, h.me)

	httpx.Register(api, httpx.Route{
		ID: "getSetupStatus", Method: http.MethodGet, Path: "/setup",
		Summary: "Check whether first-run setup is needed", Tag: "Setup", Access: httpx.Public(),
	}, h.setupStatus)

	httpx.Register(api, httpx.Route{
		ID: "completeSetup", Method: http.MethodPost, Path: "/setup",
		Summary: "Create the first admin account", Tag: "Setup", Access: httpx.Public(),
		Status: http.StatusCreated,
	}, h.setup)

	registerTokens(api, svc)
}

// --- handlers ---

func (h *handler) login(ctx context.Context, in *loginInput) (*sessionOutput, error) {
	token, info, err := h.svc.Login(ctx, in.Body.Username, in.Body.Password, in.UserAgent)
	if err != nil {
		return nil, err
	}
	return &sessionOutput{SetCookie: h.cookie(token), Body: info}, nil
}

func (h *handler) logout(ctx context.Context, _ *struct{}) (*logoutOutput, error) {
	if err := h.svc.Logout(ctx, httpx.PrincipalFrom(ctx)); err != nil {
		return nil, err
	}
	return &logoutOutput{SetCookie: h.cookie("")}, nil
}

func (h *handler) me(ctx context.Context, _ *struct{}) (*meOutput, error) {
	info, err := h.svc.Current(ctx, httpx.PrincipalFrom(ctx))
	if err != nil {
		return nil, err
	}
	return &meOutput{Body: info}, nil
}

func (h *handler) setupStatus(ctx context.Context, _ *struct{}) (*setupStatusOutput, error) {
	needed, err := h.svc.NeedsSetup(ctx)
	if err != nil {
		return nil, err
	}
	return &setupStatusOutput{Body: SetupStatus{NeedsSetup: needed}}, nil
}

func (h *handler) setup(ctx context.Context, in *setupInput) (*sessionOutput, error) {
	token, info, err := h.svc.Setup(ctx, in.Body.Username, in.Body.DisplayName, in.Body.Password, in.UserAgent)
	if err != nil {
		return nil, err
	}
	return &sessionOutput{SetCookie: h.cookie(token), Body: info}, nil
}

// cookie builds the session cookie. An empty token clears it.
func (h *handler) cookie(token string) http.Cookie {
	c := http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true, // JavaScript can never read the session token
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.svc.opts.SessionTTL.Seconds()),
	}
	if token == "" {
		c.MaxAge = -1
	}
	return c
}

// Middleware looks up the session cookie, or else an API token
// (Authorization: Bearer), and attaches the principal to the request. It
// never rejects a request itself: each route's Access does that.
func Middleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if c, err := r.Cookie(CookieName); err == nil {
				p, err := svc.Resolve(ctx, c.Value)
				if err != nil {
					slog.ErrorContext(ctx, "session lookup failed", "err", err)
				}
				if p != nil {
					ctx = logger.WithUser(httpx.WithPrincipal(ctx, p), p.Username)
				}
			}
			if httpx.PrincipalFrom(ctx) == nil {
				if token := bearerToken(r); token != "" {
					p, err := svc.ResolveToken(ctx, token)
					if err != nil {
						slog.ErrorContext(ctx, "api token lookup failed", "err", err)
					}
					if p != nil {
						ctx = logger.WithUser(httpx.WithPrincipal(ctx, p), p.Username)
						if p.TokenID > 0 {
							ctx = logger.WithTokenID(ctx, p.TokenID)
						}
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
