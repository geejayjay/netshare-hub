package activity

import (
	"context"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

type listInput struct {
	Share    string    `query:"share" maxLength:"32"`
	User     string    `query:"user" maxLength:"256"`
	Op       string    `query:"op" enum:"connect,disconnect,create_folder,write,rename,delete,login,login_failed"`
	ClientIP string    `query:"clientIp" maxLength:"64"`
	From     time.Time `query:"from" doc:"Only entries at or after this time (RFC 3339)."`
	To       time.Time `query:"to" doc:"Only entries before this time."`
	Path     string    `query:"path" maxLength:"512" doc:"Only entries whose path (or new name) contains this."`
	Before   int64     `query:"before" minimum:"0" doc:"The \"next\" value of the previous page."`
	Limit    int       `query:"limit" minimum:"0" maximum:"500" doc:"Entries per page (default 100)."`
}

func (in listInput) filter() Filter {
	return Filter{Share: in.Share, User: in.User, Op: in.Op, ClientIP: in.ClientIP, From: in.From, To: in.To,
		Path: in.Path, Before: in.Before}
}

// Page is one page of the feed.
type Page struct {
	Items []ActivityEntry `json:"items" nullable:"false"`
	// Next is the cursor for older entries ("before" in the next request);
	// 0 on the last page.
	Next int64 `json:"next"`
}

type listOutput struct {
	Body Page
}

// MaxExport is the most entries one CSV export holds.
const MaxExport = 1_000_000

type handler struct{ svc *Service }

// Register adds this module's routes.
func Register(api huma.API, svc *Service) {
	h := &handler{svc: svc}
	httpx.Register(api, httpx.Route{ID: "listActivity", Method: http.MethodGet, Path: "/activity",
		Summary: "The SMB activity log, newest first (who did what, from where)", Tag: "Activity",
		Access: httpx.Require(rbac.AuditRead)}, h.list)
	httpx.Register(api, httpx.Route{ID: "exportActivity", Method: http.MethodGet, Path: "/activity/export",
		Summary: "The activity log as CSV (same filters; at most a million entries)", Tag: "Activity",
		Access: httpx.Require(rbac.AuditRead), Binary: "text/csv"}, h.export)
}

func (h *handler) list(ctx context.Context, in *listInput) (*listOutput, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 100
	}
	items, next, err := h.svc.List(ctx, in.filter(), limit)
	if err != nil {
		return nil, err
	}
	return &listOutput{Body: Page{Items: items, Next: next}}, nil
}

func (h *handler) export(ctx context.Context, in *listInput) (*huma.StreamResponse, error) {
	f := in.filter()
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		hctx.SetHeader("Content-Type", "text/csv; charset=utf-8")
		hctx.SetHeader("Content-Disposition", `attachment; filename="activity.csv"`)
		hctx.SetStatus(http.StatusOK)
		w := csv.NewWriter(hctx.BodyWriter())
		_ = w.Write([]string{"time", "user", "client_ip", "client_name", "share", "operation", "ok", "error", "path", "new_path"})
		n := 0
		_ = h.svc.Each(ctx, f, MaxExport, func(e ActivityEntry) error {
			n++
			if n%1000 == 0 {
				w.Flush()
			}
			return w.Write([]string{e.At.Format(time.RFC3339), cell(e.User), e.ClientIP, cell(e.ClientName), cell(e.Share),
				e.Op, strconv.FormatBool(e.OK), cell(e.Error), cell(e.Path), cell(e.Path2)})
		})
		w.Flush()
	}}, nil
}

// cell keeps a value from being run as a formula when the CSV is opened in
// a spreadsheet. File names and user names come from clients: a file named
// "=HYPERLINK(...)" must stay text.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
