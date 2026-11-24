package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"netshare-hub/internal/sys"
)

// Replication operations (step 4.1). replication.run names a share and a
// peer id or a local folder; the agent finds the share's folder, the
// peer's address and its pinned host key itself.
const (
	opReplicationStatus = "replication.status"
	opReplicate         = "replication.run"
	opPublicKey         = "replication.public-key"
	opSetPeers          = "replication.set-peers"
	opHostKey           = "replication.host-key"
	opSetReceivers      = "replication.set-receivers"
	codeReplication     = "replication_failed"
)

type setPeersRequest struct {
	Peers []sys.Peer `json:"peers"`
}

type setReceiversRequest struct {
	Receivers []sys.Receiver `json:"receivers"`
}

type keyResponse struct {
	Key string `json:"key"`
}

// replicateLine is one line of the replication.run stream: progress while
// it copies, then one line with the stats or the error.
type replicateLine struct {
	Progress *sys.ReplicationProgress `json:"progress,omitempty"`
	Stats    *sys.ReplicationStats    `json:"stats,omitempty"`
	Error    *errorResponse           `json:"error,omitempty"`
}

func registerReplication(s *Server, system sys.System) {
	if system.Replication == nil {
		return
	}
	r := system.Replication
	register(s, opReplicationStatus, func(ctx context.Context, _ struct{}) (any, error) {
		return r.ReplicationStatus(ctx)
	}, nil)
	register(s, opPublicKey, func(ctx context.Context, _ struct{}) (any, error) {
		key, err := r.PublicKey(ctx)
		return keyResponse{Key: key}, err
	}, nil)
	register(s, opHostKey, func(ctx context.Context, _ struct{}) (any, error) {
		key, err := r.HostKey(ctx)
		return keyResponse{Key: key}, err
	}, nil)
	register(s, opSetPeers, func(ctx context.Context, req setPeersRequest) (any, error) {
		if len(req.Peers) > 1000 {
			return nil, sys.ErrInvalid
		}
		return nil, r.SetPeers(ctx, req.Peers)
	}, func(req setPeersRequest) []any {
		hosts := make([]string, len(req.Peers))
		for i, p := range req.Peers {
			hosts[i] = p.ID + "=" + p.Host
		}
		return []any{"peers", hosts}
	})
	register(s, opSetReceivers, func(ctx context.Context, req setReceiversRequest) (any, error) {
		if len(req.Receivers) > 1000 {
			return nil, sys.ErrInvalid
		}
		return nil, r.SetReceivers(ctx, req.Receivers)
	}, func(req setReceiversRequest) []any {
		paths := make([]string, len(req.Receivers))
		for i, rc := range req.Receivers {
			paths[i] = rc.ID + "=" + rc.Path
		}
		return []any{"receivers", paths}
	})
	if s.features.offers(opReplicate) {
		s.mux.HandleFunc("POST "+pathPrefix+opReplicate, s.replicate)
	}
}

// replicate streams one rsync run. Like fix-permissions it takes no lock:
// it can run for hours and changes no accounts or config.
func (s *Server) replicate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	peer, ok := s.authorize(r)
	if !ok {
		slog.WarnContext(r.Context(), "agent call refused: caller not allowed", "op", opReplicate, "peer_uid", peer.UID, "peer_pid", peer.PID)
		writeJSON(w, http.StatusForbidden, errorResponse{Code: codeForbidden, Message: "caller is not allowed to use the agent"})
		return
	}
	var req sys.ReplicationRun
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: codeBadRequest, Message: "malformed request"})
		return
	}
	attrs := []any{"op", opReplicate, "peer_uid", peer.UID, "peer_pid", peer.PID, "share", req.Share,
		"local_path", req.LocalPath, "peer", req.Peer, "mirror", req.Mirror, "dry_run", req.DryRun}
	slog.InfoContext(r.Context(), "agent call", append(attrs, "stream", "started")...)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flush := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	last := time.Time{}
	stats, err := s.system.Replication.Replicate(r.Context(), req, func(p sys.ReplicationProgress) {
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		_ = enc.Encode(replicateLine{Progress: &p})
		_ = flush.Flush()
	})
	attrs = append(attrs, "duration_ms", time.Since(start).Milliseconds())
	if err != nil {
		_, body := encodeReplicationError(err)
		slog.WarnContext(r.Context(), "agent call failed", append(attrs, "code", body.Code, "err", err)...)
		_ = enc.Encode(replicateLine{Stats: &stats, Error: &body})
		return
	}
	slog.InfoContext(r.Context(), "agent call", append(attrs, "files", stats.Transferred, "bytes", stats.Bytes)...)
	_ = enc.Encode(replicateLine{Stats: &stats})
}

