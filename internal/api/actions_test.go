package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/showcase"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
	"github.com/prabhavalabs/asterism/internal/service"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

func newTestServer(t *testing.T, allowActions bool) (*httptest.Server, map[string]string) {
	t.Helper()
	readTok, readDigest, _ := auth.GenerateToken()
	actTok, actDigest, _ := auth.GenerateToken()
	scopes := []auth.Scope{auth.ScopeRead}
	if allowActions {
		scopes = append(scopes, auth.ScopeActions)
	}
	mgr, err := auth.NewManager(auth.Options{
		AdminEmail: "a@example.com", AdminPassword: "pw", SessionScopes: scopes,
		Tokens: []auth.APIToken{
			{Name: "reader", SHA256: readDigest, Scopes: []auth.Scope{auth.ScopeRead}},
			{Name: "operator", SHA256: actDigest, Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeActions}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New(64)
	r := ring.New(30)
	sc := showcase.New(b, r)
	svc := service.New(b, r, nil, sc, "showcase")
	svc.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.ConfigureDocker(sc, sc, service.ActionPolicy{Enabled: allowActions, DefaultTimeout: time.Second})
	mux := http.NewServeMux()
	mgr.RegisterRoutes(mux)
	New(svc).Register(mux, mgr.RequireSession)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, map[string]string{"reader": readTok, "operator": actTok}
}

func client(srv *httptest.Server, token string, protoHeader bool) (asterismv1connect.DockerServiceClient, asterismv1connect.ActionsServiceClient, asterismv1connect.InventoryServiceClient) {
	h := map[string]string{"Authorization": "Bearer " + token}
	if !protoHeader {
		h["Connect-Protocol-Version"] = ""
	}
	hc := &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: h}}
	return asterismv1connect.NewDockerServiceClient(hc, srv.URL),
		asterismv1connect.NewActionsServiceClient(hc, srv.URL),
		asterismv1connect.NewInventoryServiceClient(hc, srv.URL)
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return 0
}

func TestDockerAndActionsEndToEnd(t *testing.T) {
	ctx := context.Background()
	srv, toks := newTestServer(t, true)

	dockerC, actionsC, invC := client(srv, toks["reader"], true)
	list, err := dockerC.ListContainers(ctx, connect.NewRequest(&asterismv1.ListContainersRequest{}))
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if list.Msg.Counts.Exited == 0 || list.Msg.Counts.Unhealthy == 0 || list.Msg.Counts.Restarting == 0 {
		t.Fatalf("showcase should include exited, unhealthy, and restarting containers: %+v", list.Msg.Counts)
	}
	if list.Msg.Capabilities.CanPerformActions || !list.Msg.Capabilities.ActionsEnabled {
		t.Fatalf("reader capabilities: %+v", list.Msg.Capabilities)
	}
	for _, c := range list.Msg.Containers {
		for _, k := range c.EnvKeys {
			if strings.Contains(k, "=") {
				t.Fatalf("env value leaked in %s: %s", c.Name, k)
			}
		}
		if strings.Contains(strings.Join(c.Command, " "), "demo-pass") || strings.Contains(strings.Join(c.Command, " "), "SMTPpass") {
			t.Fatalf("command secret leaked in %s: %v", c.Name, c.Command)
		}
		if _, ok := c.Labels["traefik.http.middlewares.admin.basicauth.users"]; ok {
			t.Fatalf("secret label leaked in %s", c.Name)
		}
	}
	for _, rpc := range []func() error{
		func() error {
			_, e := dockerC.ListImages(ctx, connect.NewRequest(&asterismv1.ListImagesRequest{}))
			return e
		},
		func() error {
			_, e := dockerC.ListVolumes(ctx, connect.NewRequest(&asterismv1.ListVolumesRequest{}))
			return e
		},
		func() error {
			_, e := dockerC.ListNetworks(ctx, connect.NewRequest(&asterismv1.ListNetworksRequest{}))
			return e
		},
		func() error {
			_, e := dockerC.GetDiskUsage(ctx, connect.NewRequest(&asterismv1.GetDiskUsageRequest{}))
			return e
		},
	} {
		if err := rpc(); err != nil {
			t.Fatalf("read rpc: %v", err)
		}
	}

	// Read-only token: refused with PermissionDenied.
	_, err = actionsC.RestartContainer(ctx, connect.NewRequest(&asterismv1.RestartContainerRequest{Id: "ctr:nova-api"}))
	if code(err) != connect.CodePermissionDenied {
		t.Fatalf("reader restart: %v", err)
	}

	// Operator without the Connect-Protocol-Version header: refused before the service runs.
	_, opNoHeader, _ := client(srv, toks["operator"], false)
	_, err = opNoHeader.StopContainer(ctx, connect.NewRequest(&asterismv1.StopContainerRequest{Id: "ctr:nova-api"}))
	if code(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing protocol header should be rejected: %v", err)
	}

	_, opActions, _ := client(srv, toks["operator"], true)
	timeout := uint32(1)
	stop, err := opActions.StopContainer(ctx, connect.NewRequest(&asterismv1.StopContainerRequest{Id: "ctr:nova-api", TimeoutSeconds: &timeout}))
	if err != nil {
		t.Fatalf("operator stop: %v", err)
	}
	if stop.Msg.Container.State != asterismv1.ContainerState_CONTAINER_STATE_EXITED || stop.Msg.AuditEvent.Audit.Actor != "token:operator" {
		t.Fatalf("stop response: %+v", stop.Msg)
	}
	start, err := opActions.StartContainer(ctx, connect.NewRequest(&asterismv1.StartContainerRequest{Id: "nova-api"}))
	if err != nil || start.Msg.Container.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
		t.Fatalf("operator start: %v", err)
	}
	if _, err := opActions.RestartContainer(ctx, connect.NewRequest(&asterismv1.RestartContainerRequest{Id: "ctr:asterism"})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("self-protection: %v", err)
	}
	if _, err := opActions.PauseContainer(ctx, connect.NewRequest(&asterismv1.PauseContainerRequest{Id: "ctr:backup-nightly"})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("pause exited: %v", err)
	}
	if _, err := opActions.StartContainer(ctx, connect.NewRequest(&asterismv1.StartContainerRequest{Id: "x; reboot"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("injection: %v", err)
	}

	got, err := dockerC.GetContainer(ctx, connect.NewRequest(&asterismv1.GetContainerRequest{Id: "ctr:nova-api"}))
	if err != nil {
		t.Fatal(err)
	}
	var audits int
	for _, e := range got.Msg.RecentEvents {
		if e.Audit != nil {
			audits++
		}
	}
	if audits < 3 { // reader denial + stop + start
		t.Fatalf("expected audit events on the container, got %d: %+v", audits, got.Msg.RecentEvents)
	}

	ov, err := invC.GetOverview(ctx, connect.NewRequest(&asterismv1.GetOverviewRequest{}))
	if err != nil || ov.Msg.Capabilities == nil || !ov.Msg.Capabilities.DockerAvailable {
		t.Fatalf("overview capabilities: %v %+v", err, ov)
	}
}

func TestActionsDisabled(t *testing.T) {
	srv, toks := newTestServer(t, false)
	_, actionsC, invC := client(srv, toks["operator"], true)
	_, err := actionsC.RestartContainer(context.Background(), connect.NewRequest(&asterismv1.RestartContainerRequest{Id: "ctr:nova-api"}))
	if code(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "allow_actions") {
		t.Fatalf("disabled: %v", err)
	}
	ov, _ := invC.GetOverview(context.Background(), connect.NewRequest(&asterismv1.GetOverviewRequest{}))
	if ov.Msg.Capabilities.ActionsEnabled || ov.Msg.Capabilities.CanPerformActions {
		t.Fatalf("capabilities: %+v", ov.Msg.Capabilities)
	}
}
