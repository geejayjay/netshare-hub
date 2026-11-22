//go:build linux

// Package peercred asks the kernel who is on the other end of a unix socket.
// The answer cannot be faked by the peer, which makes it a sound basis for
// deciding which local process may use a privileged socket.
package peercred

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// Peer identifies the process on the other end of a unix socket.
type Peer struct {
	UID int
	PID int
}

// Of reads the peer's identity from the kernel (SO_PEERCRED).
func Of(c net.Conn) (Peer, error) {
	unixConn, ok := c.(*net.UnixConn)
	if !ok {
		return Peer{}, errors.New("not a unix connection")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if credErr != nil {
		return Peer{}, credErr
	}
	return Peer{UID: int(cred.Uid), PID: int(cred.Pid)}, nil
}

type key struct{}

// ConnContext is an http.Server.ConnContext that records the peer of every
// new connection, for FromContext to read in handlers.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if peer, err := Of(c); err == nil {
		return context.WithValue(ctx, key{}, peer)
	}
	return ctx
}

// FromContext returns the peer recorded by ConnContext.
func FromContext(ctx context.Context) (Peer, bool) {
	peer, ok := ctx.Value(key{}).(Peer)
	return peer, ok
}
