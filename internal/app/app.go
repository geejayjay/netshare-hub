// Package app wires the application together. It is the only place that
// knows about every module; modules never construct each other.
package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"netshare-hub/internal/agent"
	"netshare-hub/internal/db/migrations"
	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/modules/auth"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/modules/shares"
	"netshare-hub/internal/modules/system"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/sse"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sysexec"
	"netshare-hub/internal/sys/sysfake"
)

// Version is set at build time with -ldflags "-X netshare-hub/internal/app.Version=1.2.3".
var Version = "dev"

// APIPrefix is where the whole API lives. A future breaking change gets /api/v2.
const APIPrefix = "/api/v1"

// App is the assembled application.
type App struct {
	Handler http.Handler
	API     huma.API
	Events  *sse.Hub
	// Files runs file operations as a user, through that user's file worker.
	// The file manager (step 1.11a) is built on it.
	Files *fileworker.Client
	// Jobs runs long work in the background (step 1.3).
	Jobs *jobs.Service
	db   *db.DB
}

// Options are the parts of the application a caller may supply.
type Options struct {
	// Static is the built frontend. Leave nil to run the API alone (tests,
	// or frontend development with the Vite dev server).
	Static fs.FS
	// System replaces the system chosen by cfg.Sys. Tests use it to inject
	// a fake they can inspect.
	System *sys.System
	// JobKinds registers extra job kinds before the engine starts. Tests use
	// it; features register their kinds in New.
	JobKinds map[string]jobs.Handler
	// JobOptions tunes the job engine (tests use short intervals).
	JobOptions jobs.Options
}

// New builds the application.
func New(ctx context.Context, cfg config.Config, opts Options) (*App, error) {
	httpx.Install()

	osLayer, err := chooseSystem(ctx, cfg, opts.System)
	if err != nil {
		return nil, err
	}

	database, err := db.Open(filepath.Join(cfg.DataDir, "netshare-hub.db"))
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(ctx, migrations.FS); err != nil {
		database.Close()
		return nil, err
	}

	// Services. Dependencies point one way: auth uses users, never the reverse.
	userService := users.NewService(users.NewRepo(database), osLayer.Accounts)
	authService := auth.NewService(auth.NewRepo(database), userService, auth.DefaultOptions())
	sseHub := sse.NewHub()
	jobService := jobs.NewService(jobs.NewRepo(database), sseHub, opts.JobOptions)
	shareService := shares.NewService(shares.NewRepo(database), userService, osLayer, jobService, cfg.ProtectedDirs()...)
	// Users must be taken off every share before their system account goes away.
	userService.SetAccessRevoker(shareService)
	jobService.Register(shares.JobFixPermissions, shareService.RunFixPermissions)
	for kind, h := range opts.JobKinds {
		jobService.Register(kind, h)
	}
	files := fileworker.NewClient(osLayer.Workers)

	router := chi.NewRouter()
	router.Use(httpx.RequestID, httpx.RealIP, httpx.AccessLog, httpx.Recover, httpx.SecurityHeaders)

	var api huma.API
	router.Route(APIPrefix, func(r chi.Router) {
		r.Use(httpx.NoStore, auth.Middleware(authService))
		r.NotFound(httpx.NotFound)
		r.MethodNotAllowed(httpx.MethodNotAllowed)

		r.Get("/events", sseHub.ServeHTTP)

		api = humachi.New(r, apiConfig())

		// Each module registers its own routes.
		system.Register(api, Version)
		auth.Register(api, authService, cfg.SecureCookies)
		users.Register(api, userService)
		shares.Register(api, shareService)
		jobs.Register(api, jobService)
	})

	if opts.Static != nil {
		router.Handle("/*", spaHandler(opts.Static))
	}

	// Every job kind is registered by now: resume interrupted jobs and start.
	if err := jobService.Start(ctx); err != nil {
		database.Close()
		return nil, err
	}

	slog.Info("application ready", "version", Version, "data_dir", cfg.DataDir)
	return &App{Handler: router, API: api, Events: sseHub, Files: files, Jobs: jobService, db: database}, nil
}

// chooseSystem returns how this process reaches the operating system.
func chooseSystem(ctx context.Context, cfg config.Config, override *sys.System) (sys.System, error) {
	if override != nil {
		return *override, nil
	}
	switch cfg.Sys {
	case "fake":
		slog.Warn(`running with sys = "fake": no Linux users or Samba config are touched; for development only`)
		return sysfake.New().System(), nil
	case "agent":
		// The server stays unprivileged; the root agent does the system work.
		client := agent.Dial(cfg.AgentSocket)
		if err := client.Ping(ctx); err != nil {
			return sys.System{}, fmt.Errorf("cannot reach the agent at %s (is `netshare-hub agent` running?): %w", cfg.AgentSocket, err)
		}
		return client.System(), nil
	default:
		// Everything in this one root process: the web server is the caller.
		return sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs()})
	}
}

// Close releases resources.
func (a *App) Close() error {
	// Running jobs pause and resume at the next start.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a.Jobs.Stop(ctx)
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
