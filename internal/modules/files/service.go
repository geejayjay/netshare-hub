package files

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/platform/db"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// MaxItems is the most files and folders one request may name.
const MaxItems = 1000

// Service holds the file manager's rules.
type Service struct {
	files  *fileworker.Client
	users  Users
	shares Shares
	jobs   Jobs
	zips   *zipTokens
	up     *uploads
	now    func() time.Time
}

// Options tune the file manager.
type Options struct {
	// MaxUploadBytes is the largest upload accepted; 0 means no limit.
	MaxUploadBytes int64
	// UploadExpiry is how long an upload may sit idle (default a day).
	UploadExpiry time.Duration
}

func NewService(database *db.DB, files *fileworker.Client, u Users, s Shares, j Jobs, opts Options) *Service {
	if opts.UploadExpiry <= 0 {
		opts.UploadExpiry = DefaultUploadExpiry
	}
	return &Service{files: files, users: u, shares: s, jobs: j, zips: newZipTokens(), now: time.Now,
		up: &uploads{repo: uploadRepo{db: database}, maxSize: opts.MaxUploadBytes, expiry: opts.UploadExpiry, busy: map[string]bool{}}}
}

// accountOf returns the system account a user's file operations run as.
// Only users with SMB access have one; a disabled user may not act.
func (s *Service) accountOf(ctx context.Context, userID int64) (string, error) {
	byID, err := s.users.GetMany(ctx, []int64{userID})
	if err != nil {
		return "", err
	}
	u, ok := byID[userID]
	switch {
	case !ok:
		return "", errs.Forbidden(CodeNoSMBAccess, "Your account no longer exists.")
	case !u.SMBAccess:
		return "", errs.Forbidden(CodeNoSMBAccess,
			"Your account has no SMB access, so it cannot open shares. An admin can turn it on.")
	case u.Disabled:
		return "", errs.Forbidden(CodeAccountDisabled, "Your account is disabled.")
	}
	return u.Username, nil
}

// reach checks that the actor may use these shares from where they are, and
// returns the account to act as.
func (s *Service) reach(ctx context.Context, a Actor, shares ...string) (string, error) {
	account, err := s.accountOf(ctx, a.UserID)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, share := range shares {
		if seen[share] {
			continue
		}
		seen[share] = true
		ok, err := s.shares.NetworkAllowed(ctx, share, a.IP)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errs.Forbidden(CodeNetworkBlocked,
				fmt.Sprintf("Share %q cannot be opened from your network address (%s).", share, a.IP))
		}
	}
	return account, nil
}

// checkItem validates a share name and a path in it.
func checkItem(v *validate.Errors, field string, it *Item, allowTop bool) {
	if !sys.ValidShareName(it.Share) {
		v.Add(field+".share", "invalid_format", "Unknown share.")
	}
	p, err := fileworker.CleanPath(it.Path)
	switch {
	case err != nil:
		v.Add(field+".path", "invalid_format", "Use a path inside the share like Photos/2025, without \"..\" or a leading /.")
	case p == "." && !allowTop:
		v.Add(field+".path", "top_folder", "The share's own folder cannot be moved, copied or deleted.")
	default:
		it.Path = p
	}
}

func checkItems(v *validate.Errors, items []Item) {
	if len(items) == 0 || len(items) > MaxItems {
		v.Add("items", "count", fmt.Sprintf("Choose between 1 and %d files or folders.", MaxItems))
	}
	for i := range items {
		checkItem(v, fmt.Sprintf("items[%d]", i), &items[i], false)
	}
}

func sharesOf(items []Item, more ...Item) []string {
	var out []string
	for _, it := range append(append([]Item{}, items...), more...) {
		out = append(out, it.Share)
	}
	return out
}

// --- simple operations ---

func (s *Service) List(ctx context.Context, a Actor, share, dir, after string, limit int) (fileworker.ListResult, error) {
	it := Item{Share: share, Path: dir}
	v := validate.New()
	checkItem(v, "", &it, true)
	if err := v.Err(); err != nil {
		return fileworker.ListResult{}, err
	}
	account, err := s.reach(ctx, a, share)
	if err != nil {
		return fileworker.ListResult{}, err
	}
	res, err := s.files.List(ctx, account, share, it.Path, after, limit)
	return res, fail(err, "path")
}

