// Package discovery switches network discovery on and off: WS-Discovery
// (the server shows up in Windows' Network folder) and mDNS / Bonjour (in
// macOS Finder and Linux file managers, and how Macs find Time Machine
// shares). The daemons' state lives in systemd, not in the database, so
// what the API reports is what really runs.
package discovery

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/sys"
)

// CodeUnavailable means a daemon cannot be switched: it is not installed,
// or the machine has no systemd.
const CodeUnavailable errs.Code = "DISCOVERY_UNAVAILABLE"

// DiscoveryService is one discovery service's state.
type DiscoveryService struct {
	Installed bool   `json:"installed" doc:"A package provides the daemon (wsdd or wsdd2; avahi-daemon)."`
	Unit      string `json:"unit,omitempty" doc:"The systemd unit in use."`
	Running   bool   `json:"running"`
	Enabled   bool   `json:"enabled" doc:"Starts when the machine starts."`
	On        bool   `json:"on" doc:"Running and enabled: what the toggle shows."`
	Hint      string `json:"hint,omitempty" doc:"What to do when the service cannot be switched on."`
}

// DiscoveryStatus is both services' state.
type DiscoveryStatus struct {
	Systemd bool             `json:"systemd" doc:"False when the machine is not run by systemd: the daemons cannot be switched from here."`
	WSD     DiscoveryService `json:"wsd" doc:"WS-Discovery: the server shows up in Windows' Network folder, under the machine's hostname."`
	MDNS    DiscoveryService `json:"mdns" doc:"mDNS / Bonjour: the server shows up in macOS Finder and Linux file managers, and Macs find Time Machine shares."`
}

type statusOutput struct {
	Body DiscoveryStatus
}

// DiscoveryUpdate switches the services that are present.
type DiscoveryUpdate struct {
	WSD  *bool `json:"wsd,omitempty"`
	MDNS *bool `json:"mdns,omitempty"`
}

type updateInput struct {
	Body DiscoveryUpdate
}

func fromSys(st sys.DiscoveryStatus) DiscoveryStatus {
	svc := func(s sys.ServiceState, pkg string) DiscoveryService {
		out := DiscoveryService{Installed: s.Installed, Unit: s.Unit, Running: s.Running, Enabled: s.Enabled, On: s.Running && s.Enabled}
		switch {
		case !st.Systemd:
			out.Hint = "This machine is not run by systemd, so NetShare Hub cannot start or stop the daemon. Start " + pkg + " by hand."
		case !s.Installed:
			out.Hint = "Not installed. On Debian and Ubuntu: apt install " + pkg
		}
		return out
	}
	return DiscoveryStatus{Systemd: st.Systemd, WSD: svc(st.WSD, "wsdd"), MDNS: svc(st.MDNS, "avahi-daemon")}
}

// Register adds the routes.
func Register(api huma.API, d sys.Discovery) {
	const tag = "Settings"
	httpx.Register(api, httpx.Route{ID: "getDiscovery", Method: http.MethodGet, Path: "/settings/discovery",
		Summary: "Get network discovery (Windows Network, macOS Finder)", Tag: tag, Access: httpx.Require(rbac.SettingsRead)},
		func(ctx context.Context, _ *struct{}) (*statusOutput, error) {
			st, err := d.DiscoveryStatus(ctx)
			if err != nil {
				return nil, err
			}
			return &statusOutput{Body: fromSys(st)}, nil
		})
	httpx.Register(api, httpx.Route{ID: "updateDiscovery", Method: http.MethodPut, Path: "/settings/discovery",
		Summary: "Switch network discovery on or off", Tag: tag, Access: httpx.Require(rbac.SettingsWrite)},
		func(ctx context.Context, in *updateInput) (*statusOutput, error) {
			for _, c := range []struct {
				svc sys.DiscoveryService
				on  *bool
			}{{sys.WSD, in.Body.WSD}, {sys.MDNS, in.Body.MDNS}} {
				if c.on == nil {
					continue
				}
				err := d.SetDiscovery(ctx, c.svc, *c.on)
				if errors.Is(err, sys.ErrNotFound) {
					st, _ := d.DiscoveryStatus(ctx)
					hint := fromSys(st).WSD.Hint
					if c.svc == sys.MDNS {
						hint = fromSys(st).MDNS.Hint
					}
					if hint == "" {
						hint = "This service cannot be switched from NetShare Hub on this machine."
					}
					return nil, errs.Conflict(CodeUnavailable, hint).WithCause(err)
				}
				if err != nil {
					return nil, err
				}
				slog.InfoContext(ctx, "network discovery switched", "service", string(c.svc), "on", *c.on,
					"by", httpx.PrincipalFrom(ctx).Username)
			}
			st, err := d.DiscoveryStatus(ctx)
			if err != nil {
				return nil, err
			}
			return &statusOutput{Body: fromSys(st)}, nil
		})
}
