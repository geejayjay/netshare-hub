package sysexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"netshare-hub/internal/sys"
)

// scriptRunner records every command and answers from a script, so the tests
// can check the exact argument lists without touching the machine.
type scriptRunner struct {
	calls []Command
	// answer decides the result of a command. nil result = exit 0.
	answer func(cmd Command) Result
}

func (s *scriptRunner) Run(_ context.Context, cmd Command) (Result, error) {
	s.calls = append(s.calls, cmd)
	if s.answer == nil {
		return Result{}, nil
	}
	return s.answer(cmd), nil
}

// lines returns the recorded commands as "name arg arg" strings.
func (s *scriptRunner) lines() []string {
	out := make([]string, len(s.calls))
	for i, c := range s.calls {
		out[i] = c.Name + " " + strings.Join(c.Args, " ")
	}
	return out
}

func (s *scriptRunner) ran(prefix string) bool {
	for _, l := range s.lines() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// machine simulates a small Linux box: which accounts exist and their groups.
type machine struct {
	passwd   map[string]int      // name -> uid
	groups   map[string][]string // name -> groups
	group    bool                // does the managed group exist
	fail     map[string]int      // program -> exit code to return
	disabled map[string]bool     // accounts whose Samba entry is disabled
}

func (m *machine) answer(cmd Command) Result {
	if code, ok := m.fail[cmd.Name]; ok {
		return Result{ExitCode: code, Stderr: cmd.Name + " failed"}
	}
	switch cmd.Name {
	case "getent":
		if cmd.Args[0] == "group" {
			if !m.group {
				return Result{ExitCode: 2}
			}
			return Result{Stdout: sys.ManagedGroup + ":x:1500:\n"}
		}
		uid, ok := m.passwd[cmd.Args[1]]
		if !ok {
			return Result{ExitCode: 2}
		}
		return Result{Stdout: cmd.Args[1] + ":x:" + itoa(uid) + ":100::/nonexistent:/usr/sbin/nologin\n"}
	case "id":
		return Result{Stdout: strings.Join(m.groups[cmd.Args[1]], " ") + "\n"}
	case "groupadd":
		m.group = true
	case "pdbedit":
		name := cmd.Args[1]
		if _, ok := m.passwd[name]; !ok {
			return Result{ExitCode: 255}
		}
		flags := "[U          ]"
		if m.disabled[name] {
			flags = "[DU         ]"
		}
		return Result{Stdout: name + ":1000:XXX:" + "0123456789ABCDEF0123456789ABCDEF:" + flags + ":LCT-00000000:\n"}
	}
	return Result{}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func newAccounts(m *machine) (*Accounts, *scriptRunner) {
	r := &scriptRunner{answer: m.answer}
	return NewAccounts(r), r
}

var ctx = context.Background()

func TestCreateUser(t *testing.T) {
	m := &machine{passwd: map[string]int{}}
	a, r := newAccounts(m)

	if err := a.CreateUser(ctx, "jay", "s3cret pass"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"getent passwd jay",
		"getent group " + sys.ManagedGroup,
		"groupadd " + sys.ManagedGroup,
		"useradd --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin --groups " + sys.ManagedGroup + " --comment NetShare Hub user jay",
		"smbpasswd -s -a jay",
		"smbpasswd -e jay",
	}
	got := r.lines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The password must reach smbpasswd over stdin and nowhere else.
	for _, c := range r.calls {
		if strings.Contains(strings.Join(c.Args, " "), "s3cret") {
			t.Errorf("password leaked into arguments of %s", c.Name)
		}
		if c.Name == "smbpasswd" && c.Args[0] == "-s" && c.Stdin != "s3cret pass\ns3cret pass\n" {
			t.Errorf("smbpasswd stdin = %q", c.Stdin)
		}
	}
}

func TestCreateUserRefusesBadInput(t *testing.T) {
	cases := []struct{ name, password string }{
		{"-rf", "password123"},          // would be read as an option
		{"jay;rm -rf /", "password123"}, // shell metacharacters
		{"Jay", "password123"},          // uppercase
		{"jay", "line1\nline2"},         // would end smbpasswd's input early
		{"jay", ""},                     // empty
		{"a", "password123"},            // too short
		{strings.Repeat("a", 33), "password123"},
	}
	for _, c := range cases {
		a, r := newAccounts(&machine{})
		err := a.CreateUser(ctx, c.name, c.password)
		if !errors.Is(err, sys.ErrInvalid) {
			t.Errorf("CreateUser(%q, %q) = %v, want ErrInvalid", c.name, c.password, err)
		}
		if len(r.calls) != 0 {
			t.Errorf("CreateUser(%q): ran %v before validating", c.name, r.lines())
		}
	}
}

func TestCreateUserNeverTakesOverSystemAccount(t *testing.T) {
	m := &machine{
		passwd: map[string]int{"backup": 34, "alice": 1001},
		groups: map[string][]string{"backup": {"backup"}, "alice": {"alice", "sudo"}},
	}
	for _, name := range []string{"backup", "alice"} {
		a, r := newAccounts(m)
		if err := a.CreateUser(ctx, name, "password123"); !errors.Is(err, sys.ErrNameTaken) {
			t.Errorf("CreateUser(%q) = %v, want ErrNameTaken", name, err)
		}
		if r.ran("useradd") || r.ran("smbpasswd") {
			t.Errorf("CreateUser(%q) touched an unmanaged account: %v", name, r.lines())
		}
	}
}

func TestCreateUserRejectsLowUIDEvenInManagedGroup(t *testing.T) {
	// Someone added a system account to our group by hand. It is still off limits.
	m := &machine{
		passwd: map[string]int{"daemon": 1},
		groups: map[string][]string{"daemon": {"daemon", sys.ManagedGroup}},
	}
	a, _ := newAccounts(m)
	if err := a.CreateUser(ctx, "daemon", "password123"); !errors.Is(err, sys.ErrNameTaken) {
		t.Errorf("got %v, want ErrNameTaken", err)
	}
}

func TestCreateUserRollsBackWhenSambaFails(t *testing.T) {
	m := &machine{passwd: map[string]int{}, group: true, fail: map[string]int{"smbpasswd": 1}}
	a, r := newAccounts(m)

	if err := a.CreateUser(ctx, "jay", "password123"); err == nil {
		t.Fatal("expected an error")
	}
	if !r.ran("userdel jay") {
		t.Errorf("half-created account was not removed: %v", r.lines())
	}
}

func TestCreateUserAdoptsLeftoverManagedAccount(t *testing.T) {
	m := &machine{
		passwd: map[string]int{"jay": 1002},
		groups: map[string][]string{"jay": {"jay", sys.ManagedGroup}},
		group:  true,
	}
	a, r := newAccounts(m)
	if err := a.CreateUser(ctx, "jay", "password123"); err != nil {
		t.Fatal(err)
	}
	if r.ran("useradd") {
		t.Error("must not re-create an existing account")
	}
	if !r.ran("smbpasswd -s -a jay") {
		t.Error("adopted account must get the new password")
	}
}

func TestChangesOnlyTouchManagedAccounts(t *testing.T) {
	m := &machine{
		passwd: map[string]int{"root": 0, "jay": 1002},
		groups: map[string][]string{"root": {"root"}, "jay": {"jay", sys.ManagedGroup}},
	}
	a, r := newAccounts(m)

	if err := a.SetPassword(ctx, "root", "password123"); !errors.Is(err, sys.ErrNotManaged) {
		t.Errorf("SetPassword(root) = %v, want ErrNotManaged", err)
	}
	if err := a.SetEnabled(ctx, "root", false); !errors.Is(err, sys.ErrNotManaged) {
		t.Errorf("SetEnabled(root) = %v, want ErrNotManaged", err)
	}
	if err := a.DeleteUser(ctx, "root"); !errors.Is(err, sys.ErrNotManaged) {
		t.Errorf("DeleteUser(root) = %v, want ErrNotManaged", err)
	}
	if r.ran("smbpasswd") || r.ran("userdel") {
		t.Fatalf("an unmanaged account was touched: %v", r.lines())
	}

	if err := a.SetPassword(ctx, "ghost", "password123"); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("SetPassword(ghost) = %v, want ErrNotFound", err)
	}
	if err := a.DeleteUser(ctx, "ghost"); err != nil {
		t.Errorf("DeleteUser of a missing account should succeed, got %v", err)
	}

	if err := a.SetEnabled(ctx, "jay", false); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteUser(ctx, "jay"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"smbpasswd -d jay", "smbpasswd -x jay", "userdel jay"} {
		if !r.ran(want) {
			t.Errorf("missing command %q in %v", want, r.lines())
		}
	}
}

// --- Samba config ---

func newSamba(t *testing.T, answer func(Command) Result) (*Samba, *scriptRunner, string, string) {
	t.Helper()
	dir := t.TempDir()
	managed := filepath.Join(dir, "netshare-hub.conf")
	main := filepath.Join(dir, "smb.conf")
	if err := os.WriteFile(main, []byte("[global]\n   workgroup = HOME\n\n[old]\n   path = /srv/old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &scriptRunner{answer: answer}
	return NewSamba(r, managed, main), r, managed, main
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyConfig(t *testing.T) {
	s, r, managed, main := newSamba(t, nil)

	if err := s.applyConfig(ctx, "[media]\n   path = /srv/media\n"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, managed); got != "[media]\n   path = /srv/media\n" {
		t.Errorf("managed config = %q", got)
	}

	// smb.conf keeps the admin's content, gains exactly one include, and is backed up.
	mainNow := read(t, main)
	if !strings.HasPrefix(mainNow, "[global]\n   workgroup = HOME\n\n[old]\n   path = /srv/old\n") {
		t.Errorf("admin's smb.conf content was changed:\n%s", mainNow)
	}
	if strings.Count(mainNow, "include = "+managed) != 1 {
		t.Errorf("want exactly one include line:\n%s", mainNow)
	}
	if !strings.Contains(read(t, main+".netshare-hub.bak"), "workgroup = HOME") {
		t.Error("smb.conf was not backed up")
	}
	if !r.ran("smbcontrol all reload-config") {
		t.Error("samba was not reloaded")
	}

	// A second apply keeps a backup of the previous version and adds no second include.
	if err := s.applyConfig(ctx, "[docs]\n   path = /srv/docs\n"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, managed+".bak"); !strings.Contains(got, "[media]") {
		t.Errorf("previous config not kept, .bak = %q", got)
	}
	if strings.Count(read(t, main), "include = ") != 1 {
		t.Error("include line was added twice")
	}
}

func TestApplyConfigRejectedChangesNothing(t *testing.T) {
	for name, bad := range map[string]Result{
		"exit code":         {ExitCode: 1, Stderr: "Error loading services."},
		"unknown parameter": {ExitCode: 0, Stderr: `Unknown parameter encountered: "raed only"`},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, managed, main := newSamba(t, func(c Command) Result {
				if c.Name == "testparm" {
					return bad
				}
				return Result{}
			})
			before := read(t, main)

			err := s.applyConfig(ctx, "[x]\n   raed only = yes\n")
			var cfgErr *sys.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("got %v, want *sys.ConfigError", err)
			}
			if _, statErr := os.Stat(managed); !os.IsNotExist(statErr) {
				t.Error("a rejected config was written")
			}
			if read(t, main) != before {
				t.Error("smb.conf was changed by a rejected config")
			}
			if r.ran("smbcontrol") {
				t.Error("samba was reloaded for a rejected config")
			}
			if left, _ := filepath.Glob(filepath.Join(filepath.Dir(managed), ".netshare-hub-*")); len(left) != 0 {
				t.Errorf("candidate file left behind: %v", left)
			}
		})
	}
}

func TestApplyConfigRestoresSmbConfIfIncludeBreaksIt(t *testing.T) {
	// The candidate is fine, but smb.conf with the include is rejected.
	s, _, _, main := newSamba(t, func(c Command) Result {
		if c.Name == "testparm" && strings.HasSuffix(c.Args[1], "smb.conf") {
			return Result{ExitCode: 1, Stderr: "Error loading services."}
		}
		return Result{}
	})
	before := read(t, main)

	if err := s.applyConfig(ctx, "[media]\n   path = /srv/media\n"); err == nil {
		t.Fatal("expected an error")
	}
	if read(t, main) != before {
		t.Errorf("smb.conf was not restored:\n%s", read(t, main))
	}
}

func TestApplyConfigReloadFailure(t *testing.T) {
	s, _, managed, _ := newSamba(t, func(c Command) Result {
		if c.Name == "smbcontrol" {
			return Result{ExitCode: 1, Stderr: "Can't send to all"}
		}
		return Result{}
	})
	err := s.applyConfig(ctx, "[media]\n   path = /srv/media\n")
	if !errors.Is(err, sys.ErrReloadFailed) {
		t.Fatalf("got %v, want ErrReloadFailed", err)
	}
	// The config itself is valid and stays in place.
	if !strings.Contains(read(t, managed), "[media]") {
		t.Error("valid config should stay written when only the reload fails")
	}
}

func TestRunnerRefusesUnknownPrograms(t *testing.T) {
	if _, err := (ExecRunner{}).Run(ctx, Command{Name: "sh", Args: []string{"-c", "id"}}); err == nil {
		t.Fatal("a program outside the allow-list must be refused")
	}
}

func TestDisabledAccountDetection(t *testing.T) {
	m := &machine{passwd: map[string]int{"jay": 1002, "ana": 1003}, disabled: map[string]bool{"ana": true}}
	a, _ := newAccounts(m)
	for name, want := range map[string]bool{"jay": false, "ana": true, "ghost": true} {
		got, err := a.disabled(ctx, name)
		if err != nil || got != want {
			t.Errorf("disabled(%s) = %v, %v; want %v", name, got, err, want)
		}
	}
}
