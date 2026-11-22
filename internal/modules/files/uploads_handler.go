package files

import (
	"encoding/base64"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

// The tus 1.0.0 protocol (https://tus.io/protocols/resumable-upload), with
// the creation, termination and expiration extensions: what tus-js-client
// needs. These are plain handlers rather than API operations because the
// request body must be streamed, never held in memory.
//
//	POST   /uploads        create (Upload-Length, Upload-Metadata)
//	HEAD   /uploads/{id}   where to resume (Upload-Offset)
//	PATCH  /uploads/{id}   send data at Upload-Offset
//	DELETE /uploads/{id}   cancel
//	OPTIONS /uploads       what the server supports
//
// Upload-Metadata keys: "share", "path" (the folder, "" for the top),
// "filename", and optional "conflict" ("rename" keeps both, the default;
// "replace"). When the last byte is in, the response carries
// NetShare-File-Path: the file's path in the share, URL-escaped (it may
// differ from the name asked for, when both are kept).

const (
	tusVersion    = "1.0.0"
	tusExtensions = "creation,termination,expiration"
	// HeaderFilePath is sent with the last PATCH: where the file landed.
	HeaderFilePath = "NetShare-File-Path"
)

// RegisterUploads mounts the tus endpoints on a router (the API prefix).
func RegisterUploads(r chi.Router, svc *Service) {
	h := &tusHandler{svc: svc}
	write := httpx.Require(rbac.FilesWrite)
	r.Method(http.MethodOptions, "/uploads", httpx.Protect(write, http.HandlerFunc(h.options)))
	r.Method(http.MethodPost, "/uploads", httpx.Protect(write, http.HandlerFunc(h.create)))
	r.Method(http.MethodHead, "/uploads/{id}", httpx.Protect(write, http.HandlerFunc(h.head)))
	r.Method(http.MethodPatch, "/uploads/{id}", httpx.Protect(write, http.HandlerFunc(h.patch)))
	r.Method(http.MethodDelete, "/uploads/{id}", httpx.Protect(write, http.HandlerFunc(h.delete)))
}

type tusHandler struct {
	svc *Service
}

func requestActor(r *http.Request) Actor {
	ip, _ := netip.ParseAddr(httpx.ClientIP(r.Context()))
	return Actor{UserID: httpx.PrincipalFrom(r.Context()).UserID, IP: ip.Unmap()}
}

// common sets the headers every tus response carries and checks the
// client speaks our version.
func (h *tusHandler) common(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Tus-Resumable", tusVersion)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodOptions && r.Header.Get("Tus-Resumable") != tusVersion {
		w.Header().Set("Tus-Version", tusVersion)
		h.problem(w, r, http.StatusPreconditionFailed, errs.Invalid("TUS_VERSION", "Only tus 1.0.0 is supported."))
		return false
	}
	return true
}

// problem writes an error. For HEAD only the status goes out.
func (h *tusHandler) problem(w http.ResponseWriter, r *http.Request, status int, err error) {
	p := httpx.FromError(r.Context(), err)
	if status != 0 {
		p.Status = status
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(p.Status)
		return
	}
	httpx.WriteProblem(w, p)
}

func (h *tusHandler) options(w http.ResponseWriter, r *http.Request) {
	h.common(w, r)
	w.Header().Set("Tus-Version", tusVersion)
	w.Header().Set("Tus-Extension", tusExtensions)
	if max := h.svc.up.maxSize; max > 0 {
		w.Header().Set("Tus-Max-Size", strconv.FormatInt(max, 10))
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseMetadata reads "key base64,key base64".
func parseMetadata(header string) (map[string]string, bool) {
	out := map[string]string{}
	if strings.TrimSpace(header) == "" {
		return out, true
	}
	for _, pair := range strings.Split(header, ",") {
		key, value, _ := strings.Cut(strings.TrimSpace(pair), " ")
		raw, err := base64.StdEncoding.DecodeString(value)
		if key == "" || err != nil {
			return nil, false
		}
		out[key] = string(raw)
	}
	return out, true
}

func (h *tusHandler) create(w http.ResponseWriter, r *http.Request) {
	if !h.common(w, r) {
		return
	}
	if r.Header.Get("Upload-Defer-Length") != "" {
		h.problem(w, r, http.StatusBadRequest, errs.Invalid("TUS_DEFER_LENGTH", "The file size must be known when the upload starts."))
		return
	}
	size, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || size < 0 {
		h.problem(w, r, http.StatusBadRequest, errs.Invalid("TUS_LENGTH", "Upload-Length must be the file size in bytes."))
		return
	}
	meta, ok := parseMetadata(r.Header.Get("Upload-Metadata"))
	if !ok || len(r.Header.Get("Upload-Metadata")) > 8<<10 {
		h.problem(w, r, http.StatusBadRequest, errs.Invalid("TUS_METADATA", "Upload-Metadata is malformed."))
		return
	}
	u, entry, err := h.svc.CreateUpload(r.Context(), requestActor(r), NewUpload{
		Share: meta["share"], Dir: meta["path"], Name: meta["filename"], Size: size, Conflict: Conflict(meta["conflict"]),
	})
	if err != nil {
		h.problem(w, r, 0, err)
		return
	}
	w.Header().Set("Location", "/api/v1/uploads/"+u.ID)
	if entry != nil { // an empty file is complete at once
		w.Header().Set(HeaderFilePath, url.PathEscape(path.Join(u.Dir, entry.Name)))
		w.Header().Set("Upload-Offset", "0")
	} else {
		w.Header().Set("Upload-Expires", h.svc.expires(u).Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *tusHandler) head(w http.ResponseWriter, r *http.Request) {
	if !h.common(w, r) {
		return
	}
	u, err := h.svc.UploadOffset(r.Context(), requestActor(r), chi.URLParam(r, "id"))
	if err != nil {
		h.problem(w, r, 0, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(u.Received, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(u.Size, 10))
	w.Header().Set("Upload-Expires", h.svc.expires(u).Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
}

func (h *tusHandler) patch(w http.ResponseWriter, r *http.Request) {
	if !h.common(w, r) {
		return
	}
	if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
		h.problem(w, r, http.StatusUnsupportedMediaType, errs.Invalid("TUS_CONTENT_TYPE", "Send data as application/offset+octet-stream."))
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		h.problem(w, r, http.StatusBadRequest, errs.Invalid("TUS_OFFSET", "Upload-Offset must be a number of bytes."))
		return
	}
	u, entry, err := h.svc.AppendUpload(r.Context(), requestActor(r), chi.URLParam(r, "id"), offset, r.Body)
	if err != nil {
		h.problem(w, r, 0, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(u.Received, 10))
	if entry != nil {
		w.Header().Set(HeaderFilePath, url.PathEscape(path.Join(u.Dir, entry.Name)))
	} else {
		w.Header().Set("Upload-Expires", h.svc.expires(u).Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *tusHandler) delete(w http.ResponseWriter, r *http.Request) {
	if !h.common(w, r) {
		return
	}
	if err := h.svc.CancelUpload(r.Context(), requestActor(r), chi.URLParam(r, "id")); err != nil {
		h.problem(w, r, 0, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
