package app_test

import (
	"strings"
	"testing"

	"netshare-hub/internal/sys"
)

// Step 3.5a: Time Machine shares, Mac support and network discovery.

type macSettings struct {
	MacSupport  bool
	ActivityLog bool
}

var serverDefaults = map[string]any{"serverName": "", "workgroup": "", "minProtocol": "default",
	"maxProtocol": "default", "encryption": "default", "multichannel": "default"}

func withMac(on bool) map[string]any {
	out := map[string]any{"macSupport": on}
	for k, v := range serverDefaults {
		out[k] = v
	}
	return out
}

func TestTimeMachinePreset(t *testing.T) {
	admin, _, _, _ := activityServer(t)
	var presets struct {
		Items []struct {
			ID           string
			TimeMachine  bool
			ReadOnly     bool
			GuestAccess  bool
			DefaultLevel string
			Levels       []string
		}
	}
	admin.do("GET", "/shares/presets", nil, &presets)
	found := false
	for _, p := range presets.Items {
		if p.ID == "time-machine" {
			found = true
			if !p.TimeMachine || p.ReadOnly || p.GuestAccess || p.DefaultLevel != "write" || strings.Join(p.Levels, ",") != "write" {
				t.Errorf("preset: %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("no time-machine preset in %+v", presets.Items)
	}
}

func TestTimeMachineShare(t *testing.T) {
	admin, fake, _, _ := activityServer(t)
	jay := createUser(t, admin, "jay")

	var mac macSettings
	admin.do("GET", "/settings/samba", nil, &mac)
	want(t, "Mac support starts off", mac.MacSupport, false)

	var sh struct {
		ID                int64
		TimeMachine       bool
		TimeMachineMaxGiB int
		Preset            string
	}
	status, _ := admin.do("POST", "/shares", map[string]any{"name": "backup", "path": "/srv/backup", "preset": "time-machine",
		"timeMachine": true, "timeMachineMaxGiB": 500, "access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, &sh)
	want(t, "create", status, 201)
	if !sh.TimeMachine || sh.TimeMachineMaxGiB != 500 || sh.Preset != "time-machine" {
		t.Errorf("share: %+v", sh)
	}
	sec := section(t, fake, "backup")
	for _, line := range []string{"vfs objects = catia fruit streams_xattr", "fruit:time machine = yes", "fruit:time machine max size = 500G"} {
		if !strings.Contains(sec, line) {
			t.Errorf("missing %q:\n%s", line, sec)
		}
	}
	// Creating it turned Mac support on, and it stays on while the share is
	// a Time Machine share.
	admin.do("GET", "/settings/samba", nil, &mac)
	want(t, "Mac support after a Time Machine share", mac.MacSupport, true)
	var p problem
	status, _ = admin.do("PUT", "/settings/samba", withMac(false), &p)
	want(t, "Mac support off with a Time Machine share", status, 422)
	if fieldCodes(p)["macSupport"] != "needed_by_time_machine" || !strings.Contains(p.Errors[0].Message, "backup") {
		t.Errorf("problem: %+v", p)
	}

	// Turning Time Machine off takes the limit with it; Mac support stays
	// (Macs already connected expect it) until the admin turns it off.
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"timeMachine": false}, &sh)
	want(t, "time machine off", status, 200)
	if sh.TimeMachine || sh.TimeMachineMaxGiB != 0 {
		t.Errorf("share: %+v", sh)
	}
	if sec := section(t, fake, "backup"); strings.Contains(sec, "time machine") || !strings.Contains(sec, "fruit") {
		t.Errorf("after time machine off:\n%s", sec)
	}
	status, _ = admin.do("PUT", "/settings/samba", withMac(false), &mac)
	want(t, "Mac support off", status, 200)
	if strings.Contains(fake.CurrentConfig(), "fruit") {
		t.Errorf("Mac support off, but:\n%s", fake.CurrentConfig())
	}

	// Turning it on again on an existing share turns Mac support on too.
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"timeMachine": true}, &sh)
	want(t, "time machine on again", status, 200)
	admin.do("GET", "/settings/samba", nil, &mac)
	want(t, "Mac support on again", mac.MacSupport, true)
}

func TestTimeMachineRules(t *testing.T) {
	admin, _, _, _ := activityServer(t)
	jay := createUser(t, admin, "jay")
	cases := []struct {
		name  string
		body  map[string]any
		field string
		code  string
	}{
		{"guest", map[string]any{"guestAccess": true}, "guestAccess", "not_with_time_machine"},
		{"read-only", map[string]any{"readOnly": true}, "readOnly", "not_with_time_machine"},
		{"upload", map[string]any{"access": []map[string]any{{"userId": jay.ID, "level": "upload"}}}, "access[0].level", "not_with_time_machine"},
	}
	for _, c := range cases {
		body := map[string]any{"name": "backup", "path": "/srv/backup", "timeMachine": true,
			"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}
		for k, v := range c.body {
			body[k] = v
		}
		var p problem
		status, _ := admin.do("POST", "/shares", body, &p)
		if status != 422 || fieldCodes(p)[c.field] != c.code {
			t.Errorf("%s: %d %+v", c.name, status, p.Errors)
		}
	}
	// A size limit without Time Machine.
	var p problem
	status, _ := admin.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs", "timeMachineMaxGiB": 10}, &p)
	if status != 422 || fieldCodes(p)["timeMachineMaxGiB"] != "needs_time_machine" {
		t.Errorf("limit without time machine: %d %+v", status, p.Errors)
	}
	// The preview says the same before saving.
	p = problem{}
	status, _ = admin.do("POST", "/shares/effective-access", map[string]any{"timeMachine": true,
		"access": []map[string]any{{"userId": jay.ID, "level": "upload"}}}, &p)
	if status != 422 || fieldCodes(p)["access[0].level"] != "not_with_time_machine" {
		t.Errorf("preview: %d %+v", status, p.Errors)
	}
	// Making an existing guest share a Time Machine share.
	var sh shareBody
	admin.do("POST", "/shares", map[string]any{"name": "public", "path": "/srv/public", "guestAccess": true, "readOnly": true}, &sh)
	p = problem{}
	status, _ = admin.do("PATCH", "/shares/"+itoa(sh.ID), map[string]any{"timeMachine": true}, &p)
	if status != 422 || fieldCodes(p)["guestAccess"] != "not_with_time_machine" {
		t.Errorf("guest share to time machine: %d %+v", status, p.Errors)
	}
}

func TestMacSupportNeedsModules(t *testing.T) {
	admin, fake, _, _ := activityServer(t)
	fake.MissingModules = []string{"fruit"}
	var p problem
	status, _ := admin.do("PUT", "/settings/samba", withMac(true), &p)
	if status != 422 || p.Code != "SAMBA_MODULE_MISSING" || !strings.Contains(p.Detail, "fruit") {
		t.Errorf("missing module: %d %+v", status, p)
	}
	var mac macSettings
	admin.do("GET", "/settings/samba", nil, &mac)
	want(t, "nothing changed", mac.MacSupport, false)
	fake.MissingModules = nil
	status, _ = admin.do("PUT", "/settings/samba", withMac(true), nil)
	want(t, "with the modules", status, 200)
	if !strings.Contains(fake.CurrentConfig(), "fruit:metadata = stream") {
		t.Errorf("config:\n%s", fake.CurrentConfig())
	}
}

type discoveryBody struct {
	Systemd   bool
	WSD, MDNS struct {
		Installed, Running, Enabled, On bool
		Unit, Hint                      string
	}
}

func TestDiscoverySettings(t *testing.T) {
	admin, fake, base, _ := activityServer(t)
	var d discoveryBody
	status, _ := admin.do("GET", "/settings/discovery", nil, &d)
	want(t, "get", status, 200)
	if !d.Systemd || !d.WSD.Installed || d.WSD.On || d.MDNS.On {
		t.Errorf("initial: %+v", d)
	}
	status, _ = admin.do("PUT", "/settings/discovery", map[string]any{"wsd": true, "mdns": true}, &d)
	want(t, "switch on", status, 200)
	if !d.WSD.On || !d.MDNS.On || !d.WSD.Running || !d.MDNS.Enabled {
		t.Errorf("after on: %+v", d)
	}
	// Only what is sent changes.
	admin.do("PUT", "/settings/discovery", map[string]any{"wsd": false}, &d)
	if d.WSD.On || !d.MDNS.On {
		t.Errorf("after wsd off: %+v", d)
	}

	// Not installed: refused, with what to install.
	fake.SetDiscoveryStatus(sys.DiscoveryStatus{Systemd: true, MDNS: sys.ServiceState{Installed: true, Unit: "avahi-daemon.service"}})
	var p problem
	status, _ = admin.do("PUT", "/settings/discovery", map[string]any{"wsd": true}, &p)
	if status != 409 || p.Code != "DISCOVERY_UNAVAILABLE" || !strings.Contains(p.Detail, "apt install wsdd") {
		t.Errorf("not installed: %d %+v", status, p)
	}
	// No systemd: says so.
	fake.SetDiscoveryStatus(sys.DiscoveryStatus{})
	admin.do("GET", "/settings/discovery", nil, &d)
	if d.Systemd || !strings.Contains(d.MDNS.Hint, "not run by systemd") {
		t.Errorf("no systemd: %+v", d)
	}

	// A plain user can neither read nor switch it.
	createUser(t, admin, "jay")
	jay := newClient(t, base)
	jay.login("jay", pw)
	if status, _ := jay.do("GET", "/settings/discovery", nil, nil); status != 403 {
		t.Errorf("user reads discovery: %d", status)
	}
	if status, _ := jay.do("PUT", "/settings/discovery", map[string]any{"wsd": true}, nil); status != 403 {
		t.Errorf("user switches discovery: %d", status)
	}
}
