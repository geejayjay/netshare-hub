package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
)

// client is a tiny API client for tests: it keeps cookies and the CSRF token
// like the real frontend does.
type client struct {
	t    *testing.T
	http *http.Client
	base string
	csrf string
	// bearer, when set, is sent as an API token.
	bearer string
}

type problem struct {
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail"`
	RequestID string `json:"requestId"`
	Errors    []struct {
		Field   string `json:"field"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func newServer(t *testing.T) string {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false // the test server is plain http
	cfg.Sys = "fake"

	application, err := app.New(context.Background(), cfg, app.Options{})
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	server := httptest.NewServer(application.Handler)
	t.Cleanup(func() {
		server.Close()
		application.Close()
	})
	return server.URL + app.APIPrefix
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, http: &http.Client{Jar: jar}, base: base}
}

// do sends a request and decodes the JSON response into out (if not nil).
func (c *client) do(method, path string, body, out any) (int, http.Header) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.base+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return res.StatusCode, res.Header
}

type session struct {
	User        struct{ ID int64 }
	Permissions []string
	CSRFToken   string
}

func (c *client) login(username, password string) {
	c.t.Helper()
	var s session
	status, _ := c.do("POST", "/auth/login", map[string]string{"username": username, "password": password}, &s)
	if status != http.StatusOK {
		c.t.Fatalf("login as %s: status %d", username, status)
	}
	c.csrf = s.CSRFToken
}

func want(t *testing.T, what string, got, expected any) {
	t.Helper()
	if got != expected {
		t.Errorf("%s: got %v, want %v", what, got, expected)
	}
}

func TestAPIFlow(t *testing.T) {
	base := newServer(t)
	admin := newClient(t, base)

	// --- health is public ---
	var health struct{ Status string }
	status, _ := admin.do("GET", "/health", nil, &health)
	want(t, "health status", status, 200)
	want(t, "health body", health.Status, "ok")

	// --- anonymous callers get a 401 problem, before any validation ---
	var p problem
	status, header := admin.do("GET", "/users", nil, &p)
	want(t, "anonymous status", status, 401)
	want(t, "anonymous code", p.Code, "UNAUTHENTICATED")
	want(t, "problem content type", header.Get("Content-Type"), "application/problem+json")
	if p.RequestID == "" || p.RequestID != header.Get("X-Request-ID") {
		t.Errorf("requestId %q should match X-Request-ID header %q", p.RequestID, header.Get("X-Request-ID"))
	}

	// --- first-run setup ---
	var setup struct{ NeedsSetup bool }
	admin.do("GET", "/setup", nil, &setup)
	want(t, "needs setup", setup.NeedsSetup, true)

	// Validation errors name the field and explain the problem in plain words.
	p = problem{}
	status, _ = admin.do("POST", "/setup", map[string]string{"username": "admin", "password": "short"}, &p)
	want(t, "short password status", status, 422)
	want(t, "short password code", p.Code, "VALIDATION_FAILED")
	if len(p.Errors) != 1 {
		t.Fatalf("want 1 field error, got %+v", p.Errors)
	}
	want(t, "field", p.Errors[0].Field, "password")
	want(t, "field code", p.Errors[0].Code, "too_short")
	want(t, "field message", p.Errors[0].Message, "Must be at least 8 characters.")

	// A missing required field is reported against that field.
	p = problem{}
	admin.do("POST", "/setup", map[string]string{"password": "long-enough-password"}, &p)
	if len(p.Errors) != 1 || p.Errors[0].Field != "username" || p.Errors[0].Code != "required" {
		t.Errorf("missing username: got %+v", p.Errors)
	}

	var s session
	status, _ = admin.do("POST", "/setup", map[string]string{"username": "admin", "password": "correct-horse"}, &s)
	want(t, "setup status", status, 201)
	admin.csrf = s.CSRFToken

	// Setup only works once.
	p = problem{}
	status, _ = admin.do("POST", "/setup", map[string]string{"username": "evil", "password": "correct-horse"}, &p)
	want(t, "second setup status", status, 409)
	want(t, "second setup code", p.Code, "SETUP_ALREADY_DONE")

	// --- session ---
	s = session{}
	status, _ = admin.do("GET", "/auth/me", nil, &s)
	want(t, "me status", status, 200)
	if len(s.Permissions) == 0 {
		t.Error("admin should have permissions")
	}

	// --- CSRF: a state-changing request without the token is refused ---
	token := admin.csrf
	admin.csrf = ""
	p = problem{}
	status, _ = admin.do("POST", "/users", map[string]string{"username": "jay", "role": "user", "password": "correct-horse"}, &p)
	want(t, "missing csrf status", status, 403)
	want(t, "missing csrf code", p.Code, "CSRF_INVALID")
	admin.csrf = token

	// --- create users; business rules use the same error shape ---
	for _, name := range []string{"jay", "ana", "ben"} {
		status, _ = admin.do("POST", "/users", map[string]string{"username": name, "role": "user", "password": "correct-horse"}, nil)
		want(t, "create "+name, status, 201)
	}
	p = problem{}
	status, _ = admin.do("POST", "/users", map[string]string{"username": "jay", "role": "wizard", "password": "correct-horse"}, &p)
	want(t, "duplicate status", status, 422)
	fields := map[string]string{}
	for _, e := range p.Errors {
		fields[e.Field] = e.Code
	}
	want(t, "username error", fields["username"], "taken")
	want(t, "role error", fields["role"], "invalid_choice")

	// --- pagination, sorting and search ---
	var list struct {
		Items []struct{ Username string }
		Page  *struct {
			Page, PageSize, Total, TotalPages int
			HasNext                           bool
		}
	}
	admin.do("GET", "/users?pageSize=2&sort=-username", nil, &list)
	if list.Page == nil {
		t.Fatal("paginated list must include page metadata")
	}
	want(t, "items on page", len(list.Items), 2)
	want(t, "total", list.Page.Total, 4)
	want(t, "total pages", list.Page.TotalPages, 2)
	want(t, "has next", list.Page.HasNext, true)
	want(t, "sorted desc", list.Items[0].Username, "jay")

	list.Items, list.Page = nil, nil
	admin.do("GET", "/users?q=an", nil, &list)
	want(t, "search results", len(list.Items), 1)

	p = problem{}
	status, _ = admin.do("GET", "/users?sort=password", nil, &p)
	want(t, "bad sort status", status, 422)

	// An unpaginated list uses the same envelope without "page".
	var roles struct {
		Items []struct{ ID string }
		Page  *struct{}
	}
	admin.do("GET", "/roles", nil, &roles)
	want(t, "roles", len(roles.Items), 4)
	if roles.Page != nil {
		t.Error("unpaginated list must not include page metadata")
	}

	// --- RBAC: a plain user may not manage users ---
	jay := newClient(t, base)
	jay.login("jay", "correct-horse")
	p = problem{}
	status, _ = jay.do("GET", "/users", nil, &p)
	want(t, "user list status", status, 403)
	want(t, "user list code", p.Code, "FORBIDDEN")

	// --- protecting the last admin ---
	p = problem{}
	status, _ = admin.do("DELETE", "/users/1", nil, &p)
	want(t, "delete self status", status, 409)
	want(t, "delete self code", p.Code, "CANNOT_MODIFY_SELF")

	// --- not found uses the module's own code ---
	p = problem{}
	status, _ = admin.do("GET", "/users/999", nil, &p)
	want(t, "not found status", status, 404)
	want(t, "not found code", p.Code, "USER_NOT_FOUND")

	// --- unknown routes stay in the problem format ---
	p = problem{}
	status, _ = admin.do("GET", "/nope", nil, &p)
	want(t, "unknown route status", status, 404)
	want(t, "unknown route code", p.Code, "NOT_FOUND")

	// --- logout ends the session ---
	status, _ = admin.do("POST", "/auth/logout", nil, nil)
	want(t, "logout status", status, 204)
	status, _ = admin.do("GET", "/auth/me", nil, nil)
	want(t, "after logout", status, 401)
}

func TestLoginRateLimit(t *testing.T) {
	base := newServer(t)
	c := newClient(t, base)
	c.do("POST", "/setup", map[string]string{"username": "admin", "password": "correct-horse"}, nil)

	attacker := newClient(t, base)
	var p problem
	for range 5 {
		status, _ := attacker.do("POST", "/auth/login", map[string]string{"username": "admin", "password": "wrong-guess"}, &p)
		want(t, "wrong password status", status, 401)
		want(t, "wrong password code", p.Code, "INVALID_CREDENTIALS")
	}
	status, header := attacker.do("POST", "/auth/login", map[string]string{"username": "admin", "password": "correct-horse"}, &p)
	want(t, "locked out status", status, 429)
	want(t, "locked out code", p.Code, "RATE_LIMITED")
	if header.Get("Retry-After") == "" {
		t.Error("rate-limited response should carry Retry-After")
	}
}
