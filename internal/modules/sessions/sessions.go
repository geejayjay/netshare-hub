// Package sessions shows who is connected over SMB, and ends one session
// when an admin asks (for example a computer left logged in, or one that
// misbehaves). The system layer finds the process from Samba's own list;
// the web server only names a session.
package sessions

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
	"netshare-hub/internal/sys"
)

// Samba is what this module needs from the system layer.
type Samba interface {
	Sessions(ctx context.Context) ([]sys.Session, error)
	DisconnectSession(ctx context.Context, id string) error
}

const CodeSessionNotFound errs.Code = "SESSION_NOT_FOUND"

type listOutput struct {
	Body page.List[sys.Session]
}

type idInput struct {
	ID string `path:"id" pattern:"^[0-9]{1,20}$"`
}

// Register adds the routes.
func Register(api huma.API, samba Samba) {
	const tag = "System"
	httpx.Register(api, httpx.Route{ID: "listSMBSessions", Method: http.MethodGet, Path: "/system/sessions",
		Summary: "List who is connected over SMB now", Tag: tag, Access: httpx.Require(rbac.SystemRead)},
		func(ctx context.Context, _ *struct{}) (*listOutput, error) {
			s, err := samba.Sessions(ctx)
			if err != nil {
				return nil, err
			}
			if s == nil {
				s = []sys.Session{}
			}
			return &listOutput{Body: page.All(s)}, nil
		})
	httpx.Register(api, httpx.Route{ID: "disconnectSMBSession", Method: http.MethodPost, Path: "/system/sessions/{id}/disconnect",
		Summary: "End one SMB session (the computer may connect again)", Tag: tag,
		Access: httpx.Require(rbac.SessionsManage), Status: http.StatusNoContent},
		func(ctx context.Context, in *idInput) (*struct{}, error) {
			err := samba.DisconnectSession(ctx, in.ID)
			switch {
			case errors.Is(err, sys.ErrNotFound), errors.Is(err, sys.ErrInvalid):
				return nil, errs.NotFound(CodeSessionNotFound, "This session is not connected (any more).")
			case err != nil:
				return nil, err
			}
			// Until the audit log exists (1.15), the server log is the record.
			slog.InfoContext(ctx, "SMB session disconnected", "session", in.ID, "by", httpx.PrincipalFrom(ctx).Username)
			return nil, nil
		})
}
