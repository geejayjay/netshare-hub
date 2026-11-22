package files

import (
	"context"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"path"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/fileworker"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

// --- request and response types ---

type listInput struct {
	Share string `path:"share" minLength:"1" maxLength:"32"`
	Path  string `query:"path" maxLength:"4096" doc:"The folder, inside the share. Empty: the share's top folder."`
	After string `query:"after" maxLength:"255" doc:"The \"next\" value of the previous page."`
	Limit int    `query:"limit" minimum:"0" maximum:"1000" doc:"Entries per page (default 500)."`
}

type pathInput struct {
	Share string `path:"share" minLength:"1" maxLength:"32"`
	Path  string `query:"path" maxLength:"4096"`
}

type listOutput struct {
	Body fileworker.ListResult
}

type entryOutput struct {
	Body Entry
}

type mkdirInput struct {
	Share string `path:"share" minLength:"1" maxLength:"32"`
	Body  struct {
		Path string `json:"path" minLength:"1" maxLength:"4096" doc:"The new folder, inside the share. Its parent must exist."`
	}
}

type renameInput struct {
	Share string `path:"share" minLength:"1" maxLength:"32"`
	Body  struct {
		Path string `json:"path" minLength:"1" maxLength:"4096"`
		Name string `json:"name" minLength:"1" maxLength:"255" doc:"The new name (no folders: use move for that)."`
	}
}

// TransferRequest moves or copies items into a folder.
type TransferRequest struct {
	Items       []Item   `json:"items" minItems:"1" maxItems:"1000"`
	Destination Item     `json:"destination" doc:"The folder to put them in."`
	Conflict    Conflict `json:"conflict,omitempty" enum:"rename,replace,skip" doc:"When a file with the same name is there already: \"rename\" keeps both (default), \"replace\" overwrites it, \"skip\" leaves it."`
}

type transferInput struct {
	Body TransferRequest
}

type itemsInput struct {
	Body struct {
		Items []Item `json:"items" minItems:"1" maxItems:"1000"`
	}
}

type resultOutput struct {
	Status int
	Body   Result
}

type downloadInput struct {
	Share  string `path:"share" minLength:"1" maxLength:"32"`
	Path   string `query:"path" minLength:"1" maxLength:"4096"`
	Inline bool   `query:"inline" doc:"Show the file in the browser instead of saving it. Only for types that cannot run code there (images, audio, video, PDF, plain text); anything else is still downloaded."`
	Range  string `header:"Range" maxLength:"200"`
}

type zipOutput struct {
	Body struct {
		URL       string    `json:"url" doc:"Open it (GET) to download the zip. Works once, for a minute."`
		ExpiresAt time.Time `json:"expiresAt"`
	}
}

type zipTokenInput struct {
	Token string `path:"token" minLength:"48" maxLength:"48"`
}

// --- routes ---

type handler struct {
	svc *Service
}

// Register adds this module's routes.
func Register(api huma.API, svc *Service) {
	h := &handler{svc: svc}
	const tag = "Files"
	read, write := httpx.Require(rbac.FilesRead), httpx.Require(rbac.FilesWrite)

	httpx.Register(api, httpx.Route{ID: "listFiles", Method: http.MethodGet, Path: "/files/{share}/entries",
		Summary: "List a folder (one page, sorted by name)", Tag: tag, Access: read}, h.list)
	httpx.Register(api, httpx.Route{ID: "getFile", Method: http.MethodGet, Path: "/files/{share}/entry",
		Summary: "Describe one file or folder", Tag: tag, Access: read}, h.stat)
	httpx.Register(api, httpx.Route{ID: "downloadFile", Method: http.MethodGet, Path: "/files/{share}/download",
		Summary: "Download a file (supports Range, so downloads can resume)", Tag: tag, Access: read,
		Binary: "application/octet-stream"}, h.download)
	httpx.Register(api, httpx.Route{ID: "createFolder", Method: http.MethodPost, Path: "/files/{share}/folders",
		Summary: "Create a folder", Tag: tag, Access: write, Status: http.StatusCreated}, h.mkdir)
	httpx.Register(api, httpx.Route{ID: "renameFile", Method: http.MethodPost, Path: "/files/{share}/rename",
		Summary: "Rename a file or folder", Tag: tag, Access: write}, h.rename)
	httpx.Register(api, httpx.Route{ID: "moveFiles", Method: http.MethodPost, Path: "/files/move",
		Summary: "Move files and folders (at once within a share, as a job between shares)", Tag: tag, Access: write}, h.move)
	httpx.Register(api, httpx.Route{ID: "copyFiles", Method: http.MethodPost, Path: "/files/copy",
		Summary: "Copy files and folders (a background job)", Tag: tag, Access: write}, h.copy)
	httpx.Register(api, httpx.Route{ID: "deleteFiles", Method: http.MethodPost, Path: "/files/delete",
		Summary: "Delete files and folders (folders as a job). Permanent until the recycle bin exists.", Tag: tag, Access: write}, h.delete)
	httpx.Register(api, httpx.Route{ID: "prepareZip", Method: http.MethodPost, Path: "/files/zip",
		Summary: "Get a one-time link to download several files as a zip", Tag: tag, Access: read}, h.prepareZip)
	httpx.Register(api, httpx.Route{ID: "downloadZip", Method: http.MethodGet, Path: "/files/zip/{token}",
		Summary: "Download a zip prepared with POST /files/zip", Tag: tag, Access: read,
		Binary: "application/zip"}, h.downloadZip)
}

// --- handlers: translate HTTP <-> service, nothing else ---

func actor(ctx context.Context) Actor {
	ip, _ := netip.ParseAddr(httpx.ClientIP(ctx))
	return Actor{UserID: httpx.PrincipalFrom(ctx).UserID, IP: ip.Unmap()}
}

func (h *handler) list(ctx context.Context, in *listInput) (*listOutput, error) {
	res, err := h.svc.List(ctx, actor(ctx), in.Share, in.Path, in.After, in.Limit)
	if err != nil {
		return nil, err
	}
	return &listOutput{Body: res}, nil
}

func (h *handler) stat(ctx context.Context, in *pathInput) (*entryOutput, error) {
	e, err := h.svc.Stat(ctx, actor(ctx), in.Share, in.Path)
	if err != nil {
		return nil, err
	}
	return &entryOutput{Body: e}, nil
}

func (h *handler) mkdir(ctx context.Context, in *mkdirInput) (*entryOutput, error) {
	e, err := h.svc.Mkdir(ctx, actor(ctx), in.Share, in.Body.Path)
	if err != nil {
		return nil, err
	}
	return &entryOutput{Body: e}, nil
}

func (h *handler) rename(ctx context.Context, in *renameInput) (*entryOutput, error) {
	e, err := h.svc.Rename(ctx, actor(ctx), in.Share, in.Body.Path, in.Body.Name)
	if err != nil {
		return nil, err
	}
	return &entryOutput{Body: e}, nil
}

func result(r Result, err error) (*resultOutput, error) {
	if err != nil {
		return nil, err
	}
	status := http.StatusOK
	if r.Job != nil {
		status = http.StatusAccepted
	}
	return &resultOutput{Status: status, Body: r}, nil
}

func (h *handler) move(ctx context.Context, in *transferInput) (*resultOutput, error) {
	return result(h.svc.Move(ctx, actor(ctx), in.Body.Items, in.Body.Destination, in.Body.Conflict))
}

func (h *handler) copy(ctx context.Context, in *transferInput) (*resultOutput, error) {
	return result(h.svc.Copy(ctx, actor(ctx), in.Body.Items, in.Body.Destination, in.Body.Conflict))
}

func (h *handler) delete(ctx context.Context, in *itemsInput) (*resultOutput, error) {
	return result(h.svc.Delete(ctx, actor(ctx), in.Body.Items))
}

func (h *handler) download(ctx context.Context, in *downloadInput) (*huma.StreamResponse, error) {
	d, err := h.svc.Download(ctx, actor(ctx), in.Share, in.Path, in.Range)
	if err != nil {
		return nil, err
	}
	name := path.Base(in.Path)
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer d.Body.Close()
		for _, k := range []string{"Content-Length", "Content-Range", "Last-Modified", "Accept-Ranges"} {
			if v := d.Header.Get(k); v != "" {
				hctx.SetHeader(k, v)
			}
		}
		contentType, disposition := fileHeaders(name, in.Inline)
		if t := d.Header.Get("Content-Type"); strings.HasPrefix(t, "multipart/byteranges") {
			contentType = t // several ranges: the parts carry the boundary
		}
		setFileHeaders(hctx, contentType, disposition)
		hctx.SetStatus(d.Status)
		if _, err := io.Copy(hctx.BodyWriter(), d.Body); err != nil {
			slog.DebugContext(ctx, "download ended early", "share", in.Share, "err", err)
		}
	}}, nil
}

