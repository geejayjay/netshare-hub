// Package app wires the application together. It is the only place that
// knows about every module; modules never construct each other.
package app

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"netshare-hub/internal/db/migrations"
	"netshare-hub/internal/modules/auth"
	"netshare-hub/internal/modules/system"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/httpx"
)

// Version is set at build time with -ldflags "-X netshare-hub/internal/app.Version=1.2.3".
var Version = "dev"

// APIPrefix is where the whole API lives. A future breaking change gets /api/v2.
const APIPrefix = "/api/v1"

// App is the assembled application.
type App struct {
	Handler http.Handler
	API     huma.API
	db      *db.DB
}

// New builds the application. static is the built frontend; pass nil to run
// the API alone (tests, or frontend development with the Vite dev server).
func New(ctx context.Context, cfg config.Config, static fs.FS) (*App, error) {
	httpx.Install()

	database, err := db.Open(filepath.Join(cfg.DataDir, "netshare-hub.db"))
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(ctx, migrations.FS); err != nil {
		database.Close()
		return nil, err
	}

	// Services. Dependencies point one way: auth uses users, never the reverse.
	userService := users.NewService(users.NewRepo(database))
	authService := auth.NewService(auth.NewRepo(database), userService, auth.DefaultOptions())

	router := chi.NewRouter()
	router.Use(httpx.RequestID, httpx.RealIP, httpx.AccessLog, httpx.Recover, httpx.SecurityHeaders)

	var api huma.API
	router.Route(APIPrefix, func(r chi.Router) {
		r.Use(httpx.NoStore, auth.Middleware(authService))
		r.NotFound(httpx.NotFound)
		r.MethodNotAllowed(httpx.MethodNotAllowed)

		api = humachi.New(r, apiConfig())

		// Each module registers its own routes.
		system.Register(api, Version)
		auth.Register(api, authService, cfg.SecureCookies)
		users.Register(api, userService)
	})

	if static != nil {
		router.Handle("/*", spaHandler(static))
	}

	slog.Info("application ready", "version", Version, "data_dir", cfg.DataDir)
	return &App{Handler: router, API: api, db: database}, nil
}

// Close releases resources.
func (a *App) Close() error {
	return a.db.Close()
}

func apiConfig() huma.Config {
	c := huma.DefaultConfig("NetShare Hub API", Version)
	c.Info.Description = "Simple network file sharing for Proxmox LXC and any Linux."
	c.Servers = []*huma.Server{{URL: APIPrefix}}
	// The built-in docs page loads scripts from a CDN, which would break the
	// "fully offline" rule. The spec itself is still served at /openapi.json.
	c.DocsPath = ""
	c.SchemasPath = ""
	// Do not add a "$schema" link to every response body.
	c.CreateHooks = nil
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		httpx.SecurityScheme: {Type: "apiKey", In: "cookie", Name: auth.CookieName},
	}
	return c
}
