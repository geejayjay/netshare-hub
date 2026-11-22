package fileworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"netshare-hub/internal/platform/peercred"
)

// listenerFD is where the root side hands over the listening socket. The
// root side creates the socket (so its permissions are right from the first
// instant) and the worker only inherits it.
const listenerFD = 3

// Run is the worker process. It is started by the root side, already
// switched to the user's identity, with its Config on stdin and its
// listening socket on file descriptor 3. It returns when the worker has been
// idle for the configured time.
func Run(stdin io.Reader) error {
	if os.Geteuid() == 0 {
		// The whole point of a worker is to NOT be root.
		return errors.New("a file worker must never run as root")
	}
	var cfg Config
	if err := json.NewDecoder(stdin).Decode(&cfg); err != nil {
		return fmt.Errorf("read worker config: %w", err)
	}
	listener, err := net.FileListener(os.NewFile(listenerFD, "listener"))
	if err != nil {
		return fmt.Errorf("inherit listener: %w", err)
	}
	prepareProcess()
	return Serve(listener, cfg, peerAuthorizer(cfg.AllowUID))
}

// Authorizer decides whether a request's caller may use the worker.
type Authorizer func(r *http.Request) bool

// peerAuthorizer accepts only root and the web server's user, as reported
// by the kernel for the connection.
func peerAuthorizer(allowUID int) Authorizer {
	return func(r *http.Request) bool {
		peer, ok := peercred.FromContext(r.Context())
		return ok && (peer.UID == 0 || peer.UID == allowUID)
	}
}

// Serve runs a worker on a listener until it has been idle for
// cfg.IdleSeconds. A request that is still running (a long download) keeps
// the worker alive.
func Serve(listener net.Listener, cfg Config, authorize Authorizer) error {
	idle := time.Duration(cfg.IdleSeconds) * time.Second
	if idle <= 0 {
		idle = 5 * time.Minute
	}

	var active atomic.Int64
	var lastDone atomic.Int64
	lastDone.Store(time.Now().UnixNano())

	files := NewHandler(cfg.Roots)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorize(r) {
			writeJSON(w, http.StatusForbidden, errorResponse{codeForbidden, "caller is not allowed to use this worker"})
			return
		}
		active.Add(1)
		defer func() {
			lastDone.Store(time.Now().UnixNano())
			active.Add(-1)
		}()
		files.ServeHTTP(w, r)
	})

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext:       peercred.ConnContext,
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(min(idle/4+time.Millisecond, 15*time.Second))
		defer ticker.Stop()
		for range ticker.C {
			if active.Load() == 0 && time.Since(time.Unix(0, lastDone.Load())) >= idle {
				slog.Info("file worker idle, exiting")
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = server.Shutdown(ctx)
				return
			}
		}
	}()

	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-stopped
		return nil
	}
	return err
}
