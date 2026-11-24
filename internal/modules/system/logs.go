package system

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/logger"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

type logsInput struct {
	Level     string `query:"level" enum:",debug,info,warn,error"`
	RequestID string `query:"requestId" maxLength:"128"`
	Q         string `query:"q" maxLength:"256"`
	Limit     int    `query:"limit" minimum:"1" maximum:"1000" default:"200"`
}

type logsOutput struct {
	Body page.List[logger.LogEntry]
}

// RegisterLogs adds GET /system/logs for inspecting recent structured logs.
func RegisterLogs(api huma.API, ring *logger.Ring) {
	httpx.Register(api, httpx.Route{
		ID: "listSystemLogs", Method: http.MethodGet, Path: "/system/logs",
		Summary: "List recent structured server logs (live stream arrives on SSE \"log\" events)",
		Tag:     "System", Access: httpx.Require(rbac.SystemRead),
	}, func(_ context.Context, in *logsInput) (*logsOutput, error) {
		entries := ring.Query(logger.Filter{
			Level:     in.Level,
			RequestID: in.RequestID,
			Query:     in.Q,
			Limit:     in.Limit,
		})
		return &logsOutput{Body: page.All(entries)}, nil
	})
}
