package scheduler

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

type listOutput struct {
	Body page.List[Status]
}

// Register adds the read-only task list (for the system status page).
func Register(api huma.API, s *Scheduler) {
	httpx.Register(api, httpx.Route{
		ID: "listScheduledTasks", Method: http.MethodGet, Path: "/system/tasks",
		Summary: "List scheduled tasks: when they last ran, how it went, when they run next",
		Tag:     "System", Access: httpx.Require(rbac.SystemRead),
	}, func(context.Context, *struct{}) (*listOutput, error) {
		return &listOutput{Body: page.All(s.Status())}, nil
	})
}
