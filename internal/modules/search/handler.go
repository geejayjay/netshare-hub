package search

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

type searchInput struct {
	Q       string `query:"q" doc:"Search terms matching file or folder names and paths."`
	ShareID int64  `query:"shareId" minimum:"0" doc:"Optional share ID to narrow results."`
	Kind    string `query:"kind" enum:",all,file,dir" doc:"Optional filter: all, file, or dir."`
	Limit   int    `query:"limit" minimum:"1" maximum:"100" doc:"Maximum hits to return (default 30)."`
}

type searchOutput struct {
	Body SearchResponse
}

type statusOutput struct {
	Body SearchStatus
}

// Register adds cross-share file search endpoints.
func Register(api huma.API, svc *Service) {
	httpx.Register(api, httpx.Route{
		ID: "searchFiles", Method: http.MethodGet, Path: "/search",
		Summary: "Search files and folders across accessible shares", Tag: "Search",
		Access: httpx.Authenticated(),
	}, func(ctx context.Context, in *searchInput) (*searchOutput, error) {
		p := httpx.PrincipalFrom(ctx)
		res, err := svc.Search(ctx, p.UserID, Query{
			Text:    in.Q,
			ShareID: in.ShareID,
			Kind:    in.Kind,
			Limit:   in.Limit,
		})
		if err != nil {
			return nil, err
		}
		return &searchOutput{Body: res}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "getSearchStatus", Method: http.MethodGet, Path: "/search/status",
		Summary: "Get search index status", Tag: "Search",
		Access: httpx.Authenticated(),
	}, func(ctx context.Context, _ *struct{}) (*statusOutput, error) {
		st, err := svc.Status(ctx)
		if err != nil {
			return nil, err
		}
		return &statusOutput{Body: st}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "reindexSearch", Method: http.MethodPost, Path: "/search/reindex",
		Summary: "Rebuild the cross-share file search index", Tag: "Search",
		Access: httpx.Require(rbac.SettingsWrite),
	}, func(ctx context.Context, _ *struct{}) (*statusOutput, error) {
		if err := svc.Reindex(ctx); err != nil {
			return nil, err
		}
		st, err := svc.Status(ctx)
		if err != nil {
			return nil, err
		}
		return &statusOutput{Body: st}, nil
	})
}
