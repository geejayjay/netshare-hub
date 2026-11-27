package agent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"netshare-hub/internal/agent"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sambalog"
	"netshare-hub/internal/sys/sysfake"
)

var ctx = context.Background()

// start runs the agent handler in front of a fake system and returns a
// client for it. allow decides whether callers are accepted.
func start(t *testing.T, allow bool) (*agent.Client, *sysfake.Fake, string) {
	t.Helper()
	fake := sysfake.New()
	authorize := func(*http.Request) (agent.Peer, bool) { return agent.Peer{UID: 1000, PID: 42}, allow }
	server := httptest.NewServer(agent.NewServer(fake.System(), authorize, "test"))
	t.Cleanup(server.Close)
	return agent.NewClient(server.Client(), server.URL), fake, server.URL
}

func TestRoundTrip(t *testing.T) {
	client, fake, _ := start(t, true)
	system := client.System()

	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Accounts.
	if err := system.Accounts.CreateUser(ctx, "jay", "first-password"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if acc := fake.Account("jay"); acc == nil || acc.Password != "first-password" {
		t.Fatalf("account after CreateUser = %+v", acc)
	}
	if err := system.Accounts.SetPassword(ctx, "jay", "second-password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if err := system.Accounts.SetEnabled(ctx, "jay", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if acc := fake.Account("jay"); acc.Password != "second-password" || acc.Enabled {
		t.Fatalf("account after changes = %+v", acc)
	}

	// Shares: the agent side builds the config from structured data.
	err := system.Samba.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{
		{Name: "media", Path: "/srv/media", Comment: "Films", Readers: []string{"jay"}, Writers: []string{"jay"}},
	}})
	if err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	for _, want := range []string{"[media]", "path = /srv/media", "valid users = jay", "write list = jay"} {
		if !strings.Contains(fake.Config, want) {
			t.Errorf("config is missing %q:\n%s", want, fake.Config)
		}
	}
	// An empty list is valid: it removes every share.
	if err := system.Samba.ApplyConfig(ctx, sys.SambaConfig{}); err != nil {
		t.Fatalf("ApplyConfig(nil): %v", err)
	}
	if strings.Contains(fake.Config, "[media]") {
		t.Error("shares were not removed")
	}

	// Files.
	real, err := system.Files.CreateShareDir(ctx, "/missing/new")
	if err != nil || real != "/missing/new" {
		t.Fatalf("CreateShareDir = %q, %v", real, err)
	}
	if real, err := system.Files.ResolveDir(ctx, "/missing/new"); err != nil || real != "/missing/new" {
		t.Fatalf("ResolveDir = %q, %v", real, err)
	}

	if err := system.Accounts.DeleteUser(ctx, "jay"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if fake.Account("jay") != nil {
		t.Error("account still exists after DeleteUser")
	}
}

// Services use errors.Is on what the system layer returns. That must keep
// working when the system layer is on the other side of the socket.
func TestErrorsSurviveTheSocket(t *testing.T) {
	client, fake, _ := start(t, true)
	system := client.System()

	cases := []struct {
		name string
		call func() error
		want error
	}{
		{"system account name", func() error { return system.Accounts.CreateUser(ctx, "root", "password-123") }, sys.ErrNameTaken},
		{"invalid name", func() error { return system.Accounts.CreateUser(ctx, "-rf", "password-123") }, sys.ErrInvalid},
		{"missing account", func() error { return system.Accounts.SetPassword(ctx, "ghost", "password-123") }, sys.ErrNotFound},
		{"unmanaged account", func() error { return system.Accounts.SetEnabled(ctx, "root", false) }, sys.ErrNotManaged},
		{"missing folder", func() error { _, err := system.Files.ResolveDir(ctx, "/missing/x"); return err }, fs.ErrNotExist},
		{"folder exists", func() error {
			system.Files.CreateShareDir(ctx, "/missing/dup")
			_, err := system.Files.CreateShareDir(ctx, "/missing/dup")
			return err
		}, fs.ErrExist},
		{"reload failed", func() error {
			fake.FailNext("ApplyConfig", sys.ErrReloadFailed)
			return system.Samba.ApplyConfig(ctx, sys.SambaConfig{})
		}, sys.ErrReloadFailed},
	}
	for _, c := range cases {
		if err := c.call(); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}

	fake.FailNext("ApplyConfig", &sys.ConfigError{Output: "Unknown parameter"})
	var cfgErr *sys.ConfigError
	if err := system.Samba.ApplyConfig(ctx, sys.SambaConfig{}); !errors.As(err, &cfgErr) || cfgErr.Output != "Unknown parameter" {
		t.Errorf("config error: got %v", err)
	}
}

