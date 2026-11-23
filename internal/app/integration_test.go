//go:build integration && linux

package app_test

// Step 1.6a against the real system: every preset is created through the
// API, then checked over SMB with smbclient as each kind of user. Run it
// ONLY in the disposable test container, as root:
//
//	go test -p 1 -tags integration ./internal/app/

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"netshare-hub/internal/app"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysexec"
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

// Step 1.11a with real users and real file workers: what the API does is
// done AS the user, and the kernel has the last word.
func TestFilesOverRealWorkers(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	// Workers run "<program> fileworker" as the user: build the real program
	// into a folder every user can enter.
	binDir, err := os.MkdirTemp("/usr/local/lib", "nsh-files-itest-")
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(binDir, 0o755)
	t.Cleanup(func() { os.RemoveAll(binDir) })
	bin := filepath.Join(binDir, "netshare-hub")
	if out, err := exec.Command("go", "build", "-o", bin, "netshare-hub/cmd/netshare-hub").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	system, err := sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs(), WorkerExe: bin})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	base := server.URL + app.APIPrefix
	admin := newClient(t, base)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	suffix := fmt.Sprint(os.Getpid() % 10000)
	jayName, anaName := "fj"+suffix, "fa"+suffix
	jayID, anaID := createUser(t, admin, jayName).ID, createUser(t, admin, anaName).ID
	dirs := map[string]string{}
	var shareIDs []int64
	t.Cleanup(func() {
		for _, id := range shareIDs {
			admin.do("DELETE", "/shares/"+itoa(id), nil, nil)
		}
		admin.do("DELETE", "/users/"+itoa(jayID), nil, nil)
		admin.do("DELETE", "/users/"+itoa(anaID), nil, nil)
		server.Close()
		application.Close()
		for _, d := range dirs {
			os.RemoveAll(d)
		}
	})
	for _, name := range []string{"fm" + suffix, "fn" + suffix} {
		dir := fmt.Sprintf("/srv/shares/%s", name)
		os.Mkdir(dir, 0o750)
		dirs[name] = dir
		os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o600) // root's, before sharing
		var sh shareBody
		status, _ := admin.do("POST", "/shares", map[string]any{"name": name, "path": dir, "access": []map[string]any{
			{"userId": jayID, "level": "write"}, {"userId": anaID, "level": "read"},
		}}, &sh)
		if status != 201 {
			t.Fatalf("create share %s: %d", name, status)
		}
		shareIDs = append(shareIDs, sh.ID)
	}
	waitJobs(t, admin) // the fix-permissions jobs for the seed files
	fm, fn := "fm"+suffix, "fn"+suffix

	jay, ana := newClient(t, base), newClient(t, base)
	jay.login(jayName, pw)
	ana.login(anaName, pw)
	uidOf := func(name string) uint32 {
		out, _ := exec.Command("id", "-u", name).Output()
		var n uint32
		fmt.Sscan(strings.TrimSpace(string(out)), &n)
		return n
	}
	owner := func(p string) uint32 {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		return info.Sys().(*syscall.Stat_t).Uid
	}
	item := func(share, p string) map[string]any { return map[string]any{"share": share, "path": p} }

	// What jay makes belongs to jay.
	if status, _ := jay.do("POST", "/files/"+fm+"/folders", map[string]any{"path": "work"}, nil); status != 201 {
		t.Fatalf("mkdir: %d", status)
	}
	if got := owner(filepath.Join(dirs[fm], "work")); got != uidOf(jayName) {
		t.Errorf("folder owner uid %d, want %s's", got, jayName)
	}
	var r resultBody
	jay.do("POST", "/files/copy", map[string]any{"items": []map[string]any{item(fm, "seed.txt")}, "destination": item(fm, "work")}, &r)
	if st, code := waitJob(t, jay, r.Job.ID); st != "succeeded" {
		t.Fatalf("copy job: %s %s", st, code)
	}
	if got := owner(filepath.Join(dirs[fm], "work", "seed.txt")); got != uidOf(jayName) {
		t.Errorf("copied file owner uid %d, want %s's", got, jayName)
	}

	// Moving between shares: the copy is jay's, the original is gone.
	r = resultBody{}
	jay.do("POST", "/files/move", map[string]any{"items": []map[string]any{item(fm, "work")}, "destination": item(fn, "")}, &r)
	if r.Job == nil {
		t.Fatal("a move between shares should be a job")
	}
	if st, code := waitJob(t, jay, r.Job.ID); st != "succeeded" {
		t.Fatalf("move job: %s %s", st, code)
	}
	if _, err := os.Stat(filepath.Join(dirs[fm], "work")); err == nil {
		t.Error("the source is still there after the move")
	}
	if got := owner(filepath.Join(dirs[fn], "work", "seed.txt")); got != uidOf(jayName) {
		t.Errorf("moved file owner uid %d, want %s's", got, jayName)
	}

	// ana reads, but the kernel stops her writing (she is read-only).
	if status, _, body := ana.raw("GET", "/files/"+fn+"/download?path=work/seed.txt", nil); status != 200 || string(body) != "seed" {
		t.Errorf("ana download: %d %q", status, body)
	}
	var p problem
	if status, _ := ana.do("POST", "/files/"+fn+"/folders", map[string]any{"path": "mine"}, &p); status != 403 {
		t.Errorf("ana mkdir on a read-only share: %d", status)
	}

	// A file the kernel does not let jay read stays unread.
	os.WriteFile(filepath.Join(dirs[fm], "root-only.txt"), []byte("root secret"), 0o600)
	p = problem{}
	status, _ := jay.do("GET", "/files/"+fm+"/download?path=root-only.txt", nil, &p)
	if status != 403 || p.Code != "FILE_PERMISSION_DENIED" {
		t.Errorf("download of a root-only file: %d %s", status, p.Code)
	}

	// A link to /etc/shadow is not a way out, for download, copy or zip.
	os.Symlink("/etc/shadow", filepath.Join(dirs[fm], "shadow"))
	if status, _, body := jay.raw("GET", "/files/"+fm+"/download?path=shadow", nil); status == 200 || strings.Contains(string(body), "root:") {
		t.Errorf("download through a link out of the share: %d", status)
	}
	var z struct{ URL string }
	jay.do("POST", "/files/zip", map[string]any{"items": []map[string]any{item(fm, "shadow"), item(fm, "seed.txt")}}, &z)
	if status, _, body := jay.raw("GET", strings.TrimPrefix(z.URL, "/api/v1"), nil); status != 200 || bytes.Contains(body, []byte("root:")) {
		t.Errorf("zip with a link out of the share: %d", status)
	}

	// --- 1.13a: an upload lands as the user, with the folder's permissions ---
	up := startUpload(t, jay, 6, "share", fm, "path", "", "filename", "upload.txt")
	if res := send(jay, up, 0, []byte("abc")); res.status != 204 {
		t.Fatalf("upload part 1: %d %s", res.status, res.body)
	}
	if res := send(jay, up, 3, []byte("def")); res.status != 204 || res.header.Get("NetShare-File-Path") != "upload.txt" {
		t.Fatalf("upload part 2: %d %v", res.status, res.header)
	}
	final := filepath.Join(dirs[fm], "upload.txt")
	if got, _ := os.ReadFile(final); string(got) != "abcdef" {
		t.Errorf("uploaded content %q", got)
	}
	if got := owner(final); got != uidOf(jayName) {
		t.Errorf("uploaded file owner uid %d, want %s's", got, jayName)
	}
	// Inherited from the share folder: ana (read-only) can read it.
	if status, _, body := ana.raw("GET", "/files/"+fm+"/download?path=upload.txt", nil); status != 200 || string(body) != "abcdef" {
		t.Errorf("ana reading jay's upload: %d %q", status, body)
	}
	// The temporary file was hidden over SMB while it existed, and is gone.
	if acl, _ := exec.Command("ls", "-a", dirs[fm]).Output(); strings.Contains(string(acl), ".nsh-upload-") {
		t.Errorf("temporary upload file left behind:\n%s", acl)
	}
	// ana may not upload (read-only), whatever she sends.
	res := ana.tus("POST", "/uploads", map[string]string{"Upload-Length": "3",
		"Upload-Metadata": metadata("share", fm, "path", "", "filename", "ana.txt")}, nil)
	if res.status != 403 {
		t.Errorf("upload by a read-only user: %d", res.status)
	}
}