func (s *Service) Stat(ctx context.Context, a Actor, share, p string) (Entry, error) {
	it := Item{Share: share, Path: p}
	v := validate.New()
	checkItem(v, "", &it, true)
	if err := v.Err(); err != nil {
		return Entry{}, err
	}
	account, err := s.reach(ctx, a, share)
	if err != nil {
		return Entry{}, err
	}
	e, err := s.files.Stat(ctx, account, share, it.Path)
	return e, fail(err, "path")
}

func (s *Service) Mkdir(ctx context.Context, a Actor, share, p string) (Entry, error) {
	it := Item{Share: share, Path: p}
	v := validate.New()
	checkItem(v, "", &it, false)
	if err := v.Err(); err != nil {
		return Entry{}, err
	}
	account, err := s.reach(ctx, a, share)
	if err != nil {
		return Entry{}, err
	}
	e, err := s.files.Mkdir(ctx, account, share, it.Path)
	return e, fail(err, "path")
}

// validName checks a new name for a file or folder (no folders in it).
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 &&
		!strings.ContainsAny(name, "/\\\x00") && strings.TrimSpace(name) == name
}

// Rename gives a file or folder a new name in the same folder.
func (s *Service) Rename(ctx context.Context, a Actor, share, p, newName string) (Entry, error) {
	it := Item{Share: share, Path: p}
	v := validate.New()
	checkItem(v, "", &it, false)
	v.Check(validName(newName), "name", "invalid_format",
		"A name cannot be empty, \".\" or \"..\", contain / or \\, or start or end with a space.")
	if err := v.Err(); err != nil {
		return Entry{}, err
	}
	account, err := s.reach(ctx, a, share)
	if err != nil {
		return Entry{}, err
	}
	to := path.Join(path.Dir(it.Path), newName)
	if err := s.files.Rename(ctx, account, share, it.Path, to, false); err != nil {
		return Entry{}, fail(err, "name")
	}
	e, err := s.files.Stat(ctx, account, share, to)
	return e, fail(err, "name")
}

// --- move, copy, delete ---

// inside reports whether p is dir itself or something inside it.
func inside(p, dir string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}

// checkDestination validates where items go: an existing folder, not one
// of the items or inside one of them (a folder copied into itself would
// never end).
func (s *Service) checkDestination(ctx context.Context, account string, v *validate.Errors, items []Item, to *Item) error {
	checkItem(v, "destination", to, true)
	if v.Has("destination.path") || v.Has("destination.share") {
		return nil
	}
	for i, it := range items {
		if it.Share == to.Share && inside(to.Path, it.Path) {
			v.Add(fmt.Sprintf("items[%d].path", i), "into_itself", "A folder cannot be moved or copied into itself.")
		}
	}
	if v.Err() != nil {
		return nil
	}
	e, err := s.files.Stat(ctx, account, to.Share, to.Path)
	if err != nil {
		return fail(err, "destination.path")
	}
	if !e.IsDir {
		v.Add("destination.path", "not_a_folder", "The destination must be a folder.")
	}
	return nil
}

type copyPayload struct {
	Items    []Item   `json:"items"`
	To       Item     `json:"to"`
	Conflict Conflict `json:"conflict"`
	Move     bool     `json:"move,omitempty"`
}

// Copy copies files and folders into a folder, as a background job.
func (s *Service) Copy(ctx context.Context, a Actor, items []Item, to Item, conflict Conflict) (Result, error) {
	return s.transfer(ctx, a, items, to, conflict, false)
}

// Move moves files and folders into a folder. Within one share that is a
// rename, done at once; between shares it is a copy and delete, as a job.
func (s *Service) Move(ctx context.Context, a Actor, items []Item, to Item, conflict Conflict) (Result, error) {
	return s.transfer(ctx, a, items, to, conflict, true)
}

