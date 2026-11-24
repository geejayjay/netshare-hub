package system

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestHealthzAndPrometheusMetrics(t *testing.T) {
	r := chi.NewRouter()
	RegisterObservability(r, "1.2.3", time.Now().Add(-10*time.Second), func(context.Context) MetricsSnapshot {
		return MetricsSnapshot{
			ShareCount:   4,
			UserCount:    7,
			ActiveAlerts: 1,
			StorageBytes: 1048576,
			StorageFiles: 42,
		}
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected /healthz: %d %s", rec.Code, rec.Body.String())
	}

	mrec := httptest.NewRecorder()
	r.ServeHTTP(mrec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if mrec.Code != http.StatusOK {
		t.Fatalf("unexpected /metrics status: %d", mrec.Code)
	}
	body := mrec.Body.String()
	for _, want := range []string{
		"netshare_up 1",
		"netshare_shares_total 4",
		"netshare_users_total 7",
		"netshare_alerts_active 1",
		"netshare_storage_analyzed_bytes 1048576",
		"netshare_storage_analyzed_files 42",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}
