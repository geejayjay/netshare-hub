package sysexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"netshare-hub/internal/sys"
)

// units is a pretend systemd: unit -> "LoadState ActiveState UnitFileState".
func systemdAnswer(units map[string]string) func(Command) Result {
	return func(cmd Command) Result {
		if cmd.Name != "systemctl" || len(cmd.Args) == 0 || cmd.Args[0] != "show" {
			return Result{}
		}
		unit := cmd.Args[len(cmd.Args)-1]
		st, ok := units[unit]
		if !ok {
			st = "not-found inactive -"
		}
		f := strings.Fields(st)
		if f[2] == "-" {
			f[2] = ""
		}
		return Result{Stdout: "LoadState=" + f[0] + "\nActiveState=" + f[1] + "\nUnitFileState=" + f[2] + "\n"}
	}
}

func TestDiscoveryStatus(t *testing.T) {
	r := &scriptRunner{answer: systemdAnswer(map[string]string{
		"init.scope":           "loaded active -",
		"wsdd2.service":        "loaded active enabled",
		"avahi-daemon.service": "loaded inactive disabled",
		"avahi-daemon.socket":  "loaded inactive disabled",
	})}
	st, err := NewDiscovery(r).DiscoveryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := sys.DiscoveryStatus{Systemd: true,
		WSD:  sys.ServiceState{Installed: true, Unit: "wsdd2.service", Running: true, Enabled: true},
		MDNS: sys.ServiceState{Installed: true, Unit: "avahi-daemon.service"}}
	if st != want {
		t.Errorf("status %+v\nwant %+v", st, want)
	}
	// Only fixed unit names, after "--".
	for _, l := range r.lines() {
		if !strings.HasPrefix(l, "systemctl show --property=LoadState,ActiveState,UnitFileState -- ") {
			t.Errorf("unexpected command %q", l)
		}
	}
}

func TestDiscoverySwitch(t *testing.T) {
	r := &scriptRunner{answer: systemdAnswer(map[string]string{
		"init.scope":           "loaded active -",
		"wsdd.service":         "loaded inactive disabled",
		"avahi-daemon.service": "loaded active enabled",
		"avahi-daemon.socket":  "loaded active enabled",
	})}
	d := NewDiscovery(r)
	ctx := context.Background()
	if err := d.SetDiscovery(ctx, sys.WSD, true); err != nil {
		t.Fatal(err)
	}
	if !r.ran("systemctl enable --now -- wsdd.service") {
		t.Errorf("commands: %q", r.lines())
	}
	// Avahi goes off with its socket, or the socket would start it again.
	if err := d.SetDiscovery(ctx, sys.MDNS, false); err != nil {
		t.Fatal(err)
	}
	if !r.ran("systemctl disable --now -- avahi-daemon.service avahi-daemon.socket") {
		t.Errorf("commands: %q", r.lines())
	}
	// Anything else is refused before systemctl runs.
	n := len(r.calls)
	if err := d.SetDiscovery(ctx, "sshd", true); !errors.Is(err, sys.ErrInvalid) {
		t.Errorf("unknown service: %v", err)
	}
	if len(r.calls) != n {
		t.Errorf("ran %q for an unknown service", r.lines()[n:])
	}
}

func TestDiscoveryNotInstalled(t *testing.T) {
	ctx := context.Background()
	// No daemon installed.
	r := &scriptRunner{answer: systemdAnswer(map[string]string{"init.scope": "loaded active -"})}
	if err := NewDiscovery(r).SetDiscovery(ctx, sys.WSD, true); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("not installed: %v", err)
	}
	if r.ran("systemctl enable") {
		t.Error("enabled a unit that does not exist")
	}
	// No systemd at all (systemctl fails).
	r = &scriptRunner{answer: func(Command) Result { return Result{ExitCode: 1, Stderr: "System has not been booted with systemd"} }}
	st, err := NewDiscovery(r).DiscoveryStatus(ctx)
	if err != nil || st.Systemd {
		t.Errorf("no systemd: %+v %v", st, err)
	}
	if err := NewDiscovery(r).SetDiscovery(ctx, sys.MDNS, true); !errors.Is(err, sys.ErrNotFound) {
		t.Errorf("no systemd: %v", err)
	}
}

func TestTimeMachineChanged(t *testing.T) {
	tm := func(name string) sys.ShareSpec {
		return sys.ShareSpec{Name: name, Path: "/srv/" + name, Readers: []string{"jay"}, Writers: []string{"jay"}, TimeMachine: true}
	}
	plain := sys.ShareSpec{Name: "docs", Path: "/srv/docs", Readers: []string{"jay"}}
	before := map[string]sys.ShareSpec{"a": tm("a"), "docs": plain}
	cases := []struct {
		name   string
		shares []sys.ShareSpec
		want   bool
	}{
		{"same", []sys.ShareSpec{tm("a"), plain}, false},
		{"other share changed", []sys.ShareSpec{tm("a")}, false},
		{"one added", []sys.ShareSpec{tm("a"), tm("b"), plain}, true},
		{"one removed", []sys.ShareSpec{plain}, true},
		{"renamed", []sys.ShareSpec{tm("b"), plain}, true},
		{"nobody may use it now", []sys.ShareSpec{{Name: "a", Path: "/srv/a", TimeMachine: true}, plain}, true},
	}
	for _, c := range cases {
		if got := timeMachineChanged(before, c.shares); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