func encodeReplicationError(err error) (int, errorResponse) {
	var re *sys.ReplicationError
	if errors.As(err, &re) {
		return http.StatusUnprocessableEntity, errorResponse{Code: codeReplication, Message: re.Message, ExitCode: re.ExitCode}
	}
	return encodeError(err)
}

// --- client ---

func (c *Client) ReplicationStatus(ctx context.Context) (sys.ReplicationStatus, error) {
	var res sys.ReplicationStatus
	err := c.call(ctx, opReplicationStatus, struct{}{}, &res)
	return res, err
}

func (c *Client) PublicKey(ctx context.Context) (string, error) {
	var res keyResponse
	err := c.call(ctx, opPublicKey, struct{}{}, &res)
	return res.Key, err
}

func (c *Client) HostKey(ctx context.Context) (string, error) {
	var res keyResponse
	err := c.call(ctx, opHostKey, struct{}{}, &res)
	return res.Key, err
}

func (c *Client) SetPeers(ctx context.Context, peers []sys.Peer) error {
	if peers == nil {
		peers = []sys.Peer{}
	}
	return c.call(ctx, opSetPeers, setPeersRequest{Peers: peers}, nil)
}

func (c *Client) SetReceivers(ctx context.Context, receivers []sys.Receiver) error {
	if receivers == nil {
		receivers = []sys.Receiver{}
	}
	return c.call(ctx, opSetReceivers, setReceiversRequest{Receivers: receivers}, nil)
}

// Replicate runs on the long client (no overall timeout): cancelling ctx
// closes the connection, which stops rsync on the agent's side.
func (c *Client) Replicate(ctx context.Context, run sys.ReplicationRun, progress func(sys.ReplicationProgress)) (sys.ReplicationStats, error) {
	if run.Excludes == nil {
		run.Excludes = []string{}
	}
	body, err := json.Marshal(run)
	if err != nil {
		return sys.ReplicationStats{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+pathPrefix+opReplicate, bytes.NewReader(body))
	if err != nil {
		return sys.ReplicationStats{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.long.Do(req)
	if err != nil {
		return sys.ReplicationStats{}, fmt.Errorf("agent unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var failure errorResponse
		if err := json.NewDecoder(res.Body).Decode(&failure); err != nil || failure.Code == "" {
			return sys.ReplicationStats{}, fmt.Errorf("agent: unexpected response (status %d)", res.StatusCode)
		}
		return sys.ReplicationStats{}, decodeError(failure)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), maxRequestBytes)
	for sc.Scan() {
		var line replicateLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return sys.ReplicationStats{}, fmt.Errorf("agent: decode replication stream: %w", err)
		}
		switch {
		case line.Progress != nil:
			if progress != nil {
				progress(*line.Progress)
			}
		case line.Error != nil:
			stats := sys.ReplicationStats{}
			if line.Stats != nil {
				stats = *line.Stats
			}
			if line.Error.Code == codeReplication {
				return stats, &sys.ReplicationError{ExitCode: line.Error.ExitCode, Message: line.Error.Message}
			}
			return stats, decodeError(*line.Error)
		case line.Stats != nil:
			return *line.Stats, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return sys.ReplicationStats{}, err
	}
	return sys.ReplicationStats{}, errors.New("agent: the replication stream ended without a result")
}
