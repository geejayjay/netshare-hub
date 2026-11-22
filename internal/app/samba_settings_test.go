package app_test

import (
	"strings"
	"testing"

	"netshare-hub/internal/sys"
)

// Step 1.16a: server-wide Samba settings, through the same safe path as
// shares.

type sambaSettings struct {
	ServerName, Workgroup, MinProtocol, MaxProtocol, Encryption, Multichannel string
}

func TestSambaServerSettings(t *testing.T) {
	admin, fake, base := newSystemServer(t)
	jay := createUser(t, admin, "jay")
	admin.do("POST", "/shares", map[string]any{"name": "media", "path": "/srv/media",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, nil)

	// Nothing managed at first: smb.conf decides.
	var s sambaSettings
	status, _ := admin.do("GET", "/settings/samba", nil, &s)
	want(t, "get status", status, 200)
	want(t, "default protocol", s.MinProtocol, "default")
	if strings.Contains(fake.Config, "[global]") {
		t.Errorf("no settings yet, but a [global] section:\n%s", fake.Config)
	}

	all := map[string]any{"serverName": "nas-01", "workgroup": "HOME", "minProtocol": "SMB3_00",
		"maxProtocol": "default", "encryption": "if-supported", "multichannel": "on"}
	status, _ = admin.do("PUT", "/settings/samba", all, &s)
	want(t, "put status", status, 200)
	want(t, "server name kept", s.ServerName, "nas-01")
	want(t, "encryption", s.Encryption, "if-supported")
	for _, line := range []string{"[global]", "netbios name = NAS-01", "workgroup = HOME",
		"server min protocol = SMB3_00", "server smb encrypt = desired", "server multi channel support = yes"} {
		if !strings.Contains(fake.Config, line) {
			t.Errorf("config is missing %q:\n%s", line, fake.Config)
		}
	}
	if strings.Contains(fake.Config, "server max protocol") {
		t.Error("a \"default\" setting must not be written")
	}
	// The shares are still there, and later share changes keep the settings.
	if !strings.Contains(fake.Config, "[media]") {
		t.Error("the shares are gone")
	}
	admin.do("PATCH", "/shares/1", map[string]any{"comment": "Films"}, nil)
	if !strings.Contains(fake.Config, "workgroup = HOME") {
		t.Errorf("a share change lost the server settings:\n%s", fake.Config)
	}

	// --- bad settings are refused, and the running config is untouched ---
	before, applied := fake.Config, fake.Applied
	for _, c := range []struct {
		body        map[string]any
		field, code string
	}{
		{map[string]any{"serverName": "my nas"}, "serverName", "invalid_format"},
		{map[string]any{"serverName": "nas\n[global]"}, "serverName", "invalid_format"},
		{map[string]any{"serverName": "12345"}, "serverName", "invalid_format"},
		{map[string]any{"workgroup": "WG%U"}, "workgroup", "invalid_format"},
		{map[string]any{"minProtocol": "SMB3_11", "maxProtocol": "SMB2_10"}, "maxProtocol", "older_than_min"},
		{map[string]any{"encryption": "required", "minProtocol": "SMB2_10"}, "encryption", "needs_smb3"},
	} {
		body := map[string]any{"serverName": "", "workgroup": "", "minProtocol": "default", "maxProtocol": "default",
			"encryption": "default", "multichannel": "default"}
		for k, v := range c.body {
			body[k] = v
		}
		var p problem
		status, _ := admin.do("PUT", "/settings/samba", body, &p)
		if status != 422 || fieldCodes(p)[c.field] != c.code {
			t.Errorf("%v: status %d, errors %+v; want %s=%s", c.body, status, p.Errors, c.field, c.code)
		}
	}
	// SMB1 and unknown values are not even accepted by the API.
	var p problem
	status, _ = admin.do("PUT", "/settings/samba", map[string]any{"serverName": "", "workgroup": "", "minProtocol": "NT1",
		"maxProtocol": "default", "encryption": "default", "multichannel": "default"}, &p)
	want(t, "smb1 refused", status, 422)
	want(t, "config untouched by refused settings", fake.Config, before)
	want(t, "nothing applied", fake.Applied, applied)

	// Samba itself refuses (say, an older Samba without one of the options):
	// nothing changes, saved settings included, and Samba's words stay in the log.
	fake.FailNext("ApplyConfig", &sys.ConfigError{Output: "Unknown parameter encountered: \"server smb encrypt\""})
	p = problem{}
	status, _ = admin.do("PUT", "/settings/samba", map[string]any{"serverName": "other", "workgroup": "HOME",
		"minProtocol": "SMB3_00", "maxProtocol": "default", "encryption": "required", "multichannel": "on"}, &p)
	want(t, "samba refused status", status, 422)
	want(t, "samba refused code", p.Code, "SAMBA_CONFIG_REFUSED")
	if strings.Contains(p.Detail, "Unknown parameter") {
		t.Errorf("Samba's output leaked to the client: %q", p.Detail)
	}
	want(t, "config after Samba refused", fake.Config, before)
	s = sambaSettings{}
	admin.do("GET", "/settings/samba", nil, &s)
	want(t, "saved settings after Samba refused", s.ServerName, "nas-01")

	// A failed reload keeps the change, and says so.
	fake.FailNext("ApplyConfig", sys.ErrReloadFailed)
	p = problem{}
	status, _ = admin.do("PUT", "/settings/samba", map[string]any{"serverName": "nas-02", "workgroup": "HOME",
		"minProtocol": "default", "maxProtocol": "default", "encryption": "default", "multichannel": "default"}, &p)
	want(t, "reload failed status", status, 503)
	want(t, "reload failed code", p.Code, "SAMBA_RELOAD_FAILED")
	if !strings.Contains(fake.Config, "netbios name = NAS-02") {
		t.Errorf("a failed reload should keep the new config:\n%s", fake.Config)
	}

	// Only those with settings:write may change them.
	jayClient := newClient(t, base)
	jayClient.login("jay", pw)
	status, _ = jayClient.do("PUT", "/settings/samba", all, nil)
	want(t, "plain user", status, 403)
}
