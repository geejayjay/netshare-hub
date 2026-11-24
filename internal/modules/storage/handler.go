package storage

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

type reportOutput struct {
	Body StorageReport
}

// Register adds storage analyzer routes.
func Register(api huma.API, svc *Service) {
	httpx.Register(api, httpx.Route{
		ID: "getStorageReport", Method: http.MethodGet, Path: "/storage/report",
		Summary: "Get the latest storage analyzer report", Tag: "Storage",
		Access: httpx.Require(rbac.SystemRead),
	}, func(_ context.Context, _ *struct{}) (*reportOutput, error) {
		return &reportOutput{Body: svc.Report()}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "runStorageScan", Method: http.MethodPost, Path: "/storage/scan",
		Summary: "Scan all shares for disk usage and duplicates", Tag: "Storage",
		Access: httpx.Require(rbac.SettingsWrite),
	}, func(ctx context.Context, _ *struct{}) (*reportOutput, error) {
		rep, err := svc.Scan(ctx)
		if err != nil {
			return nil, err
		}
		return &reportOutput{Body: rep}, nil
	})
}
