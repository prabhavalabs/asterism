// Package mcp exposes the observatory to MCP clients (Claude, IDEs, scripts).
//
// The tools are thin adapters over the same ConnectRPC services the web UI
// uses, so every interface returns the same answers. Inside the hub the
// backend is the API handler itself (Streamable HTTP at /mcp); `asterism mcp`
// uses ConnectRPC clients against a running hub (stdio). All tools are
// read-only.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/prabhavalabs/asterism/internal/auth"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
)

// Backend is the read-only surface the tools need. Both the ConnectRPC
// clients and the hub's api.Handler satisfy it, because connect-go clients
// and handlers share method signatures.
type Backend interface {
	asterismv1connect.InventoryServiceClient
	asterismv1connect.MetricsServiceClient
	asterismv1connect.TopologyServiceClient
	asterismv1connect.EventsServiceClient
	asterismv1connect.LogsServiceClient
	asterismv1connect.DockerServiceClient
	AlertsReader
}

// AlertsReader is the read-only part of AlertsService the tools use.
type AlertsReader interface {
	ListAlerts(context.Context, *connect.Request[asterismv1.ListAlertsRequest]) (*connect.Response[asterismv1.ListAlertsResponse], error)
}

// Options configures the MCP server.
type Options struct {
	// Version is reported to clients in the initialize handshake.
	Version string
	// BindContext, when set, prepares the context for every backend call
	// (the hub uses it to attach the authenticated principal).
	BindContext func(context.Context) context.Context
}

const instructions = `Asterism observes one VPS: projects (compose projects, systemd units, processes), their workloads and containers, open ports, reverse-proxy ingress routes, Docker images/volumes/networks, events, logs and live metrics.

Start with "overview" for host vitals, project health and the biggest memory users, then drill down. Entity IDs are reusable across tools: host, proj:<name>, ctr:<name>, unit:<name>, proc:<name>, ingress:<kind>/<name>. CPU is reported as cpu_pct (percent of all cores for the host, of one core per workload unless stated), memory in bytes, timestamps in RFC 3339 UTC.

All tools are read-only. Lists are paged (page, page_size 10-100) and accept a free-text query and an order_by such as "memory desc". Metric history covers the last 15 minutes at 1 s resolution. Environment variable values and secrets are never returned.`

type server struct {
	b    Backend
	bind func(context.Context) context.Context
}

// NewServer returns an MCP server with every Asterism tool registered.
func NewServer(b Backend, opts Options) *sdk.Server {
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	s := sdk.NewServer(&sdk.Implementation{Name: "asterism", Title: "Asterism VPS observatory", Version: version}, &sdk.ServerOptions{
		Instructions: instructions,
		SchemaCache:  schemaCache,
	})
	srv := &server{b: b, bind: opts.BindContext}
	srv.register(s)
	return s
}

var schemaCache = sdk.NewSchemaCache()

// HTTPHandler serves stateless Streamable HTTP. It must sit behind
// authentication that stores an auth.Principal in the request context; only
// bearer tokens with the read scope are accepted (no browser cookies, so the
// endpoint cannot be driven cross-site).
func HTTPHandler(b Backend, version string) http.Handler {
	h := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		p, _ := auth.PrincipalFrom(r.Context())
		return NewServer(b, Options{
			Version:     version,
			BindContext: func(ctx context.Context) context.Context { return auth.WithPrincipal(ctx, p) },
		})
	}, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok || p.Kind != "token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="asterism"`)
			http.Error(w, "MCP requires an API bearer token: asterism token generate --name <client> --scope read", http.StatusUnauthorized)
			return
		}
		if !p.Has(auth.ScopeRead) {
			http.Error(w, "token lacks the read scope", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *server) ctx(ctx context.Context) context.Context {
	if s.bind != nil {
		return s.bind(ctx)
	}
	return ctx
}

var (
	falseVal = false
	readOnly = func(title string) *sdk.ToolAnnotations {
		return &sdk.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &falseVal}
	}
)

// addTool registers a read-only tool whose handler returns a short summary
// and a structured value. The client receives the summary followed by the
// compact JSON (for clients that ignore structuredContent) plus the
// structured content itself.
func addTool[In, Out any](s *sdk.Server, name, title, desc string, fn func(context.Context, In) (string, Out, error)) {
	sdk.AddTool(s, &sdk.Tool{Name: name, Description: desc, Annotations: readOnly(title)},
		func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
			summary, out, err := fn(ctx, in)
			if err != nil {
				var zero Out
				return nil, zero, toolError(err)
			}
			body, err := json.Marshal(out)
			if err != nil {
				var zero Out
				return nil, zero, fmt.Errorf("encode result: %w", err)
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: summary + "\n\n" + string(body)}}}, out, nil
		})
}

// toolError turns backend errors into short, actionable messages.
func toolError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Code() {
		case connect.CodeUnauthenticated:
			return errors.New("the hub rejected the API token; create one with `asterism token generate --name mcp --scope read` and add it to asterism.toml")
		case connect.CodePermissionDenied:
			return errors.New("the API token lacks the read scope")
		case connect.CodeUnavailable:
			return fmt.Errorf("the Asterism hub is unreachable (%s); is `asterism serve` running?", ce.Message())
		default:
			return errors.New(ce.Message())
		}
	}
	return err
}
