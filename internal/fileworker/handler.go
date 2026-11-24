package fileworker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"

	"netshare-hub/internal/sys"
)

const (
	defaultPageSize = 500
	maxPageSize     = 1000
	// Modes for what the worker creates. They match the Samba share settings
	// ("create mask = 0660", "directory mask = 0770"), so a file made in the
	// browser looks the same as one made over SMB: the folder's default ACL
	// applies, cut down to "rw" for files.
	fileMode = 0o660
	dirMode  = 0o770
	// uploadPrefix marks files that are still being received.
	uploadPrefix = ".nsh-upload-"
)

// Handler serves file operations for the shares it was given. It contains no
// privilege logic: it runs as the user, so the kernel does the checking.
type Handler struct {
	roots map[string]sys.WorkerRoot
	mux   *http.ServeMux
}

// NewHandler returns the worker's request handler.
func NewHandler(roots []sys.WorkerRoot) *Handler {
	h := &Handler{roots: map[string]sys.WorkerRoot{}, mux: http.NewServeMux()}
	for _, r := range roots {
		h.roots[r.Share] = r
	}
	h.mux.HandleFunc("GET /v1/list", h.list)
	h.mux.HandleFunc("GET /v1/stat", h.stat)
	h.mux.HandleFunc("GET /v1/read", h.read)
	h.mux.HandleFunc("PUT /v1/write", h.write)
	h.mux.HandleFunc("POST /v1/mkdir", h.mkdir)
	h.mux.HandleFunc("POST /v1/rename", h.rename)
	h.mux.HandleFunc("POST /v1/remove", h.remove)
	h.mux.HandleFunc("POST /v1/copy", h.copy)
	h.mux.HandleFunc("POST /v1/upload/create", h.uploadCreate)
	h.mux.HandleFunc("GET /v1/upload/size", h.uploadSize)
	h.mux.HandleFunc("POST /v1/upload/append", h.uploadAppend)
	h.mux.HandleFunc("POST /v1/upload/finish", h.uploadFinish)
	h.mux.HandleFunc("POST /v1/upload/abort", h.uploadAbort)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// open returns the share's folder as an os.Root. Everything done through the
// Root stays inside that folder: the operating system refuses "..", absolute
// paths and symbolic links that lead out of it.
func (h *Handler) open(share string, write bool) (*os.Root, error) {
	root, ok := h.roots[share]
	if !ok {
		// Same answer as for a missing folder: a worker does not reveal
		// which other shares exist.
		return nil, fs.ErrNotExist
	}
	if write && !root.Writable {
		return nil, sys.ErrReadOnly
	}
	return os.OpenRoot(root.Path)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir, err := cleanPath(q.Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	share := q.Get("share")
	root, err := h.open(share, false)
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()
	hide := h.roots[share].HideUnreadable

	f, err := root.Open(dir)
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	items, err := f.ReadDir(-1)
	if err != nil {
		fail(w, err)
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })

	// Cursor paging: "after" is the last name of the previous page.
	after := q.Get("after")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = defaultPageSize
	}
	limit = min(limit, maxPageSize)
	start := sort.Search(len(items), func(i int) bool { return items[i].Name() > after })
	if after == "" {
		start = 0
	}

	result := ListResult{Entries: []Entry{}}
	for _, item := range items[start:] {
		if len(result.Entries) == limit {
			result.Next = result.Entries[limit-1].Name
			break
		}
		if isUploadTemp(item.Name()) {
			continue // an upload (or a save) still in progress
		}
		if dir == "." && item.Name() == sys.RecycleDir && h.roots[share].Recycle {
			continue // the recycle bin has its own listing
		}
		info, err := item.Info()
		if err != nil {
			continue // removed while listing
		}
		// Like Samba's "hide unreadable": the kernel says what the user
		// (this process) may read; links are shown as they are.
		if hide && (info.IsDir() || info.Mode().IsRegular()) && !readable(f, item.Name()) {
			continue
		}
		result.Entries = append(result.Entries, entryOf(info))
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) stat(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := cleanPath(q.Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	root, err := h.open(q.Get("share"), false)
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()

	info, err := root.Lstat(p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entryOf(info))
}

func (h *Handler) read(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := cleanPath(q.Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	root, err := h.open(q.Get("share"), false)
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()

	f, err := root.Open(p)
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
	if info.IsDir() {
		fail(w, ErrIsDir)
		return
	}
	// ServeContent handles Range requests, so downloads can resume and
	// media can seek. The content type is decided by the web server.
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// write stores the request body as a file. The data goes to a temporary file
// in the same folder first and is renamed into place when complete, so a
// half-finished upload is never visible under the real name.
func (h *Handler) write(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := cleanPath(q.Get("path"))
	if err != nil || p == "." {
		fail(w, ErrInvalidPath)
		return
	}
	root, err := h.open(q.Get("share"), true)
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()

	overwrite := q.Get("overwrite") == "true"
	if info, err := root.Lstat(p); err == nil {
		if info.IsDir() {
			fail(w, ErrIsDir)
			return
		}
		if !overwrite {
			fail(w, fs.ErrExist)
			return
		}
	}

	if err := store(root, p, r.Body, overwrite); err != nil {
		fail(w, err)
		return
	}
	info, err := root.Lstat(p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entryOf(info))
}

// store writes data to a temporary file next to p and moves it into place
// when complete, so a half-written file is never visible under its name.
func store(root *os.Root, p string, data io.Reader, overwrite bool) error {
	tmp := path.Join(path.Dir(p), uploadPrefix+randomName())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		if overwrite {
			err = root.Rename(tmp, p)
		} else {
			// A hard link fails if the name already exists, so a file
			// created by someone else in the meantime is never replaced.
			if err = root.Link(tmp, p); err == nil {
				root.Remove(tmp)
			}
		}
	}
	if err != nil {
		root.Remove(tmp)
	}
	return err
}

// copy copies one regular file, within a share or between two of this
// worker's shares. The data never leaves the worker, and the user's own
// permissions apply on both sides (the worker runs as the user).
func (h *Handler) copy(w http.ResponseWriter, r *http.Request) {
	var req copyRequest
	if !decode(w, r, &req) {
		return
	}
	from, err1 := cleanPath(req.From)
	to, err2 := cleanPath(req.To)
	if err1 != nil || err2 != nil || from == "." || to == "." {
		fail(w, ErrInvalidPath)
		return
	}
	src, err := h.open(req.FromShare, false)
	if err != nil {
		fail(w, err)
		return
	}
	defer src.Close()
	dst, err := h.open(req.ToShare, true)
	if err != nil {
		fail(w, err)
		return
	}
	defer dst.Close()

	f, err := src.Open(from)
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
		fail(w, ErrIsDir)
		return
	}
	if existing, err := dst.Lstat(to); err == nil {
		if existing.IsDir() {
			fail(w, ErrIsDir)
			return
		}
		if !req.Overwrite {
			fail(w, fs.ErrExist)
			return
		}
	}
	if err := store(dst, to, f, req.Overwrite); err != nil {
		fail(w, err)
		return
	}
	// Keep the original's modification time, like a file manager does.
	_ = dst.Chtimes(to, info.ModTime(), info.ModTime())
	out, err := dst.Lstat(to)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entryOf(out))
}

func (h *Handler) mkdir(w http.ResponseWriter, r *http.Request) {
	var req pathRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := cleanPath(req.Path)
	if err != nil || p == "." {
		fail(w, ErrInvalidPath)
		return
	}
	root, err := h.open(req.Share, true)
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()

	if err := root.Mkdir(p, dirMode); err != nil {
		fail(w, err)
		return
	}
	info, err := root.Lstat(p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entryOf(info))
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	var req renameRequest
	if !decode(w, r, &req) {
		return
	}
	from, err1 := cleanPath(req.From)
	to, err2 := cleanPath(req.To)
	if err1 != nil || err2 != nil || from == "." || to == "." {
		fail(w, ErrInvalidPath)
		return
	}
	root, err := h.open(req.Share, true)
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()

	if !req.Overwrite {
		if _, err := root.Lstat(to); err == nil {
			fail(w, fs.ErrExist)
			return
		}
	}
	if err := root.Rename(from, to); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	var req pathRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := cleanPath(req.Path)
	if err != nil || p == "." { // the share's own folder is never removed
		fail(w, ErrInvalidPath)
		return
	}
	root, err := h.open(req.Share, true)
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()

	if _, err := root.Lstat(p); err != nil {
		fail(w, err)
		return
	}
	switch {
	case h.roots[req.Share].Recycle && !sys.InRecycle(p):
		// Into the recycle bin, at the same place in the tree (like Samba's
		// recycle:keeptree), as a new version if that name is taken.
		err = recycle(root, p)
	case req.Recursive:
		err = root.RemoveAll(p)
	default:
		err = root.Remove(p)
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// recycle moves p into the share's recycle bin. A name already in the bin
// gets "Copy #n of " in front, like Samba's recycle:versions.
func recycle(root *os.Root, p string) error {
	dest := path.Join(sys.RecycleDir, p)
	if err := root.MkdirAll(path.Dir(dest), dirMode); err != nil {
		return err
	}
	for n := 1; ; n++ {
		if _, err := root.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
			break
		}
		if n > 1000 {
			return fs.ErrExist
		}
		dest = path.Join(sys.RecycleDir, path.Dir(p), "Copy #"+strconv.Itoa(n)+" of "+path.Base(p))
	}
	return root.Rename(p, dest)
}

func entryOf(info fs.FileInfo) Entry {
	return Entry{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    uint32(info.Mode().Perm()),
		ModTime: info.ModTime().UTC(),
		IsDir:   info.IsDir(),
		IsLink:  info.Mode()&fs.ModeSymlink != 0,
	}
}

func randomName() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{codeInvalid, "malformed request"})
		return false
	}
	return true
}

func fail(w http.ResponseWriter, err error) {
	status, code := encodeError(err)
	message := code
	if status >= 500 {
		// Unexpected: keep the detail in the worker's log, not on the wire.
		slog.Error("file operation failed", "err", err)
	} else if !errors.Is(err, ErrInvalidPath) {
		message = err.Error()
	}
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
