package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/prabhavalabs/asterism/internal/api"
	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/showcase"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	mcpserver "github.com/prabhavalabs/asterism/internal/mcp"
	"github.com/prabhavalabs/asterism/internal/service"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

var update = flag.Bool("update", false, "rewrite golden files")

type fakeHost struct{}

func (fakeHost) HostSnapshot() *asterismv1.HostInfo {
	return &asterismv1.HostInfo{
		Id: "host", Hostname: "vps-test", Os: "ubuntu 24.04", Cores: 4,
		MemTotalBytes: 8 << 30, MemUsedBytes: 6 << 30, DiskTotalBytes: 100 << 30, DiskUsedBytes: 40 << 30,
		CpuUtil: 0.25, Load_1: 1.5, UptimeSeconds: 3 * 86400,
	}
}

type hub struct {
	url     string
	tokens  map[string]string
	handler *api.Handler
	mgr     *auth.Manager
}

func newHub(t *testing.T) hub {
	t.Helper()
	readTok, readDigest, _ := auth.GenerateToken()
	actTok, actDigest, _ := auth.GenerateToken()
	mgr, err := auth.NewManager(auth.Options{
		AdminEmail: "a@example.com", AdminPassword: "pw", SessionScopes: []auth.Scope{auth.ScopeRead},
		Tokens: []auth.APIToken{
			{Name: "reader", SHA256: readDigest, Scopes: []auth.Scope{auth.ScopeRead}},
			{Name: "actions-only", SHA256: actDigest, Scopes: []auth.Scope{auth.ScopeActions}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New(64)
	r := ring.New(900)
	now := time.Now().UnixMilli()
	for i := range 60 {
		ts := now - int64(60-i)*1000
		r.Append("host", "cpu.util", ts, 0.1+float64(i%10)/100)
		r.Append("host", "mem.used_bytes", ts, float64(6<<30))
	}
	sc := showcase.New(b, r)
	svc := service.New(b, r, fakeHost{}, sc, "showcase")
	svc.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.ConfigureDocker(sc, sc, service.ActionPolicy{DefaultTimeout: time.Second})
	h := api.New(svc)
	mux := http.NewServeMux()
	mgr.RegisterRoutes(mux)
	h.Register(mux, mgr.RequireSession)
	mux.Handle("/mcp", mgr.RequireSession(mcpserver.HTTPHandler(h, "test")))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return hub{url: srv.URL, tokens: map[string]string{"reader": readTok, "actions": actTok}, handler: h, mgr: mgr}
}

type headerRT struct {
	base   http.RoundTripper
	header http.Header
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.header {
		r.Header[k] = v
	}
	return h.base.RoundTrip(r)
}

func httpSession(t *testing.T, h hub, token string) *sdk.ClientSession {
	t.Helper()
	hc := &http.Client{Transport: headerRT{base: http.DefaultTransport, header: http.Header{"Authorization": {"Bearer " + token}}}}
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: h.url + "/mcp", HTTPClient: hc, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func memSession(t *testing.T, b mcpserver.Backend) *sdk.ClientSession {
	t.Helper()
	ct, st := sdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := mcpserver.NewServer(b, mcpserver.Options{Version: "test"}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

type result struct {
	text       string
	structured map[string]any
	isError    bool
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) result {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var r result
	r.isError = res.IsError
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			r.text += tc.Text
		}
	}
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &r.structured)
	}
	return r
}

func TestToolCatalogGolden(t *testing.T) {
	h := newHub(t)
	cs := memSession(t, h.handler)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil || !a.ReadOnlyHint || !a.IdempotentHint || a.Title == "" {
			t.Errorf("%s: want read-only, idempotent, titled annotations, got %+v", tool.Name, a)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s: missing output schema", tool.Name)
		}
		schema, _ := json.MarshalIndent(tool.InputSchema, "  ", "  ")
		buf.WriteString("## " + tool.Name + " — " + a.Title + "\n" + tool.Description + "\n  " + string(schema) + "\n\n")
	}
	golden := filepath.Join("testdata", "tools.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/mcp -update)", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Fatalf("tool catalog changed; review and run go test ./internal/mcp -update\n%s", buf.String())
	}
}

func TestToolsOverStreamableHTTP(t *testing.T) {
	h := newHub(t)
	cs := httpSession(t, h, h.tokens["reader"])

	ov := callTool(t, cs, "overview", nil)
	if ov.isError || !strings.HasPrefix(ov.text, "vps-test: CPU 25.0%") || ov.structured["host"] == nil {
		t.Fatalf("overview: %+v", ov)
	}
	if n, _ := ov.structured["projects"].(float64); n < 3 {
		t.Fatalf("overview projects = %v", ov.structured["projects"])
	}

	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"list_projects", map[string]any{"order_by": "memory desc", "page_size": 10}, "of 4 projects"},
		{"get_project", map[string]any{"id": "proj:nova-core"}, "nova-core (production"},
		{"list_workloads", map[string]any{"role": "database"}, "workloads."},
		{"get_workload", map[string]any{"id": "ctr:nova-api"}, "nova-api (app container in proj:nova-core)"},
		{"list_containers", map[string]any{"attention_only": true}, "matching containers"},
		{"get_container", map[string]any{"id": "nova-api"}, "nova-api ("},
		{"list_ports", map[string]any{"exposure": "public"}, "public"},
		{"list_ingress", nil, "ingress routes"},
		{"list_images", map[string]any{"filter": "unused"}, "unused"},
		{"list_volumes", map[string]any{"filter": "in_use"}, "volumes"},
		{"list_networks", map[string]any{"user_defined_only": true}, "networks"},
		{"disk_usage", nil, "Root disk 40.0 GiB of 100.0 GiB (40%)"},
		{"list_events", map[string]any{"severities": []string{"warning", "critical"}}, "events"},
		{"tail_logs", map[string]any{"workload_id": "ctr:nova-api", "limit": 5}, "from ctr:nova-api"},
		{"top", map[string]any{"metric": "memory", "limit": 3}, "Top 3 workloads by memory"},
		{"query_metrics", map[string]any{"window": "5m"}, "cpu.util avg"},
		{"get_topology", map[string]any{"focus": "ctr:nova-api"}, "nodes and"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			r := callTool(t, cs, tc.tool, tc.args)
			if r.isError || !strings.Contains(r.text, tc.want) || r.structured == nil {
				t.Fatalf("got error=%v text=%.300q", r.isError, r.text)
			}
		})
	}
}

