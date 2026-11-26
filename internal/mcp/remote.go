package mcp

import (
	"context"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
)

type remote struct {
	asterismv1connect.InventoryServiceClient
	asterismv1connect.MetricsServiceClient
	asterismv1connect.TopologyServiceClient
	asterismv1connect.EventsServiceClient
	asterismv1connect.LogsServiceClient
	asterismv1connect.DockerServiceClient
}

// NewRemoteBackend returns a Backend that calls a running hub over ConnectRPC,
// authenticating with an API bearer token (read scope).
func NewRemoteBackend(baseURL, token string) Backend {
	baseURL = strings.TrimRight(baseURL, "/")
	client := &http.Client{Timeout: 30 * time.Second}
	opts := connect.WithInterceptors(bearer(token))
	return &remote{
		InventoryServiceClient: asterismv1connect.NewInventoryServiceClient(client, baseURL, opts),
		MetricsServiceClient:   asterismv1connect.NewMetricsServiceClient(client, baseURL, opts),
		TopologyServiceClient:  asterismv1connect.NewTopologyServiceClient(client, baseURL, opts),
		EventsServiceClient:    asterismv1connect.NewEventsServiceClient(client, baseURL, opts),
		LogsServiceClient:      asterismv1connect.NewLogsServiceClient(client, baseURL, opts),
		DockerServiceClient:    asterismv1connect.NewDockerServiceClient(client, baseURL, opts),
	}
}

type bearer string

func (t bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+string(t))
		return next(ctx, req)
	}
}

func (t bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+string(t))
		return conn
	}
}

func (t bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
