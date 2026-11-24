package scheduler

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

type listOutput struct {
	Body page.List[Status]
}

type taskPathInput struct {
	Name string `path:"name" minLength:"1" maxLength:"64"`
}

type updateTaskInput struct {
	Name string `path:"name" minLength:"1" maxLength:"64"`
	Body UpdateRequest
}

type statusOutput struct {
	Body Status
}

// Register adds the task list, schedule editor, run-now, and reset endpoints.
func Register(api huma.API, s *Scheduler) {
	const tag = "System"

	httpx.Register(api, httpx.Route{
		ID: "listScheduledTasks", Method: http.MethodGet, Path: "/system/tasks",
		Summary: "List scheduled tasks: status, enabled state, and frequency",
		Tag:     tag, Access: httpx.Require(rbac.SystemRead),
	}, func(context.Context, *struct{}) (*listOutput, error) {
		return &listOutput{Body: page.All(s.Status())}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "updateScheduledTask", Method: http.MethodPut, Path: "/system/tasks/{name}",
		Summary: "Toggle a scheduled task on/off or change its frequency, day, and time",
		Tag:     tag, Access: httpx.Require(rbac.SettingsWrite),
	}, func(ctx context.Context, in *updateTaskInput) (*statusOutput, error) {
		st, err := s.Update(ctx, in.Name, in.Body)
		if err != nil {
			return nil, err
		}
		return &statusOutput{Body: st}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "runScheduledTask", Method: http.MethodPost, Path: "/system/tasks/{name}/run",
		Summary: "Trigger a scheduled task immediately",
		Tag:     tag, Access: httpx.Require(rbac.SettingsWrite), Status: http.StatusAccepted,
	}, func(_ context.Context, in *taskPathInput) (*struct{}, error) {
		if !s.RunNow(in.Name) {
			return nil, errs.NotFound("TASK_NOT_FOUND", "Unknown scheduled task.")
		}
		return nil, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "resetScheduledTask", Method: http.MethodPost, Path: "/system/tasks/{name}/reset",
		Summary: "Reset a scheduled task back to its default schedule",
		Tag:     tag, Access: httpx.Require(rbac.SettingsWrite),
	}, func(ctx context.Context, in *taskPathInput) (*statusOutput, error) {
		st, err := s.Reset(ctx, in.Name)
		if err != nil {
			return nil, err
		}
		return &statusOutput{Body: st}, nil
	})
}
