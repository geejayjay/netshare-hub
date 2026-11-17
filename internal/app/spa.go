package app

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the built frontend. Any path that is not a real file
// returns index.html, so client-side routes survive a page reload.
func spaHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		if info, err := fs.Stat(static, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				// Vite puts a content hash in these file names, so they never change.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}

		index, err := fs.ReadFile(static, "index.html")
		if err != nil {
			http.Error(w, "The web UI is not built. Run `pnpm build` in ./web, then rebuild the server.",
				http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
