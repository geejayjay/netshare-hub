package notify

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"netshare-hub/internal/platform/httpx"
	"netshare-hub/internal/platform/rbac"
)

type listChannelsOutput struct {
	Body struct {
		Items []NotificationChannel `json:"items" nullable:"false"`
	}
}

type channelOutput struct {
	Body NotificationChannel
}

type createChannelInput struct {
	Body ChannelInput
}

type updateChannelInput struct {
	ID   int64 `path:"id"`
	Body ChannelPatch
}

type channelIDInput struct {
	ID int64 `path:"id"`
}

type testChannelOutput struct {
	Body ChannelTestResult
}

// Register adds notification channel routes.
func Register(api huma.API, svc *Service) {
	httpx.Register(api, httpx.Route{
		ID: "listNotificationChannels", Method: http.MethodGet, Path: "/notifications/channels",
		Summary: "List external notification channels", Tag: "Notifications",
		Access: httpx.Require(rbac.SettingsRead),
	}, func(ctx context.Context, _ *struct{}) (*listChannelsOutput, error) {
		items, err := svc.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &listChannelsOutput{}
		out.Body.Items = items
		return out, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "createNotificationChannel", Method: http.MethodPost, Path: "/notifications/channels",
		Summary: "Create a notification channel", Tag: "Notifications",
		Status: http.StatusCreated, Access: httpx.Require(rbac.SettingsWrite),
	}, func(ctx context.Context, in *createChannelInput) (*channelOutput, error) {
		ch, err := svc.Create(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return &channelOutput{Body: ch}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "updateNotificationChannel", Method: http.MethodPatch, Path: "/notifications/channels/{id}",
		Summary: "Update a notification channel", Tag: "Notifications",
		Access: httpx.Require(rbac.SettingsWrite),
	}, func(ctx context.Context, in *updateChannelInput) (*channelOutput, error) {
		ch, err := svc.Update(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &channelOutput{Body: ch}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "deleteNotificationChannel", Method: http.MethodDelete, Path: "/notifications/channels/{id}",
		Summary: "Delete a notification channel", Tag: "Notifications",
		Status: http.StatusNoContent, Access: httpx.Require(rbac.SettingsWrite),
	}, func(ctx context.Context, in *channelIDInput) (*struct{}, error) {
		if err := svc.Delete(ctx, in.ID); err != nil {
			return nil, err
		}
		return &struct{}{}, nil
	})

	httpx.Register(api, httpx.Route{
		ID: "testNotificationChannel", Method: http.MethodPost, Path: "/notifications/channels/{id}/test",
		Summary: "Send a test notification to a channel", Tag: "Notifications",
		Access: httpx.Require(rbac.SettingsWrite),
	}, func(ctx context.Context, in *channelIDInput) (*testChannelOutput, error) {
		res, err := svc.Test(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &testChannelOutput{Body: res}, nil
	})
}
