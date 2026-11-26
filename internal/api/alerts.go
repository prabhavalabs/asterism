package api

import (
	"context"

	"connectrpc.com/connect"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/service"
)

func admin(ctx context.Context, peer connect.Peer) service.AdminRequest {
	return service.AdminRequest{Principal: principal(ctx), RemoteAddr: peer.Addr}
}

// ---------------------------------------------------------------------------
// AlertsService
// ---------------------------------------------------------------------------

func (h *Handler) ListAlerts(_ context.Context, req *connect.Request[asterismv1.ListAlertsRequest]) (*connect.Response[asterismv1.ListAlertsResponse], error) {
	return respond(h.svc.ListAlerts(req.Msg))
}

func (h *Handler) ListRules(_ context.Context, _ *connect.Request[asterismv1.ListRulesRequest]) (*connect.Response[asterismv1.ListRulesResponse], error) {
	return connect.NewResponse(h.svc.ListRules()), nil
}

func (h *Handler) UpdateRule(ctx context.Context, req *connect.Request[asterismv1.UpdateRuleRequest]) (*connect.Response[asterismv1.AlertRule], error) {
	return respond(h.svc.UpdateRule(ctx, admin(ctx, req.Peer()), req.Msg))
}

func (h *Handler) ListSilences(_ context.Context, req *connect.Request[asterismv1.ListSilencesRequest]) (*connect.Response[asterismv1.ListSilencesResponse], error) {
	return connect.NewResponse(h.svc.ListSilences(req.Msg)), nil
}

func (h *Handler) CreateSilence(ctx context.Context, req *connect.Request[asterismv1.CreateSilenceRequest]) (*connect.Response[asterismv1.Silence], error) {
	return respond(h.svc.CreateSilence(ctx, admin(ctx, req.Peer()), req.Msg))
}

func (h *Handler) DeleteSilence(ctx context.Context, req *connect.Request[asterismv1.DeleteSilenceRequest]) (*connect.Response[asterismv1.DeleteSilenceResponse], error) {
	if err := h.svc.DeleteSilence(ctx, admin(ctx, req.Peer()), req.Msg.Id); err != nil {
		return nil, toConnectErr(err)
	}
	return connect.NewResponse(&asterismv1.DeleteSilenceResponse{}), nil
}

// ---------------------------------------------------------------------------
// NotificationsService
// ---------------------------------------------------------------------------

func (h *Handler) ListChannels(ctx context.Context, _ *connect.Request[asterismv1.ListChannelsRequest]) (*connect.Response[asterismv1.ListChannelsResponse], error) {
	return respond(h.svc.ListChannels(ctx))
}

func (h *Handler) CreateChannel(ctx context.Context, req *connect.Request[asterismv1.CreateChannelRequest]) (*connect.Response[asterismv1.NotificationChannel], error) {
	ch := req.Msg.GetChannel()
	if ch == nil {
		ch = &asterismv1.NotificationChannel{}
	}
	return respond(h.svc.CreateChannel(ctx, admin(ctx, req.Peer()), ch))
}

func (h *Handler) UpdateChannel(ctx context.Context, req *connect.Request[asterismv1.UpdateChannelRequest]) (*connect.Response[asterismv1.NotificationChannel], error) {
	return respond(h.svc.UpdateChannel(ctx, admin(ctx, req.Peer()), req.Msg))
}

func (h *Handler) DeleteChannel(ctx context.Context, req *connect.Request[asterismv1.DeleteChannelRequest]) (*connect.Response[asterismv1.DeleteChannelResponse], error) {
	if err := h.svc.DeleteChannel(ctx, admin(ctx, req.Peer()), req.Msg.Id); err != nil {
		return nil, toConnectErr(err)
	}
	return connect.NewResponse(&asterismv1.DeleteChannelResponse{}), nil
}

func (h *Handler) TestChannel(ctx context.Context, req *connect.Request[asterismv1.TestChannelRequest]) (*connect.Response[asterismv1.TestChannelResponse], error) {
	d, err := h.svc.TestChannel(ctx, admin(ctx, req.Peer()), req.Msg.Id)
	if err != nil {
		return nil, toConnectErr(err)
	}
	return connect.NewResponse(&asterismv1.TestChannelResponse{Delivery: d}), nil
}

func (h *Handler) ListDeliveries(ctx context.Context, req *connect.Request[asterismv1.ListDeliveriesRequest]) (*connect.Response[asterismv1.ListDeliveriesResponse], error) {
	return respond(h.svc.ListDeliveries(ctx, req.Msg))
}

func (h *Handler) GetWebPushConfig(ctx context.Context, _ *connect.Request[asterismv1.GetWebPushConfigRequest]) (*connect.Response[asterismv1.GetWebPushConfigResponse], error) {
	return connect.NewResponse(h.svc.GetWebPushConfig(ctx)), nil
}

func (h *Handler) SubscribeWebPush(ctx context.Context, req *connect.Request[asterismv1.SubscribeWebPushRequest]) (*connect.Response[asterismv1.SubscribeWebPushResponse], error) {
	return respond(h.svc.SubscribeWebPush(ctx, admin(ctx, req.Peer()), req.Msg))
}

func (h *Handler) UnsubscribeWebPush(ctx context.Context, req *connect.Request[asterismv1.UnsubscribeWebPushRequest]) (*connect.Response[asterismv1.UnsubscribeWebPushResponse], error) {
	if err := h.svc.UnsubscribeWebPush(ctx, admin(ctx, req.Peer()), req.Msg); err != nil {
		return nil, toConnectErr(err)
	}
	return connect.NewResponse(&asterismv1.UnsubscribeWebPushResponse{}), nil
}

func (h *Handler) ListWebPushSubscriptions(ctx context.Context, _ *connect.Request[asterismv1.ListWebPushSubscriptionsRequest]) (*connect.Response[asterismv1.ListWebPushSubscriptionsResponse], error) {
	return respond(h.svc.ListWebPushSubscriptions(ctx))
}
