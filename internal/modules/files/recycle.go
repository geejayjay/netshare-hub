package files

import (
	"context"
	"net/http"
	"path"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/platform/validate"
	"netshare-hub/internal/sys"
)

// The recycle bin (step 2.7b). On a share with one, deleting (over SMB or
// here) moves files to the share's .recycle folder, at the same place in
// the tree; they are purged after the share's retention. Everyone sees in
// the bin what they could see before it was deleted (it keeps the share's
// permissions), and works with it as themselves, through their file
// worker.

// CodeNoRecycleBin: the share has no recycle bin.
const CodeNoRecycleBin errs.Code = "SHARE_NO_RECYCLE_BIN"

// RecycleShares says which shares have a recycle bin.
type RecycleShares interface {
	RecycleDays(ctx context.Context, share string) (int, error)
}

// SetRecycle connects the shares module for the recycle bin.
func (s *Service) SetRecycle(r RecycleShares) { s.recycle = r }

func (s *Service) needRecycle(ctx context.Context, share string) error {
	if s.recycle == nil {
		return errs.NotFound(CodeNoRecycleBin, "This share has no recycle bin.")
	}
	days, err := s.recycle.RecycleDays(ctx, share)
	if err != nil {
		return err
	}
	if days <= 0 {
		return errs.NotFound(CodeNoRecycleBin, "This share has no recycle bin: deleting there is permanent.")
	}
	return nil
}

// ListRecycle lists a folder of a share's recycle bin ("" is its top).
func (s *Service) ListRecycle(ctx context.Context, a Actor, share, dir, after string, limit int) (fileworker.ListResult, error) {
	if err := s.needRecycle(ctx, share); err != nil {
		return fileworker.ListResult{}, err
	}
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
	res, err := s.files.List(ctx, account, share, path.Join(sys.RecycleDir, it.Path), after, limit)
	if errorsIsNotExist(err) && it.Path == "." {
		return fileworker.ListResult{Entries: []Entry{}}, nil // nothing was deleted yet
	}
	return res, fail(err, "path")
}

// Restore moves an item from the recycle bin back where it was. Missing
// folders on the way are made again; a name taken since gets " (2)".
func (s *Service) Restore(ctx context.Context, a Actor, share, p string) (Entry, error) {
	if err := s.needRecycle(ctx, share); err != nil {
		return Entry{}, err
	}
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
	from := path.Join(sys.RecycleDir, it.Path)
	if _, err := s.files.Stat(ctx, account, share, from); err != nil {
		return Entry{}, fail(err, "path")
	}
	// Make the folders it was in, if they were deleted too.
	dir := path.Dir(it.Path)
	var parts []string
	for d := dir; d != "." && d != "/"; d = path.Dir(d) {
		parts = append([]string{d}, parts...)
	}
	for _, d := range parts {
		if _, err := s.files.Stat(ctx, account, share, d); errorsIsNotExist(err) {
			if _, err := s.files.Mkdir(ctx, account, share, d); err != nil && !errorsIsExist(err) {
				return Entry{}, fail(err, "path")
			}
		}
	}
	to, err := s.freeName(ctx, account, share, it.Path)
	if err != nil {
		return Entry{}, err
	}
	if err := s.files.Rename(ctx, account, share, from, to, false); err != nil {
		return Entry{}, fail(err, "path")
	}
	e, err := s.files.Stat(ctx, account, share, to)
	return e, fail(err, "path")
}

// EmptyRecycle deletes everything in a share's recycle bin, for good
// (what the user may delete: the kernel decides, as for any delete).
func (s *Service) EmptyRecycle(ctx context.Context, a Actor, share string) error {
	if err := s.needRecycle(ctx, share); err != nil {
		return err
	}
	account, err := s.reach(ctx, a, share)
	if err != nil {
		return err
	}
	err = s.files.Remove(ctx, account, share, sys.RecycleDir, true)
	if errorsIsNotExist(err) {
		return nil
	}
	return fail(err, "share")
}

type (
	restoreInput struct {
		Share string `path:"share" minLength:"1" maxLength:"32"`
		Body  struct {
			Path string `json:"path" minLength:"1" maxLength:"4096" doc:"The item's path in the share (where it was before it was deleted)."`
		}
	}
	shareInput struct {
		Share string `path:"share" minLength:"1" maxLength:"32"`
	}
)

// RegisterRecycle adds the recycle bin's routes.
func RegisterRecycle(api huma.API, svc *Service) {
	const tag = "Files"
	read, write := httpx.Require(rbac.FilesRead), httpx.Require(rbac.FilesWrite)
	httpx.Register(api, httpx.Route{ID: "listRecycleBin", Method: http.MethodGet, Path: "/files/{share}/recycle",
		Summary: "List a folder of the share's recycle bin (paths as they were before deleting)", Tag: tag, Access: read},
		func(ctx context.Context, in *listInput) (*listOutput, error) {
			res, err := svc.ListRecycle(ctx, actor(ctx), in.Share, in.Path, in.After, in.Limit)
			return &listOutput{Body: res}, err
		})
	httpx.Register(api, httpx.Route{ID: "restoreFromRecycleBin", Method: http.MethodPost, Path: "/files/{share}/recycle/restore",
		Summary: "Put a deleted file or folder back where it was", Tag: tag, Access: write},
		func(ctx context.Context, in *restoreInput) (*entryOutput, error) {
			e, err := svc.Restore(ctx, actor(ctx), in.Share, in.Body.Path)
			return &entryOutput{Body: e}, err
		})
	httpx.Register(api, httpx.Route{ID: "emptyRecycleBin", Method: http.MethodPost, Path: "/files/{share}/recycle/empty",
		Summary: "Delete everything in the share's recycle bin, for good", Tag: tag, Access: write, Status: http.StatusNoContent},
		func(ctx context.Context, in *shareInput) (*struct{}, error) {
			return nil, svc.EmptyRecycle(ctx, actor(ctx), in.Share)
		})
}
