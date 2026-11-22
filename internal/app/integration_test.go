//go:build integration && linux

package app_test

// Step 1.6a against the real system: every preset is created through the
// API, then checked over SMB with smbclient as each kind of user. Run it
// ONLY in the disposable test container, as root:
//
//	go test -p 1 -tags integration ./internal/app/

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
)

// smb runs smbclient commands on a share as a user ("" = guest, no password)
// and returns the combined output.
func smb(t *testing.T, share, user, commands string) string {
	t.Helper()
	args := []string{"//127.0.0.1/" + share, "-c", commands}
	if user == "" {
		args = append(args, "-N")
	} else {
		args = append(args, "-U", user+"%"+pw)
	}
	out, _ := exec.Command("smbclient", args...).CombinedOutput()
	return string(out)
}

const denied = "NT_STATUS_ACCESS_DENIED"

// can reports whether each operation worked for a user.
type can struct {
	connect, list, read, write bool
}

func probe(t *testing.T, share, user string) can {
	t.Helper()
	local := filepath.Join(t.TempDir(), "up.txt")
	os.WriteFile(local, []byte("from "+user), 0o644)
	name := fmt.Sprintf("probe-%s-%d.txt", strings.ReplaceAll(user, "-", ""), time.Now().UnixNano())

	conn := smb(t, share, user, "pwd")
	c := can{connect: !strings.Contains(conn, "NT_STATUS_") && strings.Contains(conn, "Current directory")}
	if !c.connect {
		return c
	}
	c.list = !strings.Contains(smb(t, share, user, "ls"), denied)
	c.read = strings.Contains(smb(t, share, user, "get seed.txt "+filepath.Join(t.TempDir(), "seed.txt")), "getting file")
	c.write = strings.Contains(smb(t, share, user, "put "+local+" "+name), "putting file")
	return c
}