func TestToolErrorsAreActionable(t *testing.T) {
	h := newHub(t)
	cs := memSession(t, h.handler)
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"get_container", map[string]any{"id": "ctr:nova"}, "did you mean ctr:nova-"},
		{"get_project", map[string]any{"id": "nope-at-all"}, "list them with list_projects"},
		{"list_workloads", map[string]any{"role": "frontend"}, "use one of: app, cache, database"},
		{"list_images", map[string]any{"filter": "big"}, "use all, unused, dangling or in_use"},
		{"list_projects", map[string]any{"order_by": "weight"}, "allowed"},
		{"query_metrics", map[string]any{"window": "2h"}, "longer than the 15m0s"},
		{"tail_logs", map[string]any{"workload_id": ""}, "workload_id is required"},
		{"get_topology", map[string]any{"focus": "ctr:ghost"}, "unknown entity"},
	} {
		r := callTool(t, cs, tc.tool, tc.args)
		if !r.isError || !strings.Contains(r.text, tc.want) {
			t.Errorf("%s(%v): isError=%v text=%q, want %q", tc.tool, tc.args, r.isError, r.text, tc.want)
		}
	}
}

func TestNoEnvironmentValuesLeak(t *testing.T) {
	h := newHub(t)
	cs := memSession(t, h.handler)
	r := callTool(t, cs, "get_workload", map[string]any{"id": "ctr:nova-api"})
	if !strings.Contains(r.text, "DATABASE_URL") {
		t.Fatalf("env names should be listed: %.400q", r.text)
	}
	if strings.Contains(r.text, "postgres://") || strings.Contains(r.text, "DATABASE_URL=") {
		t.Fatalf("env value leaked: %q", r.text)
	}
}

func TestHTTPEndpointRequiresReadToken(t *testing.T) {
	h := newHub(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`
	post := func(hdr http.Header) int {
		req, _ := http.NewRequest(http.MethodPost, h.url+"/mcp", strings.NewReader(body))
		req.Header = hdr
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(http.Header{}); got != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", got)
	}
	if got := post(http.Header{"Authorization": {"Bearer " + h.tokens["actions"]}}); got != http.StatusForbidden {
		t.Errorf("token without read scope: %d", got)
	}
	if got := post(http.Header{"Authorization": {"Bearer " + h.tokens["reader"]}}); got != http.StatusOK {
		t.Errorf("read token: %d", got)
	}

	// A browser session cookie is not enough: /mcp takes bearer tokens only.
	login, _ := http.Post(h.url+"/api/auth/login", "application/json", strings.NewReader(`{"email":"a@example.com","password":"pw"}`))
	if login == nil || login.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %v", login)
	}
	_ = login.Body.Close()
	var cookie string
	for _, c := range login.Cookies() {
		cookie = c.Name + "=" + c.Value
	}
	if got := post(http.Header{"Cookie": {cookie}}); got != http.StatusUnauthorized {
		t.Errorf("session cookie: %d", got)
	}
}

func TestRemoteBackendForStdio(t *testing.T) {
	h := newHub(t)
	cs := memSession(t, mcpserver.NewRemoteBackend(h.url, h.tokens["reader"]))
	if r := callTool(t, cs, "overview", nil); r.isError || !strings.HasPrefix(r.text, "vps-test:") {
		t.Fatalf("overview via remote backend: %+v", r)
	}
	bad := memSession(t, mcpserver.NewRemoteBackend(h.url, "asterism_wrong"))
	if r := callTool(t, bad, "overview", nil); !r.isError || !strings.Contains(r.text, "asterism token generate") {
		t.Fatalf("bad token should explain how to fix it: %+v", r)
	}
	down := memSession(t, mcpserver.NewRemoteBackend("http://127.0.0.1:1", "x"))
	if r := callTool(t, down, "overview", nil); !r.isError || !strings.Contains(r.text, "unreachable") {
		t.Fatalf("hub down should say so: %+v", r)
	}
}