// Step 1.16a against real Samba: settings reach the running Samba, and a
// bad setting leaves the running config exactly as it was.
func TestSambaSettingsOverRealSamba(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	system, err := sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs()})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken
	defaults := map[string]any{"serverName": "", "workgroup": "", "minProtocol": "default", "maxProtocol": "default",
		"encryption": "default", "multichannel": "default"}
	t.Cleanup(func() {
		admin.do("PUT", "/settings/samba", defaults, nil)
		server.Close()
		application.Close()
	})

	// First, an SMB2-only client gets in (so a refusal below means the
	// setting works, not that Samba is down).
	smb2 := func() string {
		out, _ := exec.Command("smbclient", "-L", "//127.0.0.1", "-N", "--option=client max protocol=SMB2_10").CombinedOutput()
		return string(out)
	}
	for i := 0; i < 50 && !strings.Contains(smb2(), "IPC$"); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if out := smb2(); !strings.Contains(out, "IPC$") {
		t.Fatalf("an SMB2 client cannot connect before the settings change:\n%s", out)
	}

	status, _ := admin.do("PUT", "/settings/samba", map[string]any{"serverName": "nsh-itest", "workgroup": "ITEST",
		"minProtocol": "SMB3_00", "maxProtocol": "default", "encryption": "required", "multichannel": "default"}, nil)
	if status != 200 {
		t.Fatalf("put settings: %d", status)
	}
	// What the running Samba now uses (smb.conf includes the managed file).
	out, _ := exec.Command("testparm", "-s", "-v", "--parameter-name", "workgroup").CombinedOutput()
	if !strings.Contains(string(out), "ITEST") {
		t.Errorf("workgroup not applied: %s", out)
	}
	for param, want := range map[string]string{
		"netbios name": "NSH-ITEST", "server min protocol": "SMB3_00", "server smb encrypt": "required",
	} {
		out, _ := exec.Command("testparm", "-s", "--parameter-name", param).CombinedOutput()
		if !strings.Contains(string(out), want) {
			t.Errorf("%s = %q, want %s", param, strings.TrimSpace(string(out)), want)
		}
	}
	// A client that offers only SMB2 is turned away now.
	if out := smb2(); strings.Contains(out, "IPC$") {
		t.Errorf("an SMB2-only client was let in with min protocol SMB3_00:\n%s", out)
	}

	// A bad setting, even sent straight to the system layer past every
	// check above it, changes nothing: the file Samba runs on is the same.
	before, _ := os.ReadFile(sysexec.DefaultManagedConfig)
	err = system.Samba.ApplyConfig(context.Background(), sys.SambaConfig{
		Server: sys.ServerSettings{NetbiosName: "evil\n[global]\n   guest account = root"}})
	if !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("bad setting: %v, want ErrInvalid", err)
	}
	after, _ := os.ReadFile(sysexec.DefaultManagedConfig)
	if string(before) != string(after) {
		t.Errorf("a refused setting changed the running config:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Step 2.2a against real Samba: turn the activity log on, use the share
// over SMB, and read what happened in the feed.
func TestActivityOverRealSamba(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if m, _ := filepath.Glob("/usr/lib/*/samba/vfs/full_audit.so"); len(m) == 0 {
		t.Skip("needs samba-vfs-modules")
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	system, err := sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs()})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	user := fmt.Sprintf("ac%d", os.Getpid()%10000)
	uid := createUser(t, admin, user).ID
	dir := fmt.Sprintf("/srv/shares/activity-%d", os.Getpid())
	name := "act" + fmt.Sprint(os.Getpid()%10000)
	var sh shareBody
	if status, body := admin.do("POST", "/shares", map[string]any{"name": name, "path": dir, "createDirectory": true,
		"access": []map[string]any{{"userId": uid, "level": "write"}}}, &sh); status != 201 {
		t.Fatalf("create share: %d %v", status, body)
	}
	t.Cleanup(func() {
		admin.do("PUT", "/settings/samba", map[string]any{"serverName": "", "workgroup": "", "minProtocol": "default",
			"maxProtocol": "default", "encryption": "default", "multichannel": "default", "activityLog": false}, nil)
		admin.do("DELETE", "/shares/"+itoa(sh.ID), nil, nil)
		admin.do("DELETE", "/users/"+itoa(uid), nil, nil)
		server.Close()
		application.Close()
		os.RemoveAll(dir)
	})
	if status, _ := admin.do("PUT", "/settings/samba", map[string]any{"serverName": "", "workgroup": "", "minProtocol": "default",
		"maxProtocol": "default", "encryption": "default", "multichannel": "default", "activityLog": true}, nil); status != 200 {
		t.Fatalf("turn the activity log on: %d", status)
	}

	local := filepath.Join(t.TempDir(), "report.txt")
	os.WriteFile(local, []byte("q3"), 0o644)
	exec.Command("smbclient", "//127.0.0.1/"+name, "-U", user+"%"+pw, "-c",
		`mkdir Reports; put `+local+` Reports/report.txt; rename Reports/report.txt Reports/final.txt; del Reports/final.txt`).Run()
	exec.Command("smbclient", "//127.0.0.1/"+name, "-U", user+"%not-the-password", "-c", "ls").Run()

	type entry struct{ User, Share, Op, Path, Path2 string }
	want := map[string]bool{"create_folder Reports": false, "write Reports/report.txt": false,
		"rename Reports/report.txt": false, "delete Reports/final.txt": false, "login_failed": false, "connect": false}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var page struct{ Items []entry }
		admin.do("GET", "/activity?user="+user+"&limit=500", nil, &page)
		for _, e := range page.Items {
			switch {
			case e.Op == "login_failed" || e.Op == "connect":
				want[e.Op] = true
			case e.Share == name:
				want[e.Op+" "+e.Path] = true
			}
		}
		done := true
		for _, ok := range want {
			done = done && ok
		}
		if done {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("not in the activity log: %v", want)
}

// Step 2.3 against real Samba: normal work does not trigger the guard; a
// simulated ransomware run does, and in protect mode the user is blocked
// over real SMB until an admin restores access.
func TestGuardOverRealSamba(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if m, _ := filepath.Glob("/usr/lib/*/samba/vfs/full_audit.so"); len(m) == 0 {
		t.Skip("needs samba-vfs-modules")
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	system, err := sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs()})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	user := fmt.Sprintf("gd%d", os.Getpid()%10000)
	uid := createUser(t, admin, user).ID
	dir := fmt.Sprintf("/srv/shares/guard-%d", os.Getpid())
	name := "gd" + fmt.Sprint(os.Getpid()%10000)
	var sh shareBody
	if status, _ := admin.do("POST", "/shares", map[string]any{"name": name, "path": dir, "createDirectory": true,
		"access": []map[string]any{{"userId": uid, "level": "write"}}}, &sh); status != 201 {
		t.Fatalf("create share: %d", status)
	}
	settings := map[string]any{"serverName": "", "workgroup": "", "minProtocol": "default", "maxProtocol": "default",
		"encryption": "default", "multichannel": "default", "activityLog": true}
	t.Cleanup(func() {
		settings["activityLog"] = false
		admin.do("PUT", "/settings/samba", settings, nil)
		admin.do("DELETE", "/shares/"+itoa(sh.ID), nil, nil)
		admin.do("DELETE", "/users/"+itoa(uid), nil, nil)
		server.Close()
		application.Close()
		os.RemoveAll(dir)
	})
	if status, _ := admin.do("PUT", "/settings/samba", settings, nil); status != 200 {
		t.Fatalf("activity log on: %d", status)
	}
	if status, _ := admin.do("PUT", "/guard/settings", map[string]any{"mode": "protect", "renames": 20, "noteFolders": 3}, nil); status != 200 {
		t.Fatalf("protect mode: %d", status)
	}
	smbRun := func(commands string) string {
		out, _ := exec.Command("smbclient", "//127.0.0.1/"+name, "-U", user+"%"+pw, "-c", commands).CombinedOutput()
		return string(out)
	}
	incidents := func() []incidentBody {
		var list struct{ Items []incidentBody }
		admin.do("GET", "/guard/incidents", nil, &list)
		return list.Items
	}

	// Normal work: 150 documents uploaded, then moved into another folder.
	local := t.TempDir()
	var put, move strings.Builder
	put.WriteString("mkdir Inbox; mkdir Archive; ")
	for i := 0; i < 150; i++ {
		f := filepath.Join(local, fmt.Sprintf("doc%03d.docx", i))
		os.WriteFile(f, []byte("report"), 0o644)
		fmt.Fprintf(&put, "put %s Inbox/doc%03d.docx; ", f, i)
		fmt.Fprintf(&move, "rename Inbox/doc%03d.docx Archive/doc%03d.docx; ", i, i)
	}
	smbRun(put.String())
	smbRun(move.String())
	time.Sleep(3 * time.Second) // the activity has been read by now
	if got := incidents(); len(got) != 0 {
		t.Fatalf("normal work triggered the guard: %+v", got)
	}

	// Ransomware: the same documents renamed to .locked, quickly.
	var encrypt strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&encrypt, "rename Archive/doc%03d.docx Archive/doc%03d.docx.locked; ", i, i)
	}
	smbRun(encrypt.String())
	var inc []incidentBody
	for i := 0; i < 100 && len(inc) == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		inc = incidents()
	}
	if len(inc) != 1 || !inc[0].Blocked || inc[0].Username != user {
		t.Fatalf("incident: %+v", inc)
	}
	// The user can no longer write over SMB, but can still read.
	time.Sleep(500 * time.Millisecond)
	note := filepath.Join(local, "note.txt")
	os.WriteFile(note, []byte("x"), 0o644)
	if out := smbRun("put " + note + " blocked.txt"); !strings.Contains(out, "NT_STATUS_") {
		t.Errorf("a blocked user could still write:\n%s", out)
	}
	if out := smbRun("ls Archive/doc100.docx"); !strings.Contains(out, "doc100.docx") {
		t.Errorf("a blocked user should still read:\n%s", out)
	}

	// An admin restores access: writing works again.
	if status, _ := admin.do("POST", "/guard/incidents/"+itoa(inc[0].ID)+"/restore", nil, nil); status != 200 {
		t.Fatalf("restore: %d", status)
	}
	if out := smbRun("put " + note + " restored.txt"); strings.Contains(out, "NT_STATUS_") {
		t.Errorf("writing after restore failed:\n%s", out)
	}
}

