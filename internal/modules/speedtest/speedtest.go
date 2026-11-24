// Package speedtest provides on-demand LAN throughput and server disk benchmarks.
package speedtest

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

const (
	maxNetMiB  = 256
	maxDiskMiB = 128
)

// DiskTestRequest requests a sequential write/read benchmark in a directory.
type DiskTestRequest struct {
	Path    string `json:"path,omitempty" maxLength:"1024" doc:"Target folder to benchmark (defaults to the server data directory)."`
	SizeMiB int    `json:"sizeMiB,omitempty" minimum:"1" maximum:"128" default:"32" doc:"Test file size in MiB (1–128, default 32)."`
}

// DiskTestResult reports sequential disk write and read throughput.
type DiskTestResult struct {
	Path      string  `json:"path"`
	SizeMiB   int     `json:"sizeMiB"`
	WriteMBps float64 `json:"writeMBps"`
	ReadMBps  float64 `json:"readMBps"`
	WriteMs   int64   `json:"writeMs"`
	ReadMs    int64   `json:"readMs"`
}

type diskInput struct {
	Body DiskTestRequest
}

type diskOutput struct {
	Body DiskTestResult
}

// Register adds the Huma disk benchmark endpoint and raw Chi streaming download/upload endpoints.
func Register(api huma.API, r chi.Router, defaultDir string) {
	httpx.Register(api, httpx.Route{
		ID: "runDiskSpeedTest", Method: http.MethodPost, Path: "/system/speedtest/disk",
		Summary: "Benchmark sequential disk write and read speed in a target folder",
		Tag:     "System", Access: httpx.Require(rbac.SystemRead),
	}, func(ctx context.Context, in *diskInput) (*diskOutput, error) {
		dir := strings.TrimSpace(in.Body.Path)
		if dir == "" {
			dir = defaultDir
		}
		sizeMiB := in.Body.SizeMiB
		if sizeMiB <= 0 {
			sizeMiB = 32
		}
		if sizeMiB > maxDiskMiB {
			sizeMiB = maxDiskMiB
		}
		res, err := benchmarkDisk(ctx, dir, sizeMiB)
		if err != nil {
			return nil, err
		}
		return &diskOutput{Body: res}, nil
	})

	// Raw streaming endpoints for network throughput (zero JSON/base64 overhead).
	r.Get("/system/speedtest/download", func(w http.ResponseWriter, req *http.Request) {
		p := httpx.PrincipalFrom(req.Context())
		if p == nil || !p.Can(rbac.SystemRead) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mb, _ := strconv.Atoi(req.URL.Query().Get("mb"))
		if mb <= 0 {
			mb = 32
		}
		if mb > maxNetMiB {
			mb = maxNetMiB
		}
		totalBytes := int64(mb) << 20
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(totalBytes, 10))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		buf := make([]byte, 256<<10) // 256 KiB reusable chunk
		rem := totalBytes
		for rem > 0 {
			if req.Context().Err() != nil {
				return
			}
			n := int64(len(buf))
			if rem < n {
				n = rem
			}
			written, err := w.Write(buf[:n])
			if err != nil {
				return
			}
			rem -= int64(written)
		}
	})

	r.Post("/system/speedtest/upload", func(w http.ResponseWriter, req *http.Request) {
		p := httpx.PrincipalFrom(req.Context())
		if p == nil || !p.Can(rbac.SystemRead) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		start := time.Now()
		n, err := io.Copy(io.Discard, io.LimitReader(req.Body, int64(maxNetMiB)<<20))
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		dur := time.Since(start)
		secs := math.Max(dur.Seconds(), 0.001)
		mbps := (float64(n) * 8) / (secs * 1_000_000)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bytes":      n,
			"durationMs": dur.Milliseconds(),
			"mbps":       math.Round(mbps*10) / 10,
		})
	})
}

func benchmarkDisk(ctx context.Context, dir string, sizeMiB int) (DiskTestResult, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return DiskTestResult{}, errs.Invalid("INVALID_PATH", "Target directory does not exist.")
	}

	f, err := os.CreateTemp(dir, ".netshare-speedtest-*")
	if err != nil {
		return DiskTestResult{}, errs.Invalid("PATH_NOT_WRITABLE", "Could not create temporary benchmark file in target folder.")
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	chunk := make([]byte, 1<<20) // 1 MiB non-zero pattern (avoids sparse/zero-page compression)
	for i := range chunk {
		chunk[i] = byte((i * 31) ^ 0xA5)
	}

	// Sequential write + fsync
	wStart := time.Now()
	for i := 0; i < sizeMiB; i++ {
		if ctx.Err() != nil {
			_ = f.Close()
			return DiskTestResult{}, ctx.Err()
		}
		if _, err := f.Write(chunk); err != nil {
			_ = f.Close()
			return DiskTestResult{}, err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return DiskTestResult{}, err
	}
	wDur := time.Since(wStart)
	if err := f.Close(); err != nil {
		return DiskTestResult{}, err
	}

	// Sequential read
	rf, err := os.Open(tmpPath)
	if err != nil {
		return DiskTestResult{}, err
	}
	rStart := time.Now()
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(io.Discard, rf, buf); err != nil {
		_ = rf.Close()
		return DiskTestResult{}, err
	}
	rDur := time.Since(rStart)
	_ = rf.Close()

	wSec := math.Max(wDur.Seconds(), 0.001)
	rSec := math.Max(rDur.Seconds(), 0.001)
	return DiskTestResult{
		Path:      filepath.Clean(dir),
		SizeMiB:   sizeMiB,
		WriteMBps: math.Round((float64(sizeMiB)/wSec)*10) / 10,
		ReadMBps:  math.Round((float64(sizeMiB)/rSec)*10) / 10,
		WriteMs:   wDur.Milliseconds(),
		ReadMs:    rDur.Milliseconds(),
	}, nil
}