func TestPresetsOverRealSMB(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if _, err := exec.LookPath("smbclient"); err != nil {
		t.Skip("needs smbclient")
	}

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	cfg.Sys = "real"
	application, err := app.New(context.Background(), cfg, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	// writer, other (read or upload, depending on the preset), outsider
	// (has SMB access, not on the share).
	suffix := fmt.Sprint(os.Getpid() % 10000)
	writer, other, outsider := "pw"+suffix, "po"+suffix, "px"+suffix
	ids := map[string]int64{}
	for _, name := range []string{writer, other, outsider} {
		ids[name] = createUser(t, admin, name).ID
	}

	base := fmt.Sprintf("/srv/shares/presets-%d", os.Getpid())
	os.MkdirAll(base, 0o755)
	var shareIDs []int64
	t.Cleanup(func() {
		for _, id := range shareIDs {
			admin.do("DELETE", "/shares/"+itoa(id), nil, nil)
		}
		for _, id := range ids {
			admin.do("DELETE", "/users/"+itoa(id), nil, nil)
		}
		server.Close()
		application.Close()
		os.RemoveAll(base)
	})

	cases := []struct {
		preset      string
		readOnly    bool
		guest       bool
		otherLevel  string
		writer      can
		other       can
		outsider    can
		guestCan    can
		description string
	}{
		{preset: "private", otherLevel: "read",
			writer: can{true, true, true, true}, other: can{true, true, true, false},
			outsider: can{}, guestCan: can{}},
		{preset: "team", otherLevel: "write",
			writer: can{true, true, true, true}, other: can{true, true, true, true},
			outsider: can{}, guestCan: can{}},
		{preset: "public-read", readOnly: true, guest: true, otherLevel: "read",
			writer: can{true, true, true, false}, other: can{true, true, true, false},
			outsider: can{true, true, true, false}, guestCan: can{true, true, true, false}},
		{preset: "dropbox", otherLevel: "upload",
			writer: can{true, true, true, true}, other: can{connect: true, write: true},
			outsider: can{}, guestCan: can{}},
		{preset: "media", guest: true, otherLevel: "read",
			writer: can{true, true, true, true}, other: can{true, true, true, false},
			outsider: can{true, true, true, false}, guestCan: can{true, true, true, false}},
		{preset: "read-only", readOnly: true, otherLevel: "read",
			writer: can{true, true, true, false}, other: can{true, true, true, false},
			outsider: can{}, guestCan: can{}},
	}

	for _, c := range cases {
		t.Run(c.preset, func(t *testing.T) {
			name := "p" + strings.ReplaceAll(c.preset, "-", "")
			dir := filepath.Join(base, c.preset)
			// An existing folder with a file in it, as an admin would have:
			// the file must get the share's permissions too (the fix job).
			os.Mkdir(dir, 0o750)
			os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o600)

			var sh shareBody
			status, body := admin.do("POST", "/shares", map[string]any{
				"name": name, "path": dir, "preset": c.preset, "readOnly": c.readOnly, "guestAccess": c.guest,
				"access": []map[string]any{
					{"userId": ids[writer], "level": "write"},
					{"userId": ids[other], "level": c.otherLevel},
				},
			}, &sh)
			if status != 201 {
				t.Fatalf("create %s: status %d: %s", c.preset, status, body)
			}
			shareIDs = append(shareIDs, sh.ID)
			waitJobs(t, admin)

			for _, who := range []struct {
				label, user string
				want        can
			}{
				{"writer", writer, c.writer}, {"other (" + c.otherLevel + ")", other, c.other},
				{"outsider", outsider, c.outsider}, {"guest", "", c.guestCan},
			} {
				if got := probe(t, name, who.user); got != who.want {
					t.Errorf("%s: %s can %+v, want %+v", c.preset, who.label, got, who.want)
				}
			}
		})
	}

	// The drop box writer sees what the uploader dropped in.
	if out := smb(t, "pdropbox", writer, "ls"); !strings.Contains(out, "probe-po") {
		t.Errorf("the drop box owner should see uploads:\n%s", out)
	}

	// An uploader cannot replace someone else's file by guessing its name
	// (the drop box folder is sticky). Their own file can still be renamed.
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "theirs.txt"), []byte("collector"), 0o644)
	os.WriteFile(filepath.Join(tmp, "mine.txt"), []byte("uploader"), 0o644)
	smb(t, "pdropbox", writer, "put "+filepath.Join(tmp, "theirs.txt")+" collect.txt")
	smb(t, "pdropbox", other, "put "+filepath.Join(tmp, "mine.txt")+" mine.txt")
	if out := smb(t, "pdropbox", other, "rename mine.txt collect.txt -f"); !strings.Contains(out, "NT_STATUS_") {
		t.Errorf("an uploader replaced someone else's file:\n%s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(base, "dropbox", "collect.txt")); string(got) != "collector" {
		t.Errorf("collect.txt was replaced or removed: %q", got)
	}
	// Over SMB, Samba refuses renames by upload-only users anyway. The file
	// manager acts through the kernel as the user, so check the kernel: the
	// sticky bit stops the uploader replacing someone else's file, while
	// their own file is still theirs to rename.
	drop := filepath.Join(base, "dropbox")
	asUploader := func(args ...string) error {
		return exec.Command("sudo", append([]string{"-u", other}, args...)...).Run()
	}
	if err := asUploader("mv", "-f", filepath.Join(drop, "mine.txt"), filepath.Join(drop, "collect.txt")); err == nil {
		t.Error("kernel: an uploader replaced someone else's file in the drop box")
	}
	if err := asUploader("rm", "-f", filepath.Join(drop, "collect.txt")); err == nil {
		t.Error("kernel: an uploader deleted someone else's file in the drop box")
	}
	if got, _ := os.ReadFile(filepath.Join(drop, "collect.txt")); string(got) != "collector" {
		t.Errorf("collect.txt was replaced or removed: %q", got)
	}
	if err := asUploader("mv", filepath.Join(drop, "mine.txt"), filepath.Join(drop, "renamed.txt")); err != nil {
		t.Errorf("kernel: an uploader could not rename their own file: %v", err)
	}

	// --- 1.7: the share list only shows shares a user can open ---
	list := func(user string) string {
		args := []string{"-L", "//127.0.0.1", "-g"}
		if user == "" {
			args = append(args, "-N")
		} else {
			args = append(args, "-U", user+"%"+pw)
		}
		out, _ := exec.Command("smbclient", args...).CombinedOutput()
		return string(out)
	}
	for _, c := range []struct {
		user         string
		see, dontSee []string
	}{
		{writer, []string{"pprivate", "pdropbox", "preadonly", "pmedia"}, nil},
		{outsider, []string{"ppublicread", "pmedia"}, []string{"pprivate", "pteam", "pdropbox", "preadonly"}},
		{"", []string{"ppublicread", "pmedia"}, []string{"pprivate", "pteam", "pdropbox", "preadonly"}},
	} {
		out := list(c.user)
		for _, s := range c.see {
			if !strings.Contains(out, "Disk|"+s+"|") {
				t.Errorf("%q should see share %s:\n%s", c.user, s, out)
			}
		}
		for _, s := range c.dontSee {
			if strings.Contains(out, "Disk|"+s+"|") {
				t.Errorf("%q must not see share %s:\n%s", c.user, s, out)
			}
		}
	}

	// --- 1.7: "hide unreadable" hides what the user cannot read ---
	teamDir := filepath.Join(base, "team")
	os.WriteFile(filepath.Join(teamDir, "salaries.txt"), []byte("secret"), 0o600) // root only, no ACL
	if out := smb(t, "pteam", other, "ls"); !strings.Contains(out, "salaries.txt") {
		t.Fatalf("without the switch the file is listed:\n%s", out)
	}
	if status, _ := admin.do("PATCH", "/shares/"+itoa(shareIDs[1]), map[string]any{"hideUnreadable": true}, nil); status != 200 {
		t.Fatalf("turn on hide unreadable: status %d", status)
	}
	if out := smb(t, "pteam", other, "ls"); strings.Contains(out, "salaries.txt") || !strings.Contains(out, "seed.txt") {
		t.Errorf("with hide unreadable, only readable files are listed:\n%s", out)
	}

	// A link a user planted inside a share is never given an ACL.
	dir := filepath.Join(base, "private")
	if err := os.Symlink("/etc/shadow", filepath.Join(dir, "shadow-link")); err != nil {
		t.Fatal(err)
	}
	before, _ := exec.Command("getfacl", "-cp", "/etc/shadow").CombinedOutput()
	var job struct{ ID, Status string }
	if status, _ := admin.do("POST", "/shares/"+itoa(shareIDs[0])+"/fix-permissions", nil, &job); status != 202 {
		t.Fatalf("fix permissions: status %d", status)
	}
	waitJobs(t, admin)
	after, _ := exec.Command("getfacl", "-cp", "/etc/shadow").CombinedOutput()
	if string(before) != string(after) {
		t.Errorf("fix permissions changed /etc/shadow through a link:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// Deleting a share takes the users' entries off its folder.
	admin.do("DELETE", "/shares/"+itoa(shareIDs[0]), nil, nil)
	shareIDs = shareIDs[1:]
	acl, _ := exec.Command("getfacl", "-cp", dir).CombinedOutput()
	if strings.Contains(string(acl), writer) || strings.Contains(string(acl), other) {
		t.Errorf("an unshared folder still names the share's users:\n%s", acl)
	}
}

// waitJobs waits until no job is queued or running.
func waitJobs(t *testing.T, c *client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var list struct {
			Items []struct{ Status, ErrorMessage string }
		}
		c.do("GET", "/jobs?all=true&active=true", nil, &list)
		if len(list.Items) == 0 {
			var done struct {
				Items []struct{ Status, Title, ErrorMessage string }
			}
			c.do("GET", "/jobs?all=true", nil, &done)
			for _, j := range done.Items {
				if j.Status == "failed" {
					t.Errorf("job %q failed: %s", j.Title, j.ErrorMessage)
				}
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("jobs did not finish")
}

// localIPv4 returns the container's own network address: a client that is
// NOT the loopback address, which Samba always lets in.
func localIPv4(t *testing.T) netip.Addr {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipnet.IP); ok && ip.Unmap().Is4() && !ip.IsLoopback() {
				return ip.Unmap()
			}
		}
	}
	t.Skip("no non-loopback IPv4 address")
	return netip.Addr{}
}

// Step 1.8: network rules, checked from an allowed and a blocked address.
func TestNetworkRulesOverRealSMB(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	ip := localIPv4(t)
	subnet := netip.PrefixFrom(ip, 16).Masked().String()

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	cfg.Sys = "real"
	application, err := app.New(context.Background(), cfg, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	user := fmt.Sprintf("nr%d", os.Getpid()%10000)
	uid := createUser(t, admin, user).ID
	base := fmt.Sprintf("/srv/shares/netrules-%d", os.Getpid())
	os.MkdirAll(base, 0o755)
	var ids []int64
	t.Cleanup(func() {
		for _, id := range ids {
			admin.do("DELETE", "/shares/"+itoa(id), nil, nil)
		}
		admin.do("DELETE", "/users/"+itoa(uid), nil, nil)
		server.Close()
		application.Close()
		os.RemoveAll(base)
	})

	connect := func(share string) bool {
		out, _ := exec.Command("smbclient", "//"+ip.String()+"/"+share, "-U", user+"%"+pw, "-c", "pwd").CombinedOutput()
		return strings.Contains(string(out), "Current directory")
	}

	for _, c := range []struct {
		name  string
		rules []map[string]any
		in    bool
	}{
		{"nrnone", nil, true},
		{"nrallowme", []map[string]any{{"action": "allow", "address": ip.String()}}, true},
		{"nrallowother", []map[string]any{{"action": "allow", "address": "10.99.0.0/24"}}, false},
		{"nrblockme", []map[string]any{{"action": "block", "address": ip.String()}}, false},
		{"nrblockother", []map[string]any{{"action": "block", "address": "10.99.0.0/24"}}, true},
		{"nrsubnet", []map[string]any{{"action": "allow", "address": subnet}}, true},
		{"nrsubnetbutme", []map[string]any{
			{"action": "allow", "address": subnet}, {"action": "block", "address": ip.String()},
		}, false},
	} {
		dir := filepath.Join(base, c.name)
		os.Mkdir(dir, 0o755)
		var sh shareBody
		status, body := admin.do("POST", "/shares", map[string]any{
			"name": c.name, "path": dir, "networkRules": c.rules,
			"access": []map[string]any{{"userId": uid, "level": "write"}},
		}, &sh)
		if status != 201 {
			t.Fatalf("create %s: status %d: %s", c.name, status, body)
		}
		ids = append(ids, sh.ID)
		if got := connect(c.name); got != c.in {
			t.Errorf("%s (rules %v) from %s: connected = %v, want %v", c.name, c.rules, ip, got, c.in)
		}
	}
}
