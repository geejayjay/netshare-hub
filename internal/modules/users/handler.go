package users

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
	DefaultSort: "username",
	SortFields: map[string]string{
		"username":    "username",
		"displayName": "display_name",
		"role":        "role",
		"createdAt":   "created_at",
	},
}

// --- request and response types (validation rules live in the tags) ---

type listInput struct {
	page.Query
}

type idInput struct {
	ID int64 `path:"id" minimum:"1"`
}

// CreateUserRequest and UpdateUserRequest are named types on purpose: the name
// becomes the schema name in the OpenAPI spec and the generated TypeScript.
type CreateUserRequest struct {
	Username    string `json:"username" minLength:"2" maxLength:"32" example:"jay"`
	DisplayName string `json:"displayName,omitempty" maxLength:"100" example:"Jay"`
	Role        string `json:"role" minLength:"1" example:"user"`
	Password    string `json:"password" minLength:"8" maxLength:"128"`
	SMBAccess   *bool  `json:"smbAccess,omitempty" doc:"Give the user a Samba account so they can be added to shares. Defaults to true."`
}

// UpdateUserRequest changes only the fields that are present.
type UpdateUserRequest struct {
	DisplayName *string `json:"displayName,omitempty" maxLength:"100"`
	Role        *string `json:"role,omitempty" minLength:"1"`
	Disabled    *bool   `json:"disabled,omitempty"`
	Password    *string `json:"password,omitempty" minLength:"8" maxLength:"128"`
	SMBAccess   *bool   `json:"smbAccess,omitempty" doc:"Turning this on needs \"password\" in the same request. Turning it off removes the user from every share."`
}

type createInput struct {
	Body CreateUserRequest
}

type updateInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body UpdateUserRequest
}

type userOutput struct {
	Body User
}

type listOutput struct {
	Body page.List[User]
}

type rolesOutput struct {
	Body page.List[rbac.RoleInfo]
}

// --- routes ---

type handler struct {
	svc *Service
}

// Register adds this module's routes. The table below is the whole public
// surface of the module, with the permission each route needs.
func Register(api huma.API, svc *Service) {
	h := &handler{svc: svc}
	const tag = "Users"

	httpx.Register(api, httpx.Route{
		ID: "listUsers", Method: http.MethodGet, Path: "/users",
		Summary: "List users", Tag: tag, Access: httpx.Require(rbac.UsersRead),
	}, h.list)

	httpx.Register(api, httpx.Route{
		ID: "getUser", Method: http.MethodGet, Path: "/users/{id}",
		Summary: "Get a user", Tag: tag, Access: httpx.Require(rbac.UsersRead),
	}, h.get)

	httpx.Register(api, httpx.Route{
		ID: "createUser", Method: http.MethodPost, Path: "/users",
		Summary: "Create a user", Tag: tag, Access: httpx.Require(rbac.UsersWrite),
		Status: http.StatusCreated,
	}, h.create)

	httpx.Register(api, httpx.Route{
		ID: "updateUser", Method: http.MethodPatch, Path: "/users/{id}",
		Summary: "Update a user", Tag: tag, Access: httpx.Require(rbac.UsersWrite),
	}, h.update)

	httpx.Register(api, httpx.Route{
		ID: "deleteUser", Method: http.MethodDelete, Path: "/users/{id}",
		Summary: "Delete a user", Tag: tag, Access: httpx.Require(rbac.UsersWrite),
		Status: http.StatusNoContent,
	}, h.delete)

	httpx.Register(api, httpx.Route{
		ID: "listRoles", Method: http.MethodGet, Path: "/roles",
		Summary: "List roles and their permissions", Tag: tag, Access: httpx.Require(rbac.UsersRead),
	}, h.roles)
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

func (h *handler) get(ctx context.Context, in *idInput) (*userOutput, error) {
	u, err := h.svc.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &userOutput{Body: u}, nil
}

func (h *handler) create(ctx context.Context, in *createInput) (*userOutput, error) {
	u, err := h.svc.Create(ctx, CreateInput{
		Username:    in.Body.Username,
		DisplayName: in.Body.DisplayName,
		Role:        rbac.Role(in.Body.Role),
		Password:    in.Body.Password,
		SMBAccess:   in.Body.SMBAccess == nil || *in.Body.SMBAccess,
	})
	if err != nil {
		return nil, err
	}
	return &userOutput{Body: u}, nil
}

func (h *handler) update(ctx context.Context, in *updateInput) (*userOutput, error) {
	update := UpdateInput{
		DisplayName: in.Body.DisplayName,
		Disabled:    in.Body.Disabled,
		Password:    in.Body.Password,
		SMBAccess:   in.Body.SMBAccess,
	}
	if in.Body.Role != nil {
		role := rbac.Role(*in.Body.Role)
		update.Role = &role
	}
	u, err := h.svc.Update(ctx, httpx.PrincipalFrom(ctx).UserID, in.ID, update)
	if err != nil {
		return nil, err
	}
	return &userOutput{Body: u}, nil
}

func (h *handler) delete(ctx context.Context, in *idInput) (*struct{}, error) {
	return nil, h.svc.Delete(ctx, httpx.PrincipalFrom(ctx).UserID, in.ID)
}

// roles is an example of an unpaginated list: same envelope, no "page" block.
func (h *handler) roles(context.Context, *struct{}) (*rolesOutput, error) {
	return &rolesOutput{Body: page.All(rbac.Roles())}, nil
}
