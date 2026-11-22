// Command netshare-hub is the NetShare Hub server and its admin CLI.
//
//	netshare-hub serve [--config file.toml]   run the web server (default)
//	netshare-hub agent [--config file.toml]   run the root agent (see internal/agent)
//	netshare-hub openapi [--out file.json]    print the OpenAPI spec
//	netshare-hub version                      print the version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"netshare-hub/internal/agent"
	"netshare-hub/internal/app"
	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/platform/config"
	"netshare-hub/internal/platform/logger"
	"netshare-hub/internal/sys/sysexec"
	"netshare-hub/web"
)

func main() {
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}

	var err error
	switch command {
	case "serve":
		err = serve(args)
	case "agent":
		err = runAgent(args)
	case "fileworker":
		// Internal: started by the root side as a user, never by hand.
		slog.SetDefault(logger.New(logger.Options{Level: "info", Output: os.Stderr}))
		err = fileworker.Run(os.Stdin)
	case "openapi":
		err = openapi(args)
	case "version":
		fmt.Println(app.Version)
	default:
		err = fmt.Errorf("unknown command %q (use: serve, agent, openapi, version)", command)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", os.Getenv("NSH_CONFIG"), "path to the TOML config file")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	slog.SetDefault(logger.New(logger.Options{Level: cfg.LogLevel, Format: cfg.LogFormat}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, app.Options{Static: web.Dist()})
	if err != nil {
		return err
	}
	defer application.Close()

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           application.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	failed := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Listen)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// runAgent runs the privileged half of NetShare Hub. It performs the system
// operations the web server asks for and is never exposed to the network.
func runAgent(args []string) error {
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	configPath := flags.String("config", os.Getenv("NSH_CONFIG"), "path to the TOML config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	slog.SetDefault(logger.New(logger.Options{Level: cfg.LogLevel, Format: cfg.LogFormat}))

	system, err := sysexec.New(sysexec.Options{Protected: cfg.ProtectedDirs(), WebUser: cfg.AgentUser})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return agent.Serve(ctx, cfg.AgentSocket, cfg.AgentUser, system, app.Version)
}

// openapi writes the API spec. The frontend's TypeScript types are generated
// from it, so the Go types stay the single source of truth.
func openapi(args []string) error {
	flags := flag.NewFlagSet("openapi", flag.ContinueOnError)
	out := flags.String("out", "", "write to this file instead of stdout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	slog.SetDefault(logger.New(logger.Options{Level: "error", Output: os.Stderr}))

	// Build the app against a throwaway database just to collect its routes.
	dir, err := os.MkdirTemp("", "netshare-hub-openapi")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Sys = "fake" // only the routes are needed, never the real system
	application, err := app.New(context.Background(), cfg, app.Options{})
	if err != nil {
		return err
	}
	defer application.Close()

	spec, err := json.MarshalIndent(application.API.OpenAPI(), "", "  ")
	if err != nil {
		return err
	}
	spec = append(spec, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(spec)
		return err
	}
	return os.WriteFile(*out, spec, 0o644)
}
