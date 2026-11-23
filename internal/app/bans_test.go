package app_test

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/sys/sambalog"
	"netshare-hub/internal/sys/sysfake"
)

// Step 2.4: SMB brute-force protection.

func login(fake *sysfake.Fake, ip string, user string, ok bool, n int) {
	status := "NT_STATUS_WRONG_PASSWORD"
	if ok {
		status = "NT_STATUS_OK"
	}
	for i := 0; i < n; i++ {
		fake.EmitAudit(sambalog.Event{Kind: sambalog.KindAuth, Time: time.Now(), User: user,
			ClientIP: netip.MustParseAddr(ip), OK: ok, AuthStatus: status})
	}
}

type banBody struct {
	ID       int64
	Address  string
	MAC      string
	Reason   string
	Users    []string
	Offence  int
	Active   bool
	Until    time.Time
	BannedAt time.Time
}

func activeBans(t *testing.T, c *client) []banBody {
	t.Helper()
	var list struct{ Items []banBody }
	c.do("GET", "/guard/bans?active=true", nil, &list)
	return list.Items
}

func waitBans(t *testing.T, c *client, n int) []banBody {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b := activeBans(t, c); len(b) >= n {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("want %d active bans, have %+v", n, activeBans(t, c))
	return nil
}

func banServer(t *testing.T) (*client, *sysfake.Fake) {
	t.Helper()
	admin, fake, _, _ := activityServer(t)
	jay := createUser(t, admin, "jay")
	admin.do("POST", "/shares", map[string]any{"name": "docs", "path": "/srv/docs",
		"access": []map[string]any{{"userId": jay.ID, "level": "write"}}}, nil)
	return admin, fake
}

// The step's "done when", part 1: repeated failures from one address are
// banned.
func TestRepeatedFailuresAreBanned(t *testing.T) {
	admin, fake := banServer(t)
	if !strings.Contains(fake.CurrentConfig(), "log level = 0 auth_json_audit:3") {
		t.Errorf("logins must be logged for brute-force protection:\n%s", fake.CurrentConfig())
	}

	login(fake, "192.168.1.66", "admin", false, 5)
	login(fake, "192.168.1.66", "root", false, 4)
	time.Sleep(200 * time.Millisecond)
	if len(activeBans(t, admin)) != 0 {
		t.Fatal("banned after 9 failures; the default is 10")
	}
	login(fake, "192.168.1.66", "administrator", false, 1)
	ban := waitBans(t, admin, 1)[0]
	if ban.Address != "192.168.1.66" || ban.Reason != "failures" || ban.Offence != 1 || !ban.Active {
		t.Errorf("ban: %+v", ban)
	}
	if strings.Join(ban.Users, ",") != "admin,root,administrator" {
		t.Errorf("names tried: %v", ban.Users)
	}
	if d := ban.Until.Sub(ban.BannedAt); d != 15*time.Minute {
		t.Errorf("first ban lasts %v, want 15m", d)
	}
	// Samba refuses the address; its connections were dropped.
	eventually(t, "hosts deny in the config", func() bool { return strings.Contains(fake.CurrentConfig(), "hosts deny = 192.168.1.66") })
	if d := fake.Disconnected(); len(d) != 1 || d[0].String() != "192.168.1.66" {
		t.Errorf("disconnected: %v", d)
	}
	alerts := activeAlerts(t, admin)
	if len(alerts) != 1 || alerts[0].Kind != "smb.address_banned" || alerts[0].Source != "security" {
		t.Errorf("alerts: %+v", alerts)
	}

	// Lifting it: gone from the config, alert resolved.
	status, _ := admin.do("POST", "/guard/bans/"+itoa(ban.ID)+"/lift", nil, nil)
	want(t, "lift", status, 200)
	if strings.Contains(fake.CurrentConfig(), "hosts deny") {
		t.Errorf("a lifted ban is still in the config:\n%s", fake.CurrentConfig())
	}
	if len(activeAlerts(t, admin)) != 0 {
		t.Error("the alert should be resolved")
	}

	// Again: a repeat within a week lasts 4 times longer.
	login(fake, "192.168.1.66", "admin", false, 10)
	again := waitBans(t, admin, 1)[0]
	if again.Offence != 2 || again.Until.Sub(again.BannedAt) != time.Hour {
		t.Errorf("repeat ban: offence %d, lasts %v; want 2 and 1h", again.Offence, again.Until.Sub(again.BannedAt))
	}
}

// The step's "done when", part 2: the allow-listed admin address is never
// banned, and neither is the server itself.
func TestAllowListedAddressIsNeverBanned(t *testing.T) {
	admin, fake := banServer(t)
	settings := map[string]any{"enabled": true, "failures": 10, "windowMinutes": 10, "banMinutes": 15, "maxBanHours": 168,
		"followDevice": true, "allowList": []string{"192.168.1.10", "10.0.5.0/24"}}
	status, _ := admin.do("PUT", "/guard/bans/settings", settings, nil)
	want(t, "allow list", status, 200)

	login(fake, "192.168.1.10", "admin", false, 50) // the admin mistyping, many times
	login(fake, "10.0.5.77", "admin", false, 50)    // inside an allowed range
	login(fake, "127.0.0.1", "admin", false, 50)    // the server itself
	login(fake, "::1", "admin", false, 50)
	time.Sleep(300 * time.Millisecond)
	if b := activeBans(t, admin); len(b) != 0 {
		t.Errorf("allowed addresses were banned: %+v", b)
	}

	// Adding an address to the allow list lifts its ban.
	login(fake, "192.168.1.99", "admin", false, 10)
	waitBans(t, admin, 1)
	settings["allowList"] = []string{"192.168.1.10", "10.0.5.0/24", "192.168.1.99"}
	admin.do("PUT", "/guard/bans/settings", settings, nil)
	if b := activeBans(t, admin); len(b) != 0 {
		t.Errorf("an address added to the allow list is still banned: %+v", b)
	}
	if strings.Contains(fake.CurrentConfig(), "hosts deny") {
		t.Errorf("config still bans it:\n%s", fake.CurrentConfig())
	}

	// Invalid entries are refused.
	settings["allowList"] = []string{"my-pc.local"}
	var p problem
	status, _ = admin.do("PUT", "/guard/bans/settings", settings, &p)
	want(t, "invalid allow list", status, 422)
	want(t, "invalid allow list field", fieldCodes(p)["allowList[0]"], "invalid_format")
}

func TestSuccessfulLoginResetsTheCount(t *testing.T) {
	admin, fake := banServer(t)
	login(fake, "192.168.1.30", "jay", false, 9)
	login(fake, "192.168.1.30", "jay", true, 1) // got it right in the end
	login(fake, "192.168.1.30", "jay", false, 9)
	time.Sleep(300 * time.Millisecond)
	if b := activeBans(t, admin); len(b) != 0 {
		t.Errorf("banned although a login worked in between: %+v", b)
	}
}

// DHCP: a banned computer that gets a new address is recognised by its
// hardware address and banned again at once.
func TestBannedDeviceWithANewAddressIsBanned(t *testing.T) {
	admin, fake := banServer(t)
	fake.SetMAC(netip.MustParseAddr("192.168.1.66"), "aa:bb:cc:dd:ee:ff")
	login(fake, "192.168.1.66", "admin", false, 10)
	first := waitBans(t, admin, 1)[0]
	if first.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("the ban should record the device: %+v", first)
	}

	// The same computer, a new address from DHCP: one attempt is enough.
	fake.SetMAC(netip.MustParseAddr("192.168.1.67"), "aa:bb:cc:dd:ee:ff")
	login(fake, "192.168.1.67", "admin", false, 1)
	bans := waitBans(t, admin, 2)
	var follow banBody
	for _, b := range bans {
		if b.Address == "192.168.1.67" {
			follow = b
		}
	}
	if follow.Reason != "same_device" || !follow.Until.Equal(first.Until) {
		t.Errorf("follow-up ban: %+v (first until %v)", follow, first.Until)
	}
	eventually(t, "both addresses denied", func() bool {
		return strings.Contains(fake.CurrentConfig(), "192.168.1.66") && strings.Contains(fake.CurrentConfig(), "192.168.1.67")
	})

	// Another computer is not affected.
	fake.SetMAC(netip.MustParseAddr("192.168.1.68"), "11:22:33:44:55:66")
	login(fake, "192.168.1.68", "jay", false, 1)
	time.Sleep(200 * time.Millisecond)
	if n := len(activeBans(t, admin)); n != 2 {
		t.Errorf("another computer was banned: %d bans", n)
	}
}

// IPv6: one attacker controls a whole /64, so that is what is counted and
// banned.
func TestIPv6IsBannedPer64(t *testing.T) {
	admin, fake := banServer(t)
	for i := 0; i < 10; i++ {
		login(fake, "2001:db8:1:2::"+itoa(int64(i+1)), "admin", false, 1)
	}
	ban := waitBans(t, admin, 1)[0]
	if ban.Address != "2001:db8:1:2::/64" {
		t.Errorf("ban address: %s", ban.Address)
	}
	eventually(t, "the /64 denied", func() bool { return strings.Contains(fake.CurrentConfig(), "hosts deny = 2001:db8:1:2::/64") })
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
