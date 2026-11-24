package audit

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

type listInput struct {
	Actor    string `query:"actor" maxLength:"64"`
	Action   string `query:"action" maxLength:"64"`
	Q        string `query:"q" maxLength:"128"`
	Page     int    `query:"page" minimum:"1" default:"1"`
	PageSize int    `query:"pageSize" minimum:"1" maximum:"200" default:"50"`
}

type listOutput struct {
	Body page.List[AuditEntry]
}

// Register exposes GET /audit for reading the append-only admin audit log.
func Register(api huma.API, svc *Service) {
	httpx.Register(api, httpx.Route{
		ID: "listAuditLog", Method: http.MethodGet, Path: "/audit",
		Summary: "List append-only administrative audit log entries",
		Tag:     "System", Access: httpx.Require(rbac.AuditRead),
	}, func(ctx context.Context, in *listInput) (*listOutput, error) {
		res, err := svc.List(ctx, Filter{
			Actor:    in.Actor,
			Action:   in.Action,
			Q:        in.Q,
			Page:     in.Page,
			PageSize: in.PageSize,
		})
		if err != nil {
			return nil, err
		}
		return &listOutput{Body: res}, nil
	})
}
