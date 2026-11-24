package system

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

// MountWarning codes reported by LXC mount detection.
const (
	WarnReadOnly    = "read_only_mount"
	WarnRootDisk    = "on_root_disk"
	WarnUIDMismatch = "uid_mapping_mismatch"
	WarnMissing     = "path_missing"
)

// MountCheck summarizes mount health for a single directory path.
type MountCheck struct {
	Path        string   `json:"path"`
	MountPoint  string   `json:"mountPoint"`
	FSType      string   `json:"fsType,omitempty"`
	ReadOnly    bool     `json:"readOnly"`
	OnRootDisk  bool     `json:"onRootDisk"`
	UIDMismatch bool     `json:"uidMismatch"`
	Warnings    []string `json:"warnings"`
}

// MountReport describes the container environment and checked share paths.
type MountReport struct {
	Environment  string       `json:"environment" enum:"host,lxc,docker"`
	Unprivileged bool         `json:"unprivileged"`
	Checks       []MountCheck `json:"checks"`
}

type mountInfoEntry struct {
	MountPoint string
	FSType     string
	ReadOnly   bool
}

// parseMountInfo parses Linux /proc/self/mountinfo lines.
func parseMountInfo(r io.Reader) []mountInfoEntry {
	var out []mountInfoEntry
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		left := strings.Fields(line[:sep])
		right := strings.Fields(line[sep+3:])
		if len(left) < 6 || len(right) < 1 {
			continue
		}
		mp := unescapeMountPath(left[4])
		opts := strings.Split(left[5], ",")
		ro := false
		for _, o := range opts {
			if o == "ro" {
				ro = true
				break
			}
		}
		if len(right) >= 3 {
			for _, o := range strings.Split(right[2], ",") {
				if o == "ro" {
					ro = true
					break
				}
			}
		}
		out = append(out, mountInfoEntry{
			MountPoint: mp,
			FSType:     right[0],
			ReadOnly:   ro,
		})
	}
	return out
}

func unescapeMountPath(s string) string {
	s = strings.ReplaceAll(s, `\040`, " ")
	s = strings.ReplaceAll(s, `\011`, "\t")
	s = strings.ReplaceAll(s, `\012`, "\n")
	s = strings.ReplaceAll(s, `\134`, `\`)
	return s
}

func bestMountFor(target string, mounts []mountInfoEntry) (mountInfoEntry, bool) {
	clean := filepath.ToSlash(filepath.Clean(target))
	var best mountInfoEntry
	found := false
	for _, m := range mounts {
		mp := filepath.ToSlash(filepath.Clean(m.MountPoint))
		if mp == "/" {
			if !found || len(mp) > len(best.MountPoint) {
				best = m
				found = true
			}
			continue
		}
		if clean == mp || strings.HasPrefix(clean, mp+"/") {
			if !found || len(mp) > len(best.MountPoint) {
				best = m
				found = true
			}
		}
	}
	return best, found
}

type mountsInput struct {
	Paths []string `query:"path" doc:"Optional folder paths to inspect; defaults to all configured share paths."`
}

type mountsOutput struct {
	Body MountReport
}

// RegisterMounts adds GET /system/mounts for LXC bind-mount and UID-mapping diagnostics.
func RegisterMounts(api huma.API, listPaths func(context.Context) []string) {
	httpx.Register(api, httpx.Route{
		ID: "getSystemMounts", Method: http.MethodGet, Path: "/system/mounts",
		Summary: "Inspect container/LXC bind mounts, read-only mounts, root-disk usage, and UID mapping",
		Tag:     "System", Access: httpx.Require(rbac.SystemRead),
	}, func(ctx context.Context, in *mountsInput) (*mountsOutput, error) {
		paths := in.Paths
		if len(paths) == 0 && listPaths != nil {
			paths = listPaths(ctx)
		}
		return &mountsOutput{Body: inspectMounts(paths)}, nil
	})
}
