package app_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type createdToken struct {
	ID       int64
	Token    string
	Prefix   string
	ReadOnly bool
}

func TestAPITokens(t *testing.T) {
	admin, _, base := newSystemServer(t)

	var full, ro createdToken
	if status, _ := admin.do("POST", "/auth/tokens", map[string]any{"name": "claude"}, &full); status != http.StatusCreated {
		t.Fatalf("create token: status %d", status)
	}
	if !strings.HasPrefix(full.Token, "nsh_") || !strings.HasPrefix(full.Token, full.Prefix) || len(full.Token) < 40 {
		t.Fatalf("token = %+v", full)
	}
	if status, _ := admin.do("POST", "/auth/tokens", map[string]any{"name": "monitor", "readOnly": true, "expiresInDays": 30}, &ro); status != http.StatusCreated {
		t.Fatalf("create read-only token: status %d", status)
	}

	// The list never shows a token again.
	var list struct{ Items []map[string]any }
	admin.do("GET", "/auth/tokens", nil, &list)
	if len(list.Items) != 2 {
		t.Fatalf("tokens = %+v", list.Items)
	}
	for _, item := range list.Items {
		if _, leaked := item["token"]; leaked {
			t.Errorf("listed token carries its secret: %+v", item)
		}
	}

	// A full token acts as its owner, without a cookie or CSRF token.
	bot := newClient(t, base)
	bot.bearer = full.Token
	var me struct{ User struct{ Username string } }
	if status, _ := bot.do("GET", "/auth/me", nil, &me); status != http.StatusOK || me.User.Username != "admin" {
		t.Fatalf("me with token: status %d, %+v", status, me)
	}
	if status, _ := bot.do("POST", "/users", map[string]string{"username": "jay", "role": "user", "password": pw}, nil); status != http.StatusCreated {
		t.Fatalf("create user with token: status %d", status)
	}

	// What a token can never do.
	for _, c := range []struct{ method, path string }{
		{"GET", "/auth/tokens"},
		{"POST", "/auth/tokens"},
		{"DELETE", "/auth/tokens/" + strconv.FormatInt(ro.ID, 10)},
		{"POST", "/auth/logout"},
		{"POST", "/system/backup"},
		{"POST", "/system/restore"},
	} {
		if status, _ := bot.do(c.method, c.path, map[string]any{"name": "x"}, nil); status != http.StatusForbidden {
			t.Errorf("%s %s with a token: status %d, want 403", c.method, c.path, status)
		}
	}

	// A read-only token reads, and nothing else.
	reader := newClient(t, base)
	reader.bearer = ro.Token
	if status, _ := reader.do("GET", "/users", nil, nil); status != http.StatusOK {
		t.Errorf("read with read-only token: status %d", status)
	}
	if status, _ := reader.do("POST", "/users", map[string]string{"username": "kim", "role": "user", "password": pw}, nil); status != http.StatusForbidden {
		t.Errorf("write with read-only token: status %d, want 403", status)
	}

	// Unknown and revoked tokens are anonymous.
	stranger := newClient(t, base)
	stranger.bearer = "nsh_not-a-real-token"
	if status, _ := stranger.do("GET", "/users", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("unknown token: status %d, want 401", status)
	}
	if status, _ := admin.do("DELETE", "/auth/tokens/"+strconv.FormatInt(full.ID, 10), nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke: status %d", status)
	}
	if status, _ := bot.do("GET", "/users", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("revoked token: status %d, want 401", status)
	}

	// A disabled owner's tokens stop working.
	jay := newClient(t, base)
	jay.login("jay", pw)
	var jt createdToken
	jay.do("POST", "/auth/tokens", map[string]any{"name": "jay's script"}, &jt)
	jayBot := newClient(t, base)
	jayBot.bearer = jt.Token
	if status, _ := jayBot.do("GET", "/auth/me", nil, nil); status != http.StatusOK {
		t.Fatalf("jay's token: status %d", status)
	}
	var users struct {
		Items []struct {
			ID       int64
			Username string
		}
	}
	admin.do("GET", "/users", nil, &users)
	for _, u := range users.Items {
		if u.Username == "jay" {
			admin.do("PATCH", "/users/"+strconv.FormatInt(u.ID, 10), map[string]any{"disabled": true}, nil)
		}
	}
	if status, _ := jayBot.do("GET", "/auth/me", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("disabled owner's token: status %d, want 401", status)
	}
	// Someone else's token id cannot be revoked.
	if status, _ := admin.do("DELETE", "/auth/tokens/"+strconv.FormatInt(jt.ID, 10), nil, nil); status != http.StatusNotFound {
		t.Errorf("revoke another user's token: status %d, want 404", status)
	}
}