// The agent is the enforcement point: it must refuse a dangerous request
// even when the web server (here: the test) sends it on purpose.
func TestAgentEnforcesTheRulesItself(t *testing.T) {
	client, fake, _ := start(t, true)
	system := client.System()

	bad := map[string]sys.ShareSpec{
		"system folder":     {Name: "x", Path: "/etc", Readers: []string{"jay"}},
		"root folder":       {Name: "x", Path: "/"},
		"config injection":  {Name: "x", Path: "/srv/x", Comment: "a\n[global]\n   guest ok = yes"},
		"samba variable":    {Name: "x", Path: "/srv/%U"},
		"section injection": {Name: "x]\n[global", Path: "/srv/x"},
		"group as user":     {Name: "x", Path: "/srv/x", Readers: []string{"@root"}},
	}
	for name, spec := range bad {
		if err := system.Samba.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{spec}}); !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if fake.Applied != 0 {
		t.Errorf("a refused request changed the config (%d applies)", fake.Applied)
	}

	// The agent is not an oracle for which paths exist on the system.
	for _, p := range []string{"/etc/shadow", "/root/.ssh", "/", "relative"} {
		if _, err := system.Files.ResolveDir(ctx, p); !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("ResolveDir(%q) = %v, want ErrInvalid", p, err)
		}
		if _, err := system.Files.CreateShareDir(ctx, p+"/x"); !errors.Is(err, sys.ErrInvalid) && p != "relative" {
			t.Errorf("CreateShareDir(%q) = %v, want ErrInvalid", p+"/x", err)
		}
	}
}

func TestUnauthorizedCallerIsRefused(t *testing.T) {
	client, fake, _ := start(t, false)

	if err := client.Ping(ctx); err == nil {
		t.Error("Ping from a caller that is not allowed must fail")
	}
	if err := client.System().Accounts.CreateUser(ctx, "jay", "password-123"); err == nil {
		t.Fatal("CreateUser from a caller that is not allowed must fail")
	}
	if fake.Account("jay") != nil {
		t.Error("a refused caller changed the system")
	}
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func TestMalformedRequests(t *testing.T) {
	_, fake, url := start(t, true)

	// Extra fields are refused: a request carries exactly what the operation needs.
	status, _ := post(t, url+"/v1/accounts.create", `{"name":"jay","password":"password-123","shell":"/bin/bash"}`)
	if status != http.StatusBadRequest {
		t.Errorf("unknown field: status %d, want 400", status)
	}
	status, _ = post(t, url+"/v1/accounts.create", `not json`)
	if status != http.StatusBadRequest {
		t.Errorf("bad json: status %d, want 400", status)
	}
	if fake.Account("jay") != nil {
		t.Error("a malformed request changed the system")
	}

	// There is no operation outside the fixed list, and no generic "run".
	for _, op := range []string{"run", "exec", "accounts.create/../run", ""} {
		if status, _ := post(t, url+"/v1/"+op, `{}`); status != http.StatusNotFound {
			t.Errorf("operation %q: status %d, want 404", op, status)
		}
	}
	// Operations only answer to POST.
	if res, err := http.Get(url + "/v1/ping"); err != nil || res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET ping: %v, status %v", err, res.StatusCode)
	}
}

// The audit log records every call, and must never contain a password.
func TestAuditLogHasNoSecrets(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	client, fake, _ := start(t, true)
	system := client.System()
	system.Accounts.CreateUser(ctx, "jay", "TOP-SECRET-1")
	system.Accounts.SetPassword(ctx, "jay", "TOP-SECRET-2")
	fake.FailNext("SetPassword", errors.New("smbpasswd failed"))
	system.Accounts.SetPassword(ctx, "jay", "TOP-SECRET-3")
	system.Accounts.CreateUser(ctx, "-bad", "TOP-SECRET-4")

	out := logs.String()
	if strings.Contains(out, "TOP-SECRET") {
		t.Errorf("a password reached the log:\n%s", out)
	}
	for _, want := range []string{`"op":"accounts.create"`, `"account":"jay"`, `"peer_uid":1000`, `"op":"accounts.set-password"`} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log is missing %s:\n%s", want, out)
		}
	}
}

