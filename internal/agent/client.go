package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"netshare-hub/internal/sys"
	"netshare-hub/internal/sys/sambalog"
)

// Client is the web server's side of the agent. It implements the sys
// interfaces by forwarding each call over the socket, so the rest of the
// server does not know (or care) that the system work happens elsewhere.
type Client struct {
	http *http.Client
	long *http.Client // same connection, no overall timeout (long operations)
	base string
}

// Dial returns a client for the agent listening on a unix socket.
func Dial(socket string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: time.Minute,
	}
	// The host is ignored (the dialer always uses the socket); it only has
	// to be a valid URL.
	return NewClient(&http.Client{Transport: transport, Timeout: 2 * time.Minute}, "http://agent")
}

// NewClient returns a client that reaches the agent at base. Tests use it.
func NewClient(hc *http.Client, base string) *Client {
	long := *hc
	long.Timeout = 0
	return &Client{http: hc, long: &long, base: base}
}

// System returns the client as a sys.System.
func (c *Client) System() sys.System {
	return sys.System{Accounts: c, Samba: c, Files: c, Workers: c, Audit: c, Discovery: c, NFS: c, Replication: c, Block: c}
}

// Ping checks that the agent is reachable and accepts this process.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.call(ctx, opPing, struct{}{}, nil)
}

func (c *Client) call(ctx context.Context, op string, req, res any) error {
	return c.callWith(c.http, ctx, op, req, res)
}

func (c *Client) callWith(hc *http.Client, ctx context.Context, op string, req, res any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+pathPrefix+op, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpRes, err := hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("agent unreachable: %w", err)
	}
	defer httpRes.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpRes.Body, maxRequestBytes))
	if err != nil {
		return fmt.Errorf("agent: read response: %w", err)
	}

	if httpRes.StatusCode != http.StatusOK {
		var failure errorResponse
		if err := json.Unmarshal(raw, &failure); err != nil || failure.Code == "" {
			return fmt.Errorf("agent: unexpected response (status %d)", httpRes.StatusCode)
		}
		return decodeError(failure)
	}
	if res != nil {
		if err := json.Unmarshal(raw, res); err != nil {
			return fmt.Errorf("agent: decode response: %w", err)
		}
	}
	return nil
}

// --- sys.Accounts ---

func (c *Client) CreateUser(ctx context.Context, name, password string) error {
	return c.call(ctx, opCreateUser, createUserRequest{Name: name, Password: password}, nil)
}

func (c *Client) SetPassword(ctx context.Context, name, password string) error {
	return c.call(ctx, opSetPassword, setPasswordRequest{Name: name, Password: password}, nil)
}

func (c *Client) SetEnabled(ctx context.Context, name string, enabled bool) error {
	return c.call(ctx, opSetEnabled, setEnabledRequest{Name: name, Enabled: enabled}, nil)
}

func (c *Client) DeleteUser(ctx context.Context, name string) error {
	return c.call(ctx, opDeleteUser, deleteUserRequest{Name: name}, nil)
}

// --- sys.NFS ---

func (c *Client) NFSStatus(ctx context.Context) (sys.NFSStatus, error) {
	var res sys.NFSStatus
	err := c.call(ctx, opNFSStatus, struct{}{}, &res)
	return res, err
}

func (c *Client) ApplyExports(ctx context.Context, exports []sys.NFSExport) error {
	if exports == nil {
		exports = []sys.NFSExport{}
	}
	return c.call(ctx, opApplyExports, applyExportsRequest{Exports: exports}, nil)
}

// --- sys.Samba ---

func (c *Client) ApplyConfig(ctx context.Context, cfg sys.SambaConfig) error {
	if cfg.Shares == nil {
		cfg.Shares = []sys.ShareSpec{}
	}
	return c.call(ctx, opApplyConfig, applyConfigRequest{Server: cfg.Server, Shares: cfg.Shares, Audit: cfg.Audit,
		LoginAudit: cfg.LoginAudit, Banned: cfg.Banned}, nil)
}