// Step 2.4 against real Samba: an allow-listed address is never banned;
// repeated failures from another get it refused before it can even try a
// password; lifting the ban lets it in again.
func TestBansOverRealSamba(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	ip := localIPv4(t) // not loopback: the server itself is never banned
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.SecureCookies = false
	system, err := sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs()})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(context.Background(), cfg, app.Options{System: &system})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler)
	admin := newClient(t, server.URL+app.APIPrefix)
	var s session
	admin.do("POST", "/setup", map[string]string{"username": "admin", "password": pw}, &s)
	admin.csrf = s.CSRFToken

	user := fmt.Sprintf("bf%d", os.Getpid()%10000)
	uid := createUser(t, admin, user).ID
	dir := fmt.Sprintf("/srv/shares/bans-%d", os.Getpid())
	name := "bf" + fmt.Sprint(os.Getpid()%10000)
	var sh shareBody
	admin.do("POST", "/shares", map[string]any{"name": name, "path": dir, "createDirectory": true,
		"access": []map[string]any{{"userId": uid, "level": "write"}}}, &sh)
	settings := map[string]any{"enabled": true, "failures": 10, "windowMinutes": 10, "banMinutes": 15, "maxBanHours": 168,
		"followDevice": true, "allowList": []string{ip.String()}}
	t.Cleanup(func() {
		var list struct{ Items []banBody }
		admin.do("GET", "/guard/bans?active=true", nil, &list)
		for _, b := range list.Items {
			admin.do("POST", "/guard/bans/"+itoa(b.ID)+"/lift", nil, nil)
		}
		admin.do("DELETE", "/shares/"+itoa(sh.ID), nil, nil)
		admin.do("DELETE", "/users/"+itoa(uid), nil, nil)
		server.Close()
		application.Close()
		os.RemoveAll(dir)
	})
	if status, _ := admin.do("PUT", "/guard/bans/settings", settings, nil); status != 200 {
		t.Fatalf("settings: %d", status)
	}
	try := func(password string) string {
		out, _ := exec.Command("smbclient", "//"+ip.String()+"/"+name, "-U", user+"%"+password, "-c", "pwd").CombinedOutput()
		return string(out)
	}
	bans := func() []banBody {
		var list struct{ Items []banBody }
		admin.do("GET", "/guard/bans?active=true", nil, &list)
		return list.Items
	}
	if out := try(pw); !strings.Contains(out, "Current directory") {
		t.Fatalf("the user cannot connect to begin with:\n%s", out)
	}

	// Allow-listed: as many mistakes as it takes, never banned.
	for i := 0; i < 25; i++ {
		try("wrong-password")
	}
	time.Sleep(2 * time.Second)
	if b := bans(); len(b) != 0 {
		t.Fatalf("an allow-listed address was banned: %+v", b)
	}

	// Not allow-listed: 10 failures and it is out.
	settings["allowList"] = []string{}
	admin.do("PUT", "/guard/bans/settings", settings, nil)
	for i := 0; i < 10; i++ {
		try("wrong-password")
	}
	var b []banBody
	for i := 0; i < 100 && len(b) == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		b = bans()
	}
	if len(b) != 1 || b[0].Address != ip.String() {
		t.Fatalf("bans: %+v", b)
	}
	// Even the right password does not get in: Samba refuses the address
	// when it connects, before any login.
	if out := try(pw); strings.Contains(out, "Current directory") {
		t.Errorf("a banned address still got in:\n%s", out)
	}
	if out, _ := exec.Command("testparm", "-s", "--parameter-name", "hosts deny").CombinedOutput(); !strings.Contains(string(out), ip.String()) {
		t.Errorf("hosts deny: %s", out)
	}

	// Lifted: in again.
	if status, _ := admin.do("POST", "/guard/bans/"+itoa(b[0].ID)+"/lift", nil, nil); status != 200 {
		t.Fatalf("lift: %d", status)
	}
	if out := try(pw); !strings.Contains(out, "Current directory") {
		t.Errorf("after lifting the ban:\n%s", out)
	}
}
