package rbac

import "github.com/danielgtaylor/huma/v2"

// These methods publish Permission and Role as named enums in the OpenAPI
// spec. The frontend's TypeScript types are generated from that spec, so a
// typo in a permission name on the frontend becomes a compile error.

func (Permission) Schema(r huma.Registry) *huma.Schema {
	values := make([]any, len(all))
	for i, p := range all {
		values[i] = string(p)
	}
	return named(r, "Permission", values)
}

func (Role) Schema(r huma.Registry) *huma.Schema {
	values := make([]any, len(roles))
	for i, info := range roles {
		values[i] = string(info.ID)
	}
	return named(r, "Role", values)
}

func named(r huma.Registry, name string, values []any) *huma.Schema {
	r.Map()[name] = &huma.Schema{Type: huma.TypeString, Enum: values}
	return &huma.Schema{Ref: "#/components/schemas/" + name}
}
