//go:build integration

// Package agentit is the asterism-agent integration test (make e2e-agent):
// a hub container in agent mode, a hardened agent container reaching
// Docker through a read-only socket proxy, and seed workloads (nginx,
// postgres, redis, a crash-looping busybox), all started with Docker
// Compose. Every assertion polls hub RPCs against a deadline; nothing
// sleeps.
//
//	go test -tags integration -v -timeout 15m ./scripts/agent-it
package agentit

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/prabhavalabs/asterism/internal/auth"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
)

const (
	project   = "asterism-it"
	hostName  = "it-host"
	hubPort   = "17710"
	nginxPort = 18781
	secret    = "it-s3cr3t-value"

	nginx     = "asterism-it-nginx-1"
	postgres  = "asterism-it-postgres-1"
	redis     = "asterism-it-redis-1"
	crashloop = "asterism-it-crashloop-1"
)

type rig struct {
	t           *testing.T
	dir         string // hub.toml, agent.yaml, agent.token
	composeFile string // compose.yaml
	url         string

	agentToken, agentDigest string
	apiToken, apiDigest     string

	agents  asterismv1connect.AgentAdminServiceClient
	docker  asterismv1connect.DockerServiceClient
	inv     asterismv1connect.InventoryServiceClient
	events  asterismv1connect.EventsServiceClient
	metrics asterismv1connect.MetricsServiceClient
	logs    asterismv1connect.LogsServiceClient
	actions asterismv1connect.ActionsServiceClient
}

