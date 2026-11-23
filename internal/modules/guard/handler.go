package guard

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

type settingsIO struct {
	Body Settings
}

type incidentsInput struct {
	page.Query
}

type incidentsOutput struct {
	Body page.List[Incident]
}

type idInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type incidentOutput struct {
	Body Incident
}

var incidentListOptions = page.Options{DefaultSize: 50, MaxSize: 200}

type banSettingsIO struct {
	Body BanSettings
}

type bansInput struct {
	page.Query
	Active bool `query:"active" doc:"Only bans in force now."`
}

type bansOutput struct {
	Body page.List[Ban]
}

type banOutput struct {
	Body Ban
}

// RegisterBans adds the brute-force protection routes.
func RegisterBans(api huma.API, bans *Bans) {
	const tag = "Security"
	httpx.Register(api, httpx.Route{ID: "getSMBBanSettings", Method: http.MethodGet, Path: "/guard/bans/settings",
		Summary: "Get the SMB brute-force protection settings", Tag: tag, Access: httpx.Require(rbac.SystemRead)},
		func(ctx context.Context, _ *struct{}) (*banSettingsIO, error) {
			st, err := bans.Settings(ctx)
			if err != nil {
				return nil, err
			}
			return &banSettingsIO{Body: st}, nil
		})
	httpx.Register(api, httpx.Route{ID: "updateSMBBanSettings", Method: http.MethodPut, Path: "/guard/bans/settings",
		Summary: "Change the SMB brute-force protection settings (bans of newly allowed addresses are lifted)", Tag: tag,
		Access: httpx.Require(rbac.SettingsWrite)},
		func(ctx context.Context, in *banSettingsIO) (*banSettingsIO, error) {
			st, err := bans.UpdateSettings(ctx, httpx.PrincipalFrom(ctx), in.Body)
			if err != nil {
				return nil, err
			}
			return &banSettingsIO{Body: st}, nil
		})
	httpx.Register(api, httpx.Route{ID: "listSMBBans", Method: http.MethodGet, Path: "/guard/bans",
		Summary: "List banned addresses, newest first", Tag: tag, Access: httpx.Require(rbac.SystemRead)},
		func(ctx context.Context, in *bansInput) (*bansOutput, error) {
			req, err := in.Query.Resolve(incidentListOptions)
			if err != nil {
				return nil, err
			}
			items, total, err := bans.List(ctx, in.Active, req)
			if err != nil {
				return nil, err
			}
			return &bansOutput{Body: page.Paged(items, total, req)}, nil
		})
	httpx.Register(api, httpx.Route{ID: "liftSMBBan", Method: http.MethodPost, Path: "/guard/bans/{id}/lift",
		Summary: "End a ban now", Tag: tag, Access: httpx.Require(rbac.AlertsManage)},
		func(ctx context.Context, in *idInput) (*banOutput, error) {
			ban, err := bans.Lift(ctx, httpx.PrincipalFrom(ctx), in.ID)
			if err != nil {
				return nil, err
			}
			return &banOutput{Body: ban}, nil
		})
}

// Register adds the guard's routes.
func Register(api huma.API, svc *Service) {
	const tag = "Security"
	httpx.Register(api, httpx.Route{ID: "getGuardSettings", Method: http.MethodGet, Path: "/guard/settings",
		Summary: "Get the ransomware guard's settings", Tag: tag, Access: httpx.Require(rbac.SystemRead)},
		func(ctx context.Context, _ *struct{}) (*settingsIO, error) {
			st, err := svc.Settings(ctx)
			if err != nil {
				return nil, err
			}
			return &settingsIO{Body: st}, nil
		})
	httpx.Register(api, httpx.Route{ID: "updateGuardSettings", Method: http.MethodPut, Path: "/guard/settings",
		Summary: "Change the ransomware guard's mode and thresholds", Tag: tag, Access: httpx.Require(rbac.SettingsWrite)},
		func(ctx context.Context, in *settingsIO) (*settingsIO, error) {
			st, err := svc.UpdateSettings(ctx, httpx.PrincipalFrom(ctx), in.Body)
			if err != nil {
				return nil, err
			}
			return &settingsIO{Body: st}, nil
		})
	httpx.Register(api, httpx.Route{ID: "listGuardIncidents", Method: http.MethodGet, Path: "/guard/incidents",
		Summary: "List what the ransomware guard found, newest first", Tag: tag, Access: httpx.Require(rbac.SystemRead)},
		func(ctx context.Context, in *incidentsInput) (*incidentsOutput, error) {
			req, err := in.Query.Resolve(incidentListOptions)
			if err != nil {
				return nil, err
			}
			items, total, err := svc.Incidents(ctx, req)
			if err != nil {
				return nil, err
			}
			return &incidentsOutput{Body: page.Paged(items, total, req)}, nil
		})
	httpx.Register(api, httpx.Route{ID: "restoreGuardIncident", Method: http.MethodPost, Path: "/guard/incidents/{id}/restore",
		Summary: "Restore access for the user of an incident (they may write again)", Tag: tag,
		Access: httpx.Require(rbac.AlertsManage)},
		func(ctx context.Context, in *idInput) (*incidentOutput, error) {
			inc, err := svc.Restore(ctx, httpx.PrincipalFrom(ctx), in.ID)
			if err != nil {
				return nil, err
			}
			return &incidentOutput{Body: inc}, nil
		})
}
