package files

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sync"
	"time"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/validate"
)

// Downloading several files at once: the browser first asks for a zip
// (POST, with the list), then fetches it by a short-lived link (GET), so
// the download is a normal browser download that streams straight to disk.
// The zip is built while it is sent; nothing is stored on the server.

const zipLinkTTL = time.Minute

type zipRequest struct {
	actor   Actor
	account string
	items   []Item
	name    string
	expires time.Time
}

// zipTokens holds pending zip downloads. A link works once, for one user,
// for a minute.
type zipTokens struct {
	mu      sync.Mutex
	pending map[string]zipRequest
}

func newZipTokens() *zipTokens { return &zipTokens{pending: map[string]zipRequest{}} }

func (z *zipTokens) put(r zipRequest) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	z.mu.Lock()
	defer z.mu.Unlock()
	now := time.Now()
	for t, old := range z.pending {
		if now.After(old.expires) {
			delete(z.pending, t)
		}
	}
	z.pending[token] = r
	return token
}

func (z *zipTokens) take(token string, userID int64) (zipRequest, bool) {
	z.mu.Lock()
	defer z.mu.Unlock()
	r, ok := z.pending[token]
	if !ok || r.actor.UserID != userID || time.Now().After(r.expires) {
		return zipRequest{}, false
	}
	delete(z.pending, token)
	return r, true
}

// PrepareZip checks a zip download and returns the token for its link.
func (s *Service) PrepareZip(ctx context.Context, a Actor, items []Item) (token string, expires time.Time, err error) {
	v := validate.New()
	checkItems(v, items)
	if err := v.Err(); err != nil {
		return "", time.Time{}, err
	}
	account, err := s.reach(ctx, a, sharesOf(items)...)
	if err != nil {
		return "", time.Time{}, err
	}
	for i, it := range items {
		if _, err := s.files.Stat(ctx, account, it.Share, it.Path); err != nil {
			return "", time.Time{}, fail(err, fmt.Sprintf("items[%d].path", i))
		}
	}
	name := "download.zip"
	if len(items) == 1 {
		name = path.Base(items[0].Path) + ".zip"
	}
	expires = s.now().Add(zipLinkTTL)
	return s.zips.put(zipRequest{actor: a, account: account, items: items, name: name, expires: expires}), expires, nil
}

// OpenZip takes a zip link. It returns the file name and a function that
// writes the zip. The share's network rules are checked again: the link
// may be opened from another address than it was made from.
func (s *Service) OpenZip(ctx context.Context, a Actor, token string) (string, func(io.Writer) error, error) {
	r, ok := s.zips.take(token, a.UserID)
	if !ok {
		return "", nil, errs.NotFound(CodeZipExpired, "This download link has expired. Start the download again.")
	}
	if _, err := s.reach(ctx, a, sharesOf(r.items)...); err != nil {
		return "", nil, err
	}
	return r.name, func(w io.Writer) error { return s.writeZip(ctx, r, w) }, nil
}

// writeZip streams the items as a zip. Files are stored, not compressed:
// most big files (photos, video) are compressed already, and storing keeps
// the server's CPU free. Links are left out; files the user cannot read
// are skipped (the kernel decides, as everywhere).
func (s *Service) writeZip(ctx context.Context, r zipRequest, w io.Writer) error {
	zw := zip.NewWriter(w)
	addFile := func(share, p, name string, e Entry) error {
		d, err := s.files.Read(ctx, r.account, share, p, "")
		if err != nil {
			slog.InfoContext(ctx, "left out of zip", "share", share, "path", p, "err", err)
			return nil
		}
		defer d.Body.Close()
		out, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: e.ModTime})
		if err != nil {
			return err
		}
		_, err = io.Copy(out, d.Body)
		return err
	}
	for _, it := range r.items {
		e, err := s.files.Stat(ctx, r.account, it.Share, it.Path)
		if err != nil || e.IsLink {
			continue
		}
		base := e.Name
		if !e.IsDir {
			if err := addFile(it.Share, it.Path, base, e); err != nil {
				return err
			}
			continue
		}
		if _, err := zw.CreateHeader(&zip.FileHeader{Name: base + "/", Modified: e.ModTime}); err != nil {
			return err
		}
		err = s.walk(ctx, r.account, it.Share, it.Path, true, func(rel string, e Entry) error {
			name := base + "/" + rel
			switch {
			case e.IsLink:
				return nil
			case e.IsDir:
				_, err := zw.CreateHeader(&zip.FileHeader{Name: name + "/", Modified: e.ModTime})
				return err
			default:
				return addFile(it.Share, path.Join(it.Path, rel), name, e)
			}
		})
		if err != nil {
			return err
		}
	}
	return zw.Close()
}