// The activity log's events stream from the agent to the web server, for as
// long as it stays connected; nobody else gets them.
func TestAuditEventStream(t *testing.T) {
	client, fake, _ := start(t, true)
	streamCtx, cancel := context.WithCancel(ctx)
	events, err := client.System().Audit.Events(streamCtx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50 && fake.AuditListeners() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	ip := netip.MustParseAddr("192.168.1.20")
	fake.EmitAudit(sambalog.Event{Kind: sambalog.KindFile, User: "jay", ClientIP: ip, Share: "media",
		Op: "renameat", OK: true, Path: "/srv/media/a", Path2: "/srv/media/b"})
	select {
	case ev := <-events:
		if ev.User != "jay" || ev.Op != "renameat" || ev.Path2 != "/srv/media/b" || ev.ClientIP != ip {
			t.Errorf("event over the stream: %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event over the stream")
	}

	// Hanging up ends the stream on both sides.
	cancel()
	for i := 0; i < 100 && fake.AuditListeners() != 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := fake.AuditListeners(); n != 0 {
		t.Errorf("the agent kept %d streams after the caller left", n)
	}

	// A caller the agent does not accept gets nothing.
	refused, _, _ := start(t, false)
	if _, err := refused.System().Audit.Events(ctx); err == nil {
		t.Error("a refused caller got the event stream")
	}
}

// Everything in a Samba config must survive the trip to the agent: a field
// the request drops would be silently ignored (the activity log's switch
// once was). The agent's result must be exactly what rendering locally gives.
func TestApplyConfigKeepsEveryField(t *testing.T) {
	client, fake, _ := start(t, true)
	if err := client.System().Accounts.CreateUser(ctx, "jay", "pass-word-1"); err != nil {
		t.Fatal(err)
	}
	cfg := sys.SambaConfig{
		Server: sys.ServerSettings{NetbiosName: "NAS", Workgroup: "HOME", MinProtocol: "SMB3_00", MaxProtocol: "SMB3_11",
			Encryption: "desired", Multichannel: "yes", MacSupport: true},
		Shares: []sys.ShareSpec{{Name: "media", Path: "/srv/media", Comment: "Films", Readers: []string{"jay"},
			Writers: []string{"jay"}, Guest: true, HideUnreadable: true, Allow: []string{"192.168.1.0/24"}, Block: []string{"192.168.1.66"}},
			{Name: "backup", Path: "/srv/backup", Readers: []string{"jay"}, Writers: []string{"jay"}, TimeMachine: true, TimeMachineMaxGiB: 300}},
		Audit:      true,
		LoginAudit: true,
		Banned:     []string{"192.168.1.66", "2001:db8:1:2::/64"},
	}
	if err := client.System().Samba.ApplyConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	local, err := sys.RenderConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fake.Config != local {
		t.Errorf("the agent applied a different config.\nagent:\n%s\nlocal:\n%s", fake.Config, local)
	}
}

// Step 3.5a: the discovery daemons through the agent.
func TestDiscoveryThroughAgent(t *testing.T) {
	client, _, _ := start(t, true)
	d := client.System().Discovery
	if err := d.SetDiscovery(ctx, sys.WSD, true); err != nil {
		t.Fatal(err)
	}
	st, err := d.DiscoveryStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Systemd || !st.WSD.Running || !st.WSD.Enabled || st.WSD.Unit != "wsdd.service" || st.MDNS.Running {
		t.Errorf("status: %+v", st)
	}
	if err := d.SetDiscovery(ctx, "sshd", true); !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("unknown service: %v", err)
	}
}

// The mount check runs in the agent (the web server's sandbox makes every
// folder look read-only to it). The agent only looks at folders that could
// be shared, keeps the caller's order and refuses oversized requests.
func TestInspectMountsThroughAgent(t *testing.T) {
	client, _, _ := start(t, true)
	files := client.System().Files

	report, err := files.InspectMounts(ctx, []string{"/srv/media", "/etc/shadow", "relative", "/missing/new", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Checks) != 4 {
		t.Fatalf("checks = %+v, want 4 (empty path skipped)", report.Checks)
	}
	want := []string{"/srv/media", "/etc/shadow", "relative", "/missing/new"}
	for i, c := range report.Checks {
		if c.Path != want[i] {
			t.Errorf("check %d path = %q, want %q", i, c.Path, want[i])
		}
		if c.Warnings == nil {
			t.Errorf("check %d warnings is nil, want an empty list", i)
		}
	}
	if c := report.Checks[0]; c.MountPoint != "/" || c.ReadOnly || len(c.Warnings) != 0 {
		t.Errorf("sharable folder: %+v", c)
	}
	for _, c := range report.Checks[1:3] {
		if c.MountPoint != "" || c.FSType != "" || len(c.Warnings) != 0 {
			t.Errorf("non-sharable path %q was inspected: %+v", c.Path, c)
		}
	}
	if c := report.Checks[3]; len(c.Warnings) != 1 || c.Warnings[0] != sys.MountWarnMissing {
		t.Errorf("missing folder: %+v", c)
	}

	tooMany := make([]string, sys.MaxMountPaths+1)
	for i := range tooMany {
		tooMany[i] = "/srv/x"
	}
	if _, err := files.InspectMounts(ctx, tooMany); !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("oversized request: %v, want ErrInvalid", err)
	}
}

func TestScanAndStatShareThroughAgent(t *testing.T) {
	client, fake, _ := start(t, true)
	fake.FilesDir = t.TempDir()
	system := client.System()
	if err := system.Accounts.CreateUser(ctx, "jay", "pw-123456"); err != nil {
		t.Fatal(err)
	}
	if err := system.Samba.ApplyConfig(ctx, sys.SambaConfig{Shares: []sys.ShareSpec{
		{Name: "media", Path: "/srv/media", Readers: []string{"jay"}},
		{Name: "closed", Path: "/srv/closed"}, // nobody may use it: not live
	}}); err != nil {
		t.Fatal(err)
	}
	dir := fake.ShareDir("media")
	_ = os.MkdirAll(filepath.Join(dir, "films", sys.RecycleDir), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "films", "a.mkv"), []byte("same"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.mkv"), []byte("same"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "films", sys.RecycleDir, "gone.mkv"), []byte("x"), 0o644)

	var got []string
	if err := system.Samba.ScanShare(ctx, "media", func(e sys.ShareEntry) error {
		got = append(got, e.Path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"b.mkv", "films", "films/a.mkv"}; !slices.Equal(got, want) {
		t.Errorf("scan = %v, want %v (recycle bin skipped)", got, want)
	}

	// The caller can stop a scan; its error comes back as is.
	stop := errors.New("stop")
	n := 0
	if err := system.Samba.ScanShare(ctx, "media", func(sys.ShareEntry) error { n++; return stop }); !errors.Is(err, stop) || n != 1 {
		t.Errorf("stopped scan: err=%v after %d entries", err, n)
	}
	for _, share := range []string{"closed", "nope"} {
		err := system.Samba.ScanShare(ctx, share, func(sys.ShareEntry) error { return nil })
		if !errors.Is(err, sys.ErrNotFound) {
			t.Errorf("scan of %q = %v, want ErrNotFound", share, err)
		}
	}
	if err := system.Samba.ScanShare(ctx, "../etc", func(sys.ShareEntry) error { return nil }); !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("scan of a bad name = %v, want ErrInvalid", err)
	}

	files, err := system.Samba.StatFiles(ctx, "media", []string{"b.mkv", "films/a.mkv", "films", "../x", "/etc/passwd", "films/.recycle/gone.mkv", "missing"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("stat = %+v, want b.mkv, films/a.mkv and films", files)
	}
	if files[0].SHA256 == "" || files[0].SHA256 != files[1].SHA256 || files[2].SHA256 != "" || !files[2].IsDir {
		t.Errorf("hashes: %+v", files)
	}
	if _, err := system.Samba.StatFiles(ctx, "media", make([]string, sys.MaxStatFiles+1), false); !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("oversized stat = %v, want ErrInvalid", err)
	}
}
