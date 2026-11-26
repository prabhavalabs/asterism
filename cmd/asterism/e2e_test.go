package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/prabhavalabs/asterism/internal/agent/actions"
	agentconfig "github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/sched"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	"github.com/prabhavalabs/asterism/internal/agent/uplink"
	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/config"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
)

type logWriter struct{ t *testing.T }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// fakeSource is an agent collector: one container plus container and host
// series every tick.
type fakeSource struct {
	mu       sync.Mutex
	restarts uint32
}

func (s *fakeSource) Name() string { return "fake" }
func (s *fakeSource) Init(context.Context, state.Env) ([]string, error) {
	return []string{"docker", "cgroup_v2"}, nil
}
func (s *fakeSource) Interval() time.Duration { return 20 * time.Millisecond }
func (s *fakeSource) container(name, id string, restarts uint32) *v1.Container {
	return &v1.Container{
		Id: "ctr:" + name, ContainerId: id, Name: name, Image: "ghcr.io/acme/shop-api:1.4", ImageRepo: "ghcr.io/acme/shop-api",
		State: v1.ContainerState_CONTAINER_STATE_RUNNING, StartedAtMs: time.Now().Add(-time.Hour).UnixMilli(),
		ComposeProject: "shop", ComposeService: "api", RestartCount: restarts,
		Ports: []*v1.ContainerPort{{HostIp: "127.0.0.1", HostPort: 8790, ContainerPort: 8080, Proto: "tcp", Published: true}},
	}
}

func (s *fakeSource) Tick(now time.Time, sink state.Sink) error {
	s.mu.Lock()
	restarts := s.restarts
	s.mu.Unlock()
	sink.UpsertContainer(s.container("shop-api-1", "3f2a9c1b7d4e", restarts))
	db := s.container("shop-db-1", "9b8c7d6e5f4a", 0)
	db.Image, db.ComposeService, db.Ports = "postgres:17", "db", nil
	sink.UpsertContainer(db)
	sink.SetHostInfo(&v1.HostInfo{Hostname: "prod-1", Os: "Ubuntu 24.04", Cores: 2, MemTotalBytes: 4 << 30})
	sink.Set(sink.Series("host", "cpu.util"), 0.25)
	sink.Set(sink.Series("host", "mem.used_bytes"), float32(1<<30))
	sink.Set(sink.Series("ctr:shop-api-1", "cpu.util"), 0.5)
	sink.Set(sink.Series("ctr:shop-api-1", "mem.used_bytes"), float32(256<<20))
	return nil
}

// fakeExec is the agent's Docker executor.
type fakeExec struct {
	src   *fakeSource
	calls atomic.Int32
}

func (e *fakeExec) ContainerAction(_ context.Context, id string, a v1.ContainerAction, _ time.Duration) (*v1.Container, error) {
	e.calls.Add(1)
	e.src.mu.Lock()
	e.src.restarts++
	n := e.src.restarts
	e.src.mu.Unlock()
	return e.src.container("shop-api-1", id, n), nil
}

