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

// Modules says which optional modules are on (config [modules]). The UI
// shows only the pages of modules that are on; a module that is off has
// no routes at all.
type Modules struct {
	FileManager     bool `json:"fileManager"`
	ActivityLog     bool `json:"activityLog"`
	RansomwareGuard bool `json:"ransomwareGuard"`
	BruteForce      bool `json:"bruteForce"`
	Discovery       bool `json:"discovery"`
	Sessions        bool `json:"sessions"`
	Backup          bool `json:"backup"`
	NFS             bool `json:"nfs"`
	Replication     bool `json:"replication"`
	BlockStorage    bool `json:"blockStorage"`
}

type modulesOutput struct {
	Body Modules
}

// RegisterModules adds GET /system/modules.
func RegisterModules(api huma.API, m Modules) {
	httpx.Register(api, httpx.Route{
		ID: "getModules", Method: http.MethodGet, Path: "/system/modules",
		Summary: "Which optional modules are on", Tag: "System", Access: httpx.Authenticated(),
	}, func(context.Context, *struct{}) (*modulesOutput, error) {
		return &modulesOutput{Body: m}, nil
	})
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
