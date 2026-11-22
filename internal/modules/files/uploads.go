package files

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sync"
	"time"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/validate"
)

// Resumable uploads (the tus protocol, see uploads_handler.go).
//
// The web server keeps only the bookkeeping here. The data goes straight to
// the owner's file worker, which writes it to a hidden temporary file in
// the destination folder and, once complete, renames it into place (see
// internal/fileworker/upload.go). So the file belongs to the user from the
// first byte, lands with one atomic rename on the same filesystem, and the
// web server never stores user data.

// DefaultUploadExpiry is how long an upload may sit without new data
// before it is thrown away.
const DefaultUploadExpiry = 24 * time.Hour

// Upload is one upload in progress.
type Upload struct {
	ID        string
	OwnerID   int64
	Share     string
	Dir       string
	Name      string
	Conflict  Conflict
	Size      int64
	Received  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// --- storage ---

type uploadRepo struct{ db *db.DB }

const uploadColumns = "id, owner_id, share, dir, name, conflict, size, received, created_at, updated_at"

func scanUpload(s interface{ Scan(...any) error }) (Upload, error) {
	var u Upload
	var created, updated int64
	err := s.Scan(&u.ID, &u.OwnerID, &u.Share, &u.Dir, &u.Name, &u.Conflict, &u.Size, &u.Received, &created, &updated)
	u.CreatedAt, u.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return u, err
}

func (r uploadRepo) insert(ctx context.Context, u Upload) error {
	_, err := r.db.Write.ExecContext(ctx, "INSERT INTO uploads ("+uploadColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		u.ID, u.OwnerID, u.Share, u.Dir, u.Name, u.Conflict, u.Size, u.Received, u.CreatedAt.Unix(), u.UpdatedAt.Unix())
	return err
}

func (r uploadRepo) get(ctx context.Context, id string) (Upload, bool, error) {
	u, err := scanUpload(r.db.Read.QueryRowContext(ctx, "SELECT "+uploadColumns+" FROM uploads WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, false, nil
	}
	return u, err == nil, err
}

func (r uploadRepo) progress(ctx context.Context, id string, received int64, now time.Time) error {
	_, err := r.db.Write.ExecContext(ctx, "UPDATE uploads SET received = ?, updated_at = ? WHERE id = ?", received, now.Unix(), id)
	return err
}

func (r uploadRepo) delete(ctx context.Context, id string) error {
	_, err := r.db.Write.ExecContext(ctx, "DELETE FROM uploads WHERE id = ?", id)
	return err
}

func (r uploadRepo) idleSince(ctx context.Context, t time.Time) ([]Upload, error) {
	rows, err := r.db.Read.QueryContext(ctx, "SELECT "+uploadColumns+" FROM uploads WHERE updated_at < ?", t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- the rules ---

// uploads is the upload part of Service.
type uploads struct {
	repo    uploadRepo
	maxSize int64 // 0: no limit
	expiry  time.Duration
	// busy holds the uploads receiving data right now: data for one upload
	// is only ever taken one request at a time.
	mu   sync.Mutex
	busy map[string]bool
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewUpload is what a client asks to upload.
type NewUpload struct {
	Share    string
	Dir      string
	Name     string
	Size     int64
	Conflict Conflict
}

// CreateUpload starts an upload. A file of 0 bytes is complete at once.
func (s *Service) CreateUpload(ctx context.Context, a Actor, in NewUpload) (Upload, *Entry, error) {
	if in.Conflict == "" {
		in.Conflict = ConflictRename
	}
	it := Item{Share: in.Share, Path: in.Dir}
	v := validate.New()
	checkItem(v, "", &it, true)
	v.Check(validName(in.Name) && !isTempName(in.Name), "filename", "invalid_format",
		"A file name cannot be empty, \".\" or \"..\", contain / or \\, or start or end with a space.")
	v.Check(in.Conflict == ConflictRename || in.Conflict == ConflictReplace, "conflict", "invalid_choice",
		"Must be \"rename\" or \"replace\".")
	v.Check(in.Size >= 0, "size", "invalid", "The size cannot be negative.")
	if s.up.maxSize > 0 && in.Size > s.up.maxSize {
		v.Add("size", "too_large", fmt.Sprintf("Files can be at most %d bytes here.", s.up.maxSize))
	}
	if err := v.Err(); err != nil {
		return Upload{}, nil, err
	}
	account, err := s.reach(ctx, a, in.Share)
	if err != nil {
		return Upload{}, nil, err
	}
	now := s.now().UTC()
	u := Upload{ID: newID(), OwnerID: a.UserID, Share: in.Share, Dir: it.Path, Name: in.Name, Conflict: in.Conflict,
		Size: in.Size, CreatedAt: now, UpdatedAt: now}
	if err := s.files.UploadCreate(ctx, account, u.Share, u.Dir, u.ID); err != nil {
		return Upload{}, nil, fail(err, "path")
	}
	if u.Size == 0 {
		e, err := s.completeUpload(ctx, account, u)
		return u, e, err
	}
	if err := s.up.repo.insert(ctx, u); err != nil {
		_ = s.files.UploadAbort(ctx, account, u.Share, u.Dir, u.ID)
		return Upload{}, nil, err
	}
	return u, nil, nil
}

// upload finds an upload of this user, and checks they may still use it.
func (s *Service) upload(ctx context.Context, a Actor, id string) (Upload, string, error) {
	notFound := errs.NotFound(CodeUploadNotFound, "This upload does not exist (it may have expired). Start it again.")
	if !fileworkerID(id) {
		return Upload{}, "", notFound
	}
	u, ok, err := s.up.repo.get(ctx, id)
	if err != nil {
		return Upload{}, "", err
	}
	if !ok || u.OwnerID != a.UserID {
		return Upload{}, "", notFound // someone else's upload does not exist for you
	}
	account, err := s.reach(ctx, a, u.Share)
	return u, account, err
}

// UploadOffset says how much of an upload has arrived (where to resume).
func (s *Service) UploadOffset(ctx context.Context, a Actor, id string) (Upload, error) {
	u, account, err := s.upload(ctx, a, id)
	if err != nil {
		return Upload{}, err
	}
	size, err := s.files.UploadSize(ctx, account, u.Share, u.Dir, u.ID)
	if err != nil {
		return Upload{}, fail(err, "upload")
	}
	u.Received = size
	return u, nil
}

// AppendUpload adds data at offset. When the last byte is in, the file is
// put in place and returned.
func (s *Service) AppendUpload(ctx context.Context, a Actor, id string, offset int64, data io.Reader) (Upload, *Entry, error) {
	u, account, err := s.upload(ctx, a, id)
	if err != nil {
		return Upload{}, nil, err
	}
	s.up.mu.Lock()
	if s.up.busy[id] {
		s.up.mu.Unlock()
		return Upload{}, nil, errs.Conflict(CodeUploadBusy, "This upload is already receiving data.")
	}
	s.up.busy[id] = true
	s.up.mu.Unlock()
	defer func() {
		s.up.mu.Lock()
		delete(s.up.busy, id)
		s.up.mu.Unlock()
	}()

	// Never take more than the upload's declared size.
	remaining := u.Size - offset
	if offset < 0 || remaining < 0 {
		return Upload{}, nil, errs.Conflict(CodeUploadOffset, "The upload is not at this position.")
	}
	size, err := s.files.UploadAppend(ctx, account, u.Share, u.Dir, u.ID, offset, io.LimitReader(data, remaining))
	if errors.Is(err, fileworker.ErrOffsetMismatch) {
		return Upload{}, nil, errs.Conflict(CodeUploadOffset, "The upload is not at this position; ask where to resume.")
	}
	if err != nil {
		// The connection may have broken mid-way; whatever arrived is kept.
		if size, sizeErr := s.files.UploadSize(ctx, account, u.Share, u.Dir, u.ID); sizeErr == nil {
			_ = s.up.repo.progress(ctx, id, size, s.now())
		}
		return Upload{}, nil, fail(err, "upload")
	}
	u.Received = size
	if err := s.up.repo.progress(ctx, id, size, s.now()); err != nil {
		return Upload{}, nil, err
	}
	if size < u.Size {
		u.UpdatedAt = s.now().UTC()
		return u, nil, nil
	}
	e, err := s.completeUpload(ctx, account, u)
	return u, e, err
}

// completeUpload puts the file in place (keeping both if the name is taken,
// unless asked to replace) and forgets the upload.
func (s *Service) completeUpload(ctx context.Context, account string, u Upload) (*Entry, error) {
	var e Entry
	var err error
	name := u.Name
	for attempt := 0; attempt < 5; attempt++ {
		if u.Conflict == ConflictRename {
			var target string
			if target, err = s.freeName(ctx, account, u.Share, path.Join(u.Dir, u.Name)); err != nil {
				break
			}
			name = path.Base(target)
		}
		e, err = s.files.UploadFinish(ctx, account, u.Share, u.Dir, u.ID, name, u.Conflict == ConflictReplace)
		if !errorsIsExist(err) || u.Conflict != ConflictRename {
			break // done, or failed for another reason; with rename, retry if the name was taken meanwhile
		}
	}
	if err != nil {
		return nil, fail(err, "filename")
	}
	if err := s.up.repo.delete(ctx, u.ID); err != nil {
		slog.ErrorContext(ctx, "could not forget a finished upload", "upload", u.ID, "err", err)
	}
	return &e, nil
}

// CancelUpload throws away an upload.
func (s *Service) CancelUpload(ctx context.Context, a Actor, id string) error {
	u, account, err := s.upload(ctx, a, id)
	if err != nil {
		return err
	}
	if err := s.files.UploadAbort(ctx, account, u.Share, u.Dir, u.ID); err != nil && !errorsIsNotExist(err) {
		return fail(err, "upload")
	}
	return s.up.repo.delete(ctx, u.ID)
}

// expires is when an upload is thrown away if nothing more arrives.
func (s *Service) expires(u Upload) time.Time { return u.UpdatedAt.Add(s.up.expiry) }

// SweepUploads throws away uploads that got no data for the expiry time. It
// runs as each upload's owner, through their worker. An upload whose owner
// can no longer act (SMB access removed, disabled, deleted) is forgotten;
// its temporary file stays, hidden, until someone with access to the
// folder removes it.
func (s *Service) SweepUploads(ctx context.Context) {
	idle, err := s.up.repo.idleSince(ctx, s.now().Add(-s.up.expiry))
	if err != nil {
		slog.ErrorContext(ctx, "upload cleanup: list expired uploads", "err", err)
		return
	}
	for _, u := range idle {
		if account, err := s.accountOf(ctx, u.OwnerID); err == nil {
			if err := s.files.UploadAbort(ctx, account, u.Share, u.Dir, u.ID); err != nil && !errorsIsNotExist(err) {
				slog.WarnContext(ctx, "upload cleanup: could not remove the data", "upload", u.ID, "err", err)
				continue // try again next time
			}
		}
		if err := s.up.repo.delete(ctx, u.ID); err != nil {
			slog.ErrorContext(ctx, "upload cleanup: forget upload", "upload", u.ID, "err", err)
		}
		slog.InfoContext(ctx, "expired upload removed", "upload", u.ID, "owner", u.OwnerID, "share", u.Share)
	}
}

func fileworkerID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func isTempName(name string) bool { return len(name) >= 12 && name[:12] == ".nsh-upload-" }
