package system

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/go-chi/chi/v5"
)

// MetricsSnapshot carries lightweight server metrics for Prometheus exposition.
type MetricsSnapshot struct {
	Version       string
	UptimeSeconds float64
	ShareCount    int
	UserCount     int
	ActiveAlerts  int
	StorageBytes  int64
	StorageFiles  int64
}

// RegisterObservability mounts GET /healthz (always 200 OK when the server and
// database are reachable) and GET /metrics in Prometheus text exposition format.
func RegisterObservability(r chi.Router, version string, startedAt time.Time, collect func(ctx context.Context) MetricsSnapshot) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "{\"status\":\"ok\",\"version\":%q,\"uptimeSeconds\":%.0f}\n",
			version, time.Since(startedAt).Seconds())
	})

	r.Get("/metrics", func(w http.ResponseWriter, req *http.Request) {
		snap := MetricsSnapshot{Version: version, UptimeSeconds: time.Since(startedAt).Seconds()}
		if collect != nil {
			snap = collect(req.Context())
			snap.UptimeSeconds = time.Since(startedAt).Seconds()
			if snap.Version == "" {
				snap.Version = version
			}
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, "# HELP netshare_up 1 when NetShare Hub is serving requests.\n")
		fmt.Fprintf(w, "# TYPE netshare_up gauge\nnetshare_up 1\n")
		fmt.Fprintf(w, "# HELP netshare_uptime_seconds Process uptime in seconds.\n")
		fmt.Fprintf(w, "# TYPE netshare_uptime_seconds counter\nnetshare_uptime_seconds %.2f\n", snap.UptimeSeconds)
		fmt.Fprintf(w, "# HELP netshare_shares_total Number of configured shares.\n")
		fmt.Fprintf(w, "# TYPE netshare_shares_total gauge\nnetshare_shares_total %d\n", snap.ShareCount)
		fmt.Fprintf(w, "# HELP netshare_users_total Number of configured users.\n")
		fmt.Fprintf(w, "# TYPE netshare_users_total gauge\nnetshare_users_total %d\n", snap.UserCount)
		fmt.Fprintf(w, "# HELP netshare_alerts_active Number of active unacknowledged alerts.\n")
		fmt.Fprintf(w, "# TYPE netshare_alerts_active gauge\nnetshare_alerts_active %d\n", snap.ActiveAlerts)
		fmt.Fprintf(w, "# HELP netshare_storage_analyzed_bytes Total analyzed storage across shares.\n")
		fmt.Fprintf(w, "# TYPE netshare_storage_analyzed_bytes gauge\nnetshare_storage_analyzed_bytes %d\n", snap.StorageBytes)
		fmt.Fprintf(w, "# HELP netshare_storage_analyzed_files Total analyzed files across shares.\n")
		fmt.Fprintf(w, "# TYPE netshare_storage_analyzed_files gauge\nnetshare_storage_analyzed_files %d\n", snap.StorageFiles)
		fmt.Fprintf(w, "# HELP go_goroutines Number of active goroutines.\n")
		fmt.Fprintf(w, "# TYPE go_goroutines gauge\ngo_goroutines %d\n", runtime.NumGoroutine())
		fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes Current heap allocation in bytes.\n")
		fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes gauge\ngo_memstats_alloc_bytes %d\n", mem.Alloc)
	})
}