// FixPermissions can run for hours on a big share, so it is not bound by
// the client's usual timeout: it ends when it is done or ctx is cancelled
// (closing the connection makes the agent stop the walk).
func (c *Client) FixPermissions(ctx context.Context, share string) (sys.PermissionStats, error) {
	var res sys.PermissionStats
	err := c.callWith(c.long, ctx, opFixPermissions, fixPermissionsRequest{Share: share}, &res)
	return res, err
}

// Sessions lists the SMB sessions connected now.
func (c *Client) Sessions(ctx context.Context) ([]sys.Session, error) {
	var res sessionsResponse
	err := c.call(ctx, opSessions, struct{}{}, &res)
	return res.Sessions, err
}

// DisconnectSession ends one SMB session.
func (c *Client) DisconnectSession(ctx context.Context, id string) error {
	return c.call(ctx, opDisconnectSession, sessionRequest{ID: id}, nil)
}

// SMBHashes returns Samba password hashes of managed accounts.
func (c *Client) SMBHashes(ctx context.Context, names []string) (map[string]string, error) {
	var res smbHashesResponse
	err := c.call(ctx, opSMBHashes, smbHashesRequest{Names: names}, &res)
	return res.Hashes, err
}

// CreateUserFromHash creates an account with a Samba password hash.
func (c *Client) CreateUserFromHash(ctx context.Context, name, hash string) error {
	return c.call(ctx, opCreateFromHash, createFromHashRequest{Name: name, Hash: hash}, nil)
}

// DisconnectClient drops every SMB connection from one address.
func (c *Client) DisconnectClient(ctx context.Context, ip netip.Addr) error {
	return c.call(ctx, opDisconnectClient, disconnectRequest{IP: ip.String()}, nil)
}

// --- sys.Discovery ---

func (c *Client) DiscoveryStatus(ctx context.Context) (sys.DiscoveryStatus, error) {
	var res sys.DiscoveryStatus
	err := c.call(ctx, opDiscoveryStatus, struct{}{}, &res)
	return res, err
}

func (c *Client) SetDiscovery(ctx context.Context, svc sys.DiscoveryService, on bool) error {
	return c.call(ctx, opSetDiscovery, setDiscoveryRequest{Service: svc, On: on}, nil)
}

// --- sys.Audit ---

// Events streams Samba's audit events from the agent. The channel closes
// when ctx is done or the connection breaks (the agent restarted); the
// caller asks again.
func (c *Client) Events(ctx context.Context) (<-chan sambalog.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+pathPrefix+opAuditEvents, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.long.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent unreachable: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		var failure errorResponse
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if err := json.Unmarshal(raw, &failure); err != nil || failure.Code == "" {
			return nil, fmt.Errorf("agent: unexpected response (status %d)", res.StatusCode)
		}
		return nil, decodeError(failure)
	}
	out := make(chan sambalog.Event, 1000)
	go func() {
		defer close(out)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var ev sambalog.Event
			if json.Unmarshal(sc.Bytes(), &ev) != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// --- sys.Workers ---

func (c *Client) Endpoint(ctx context.Context, account string, background bool) (sys.WorkerEndpoint, error) {
	var res sys.WorkerEndpoint
	err := c.call(ctx, opWorker, workerRequest{Name: account, Background: background}, &res)
	return res, err
}

// --- sys.Files ---

func (c *Client) ResolveDir(ctx context.Context, path string) (string, error) {
	var res pathResponse
	err := c.call(ctx, opResolveDir, pathRequest{Path: path}, &res)
	return res.Path, err
}

func (c *Client) CreateShareDir(ctx context.Context, path string) (string, error) {
	var res pathResponse
	err := c.call(ctx, opCreateShareDir, pathRequest{Path: path}, &res)
	return res.Path, err
}
