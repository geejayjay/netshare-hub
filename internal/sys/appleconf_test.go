package sys

import (
	"strings"
	"testing"
)

// Step 3.5a: Time Machine shares and Apple's SMB extensions.

func TestRenderTimeMachineShare(t *testing.T) {
	got, err := RenderConfig(SambaConfig{Shares: []ShareSpec{
		{Name: "backup", Path: "/srv/backup", Readers: []string{"jay"}, Writers: []string{"jay"}, TimeMachine: true, TimeMachineMaxGiB: 500},
		{Name: "docs", Path: "/srv/docs", Readers: []string{"jay"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// A Time Machine share turns Apple's extensions on, server-wide.
	for _, line := range []string{
		"[global]\n",
		"   fruit:metadata = stream\n",
		"   fruit:model = MacSamba\n",
		"   fruit:nfs_aces = no\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in:\n%s", line, got)
		}
	}
	backup, docs := section(got, "backup"), section(got, "docs")
	for _, line := range []string{
		"   vfs objects = catia fruit streams_xattr\n",
		"   fruit:time machine = yes\n",
		"   fruit:time machine max size = 500G\n",
	} {
		if !strings.Contains(backup, line) {
			t.Errorf("backup share: missing %q in:\n%s", line, backup)
		}
	}
	// Every share gets the extensions (a Mac decides on the first share it
	// opens), but only the backup share is a Time Machine target.
	if !strings.Contains(docs, "vfs objects = catia fruit streams_xattr") || strings.Contains(docs, "time machine") {
		t.Errorf("docs share:\n%s", docs)
	}

	// Read back as it was written.
	parsed := ParseShares(got)
	if len(parsed) != 2 || !parsed[0].TimeMachine || parsed[0].TimeMachineMaxGiB != 500 || parsed[1].TimeMachine {
		t.Errorf("parsed: %+v", parsed)
	}
}

func TestRenderMacSupportAndActivityLog(t *testing.T) {
	share := ShareSpec{Name: "docs", Path: "/srv/docs", Readers: []string{"jay"}}
	got, err := RenderConfig(SambaConfig{Server: ServerSettings{MacSupport: true}, Shares: []ShareSpec{share}, Audit: true})
	if err != nil {
		t.Fatal(err)
	}
	// full_audit comes last: it logs what reaches the disk.
	if !strings.Contains(section(got, "docs"), "   vfs objects = catia fruit streams_xattr full_audit\n") {
		t.Errorf("modules:\n%s", got)
	}
	cfg := SambaConfig{Server: ServerSettings{MacSupport: true}, Audit: true}
	if m := cfg.RequiredModules(); strings.Join(m, " ") != "catia fruit streams_xattr full_audit" {
		t.Errorf("required modules: %v", m)
	}

	// Neither: no modules at all, no fruit settings.
	plain, _ := RenderConfig(SambaConfig{Shares: []ShareSpec{share}})
	if strings.Contains(plain, "vfs objects") || strings.Contains(plain, "fruit") {
		t.Errorf("no Mac support asked for:\n%s", plain)
	}
	if m := (SambaConfig{Shares: []ShareSpec{share}}).RequiredModules(); len(m) != 0 {
		t.Errorf("required modules: %v", m)
	}
}

func TestTimeMachineShareRules(t *testing.T) {
	base := ShareSpec{Name: "backup", Path: "/srv/backup", Readers: []string{"jay"}, Writers: []string{"jay"}, TimeMachine: true}
	bad := map[string]func(s *ShareSpec){
		"guest":            func(s *ShareSpec) { s.Guest = true },
		"uploaders":        func(s *ShareSpec) { s.Readers, s.Writers, s.Uploaders = nil, nil, []string{"jay"} },
		"negative limit":   func(s *ShareSpec) { s.TimeMachineMaxGiB = -1 },
		"huge limit":       func(s *ShareSpec) { s.TimeMachineMaxGiB = MaxTimeMachineGiB + 1 },
		"limit without tm": func(s *ShareSpec) { s.TimeMachine, s.TimeMachineMaxGiB = false, 10 },
	}
	if err := base.Check(); err != nil {
		t.Fatal(err)
	}
	for name, change := range bad {
		s := base
		change(&s)
		if err := s.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := RenderConfig(SambaConfig{Shares: []ShareSpec{s}}); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}
