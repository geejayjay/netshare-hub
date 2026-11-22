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
	opApplyConfig    = "samba.apply-config"
	opFixPermissions = "samba.fix-permissions"
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

type applyConfigRequest struct {
	Server sys.ServerSettings `json:"server"`
	Shares []sys.ShareSpec    `json:"shares"`
}

// fixPermissionsRequest names a share; the agent reads its folder and access
// from the applied config, never from the request.
type fixPermissionsRequest struct {
	Share string `json:"share"`
}

type workerRequest struct {
	Name       string `json:"name"`
	Background bool   `json:"background,omitempty"`
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
	// Share and Path say which share's folder failed (ACL errors only).
	Share string `json:"share,omitempty"`
	Path  string `json:"path,omitempty"`
}

// Error codes on the wire, one per error the sys interfaces document.
const (
	codeInvalid        = "invalid"
	codeNameTaken      = "name_taken"
	codeNotFound       = "not_found"
	codeNotManaged     = "not_managed"
	codeNotDir         = "not_dir"
	codeDisabled       = "disabled"
	codeNotExist       = "not_exist"
	codeExist          = "exist"
	codeConfigRejected = "config_rejected"
	codeReloadFailed   = "reload_failed"
	codeACLUnsupported = "acl_unsupported"
	codeACLFailed      = "acl_failed"
	codeForbidden      = "forbidden"
	codeBadRequest     = "bad_request"
	codeInternal       = "internal"
)

// encodeError turns an error from the system layer into its wire form.
func encodeError(err error) (status int, res errorResponse) {
	var cfg *sys.ConfigError
	var aclErr *sys.ACLError
	switch {
	case errors.As(err, &cfg):
		return http.StatusUnprocessableEntity, errorResponse{Code: codeConfigRejected, Message: cfg.Output}
	case errors.As(err, &aclErr):
		code, status := codeACLFailed, http.StatusInternalServerError
		if errors.Is(err, sys.ErrACLUnsupported) {
			code, status = codeACLUnsupported, http.StatusUnprocessableEntity
		}
		return status, errorResponse{Code: code, Message: aclErr.Err.Error(), Share: aclErr.Share, Path: aclErr.Path}
	case errors.Is(err, sys.ErrInvalid):
		return http.StatusUnprocessableEntity, errorResponse{Code: codeInvalid, Message: err.Error()}
	case errors.Is(err, sys.ErrNameTaken):
		return http.StatusConflict, errorResponse{Code: codeNameTaken, Message: err.Error()}
	case errors.Is(err, sys.ErrNotFound):
		return http.StatusNotFound, errorResponse{Code: codeNotFound, Message: err.Error()}
	case errors.Is(err, sys.ErrNotManaged):
		return http.StatusForbidden, errorResponse{Code: codeNotManaged, Message: err.Error()}
	case errors.Is(err, sys.ErrNotDir):
		return http.StatusUnprocessableEntity, errorResponse{Code: codeNotDir, Message: err.Error()}
	case errors.Is(err, sys.ErrDisabled):
		return http.StatusForbidden, errorResponse{Code: codeDisabled, Message: err.Error()}
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound, errorResponse{Code: codeNotExist, Message: "does not exist"}
	case errors.Is(err, fs.ErrExist):
		return http.StatusConflict, errorResponse{Code: codeExist, Message: "already exists"}
	case errors.Is(err, sys.ErrReloadFailed):
		return http.StatusServiceUnavailable, errorResponse{Code: codeReloadFailed, Message: err.Error()}
	default:
		return http.StatusInternalServerError, errorResponse{Code: codeInternal, Message: err.Error()}
	}
}

// decodeError rebuilds the error value on the client side.
func decodeError(res errorResponse) error {
	wrap := func(base error) error { return &remoteError{message: res.Message, base: base} }
	switch res.Code {
	case codeConfigRejected:
		return &sys.ConfigError{Output: res.Message}
	case codeACLUnsupported:
		return &sys.ACLError{Share: res.Share, Path: res.Path, Err: wrap(sys.ErrACLUnsupported)}
	case codeACLFailed:
		return &sys.ACLError{Share: res.Share, Path: res.Path, Err: &remoteError{message: res.Message}}
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
	case codeDisabled:
		return wrap(sys.ErrDisabled)
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
