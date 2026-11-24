package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"netshare-hub/internal/sys"
)

// Peer identifies the process on the other end of the socket.
type Peer struct {
	UID int
	PID int
}

// Authorizer decides whether a caller may use the agent. The real one reads
// the caller's identity from the kernel (see listen_linux.go).
type Authorizer func(r *http.Request) (Peer, bool)

// Server is the agent's request handler. It is independent of the transport,
// so it can be tested without a unix socket.
type Server struct {
	system    sys.System
	authorize Authorizer
	features  Features
	version   string
	mux       *http.ServeMux

	// mu runs one system change at a time. The system tools are not built
	// for concurrent use (two useradd runs fight over /etc/passwd locks).
	mu sync.Mutex
}

// NewServer returns the agent handler in front of the real system layer,
// with every operation offered.
func NewServer(system sys.System, authorize Authorizer, version string) *Server {
	return NewServerWith(system, authorize, version, AllFeatures())
}

// NewServerWith offers only the operations of the features that are on.
func NewServerWith(system sys.System, authorize Authorizer, version string, features Features) *Server {
	s := &Server{system: system, authorize: authorize, version: version, features: features, mux: http.NewServeMux()}

	register(s, opPing, func(context.Context, struct{}) (any, error) {
		return pingResponse{Version: version}, nil
	}, nil)

	register(s, opCreateUser, func(ctx context.Context, r createUserRequest) (any, error) {
		return nil, system.Accounts.CreateUser(ctx, r.Name, r.Password)
	}, func(r createUserRequest) []any { return []any{"account", r.Name} })

	register(s, opSetPassword, func(ctx context.Context, r setPasswordRequest) (any, error) {
		return nil, system.Accounts.SetPassword(ctx, r.Name, r.Password)
	}, func(r setPasswordRequest) []any { return []any{"account", r.Name} })

	register(s, opSetEnabled, func(ctx context.Context, r setEnabledRequest) (any, error) {
		return nil, system.Accounts.SetEnabled(ctx, r.Name, r.Enabled)
	}, func(r setEnabledRequest) []any { return []any{"account", r.Name, "enabled", r.Enabled} })

	register(s, opDeleteUser, func(ctx context.Context, r deleteUserRequest) (any, error) {
		return nil, system.Accounts.DeleteUser(ctx, r.Name)
	}, func(r deleteUserRequest) []any { return []any{"account", r.Name} })

	register(s, opSMBHashes, func(ctx context.Context, r smbHashesRequest) (any, error) {
		if len(r.Names) > 10000 {
			return nil, sys.ErrInvalid
		}
		hashes, err := system.Accounts.SMBHashes(ctx, r.Names)
		return smbHashesResponse{Hashes: hashes}, err
	}, func(r smbHashesRequest) []any { return []any{"accounts", len(r.Names)} })

	register(s, opCreateFromHash, func(ctx context.Context, r createFromHashRequest) (any, error) {
		return nil, system.Accounts.CreateUserFromHash(ctx, r.Name, r.Hash)
	}, func(r createFromHashRequest) []any { return []any{"account", r.Name} })

	register(s, opApplyConfig, func(ctx context.Context, r applyConfigRequest) (any, error) {
		return nil, system.Samba.ApplyConfig(ctx, sys.SambaConfig{Server: r.Server, Shares: r.Shares, Audit: r.Audit,
			LoginAudit: r.LoginAudit, Banned: r.Banned})
	}, func(r applyConfigRequest) []any {
		names := make([]string, len(r.Shares))
		for i, sh := range r.Shares {
			names[i] = sh.Name
		}
		return []any{"shares", names, "server", r.Server, "audit", r.Audit, "login_audit", r.LoginAudit, "banned", r.Banned}
	})

	// Long-running (a whole share's tree), so it does not take the lock
	// that serializes the other operations: see concurrent below.
	register(s, opFixPermissions, func(ctx context.Context, r fixPermissionsRequest) (any, error) {
		return system.Samba.FixPermissions(ctx, r.Share)
	}, func(r fixPermissionsRequest) []any { return []any{"share", r.Share} })

	if features.offers(opAuditEvents) {
		s.mux.HandleFunc("POST "+pathPrefix+opAuditEvents, s.auditEvents)
	}

	register(s, opPurgeRecycle, func(ctx context.Context, r fixPermissionsRequest) (any, error) {
		return system.Samba.PurgeRecycle(ctx, r.Share)
	}, func(r fixPermissionsRequest) []any { return []any{"share", r.Share} })

	register(s, opSessions, func(ctx context.Context, _ struct{}) (any, error) {
		sessions, err := system.Samba.Sessions(ctx)
		return sessionsResponse{Sessions: sessions}, err
	}, nil)

	register(s, opDisconnectSession, func(ctx context.Context, r sessionRequest) (any, error) {
		return nil, system.Samba.DisconnectSession(ctx, r.ID)
	}, func(r sessionRequest) []any { return []any{"session", r.ID} })

	register(s, opDisconnectClient, func(ctx context.Context, r disconnectRequest) (any, error) {
		ip, err := netip.ParseAddr(r.IP)
		if err != nil {
			return nil, sys.ErrInvalid
		}
		return nil, system.Samba.DisconnectClient(ctx, ip)
	}, func(r disconnectRequest) []any { return []any{"ip", r.IP} })

	register(s, opDiscoveryStatus, func(ctx context.Context, _ struct{}) (any, error) {
		if system.Discovery == nil {
			return sys.DiscoveryStatus{}, nil
		}
		return system.Discovery.DiscoveryStatus(ctx)
	}, nil)

	register(s, opSetDiscovery, func(ctx context.Context, r setDiscoveryRequest) (any, error) {
		if system.Discovery == nil {
			return nil, sys.ErrNotFound
		}
		return nil, system.Discovery.SetDiscovery(ctx, r.Service, r.On)
	}, func(r setDiscoveryRequest) []any { return []any{"service", string(r.Service), "on", r.On} })

	register(s, opNFSStatus, func(ctx context.Context, _ struct{}) (any, error) {
		if system.NFS == nil {
			return sys.NFSStatus{Reason: "NFS is not offered on this machine.", Exports: []string{}}, nil
		}
		return system.NFS.NFSStatus(ctx)
	}, nil)

	register(s, opApplyExports, func(ctx context.Context, r applyExportsRequest) (any, error) {
		if system.NFS == nil {
			if len(r.Exports) == 0 {
				return nil, nil
			}
			return nil, sys.ErrNotFound
		}
		if len(r.Exports) > 1000 {
			return nil, sys.ErrInvalid
		}
		return nil, system.NFS.ApplyExports(ctx, r.Exports)
	}, func(r applyExportsRequest) []any {
		names := make([]string, len(r.Exports))
		for i, e := range r.Exports {
			names[i] = e.Share
		}
		return []any{"exports", names}
	})

	register(s, opApplyGroups, func(ctx context.Context, r applyGroupsRequest) (any, error) {
		if system.Groups == nil {
			return nil, sys.ErrNotFound
		}
		if len(r.Groups) > 10000 {
			return nil, sys.ErrInvalid
		}
		return nil, system.Groups.ApplyGroups(ctx, r.Groups)
	}, func(r applyGroupsRequest) []any {
		names := make([]string, len(r.Groups))
		for i, g := range r.Groups {
			names[i] = g.Name
		}
		return []any{"groups", names}
	})

	registerReplication(s, system)
	registerBlock(s, system)

	register(s, opResolveDir, func(ctx context.Context, r pathRequest) (any, error) {
		// Only answer for paths that could be shared at all, so the agent is
		// not a way to probe which files exist anywhere on the system.
		if !sys.ValidSharePath(r.Path) || !sys.SharablePath(r.Path) {
			return nil, sys.ErrInvalid
		}
		real, err := system.Files.ResolveDir(ctx, r.Path)
		return pathResponse{Path: real}, err
	}, func(r pathRequest) []any { return []any{"path", r.Path} })

	register(s, opCreateShareDir, func(ctx context.Context, r pathRequest) (any, error) {
		if !sys.ValidSharePath(r.Path) || !sys.SharablePath(r.Path) {
			return nil, sys.ErrInvalid
		}
		real, err := system.Files.CreateShareDir(ctx, r.Path)
		return pathResponse{Path: real}, err
	}, func(r pathRequest) []any { return []any{"path", r.Path} })

	// Starting a file worker is the one operation that launches a process as
	// another user. The system layer only does it for accounts NetShare Hub
	// created, and decides itself which shares the worker may reach.
	register(s, opWorker, func(ctx context.Context, r workerRequest) (any, error) {
		return system.Workers.Endpoint(ctx, r.Name, r.Background)
	}, func(r workerRequest) []any { return []any{"account", r.Name, "background", r.Background} })

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// concurrent operations run without the lock. Only operations that change
// nothing but file permissions (no accounts, no config) belong here: a
// permission walk can take hours, and must not block everything else.
var concurrent = map[string]bool{opFixPermissions: true, opPurgeRecycle: true}

// register adds one operation. Every operation goes through the same steps:
// check the caller, read exactly the expected JSON, run it (one at a time),
// and write an audit line. describe returns the fields to audit; it must
// never include a secret.
func register[Req any](s *Server, op string, run func(context.Context, Req) (any, error), describe func(Req) []any) {
	if !s.features.offers(op) {
		return // the module is off: the operation does not exist
	}
	s.mux.HandleFunc("POST "+pathPrefix+op, func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		peer, ok := s.authorize(r)
		if !ok {
			slog.WarnContext(r.Context(), "agent call refused: caller not allowed", "op", op, "peer_uid", peer.UID, "peer_pid", peer.PID)
			writeJSON(w, http.StatusForbidden, errorResponse{Code: codeForbidden, Message: "caller is not allowed to use the agent"})
			return
		}

		var req Req
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			slog.WarnContext(r.Context(), "agent call refused: bad request", "op", op, "peer_uid", peer.UID, "err", err)
			writeJSON(w, http.StatusBadRequest, errorResponse{Code: codeBadRequest, Message: "malformed request"})
			return
		}

		if !concurrent[op] {
			s.mu.Lock()
		}
		res, err := run(r.Context(), req)
		if !concurrent[op] {
			s.mu.Unlock()
		}

		// The audit trail: who asked for what, and how it ended.
		attrs := []any{"op", op, "peer_uid", peer.UID, "peer_pid", peer.PID, "duration_ms", time.Since(start).Milliseconds()}
		if describe != nil {
			attrs = append(attrs, describe(req)...)
		}
		if err != nil {
			status, body := encodeError(err)
			level := slog.LevelWarn
			if status >= 500 && !errors.Is(err, sys.ErrReloadFailed) {
				level = slog.LevelError
			}
			slog.Log(r.Context(), level, "agent call failed", append(attrs, "code", body.Code, "err", err)...)
			writeJSON(w, status, body)
			return
		}
		if op != opPing {
			slog.InfoContext(r.Context(), "agent call", attrs...)
		}
		if res == nil {
			res = struct{}{}
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// auditEvents streams Samba's audit events to the caller. It takes no
// arguments and changes nothing, so it does not take the lock.
func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	peer, ok := s.authorize(r)
	if !ok {
		slog.WarnContext(r.Context(), "agent call refused: caller not allowed", "op", opAuditEvents, "peer_uid", peer.UID, "peer_pid", peer.PID)
		writeJSON(w, http.StatusForbidden, errorResponse{Code: codeForbidden, Message: "caller is not allowed to use the agent"})
		return
	}
	if s.system.Audit == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Code: codeNotFound, Message: "no audit source"})
		return
	}
	events, err := s.system.Audit.Events(r.Context())
	if err != nil {
		status, body := encodeError(err)
		slog.WarnContext(r.Context(), "agent call failed", "op", opAuditEvents, "peer_uid", peer.UID, "err", err)
		writeJSON(w, status, body)
		return
	}
	slog.InfoContext(r.Context(), "agent call", "op", opAuditEvents, "peer_uid", peer.UID, "peer_pid", peer.PID, "stream", "started")
	defer slog.InfoContext(r.Context(), "agent call", "op", opAuditEvents, "peer_uid", peer.UID, "stream", "ended")

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flush := http.NewResponseController(w)
	_ = flush.Flush()
	// Flush when no more events are waiting, not after each one: a burst
	// goes out in few writes.
	enc := json.NewEncoder(w)
	for ev := range events {
		if err := enc.Encode(ev); err != nil {
			return // the caller went away; the source ends with r.Context()
		}
		if len(events) == 0 {
			_ = flush.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
