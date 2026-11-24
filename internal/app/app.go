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
	"netshare-hub/internal/modules/audit"
	"netshare-hub/internal/modules/auth"
	"netshare-hub/internal/modules/backup"
	"netshare-hub/internal/modules/block"
	"netshare-hub/internal/modules/discovery"
	"netshare-hub/internal/modules/files"
	"netshare-hub/internal/modules/groups"
	"netshare-hub/internal/modules/guard"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/modules/notify"
	"netshare-hub/internal/modules/replication"
	"netshare-hub/internal/modules/scheduler"
	"netshare-hub/internal/modules/search"
	"netshare-hub/internal/modules/sessions"
	"netshare-hub/internal/modules/shares"
	"netshare-hub/internal/modules/speedtest"
	"netshare-hub/internal/modules/storage"
	"netshare-hub/internal/modules/system"
	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/logger"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
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
	// MaintenanceJobKinds are the same, in the maintenance lane.
	MaintenanceJobKinds map[string]jobs.Handler
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
	osLayer = TrimSystem(osLayer, cfg.Modules)

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
	// Groups (1.5): real Linux groups; share access through a group is
	// expanded to its members.
	groupService := groups.NewService(database, userService, osLayer.Groups)
	shareService.SetGroups(groupService)
	groupService.SetShares(shareService)
	userService.SetNameTaken(groupService.NameTaken)
	// Users must be taken out of every group and off every share before
	// their system account goes away.
	userService.SetAccessRevoker(revokers{groupService, shareService})
	jobService.RegisterIn(jobs.LaneMaintenance, shares.JobFixPermissions, shareService.RunFixPermissions)
	alertService := alerts.NewService(alerts.NewRepo(database), sseHub)
	shareService.SetAlerts(alertService)
	notifyService := notify.NewService(database)
	alertService.SetOnRaise(notifyService.Enqueue)
	mods := cfg.Modules
	closeAll := func() { database.Close() }

	// Optional modules: each is built, started and scheduled only when it is
	// on (config [modules]). The guard and ban services are always built,
	// because they are only database-backed objects until started: the
	// config backup carries their settings either way.
	smbEvents := sambalog.New()
	var loops []func(context.Context)
	loops = append(loops, notifyService.Run)
	tasks := scheduler.New()

	var activityDB *db.DB
	var activityService *activity.Service
	if mods.ActivityLog {
		// The activity log has its own database file: heavy SMB activity
		// never touches the main one.
		activityDB, err = db.Open(filepath.Join(cfg.DataDir, "activity.db"))
		if err != nil {
			closeAll()
			return nil, err
		}
		closeAll = func() { activityDB.Close(); database.Close() }
		if err := activityDB.Migrate(ctx, activitymigrations.FS, filepath.Join(cfg.DataDir, "backups", "activity")); err != nil {
			closeAll()
			return nil, err
		}
		activityOpts := opts.Activity
		if activityOpts.Retention == 0 {
			activityOpts.Retention = time.Duration(cfg.ActivityRetentionDays) * 24 * time.Hour
		}
		activityService = activity.NewService(activityDB, shareService, activityOpts)
		events := smbEvents.Subscribe("activity", 50000)
		loops = append(loops, func(ctx context.Context) { activityService.Record(ctx, events) })
		tasks.Add(scheduler.Task{Name: "activity.cleanup", Daily: 4 * time.Hour, Run: activityService.Prune})
	} else {
		shareService.DisableActivityLog()
	}

	// The ransomware guard watches the same events, and tells shares which
	// users may only read.
	guardService := guard.NewService(database, userService, shareService, osLayer.Samba, alertService)
	if mods.RansomwareGuard {
		if err := guardService.Start(ctx); err != nil {
			closeAll()
			return nil, err
		}
		shareService.SetBlocks(guardService)
		events := smbEvents.Subscribe("guard", 50000)
		loops = append(loops, func(ctx context.Context) { guardService.Run(ctx, events) })
	}

	// SMB brute-force protection: failed logins from the same events.
	banService := guard.NewBans(database, shareService, osLayer.Samba, osLayer.Neighbors, alertService)
	if mods.BruteForce {
		if err := banService.Start(ctx); err != nil {
			closeAll()
			return nil, err
		}
		shareService.SetLogins(banService)
		events := smbEvents.Subscribe("bans", 50000)
		loops = append(loops, func(ctx context.Context) { banService.Run(ctx, events) })
		tasks.Add(scheduler.Task{Name: "smb-bans.expire", Every: time.Minute, Run: banService.Expire})
		tasks.Add(scheduler.Task{Name: "smb-bans.forget", Every: 10 * time.Minute, Run: banService.Purge})
	}
	if mods.SMBEvents() {
		loops = append(loops, func(ctx context.Context) { feedSMBEvents(ctx, osLayer.Audit, smbEvents) })
	}

	var backupService *backup.Service
	if mods.Backup {
		backupService = backup.NewService(database, userService, shareService, groupService, guardService, banService, jobService,
			osLayer, cfg.DataDir, Version)
	}
	// The Linux groups first (a reinstalled machine may lack them), then the
	// Samba config.
	if err := groupService.Reapply(ctx); err != nil {
		slog.WarnContext(ctx, "could not set up the Linux groups at start", "err", err)
	}
	// Write the config once at start: login logging on, ended bans gone.
	if err := shareService.Reapply(ctx); err != nil {
		slog.WarnContext(ctx, "could not write the Samba config at start", "err", err)
	}
	for kind, h := range opts.JobKinds {
		jobService.Register(kind, h)
	}
	for kind, h := range opts.MaintenanceJobKinds {
		jobService.RegisterIn(jobs.LaneMaintenance, kind, h)
	}

	var fileClient *fileworker.Client
	var fileService *files.Service
	if mods.FileManager {
		fileClient = fileworker.NewClient(osLayer.Workers)
		fileService = files.NewService(database, fileClient, userService, shareService, jobService,
			files.Options{MaxUploadBytes: cfg.MaxUploadBytes, UploadExpiry: opts.UploadExpiry})
		fileService.SetRecycle(shareService)
		jobService.Register(files.JobCopy, fileService.RunCopy)
		jobService.Register(files.JobDelete, fileService.RunDelete)
		tasks.Add(scheduler.Task{Name: "uploads.cleanup", Every: time.Hour, Run: func(ctx context.Context) error {
			fileService.SweepUploads(ctx) // abandoned uploads: a day without data
			return nil
		}})
	}

	var replicationService *replication.Service
	if mods.Replication {
		replicationService = replication.NewService(database, shareService, jobService, alertService, osLayer.Replication,
			cfg.ProtectedDirs()...)
		jobService.RegisterIn(jobs.LaneMaintenance, replication.JobKind, replicationService.RunJob)
		tasks.Add(scheduler.Task{Name: "replication.schedule", Every: time.Minute, Run: replicationService.Tick})
	}

	var blockService *block.Service
	if mods.BlockStorage {
		blockService = block.NewService(database, osLayer.Block, shareService, alertService, cfg.ProtectedDirs()...)
		// The kernel forgets its targets at every reboot.
		blockService.Reapply(ctx)
	}

	// Core tasks.
	searchService := search.NewService(database, search.ProviderFuncs{
		All: func(ctx context.Context) ([]search.ShareRef, error) {
			list, _, err := shareService.List(ctx, page.Request{Page: 1, Size: 500})
			if err != nil {
				return nil, err
			}
			out := make([]search.ShareRef, 0, len(list))
			for _, s := range list {
				out = append(out, search.ShareRef{ID: s.ID, Name: s.Name, Path: s.Path})
			}
			return out, nil
		},
		Readable: func(ctx context.Context, userID int64) ([]int64, error) {
			mine, err := shareService.ListForUser(ctx, userID)
			if err != nil {
				return nil, err
			}
			out := make([]int64, 0, len(mine))
			for _, s := range mine {
				if s.Level == shares.LevelRead || s.Level == shares.LevelWrite {
					out = append(out, s.ID)
				}
			}
			return out, nil
		},
	})
	if mods.SMBEvents() {
		ev := smbEvents.Subscribe("search", 4096)
		loops = append(loops, func(ctx context.Context) { searchService.WatchSMBEvents(ctx, ev) })
	}
	tasks.Add(scheduler.Task{Name: "search.reindex", Daily: 3 * time.Hour, Run: searchService.Reindex})
	storageService := storage.NewService(cfg.DataDir, func(ctx context.Context) ([]storage.ShareRef, error) {
		list, _, err := shareService.List(ctx, page.Request{Page: 1, Size: 500})
		if err != nil {
			return nil, err
		}
		out := make([]storage.ShareRef, 0, len(list))
		for _, s := range list {
			out = append(out, storage.ShareRef{ID: s.ID, Name: s.Name, Path: s.Path})
		}
		return out, nil
	})
	tasks.Add(scheduler.Task{Name: "storage.scan", Daily: 4*time.Hour + 30*time.Minute, Run: storageService.RunTask})
	tasks.Add(scheduler.Task{Name: "jobs.cleanup", Every: time.Hour, Run: jobService.Cleanup})
	tasks.Add(scheduler.Task{Name: "shares.recycle-purge", Daily: 2*time.Hour + 45*time.Minute, Run: shareService.PurgeRecycleBins})
	tasks.Add(scheduler.Task{Name: "alerts.cleanup", Daily: 3*time.Hour + 30*time.Minute, Run: alertService.Prune})
	if err := tasks.Load(ctx, database); err != nil {
		closeAll()
		return nil, err
	}

	auditService := audit.NewService(database)

	router := chi.NewRouter()
	router.Use(httpx.RequestID, httpx.RealIP, httpx.AccessLog, httpx.Recover, httpx.SecurityHeaders)

	var api huma.API
	router.Route(APIPrefix, func(r chi.Router) {
		r.Use(httpx.NoStore, auth.Middleware(authService), audit.Middleware(auditService))
		r.NotFound(httpx.NotFound)
		r.MethodNotAllowed(httpx.MethodNotAllowed)

		r.Get("/events", sseHub.ServeHTTP)

		api = humachi.New(r, apiConfig())

		// Each module registers its own routes.
		// Each module registers its own routes; a module that is off
		// has none (its paths answer 404).
		system.Register(api, Version)
		system.RegisterModules(api, moduleList(mods))
		system.RegisterLogs(api, logger.DefaultRing)
		system.RegisterMounts(api, func(ctx context.Context) []string {
			list, _, err := shareService.List(ctx, page.Request{Page: 1, Size: 500})
			if err != nil {
				return nil
			}
			paths := make([]string, 0, len(list))
			for _, s := range list {
				paths = append(paths, s.Path)
			}
			return paths
		})
		audit.Register(api, auditService)
		speedtest.Register(api, r, cfg.DataDir)
		auth.Register(api, authService, cfg.SecureCookies)
		users.Register(api, userService)
		groups.Register(api, groupService)
		shares.Register(api, shareService)
		jobs.Register(api, jobService)
		alerts.Register(api, alertService)
		notify.Register(api, notifyService)
		search.Register(api, searchService)
		storage.Register(api, storageService)
		scheduler.Register(api, tasks)
		if mods.NFS {
			shares.RegisterNFS(api, shareService)
		}
		if mods.FileManager {
			files.Register(api, fileService)
			files.RegisterUploads(r, fileService)
			files.RegisterRecycle(api, fileService)
		}
		if mods.ActivityLog {
			activity.Register(api, activityService)
		}
		if mods.RansomwareGuard {
			guard.Register(api, guardService)
		}
		if mods.BruteForce {
			guard.RegisterBans(api, banService)
		}
		if mods.Sessions {
			sessions.Register(api, osLayer.Samba)
		}
		if mods.Discovery {
			discovery.Register(api, osLayer.Discovery)
		}
		if mods.Backup {
			backup.Register(api, backupService)
		}
		if mods.Replication {
			replication.Register(api, replicationService)
		}
		if mods.BlockStorage {
			block.Register(api, blockService)
		}
	})

	if opts.Static != nil {
		router.Handle("/*", spaHandler(opts.Static))
	}

	// Every job kind is registered by now: resume interrupted jobs and start.
	if err := jobService.Start(ctx); err != nil {
		closeAll()
		return nil, err
	}
	if replicationService != nil {
		// Runs a crash left open (their job was given up or is gone).
		if err := replicationService.Reconcile(ctx); err != nil {
			slog.WarnContext(ctx, "could not close unfinished replication runs", "err", err)
		}
	}

	logger.DefaultRing.SetListener(func(e logger.LogEntry) {
		sseHub.PublishTo("log", e, func(p *httpx.Principal) bool { return p != nil && p.Can(rbac.SystemRead) })
	})
	tasks.Start()
	background, cancel := context.WithCancel(context.Background())
	var running sync.WaitGroup
	for _, loop := range loops {
		running.Add(1)
		go func() { defer running.Done(); loop(background) }()
	}
	stop := func() {
		logger.DefaultRing.SetListener(nil)
		tasks.Stop()
		cancel()
		running.Wait()
	}

	slog.Info("application ready", "version", Version, "data_dir", cfg.DataDir, "modules", moduleList(mods))
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
func (a *App) SweepUploads(ctx context.Context) {
	if a.files != nil {
		a.files.SweepUploads(ctx)
	}
}

// Close releases resources.
func (a *App) Close() error {
	a.stop()
	// Running jobs pause and resume at the next start.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a.Jobs.Stop(ctx)
	if a.activityDB != nil {
		a.activityDB.Close()
	}
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
