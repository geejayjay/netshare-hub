package system

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/sys"
)

// MountWarning codes reported by LXC mount detection.
const (
	WarnReadOnly    = sys.MountWarnReadOnly
	WarnRootDisk    = sys.MountWarnRootDisk
	WarnUIDMismatch = sys.MountWarnUIDMismatch
	WarnMissing     = sys.MountWarnMissing
)

// MountCheck summarizes mount health for a single directory path.
type MountCheck = sys.MountCheck

// MountReport describes the container environment and checked share paths.
type MountReport = sys.MountReport

type mountsInput struct {
	Paths []string `query:"path" doc:"Optional folder paths to inspect; defaults to all configured share paths."`
}

type mountsOutput struct {
	Body MountReport
}

// RegisterMounts adds GET /system/mounts for LXC bind-mount and UID-mapping
// diagnostics. The inspection itself runs in the system layer (the root
// agent in production): the web server is sandboxed by systemd
// (ProtectSystem=strict), so its own mount table shows every folder as
// read-only and cannot tell a real read-only mount from the sandbox.
func RegisterMounts(api huma.API, listPaths func(context.Context) []string, files sys.Files) {
	httpx.Register(api, httpx.Route{
		ID: "getSystemMounts", Method: http.MethodGet, Path: "/system/mounts",
		Summary: "Inspect container/LXC bind mounts, read-only mounts, root-disk usage, and UID mapping",
		Tag:     "System", Access: httpx.Require(rbac.SystemRead),
	}, func(ctx context.Context, in *mountsInput) (*mountsOutput, error) {
		paths := in.Paths
		if len(paths) == 0 && listPaths != nil {
			paths = listPaths(ctx)
		}
		if len(paths) > sys.MaxMountPaths {
			paths = paths[:sys.MaxMountPaths]
		}
		report, err := files.InspectMounts(ctx, paths)
		if err != nil {
			return nil, errs.Unavailable(errs.CodeUnavailable, "Could not inspect the mounts: "+sys.Message(err)).WithCause(err)
		}
		if report.Checks == nil {
			report.Checks = []MountCheck{}
		}
		return &mountsOutput{Body: report}, nil
	})
}
