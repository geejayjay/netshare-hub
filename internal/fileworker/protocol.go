// Package fileworker is the process that does file operations AS a user.
//
// One worker runs per logged-in user, started by the root side (see
// sys.Workers) with that user's UID and groups. It can therefore do exactly
// what the user could do over SMB, and nothing more: the kernel checks every
// read and write. On top of that the worker itself:
//
//   - only reaches the shares it was started with (never an arbitrary path),
//   - confines every operation to the share folder with os.Root, which the
//     operating system guarantees cannot be escaped with "..", absolute
//     paths or symbolic links,
//   - refuses changes on shares the user has read-only access to,
//   - only talks to the web server (checked by the kernel-reported caller),
//   - exits on its own after a period without requests.
//
// The web server talks to it with JSON over HTTP on a unix socket. Client is
// the web server's side.
package fileworker

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"syscall"
	"time"

	"netshare-hub/internal/sys"
)

// Config is what the root side hands a worker when it starts it (on stdin).
type Config struct {
	// Roots are the shares this worker may reach.
	Roots []sys.WorkerRoot `json:"roots"`
	// AllowUID is the only user, besides root, whose requests are served:
	// the account the web server runs as.
	AllowUID int `json:"allowUid"`
	// IdleSeconds is how long the worker waits without requests before exiting.
	IdleSeconds int `json:"idleSeconds"`
}

// Entry describes a file or folder.
type Entry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    uint32    `json:"mode" doc:"Unix permission bits, e.g. 0644."`
	ModTime time.Time `json:"modTime"`
	IsDir   bool      `json:"isDir"`
	IsLink  bool      `json:"isLink"`
}

// ListResult is one page of a folder listing, sorted by name.
type ListResult struct {
	Entries []Entry `json:"entries"`
	// Next is the cursor for the following page; empty on the last page.
	Next string `json:"next,omitempty"`
}

type renameRequest struct {
	Share     string `json:"share"`
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite"`
}

// copyRequest copies one file, possibly between two shares of the worker.
type copyRequest struct {
	FromShare string `json:"fromShare"`
	From      string `json:"from"`
	ToShare   string `json:"toShare"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite"`
}

type pathRequest struct {
	Share     string `json:"share"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Errors a file operation can return, besides those from io/fs
// (fs.ErrNotExist, fs.ErrExist, fs.ErrPermission) and sys.ErrReadOnly.
var (
	// ErrInvalidPath means the path is not a clean relative path inside the share.
	ErrInvalidPath = errors.New("invalid path")
	ErrNotDir      = errors.New("not a folder")
	ErrIsDir       = errors.New("is a folder")
	ErrNotEmpty    = errors.New("folder is not empty")
)

// Wire codes, one per error above.
const (
	codeNotFound   = "not_found"
	codeExists     = "exists"
	codePermission = "permission"
	codeReadOnly   = "read_only"
	codeInvalid    = "invalid_path"
	codeNotDir     = "not_dir"
	codeIsDir      = "is_dir"
	codeNotEmpty   = "not_empty"
	codeForbidden  = "forbidden"
	codeInternal   = "internal"
)

// encodeError maps an error to its HTTP status and wire code.
func encodeError(err error) (int, string) {
	switch {
	case errors.Is(err, sys.ErrReadOnly):
		return http.StatusForbidden, codeReadOnly
	case errors.Is(err, ErrInvalidPath), isEscape(err):
		return http.StatusBadRequest, codeInvalid
	case errors.Is(err, syscall.EINVAL):
		// For example moving a folder into itself.
		return http.StatusBadRequest, codeInvalid
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound, codeNotFound
	case errors.Is(err, fs.ErrExist):
		return http.StatusConflict, codeExists
	case errors.Is(err, fs.ErrPermission):
		return http.StatusForbidden, codePermission
	case errors.Is(err, ErrNotEmpty), errors.Is(err, syscall.ENOTEMPTY):
		return http.StatusConflict, codeNotEmpty
	case errors.Is(err, ErrNotDir), errors.Is(err, syscall.ENOTDIR):
		return http.StatusConflict, codeNotDir
	case errors.Is(err, ErrIsDir), errors.Is(err, syscall.EISDIR):
		return http.StatusConflict, codeIsDir
	default:
		return http.StatusInternalServerError, codeInternal
	}
}

// isEscape reports whether os.Root refused a path because it leads outside
// the share (through "..", an absolute path or a symbolic link).
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path escapes from parent")
}

// decodeError rebuilds the error on the client side.
func decodeError(code, message string) error {
	base := map[string]error{
		codeNotFound:   fs.ErrNotExist,
		codeExists:     fs.ErrExist,
		codePermission: fs.ErrPermission,
		codeReadOnly:   sys.ErrReadOnly,
		codeInvalid:    ErrInvalidPath,
		codeNotDir:     ErrNotDir,
		codeIsDir:      ErrIsDir,
		codeNotEmpty:   ErrNotEmpty,
	}[code]
	return &remoteError{code: code, message: message, base: base}
}

type remoteError struct {
	code, message string
	base          error
}

func (e *remoteError) Error() string { return "file worker: " + e.code + ": " + e.message }
func (e *remoteError) Unwrap() error { return e.base }

// CleanPath validates a path inside a share and returns it in the form the
// worker uses ("." for the share's top folder). The web server checks paths
// with it too, to answer with a clear error before asking a worker.
func CleanPath(p string) (string, error) { return cleanPath(p) }

// cleanPath validates a path inside a share and returns it in a form os.Root
// accepts. "" means the share's top folder.
func cleanPath(p string) (string, error) {
	if p == "" || p == "." {
		return ".", nil
	}
	// fs.ValidPath: relative, slash-separated, no "." or ".." parts, no
	// empty parts, no leading or trailing slash.
	if !fs.ValidPath(p) || strings.ContainsRune(p, 0) || strings.Contains(p, `\`) {
		return "", ErrInvalidPath
	}
	return p, nil
}
