package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/prabhavalabs/asterism/internal/auth"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/service"
)

// principal returns the caller placed in the context by auth.RequireSession.
func principal(ctx context.Context) auth.Principal {
	p, _ := auth.PrincipalFrom(ctx)
	return p
}

// respond wraps a service result, mapping service error kinds to Connect codes.
func respond[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, toConnectErr(err)
	}
	return connect.NewResponse(msg), nil
}

// toConnectErr maps service error kinds to Connect codes.
func toConnectErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrInvalidArgument):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, service.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, service.ErrFailedPrecondition):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, service.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, service.ErrAborted):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, service.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func (h *Handler) ListContainers(ctx context.Context, req *connect.Request[asterismv1.ListContainersRequest]) (*connect.Response[asterismv1.ListContainersResponse], error) {
	resp, err := h.svc.ListContainers(req.Msg)
	if err != nil {
		return nil, toConnectErr(err)
	}
	resp.Capabilities = h.svc.Capabilities(principal(ctx))
	return connect.NewResponse(resp), nil
}

func (h *Handler) GetContainer(ctx context.Context, req *connect.Request[asterismv1.GetContainerRequest]) (*connect.Response[asterismv1.GetContainerResponse], error) {
	resp, err := h.svc.GetContainer(req.Msg.Id)
	if err != nil {
		return nil, toConnectErr(err)
	}
	resp.Capabilities = h.svc.Capabilities(principal(ctx))
	return connect.NewResponse(resp), nil
}

func (h *Handler) ListImages(_ context.Context, req *connect.Request[asterismv1.ListImagesRequest]) (*connect.Response[asterismv1.ListImagesResponse], error) {
	return respond(h.svc.ListImages(req.Msg))
}

func (h *Handler) ListVolumes(_ context.Context, req *connect.Request[asterismv1.ListVolumesRequest]) (*connect.Response[asterismv1.ListVolumesResponse], error) {
	return respond(h.svc.ListVolumes(req.Msg))
}

func (h *Handler) ListNetworks(_ context.Context, req *connect.Request[asterismv1.ListNetworksRequest]) (*connect.Response[asterismv1.ListNetworksResponse], error) {
	return respond(h.svc.ListNetworks(req.Msg))
}

func (h *Handler) GetDiskUsage(_ context.Context, _ *connect.Request[asterismv1.GetDiskUsageRequest]) (*connect.Response[asterismv1.GetDiskUsageResponse], error) {
	return connect.NewResponse(h.svc.DiskUsage()), nil
}

func (h *Handler) action(ctx context.Context, peer connect.Peer, action asterismv1.ContainerAction, id string, timeout *uint32) (*connect.Response[asterismv1.ContainerActionResponse], error) {
	resp, err := h.svc.PerformContainerAction(ctx, principal(ctx), service.ActionRequest{
		Action:         action,
		Ref:            id,
		TimeoutSeconds: timeout,
		RemoteAddr:     peer.Addr,
	})
	if err != nil {
		return nil, toConnectErr(err)
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) StartContainer(ctx context.Context, req *connect.Request[asterismv1.StartContainerRequest]) (*connect.Response[asterismv1.ContainerActionResponse], error) {
	return h.action(ctx, req.Peer(), asterismv1.ContainerAction_CONTAINER_ACTION_START, req.Msg.Id, nil)
}

func (h *Handler) StopContainer(ctx context.Context, req *connect.Request[asterismv1.StopContainerRequest]) (*connect.Response[asterismv1.ContainerActionResponse], error) {
	return h.action(ctx, req.Peer(), asterismv1.ContainerAction_CONTAINER_ACTION_STOP, req.Msg.Id, req.Msg.TimeoutSeconds)
}

func (h *Handler) RestartContainer(ctx context.Context, req *connect.Request[asterismv1.RestartContainerRequest]) (*connect.Response[asterismv1.ContainerActionResponse], error) {
	return h.action(ctx, req.Peer(), asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, req.Msg.Id, req.Msg.TimeoutSeconds)
}

func (h *Handler) PauseContainer(ctx context.Context, req *connect.Request[asterismv1.PauseContainerRequest]) (*connect.Response[asterismv1.ContainerActionResponse], error) {
	return h.action(ctx, req.Peer(), asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE, req.Msg.Id, nil)
}

func (h *Handler) UnpauseContainer(ctx context.Context, req *connect.Request[asterismv1.UnpauseContainerRequest]) (*connect.Response[asterismv1.ContainerActionResponse], error) {
	return h.action(ctx, req.Peer(), asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE, req.Msg.Id, nil)
}