func TestAgentIntegration(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("docker is required for the agent integration test")
	}
	r := newRig(t)
	ctx := context.Background()

	// Read-only phase: hub actions off, per-agent gate off, agent YAML off.
	r.writeHubConfig(false)
	r.writeAgentConfig(false)
	r.up()

	// --- the agent connects; systemd is gracefully absent ---------------
	var st *v1.AgentStatus
	r.eventually("agent connected", 3*time.Minute, func() (bool, string) {
		st = r.agentStatus(ctx)
		return st.GetState() == "connected" && st.GetInventoryVersion() > 0, fmt.Sprintf("%v", st)
	})
	caps := st.GetCapabilities()
	if !slices.Contains(caps, "docker") || !slices.Contains(caps, "cgroup_v2") || st.GetTransport() != "bidi" || st.GetAgentVersion() != "it" {
		t.Fatalf("agent status %v", st)
	}
	if dockerDesktop() && slices.Contains(caps, "systemd") {
		t.Fatalf("Docker Desktop's VM has no systemd; the agent must report the source as missing: %v", caps)
	}
	if st.GetActionsEnabled() {
		t.Fatal("per-agent actions gate is off but reported on")
	}

	// --- seeds with health, ports, stats and the restart-loop insight ----
	r.eventually("seed containers healthy with live stats", 3*time.Minute, func() (bool, string) {
		cs := r.containers(ctx)
		n, p, rd, cl := cs[nginx], cs[postgres], cs[redis], cs[crashloop]
		switch {
		case n == nil || p == nil || rd == nil || cl == nil:
			return false, fmt.Sprintf("seen %d containers", len(cs))
		case n.GetHealth() != v1.ContainerHealth_CONTAINER_HEALTH_HEALTHY || p.GetHealth() != v1.ContainerHealth_CONTAINER_HEALTH_HEALTHY ||
			rd.GetHealth() != v1.ContainerHealth_CONTAINER_HEALTH_HEALTHY:
			return false, fmt.Sprintf("health nginx=%v postgres=%v redis=%v", n.GetHealth(), p.GetHealth(), rd.GetHealth())
		case !hasPort(n, nginxPort):
			return false, fmt.Sprintf("nginx ports %v", n.GetPorts())
		case n.GetStats().GetMemUsedBytes() == 0 || n.GetStats().GetPids() == 0:
			return false, fmt.Sprintf("nginx stats %v", n.GetStats())
		case !hasInsight(cl, "restart_loop"):
			return false, fmt.Sprintf("crashloop restarts=%d state=%v insights=%v", cl.GetRestartCount(), cl.GetState(), cl.GetInsights())
		}
		return true, ""
	})
	// Secrets never leave the host: env values, shell-string flags.
	resp, err := r.docker.ListContainers(ctx, connect.NewRequest(&v1.ListContainersRequest{Query: "asterism-it"}))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := protojson.Marshal(resp.Msg); bytes.Contains(b, []byte(secret)) {
		t.Fatalf("a seed secret reached the hub API: %s", b)
	}
	if cs := r.containers(ctx); !slices.Contains(cs[postgres].GetEnvKeys(), "POSTGRES_PASSWORD") ||
		!strings.Contains(strings.Join(cs[redis].GetCommand(), " "), "--requirepass [REDACTED]") {
		t.Fatalf("redaction: env keys %v, command %q", cs[postgres].GetEnvKeys(), cs[redis].GetCommand())
	}

	r.eventually("published nginx port in the ports listing", time.Minute, func() (bool, string) {
		ports, err := r.inv.ListPorts(ctx, connect.NewRequest(&v1.ListPortsRequest{}))
		if err != nil {
			return false, err.Error()
		}
		for _, p := range ports.Msg.GetPorts() {
			if p.GetHostPort() == nginxPort && p.GetWorkloadId() == "ctr:"+nginx {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d ports", len(ports.Msg.GetPorts()))
	})
	r.eventually("lifecycle events for the crash loop", time.Minute, func() (bool, string) {
		ev, err := r.events.ListEvents(ctx, connect.NewRequest(&v1.ListEventsRequest{EntityId: "ctr:" + crashloop, KindGroup: "lifecycle"}))
		if err != nil {
			return false, err.Error()
		}
		return len(ev.Msg.GetEvents()) > 0, fmt.Sprintf("%d events", len(ev.Msg.GetEvents()))
	})
	r.eventually("container logs over the agent session", time.Minute, func() (bool, string) {
		lg, err := r.logs.TailLogs(ctx, connect.NewRequest(&v1.TailLogsRequest{WorkloadId: "ctr:" + crashloop, Limit: 20}))
		if err != nil {
			return false, err.Error()
		}
		for _, l := range lg.Msg.GetLines() {
			if strings.Contains(l.GetText(), "crashloop starting") {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d lines", len(lg.Msg.GetLines()))
	})

	// --- actions are refused by default ----------------------------------
	_, err = r.actions.RestartContainer(ctx, connect.NewRequest(&v1.RestartContainerRequest{Id: "ctr:" + nginx}))
	if code := connect.CodeOf(err); code != connect.CodeFailedPrecondition {
		t.Fatalf("restart with every gate closed: %v (code %v), want FailedPrecondition", err, code)
	}

	// --- hub outage: mutate, restart, reconnect, resync, replay -----------
	r.compose("stop", "hub")
	stoppedAt := time.Now()
	r.docker1("stop", "-t", "2", redis)
	// Keep the hub down until the agent has failed 12 reconnects (full
	// jitter, capped at 2 s: roughly 10-15 s), so the buffered rows span
	// at least one idle host interval (2 s x idle_factor 3).
	r.eventually("agent retries while the hub is down", 2*time.Minute, func() (bool, string) {
		logs := r.composeOut("logs", "--since", stoppedAt.UTC().Format(time.RFC3339Nano), "agent")
		n := strings.Count(logs, "hub connection failed")
		return n >= 12, fmt.Sprintf("%d failed attempts", n)
	})
	r.compose("start", "hub")
	restartedAt := time.Now()
	r.eventually("agent reconnected", 2*time.Minute, func() (bool, string) {
		st = r.agentStatus(ctx)
		return st.GetState() == "connected" && st.GetConnectedSinceMs() >= stoppedAt.UnixMilli(), fmt.Sprintf("%v", st)
	})
	r.eventually("inventory resynced with the stopped redis", time.Minute, func() (bool, string) {
		c := r.containers(ctx)[redis]
		return c != nil && c.GetState() == v1.ContainerState_CONTAINER_STATE_EXITED, fmt.Sprintf("%v", c.GetState())
	})
	r.eventually("buffered metrics from the outage replayed", time.Minute, func() (bool, string) {
		q, err := r.metrics.QueryRange(ctx, connect.NewRequest(&v1.QueryRangeRequest{
			EntityId: "host", Metrics: []string{"cpu.util", "mem.used_bytes"},
			FromMs: stoppedAt.UnixMilli(), ToMs: restartedAt.UnixMilli(),
		}))
		if err != nil {
			return false, err.Error()
		}
		n := 0
		for _, s := range q.Msg.GetSeries() {
			n += len(s.GetPoints())
		}
		return n > 0, fmt.Sprintf("%d points inside the outage window", n)
	})

	// --- open both gates: hub + per-agent + agent YAML allow-list ---------
	r.writeHubConfig(true)
	r.writeAgentConfig(true)
	r.compose("restart", "hub", "agent")
	r.eventually("restart offered for nginx", 2*time.Minute, func() (bool, string) {
		lc, err := r.docker.ListContainers(ctx, connect.NewRequest(&v1.ListContainersRequest{Query: "asterism-it"}))
		if err != nil {
			return false, err.Error()
		}
		capa := lc.Msg.GetCapabilities()
		var n *v1.Container
		for _, c := range lc.Msg.GetContainers() {
			if c.GetName() == nginx {
				n = c
			}
		}
		ok := capa.GetCanPerformActions() && slices.Contains(capa.GetContainerActions(), v1.ContainerAction_CONTAINER_ACTION_RESTART) &&
			n != nil && !n.GetProtected()
		return ok, fmt.Sprintf("caps %v nginx protected=%v (%s)", capa, n.GetProtected(), n.GetProtectedReason())
	})
	before := r.containers(ctx)[nginx].GetStartedAtMs()
	res, err := r.actions.RestartContainer(ctx, connect.NewRequest(&v1.RestartContainerRequest{Id: "ctr:" + nginx}))
	if err != nil {
		t.Fatalf("restart with both gates open: %v", err)
	}
	ev := res.Msg.GetAuditEvent()
	if ev.GetAudit().GetResult() != "success" || ev.GetAttrs()["executed_by"] != "agent:"+hostName || ev.GetAttrs()["agent_audit_id"] == "" {
		t.Fatalf("audit %v", ev)
	}
	r.eventually("nginx restarted", time.Minute, func() (bool, string) {
		c := r.containers(ctx)[nginx]
		return c.GetStartedAtMs() > before && c.GetState() == v1.ContainerState_CONTAINER_STATE_RUNNING, fmt.Sprintf("%v %d", c.GetState(), c.GetStartedAtMs())
	})
	r.eventually("restart audited on the events page", time.Minute, func() (bool, string) {
		el, err := r.events.ListEvents(ctx, connect.NewRequest(&v1.ListEventsRequest{KindGroup: "audit"}))
		if err != nil {
			return false, err.Error()
		}
		for _, e := range el.Msg.GetEvents() {
			if e.GetKind() == "audit.container.restart" && e.GetEntityId() == "ctr:"+nginx && e.GetAudit().GetResult() == "success" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d audit events", len(el.Msg.GetEvents()))
	})
	// Outside the agent's allow-list: refused before it leaves the hub.
	_, err = r.actions.RestartContainer(ctx, connect.NewRequest(&v1.RestartContainerRequest{Id: "ctr:" + postgres}))
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Fatalf("restart outside the agent allow-list: %v (code %v), want PermissionDenied", err, code)
	}
}

// ---------------------------------------------------------------------------
// rig
// ---------------------------------------------------------------------------

func newRig(t *testing.T) *rig {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, dir: t.TempDir(), composeFile: filepath.Join(wd, "compose.yaml"), url: "http://127.0.0.1:" + hubPort}
	if r.agentToken, r.agentDigest, err = auth.GenerateAgentToken(); err != nil {
		t.Fatal(err)
	}
	if r.apiToken, r.apiDigest, err = auth.GenerateToken(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "agent.token"), []byte(r.agentToken+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Timeout: 20 * time.Second, Transport: bearer{r.apiToken}}
	r.agents = asterismv1connect.NewAgentAdminServiceClient(hc, r.url)
	r.docker = asterismv1connect.NewDockerServiceClient(hc, r.url)
	r.inv = asterismv1connect.NewInventoryServiceClient(hc, r.url)
	r.events = asterismv1connect.NewEventsServiceClient(hc, r.url)
	r.metrics = asterismv1connect.NewMetricsServiceClient(hc, r.url)
	r.logs = asterismv1connect.NewLogsServiceClient(hc, r.url)
	r.actions = asterismv1connect.NewActionsServiceClient(hc, r.url)
	t.Cleanup(r.down)
	return r
}

type bearer struct{ token string }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}

func (r *rig) write(name, body string) {
	r.t.Helper()
	// Rewrite in place (same inode) so single-file bind mounts see it.
	f, err := os.OpenFile(filepath.Join(r.dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		r.t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) writeHubConfig(actions bool) {
	r.write("hub.toml", fmt.Sprintf(`[server]
listen = "0.0.0.0:7700"

[collect]
mode = "agent"

[collect.agent]
primary_host = %[1]q
stale_after = "10s"

[auth]
admin_email = "it@example.com"
admin_password = "it-admin-password"

[[auth.tokens]]
name = "it"
sha256 = %[2]q
scopes = ["read", "actions"]

[actions]
allow_actions = %[4]t

[[agents]]
name = %[1]q
sha256 = %[3]q
allow_actions = %[4]t
`, hostName, r.apiDigest, r.agentDigest, actions))
}

func (r *rig) writeAgentConfig(actions bool) {
	act := "actions: {enabled: false}\n"
	if actions {
		act = "actions:\n  enabled: true\n  allow: [restart]\n  containers: [\"asterism-it-nginx-*\"]\n  protect: [\"*postgres*\"]\n"
	}
	r.write("agent.yaml", `version: 1
hub:
  url: http://hub:7700
  tls: {insecure_dev: true}
  backoff: {initial: 500ms, max: 2s}
host:
  name: `+hostName+`
intervals:
  flush: 2s
  docker_resources: 30s
agent:
  log_level: info
`+act)
}

func (r *rig) env() []string {
	return append(os.Environ(), "ASTERISM_IT_DIR="+r.dir, "ASTERISM_IT_HUB_PORT="+hubPort, fmt.Sprintf("ASTERISM_IT_NGINX_PORT=%d", nginxPort))
}

func (r *rig) composeCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", r.composeFile}, args...)...)
	cmd.Env = r.env()
	return cmd
}

func (r *rig) compose(args ...string) {
	r.t.Helper()
	cmd := r.composeCmd(args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (r *rig) composeOut(args ...string) string {
	out, _ := r.composeCmd(args...).CombinedOutput()
	return string(out)
}

func (r *rig) docker1(args ...string) {
	r.t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		r.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (r *rig) up() {
	r.t.Helper()
	cmd := r.composeCmd("up", "-d", "--build", "--quiet-pull")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("docker compose up: %v", err)
	}
}

// down removes every container, network and volume of the rig, then any
// leftover resource labelled asterism.test=1 in this compose project.
func (r *rig) down() {
	if r.t.Failed() {
		r.t.Logf("hub logs:\n%s", r.composeOut("logs", "--tail", "80", "hub"))
		r.t.Logf("agent logs:\n%s", r.composeOut("logs", "--tail", "80", "agent"))
	}
	if os.Getenv("ASTERISM_IT_KEEP") != "" {
		r.t.Logf("ASTERISM_IT_KEEP set: leaving the rig running (docker compose -p %s down -v)", project)
		return
	}
	_ = r.composeCmd("down", "-v", "--remove-orphans", "--timeout", "5").Run()
	filter := []string{"--filter", "label=asterism.test=1", "--filter", "label=com.docker.compose.project=" + project}
	if ids, _ := exec.Command("docker", append([]string{"ps", "-aq"}, filter...)...).Output(); len(bytes.TrimSpace(ids)) > 0 {
		_ = exec.Command("docker", append([]string{"rm", "-f"}, strings.Fields(string(ids))...)...).Run()
	}
	if ids, _ := exec.Command("docker", append([]string{"network", "ls", "-q"}, filter...)...).Output(); len(bytes.TrimSpace(ids)) > 0 {
		_ = exec.Command("docker", append([]string{"network", "rm"}, strings.Fields(string(ids))...)...).Run()
	}
}

// eventually polls cond every 500 ms on a ticker until it holds or the
// deadline passes; no sleeping.
func (r *rig) eventually(what string, timeout time.Duration, cond func() (bool, string)) {
	r.t.Helper()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	start := time.Now()
	for {
		ok, detail := cond()
		if ok {
			r.t.Logf("ok after %s: %s", time.Since(start).Round(100*time.Millisecond), what)
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			r.t.Fatalf("timed out after %s waiting for %s; last: %s", timeout, what, detail)
		}
	}
}

func (r *rig) agentStatus(ctx context.Context) *v1.AgentStatus {
	resp, err := r.agents.ListAgents(ctx, connect.NewRequest(&v1.ListAgentsRequest{}))
	if err != nil {
		return nil
	}
	for _, a := range resp.Msg.GetAgents() {
		if a.GetName() == hostName {
			return a
		}
	}
	return nil
}

func (r *rig) containers(ctx context.Context) map[string]*v1.Container {
	resp, err := r.docker.ListContainers(ctx, connect.NewRequest(&v1.ListContainersRequest{Query: "asterism-it"}))
	if err != nil {
		return map[string]*v1.Container{}
	}
	out := map[string]*v1.Container{}
	for _, c := range resp.Msg.GetContainers() {
		out[c.GetName()] = c
	}
	return out
}

func hasPort(c *v1.Container, port uint32) bool {
	for _, p := range c.GetPorts() {
		if p.GetHostPort() == port {
			return true
		}
	}
	return false
}

func hasInsight(c *v1.Container, code string) bool {
	for _, i := range c.GetInsights() {
		if i.GetCode() == code {
			return true
		}
	}
	return false
}

// dockerDesktop reports whether the engine is Docker Desktop, whose Linux
// VM (the agent's "host" here) runs no systemd.
func dockerDesktop() bool {
	out, _ := exec.Command("docker", "info", "--format", "{{.OperatingSystem}}").Output()
	return strings.Contains(string(out), "Docker Desktop")
}
