//go:build !linux

// Package peercred asks the kernel who is on the other end of a unix socket.
// It only works on Linux; elsewhere no peer is ever reported, so callers
// that require one refuse every connection.
package peercred

import (
	"context"
	"errors"
	"net"
)

// Peer identifies the process on the other end of a unix socket.
type Peer struct {
	UID int
	PID int
}

func Of(net.Conn) (Peer, error) {
	return Peer{}, errors.New("peer credentials need Linux")
}

func ConnContext(ctx context.Context, _ net.Conn) context.Context { return ctx }

func FromContext(context.Context) (Peer, bool) { return Peer{}, false }