func (h *handler) prepareZip(ctx context.Context, in *itemsInput) (*zipOutput, error) {
	token, expires, err := h.svc.PrepareZip(ctx, actor(ctx), in.Body.Items)
	if err != nil {
		return nil, err
	}
	out := &zipOutput{}
	out.Body.URL = "/api/v1/files/zip/" + token
	out.Body.ExpiresAt = expires
	return out, nil
}

func (h *handler) downloadZip(ctx context.Context, in *zipTokenInput) (*huma.StreamResponse, error) {
	name, write, err := h.svc.OpenZip(ctx, actor(ctx), in.Token)
	if err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		setFileHeaders(hctx, "application/zip", disposition("attachment", name))
		hctx.SetStatus(http.StatusOK)
		if err := write(hctx.BodyWriter()); err != nil {
			// The headers are sent: all that is left is to stop. The browser
			// sees an incomplete download, never a "complete" broken zip.
			slog.WarnContext(ctx, "zip download failed", "err", err)
			panic(http.ErrAbortHandler)
		}
	}}, nil
}

// --- file response headers ---

// inlineTypes are the only types ever shown in the browser: none can run
// code there. HTML, SVG (it can hold scripts), XML and everything unknown
// are always downloaded, so a file uploaded to a share can never act as a
// page of this site.
var inlineTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".ogg": "audio/ogg",
	".opus": "audio/ogg", ".wav": "audio/wav", ".flac": "audio/flac",
	".pdf": "application/pdf",
	".txt": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
	".csv": "text/plain; charset=utf-8",
}

func fileHeaders(name string, inline bool) (contentType, contentDisposition string) {
	if t, ok := inlineTypes[strings.ToLower(path.Ext(name))]; ok && inline {
		return t, disposition("inline", name)
	}
	return "application/octet-stream", disposition("attachment", name)
}

func disposition(kind, name string) string {
	// FormatMediaType encodes non-ASCII names the standard way (RFC 2231),
	// and quoting means a name cannot add header parameters.
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}

func setFileHeaders(hctx huma.Context, contentType, contentDisposition string) {
	hctx.SetHeader("Content-Type", contentType)
	hctx.SetHeader("Content-Disposition", contentDisposition)
	// Even a file shown inline is treated as from a foreign, empty origin:
	// no scripts, no forms, no requests to anything.
	hctx.SetHeader("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	hctx.SetHeader("X-Content-Type-Options", "nosniff")
	hctx.SetHeader("Cache-Control", "private, no-store")
}
