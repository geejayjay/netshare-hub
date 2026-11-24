package replication

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// See the package doc for the protocol.

const (
	pairPath   = "/api/v1/replication/pair"
	pairLabel  = "netshare-hub pair v1"
	pairedLabl = "netshare-hub paired v1"
)

var secretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewPairingCode makes a new one-time code for a receiving folder (the old
// one stops working). Pairing again replaces the sender's key.
func (s *Service) NewPairingCode(ctx context.Context, id int64) (string, error) {
	raw := make([]byte, 20) // 160 bits
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	secret := strings.ToLower(secretEncoding.EncodeToString(raw))
	res, err := s.repo.db.Write.ExecContext(ctx, "UPDATE replication_receivers SET pair_secret = ?, pair_expires = ? WHERE id = ?",
		secret, s.now().Add(pairingValidity).Unix(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errs.NotFound(CodeReceiverNotFound, "This receiving folder does not exist.")
	}
	return sysReceiver(id) + "-" + secret, nil
}

func pairMAC(secret string, parts ...string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(m.Sum(nil))
}

// ReplicationPairRequest is what a sender sends to pair (POST /replication/pair).
type ReplicationPairRequest struct {
	Receiver  string `json:"receiver" maxLength:"32" doc:"The receiving folder, from the pairing code."`
	Nonce     string `json:"nonce" minLength:"32" maxLength:"64"`
	PublicKey string `json:"publicKey" maxLength:"200"`
	MAC       string `json:"mac" minLength:"64" maxLength:"64" doc:"HMAC-SHA256 over the request, keyed with the code's secret."`
}

// ReplicationPairResponse is the receiver's answer.
type ReplicationPairResponse struct {
	HostKey string `json:"hostKey"`
	MAC     string `json:"mac" doc:"HMAC-SHA256 over the whole exchange, keyed with the code's secret."`
}

var hexNonce = regexp.MustCompile(`^[0-9a-f]{32,64}$`)

func errPairRefused() error {
	return errs.Forbidden(CodePairingRefused, "Pairing refused: the code is wrong, already used or expired.")
}

// Pair is the receiving side of pairing. It is reachable without signing
// in (the code is the credential), so it is rate-limited per address and
// says nothing about why a code was refused.
func (s *Service) Pair(ctx context.Context, in ReplicationPairRequest) (ReplicationPairResponse, error) {
	ip := httpx.ClientIP(ctx)
	if blocked, retry := s.limiter.Blocked(ip); blocked {
		return ReplicationPairResponse{}, errs.RateLimited("Too many pairing attempts. Please wait and try again.", retry)
	}
	refuse := func(why string) (ReplicationPairResponse, error) {
		s.limiter.Fail(ip)
		slog.WarnContext(ctx, "replication pairing refused", "ip", ip, "receiver", in.Receiver, "why", why)
		return ReplicationPairResponse{}, errPairRefused()
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(in.Receiver, "r"), 10, 64)
	if !strings.HasPrefix(in.Receiver, "r") || err != nil || !hexNonce.MatchString(in.Nonce) || !sys.ValidSSHKey(in.PublicKey) {
		return refuse("malformed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.repo.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return ReplicationPairResponse{}, err
	}
	defer tx.Rollback()
	var secret string
	var expires int64
	if err := tx.QueryRowContext(ctx, "SELECT pair_secret, pair_expires FROM replication_receivers WHERE id = ?", id).
		Scan(&secret, &expires); err != nil {
		return refuse("no such receiver")
	}
	if secret == "" || s.now().Unix() > expires {
		return refuse("no code, or expired")
	}
	want := pairMAC(secret, pairLabel, in.Receiver, in.Nonce, in.PublicKey)
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(in.MAC))) {
		return refuse("wrong code")
	}
	hostKey, err := s.system.HostKey(ctx)
	if err != nil {
		return ReplicationPairResponse{}, unavailable(err)
	}
	// The code is burned in the same step that installs the key.
	if _, err := tx.ExecContext(ctx, `UPDATE replication_receivers SET public_key = ?, pair_secret = '', pair_expires = 0,
		paired_at = ? WHERE id = ?`, in.PublicKey, s.now().Unix(), id); err != nil {
		return ReplicationPairResponse{}, err
	}
	if err := s.applyReceivers(ctx, tx); err != nil {
		if errors.Is(err, sys.ErrNotFound) {
			return ReplicationPairResponse{}, unavailable(err)
		}
		if errors.Is(err, sys.ErrInvalid) {
			return ReplicationPairResponse{}, errs.Conflict(CodeFolderRefused, "The receiving machine refused its folder: "+
				sys.Message(err)).WithCause(err)
		}
		return ReplicationPairResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReplicationPairResponse{}, err
	}
	s.limiter.Reset(ip)
	slog.InfoContext(ctx, "replication receiver paired", "receiver", in.Receiver, "ip", ip)
	return ReplicationPairResponse{HostKey: hostKey, MAC: pairMAC(secret, pairedLabl, in.Receiver, in.Nonce, in.PublicKey, hostKey)}, nil
}

// pairClient sends the pairing request to a receiver.
type pairClient func(ctx context.Context, endpoint string, body []byte) (status int, response []byte, err error)

// httpPairer posts over HTTP(S). TLS certificates are not checked: the
// exchange is authenticated by the HMACs (an impostor without the code's
// secret cannot answer correctly), and nothing secret travels.
func httpPairer() pairClient {
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // see above
			Proxy:           nil,
			DialContext:     (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(ctx context.Context, endpoint string, body []byte) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return res.StatusCode, raw, err
	}
}

// SetPairClient replaces how pairing requests are sent (tests).
func (s *Service) SetPairClient(c func(ctx context.Context, endpoint string, body []byte) (int, []byte, error)) {
	s.pairer = c
}

// PeerInput adds a peer.
type PeerInput struct {
	Name    string
	URL     string
	Code    string
	SSHPort int
}

var pairingCode = regexp.MustCompile(`^r([0-9]{1,18})-([a-z2-7]{32})$`)

// AddPeer pairs with another NetShare Hub (the sending side).
func (s *Service) AddPeer(ctx context.Context, in PeerInput) (ReplicationPeer, error) {
	in.Name, in.URL, in.Code = strings.TrimSpace(in.Name), strings.TrimSpace(in.URL), strings.ToLower(strings.TrimSpace(in.Code))
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	v := validate.New()
	v.Check(validName(in.Name), "name", "invalid_format", "Use 1-64 characters.")
	if !v.Has("name") {
		taken, err := s.repo.nameTaken(ctx, "replication_peers", in.Name, 0)
		if err != nil {
			return ReplicationPeer{}, err
		}
		v.Check(!taken, "name", "taken", "A peer with this name already exists.")
	}
	u, err := url.Parse(in.URL)
	host := ""
	if err == nil {
		host = u.Hostname()
	}
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !sys.ValidHost(host) || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		v.Add("url", "invalid_format", "Enter the other NetShare Hub's address, like http://192.168.1.20:8080.")
	}
	m := pairingCode.FindStringSubmatch(in.Code)
	v.Check(m != nil, "code", "invalid_format", "Paste the pairing code shown on the other NetShare Hub (like r3-abcd...).")
	v.Check(in.SSHPort >= 1 && in.SSHPort <= 65535, "sshPort", "out_of_range", "Use a port from 1 to 65535.")
	if err := v.Err(); err != nil {
		return ReplicationPeer{}, err
	}
	receiver, secret := "r"+m[1], m[2]

	key, err := s.system.PublicKey(ctx)
	if err != nil {
		return ReplicationPeer{}, err
	}
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		return ReplicationPeer{}, err
	}
	nonce := hex.EncodeToString(nonceRaw)
	body, _ := json.Marshal(ReplicationPairRequest{Receiver: receiver, Nonce: nonce, PublicKey: key,
		MAC: pairMAC(secret, pairLabel, receiver, nonce, key)})
	endpoint := strings.TrimSuffix(u.Scheme+"://"+u.Host, "/") + pairPath
	status, raw, err := s.pairer(ctx, endpoint, body)
	if err != nil {
		return ReplicationPeer{}, errs.Unavailable(CodePairingFailed, "Could not reach the other NetShare Hub at "+u.Host+". Check the address and that it is running.").
			WithCause(err)
	}
	switch {
	case status == http.StatusForbidden:
		return ReplicationPeer{}, errs.Invalid(CodePairingFailed, "The other NetShare Hub refused the code: it is wrong, already used or expired. Make a new one there.")
	case status == http.StatusNotFound:
		return ReplicationPeer{}, errs.Invalid(CodePairingFailed, "The other machine does not accept pairings: is it a NetShare Hub with the replication module on?")
	case status != http.StatusOK:
		var p struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &p)
		msg := "The other NetShare Hub could not complete the pairing"
		if p.Detail != "" {
			msg += ": " + truncate(p.Detail, 300)
		}
		return ReplicationPeer{}, errs.Unavailable(CodePairingFailed, msg).WithCause(fmt.Errorf("pair: status %d", status))
	}
	var res ReplicationPairResponse
	if err := json.Unmarshal(raw, &res); err != nil || !sys.ValidSSHKey(res.HostKey) {
		return ReplicationPeer{}, errs.Unavailable(CodePairingFailed, "The other machine gave an answer that is not a pairing answer.")
	}
	// Only the machine that holds the secret can compute this: an impostor
	// in the middle cannot slip its own host key in.
	want := pairMAC(secret, pairedLabl, receiver, nonce, key, res.HostKey)
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(res.MAC))) {
		slog.WarnContext(ctx, "replication pairing: the answer did not prove the code", "url", endpoint)
		return ReplicationPeer{}, errs.Invalid(CodePairingFailed, "The answer did not prove it came from the machine that made the code. Nothing was saved.")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.repo.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return ReplicationPeer{}, err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, "INSERT INTO replication_peers (name, url, host, ssh_port, host_key, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		in.Name, u.Scheme+"://"+u.Host, host, in.SSHPort, res.HostKey, s.now().Unix())
	if err != nil {
		return ReplicationPeer{}, err
	}
	id, _ := r.LastInsertId()
	if err := s.applyPeers(ctx, tx); err != nil {
		return ReplicationPeer{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReplicationPeer{}, err
	}
	slog.InfoContext(ctx, "replication peer paired", "peer", in.Name, "host", host)
	for _, p := range mustPeers(s.Peers(ctx)) {
		if p.ID == id {
			return p, nil
		}
	}
	return ReplicationPeer{}, errs.Internal(errors.New("paired peer not found"))
}

func mustPeers(p []ReplicationPeer, _ error) []ReplicationPeer { return p }
