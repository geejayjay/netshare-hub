package jobs

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
)

// listOptions is this endpoint's pagination policy.
var listOptions = page.Options{
	DefaultSize: 25,
	MaxSize:     100,
	DefaultSort: "-createdAt",
	SortFields:  map[string]string{"createdAt": "created_at"},
}

type listInput struct {
	page.Query
	All    bool `query:"all" doc:"Everyone's jobs instead of your own. Needs the jobs:manage permission."`
	Active bool `query:"active" doc:"Only jobs that are queued or running."`
}

type idInput struct {
	ID string `path:"id" minLength:"1" maxLength:"64" pattern:"^[a-f0-9]+$"`
}

type jobOutput struct {
	Body Job
}

type listOutput struct {
	Body page.List[Job]
}

type handler struct {
	svc *Service
}

// Register adds the job routes. Every signed-in user may use them: each user
// sees and cancels their own jobs, and jobs:manage extends that to everyone's.
// Live updates arrive on the events stream as "job" events.
func Register(api huma.API, svc *Service) {
	h := &handler{svc: svc}
	const tag = "Jobs"

	httpx.Register(api, httpx.Route{
		ID: "listJobs", Method: http.MethodGet, Path: "/jobs",
		Summary: "List background jobs", Tag: tag, Access: httpx.Authenticated(),
	}, h.list)

	httpx.Register(api, httpx.Route{
		ID: "getJob", Method: http.MethodGet, Path: "/jobs/{id}",
		Summary: "Get a background job", Tag: tag, Access: httpx.Authenticated(),
	}, h.get)

	httpx.Register(api, httpx.Route{
		ID: "cancelJob", Method: http.MethodPost, Path: "/jobs/{id}/cancel",
		Summary: "Cancel a background job", Tag: tag, Access: httpx.Authenticated(),
	}, h.cancel)
}

func (h *handler) list(ctx context.Context, in *listInput) (*listOutput, error) {
	req, err := in.Query.Resolve(listOptions)
	if err != nil {
		return nil, err
	}
	items, total, err := h.svc.List(ctx, httpx.PrincipalFrom(ctx), in.All, in.Active, req)
	if err != nil {
		return nil, err
	}
	return &listOutput{Body: page.Paged(items, total, req)}, nil
}

func (h *handler) get(ctx context.Context, in *idInput) (*jobOutput, error) {
	j, err := h.svc.Get(ctx, httpx.PrincipalFrom(ctx), in.ID)
	if err != nil {
		return nil, err
	}
	return &jobOutput{Body: j}, nil
}

func (h *handler) cancel(ctx context.Context, in *idInput) (*jobOutput, error) {
	j, err := h.svc.Cancel(ctx, httpx.PrincipalFrom(ctx), in.ID)
	if err != nil {
		return nil, err
	}
	return &jobOutput{Body: j}, nil
}
