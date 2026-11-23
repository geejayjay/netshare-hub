package app_test

import (
	"testing"

	"netshare-hub/internal/sys"
)

// Step 2.7a: ending one SMB session.
func TestDisconnectOneSession(t *testing.T) {
	admin, fake, base := newSystemServer(t)
	fake.SetSessions([]sys.Session{
		{ID: "111", PID: 43, Username: "jay", ClientIP: "192.168.1.20", Dialect: "SMB3_11", Shares: []string{"media"}},
		{ID: "222", PID: 44, Username: "ana", ClientIP: "192.168.1.21", Dialect: "SMB3_11", Shares: []string{"docs"}},
	})
	var list struct {
		Items []struct{ ID, Username string }
	}
	status, _ := admin.do("GET", "/system/sessions", nil, &list)
	want(t, "list", status, 200)
	want(t, "sessions", len(list.Items), 2)

	status, _ = admin.do("POST", "/system/sessions/111/disconnect", nil, nil)
	want(t, "disconnect", status, 204)
	list.Items = nil
	admin.do("GET", "/system/sessions", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Username != "ana" {
		t.Errorf("only jay's session should be gone: %+v", list.Items)
	}

	var p problem
	status, _ = admin.do("POST", "/system/sessions/111/disconnect", nil, &p)
	want(t, "already gone", status, 404)
	want(t, "already gone code", p.Code, "SESSION_NOT_FOUND")
	status, _ = admin.do("POST", "/system/sessions/1%3B2/disconnect", nil, nil)
	want(t, "malformed id", status, 422)

	createUser(t, admin, "jay")
	plain := newClient(t, base)
	plain.login("jay", pw)
	if status, _ := plain.do("GET", "/system/sessions", nil, nil); status != 403 {
		t.Errorf("a plain user listed sessions: %d", status)
	}
	if status, _ := plain.do("POST", "/system/sessions/222/disconnect", nil, nil); status != 403 {
		t.Errorf("a plain user disconnected a session: %d", status)
	}
}
