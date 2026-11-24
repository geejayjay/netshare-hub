package thumbs

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"netshare-hub/internal/modules/files"
	"netshare-hub/internal/platform/httpx"
)

type capsOutput struct {
	Body ThumbnailCapabilities
}

// Register adds thumbnail capability and image streaming endpoints.
func Register(api huma.API, r chi.Router, svc *Service) {
	httpx.Register(api, httpx.Route{
		ID: "getThumbnailCapabilities", Method: http.MethodGet, Path: "/files/thumbnails/capabilities",
		Summary: "Report whether image and video thumbnails are supported", Tag: "Files",
		Access: httpx.Authenticated(),
	}, func(_ context.Context, _ *struct{}) (*capsOutput, error) {
		return &capsOutput{Body: svc.Capabilities()}, nil
	})

	r.Get("/files/thumbnail", func(w http.ResponseWriter, req *http.Request) {
		p := httpx.PrincipalFrom(req.Context())
		if p == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		share := req.URL.Query().Get("share")
		relPath := req.URL.Query().Get("path")
		if share == "" || relPath == "" {
			http.Error(w, "share and path are required", http.StatusBadRequest)
			return
		}
		size, _ := strconv.Atoi(req.URL.Query().Get("size"))

		ip, _ := netip.ParseAddr(httpx.ClientIP(req.Context()))
		actor := files.Actor{UserID: p.UserID, IP: ip}
		raw, etag, err := svc.Get(req.Context(), actor, share, relPath, size)
		if err != nil {
			http.Error(w, "thumbnail unavailable", http.StatusNotFound)
			return
		}
		if len(raw) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if req.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	})
}
