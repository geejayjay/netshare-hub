package fileworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"netshare-hub/internal/sys"
)

// Client is the web server's side: it runs file operations as a user by
// sending them to that user's worker. Callers name an account and a share;
// they never supply a path on the server.
type Client struct {
	workers sys.Workers

	mu    sync.Mutex
	conns map[sys.WorkerEndpoint]*http.Client
}

// NewClient returns a client that finds workers through the system layer.
func NewClient(workers sys.Workers) *Client {
	return &Client{workers: workers, conns: map[sys.WorkerEndpoint]*http.Client{}}
}

// httpFor returns an HTTP client that always dials the given endpoint.
func (c *Client) httpFor(ep sys.WorkerEndpoint) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hc, ok := c.conns[ep]; ok {
		return hc
	}
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, ep.Network, ep.Address)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}}
	c.conns[ep] = hc
	return hc
}

func (c *Client) forget(ep sys.WorkerEndpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hc, ok := c.conns[ep]; ok {
		hc.CloseIdleConnections()
		delete(c.conns, ep)
	}
}

// send performs one request against the account's worker. A worker may have
// stopped since we last used it (idle, or its shares changed), so a failed
// connection is retried once against a freshly started worker. newBody is
// called again for the retry; a body that cannot be replayed (an upload
// stream) passes nil and is sent on the first attempt only.
func (c *Client) send(ctx context.Context, account, method, op string, query url.Values, newBody func() io.Reader, stream io.Reader) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ep, err := c.workers.Endpoint(ctx, account)
		if err != nil {
			return nil, err
		}
		var body io.Reader
		switch {
		case stream != nil:
			body = stream
		case newBody != nil:
			body = newBody()
		}
		target := "http://worker/v1/" + op
		if len(query) > 0 {
			target += "?" + query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return nil, err
		}
		if newBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := c.httpFor(ep).Do(req)
		if err == nil {
			return res, nil
		}
		lastErr = err
		c.forget(ep)
		if stream != nil || ctx.Err() != nil {
			break // an upload stream cannot be replayed
		}
	}
	return nil, fmt.Errorf("file worker unreachable: %w", lastErr)
}

// finish reads a response: JSON into out on success, an error otherwise.
func finish(res *http.Response, out any) error {
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errorFrom(res)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func errorFrom(res *http.Response) error {
	var failure errorResponse
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err := json.Unmarshal(raw, &failure); err != nil || failure.Code == "" {
		return fmt.Errorf("file worker: unexpected response (status %d)", res.StatusCode)
	}
	return decodeError(failure.Code, failure.Message)
}

func jsonBody(v any) func() io.Reader {
	raw, _ := json.Marshal(v)
	return func() io.Reader { return bytes.NewReader(raw) }
}

// List returns one page of a folder, sorted by name. after is the Next
// cursor of the previous page ("" for the first page).
func (c *Client) List(ctx context.Context, account, share, dir, after string, limit int) (ListResult, error) {
	q := url.Values{"share": {share}, "path": {dir}}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out ListResult
	res, err := c.send(ctx, account, http.MethodGet, "list", q, nil, nil)
	if err != nil {
		return out, err
	}
	return out, finish(res, &out)
}

// Stat describes one file or folder. It does not follow a final symlink.
func (c *Client) Stat(ctx context.Context, account, share, path string) (Entry, error) {
	var out Entry
	res, err := c.send(ctx, account, http.MethodGet, "stat", url.Values{"share": {share}, "path": {path}}, nil, nil)
	if err != nil {
		return out, err
	}
	return out, finish(res, &out)
}

// Mkdir creates one folder. Its parent must exist.
func (c *Client) Mkdir(ctx context.Context, account, share, path string) (Entry, error) {
	var out Entry
	res, err := c.send(ctx, account, http.MethodPost, "mkdir", nil, jsonBody(pathRequest{Share: share, Path: path}), nil)
	if err != nil {
		return out, err
	}
	return out, finish(res, &out)
}

// Rename moves a file or folder within a share.
func (c *Client) Rename(ctx context.Context, account, share, from, to string, overwrite bool) error {
	res, err := c.send(ctx, account, http.MethodPost, "rename", nil,
		jsonBody(renameRequest{Share: share, From: from, To: to, Overwrite: overwrite}), nil)
	if err != nil {
		return err
	}
	return finish(res, nil)
}

// Remove deletes a file or an empty folder; with recursive, a whole folder.
func (c *Client) Remove(ctx context.Context, account, share, path string, recursive bool) error {
	res, err := c.send(ctx, account, http.MethodPost, "remove", nil,
		jsonBody(pathRequest{Share: share, Path: path, Recursive: recursive}), nil)
	if err != nil {
		return err
	}
	return finish(res, nil)
}

// Write stores data as a file. The file appears under its name only once it
// is complete. Without overwrite, an existing file is left alone
// (fs.ErrExist).
func (c *Client) Write(ctx context.Context, account, share, path string, data io.Reader, overwrite bool) (Entry, error) {
	var out Entry
	q := url.Values{"share": {share}, "path": {path}, "overwrite": {strconv.FormatBool(overwrite)}}
	res, err := c.send(ctx, account, http.MethodPut, "write", q, nil, data)
	if err != nil {
		return out, err
	}
	return out, finish(res, &out)
}

// Download is an open file being read.
type Download struct {
	// Body is the file's content (or the requested range). Close it when done.
	Body io.ReadCloser
	// Status is 200 for the whole file or 206 for a range.
	Status int
	// Header carries Content-Length, Content-Range, Last-Modified and
	// Accept-Ranges, ready to pass on to a browser.
	Header http.Header
}

// Read opens a file for reading. rangeHeader is an HTTP Range header value
// ("bytes=0-1023") or "" for the whole file, so downloads can resume and
// media can seek.
func (c *Client) Read(ctx context.Context, account, share, path, rangeHeader string) (*Download, error) {
	q := url.Values{"share": {share}, "path": {path}}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ep, err := c.workers.Endpoint(ctx, account)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://worker/v1/read?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		res, err := c.httpFor(ep).Do(req)
		if err != nil {
			lastErr = err
			c.forget(ep)
			continue
		}
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
			defer res.Body.Close()
			if res.StatusCode == http.StatusRequestedRangeNotSatisfiable {
				return nil, ErrInvalidRange
			}
			return nil, errorFrom(res)
		}
		return &Download{Body: res.Body, Status: res.StatusCode, Header: res.Header}, nil
	}
	return nil, fmt.Errorf("file worker unreachable: %w", lastErr)
}

// ErrInvalidRange means the requested byte range is outside the file.
var ErrInvalidRange = errors.New("requested range is outside the file")
