package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"netshare-hub/internal/sys"
)

// scanShare streams a live share's entries (opScanShare). It only reads,
// so it does not take the lock: a scan of a big share takes long and must
// not hold up the system changes.
func (s *Server) scanShare(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	peer, ok := s.authorize(r)
	if !ok {
		slog.WarnContext(r.Context(), "agent call refused: caller not allowed", "op", opScanShare, "peer_uid", peer.UID, "peer_pid", peer.PID)
		writeJSON(w, http.StatusForbidden, errorResponse{Code: codeForbidden, Message: "caller is not allowed to use the agent"})
		return
	}
	var req fixPermissionsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		slog.WarnContext(r.Context(), "agent call refused: bad request", "op", opScanShare, "peer_uid", peer.UID, "err", err)
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: codeBadRequest, Message: "malformed request"})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	bw := bufio.NewWriterSize(w, 64<<10)
	enc := json.NewEncoder(bw)
	entries := 0
	err := s.system.Samba.ScanShare(r.Context(), req.Share, func(e sys.ShareEntry) error {
		entries++
		return enc.Encode(scanLine{Entry: &e})
	})

	attrs := []any{"op", opScanShare, "peer_uid", peer.UID, "peer_pid", peer.PID,
		"duration_ms", time.Since(start).Milliseconds(), "share", req.Share, "entries", entries}
	last := scanLine{Done: true}
	if err != nil {
		_, body := encodeError(err)
		last = scanLine{Error: &body}
		slog.WarnContext(r.Context(), "agent call failed", append(attrs, "code", body.Code, "err", err)...)
	} else {
		slog.InfoContext(r.Context(), "agent call", attrs...)
	}
	_ = enc.Encode(last)
	_ = bw.Flush()
}

// ScanShare streams a share's entries from the agent. Like FixPermissions
// it has no overall timeout; it ends when the scan does, fn fails, or ctx
// is done (closing the connection stops the agent's walk).
func (c *Client) ScanShare(ctx context.Context, share string, fn func(sys.ShareEntry) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, err := json.Marshal(fixPermissionsRequest{Share: share})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+pathPrefix+opScanShare, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.long.Do(req)
	if err != nil {
		return fmt.Errorf("agent unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var failure errorResponse
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if err := json.Unmarshal(raw, &failure); err != nil || failure.Code == "" {
			return fmt.Errorf("agent: unexpected response (status %d)", res.StatusCode)
		}
		return decodeError(failure)
	}

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var line scanLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return fmt.Errorf("agent: decode scan: %w", err)
		}
		switch {
		case line.Error != nil:
			return decodeError(*line.Error)
		case line.Done:
			return nil
		case line.Entry != nil:
			if err := fn(*line.Entry); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("agent: scan: %w", err)
	}
	return errors.New("agent: scan ended early")
}

// StatFiles looks up files of a share in the agent. Hashing many big files
// takes long, so there is no overall timeout.
func (c *Client) StatFiles(ctx context.Context, share string, paths []string, hash bool) ([]sys.ShareFile, error) {
	if paths == nil {
		paths = []string{}
	}
	var res statFilesResponse
	err := c.callWith(c.long, ctx, opStatFiles, statFilesRequest{Share: share, Paths: paths, Hash: hash}, &res)
	return res.Files, err
}
