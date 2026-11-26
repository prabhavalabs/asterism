package api

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/collect/agentingest"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
)

// agentReadMax bounds one AgentService request message at the HTTP layer: a
// unary Push may batch several frames. Each frame is additionally capped at
// agentingest.DefaultMaxFrameBytes by the registry.
const agentReadMax = 4 << 20

// AgentHandler adapts AgentService (agent-token auth) onto the ingest
// registry. It is mounted only in collect.mode = "agent".
type AgentHandler struct {
	reg *agentingest.Registry
}

// NewAgentHandler returns the AgentService adapter.
func NewAgentHandler(reg *agentingest.Registry) *AgentHandler { return &AgentHandler{reg: reg} }

// Register mounts AgentService behind wrap, which must be
// auth.Manager.RequireIngest: only agent tokens may call it.
func (h *AgentHandler) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	path, handler := asterismv1connect.NewAgentServiceHandler(h,
		connect.WithReadMaxBytes(agentReadMax),
		connect.WithCompressMinBytes(1024),
	)
	mux.Handle(path, wrap(handler))
}

// agentHost returns the host an agent token is bound to. RequireIngest has
// already authenticated the caller; this is defence in depth.
func agentHost(ctx context.Context) (string, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || p.Kind != auth.PrincipalKindAgent || !p.Has(auth.ScopeIngest) || p.Subject == "" {
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("AgentService accepts only asterism-agent tokens"))
	}
	return p.Subject, nil
}

// Stream is the primary (HTTP/2 bidi) transport.
func (h *AgentHandler) Stream(ctx context.Context, s *connect.BidiStream[asterismv1.AgentFrame, asterismv1.HubFrame]) error {
	host, err := agentHost(ctx)
	if err != nil {
		return err
	}
	return h.reg.ServeStream(ctx, host, s, s.Peer().Addr)
}

// Push is the HTTP/1.1 fallback transport.
func (h *AgentHandler) Push(ctx context.Context, req *connect.Request[asterismv1.PushRequest]) (*connect.Response[asterismv1.PushResponse], error) {
	host, err := agentHost(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.reg.Push(ctx, host, req.Msg, req.Peer().Addr)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// Commands carries hub frames for a unary session.
func (h *AgentHandler) Commands(ctx context.Context, req *connect.Request[asterismv1.CommandsRequest], s *connect.ServerStream[asterismv1.HubFrame]) error {
	host, err := agentHost(ctx)
	if err != nil {
		return err
	}
	return h.reg.Commands(ctx, host, req.Msg, s.Send)
}

// ListAgents serves AgentAdminService (read scope).
func (h *Handler) ListAgents(_ context.Context, _ *connect.Request[asterismv1.ListAgentsRequest]) (*connect.Response[asterismv1.ListAgentsResponse], error) {
	return connect.NewResponse(h.svc.ListAgents()), nil
}

var _ asterismv1connect.AgentAdminServiceHandler = (*Handler)(nil)
var _ asterismv1connect.AgentServiceHandler = (*AgentHandler)(nil)
