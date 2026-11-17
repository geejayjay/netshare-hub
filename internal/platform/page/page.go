// Package page is the one place that defines how list endpoints work.
//
// Every list endpoint returns the same envelope:
//
//	{ "items": [...], "page": { "page": 1, "pageSize": 25, "total": 80, ... } }
//
// "page" is simply absent when the endpoint is not paginated, so switching an
// endpoint between the two is a one-line change and never breaks the client:
//
//	return page.Paged(items, total, req)   // paginated
//	return page.All(items)                 // everything, no paging
//
// To make an endpoint paginated:
//  1. embed page.Query in its input struct,
//  2. call in.Query.Resolve(opts) with that endpoint's Options,
//  3. pass the Request to the repo and return page.Paged(...).
package page

import (
	"strings"

	"netshare-hub/internal/platform/errs"
)

// Query holds the query-string parameters. Embed it in a handler input struct.
type Query struct {
	Page     int    `query:"page" minimum:"1" default:"1" doc:"Page number, starting at 1."`
	PageSize int    `query:"pageSize" minimum:"1" maximum:"500" doc:"Items per page. Each endpoint has its own default and maximum."`
	Sort     string `query:"sort" maxLength:"64" doc:"Field to sort by. Prefix with - for descending, e.g. -createdAt."`
	Search   string `query:"q" maxLength:"200" doc:"Free-text search."`
}

// Options lets each endpoint tune pagination. Zero values fall back to Defaults.
type Options struct {
	DefaultSize int
	MaxSize     int
	// SortFields maps the public field name to the SQL column. Only listed
	// fields can be sorted on, which also makes ORDER BY injection-safe.
	SortFields map[string]string
	// DefaultSort uses the same syntax as the query parameter, e.g. "-createdAt".
	DefaultSort string
}

// Defaults apply when an endpoint leaves an option empty. Change them here to
// change the whole API.
var Defaults = Options{DefaultSize: 25, MaxSize: 100}

// Request is the resolved, validated form that services and repos use.
type Request struct {
	Page       int
	Size       int
	SortColumn string // SQL column, already checked against the allow-list; "" = none
	Desc       bool
	Search     string
}

func (r Request) Offset() int { return (r.Page - 1) * r.Size }

// OrderBy returns a safe "column ASC|DESC" fragment, or fallback when no sort
// was requested.
func (r Request) OrderBy(fallback string) string {
	if r.SortColumn == "" {
		return fallback
	}
	if r.Desc {
		return r.SortColumn + " DESC"
	}
	return r.SortColumn + " ASC"
}

// Resolve applies the endpoint's options and validates the sort field.
func (q Query) Resolve(o Options) (Request, error) {
	if o.DefaultSize <= 0 {
		o.DefaultSize = Defaults.DefaultSize
	}
	if o.MaxSize <= 0 {
		o.MaxSize = Defaults.MaxSize
	}

	r := Request{Page: max(q.Page, 1), Size: q.PageSize, Search: strings.TrimSpace(q.Search)}
	if r.Size <= 0 {
		r.Size = o.DefaultSize
	}
	r.Size = min(r.Size, o.MaxSize)

	sort := q.Sort
	if sort == "" {
		sort = o.DefaultSort
	}
	if sort != "" {
		field := strings.TrimPrefix(sort, "-")
		column, ok := o.SortFields[field]
		if !ok {
			return Request{}, errs.Invalid(errs.CodeValidationFailed, "Some fields need your attention.",
				errs.FieldError{Field: "sort", Code: "invalid_choice", Message: "Cannot sort by \"" + field + "\"."})
		}
		r.SortColumn = column
		r.Desc = strings.HasPrefix(sort, "-")
	}
	return r, nil
}

// Meta describes the current page. It is omitted for unpaginated lists.
type Meta struct {
	Page       int  `json:"page"`
	PageSize   int  `json:"pageSize"`
	Total      int  `json:"total" doc:"Total number of matching items across all pages."`
	TotalPages int  `json:"totalPages"`
	HasNext    bool `json:"hasNext"`
}

// List is the response envelope for every list endpoint.
type List[T any] struct {
	Items []T   `json:"items" nullable:"false"`
	Page  *Meta `json:"page,omitempty" doc:"Present only when the list is paginated."`
}

// Paged builds a paginated response.
func Paged[T any](items []T, total int, r Request) List[T] {
	pages := 0
	if r.Size > 0 {
		pages = (total + r.Size - 1) / r.Size
	}
	return List[T]{
		Items: nonNil(items),
		Page:  &Meta{Page: r.Page, PageSize: r.Size, Total: total, TotalPages: pages, HasNext: r.Page < pages},
	}
}

// All builds an unpaginated response with every item.
func All[T any](items []T) List[T] {
	return List[T]{Items: nonNil(items)}
}

// nonNil makes sure an empty list is encoded as [] and not null.
func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