func (s *Service) transfer(ctx context.Context, a Actor, items []Item, to Item, conflict Conflict, move bool) (Result, error) {
	if conflict == "" {
		conflict = ConflictRename
	}
	v := validate.New()
	checkItems(v, items)
	v.Check(conflict.valid(), "conflict", "invalid_choice", "Must be \"rename\", \"replace\" or \"skip\".")
	if err := v.Err(); err != nil {
		return Result{}, err
	}
	account, err := s.reach(ctx, a, sharesOf(items, to)...)
	if err != nil {
		return Result{}, err
	}
	if err := s.checkDestination(ctx, account, v, items, &to); err != nil {
		return Result{}, err
	}
	if err := v.Err(); err != nil {
		return Result{}, err
	}

	sameShare := true
	for _, it := range items {
		sameShare = sameShare && it.Share == to.Share
	}
	if move && sameShare {
		done, err := s.renameAll(ctx, account, items, to, conflict)
		return Result{Done: done}, err
	}

	verb := "Copy"
	if move {
		verb = "Move"
	}
	job, err := s.jobs.Submit(ctx, jobs.Submit{
		Kind: JobCopy, OwnerID: a.UserID,
		Title:   fmt.Sprintf("%s %s to %s", verb, describe(items), to.Share),
		Payload: copyPayload{Items: items, To: to, Conflict: conflict, Move: move},
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Job: &job}, nil
}

// renameAll moves items within one share. It stops at the first failure;
// Done says how many were moved before it.
func (s *Service) renameAll(ctx context.Context, account string, items []Item, to Item, conflict Conflict) (int, error) {
	for i, it := range items {
		target := path.Join(to.Path, path.Base(it.Path))
		if target == it.Path {
			continue // already there
		}
		var err error
		switch conflict {
		case ConflictReplace:
			err = s.files.Rename(ctx, account, it.Share, it.Path, target, true)
		case ConflictSkip:
			if err = s.files.Rename(ctx, account, it.Share, it.Path, target, false); errorsIsExist(err) {
				err = nil
			}
		default:
			target, err = s.freeName(ctx, account, to.Share, target)
			if err == nil {
				err = s.files.Rename(ctx, account, it.Share, it.Path, target, false)
			}
		}
		if err != nil {
			return i, fail(err, fmt.Sprintf("items[%d].path", i))
		}
	}
	return len(items), nil
}

type deletePayload struct {
	Items []Item `json:"items"`
}

// Delete removes files and folders. Files go at once; if any item is a
// folder, the whole request runs as a job (a folder can hold a lot).
//
// Deleting is permanent until the recycle bin arrives (step 2.7b).
func (s *Service) Delete(ctx context.Context, a Actor, items []Item) (Result, error) {
	v := validate.New()
	checkItems(v, items)
	if err := v.Err(); err != nil {
		return Result{}, err
	}
	account, err := s.reach(ctx, a, sharesOf(items)...)
	if err != nil {
		return Result{}, err
	}
	folders := false
	for i, it := range items {
		e, err := s.files.Stat(ctx, account, it.Share, it.Path)
		if err != nil {
			return Result{}, fail(err, fmt.Sprintf("items[%d].path", i))
		}
		folders = folders || e.IsDir
	}
	if folders {
		job, err := s.jobs.Submit(ctx, jobs.Submit{
			Kind: JobDelete, OwnerID: a.UserID, Title: "Delete " + describe(items),
			Payload: deletePayload{Items: items},
		})
		if err != nil {
			return Result{}, err
		}
		return Result{Job: &job}, nil
	}
	for i, it := range items {
		if err := s.files.Remove(ctx, account, it.Share, it.Path, false); err != nil {
			return Result{Done: i}, fail(err, fmt.Sprintf("items[%d].path", i))
		}
	}
	return Result{Done: len(items)}, nil
}

// Download opens a file for reading (rangeHeader as in HTTP, or "").
func (s *Service) Download(ctx context.Context, a Actor, share, p, rangeHeader string) (*fileworker.Download, error) {
	it := Item{Share: share, Path: p}
	v := validate.New()
	checkItem(v, "", &it, false)
	if err := v.Err(); err != nil {
		return nil, err
	}
	account, err := s.reach(ctx, a, share)
	if err != nil {
		return nil, err
	}
	d, err := s.files.Read(ctx, account, share, it.Path, rangeHeader)
	return d, fail(err, "path")
}

// --- helpers ---

// freeName returns p, or "name (2).ext", "name (3).ext"... if p is taken.
func (s *Service) freeName(ctx context.Context, account, share, p string) (string, error) {
	dir, base := path.Split(p)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" { // ".bashrc": the whole name is the stem
		stem, ext = base, ""
	}
	candidate := p
	for n := 2; n <= 1000; n++ {
		_, err := s.files.Stat(ctx, account, share, candidate)
		if errorsIsNotExist(err) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = path.Join(dir, fmt.Sprintf("%s (%d)%s", stem, n, ext))
	}
	return "", errs.Conflict(CodeExists, "Too many files with this name already exist here.")
}

// describe names items for a job title: "holiday.mp4" or "3 items".
func describe(items []Item) string {
	if len(items) == 1 {
		return path.Base(items[0].Path)
	}
	return fmt.Sprintf("%d items", len(items))
}
