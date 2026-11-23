package sysexec

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"netshare-hub/internal/sys"
)

// discoveryUnits are the systemd units each discovery service may use, in
// order of preference. These names are the whole list: nothing from a
// request ever becomes a unit name.
var discoveryUnits = map[sys.DiscoveryService][]string{
	sys.WSD:  {"wsdd.service", "wsdd2.service"},
	sys.MDNS: {"avahi-daemon.service"},
}

// companionUnits are switched on and off with a unit. avahi-daemon is also
// started by its socket, so stopping only the service would not last.
var companionUnits = map[string][]string{
	"avahi-daemon.service": {"avahi-daemon.socket"},
}

// avahiUnit is restarted (only if running) when Time Machine shares change:
// Samba registers its Time Machine record when Avahi (re)connects, and does
// not update it on a config reload. Restarting Avahi does not disconnect
// any SMB client; restarting Samba would.
const avahiUnit = "avahi-daemon.service"

// Discovery controls wsdd and avahi-daemon through systemctl (sys.Discovery).
type Discovery struct {
	runner Runner
}

func NewDiscovery(r Runner) *Discovery { return &Discovery{runner: r} }

type unitState struct {
	loaded, active, enabled bool
}

// state asks systemd about one unit. systemctl show succeeds for a unit
// that does not exist (LoadState=not-found).
func (d *Discovery) state(ctx context.Context, unit string) (unitState, error) {
	res, err := run(ctx, d.runner, "systemctl", "show", "--property=LoadState,ActiveState,UnitFileState", "--", unit)
	if err != nil {
		return unitState{}, err
	}
	var st unitState
	for _, line := range strings.Split(res.Stdout, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "LoadState":
			st.loaded = value == "loaded"
		case "ActiveState":
			st.active = value == "active" || value == "reloading"
		case "UnitFileState":
			st.enabled = value == "enabled" || value == "enabled-runtime"
		}
	}
	return st, nil
}

// service finds the unit in use for a service: the first installed one
// that runs, else the first installed one.
func (d *Discovery) service(ctx context.Context, svc sys.DiscoveryService) (sys.ServiceState, error) {
	var found sys.ServiceState
	for _, unit := range discoveryUnits[svc] {
		st, err := d.state(ctx, unit)
		if err != nil {
			return sys.ServiceState{}, err
		}
		if !st.loaded {
			continue
		}
		s := sys.ServiceState{Installed: true, Unit: unit, Running: st.active, Enabled: st.enabled}
		if s.Running {
			return s, nil
		}
		if !found.Installed {
			found = s
		}
	}
	return found, nil
}

// systemd reports whether systemctl works here (it does not in a container
// without systemd, or on a machine run by another init system).
func (d *Discovery) systemd(ctx context.Context) bool {
	st, err := d.state(ctx, "init.scope")
	if err != nil {
		slog.DebugContext(ctx, "systemd is not available", "err", err)
		return false
	}
	return st.loaded
}

func (d *Discovery) DiscoveryStatus(ctx context.Context) (sys.DiscoveryStatus, error) {
	if !d.systemd(ctx) {
		return sys.DiscoveryStatus{}, nil
	}
	status := sys.DiscoveryStatus{Systemd: true}
	var err error
	if status.WSD, err = d.service(ctx, sys.WSD); err != nil {
		return sys.DiscoveryStatus{}, err
	}
	if status.MDNS, err = d.service(ctx, sys.MDNS); err != nil {
		return sys.DiscoveryStatus{}, err
	}
	return status, nil
}

func (d *Discovery) SetDiscovery(ctx context.Context, svc sys.DiscoveryService, on bool) error {
	if _, ok := discoveryUnits[svc]; !ok {
		return fmt.Errorf("%w: discovery service %q", sys.ErrInvalid, svc)
	}
	if !d.systemd(ctx) {
		return fmt.Errorf("%w: systemd is not available, so %s cannot be controlled", sys.ErrNotFound, svc)
	}
	st, err := d.service(ctx, svc)
	if err != nil {
		return err
	}
	if !st.Installed {
		return fmt.Errorf("%w: no %s daemon is installed (%s)", sys.ErrNotFound, svc, strings.Join(discoveryUnits[svc], " or "))
	}
	units := []string{st.Unit}
	for _, c := range companionUnits[st.Unit] {
		if cs, err := d.state(ctx, c); err == nil && cs.loaded {
			units = append(units, c)
		}
	}
	verb := "disable"
	if on {
		verb = "enable"
	}
	_, err = run(ctx, d.runner, "systemctl", append([]string{verb, "--now", "--"}, units...)...)
	return err
}

// refreshMDNS makes Samba register its mDNS records again (see avahiUnit).
// try-restart does nothing when Avahi is not running.
func refreshMDNS(ctx context.Context, r Runner) {
	if _, err := run(ctx, r, "systemctl", "try-restart", "--", avahiUnit); err != nil {
		slog.WarnContext(ctx, "could not refresh the Time Machine announcement (restart avahi-daemon to do it)", "err", err)
	}
}