func bearerClient(token string) *http.Client {
	return &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// eventually polls cond on a ticker (no sleeping) until it holds or 10 s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(10 * time.Second)
	for !cond() {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestAgentModeEndToEnd(t *testing.T) {
	for _, transport := range []string{"bidi", "unary"} {
		t.Run(transport, func(t *testing.T) { runEndToEnd(t, transport) })
	}
}

func runEndToEnd(t *testing.T, transport string) {
	logger := slog.New(slog.NewTextHandler(logWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	agentTok, agentDigest, _ := auth.GenerateAgentToken()
	opTok, opDigest, _ := auth.GenerateToken()

	// --- hub ---
	cfg := config.Default()
	cfg.Server.DataDir = t.TempDir()
	cfg.Collect.Mode = "agent"
	cfg.Collect.Agent.StaleAfter = 30 * time.Second
	cfg.Auth.AdminEmail, cfg.Auth.AdminPassword = "admin@example.com", "pw"
	cfg.Auth.Tokens = []config.APITokenConfig{{Name: "operator", SHA256: opDigest, Scopes: []string{"read", "actions"}}}
	cfg.Agents = []config.AgentConfig{{Name: "prod-1", SHA256: agentDigest, AllowActions: true}}
	cfg.Actions.AllowActions = true
	mgr, err := newAuthManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := newHub(cfg, logger, mgr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.run(ctx)
	srv := httptest.NewServer(h2c.NewHandler(h.handler, &http2.Server{}))
	defer srv.Close()

	// --- agent (chunk A core with a fake collector and executor) ---
	acfg := agentconfig.Default()
	acfg.Hub.URL = srv.URL
	acfg.Hub.Transport = transport
	acfg.Hub.Backoff.Initial = agentconfig.Duration(5 * time.Millisecond)
	acfg.Hub.Backoff.Max = agentconfig.Duration(20 * time.Millisecond)
	acfg.Intervals.Flush = agentconfig.Duration(20 * time.Millisecond)
	acfg.Intervals.Min = agentconfig.Duration(10 * time.Millisecond)
	acfg.Actions = agentconfig.Actions{Enabled: true, Allow: []string{"restart"}, Containers: []string{"shop-api-*"}}
	gate := actions.NewGate(acfg.Actions, actions.Self{})
	store := state.NewStore(state.Options{AnnotateContainer: gate.Annotate})
	src := &fakeSource{}
	sch := sched.New(store, sched.Options{Intervals: acfg.Intervals, Logger: logger})
	sch.Add(src)
	caps := append(sch.Init(ctx), gate.Capabilities()...)
	exec := &fakeExec{src: src}
	var liveHint atomic.Bool
	up, err := uplink.New(uplink.Options{
		Config: acfg, Token: agentTok, Store: store, Version: "e2e",
		Hello: func() *v1.AgentHello {
			return &v1.AgentHello{HostName: "prod-1", Capabilities: caps, AllowedActions: gate.Allowed(), Host: store.HostInfo()}
		},
		Commands: actions.NewService(gate, exec, store.FindContainer, logger),
		OnHints: func(hn state.Hints) {
			liveHint.Store(hn.LiveViewers)
			sch.SetHints(hn)
		},
		Logger:         logger,
		HeartbeatEvery: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	wg.Go(func() { _ = sch.Run(ctx) })
	wg.Go(func() { _ = up.Run(ctx) })

	// --- hub reads ---
	op := bearerClient(opTok)
	docker := asterismv1connect.NewDockerServiceClient(op, srv.URL)
	admin := asterismv1connect.NewAgentAdminServiceClient(op, srv.URL)
	inventory := asterismv1connect.NewInventoryServiceClient(op, srv.URL)
	live := asterismv1connect.NewLiveServiceClient(op, srv.URL)
	acts := asterismv1connect.NewActionsServiceClient(op, srv.URL)

	var containers *v1.ListContainersResponse
	eventually(t, "containers with live stats", func() bool {
		resp, err := docker.ListContainers(ctx, connect.NewRequest(&v1.ListContainersRequest{}))
		if err != nil {
			t.Fatalf("ListContainers: %v", err)
		}
		containers = resp.Msg
		for _, c := range containers.GetContainers() {
			if c.GetName() == "shop-api-1" && c.GetStats().GetCpuUtil() == 0.5 {
				return true
			}
		}
		return false
	})
	if c := containers.GetCapabilities(); c.GetSourceMode() != "agent" || !c.GetCanPerformActions() ||
		len(c.GetContainerActions()) != 1 || c.GetContainerActions()[0] != v1.ContainerAction_CONTAINER_ACTION_RESTART {
		t.Fatalf("capabilities %v", c)
	}
	for _, c := range containers.GetContainers() {
		if c.GetName() == "shop-db-1" && (!c.GetProtected() || c.GetProtectedReason() == "") {
			t.Fatalf("container outside the agent allow-list must be protected: %v", c)
		}
	}
	proj, err := inventory.GetProject(ctx, connect.NewRequest(&v1.GetProjectRequest{Id: "proj:shop"}))
	if err != nil || len(proj.Msg.GetProject().GetWorkloads()) != 2 {
		t.Fatalf("project %v %v", proj, err)
	}
	ov, err := inventory.GetOverview(ctx, connect.NewRequest(&v1.GetOverviewRequest{}))
	if err != nil || ov.Msg.GetHost().GetSourceMode() != "agent" || ov.Msg.GetHost().GetHostname() != "prod-1" {
		t.Fatalf("overview host %v %v", ov.Msg.GetHost(), err)
	}

	agents, err := admin.ListAgents(ctx, connect.NewRequest(&v1.ListAgentsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	a := agents.Msg.GetAgents()[0]
	if agents.Msg.GetPrimaryHostId() != "prod-1" || a.GetState() != "connected" || a.GetTransport() != transport ||
		a.GetInventoryVersion() == 0 || a.GetAgentVersion() != "e2e" || !a.GetActionsEnabled() || !strings.Contains(strings.Join(a.GetCapabilities(), ","), "docker") {
		t.Fatalf("agent status %v", a)
	}
	eventually(t, "a heartbeat", func() bool {
		r, _ := admin.ListAgents(ctx, connect.NewRequest(&v1.ListAgentsRequest{}))
		return r.Msg.GetAgents()[0].GetStats().GetFramesSent() > 0
	})

	// LiveService carries the agent's series under the SSH bridge's names, and
	// an open subscription turns into a live-viewer hint for the agent.
	sctx, scancel := context.WithCancel(ctx)
	stream, err := live.Subscribe(sctx, connect.NewRequest(&v1.SubscribeRequest{IntervalMs: 1000}))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[uint32]string{}
	gotLive := false
	for !gotLive && stream.Receive() {
		m := stream.Msg()
		for _, e := range m.GetSeriesDict().GetEntries() {
			ids[e.GetSeriesId()] = e.GetEntityId() + " " + e.GetMetric()
		}
		for i, id := range m.GetMetricBatch().GetSeriesIds() {
			if ids[id] == "ctr:shop-api-1 cpu.util" && m.GetMetricBatch().GetValues()[i] == 0.5 {
				gotLive = true
			}
		}
	}
	if !gotLive {
		t.Fatalf("live stream ended without the container series: %v", stream.Err())
	}
	eventually(t, "live-viewer hint at the agent", liveHint.Load)
	scancel()
	_ = stream.Close()
	eventually(t, "idle hint at the agent", func() bool { return !liveHint.Load() })

	// Restart round trip with both gates open: hub audit attributes link to
	// the agent's audit line.
	res, err := acts.RestartContainer(ctx, connect.NewRequest(&v1.RestartContainerRequest{Id: "ctr:shop-api-1"}))
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	ev := res.Msg.GetAuditEvent()
	if exec.calls.Load() != 1 || res.Msg.GetContainer().GetRestartCount() != 1 || ev.GetAttrs()["executed_by"] != "agent:prod-1" ||
		ev.GetAttrs()["agent_audit_id"] == "" || ev.GetAudit().GetResult() != "success" || ev.GetAudit().GetActor() != "token:operator" {
		t.Fatalf("restart result %v", res.Msg)
	}
	// The agent's allow-list refuses a container outside actions.containers;
	// the hub refuses it first because the container arrives protected.
	_, err = acts.RestartContainer(ctx, connect.NewRequest(&v1.RestartContainerRequest{Id: "ctr:shop-db-1"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || exec.calls.Load() != 1 {
		t.Fatalf("protected restart: %v (calls %d)", err, exec.calls.Load())
	}
	// An action the agent does not allow never leaves the hub.
	_, err = acts.StopContainer(ctx, connect.NewRequest(&v1.StopContainerRequest{Id: "ctr:shop-api-1"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || exec.calls.Load() != 1 {
		t.Fatalf("stop: %v", err)
	}
	eventually(t, "the restart count in hub inventory", func() bool {
		r, _ := docker.GetContainer(ctx, connect.NewRequest(&v1.GetContainerRequest{Id: "shop-api-1"}))
		return r != nil && r.Msg.GetContainer().GetRestartCount() == 1
	})

	// Scope isolation over HTTP: the agent token reads nothing, and a token
	// with every API scope cannot ingest.
	agentAsReader := asterismv1connect.NewInventoryServiceClient(bearerClient(agentTok), srv.URL)
	if _, err := agentAsReader.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("agent token on InventoryService: %v", err)
	}
	opAsAgent := asterismv1connect.NewAgentServiceClient(op, srv.URL)
	_, err = opAsAgent.Push(ctx, connect.NewRequest(&v1.PushRequest{SessionId: "x", Frames: []*v1.AgentFrame{{Payload: &v1.AgentFrame_Hello{Hello: &v1.AgentHello{ProtocolVersion: 1, SessionId: "x"}}}}}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator token on AgentService: %v", err)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || !strings.Contains(ce.Message(), "agent") {
		t.Fatalf("refusal should explain itself: %v", err)
	}
}
