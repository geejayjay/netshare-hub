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
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"netshare-hub/internal/agent"
	"netshare-hub/internal/db/migrations"
	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/modules/activity"
	activitymigrations "netshare-hub/internal/modules/activity/migrations"
	"netshare-hub/internal/modules/alerts"
	"netshare-hub/internal/modules/auth"
	"netshare-hub/internal/modules/files"
	"netshare-hub/internal/modules/guard"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/modules/scheduler"
	"netshare-hub/internal/modules/sessions"
	"netshare-hub/internal/modules/shares"
	"netshare-hub/internal/modules/system"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/sse"
	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sambalog"
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
	// SMBEvents is the one fan-out of what happens over SMB (from Samba's
	// audit log). The activity log subscribes; so will the ransomware guard
	// and brute-force protection.
	SMBEvents *sambalog.Ingester
	db        *db.DB
	// stop ends the scheduled tasks and background loops and waits for them.
	stop       func()
	files      *files.Service
	activityDB *db.DB
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
	// UploadExpiry overrides how long an idle upload is kept (tests).
	UploadExpiry time.Duration
	// Activity tunes the activity log (tests use short batches).
	Activity activity.Options
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
	if err := database.Migrate(ctx, migrations.FS, filepath.Join(cfg.DataDir, "backups")); err != nil {
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
	alertService := alerts.NewService(alerts.NewRepo(database), sseHub)
	shareService.SetAlerts(alertService)

	// The activity log has its own database file: heavy SMB activity never
	// touches the main one.
	activityDB, err := db.Open(filepath.Join(cfg.DataDir, "activity.db"))
	if err != nil {
		database.Close()
		return nil, err
	}
	if err := activityDB.Migrate(ctx, activitymigrations.FS, filepath.Join(cfg.DataDir, "backups", "activity")); err != nil {
		activityDB.Close()
		database.Close()
		return nil, err
	}
	activityOpts := opts.Activity
	if activityOpts.Retention == 0 {
		activityOpts.Retention = time.Duration(cfg.ActivityRetentionDays) * 24 * time.Hour
	}
	activityService := activity.NewService(activityDB, shareService, activityOpts)
	smbEvents := sambalog.New()
	activityEvents := smbEvents.Subscribe("activity", 50000)

	// The ransomware guard watches the same events, and tells shares which
	// users may only read.
	guardService := guard.NewService(database, userService, shareService, osLayer.Samba, alertService)
	if err := guardService.Start(ctx); err != nil {
		activityDB.Close()
		database.Close()
		return nil, err
	}
	shareService.SetBlocks(guardService)
	guardEvents := smbEvents.Subscribe("guard", 50000)

	// SMB brute-force protection: failed logins from the same events.
	banService := guard.NewBans(database, shareService, osLayer.Samba, osLayer.Neighbors, alertService)
	if err := banService.Start(ctx); err != nil {
		activityDB.Close()
		database.Close()
		return nil, err
	}
	shareService.SetLogins(banService)
	banEvents := smbEvents.Subscribe("bans", 50000)
	// Write the config once at start: login logging on, ended bans gone.
	if err := shareService.Reapply(ctx); err != nil {
		slog.WarnContext(ctx, "could not write the Samba config at start", "err", err)
	}
	for kind, h := range opts.JobKinds {
		jobService.Register(kind, h)
	}
	fileClient := fileworker.NewClient(osLayer.Workers)
	fileService := files.NewService(database, fileClient, userService, shareService, jobService,
		files.Options{MaxUploadBytes: cfg.MaxUploadBytes, UploadExpiry: opts.UploadExpiry})
	jobService.Register(files.JobCopy, fileService.RunCopy)
	jobService.Register(files.JobDelete, fileService.RunDelete)

	// Everything timed, in one place.
	tasks := scheduler.New()
	tasks.Add(scheduler.Task{Name: "uploads.cleanup", Every: time.Hour, Run: func(ctx context.Context) error {
		fileService.SweepUploads(ctx) // abandoned uploads: a day without data
		return nil
	}})
	tasks.Add(scheduler.Task{Name: "jobs.cleanup", Every: time.Hour, Run: jobService.Cleanup})
	tasks.Add(scheduler.Task{Name: "alerts.cleanup", Daily: 3*time.Hour + 30*time.Minute, Run: alertService.Prune})
	tasks.Add(scheduler.Task{Name: "activity.cleanup", Daily: 4 * time.Hour, Run: activityService.Prune})
	tasks.Add(scheduler.Task{Name: "smb-bans.expire", Every: time.Minute, Run: banService.Expire})
	tasks.Add(scheduler.Task{Name: "smb-bans.forget", Every: 10 * time.Minute, Run: banService.Purge})

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
		files.Register(api, fileService)
		files.RegisterUploads(r, fileService)
		alerts.Register(api, alertService)
		activity.Register(api, activityService)
		guard.Register(api, guardService)
		guard.RegisterBans(api, banService)
		sessions.Register(api, osLayer.Samba)
		scheduler.Register(api, tasks)
	})

	if opts.Static != nil {
		router.Handle("/*", spaHandler(opts.Static))
	}

	// Every job kind is registered by now: resume interrupted jobs and start.
	if err := jobService.Start(ctx); err != nil {
		database.Close()
		return nil, err
	}

	tasks.Start()
	background, cancel := context.WithCancel(context.Background())
	var loops sync.WaitGroup
	loops.Add(4)
	go func() { defer loops.Done(); activityService.Record(background, activityEvents) }()
	go func() { defer loops.Done(); guardService.Run(background, guardEvents) }()
	go func() { defer loops.Done(); banService.Run(background, banEvents) }()
	go func() { defer loops.Done(); feedSMBEvents(background, osLayer.Audit, smbEvents) }()
	stop := func() {
		tasks.Stop()
		cancel()
		loops.Wait()
	}

	slog.Info("application ready", "version", Version, "data_dir", cfg.DataDir)
	return &App{Handler: router, API: api, Events: sseHub, Files: fileClient, Jobs: jobService, db: database, stop: stop, files: fileService,
		SMBEvents: smbEvents, activityDB: activityDB}, nil
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
		system := client.System()
		// The neighbour table is readable without privileges: the web
		// server reads it itself.
		system.Neighbors = sysexec.NewNeighbors()
		return system, nil
	default:
		// Everything in this one root process: the web server is the caller.
		return sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs()})
	}
}

// feedSMBEvents passes what the audit source reports (the agent's stream,
// in production) to the fan-out, and reconnects when the source ends: the
// agent restarted, or is not up yet. It waits longer after each failure.
func feedSMBEvents(ctx context.Context, source sys.Audit, out *sambalog.Ingester) {
	if source == nil {
		return
	}
	wait := time.Second
	for ctx.Err() == nil {
		events, err := source.Events(ctx)
		if err == nil {
			wait = time.Second
			for ev := range events {
				out.Emit(ev)
			}
		} else {
			slog.WarnContext(ctx, "cannot read the SMB activity yet; trying again", "err", err, "in", wait.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

// SweepUploads throws away abandoned uploads now (it also runs every hour).
func (a *App) SweepUploads(ctx context.Context) { a.files.SweepUploads(ctx) }

// Close releases resources.
func (a *App) Close() error {
	a.stop()
	// Running jobs pause and resume at the next start.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a.Jobs.Stop(ctx)
	a.activityDB.Close()
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
