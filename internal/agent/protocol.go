// Package agent splits NetShare Hub into two processes with different rights:
//
//	netshare-hub serve   the web server. Faces the network, runs as an
//	                     unprivileged user, and cannot change the system.
//	netshare-hub agent   runs as root, is NOT reachable from the network, and
//	                     performs a short, fixed list of system operations.
//
// They talk over a unix socket. The point of the split: if the web server is
// ever compromised, the attacker holds an unprivileged account that can only
// ask the agent for the operations below, and the agent checks every request
// itself. It never runs a command, writes a file or builds a config from
// text the server sends; it only takes structured arguments and applies its
// own rules to them (see internal/sys).
//
// The wire format is JSON over HTTP on the socket: POST /v1/<operation>.
package agent

import (
	"errors"
	"io/fs"
	"net/http"

	"netshare-hub/internal/sys"
)

// Operations. This list is the agent's whole surface: adding an operation
// here is a security decision, not a convenience.
const (
	opPing           = "ping"
	opCreateUser     = "accounts.create"
	opSetPassword    = "accounts.set-password"
	opSetEnabled     = "accounts.set-enabled"
	opDeleteUser     = "accounts.delete"
	opApplyShares    = "samba.apply-shares"
	opResolveDir     = "files.resolve-dir"
	opCreateShareDir = "files.create-share-dir"
	opWorker         = "files.worker"
	pathPrefix       = "/v1/"
	maxRequestBytes  = 8 << 20
)

// Request and response bodies. Each operation has its own type and unknown
// fields are refused, so a request can never carry more than it should.

type createUserRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type setPasswordRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type setEnabledRequest struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type deleteUserRequest struct {
	Name string `json:"name"`
}

type applySharesRequest struct {
	Shares []sys.ShareSpec `json:"shares"`
}

type workerRequest struct {
	Name string `json:"name"`
}

type pathRequest struct {
	Path string `json:"path"`
}

type pathResponse struct {
	Path string `json:"path"`
}

type pingResponse struct {
	Version string `json:"version"`
}

// errorResponse carries a failure back to the server. Code lets the client
// rebuild the right error value, so errors.Is works across the socket.
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes on the wire, one per error the sys interfaces document.
const (
	codeInvalid        = "invalid"
	codeNameTaken      = "name_taken"
	codeNotFound       = "not_found"
	codeNotManaged     = "not_managed"
	codeNotDir         = "not_dir"
	codeNotExist       = "not_exist"
	codeExist          = "exist"
	codeConfigRejected = "config_rejected"
	codeReloadFailed   = "reload_failed"
	codeForbidden      = "forbidden"
	codeBadRequest     = "bad_request"
	codeInternal       = "internal"
)

// encodeError turns an error from the system layer into its wire form.
func encodeError(err error) (status int, res errorResponse) {
	var cfg *sys.ConfigError
	switch {
	case errors.As(err, &cfg):
		return http.StatusUnprocessableEntity, errorResponse{codeConfigRejected, cfg.Output}
	case errors.Is(err, sys.ErrInvalid):
		return http.StatusUnprocessableEntity, errorResponse{codeInvalid, err.Error()}
	case errors.Is(err, sys.ErrNameTaken):
		return http.StatusConflict, errorResponse{codeNameTaken, err.Error()}
	case errors.Is(err, sys.ErrNotFound):
		return http.StatusNotFound, errorResponse{codeNotFound, err.Error()}
	case errors.Is(err, sys.ErrNotManaged):
		return http.StatusForbidden, errorResponse{codeNotManaged, err.Error()}
	case errors.Is(err, sys.ErrNotDir):
		return http.StatusUnprocessableEntity, errorResponse{codeNotDir, err.Error()}
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound, errorResponse{codeNotExist, "does not exist"}
	case errors.Is(err, fs.ErrExist):
		return http.StatusConflict, errorResponse{codeExist, "already exists"}
	case errors.Is(err, sys.ErrReloadFailed):
		return http.StatusServiceUnavailable, errorResponse{codeReloadFailed, err.Error()}
	default:
		return http.StatusInternalServerError, errorResponse{codeInternal, err.Error()}
	}
}

// decodeError rebuilds the error value on the client side.
func decodeError(res errorResponse) error {
	wrap := func(base error) error { return &remoteError{message: res.Message, base: base} }
	switch res.Code {
	case codeConfigRejected:
		return &sys.ConfigError{Output: res.Message}
	case codeInvalid:
		return wrap(sys.ErrInvalid)
	case codeNameTaken:
		return wrap(sys.ErrNameTaken)
	case codeNotFound:
		return wrap(sys.ErrNotFound)
	case codeNotManaged:
		return wrap(sys.ErrNotManaged)
	case codeNotDir:
		return wrap(sys.ErrNotDir)
	case codeNotExist:
		return wrap(fs.ErrNotExist)
	case codeExist:
		return wrap(fs.ErrExist)
	case codeReloadFailed:
		return wrap(sys.ErrReloadFailed)
	default:
		return &remoteError{message: res.Code + ": " + res.Message}
	}
}

// remoteError is an error reported by the agent.
type remoteError struct {
	message string
	base    error
}

func (e *remoteError) Error() string { return "agent: " + e.message }
func (e *remoteError) Unwrap() error { return e.base }
