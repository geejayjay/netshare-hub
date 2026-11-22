//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"netshare-hub/internal/platform/peercred"
	"netshare-hub/internal/sys"
)

// Serve runs the agent on a unix socket until ctx is cancelled.
//
// Who may call it is enforced twice, by the kernel both times:
//  1. File permissions: the socket is root:<allowUser's group>, mode 0660,
//     inside a 0750 directory, so other local users cannot even connect.
//  2. Peer credentials: for every connection the kernel tells us the caller's
//     real UID (SO_PEERCRED). Only root and allowUser are served. A process
//     cannot fake this value.
func Serve(ctx context.Context, socket, allowUser string, system sys.System, version string) error {
	if os.Geteuid() != 0 {
		return errors.New("the agent must run as root")
	}
	allowed, err := user.Lookup(allowUser)
	if err != nil {
		return fmt.Errorf("agent_user %q does not exist (create it, or set agent_user in the config): %w", allowUser, err)
	}
	uid, _ := strconv.Atoi(allowed.Uid)
	gid, _ := strconv.Atoi(allowed.Gid)

	listener, err := listen(socket, gid)
	if err != nil {
		return err
	}
	defer os.Remove(socket)

	authorize := func(r *http.Request) (Peer, bool) {
		peer, ok := peercred.FromContext(r.Context())
		if !ok {
			return Peer{UID: -1}, false
		}
		return Peer{UID: peer.UID, PID: peer.PID}, peer.UID == 0 || peer.UID == uid
	}

	server := &http.Server{
		Handler:           NewServer(system, authorize, version),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// Ask the kernel who is on the other end as soon as a connection opens.
		ConnContext: peercred.ConnContext,
	}

	failed := make(chan error, 1)
	go func() {
		slog.Info("agent listening", "socket", socket, "allow_user", allowUser, "allow_uid", uid)
		if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// listen creates the socket with tight permissions from the first instant.
func listen(socket string, gid int) (net.Listener, error) {
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	// The directory must be a real directory. If it were a symbolic link,
	// the chown below would hand someone else's folder to the web user.
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("socket directory %s must be a real directory, not a link", dir)
	}
	if err := os.Chown(dir, 0, gid); err != nil {
		return nil, fmt.Errorf("secure socket directory: %w", err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		return nil, fmt.Errorf("secure socket directory: %w", err)
	}

	// Remove a socket left behind by a previous run, but never anything else.
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&fs.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket; refusing to replace it", socket)
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	}

	// With this umask the socket is created as 0600, so there is no moment
	// at which it is more open than intended.
	old := syscall.Umask(0o177)
	listener, err := net.Listen("unix", socket)
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socket, err)
	}
	if err := os.Chown(socket, 0, gid); err == nil {
		err = os.Chmod(socket, 0o660)
	}
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("secure socket: %w", err)
	}
	return listener, nil
}
