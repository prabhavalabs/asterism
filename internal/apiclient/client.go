// Package apiclient builds ConnectRPC clients for a running Asterism hub.
// The CLI views and `asterism mcp` share it, so they authenticate and reach
// the hub the same way.
package apiclient

import (
	"context"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
)

// DefaultTimeout bounds a single unary call.
const DefaultTimeout = 30 * time.Second

// Client embeds the read-only service clients. Method names do not collide
// across services, so every RPC is callable directly on the Client.
type Client struct {
	asterismv1connect.InventoryServiceClient
	asterismv1connect.MetricsServiceClient
	asterismv1connect.TopologyServiceClient
	asterismv1connect.EventsServiceClient
	asterismv1connect.LogsServiceClient
	asterismv1connect.DockerServiceClient
	asterismv1connect.AlertsServiceClient

	// BaseURL is the hub URL without a trailing slash.
	BaseURL string
}

// New returns a Client for baseURL that sends token as a bearer credential.
// An empty token sends no Authorization header.
func New(baseURL, token string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	httpClient := &http.Client{Timeout: DefaultTimeout}
	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(Bearer(token)))
	}
	return &Client{
		InventoryServiceClient: asterismv1connect.NewInventoryServiceClient(httpClient, baseURL, opts...),
		MetricsServiceClient:   asterismv1connect.NewMetricsServiceClient(httpClient, baseURL, opts...),
		TopologyServiceClient:  asterismv1connect.NewTopologyServiceClient(httpClient, baseURL, opts...),
		EventsServiceClient:    asterismv1connect.NewEventsServiceClient(httpClient, baseURL, opts...),
		LogsServiceClient:      asterismv1connect.NewLogsServiceClient(httpClient, baseURL, opts...),
		DockerServiceClient:    asterismv1connect.NewDockerServiceClient(httpClient, baseURL, opts...),
		AlertsServiceClient:    asterismv1connect.NewAlertsServiceClient(httpClient, baseURL, opts...),
		BaseURL:                baseURL,
	}
}

// Bearer is a client interceptor that sets "Authorization: Bearer <token>".
type Bearer string

// WrapUnary implements connect.Interceptor.
func (t Bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+string(t))
		return next(ctx, req)
	}
}

// WrapStreamingClient implements connect.Interceptor.
func (t Bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+string(t))
		return conn
	}
}

// WrapStreamingHandler implements connect.Interceptor; it is a no-op.
func (t Bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
