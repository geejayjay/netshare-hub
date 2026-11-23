package httpx

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/errs"
	"netshare-hub/internal/platform/rbac"
)

// HeaderCSRF carries the CSRF token on every state-changing request.
const HeaderCSRF = "X-CSRF-Token"

// SecurityScheme is the name of the session-cookie scheme in the OpenAPI spec.
const SecurityScheme = "session"

// Access says who may call a route. Every route must pick one, so a route
// can never be left open by accident.
type Access struct {
	public     bool
	permission rbac.Permission
}

// Public routes need no login (health, login, first-run setup).
func Public() Access { return Access{public: true} }

// Authenticated routes need a login but no specific permission.
func Authenticated() Access { return Access{} }

// Require routes need a login and the given permission.
func Require(p rbac.Permission) Access { return Access{permission: p} }

// Route describes one endpoint. It reads like a table row on purpose:
//
//	httpx.Register(api, httpx.Route{
//		ID: "listUsers", Method: http.MethodGet, Path: "/users",
//		Summary: "List users", Tag: "Users",
//		Access: httpx.Require(rbac.UsersRead),
//	}, h.list)
type Route struct {
	ID      string // operation ID, becomes the function name in generated clients
	Method  string
	Path    string
	Summary string
	Tag     string
	Access  Access
	Status  int // success status; 0 = framework default (200, or 204 with no body)
	// Binary marks a route that streams a file instead of JSON (the handler
	// returns *huma.StreamResponse); it is the response's content type.
	Binary string
	// MaxBody raises the request body limit (default 1 MiB), for the few
	// routes that take a file in JSON.
	MaxBody int64
}

// Register adds a route to the API. It is the only way routes are added, so
// access control, CSRF and error conversion can never be forgotten.
func Register[I, O any](api huma.API, r Route, handler func(context.Context, *I) (*O, error)) {
	op := huma.Operation{
		OperationID:   r.ID,
		Method:        r.Method,
		Path:          r.Path,
		Summary:       r.Summary,
		Tags:          []string{r.Tag},
		DefaultStatus: r.Status,
		Middlewares:   huma.Middlewares{r.Access.guard(r.Method)},
		MaxBodyBytes:  r.MaxBody,
	}
	if !r.Access.public {
		op.Security = []map[string][]string{{SecurityScheme: {}}}
	}
	if r.Access.permission != "" {
		op.Extensions = map[string]any{"x-permission": string(r.Access.permission)}
	}
	if r.Binary != "" {
		file := &huma.Response{
			Description: "The file's content.",
			Content:     map[string]*huma.MediaType{r.Binary: {Schema: &huma.Schema{Type: "string", Format: "binary"}}},
		}
		op.Responses = map[string]*huma.Response{"200": file}
		if r.Binary == "application/octet-stream" {
			op.Responses["206"] = &huma.Response{Description: "Part of the file (a Range request).", Content: file.Content}
		}
	}

	huma.Register(api, op, func(ctx context.Context, in *I) (*O, error) {
		out, err := handler(ctx, in)
		if err != nil {
			return nil, FromError(ctx, err)
		}
		return out, nil
	})
}

// guard runs before the request body is read or validated, so an anonymous
// caller learns nothing about an endpoint except that it needs a login.
func (a Access) guard(method string) func(huma.Context, func(huma.Context)) {
	mutating := method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions

	return func(hctx huma.Context, next func(huma.Context)) {
		if a.public {
			next(hctx)
			return
		}
		ctx := hctx.Context()
		p := PrincipalFrom(ctx)

		var denied *errs.E
		switch {
		case p == nil:
			denied = errs.Unauthenticated(errs.CodeUnauthenticated, "Please log in to continue.")
		case mutating && !validCSRF(p, hctx.Header(HeaderCSRF)):
			denied = errs.Forbidden(errs.CodeCSRFInvalid, "Your session could not be verified. Please reload the page and try again.")
		case a.permission != "" && !p.Can(a.permission):
			denied = errs.Forbidden(errs.CodeForbidden, "You do not have permission to do this.")
		}
		if denied != nil {
			writeHuma(hctx, FromError(ctx, denied))
			return
		}
		next(hctx)
	}
}

// Protect applies the same checks as Register (login, CSRF on changes,
// permission) to a plain handler: for the few endpoints that are not JSON
// and must stream a request body (tus uploads).
func Protect(a Access, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.public {
			next.ServeHTTP(w, r)
			return
		}
		mutating := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		p := PrincipalFrom(r.Context())
		var denied *errs.E
		switch {
		case p == nil:
			denied = errs.Unauthenticated(errs.CodeUnauthenticated, "Please log in to continue.")
		case mutating && !validCSRF(p, r.Header.Get(HeaderCSRF)):
			denied = errs.Forbidden(errs.CodeCSRFInvalid, "Your session could not be verified. Please reload the page and try again.")
		case a.permission != "" && !p.Can(a.permission):
			denied = errs.Forbidden(errs.CodeForbidden, "You do not have permission to do this.")
		}
		if denied != nil {
			WriteProblem(w, FromError(r.Context(), denied))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validCSRF(p *Principal, token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(p.CSRFToken)) == 1
}

func writeHuma(hctx huma.Context, p *Problem) {
	hctx.SetHeader("Content-Type", "application/problem+json")
	hctx.SetStatus(p.Status)
	_ = json.NewEncoder(hctx.BodyWriter()).Encode(p)
}
