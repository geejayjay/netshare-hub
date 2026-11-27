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

type configInput struct {
	Body StorageScanConfig
}

type configOutput struct {
	Body StorageScanConfig
}

type scanInput struct {
	Body *StorageScanConfig
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
		ID: "updateStorageConfig", Method: http.MethodPut, Path: "/storage/config",
		Summary: "Save share and path exclusions for storage scans", Tag: "Storage",
		Access: httpx.Require(rbac.SettingsWrite),
	}, func(_ context.Context, in *configInput) (*configOutput, error) {
		return &configOutput{Body: svc.SetConfig(in.Body)}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "runStorageScan", Method: http.MethodPost, Path: "/storage/scan",
		Summary: "Start a background scan of configured shares for disk usage and duplicates", Tag: "Storage",
		Access: httpx.Require(rbac.SettingsWrite),
	}, func(_ context.Context, in *scanInput) (*reportOutput, error) {
		var opts *StorageScanConfig
		if in != nil && in.Body != nil {
			opts = in.Body
		}
		return &reportOutput{Body: svc.StartScan(opts)}, nil
	})
}
