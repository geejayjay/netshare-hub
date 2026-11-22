package alerts

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

var listOptions = page.Options{
	DefaultSize: 50,
	MaxSize:     200,
	DefaultSort: "-lastAt",
	SortFields:  map[string]string{"lastAt": "last_at", "firstAt": "first_at", "severity": "severity"},
}

type listInput struct {
	page.Query
	Active   bool   `query:"active" doc:"Only alerts that are neither resolved nor acknowledged."`
	Severity string `query:"severity" enum:"info,warning,error,critical"`
	Source   string `query:"source" enum:"shares,uploads,security,storage,replication,system"`
}

type idInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type listOutput struct {
	Body page.List[Alert]
}

type alertOutput struct {
	Body Alert
}

type handler struct{ svc *Service }

// Register adds this module's routes. Live updates arrive as "alert"
// events on /api/v1/events.
func Register(api huma.API, svc *Service) {
	h := &handler{svc: svc}
	const tag = "Alerts"
	httpx.Register(api, httpx.Route{ID: "listAlerts", Method: http.MethodGet, Path: "/alerts",
		Summary: "List server alerts, newest first", Tag: tag, Access: httpx.Require(rbac.SystemRead)}, h.list)
	httpx.Register(api, httpx.Route{ID: "getAlert", Method: http.MethodGet, Path: "/alerts/{id}",
		Summary: "Get an alert", Tag: tag, Access: httpx.Require(rbac.SystemRead)}, h.get)
	httpx.Register(api, httpx.Route{ID: "acknowledgeAlert", Method: http.MethodPost, Path: "/alerts/{id}/acknowledge",
		Summary: "Mark an alert as seen (it is no longer active)", Tag: tag, Access: httpx.Require(rbac.AlertsManage)}, h.acknowledge)
	httpx.Register(api, httpx.Route{ID: "deleteAlert", Method: http.MethodDelete, Path: "/alerts/{id}",
		Summary: "Remove an alert from the list", Tag: tag, Access: httpx.Require(rbac.AlertsManage),
		Status: http.StatusNoContent}, h.delete)
}

func (h *handler) list(ctx context.Context, in *listInput) (*listOutput, error) {
	req, err := in.Query.Resolve(listOptions)
	if err != nil {
		return nil, err
	}
	items, total, err := h.svc.List(ctx, Filter{ActiveOnly: in.Active, Severity: Severity(in.Severity), Source: Source(in.Source)}, req)
	if err != nil {
		return nil, err
	}
	return &listOutput{Body: page.Paged(items, total, req)}, nil
}

func (h *handler) get(ctx context.Context, in *idInput) (*alertOutput, error) {
	a, err := h.svc.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &alertOutput{Body: a}, nil
}

func (h *handler) acknowledge(ctx context.Context, in *idInput) (*alertOutput, error) {
	a, err := h.svc.Acknowledge(ctx, httpx.PrincipalFrom(ctx), in.ID)
	if err != nil {
		return nil, err
	}
	return &alertOutput{Body: a}, nil
}

func (h *handler) delete(ctx context.Context, in *idInput) (*struct{}, error) {
	return nil, h.svc.Delete(ctx, httpx.PrincipalFrom(ctx), in.ID)
}
