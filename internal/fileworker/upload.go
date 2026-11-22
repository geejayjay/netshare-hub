package fileworker

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
)

// Resumable uploads.
//
// The data of an upload in progress lives in a hidden temporary file in the
// folder it is going to, created and written by this worker, so it belongs
// to the user from the first byte. When the last byte is in, the file is
// renamed into place: on the same filesystem, atomically, and with the
// folder's permissions (its default ACL), exactly like a file saved over
// SMB. The web server only keeps track of the upload; it never holds the
// data. Temporary files are hidden from listings (here and over SMB).

// uploadID is how the web server names an upload: random, and nothing a
// path can be built from.
var uploadID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ErrOffsetMismatch means data was sent for a different position than where
// the upload is (the client must ask where to resume).
var ErrOffsetMismatch = errors.New("upload offset does not match")

type uploadRequest struct {
	Share string `json:"share"`
	Dir   string `json:"dir"`
	ID    string `json:"id"`
	// Name and Overwrite are for finish only.
	Name      string `json:"name,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

type sizeResponse struct {
	Size int64 `json:"size"`
}

// tempPath returns the temporary file's path inside the share.
func tempPath(dir, id string) (string, error) {
	d, err := cleanPath(dir)
	if err != nil || !uploadID.MatchString(id) {
		return "", ErrInvalidPath
	}
	return path.Join(d, uploadPrefix+id), nil
}

func (h *Handler) uploadRoot(w http.ResponseWriter, share, dir, id string) (*os.Root, string, bool) {
	tmp, err := tempPath(dir, id)
	if err != nil {
		fail(w, err)
		return nil, "", false
	}
	root, err := h.open(share, true)
	if err != nil {
		fail(w, err)
		return nil, "", false
	}
	return root, tmp, true
}

// uploadCreate makes the empty temporary file. The folder must exist.
func (h *Handler) uploadCreate(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if !decode(w, r, &req) {
		return
	}
	root, tmp, ok := h.uploadRoot(w, req.Share, req.Dir, req.ID)
	if !ok {
		return
	}
	defer root.Close()
	if info, err := root.Stat(path.Dir(tmp)); err != nil || !info.IsDir() {
		if err == nil {
			err = ErrNotDir
		}
		fail(w, err)
		return
	}
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		fail(w, err)
		return
	}
	f.Close()
	writeJSON(w, http.StatusOK, sizeResponse{})
}

// uploadSize says how much has arrived: the file, not anyone's bookkeeping,
// is the truth (after a crash, the web server's count may be behind).
func (h *Handler) uploadSize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	root, tmp, ok := h.uploadRoot(w, q.Get("share"), q.Get("dir"), q.Get("id"))
	if !ok {
		return
	}
	defer root.Close()
	info, err := root.Lstat(tmp)
	if err != nil {
		fail(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		fail(w, fs.ErrNotExist)
		return
	}
	writeJSON(w, http.StatusOK, sizeResponse{Size: info.Size()})
}

// uploadAppend adds the request body at offset, which must be where the
// file ends now. The web server limits how much it sends.
func (h *Handler) uploadAppend(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		fail(w, ErrInvalidPath)
		return
	}
	root, tmp, ok := h.uploadRoot(w, q.Get("share"), q.Get("dir"), q.Get("id"))
	if !ok {
		return
	}
	defer root.Close()
	f, err := root.OpenFile(tmp, os.O_WRONLY|noFollow, 0)
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fail(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		fail(w, fs.ErrNotExist)
		return
	}
	if info.Size() != offset {
		fail(w, ErrOffsetMismatch)
		return
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		fail(w, err)
		return
	}
	// Whatever arrived is kept, even if the connection breaks: the client
	// asks for the size and resumes from there.
	n, copyErr := io.Copy(f, r.Body)
	if err := f.Sync(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil && n == 0 {
		fail(w, copyErr)
		return
	}
	writeJSON(w, http.StatusOK, sizeResponse{Size: offset + n})
}

// uploadFinish puts the complete file in place under its name. Without
// overwrite an existing file is never replaced (fs.ErrExist).
func (h *Handler) uploadFinish(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if !decode(w, r, &req) {
		return
	}
	root, tmp, ok := h.uploadRoot(w, req.Share, req.Dir, req.ID)
	if !ok {
		return
	}
	defer root.Close()
	// The name is one plain name in the upload's own folder.
	if req.Name == "" || path.Base(req.Name) != req.Name || len(req.Name) > 255 {
		fail(w, ErrInvalidPath)
		return
	}
	final, err := cleanPath(path.Join(path.Dir(tmp), req.Name))
	if err != nil || path.Base(final) != req.Name || isUploadTemp(req.Name) {
		fail(w, ErrInvalidPath)
		return
	}
	if info, err := root.Lstat(tmp); err != nil || !info.Mode().IsRegular() {
		fail(w, fs.ErrNotExist)
		return
	}
	if existing, err := root.Lstat(final); err == nil && existing.IsDir() {
		fail(w, ErrIsDir)
		return
	}
	if req.Overwrite {
		err = root.Rename(tmp, final)
	} else if err = root.Link(tmp, final); err == nil {
		// A hard link fails if the name is taken: nothing is ever replaced.
		root.Remove(tmp)
	}
	if err != nil {
		fail(w, err)
		return
	}
	info, err := root.Lstat(final)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entryOf(info))
}

// uploadAbort throws away an upload's data.
func (h *Handler) uploadAbort(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if !decode(w, r, &req) {
		return
	}
	root, tmp, ok := h.uploadRoot(w, req.Share, req.Dir, req.ID)
	if !ok {
		return
	}
	defer root.Close()
	if err := root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func isUploadTemp(name string) bool {
	return len(name) >= len(uploadPrefix) && name[:len(uploadPrefix)] == uploadPrefix
}
