// Package system exposes health and version information.
package system

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
)

// Health is the health-check response.
type Health struct {
	Status  string `json:"status" example:"ok"`
	Version string `json:"version" example:"0.1.0"`
}

type healthOutput struct {
	Body Health
}

// Register adds the system routes.
func Register(api huma.API, version string) {
	httpx.Register(api, httpx.Route{
		ID: "getHealth", Method: http.MethodGet, Path: "/health",
		Summary: "Health check", Tag: "System", Access: httpx.Public(),
	}, func(context.Context, *struct{}) (*healthOutput, error) {
		return &healthOutput{Body: Health{Status: "ok", Version: version}}, nil
	})
}
