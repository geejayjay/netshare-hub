package shares

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/page"
	"netshare-hub/internal/platform/rbac"
)

// listOptions is this endpoint's pagination policy. Change it here.
var listOptions = page.Options{
	DefaultSize: 25,
	MaxSize:     100,
	DefaultSort: "name",
	SortFields: map[string]string{
		"name":      "name",
		"path":      "path",
		"createdAt": "created_at",
	},
}

// --- request and response types (validation rules live in the tags) ---

// ShareAccessInput grants one user access to a share.
type ShareAccessInput struct {
	UserID int64  `json:"userId" minimum:"1"`
	Level  string `json:"level" enum:"read,write" doc:"\"read\" = read only, \"write\" = read and write."`
}

type CreateShareRequest struct {
	Name            string             `json:"name" minLength:"1" maxLength:"32" example:"media"`
	Path            string             `json:"path" minLength:"2" maxLength:"1024" example:"/srv/media" doc:"Full path of the folder on the server."`
	Comment         string             `json:"comment,omitempty" maxLength:"100"`
	CreateDirectory bool               `json:"createDirectory,omitempty" doc:"Create the folder if it does not exist. Its parent must exist."`
	Access          []ShareAccessInput `json:"access,omitempty" maxItems:"500" doc:"Users who may use the share. Without any, nobody can connect."`
}

// UpdateShareRequest changes only the fields that are present.
type UpdateShareRequest struct {
	Comment *string             `json:"comment,omitempty" maxLength:"100"`
	Access  *[]ShareAccessInput `json:"access,omitempty" maxItems:"500" doc:"Replaces the whole access list."`
}

type listInput struct {
	page.Query
}

type idInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type createInput struct {
	Body CreateShareRequest
}

type updateInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body UpdateShareRequest
}

type shareOutput struct {
	Body Share
}

type listOutput struct {
	Body page.List[Share]
}

type mySharesOutput struct {
	Body page.List[MyShare]
}

// --- routes ---

type handler struct {
	svc *Service
}

// Register adds this module's routes.
func Register(api huma.API, svc *Service) {
	h := &handler{svc: svc}
	const tag = "Shares"

	httpx.Register(api, httpx.Route{
		ID: "listShares", Method: http.MethodGet, Path: "/shares",
		Summary: "List shares", Tag: tag, Access: httpx.Require(rbac.SharesRead),
	}, h.list)

	httpx.Register(api, httpx.Route{
		ID: "listMyShares", Method: http.MethodGet, Path: "/my/shares",
		Summary: "List shares the signed-in user can access", Tag: tag, Access: httpx.Authenticated(),
	}, h.listMine)

	httpx.Register(api, httpx.Route{
		ID: "getShare", Method: http.MethodGet, Path: "/shares/{id}",
		Summary: "Get a share", Tag: tag, Access: httpx.Require(rbac.SharesRead),
	}, h.get)

	httpx.Register(api, httpx.Route{
		ID: "createShare", Method: http.MethodPost, Path: "/shares",
		Summary: "Create a share", Tag: tag, Access: httpx.Require(rbac.SharesWrite),
		Status: http.StatusCreated,
	}, h.create)

	httpx.Register(api, httpx.Route{
		ID: "updateShare", Method: http.MethodPatch, Path: "/shares/{id}",
		Summary: "Update a share's comment or access list", Tag: tag, Access: httpx.Require(rbac.SharesWrite),
	}, h.update)

	httpx.Register(api, httpx.Route{
		ID: "deleteShare", Method: http.MethodDelete, Path: "/shares/{id}",
		Summary: "Stop sharing a folder (files are kept)", Tag: tag, Access: httpx.Require(rbac.SharesWrite),
		Status: http.StatusNoContent,
	}, h.delete)
}

// --- handlers: translate HTTP <-> service, nothing else ---

func (h *handler) list(ctx context.Context, in *listInput) (*listOutput, error) {
	req, err := in.Query.Resolve(listOptions)
	if err != nil {
		return nil, err
	}
	items, total, err := h.svc.List(ctx, req)
	if err != nil {
		return nil, err
	}
	return &listOutput{Body: page.Paged(items, total, req)}, nil
}

func (h *handler) listMine(ctx context.Context, _ *struct{}) (*mySharesOutput, error) {
	p := httpx.PrincipalFrom(ctx)
	items, err := h.svc.ListForUser(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return &mySharesOutput{Body: page.All(items)}, nil
}

func (h *handler) get(ctx context.Context, in *idInput) (*shareOutput, error) {
	sh, err := h.svc.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &shareOutput{Body: sh}, nil
}

func (h *handler) create(ctx context.Context, in *createInput) (*shareOutput, error) {
	sh, err := h.svc.Create(ctx, CreateInput{
		Name:            in.Body.Name,
		Path:            in.Body.Path,
		Comment:         in.Body.Comment,
		CreateDirectory: in.Body.CreateDirectory,
		Access:          accessInputs(in.Body.Access),
	})
	if err != nil {
		return nil, err
	}
	return &shareOutput{Body: sh}, nil
}

func (h *handler) update(ctx context.Context, in *updateInput) (*shareOutput, error) {
	update := UpdateInput{Comment: in.Body.Comment}
	if in.Body.Access != nil {
		access := accessInputs(*in.Body.Access)
		update.Access = &access
	}
	sh, err := h.svc.Update(ctx, in.ID, update)
	if err != nil {
		return nil, err
	}
	return &shareOutput{Body: sh}, nil
}

func (h *handler) delete(ctx context.Context, in *idInput) (*struct{}, error) {
	return nil, h.svc.Delete(ctx, in.ID)
}

func accessInputs(in []ShareAccessInput) []AccessInput {
	out := make([]AccessInput, len(in))
	for i, a := range in {
		out[i] = AccessInput{UserID: a.UserID, Level: Level(a.Level)}
	}
	return out
}
